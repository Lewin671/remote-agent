package relay

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"github.com/Lewin671/remote-agent/internal/transport"
	"github.com/Lewin671/remote-agent/internal/transport/direct"
	"github.com/Lewin671/remote-agent/shared/peerwire"
)

func TestDirectPeerQueueLimitsAndFreshnessRevocationGate(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	c := &conn{}
	keys := initializeTestSecurityKeys(t, c)
	var nowMs atomic.Uint64
	nowMs.Store(uint64(time.Now().UnixMilli()))
	c.security.clock = nowMs.Load

	pin, head, selfID := c.security.endpoint.Snapshot()
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
	defer cancel()
	link := transport.NewLink(ctx)
	defer link.Close()
	c.acct.Security = &SecurityConfig{XPrivate: keys.x}
	c.r = &Relay{directLoopback: true}
	ep := &endpoint{r: c.r, acct: c.acct, security: c.security, lifetime: link.Context(), link: link}
	ep.admissionOwner()
	defer ep.admission.close()

	d := &dispatcher{ctx: ctx, cancel: cancel, link: link, endpoint: ep, security: c.security, acct: c.acct}
	m, err := newDirectManager(d)
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()

	self, _ := head.Core.Member(selfID)
	peerMem, _ := head.Core.Member(head.Core.AuthorityID)
	boxPub, _ := ecdh.X25519().NewPublicKey(peerMem.XPublic[:])
	codec, _ := peerwire.NewCodec(pin.Account, pin.Domain, hash, head.Core.Epoch, protocol.Version)
	sess, err := direct.NewSession(ctx, direct.SessionConfig{
		Self:       m.self,
		Peer:       peerwire.Public{ID: []byte(peerMem.ID), Role: 1, Sign: peerMem.EdPublic[:], Box: boxPub.Bytes()},
		Codec:      codec,
		Connection: make([]byte, 16),
		Deadline:   nowMs.Load() + 10_000,
		Pool:       direct.NewPool(4),
		Allow:      func() bool { return true },
		Send:       func(direct.Outbound) bool { return true },
		Now:        func() time.Time { return time.UnixMilli(int64(nowMs.Load())) },
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	dp := &directPeer{
		policyVersion: m.policy.version(peerMem.ID),
		binding:       deviceauth.PeerBinding{Self: self, Peer: peerMem, Head: hash},
		codec:         codec,
		session:       sess,
		writeWake:     make(chan struct{}, 1),
	}
	dp.processValid.Store(true)

	// 1. Low queue drops oversize (> MaxSingleFragmentBytes) and > 32 items without blocking high queue.
	if dp.enqueueLow(make([]byte, direct.MaxSingleFragmentBytes+1)) {
		t.Fatal("expected enqueueLow to reject oversize payload")
	}
	small := make([]byte, 128)
	for i := range directEventQueueMaxItems {
		if !dp.enqueueLow(small) {
			t.Fatalf("expected enqueueLow #%d to succeed", i)
		}
	}
	if dp.enqueueLow(small) {
		t.Fatal("expected enqueueLow #33 to drop when queue full")
	}
	if !dp.enqueueHigh([]byte("result"), time.Now().Add(time.Second)) {
		t.Fatal("high-priority CommandResult must succeed even when lowQueue is full")
	}
	// Verify popWrite strictly prioritizes highQueue before lowQueue.
	if item, isHigh, ok := dp.popWrite(); !ok || !isHigh || string(item.raw) != "result" {
		t.Fatalf("expected highQueue item first, got ok=%v isHigh=%v raw=%q", ok, isHigh, item.raw)
	}
	if _, isHigh, ok := dp.popWrite(); !ok || isHigh {
		t.Fatalf("expected lowQueue item after highQueue drained, got ok=%v isHigh=%v", ok, isHigh)
	}

	// 2. Freshness expiry immediately disables peerAuthorized / peerAllowed.
	if !m.peerAuthorized(dp) {
		t.Fatal("expected peerAuthorized true before freshness expiry")
	}
	nowMs.Add(25_000) // past 20s directFresh window
	if m.peerAuthorized(dp) {
		t.Fatal("expected peerAuthorized false after freshness expiry")
	}

	// 3. Renew freshness, then add and revoke a real non-authority client via deviceauth.Install.
	c.security.directFresh.Store(&directFreshness{
		head:       hash,
		until:      nowMs.Load() + 20_000,
		leaseUntil: c.security.endpoint.LeaseExpiration(),
	})
	if !m.peerAuthorized(dp) {
		t.Fatal("expected peerAuthorized true after freshness renewal")
	}

	_, c2Ed, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, c2XPub, err := deviceauth.NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	c2, err := deviceauth.NewMember(c2Ed.Public().(ed25519.PublicKey), c2XPub, deviceauth.Client)
	if err != nil {
		t.Fatal(err)
	}
	secret3 := make([]byte, 32)
	secret3[0] = 3
	addedState, err := deviceauth.BuildPairState(head, c2, c2XPub, keys.secret, secret3, keys.phone, nowMs.Load()+1)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.security.endpoint.Install([]deviceauth.State{addedState}, []*deviceauth.RecoveryStatement{nil}); err != nil {
		t.Fatal(err)
	}
	addedLease, err := deviceauth.BuildLease(addedState, keys.phone, addedState.Core.IssuedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.security.endpoint.InstallLease(addedLease, nowMs.Load()); err != nil {
		t.Fatal(err)
	}
	addedHash, _ := addedState.Hash()
	addedCodec, err := peerwire.NewCodec(pin.Account, pin.Domain, addedHash, addedState.Core.Epoch, protocol.Version)
	if err != nil {
		t.Fatal(err)
	}
	c.security.directFresh.Store(&directFreshness{
		head:       addedHash,
		until:      nowMs.Load() + 20_000,
		leaseUntil: c.security.endpoint.LeaseExpiration(),
	})
	sess2, err := direct.NewSession(ctx, direct.SessionConfig{
		Self:       m.self,
		Peer:       peerwire.Public{ID: []byte(c2.ID), Role: 1, Sign: c2.EdPublic[:], Box: c2BoxPub(t, c2XPub).Bytes()},
		Codec:      addedCodec,
		Connection: make([]byte, 16),
		Deadline:   nowMs.Load() + 10_000,
		Pool:       direct.NewPool(4),
		Allow:      func() bool { return true },
		Send:       func(direct.Outbound) bool { return true },
		Now:        func() time.Time { return time.UnixMilli(int64(nowMs.Load())) },
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer sess2.Close()
	addedSelf, _ := addedState.Core.Member(selfID)
	dp2 := &directPeer{
		policyVersion: m.policy.version(c2.ID),
		binding:       deviceauth.PeerBinding{Self: addedSelf, Peer: c2, Head: addedHash},
		codec:         addedCodec,
		session:       sess2,
		writeWake:     make(chan struct{}, 1),
	}
	dp2.processValid.Store(true)
	if !m.peerAuthorized(dp2) {
		t.Fatalf("expected added client dp2 authorized before revocation: allowed=%v directCurrent=%v checkPeer=%v active=%v now=%d lease=%d",
			m.peerAllowed(dp2), m.d.security.directCurrent(dp2.binding.Self, dp2.binding.Peer, dp2.binding.Head),
			m.d.security.endpoint.CheckPeer(dp2.binding.Self, dp2.binding.Peer, dp2.binding.Head, m.d.security.now()),
			sess2.Active(), m.d.security.now(), c.security.endpoint.LeaseExpiration())
	}
	revokedState, err := deviceauth.BuildMembershipState(addedState, head.Core.Members, []string{c2.ID}, secret3, keys.secret, keys.phone, nowMs.Load()+2)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.security.endpoint.Install([]deviceauth.State{revokedState}, []*deviceauth.RecoveryStatement{nil}); err != nil {
		t.Fatal(err)
	}
	revokedLease, err := deviceauth.BuildLease(revokedState, keys.phone, revokedState.Core.IssuedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.security.endpoint.InstallLease(revokedLease, nowMs.Load()); err != nil {
		t.Fatal(err)
	}
	revokedHash, _ := revokedState.Hash()
	c.security.directFresh.Store(&directFreshness{
		head:       revokedHash,
		until:      nowMs.Load() + 20_000,
		leaseUntil: c.security.endpoint.LeaseExpiration(),
	})
	if m.peerAuthorized(dp2) {
		t.Fatal("expected peerAuthorized false after member revocation via Install")
	}

	// 4. Low queue also enforces the 256 KiB total byte cap even with < 32 items.
	dp.writeMu.Lock()
	dp.lowQueue = nil
	dp.lowBytes = 0
	dp.writeMu.Unlock()
	chunk := make([]byte, direct.MaxSingleFragmentBytes) // ~16 KiB; 17 items would exceed 256 KiB
	accepted := 0
	for range 20 {
		if dp.enqueueLow(chunk) {
			accepted++
		}
	}
	if accepted == 0 || accepted >= 20 {
		t.Fatalf("expected byte cap to bound queued items, got accepted=%d", accepted)
	}

	// 5. Missing key in non-nil initialAfter map calls startTail with hasInitial=false (after=0) in reconcileTailersLocked.
	type tailStartCall struct {
		after      uint64
		hasInitial bool
	}
	var callsMu sync.Mutex
	calls := map[string]tailStartCall{}
	done := make(chan struct{}, 2)
	m.mu.Lock()
	m.startTail = func(_ context.Context, sid string, after uint64, hasInitial bool) {
		callsMu.Lock()
		calls[sid] = tailStartCall{after: after, hasInitial: hasInitial}
		callsMu.Unlock()
		done <- struct{}{}
	}
	dp.paths[0] = new(direct.Path)
	dp.subscribed = map[string]struct{}{"present": {}, "missing": {}}
	m.active[peerMem.ID] = dp
	m.reconcileTailersLocked(map[string]uint64{"present": 42})
	m.mu.Unlock()
	for range 2 {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for reconcileTailersLocked starters")
		}
	}
	m.mu.Lock()
	delete(m.active, peerMem.ID)
	dp.paths[0] = nil
	dp.subscribed = nil
	m.reconcileTailersLocked(nil)
	m.mu.Unlock()
	callsMu.Lock()
	defer callsMu.Unlock()
	if got := calls["present"]; !got.hasInitial || got.after != 42 {
		t.Fatalf("expected present key call={42, true}, got %+v", got)
	}
	if got := calls["missing"]; got.hasInitial || got.after != 0 {
		t.Fatalf("expected missing key call={0, false}, got %+v", got)
	}
}

func c2BoxPub(t *testing.T, raw []byte) *ecdh.PublicKey {
	t.Helper()
	k, err := ecdh.X25519().NewPublicKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	return k
}
