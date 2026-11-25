package relay

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"github.com/Lewin671/remote-agent/internal/transport"
	"github.com/Lewin671/remote-agent/internal/transport/direct"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"github.com/Lewin671/remote-agent/shared/linkcore"
	"github.com/Lewin671/remote-agent/shared/peerwire"
	"google.golang.org/protobuf/proto"
)

type auditTestEnv struct {
	c      *conn
	keys   *testSecurityKeys
	nowMs  *atomic.Uint64
	d      *dispatcher
	m      *directManager
	ctx    context.Context
	cancel context.CancelFunc
}

func newAuditTestEnv(t *testing.T, extraClients int) (*auditTestEnv, []ed25519.PrivateKey, []*ecdh.PrivateKey) {
	t.Helper()
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	c := &conn{}
	keys := initializeTestSecurityKeys(t, c)
	nowMs := &atomic.Uint64{}
	nowMs.Store(uint64(time.Now().UnixMilli()))
	c.security.clock = nowMs.Load

	var clientEds []ed25519.PrivateKey
	var clientXs []*ecdh.PrivateKey
	curSecret := keys.secret
	for i := range extraClients {
		_, head, _ := c.security.endpoint.Snapshot()
		_, edPriv, _ := ed25519.GenerateKey(rand.Reader)
		xPriv, _ := ecdh.X25519().GenerateKey(rand.Reader)
		mem, err := devicesec.NewMember(edPriv.Public().(ed25519.PublicKey), xPriv.PublicKey().Bytes(), devicesec.Client)
		if err != nil {
			t.Fatal(err)
		}
		nextSecret := make([]byte, 32)
		nextSecret[0] = byte(10 + i)
		next, err := deviceauth.BuildPairState(head, mem, xPriv.PublicKey().Bytes(), curSecret, nextSecret, keys.phone, nowMs.Load()+uint64(i+1))
		if err != nil {
			t.Fatal(err)
		}
		if err := c.security.endpoint.Install([]devicesec.State{next}, []*devicesec.RecoveryStatement{nil}); err != nil {
			t.Fatal(err)
		}
		lease, err := deviceauth.BuildLease(next, keys.phone, nowMs.Load()+uint64(i+1))
		if err != nil {
			t.Fatal(err)
		}
		if err := c.security.endpoint.InstallLease(lease, nowMs.Load()+uint64(i+1)); err != nil {
			t.Fatal(err)
		}
		curSecret = nextSecret
		clientEds = append(clientEds, edPriv)
		clientXs = append(clientXs, xPriv)
	}

	_, head, _ := c.security.endpoint.Snapshot()
	hash, err := head.Hash()
	if err != nil {
		t.Fatal(err)
	}
	c.security.directFresh.Store(&directFreshness{
		head:       hash,
		until:      nowMs.Load() + 20_000,
		leaseUntil: c.security.endpoint.LeaseExpiration(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	link := transport.NewLink(ctx)
	c.acct.Security = &SecurityConfig{XPrivate: keys.x}
	c.r = &Relay{
		directLoopback: true,
		directClock:    func() time.Time { return time.UnixMilli(int64(nowMs.Load())) },
	}
	ep := &endpoint{r: c.r, acct: c.acct, security: c.security, lifetime: link.Context(), link: link}
	ep.admissionOwner()
	d := &dispatcher{ctx: ctx, cancel: cancel, link: link, endpoint: ep, security: c.security, acct: c.acct}
	m, err := newDirectManager(d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		m.close()
		ep.admission.close()
		link.Close()
		cancel()
	})
	return &auditTestEnv{c: c, keys: keys, nowMs: nowMs, d: d, m: m, ctx: ctx, cancel: cancel}, clientEds, clientXs
}

type fakeRelayPath struct {
	mu     sync.Mutex
	writes [][]byte
	notify chan struct{}
}

func (f *fakeRelayPath) Read(ctx context.Context) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() }
func (f *fakeRelayPath) Write(_ context.Context, b []byte) error {
	f.mu.Lock()
	f.writes = append(f.writes, append([]byte(nil), b...))
	f.mu.Unlock()
	select {
	case f.notify <- struct{}{}:
	default:
	}
	return nil
}
func (f *fakeRelayPath) Close()  {}
func (f *fakeRelayPath) Finish() {}

// Bug 3: Stale readPath defer must not tear down new session's direct terminal leg.
func TestStaleReadPathDoesNotTearDownNewDirectTerminalLeg(t *testing.T) {
	env, _, _ := newAuditTestEnv(t, 0)
	_, head, selfID := env.c.security.endpoint.Snapshot()
	self, _ := head.Core.Member(selfID)
	peerMem, _ := head.Core.Member(head.Core.AuthorityID)
	hash, _ := head.Hash()
	binding := deviceauth.PeerBinding{Self: self, Peer: peerMem, Head: hash}

	pool := direct.NewPool(4)
	path1, err := direct.New(env.ctx, direct.Config{
		Use: 1, Owner: "conn1", Peer: peerMem.ID, Generation: 1, Pool: pool,
		Allow: func() bool { return true }, LoopbackOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	path2, err := direct.New(env.ctx, direct.Config{
		Use: 1, Owner: "conn2", Peer: peerMem.ID + "-2", Generation: 1, Pool: pool,
		Allow: func() bool { return true }, LoopbackOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { path2.Close(); path2.Wait() }()

	p1 := &directPeer{binding: binding, writeWake: make(chan struct{}, 1)}
	p1.paths[0] = path1
	p2 := &directPeer{binding: binding, writeWake: make(chan struct{}, 1)}
	p2.paths[0] = path2

	// Register live terminal with a direct leg attached on p2.
	hub := env.d.endpoint.terminalHubOwner()
	lt := &liveTerminal{
		ownerPeer:      peerMem.ID,
		streamID:       "stream-1",
		sessionID:      "sess-1",
		stream:         linkcore.NewTerminalStream("stream-1", "sess-1", peerMem.ID, "inst", env.nowMs.Load()),
		directGen:      2,
		hadDirectLeg:   true,
		phase3Eligible: true,
	}
	_ = lt.stream.AttachLeg("direct", "d:leg-p2", "inst", 2)
	hub.mu.Lock()
	hub.live["stream-1"] = lt
	hub.mu.Unlock()

	env.m.mu.Lock()
	env.m.active[peerMem.ID] = p2
	env.m.mu.Unlock()

	done := make(chan struct{})
	go func() {
		defer close(done)
		env.m.readPath(p1, 1, path1)
	}()
	path1.Close()
	<-done

	lt.mu.Lock()
	hasDirect := lt.stream.HasLeg("direct")
	gen := lt.directGen
	lt.mu.Unlock()
	if !hasDirect || gen != 2 {
		t.Fatalf("stale p1.readPath tore down p2's active direct terminal leg: hasDirect=%v gen=%d", hasDirect, gen)
	}
}

// Bug 5: Daemon probes to offline clients must not fill all 8 active slots and lock out client #9.
func TestOfflineDaemonProbesDoNotLockOutNinthOnlineClient(t *testing.T) {
	// 1 authority client + 8 extra clients = 9 total clients.
	env, clientEds, clientXs := newAuditTestEnv(t, 8)
	_, head, _ := env.c.security.endpoint.Snapshot()

	var clients []devicesec.Member
	for _, m := range head.Core.Members {
		if m.Kind == devicesec.Client {
			clients = append(clients, m)
		}
	}
	if len(clients) != 9 {
		t.Fatalf("clients = %d, want 9", len(clients))
	}

	// Attach a fake relay path and let maintenance announce (probing first 8 clients).
	fp := &fakeRelayPath{notify: make(chan struct{}, 32)}
	gen, detach := env.m.attach(fp)
	defer detach()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		env.m.mu.Lock()
		n := len(env.m.active)
		env.m.mu.Unlock()
		if n >= 8 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Find a client NOT currently in m.active (the 9th client).
	var ninth devicesec.Member
	var ninthIdx int = -1
	env.m.mu.Lock()
	for _, cm := range clients {
		if env.m.active[cm.ID] == nil {
			ninth = cm
			break
		}
	}
	env.m.mu.Unlock()
	if ninth.ID == "" {
		t.Fatal("expected at least one client outside the first 8 active slots")
	}
	for i, ed := range clientEds {
		if devicesec.ID(ed.Public().(ed25519.PublicKey)) == ninth.ID {
			ninthIdx = i
			break
		}
	}
	if ninthIdx < 0 {
		// Authority client is the 9th; use keys.phone instead.
		_ = clientXs
	}

	// Case A: Even BEFORE OFFLINE delivery results arrive, an inbound Probe from client #9 must be admitted.
	binding, codec, peerPub, ok := env.m.scope(ninth.ID)
	if !ok {
		t.Fatal("scope failed for ninth client")
	}
	peerPub.Instance = bytes.Repeat([]byte{0x11}, 16)
	peerPub.Boot = bytes.Repeat([]byte{0x22}, 32)
	connID := bytes.Repeat([]byte{0x99}, 16)
	env.m.mu.Lock()
	created := env.m.createLocked(binding, codec, peerPub, connID, false, env.nowMs.Load(), env.nowMs.Load()+10_000)
	env.m.mu.Unlock()
	if created == nil {
		t.Fatal("ninth online client was locked out by unestablished daemon-initiated probes")
	}

	// Case B: PeerFrameResult_OFFLINE for a daemon-initiated probe must release its active slot immediately.
	// Wait for at least one Probe frame to be written on fp.
	select {
	case <-fp.notify:
	case <-time.After(2 * time.Second):
		t.Fatal("no probe frames sent")
	}
	fp.mu.Lock()
	writes := append([][]byte(nil), fp.writes...)
	fp.mu.Unlock()
	var offlineTargets int
	for _, raw := range writes {
		var frame pb.PeerFrame
		if proto.Unmarshal(raw, &frame) != nil {
			continue
		}
		dpf := frame.GetDirectedPeerFrame()
		if dpf == nil || len(dpf.FrameId) != 16 {
			continue
		}
		toID := string(dpf.ToDevice)
		if toID == ninth.ID {
			continue
		}
		env.m.delivery(&pb.PeerFrameResult{
			FrameId: dpf.FrameId,
			Status:  pb.PeerFrameResult_OFFLINE,
		}, gen)
		offlineTargets++
	}
	if offlineTargets == 0 {
		t.Fatal("no probe frames for offline clients found")
	}
	env.m.mu.Lock()
	remainingOffline := 0
	for id, p := range env.m.active {
		if id != ninth.ID && p != nil {
			remainingOffline++
		}
	}
	env.m.mu.Unlock()
	if remainingOffline >= 8 {
		t.Fatalf("offline daemon probes still occupy %d active slots after OFFLINE result", remainingOffline)
	}
}

// Bug 6: highQueue must enforce 2 MiB byte cap and popWrite must clear backing array references.
func TestDirectPeerHighQueueByteCapAndPopWriteClearsBackingArray(t *testing.T) {
	p := &directPeer{writeWake: make(chan struct{}, 1)}
	oneMiB := make([]byte, 1<<20)
	dl := time.Now().Add(time.Minute)
	if !p.enqueueHigh(oneMiB, dl) {
		t.Fatal("1st 1MiB item failed")
	}
	if !p.enqueueHigh(oneMiB, dl) {
		t.Fatal("2nd 1MiB item failed")
	}
	if p.enqueueHigh(oneMiB, dl) {
		t.Fatal("3rd 1MiB item accepted: highQueue has no 2 MiB byte cap")
	}
	backing := p.highQueue[:cap(p.highQueue)]
	for range 2 {
		if _, _, ok := p.popWrite(); !ok {
			t.Fatal("popWrite failed")
		}
	}
	if backing[0].raw != nil || backing[1].raw != nil {
		t.Fatal("popWrite did not zero popped slice slots (pins backing array)")
	}
	if p.highQueue != nil {
		t.Fatalf("empty highQueue kept capacity=%d instead of resetting to nil", cap(p.highQueue))
	}
}

// Bug 7: maintenance capability refresh must respect 1s backoff when capabilities is nil or SendControl(14) fails.
func TestCapabilityRefreshRespectsBackoffWhenControl14Fails(t *testing.T) {
	env, _, _ := newAuditTestEnv(t, 0)
	_, head, selfID := env.c.security.endpoint.Snapshot()
	self, _ := head.Core.Member(selfID)
	peerMem, _ := head.Core.Member(head.Core.AuthorityID)
	hash, _ := head.Hash()
	pin, _, _ := env.c.security.endpoint.Snapshot()
	codec, _ := peerwire.NewCodec(pin.Account, pin.Domain, hash, head.Core.Epoch, protocol.PeerVersion)

	var sent atomic.Int32
	sess, err := direct.NewSession(env.ctx, direct.SessionConfig{
		Self:       env.m.self,
		Peer:       peerwire.Public{ID: []byte(peerMem.ID), Role: 1, Sign: peerMem.EdPublic[:], Box: peerMem.XPublic[:]},
		Codec:      codec,
		Connection: bytes.Repeat([]byte{0x33}, 16),
		Deadline:   env.nowMs.Load() + 10_000,
		Pool:       direct.NewPool(4),
		Allow:      func() bool { return true },
		Send: func(direct.Outbound) bool {
			sent.Add(1)
			return true
		},
		Now: func() time.Time { return time.UnixMilli(int64(env.nowMs.Load())) },
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// Ensure capabilities is nil on m, and test the helper directly so we don't need full Pion establishment.
	p := &directPeer{
		binding:          deviceauth.PeerBinding{Self: self, Peer: peerMem, Head: hash},
		session:          sess,
		nextCapabilities: 0,
	}
	// Simulate capabilities == nil: must NOT send Control 1 at all.
	env.m.capabilities.Store(nil)
	var control1Calls, control14Calls int
	now := env.nowMs.Load()
	trySend := func(typ uint64, _ []byte) bool {
		if typ == 1 {
			control1Calls++
			return true
		}
		control14Calls++
		return false // simulate Control 14 queue full
	}
	// Run 5 ticks (every 25ms) with nil capabilities: Control 1 is sent once (for transport caps)
	// and then obeys the 55s refresh interval without flooding every 25ms.
	for i := range 5 {
		env.m.refreshPeerCapabilitiesLocked(p, nil, now+uint64(i*25), trySend)
	}
	if control1Calls != 1 || control14Calls != 0 {
		t.Fatalf("nil capabilities sent control1=%d control14=%d, want 1 and 0", control1Calls, control14Calls)
	}
	// Now provide non-nil capabilities, with Control 14 failing -> must back off 1000ms, NOT flood every 25ms!
	control1Calls = 0
	caps := &pb.MachineCapabilities{FileUpload: true}
	for i := range 5 {
		env.m.refreshPeerCapabilitiesLocked(p, caps, now+uint64(i*25), trySend)
	}
	if control1Calls != 1 || control14Calls != 1 {
		t.Fatalf("Control 14 failure bypassed 1s backoff: control1=%d control14=%d, want 1 and 1", control1Calls, control14Calls)
	}
	// After 1000ms, retry is allowed once more.
	env.m.refreshPeerCapabilitiesLocked(p, caps, now+1000, trySend)
	if control1Calls != 2 || control14Calls != 2 {
		t.Fatalf("after 1s backoff expected retry: control1=%d control14=%d, want 2 and 2", control1Calls, control14Calls)
	}
}

// Bug 9: Old writePath for closed/superseded path1 must not steal items queued for path2.
func TestSupersededWritePathDoesNotStealQueuedFrames(t *testing.T) {
	env, _, _ := newAuditTestEnv(t, 0)
	_, head, selfID := env.c.security.endpoint.Snapshot()
	self, _ := head.Core.Member(selfID)
	peerMem, _ := head.Core.Member(head.Core.AuthorityID)
	hash, _ := head.Hash()
	binding := deviceauth.PeerBinding{Self: self, Peer: peerMem, Head: hash}

	pool := direct.NewPool(4)
	path1, err := direct.New(env.ctx, direct.Config{
		Use: 1, Owner: "c1", Peer: peerMem.ID, Generation: 1, Pool: pool,
		Allow: func() bool { return true }, LoopbackOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	path2, err := direct.New(env.ctx, direct.Config{
		Use: 1, Owner: "c2", Peer: peerMem.ID + "-2", Generation: 2, Pool: pool,
		Allow: func() bool { return true }, LoopbackOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { path2.Close(); path2.Wait() }()

	// Close path1 BEFORE writePath(p, path1) runs, and queue 5 high-priority items.
	path1.Close()
	path1.Wait()

	p := &directPeer{
		binding:   binding,
		writeWake: make(chan struct{}, 1),
	}
	p.paths[0] = path2
	dl := time.UnixMilli(int64(env.nowMs.Load() + 60_000))
	for i := range 5 {
		p.enqueueHigh([]byte{byte(i + 1)}, dl)
	}

	env.m.writePath(p, path1)

	p.writeMu.Lock()
	remaining := len(p.highQueue)
	p.writeMu.Unlock()
	if remaining != 5 {
		t.Fatalf("closed/superseded writePath stole %d queued items (remaining=%d, want 5)", 5-remaining, remaining)
	}
}

// Bug 11: Unvalidated/forged Challenge must not overwrite target.peer.Instance/Boot before Session validates Probe hash.
func TestInvalidChallengeDoesNotPollutePeerRoutingIdentity(t *testing.T) {
	env, _, _ := newAuditTestEnv(t, 0)
	_, head, _ := env.c.security.endpoint.Snapshot()
	peerID := head.Core.AuthorityID
	binding, codec, peerPub, ok := env.m.scope(peerID)
	if !ok {
		t.Fatal("scope failed")
	}

	// Daemon initiates a session (peer Instance/Boot unknown initially).
	connID := bytes.Repeat([]byte{0x44}, 16)
	now := env.nowMs.Load()
	env.m.mu.Lock()
	target := env.m.createLocked(binding, codec, peerPub, connID, true, now, now+10_000)
	env.m.mu.Unlock()
	if target == nil {
		t.Fatal("createLocked failed")
	}

	// Build a signed Challenge from the authority client with a BOGUS probe hash.
	clientPriv := env.keys.phone
	clientX, err := ecdh.X25519().NewPrivateKey(env.keys.phoneX)
	if err != nil {
		t.Fatal(err)
	}
	selfBoxPub, err := ecdh.X25519().NewPublicKey(env.m.self.Box.PublicKey().Bytes())
	if err != nil {
		t.Fatal(err)
	}
	shared, err := clientX.ECDH(selfBoxPub)
	if err != nil {
		t.Fatal(err)
	}
	bogusInst := bytes.Repeat([]byte{0xaa}, 16)
	bogusBoot := bytes.Repeat([]byte{0xbb}, 32)
	fromPub := peerwire.Public{
		ID:       []byte(peerID),
		Role:     1,
		Sign:     clientPriv.Public().(ed25519.PublicKey),
		Box:      clientX.PublicKey().Bytes(),
		Instance: bogusInst,
		Boot:     bogusBoot,
	}
	toPub := env.m.self.Public()
	c := peerwire.NewContext(fromPub, toPub, connID, 0, 0, 1, now, 1, true)
	body := peerwire.PB(
		peerwire.B(1, connID),
		peerwire.B(2, bogusInst),
		peerwire.B(3, bogusBoot),
		peerwire.B(4, toPub.Boot),
		peerwire.B(6, bytes.Repeat([]byte{0xde}, 32)), // wrong probe hash
	)
	entropy := make([]byte, peerwire.SealEntropy)
	_, _ = rand.Read(entropy)
	unsigned, err := codec.Prepare(fromPub, toPub, shared, c, 2, body, entropy)
	if err != nil {
		t.Fatal(err)
	}
	rawChallenge, err := unsigned.Finish(ed25519.Sign(clientPriv, unsigned.Message))
	if err != nil {
		t.Fatal(err)
	}

	env.m.receive(rawChallenge)

	// Wait briefly for readLoop + session to process the invalid Challenge.
	time.Sleep(100 * time.Millisecond)

	env.m.mu.Lock()
	gotPeerBoot := append([]byte(nil), target.peer.Boot...)
	env.m.mu.Unlock()
	if bytes.Equal(gotPeerBoot, bogusBoot) {
		t.Fatal("invalid Challenge with wrong probe hash polluted target.peer.Boot")
	}
}

// Bug 12: Exhausting Control rate limit must not starve Signal frames in writeLoop.
func TestWriteLoopControlRateLimitDoesNotStarveSignalQueue(t *testing.T) {
	env, _, _ := newAuditTestEnv(t, 0)
	fp := &fakeRelayPath{notify: make(chan struct{}, 16)}
	_, detach := env.m.attach(fp)
	defer detach()

	// Exhaust class 1 (Control) token bucket completely while leaving class 0 (Signal) full.
	now := env.m.nowTime()
	env.m.mu.Lock()
	for env.m.reserveRate(1, 512<<10, now) {
	}
	// Enqueue one Control frame into outgoing[0] (class 1) and one Signal frame into outgoing[1] (class 0).
	ctrlData := []byte("control-frame")
	sigData := []byte("signal-frame")
	env.m.queued["p1"] = &directedUsage{frames: [2]int{1, 1}, bytes: [2]int{len(sigData), len(ctrlData)}}
	env.m.queuedBytes = [2]int{len(sigData), len(ctrlData)}
	env.m.outgoing[0].Push("p1", "p1", len(ctrlData), &peerOutput{
		peer:  "p1",
		class: 1,
		data:  ctrlData,
		out:   direct.Outbound{Valid: func() bool { return true }},
		until: now.Add(time.Second),
	})
	env.m.outgoing[1].Push("p1", "p1", len(sigData), &peerOutput{
		peer:  "p1",
		class: 0,
		data:  sigData,
		out:   direct.Outbound{Valid: func() bool { return true }, Critical: true},
		until: now.Add(time.Second),
	})
	env.m.mu.Unlock()
	notify(env.m.writeWake)

	select {
	case <-fp.notify:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("Signal frame was starved by rate-limited Control queue")
	}
	fp.mu.Lock()
	defer fp.mu.Unlock()
	if len(fp.writes) == 0 || !bytes.Equal(fp.writes[0], sigData) {
		t.Fatalf("expected Signal frame to be written immediately, got %q", fp.writes)
	}
}

// Opt 3: reserveAuthentication sweeps periodically (not on every call) and always prunes on cap.
func TestReserveAuthenticationPeriodicSweepAndFullCapacityPrune(t *testing.T) {
	m := &directManager{}
	base := time.Now().Truncate(time.Second)
	mkOpened := func(id int, deadline uint64) peerwire.Opened {
		h := make([]byte, 32)
		h[0] = byte(id >> 8)
		h[1] = byte(id)
		return peerwire.Opened{
			Hash:    h,
			Context: peerwire.Context{Deadline: deadline},
		}
	}
	// Fill peer "p1" to its 2048 cap with entries expiring at base + 1s + MaxClockSkew.
	dl := uint64(base.Add(time.Second).UnixMilli())
	for i := range 2048 {
		if !m.reserveAuthentication("p1", mkOpened(i, dl), base) {
			t.Fatalf("entry %d rejected before cap", i)
		}
	}
	// Still before expiry: 2049th entry must be rejected.
	if m.reserveAuthentication("p1", mkOpened(9999, dl), base.Add(500*time.Millisecond)) {
		t.Fatal("expected rejection at 2048 cap before expiry")
	}
	// Advance past expiry (wall > until && now.After(elapsed)): cap prune must immediately reclaim and admit.
	after := base.Add(time.Duration(devicesec.MaxClockSkew+2000) * time.Millisecond)
	newDL := uint64(after.Add(10 * time.Second).UnixMilli())
	if !m.reserveAuthentication("p1", mkOpened(10000, newDL), after) {
		t.Fatal("expected expired entries to be pruned on capacity check")
	}
	if len(m.authReservations) != 1 {
		t.Fatalf("authReservations = %d, want 1", len(m.authReservations))
	}
}
