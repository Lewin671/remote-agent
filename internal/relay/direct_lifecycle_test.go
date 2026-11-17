package relay

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"github.com/Lewin671/remote-agent/internal/transport"
	"github.com/Lewin671/remote-agent/internal/transport/direct"
	"github.com/Lewin671/remote-agent/internal/transport/peerwire"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

func TestRetiredDirectChooserCannotReplaceCurrentPeer(t *testing.T) {
	makeIdentity := func(role uint64) peerwire.Identity {
		pub, key, _ := ed25519.GenerateKey(rand.Reader)
		box, _ := ecdh.X25519().GenerateKey(rand.Reader)
		return peerwire.Identity{ID: []byte(hex.EncodeToString(peerwire.Hash(pub)[:16])), Role: role, Sign: key, Box: box, Instance: make([]byte, 16), Boot: make([]byte, 32)}
	}
	self, peer := makeIdentity(2), makeIdentity(1)
	codec, _ := peerwire.NewCodec("00000000000000000000000000000001", [32]byte{1}, [32]byte{2}, 1)
	makePeer := func(value byte) *directPeer {
		connection := make([]byte, 16)
		connection[0] = value
		s, err := direct.NewSession(context.Background(), direct.SessionConfig{Self: self, Peer: peer.Public(), Codec: codec, Connection: connection, Deadline: uint64(time.Now().Add(10 * time.Second).UnixMilli()), Allow: func() bool { return true }, Send: func(direct.Outbound) bool { return true }}, false)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close(); s.Wait() })
		p := &directPeer{session: s, connection: connection}
		p.binding.Peer.ID = string(peer.ID)
		p.processValid.Store(true)
		return p
	}
	old, current := makePeer(1), makePeer(2)
	id := string(peer.ID)
	m := &directManager{active: map[string]*directPeer{id: current}, sessions: map[string]*directPeer{string(old.connection): old, string(current.connection): current}}
	old.session.Retire()
	m.choose(old)
	if m.active[id] != current || !current.session.Active() {
		t.Fatal("retired chooser replaced or retired the current peer")
	}
}

type delayedRelayConn struct {
	net.Conn
	delayActive *atomic.Bool
	delay       time.Duration
	writes      *atomic.Int32
}

func (c *delayedRelayConn) Write(p []byte) (int, error) {
	if c.delayActive.CompareAndSwap(true, false) {
		c.writes.Add(1)
		time.Sleep(c.delay)
	}
	return c.Conn.Write(p)
}

func TestDirectWriteLoopRemainingResidenceDoesNotCloseSlowHealthyMainWebSocket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	received := make(chan *pb.PeerFrame, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		for {
			_, raw, err := ws.Read(r.Context())
			if err != nil {
				return
			}
			f := &pb.PeerFrame{}
			if proto.Unmarshal(raw, f) == nil {
				received <- f
			}
		}
	}))
	defer srv.Close()

	var delayActive atomic.Bool
	var delayedWrites atomic.Int32
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(dctx context.Context, network, addr string) (net.Conn, error) {
				conn, err := (&net.Dialer{}).DialContext(dctx, network, addr)
				if err != nil {
					return nil, err
				}
				return &delayedRelayConn{Conn: conn, delayActive: &delayActive, delay: 60 * time.Millisecond, writes: &delayedWrites}, nil
			},
		},
	}
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	relayPath := transport.NewRelayPath(ws)

	mctx, mcancel := context.WithCancel(ctx)
	m := &directManager{
		ctx:       mctx,
		cancel:    mcancel,
		queued:    map[string]*directedUsage{},
		writeWake: make(chan struct{}, 1),
	}
	for i := range m.outgoing {
		m.outgoing[i] = transport.FairQueue[*peerOutput]{Entries: 64, Bytes: 1 << 20, PeerEntries: 16, PeerBytes: 256 << 10}
	}
	m.workers.Go(m.writeLoop)
	defer func() {
		mcancel()
		m.workers.Wait()
	}()
	_, detach := m.attach(relayPath)
	defer detach()

	peerID := strings.Repeat("a", 32)
	id := make([]byte, 16)
	raw := peerwire.PB(
		peerwire.B(1, []byte(strings.Repeat("b", 32))),
		peerwire.B(2, []byte(peerID)),
		peerwire.U(3, 1),
		peerwire.B(4, id),
		peerwire.B(5, []byte("sealed-signal")),
	)

	// Remaining residence is ~15 ms, while the healthy socket write takes 60 ms.
	delayActive.Store(true)
	if !m.send(peerID, direct.Outbound{
		Raw:      raw,
		Deadline: uint64(time.Now().Add(15 * time.Millisecond).UnixMilli()),
		Critical: true,
		Valid:    func() bool { return true },
	}) {
		t.Fatal("failed to queue directed frame")
	}

	drainDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(drainDeadline) {
		m.mu.Lock()
		empty := m.queuedBytes == [2]int{} && len(m.queued) == 0
		m.mu.Unlock()
		if empty {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if delayedWrites.Load() != 1 {
		t.Fatalf("expected delayed write to run, got %d", delayedWrites.Load())
	}

	// Main WebSocket must still be open and able to carry normal relay traffic.
	appFrame, _ := proto.Marshal(&pb.PeerFrame{Kind: &pb.PeerFrame_Hello{Hello: &pb.StreamHello{Token: "healthy"}}})
	if err := relayPath.Write(ctx, appFrame); err != nil {
		t.Fatalf("main WebSocket closed by optional signaling write: %v", err)
	}
	select {
	case f := <-received:
		if f.GetDirectedPeerFrame() == nil || !bytes.Equal(f.GetDirectedPeerFrame().FrameId, id) {
			t.Fatalf("unexpected first frame %v", f)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for directed frame")
	}
	select {
	case f := <-received:
		if f.GetHello() == nil || f.GetHello().Token != "healthy" {
			t.Fatalf("unexpected application frame %v", f)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for application frame on main WebSocket")
	}
}

type blockingWritePath struct {
	transport.Path
	writes atomic.Int32
}

func (p *blockingWritePath) Write(context.Context, []byte) error {
	p.writes.Add(1)
	return nil
}

func TestDirectWriteLoopDropsExpiredFrameBeforeDispatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mctx, mcancel := context.WithCancel(ctx)
	m := &directManager{
		ctx:       mctx,
		cancel:    mcancel,
		queued:    map[string]*directedUsage{},
		writeWake: make(chan struct{}, 1),
	}
	for i := range m.outgoing {
		m.outgoing[i] = transport.FairQueue[*peerOutput]{Entries: 64, Bytes: 1 << 20, PeerEntries: 16, PeerBytes: 256 << 10}
	}
	p := &blockingWritePath{}
	_, detach := m.attach(p)
	defer detach()

	peerID := strings.Repeat("c", 32)
	q := &peerOutput{
		peer:  peerID,
		class: 0,
		data:  []byte("expired"),
		out:   direct.Outbound{Valid: func() bool { return true }},
		until: time.Now().Add(-time.Millisecond),
	}
	m.mu.Lock()
	m.outgoing[1].Push(peerID, peerID, len(q.data), q)
	m.queued[peerID] = &directedUsage{frames: [2]int{1, 0}, bytes: [2]int{len(q.data), 0}}
	m.queuedBytes[0] = len(q.data)
	m.mu.Unlock()
	notify(m.writeWake)

	m.workers.Go(m.writeLoop)
	defer func() {
		mcancel()
		m.workers.Wait()
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		empty := m.queuedBytes == [2]int{} && len(m.queued) == 0
		m.mu.Unlock()
		if empty {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if p.writes.Load() != 0 {
		t.Fatalf("expired frame was physically written (%d writes)", p.writes.Load())
	}
	m.mu.Lock()
	leaked := m.queuedBytes != [2]int{} || len(m.queued) != 0
	m.mu.Unlock()
	if leaked {
		t.Fatal("expired frame leaked queue accounting")
	}
}

type directLifecycleBackend struct{ publishBackend }

type directLifecycleLocal struct {
	remoteagentv1connect.LocalServiceHandler
}

func (directLifecycleBackend) Local() remoteagentv1connect.LocalServiceHandler {
	return directLifecycleLocal{}
}

func (directLifecycleLocal) GetSettings(context.Context, *connect.Request[pb.GetSettingsRequest]) (*connect.Response[pb.GetSettingsResponse], error) {
	return connect.NewResponse(&pb.GetSettingsResponse{}), nil
}

type directMemoryPath struct {
	onWrite func([]byte)
}

func (p *directMemoryPath) Read(ctx context.Context) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (p *directMemoryPath) Write(_ context.Context, frame []byte) error {
	if p.onWrite != nil {
		p.onWrite(frame)
	}
	return nil
}
func (p *directMemoryPath) Close()  {}
func (p *directMemoryPath) Finish() {}

func TestLateProbePreservesEstablishedRealPionSession(t *testing.T) {
	for _, wantDaemonOfferer := range []bool{false, true} {
		name := "client-offerer"
		if wantDaemonOfferer {
			name = "daemon-offerer"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			r := New(directLifecycleBackend{})
			SetDirectLoopbackForTest(r)

			var c *conn
			var keys *testSecurityKeys
			for {
				c = &conn{r: r}
				keys = initializeTestSecurityKeys(t, c)
				_, head, selfID := c.security.endpoint.Snapshot()
				if (selfID < head.Core.AuthorityID) == wantDaemonOfferer {
					break
				}
			}

			pin, head, selfID := c.security.endpoint.Snapshot()
			c.acct.Security = &SecurityConfig{XPrivate: keys.x}
			hash, err := head.Hash()
			if err != nil {
				t.Fatal(err)
			}
			now := c.security.now()
			c.security.directFresh.Store(&directFreshness{
				head:       hash,
				until:      now + 60000,
				leaseUntil: c.security.endpoint.LeaseExpiration(),
			})

			link := transport.NewLink(ctx)
			defer link.Close()
			ep := &endpoint{r: r, acct: c.acct, security: c.security, lifetime: link.Context(), link: link, sessionKey: c.sessionKey}
			ep.admissionOwner()
			defer ep.admission.close()

			d := &dispatcher{ctx: ctx, cancel: cancel, link: link, endpoint: ep, security: c.security, acct: c.acct}
			m, err := newDirectManager(d)
			if err != nil {
				t.Fatal(err)
			}
			defer m.close()

			clientID := head.Core.AuthorityID
			clientBoot := make([]byte, 32)
			clientInstance := make([]byte, 16)
			_, _ = rand.Read(clientBoot)
			_, _ = rand.Read(clientInstance)
			clientBox, err := ecdh.X25519().NewPrivateKey(keys.phoneX)
			if err != nil {
				t.Fatal(err)
			}
			clientSelf := peerwire.Identity{
				ID:       []byte(clientID),
				Role:     1,
				Instance: clientInstance,
				Boot:     clientBoot,
				Sign:     keys.phone,
				Box:      clientBox,
			}
			daemonMember, _ := head.Core.Member(selfID)
			daemonBoxPub, err := ecdh.X25519().NewPublicKey(daemonMember.XPublic[:])
			if err != nil {
				t.Fatal(err)
			}
			daemonPublic := peerwire.Public{
				ID:   []byte(selfID),
				Role: 2,
				Sign: daemonMember.EdPublic[:],
				Box:  daemonBoxPub,
			}
			codec, err := peerwire.NewCodec(pin.Account, pin.Domain, hash, head.Core.Epoch)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := deviceauth.BuildLease(head, keys.phone, head.Core.IssuedAt)
			if err != nil {
				t.Fatal(err)
			}
			clientEndpoint, err := deviceauth.InitEndpoint("", pin, keys.states, make([]*deviceauth.RecoveryStatement, len(keys.states)), clientID, keys.phone.Seed(), keys.phoneX, lease)
			if err != nil {
				t.Fatal(err)
			}
			defer clientEndpoint.Close()

			var mu sync.Mutex
			clientSessions := map[string]*direct.Session{}
			challengeSeen := make(chan []byte, 16)

			_, detach := m.attach(&directMemoryPath{onWrite: func(raw []byte) {
				f := &pb.PeerFrame{}
				if proto.Unmarshal(raw, f) != nil || f.GetDirectedPeerFrame() == nil {
					return
				}
				df := f.GetDirectedPeerFrame()
				delivery, err := proto.Marshal(&pb.ServerFrame{
					Kind: &pb.ServerFrame_PeerFrameDelivery{
						PeerFrameDelivery: &pb.PeerFrameDelivery{
							FromDevice: []byte(selfID),
							ToDevice:   df.ToDevice,
							FrameClass: df.FrameClass,
							FrameId:    df.FrameId,
							Sealed:     df.Sealed,
						},
					},
				})
				if err != nil {
					return
				}
				// Feed back delivery ack to daemon and forward sealed packet to matching client session.
				m.delivery(&pb.PeerFrameResult{FrameId: df.FrameId, Status: pb.PeerFrameResult_DELIVERED}, m.pathGeneration.Load())
				fields, err := peerwire.Parse(delivery, 1)
				if err != nil {
					return
				}
				packet := fields[11].B
				if o, err := codec.Open(packet, m.self.Public(), clientSelf, uint64(time.Now().UnixMilli())); err == nil {
					if o.Context.Class == 1 && o.Type == 2 {
						select {
						case challengeSeen <- append([]byte(nil), o.Context.Connection...):
						default:
						}
					}
					mu.Lock()
					target := clientSessions[string(o.Context.Connection)]
					mu.Unlock()
					if target != nil {
						target.Receive(packet)
					}
				}
			}})
			defer detach()

			forwardToDaemon := func(out direct.Outbound) bool {
				fields, err := peerwire.Parse(out.Raw, 5)
				if err != nil {
					return false
				}
				delivery, err := proto.Marshal(&pb.PeerFrameDelivery{
					FromDevice: fields[1].B,
					ToDevice:   fields[2].B,
					FrameClass: pb.FrameClass(fields[3].U),
					FrameId:    fields[4].B,
					Sealed:     fields[5].B,
				})
				if err != nil {
					return false
				}
				m.receive(delivery)
				return true
			}

			clientPool := direct.NewPool(4)
			startClientRound := func(connID []byte) (*direct.Session, [2]*direct.Path) {
				ready := make(chan struct {
					use  uint64
					path *direct.Path
				}, 2)
				cs, err := direct.NewSession(ctx, direct.SessionConfig{
					Self:         clientSelf,
					Peer:         daemonPublic,
					Codec:        codec,
					Connection:   connID,
					Deadline:     uint64(time.Now().Add(10 * time.Second).UnixMilli()),
					Pool:         clientPool,
					Allow:        func() bool { return ctx.Err() == nil },
					LoopbackOnly: true,
					Send:         forwardToDaemon,
					Ready: func(use uint64, p *direct.Path) {
						ready <- struct {
							use  uint64
							path *direct.Path
						}{use, p}
					},
				}, true)
				if err != nil {
					t.Fatal(err)
				}
				mu.Lock()
				clientSessions[string(connID)] = cs
				mu.Unlock()
				var paths [2]*direct.Path
				for range 2 {
					select {
					case item := <-ready:
						paths[item.use-1] = item.path
					case <-ctx.Done():
						t.Fatalf("client round timed out waiting for Pion paths")
					}
				}
				return cs, paths
			}

			executeGetSettings := func(path *direct.Path) error {
				cmd := &pb.Command{ProtocolVersion: protocol.Version, Kind: &pb.Command_GetSettings{GetSettings: &pb.GetSettingsRequest{}}}
				plain, _ := proto.Marshal(cmd)
				var nonce [16]byte
				_, _ = rand.Read(nonce[:])
				reqNow := uint64(time.Now().UnixMilli())
				h := deviceauth.Header{Kind: 5, Purpose: deviceauth.Command, Machine: selfID, Request: hex.EncodeToString(nonce[:]), Deadline: reqNow + 5000}
				sealed, err := clientEndpoint.Prepare(h, nil, plain)
				if err != nil {
					return err
				}
				env, err := sealed.Envelope()
				if err != nil {
					return err
				}
				packet, _ := proto.Marshal(env)
				callCtx, callCancel := context.WithTimeout(ctx, 3*time.Second)
				defer callCancel()
				if err := path.Write(callCtx, packet); err != nil {
					return err
				}
				respRaw, err := path.Read(callCtx)
				if err != nil {
					return err
				}
				respEnv := &pb.Envelope{}
				if err := proto.Unmarshal(respRaw, respEnv); err != nil {
					return err
				}
				wire, err := deviceauth.FromEnvelope(respEnv)
				if err != nil {
					return err
				}
				got, body, err := clientEndpoint.Open(wire, nil, true, uint64(time.Now().UnixMilli()))
				if err != nil {
					return err
				}
				body, err = deviceauth.Unpack(got, body)
				if err != nil {
					return err
				}
				result := &pb.CommandResult{}
				if err := proto.Unmarshal(body, result); err != nil {
					return err
				}
				if result.Outcome != "executed" || result.GetGetSettings() == nil {
					return errors.New("unexpected command result")
				}
				return nil
			}

			// 1. Establish initial healthy Pion session (connA).
			connA := make([]byte, 16)
			_, _ = rand.Read(connA)
			csA, pathsA := startClientRound(connA)
			defer func() {
				csA.Close()
				csA.Wait()
			}()

			// Wait until daemon's directPeer also marks both paths ready.
			var pA *directPeer
			waitDeadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(waitDeadline) {
				m.mu.Lock()
				cand := m.active[clientID]
				if cand != nil && cand.session.Established() && cand.paths[0] != nil && cand.paths[1] != nil {
					pA = cand
				}
				m.mu.Unlock()
				if pA != nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if pA == nil {
				t.Fatal("initial daemon session did not establish")
			}
			if err := executeGetSettings(pathsA[0]); err != nil {
				t.Fatalf("initial Pion command failed: %v", err)
			}

			// 2. Deliver a delayed Probe from a retired losing connection (connB, same client Boot).
			connB := make([]byte, 16)
			_, _ = rand.Read(connB)
			probeNow := uint64(time.Now().UnixMilli())
			pc := peerwire.NewProbeContext(clientSelf, daemonPublic, connB, 1, probeNow)
			pc.Deadline = probeNow + 800
			probeRaw, err := codec.Seal(clientSelf, daemonPublic, pc, 1, peerwire.PB(peerwire.B(1, connB), peerwire.B(2, clientInstance), peerwire.B(3, clientBoot)), nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !forwardToDaemon(direct.Outbound{Raw: probeRaw, Deadline: pc.Deadline, Critical: true}) {
				t.Fatal("failed to inject late Probe")
			}

			// Wait for daemon to open the candidate round and emit Challenge(connB).
			gotChallenge := false
			for !gotChallenge {
				select {
				case conn := <-challengeSeen:
					if bytes.Equal(conn, connB) {
						gotChallenge = true
					}
				case <-ctx.Done():
					t.Fatal("daemon never sent Challenge for late Probe")
				}
			}

			// Client retired connB and ignores Challenge(connB). Verify pA stays active & healthy immediately...
			m.mu.Lock()
			activeNow := m.active[clientID]
			pB := m.sessions[string(connB)]
			m.mu.Unlock()
			if activeNow != pA || !pA.session.Active() || !pA.session.Established() {
				t.Fatalf("late Probe retired or replaced healthy established session: active=%v want=%v sessionActive=%v", activeNow == pA, true, pA.session.Active())
			}
			if pB == nil {
				t.Fatal("expected candidate round for late Probe in m.sessions")
			}
			if err := executeGetSettings(pathsA[0]); err != nil {
				t.Fatalf("established Pion path failed right after late Probe: %v", err)
			}

			// ...and after the uncompleted candidate round times out.
			select {
			case <-pB.session.Done():
			case <-ctx.Done():
				t.Fatal("candidate round did not time out")
			}
			m.mu.Lock()
			activeAfterTimeout := m.active[clientID]
			m.mu.Unlock()
			if activeAfterTimeout != pA || !pA.session.Active() {
				t.Fatalf("timed-out candidate affected established session: active=%v sessionActive=%v", activeAfterTimeout == pA, pA.session.Active())
			}
			if err := executeGetSettings(pathsA[0]); err != nil {
				t.Fatalf("established Pion path failed after candidate timeout: %v", err)
			}

			// 3. A genuine replacement round (connC) that proves bootstrap and handoff retires pA and succeeds.
			csA.Retire()
			connC := make([]byte, 16)
			_, _ = rand.Read(connC)
			csC, pathsC := startClientRound(connC)
			defer func() {
				csC.Close()
				csC.Wait()
			}()
			var pC *directPeer
			waitDeadline = time.Now().Add(5 * time.Second)
			for time.Now().Before(waitDeadline) {
				m.mu.Lock()
				cand := m.active[clientID]
				if cand != nil && cand != pA && bytes.Equal(cand.connection, connC) && cand.session.Established() && cand.paths[0] != nil && cand.paths[1] != nil {
					pC = cand
				}
				m.mu.Unlock()
				if pC != nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if pC == nil {
				t.Fatal("legitimate replacement round did not take over m.active")
			}
			if pA.session.Active() {
				t.Fatal("old session remained active after verified handoff")
			}
			if err := executeGetSettings(pathsC[0]); err != nil {
				t.Fatalf("replacement Pion command failed: %v", err)
			}
		})
	}
}

type dualSendLocal struct {
	remoteagentv1connect.LocalServiceHandler
	started chan struct{}
	release chan struct{}
	calls   *atomic.Int32
}

type dualSendBackendWithLocal struct {
	publishBackend
	local remoteagentv1connect.LocalServiceHandler
}

func (b dualSendBackendWithLocal) SessionIDs() []string { return []string{"s-dual"} }
func (b dualSendBackendWithLocal) Local() remoteagentv1connect.LocalServiceHandler {
	return b.local
}

func (l *dualSendLocal) Send(ctx context.Context, _ *connect.Request[pb.SendRequest]) (*connect.Response[pb.SendResponse], error) {
	if l.calls.Add(1) == 1 {
		close(l.started)
	}
	select {
	case <-l.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return connect.NewResponse(&pb.SendResponse{QueuePosition: 1}), nil
}

func TestDirectDualSentInteractiveDeduplicatesAndSharesResult(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var execCount atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	r := New(dualSendBackendWithLocal{
		local: &dualSendLocal{started: started, release: release, calls: &execCount},
	})

	c := &conn{r: r}
	keys := initializeTestSecurityKeys(t, c)
	pin, head, selfID := c.security.endpoint.Snapshot()
	c.acct.Security = &SecurityConfig{XPrivate: keys.x}
	hash, err := head.Hash()
	if err != nil {
		t.Fatal(err)
	}
	now := c.security.now()
	c.security.directFresh.Store(&directFreshness{
		head:       hash,
		until:      now + 60000,
		leaseUntil: c.security.endpoint.LeaseExpiration(),
	})

	link := transport.NewLink(ctx)
	defer link.Close()
	ep := &endpoint{r: r, acct: c.acct, security: c.security, lifetime: link.Context(), link: link, sessionKey: c.sessionKey}
	ep.admissionOwner()
	defer ep.admission.close()

	clientID := head.Core.AuthorityID
	lease, err := deviceauth.BuildLease(head, keys.phone, head.Core.IssuedAt)
	if err != nil {
		t.Fatal(err)
	}
	clientEndpoint, err := deviceauth.InitEndpoint("", pin, keys.states, make([]*deviceauth.RecoveryStatement, len(keys.states)), clientID, keys.phone.Seed(), keys.phoneX, lease)
	if err != nil {
		t.Fatal(err)
	}
	defer clientEndpoint.Close()

	var nonce [16]byte
	_, _ = rand.Read(nonce[:])
	reqNow := uint64(time.Now().UnixMilli())
	cmd := &pb.Command{ProtocolVersion: protocol.Version, Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: "s-dual", Text: "hello"}}}
	plain, err := proto.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	h := deviceauth.Header{Kind: 5, Purpose: deviceauth.Command, Machine: selfID, Request: hex.EncodeToString(nonce[:]), Session: "s-dual", Deadline: reqNow + 5000}
	sealed, err := clientEndpoint.Prepare(h, nil, plain)
	if err != nil {
		t.Fatal(err)
	}
	env, err := sealed.Envelope()
	if err != nil {
		t.Fatal(err)
	}

	selfMember, _ := head.Core.Member(selfID)
	peerMember, _ := head.Core.Member(clientID)
	binding := deviceauth.PeerBinding{Self: selfMember, Peer: peerMember, Head: hash}

	relayCh := make(chan *pb.ForwardReply, 1)
	directCh := make(chan *pb.ForwardReply, 1)

	go ep.dispatch(ctx, &pb.ForwardCall{CallId: 1, AttemptId: "relay-leg", Command: env}, func(rep *pb.ForwardReply) {
		relayCh <- rep
	})
	go ep.dispatchDirect(ctx, binding, 1, func() bool { return true }, env, func(rep *pb.ForwardReply) {
		directCh <- rep
	})

	select {
	case <-started:
	case rep := <-relayCh:
		t.Fatalf("relay replied before Send started: %+v", rep)
	case rep := <-directCh:
		t.Fatalf("direct replied before Send started: %+v", rep)
	case <-ctx.Done():
		t.Fatal("command never started executing")
	}
	// Give the second arrival time to authenticate and attach to the in-flight execution.
	time.Sleep(50 * time.Millisecond)
	close(release)

	var relayReply, directReply *pb.ForwardReply
	select {
	case relayReply = <-relayCh:
	case <-ctx.Done():
		t.Fatal("relay waiter did not receive reply")
	}
	select {
	case directReply = <-directCh:
	case <-ctx.Done():
		t.Fatal("direct waiter did not receive reply")
	}

	if got := execCount.Load(); got != 1 {
		t.Fatalf("expected interactive command to execute once across dual legs, executed %d times", got)
	}
	for name, rep := range map[string]*pb.ForwardReply{"relay": relayReply, "direct": directReply} {
		if rep.Result == nil {
			t.Fatalf("%s waiter received empty result: %+v", name, rep)
		}
		wire, err := deviceauth.FromEnvelope(rep.Result)
		if err != nil {
			t.Fatalf("%s invalid envelope: %v", name, err)
		}
		got, body, err := clientEndpoint.Open(wire, nil, true, uint64(time.Now().UnixMilli()))
		if err != nil {
			t.Fatalf("%s open failed: %v", name, err)
		}
		body, err = deviceauth.Unpack(got, body)
		if err != nil {
			t.Fatalf("%s unpack failed: %v", name, err)
		}
		res := &pb.CommandResult{}
		if err := proto.Unmarshal(body, res); err != nil {
			t.Fatalf("%s unmarshal failed: %v", name, err)
		}
		if res.Outcome != "executed" || res.GetSend() == nil || res.GetSend().QueuePosition != 1 {
			t.Fatalf("%s unexpected command result: %+v", name, res)
		}
	}
}

type blockingInterruptLocal struct {
	*dualSendLocal
	blockEntered chan struct{}
	blockRelease chan struct{}
}

func (l *blockingInterruptLocal) Interrupt(ctx context.Context, _ *connect.Request[pb.InterruptRequest]) (*connect.Response[pb.InterruptResponse], error) {
	close(l.blockEntered)
	select {
	case <-l.blockRelease:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return connect.NewResponse(&pb.InterruptResponse{}), nil
}

func setupDualAdmissionFixture(t *testing.T) (
	ctx context.Context,
	cancel context.CancelFunc,
	ep *endpoint,
	clientEndpoint *deviceauth.Endpoint,
	binding deviceauth.PeerBinding,
	execCount *atomic.Int32,
	blockEntered chan struct{},
	blockRelease chan struct{},
	sendRelease chan struct{},
	sealCmd func(reqID string, cmd *pb.Command) *pb.Envelope,
) {
	t.Helper()
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)

	execCount = &atomic.Int32{}
	started := make(chan struct{})
	sendRelease = make(chan struct{})
	blockEntered = make(chan struct{})
	blockRelease = make(chan struct{})

	local := &blockingInterruptLocal{
		dualSendLocal: &dualSendLocal{started: started, release: sendRelease, calls: execCount},
		blockEntered:  blockEntered,
		blockRelease:  blockRelease,
	}
	r := New(dualSendBackendWithLocal{local: local})
	c := &conn{r: r}
	keys := initializeTestSecurityKeys(t, c)
	pin, head, selfID := c.security.endpoint.Snapshot()
	c.acct.Security = &SecurityConfig{XPrivate: keys.x}
	hash, err := head.Hash()
	if err != nil {
		t.Fatal(err)
	}
	now := c.security.now()
	c.security.directFresh.Store(&directFreshness{
		head:       hash,
		until:      now + 60000,
		leaseUntil: c.security.endpoint.LeaseExpiration(),
	})

	link := transport.NewLink(ctx)
	t.Cleanup(func() { link.Close() })
	ep = &endpoint{r: r, acct: c.acct, security: c.security, lifetime: link.Context(), link: link, sessionKey: c.sessionKey}
	ep.admissionOwner()
	t.Cleanup(ep.admission.close)

	clientID := head.Core.AuthorityID
	lease, err := deviceauth.BuildLease(head, keys.phone, head.Core.IssuedAt)
	if err != nil {
		t.Fatal(err)
	}
	clientEndpoint, err = deviceauth.InitEndpoint("", pin, keys.states, make([]*deviceauth.RecoveryStatement, len(keys.states)), clientID, keys.phone.Seed(), keys.phoneX, lease)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clientEndpoint.Close() })

	selfMember, _ := head.Core.Member(selfID)
	peerMember, _ := head.Core.Member(clientID)
	binding = deviceauth.PeerBinding{Self: selfMember, Peer: peerMember, Head: hash}

	sealCmd = func(reqID string, cmd *pb.Command) *pb.Envelope {
		t.Helper()
		plain, err := proto.Marshal(cmd)
		if err != nil {
			t.Fatal(err)
		}
		h := deviceauth.Header{Kind: 5, Purpose: deviceauth.Command, Machine: selfID, Request: reqID, Session: "s-dual", Deadline: uint64(time.Now().Add(5 * time.Second).UnixMilli())}
		sealed, err := clientEndpoint.Prepare(h, nil, plain)
		if err != nil {
			t.Fatal(err)
		}
		env, err := sealed.Envelope()
		if err != nil {
			t.Fatal(err)
		}
		return env
	}
	return
}

func assertExecutedSendReply(t *testing.T, clientEndpoint *deviceauth.Endpoint, rep *pb.ForwardReply) {
	t.Helper()
	if rep.Result == nil {
		t.Fatalf("empty reply: %+v", rep)
	}
	wire, err := deviceauth.FromEnvelope(rep.Result)
	if err != nil {
		t.Fatal(err)
	}
	got, body, err := clientEndpoint.Open(wire, nil, true, uint64(time.Now().UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	body, err = deviceauth.Unpack(got, body)
	if err != nil {
		t.Fatal(err)
	}
	res := &pb.CommandResult{}
	if err := proto.Unmarshal(body, res); err != nil {
		t.Fatal(err)
	}
	if res.Outcome != "executed" || res.GetSend() == nil || res.GetSend().QueuePosition != 1 {
		t.Fatalf("expected executed Send, got outcome=%q reason=%q res=%+v", res.Outcome, res.Reason, res)
	}
}

func TestCoalescedDirectOwnerCloseDoesNotPoisonSurvivingRelayWaiter(t *testing.T) {
	ctx, cancel, ep, clientEndpoint, binding, execCount, blockEntered, blockRelease, sendRelease, sealCmd := setupDualAdmissionFixture(t)
	defer cancel()
	close(sendRelease)

	// Occupy session s-dual in scheduler so the dual-sent Send queues in admission.
	blockerEnv := sealCmd("req-blocker-close-1111", &pb.Command{ProtocolVersion: protocol.Version, Kind: &pb.Command_Interrupt{Interrupt: &pb.InterruptRequest{SessionId: "s-dual"}}})
	go ep.dispatch(ctx, &pb.ForwardCall{CallId: 1, Command: blockerEnv}, func(*pb.ForwardReply) {})
	<-blockEntered

	var directAllowed atomic.Bool
	directAllowed.Store(true)
	sendEnv := sealCmd("req-dual-close-1111111", &pb.Command{ProtocolVersion: protocol.Version, Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: "s-dual", Text: "hello"}}})
	directCh := make(chan *pb.ForwardReply, 1)
	relayCh := make(chan *pb.ForwardReply, 1)

	// Direct arrives first and owns the coalesced inflight entry.
	ep.dispatchDirect(ctx, binding, 1, directAllowed.Load, sendEnv, func(rep *pb.ForwardReply) { directCh <- rep })
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ep.admission.mu.Lock()
		n := len(ep.admission.inflight)
		ep.admission.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Relay arrives while queued behind the blocker and joins inflight.
	ep.dispatch(ctx, &pb.ForwardCall{CallId: 2, Command: sendEnv}, func(rep *pb.ForwardReply) { relayCh <- rep })
	for time.Now().Before(deadline) {
		ep.admission.mu.Lock()
		w := ep.admission.inflightWaiters
		ep.admission.mu.Unlock()
		if w == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Simulate Direct closing (e.g., backgrounding) before the scheduler grants the slot.
	directAllowed.Store(false)
	close(blockRelease)

	select {
	case rep := <-relayCh:
		assertExecutedSendReply(t, clientEndpoint, rep)
	case <-ctx.Done():
		t.Fatal("surviving relay waiter never received reply")
	}
	if got := execCount.Load(); got != 1 {
		t.Fatalf("expected 1 execution for surviving relay waiter, got %d", got)
	}
	_ = directCh
}

func TestCoalescedOwnerContextCancelDoesNotAbortSurvivingWaiter(t *testing.T) {
	ctx, cancel, ep, clientEndpoint, binding, execCount, blockEntered, blockRelease, sendRelease, sealCmd := setupDualAdmissionFixture(t)
	defer cancel()
	close(sendRelease)

	blockerEnv := sealCmd("req-blocker-cancel-111", &pb.Command{ProtocolVersion: protocol.Version, Kind: &pb.Command_Interrupt{Interrupt: &pb.InterruptRequest{SessionId: "s-dual"}}})
	go ep.dispatch(ctx, &pb.ForwardCall{CallId: 1, Command: blockerEnv}, func(*pb.ForwardReply) {})
	<-blockEntered

	sendEnv := sealCmd("req-dual-cancel-111111", &pb.Command{ProtocolVersion: protocol.Version, Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: "s-dual", Text: "hello"}}})
	ownerCtx, cancelOwner := context.WithCancel(ctx)
	leg1Ch := make(chan *pb.ForwardReply, 1)
	leg2Ch := make(chan *pb.ForwardReply, 1)

	ep.dispatch(ownerCtx, &pb.ForwardCall{CallId: 2, Command: sendEnv}, func(rep *pb.ForwardReply) { leg1Ch <- rep })
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ep.admission.mu.Lock()
		n := len(ep.admission.inflight)
		ep.admission.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	ep.dispatchDirect(ctx, binding, 1, func() bool { return true }, sendEnv, func(rep *pb.ForwardReply) { leg2Ch <- rep })
	for time.Now().Before(deadline) {
		ep.admission.mu.Lock()
		w := ep.admission.inflightWaiters
		ep.admission.mu.Unlock()
		if w == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Cancel the first leg's context before admission runs.
	cancelOwner()
	close(blockRelease)

	select {
	case rep := <-leg2Ch:
		assertExecutedSendReply(t, clientEndpoint, rep)
	case <-ctx.Done():
		t.Fatal("surviving leg never received reply")
	}
	if got := execCount.Load(); got != 1 {
		t.Fatalf("expected 1 execution for surviving leg, got %d", got)
	}
	_ = leg1Ch
}

type notifiedWaitCtx struct {
	context.Context
	notify chan<- struct{}
}

func (c notifiedWaitCtx) Done() <-chan struct{} {
	select {
	case c.notify <- struct{}{}:
	default:
	}
	return c.Context.Done()
}

func replyReason(t *testing.T, clientEndpoint *deviceauth.Endpoint, rep *pb.ForwardReply) string {
	t.Helper()
	if rep.Reason != "" {
		return rep.Reason
	}
	if rep.Result == nil {
		return ""
	}
	wire, err := deviceauth.FromEnvelope(rep.Result)
	if err != nil {
		t.Fatal(err)
	}
	got, body, err := clientEndpoint.Open(wire, nil, true, uint64(time.Now().UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	body, err = deviceauth.Unpack(got, body)
	if err != nil {
		t.Fatal(err)
	}
	res := &pb.CommandResult{}
	if err := proto.Unmarshal(body, res); err != nil {
		t.Fatal(err)
	}
	return res.Reason
}

func TestCombinedInflightAndEndpointWaiterLimitRespectsMaxPerKey(t *testing.T) {
	ctx, cancel, ep, clientEndpoint, _, _, blockEntered, blockRelease, sendRelease, sealCmd := setupDualAdmissionFixture(t)
	defer cancel()

	// Block scheduler slot for s-dual so owner + first batch of duplicates sit in a.inflight.
	blockerEnv := sealCmd("req-blocker-waitcap-111", &pb.Command{ProtocolVersion: protocol.Version, Kind: &pb.Command_Interrupt{Interrupt: &pb.InterruptRequest{SessionId: "s-dual"}}})
	go ep.dispatch(ctx, &pb.ForwardCall{CallId: 1, Command: blockerEnv}, func(*pb.ForwardReply) {})
	<-blockEntered

	sendEnv := sealCmd("req-waitcap-11111111111", &pb.Command{ProtocolVersion: protocol.Version, Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: "s-dual", Text: "hello"}}})
	replies := make(chan *pb.ForwardReply, 80)
	replied := make(chan struct{}, 80)
	reply := func(rep *pb.ForwardReply) {
		replies <- rep
		select {
		case replied <- struct{}{}:
		default:
		}
	}

	// 1 owner + 20 waiters in a.inflight (pacing each so a.frames PeerEntries=8 never drops any).
	ep.dispatch(ctx, &pb.ForwardCall{CallId: 2, Command: sendEnv}, reply)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ep.admission.mu.Lock()
		n := len(ep.admission.inflight)
		ep.admission.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	for i := range 20 {
		ep.dispatch(ctx, &pb.ForwardCall{CallId: uint64(10 + i), Command: sendEnv}, reply)
		for time.Now().Before(deadline) {
			ep.admission.mu.Lock()
			w := ep.admission.inflightWaiters
			ep.admission.mu.Unlock()
			if w == i+1 {
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
	}

	// Release blocker so owner enters Send (blocks on sendRelease) and registers e.running[key].
	close(blockRelease)
	// Wait until Send is actually running inside AcceptAuthenticated.
	for time.Now().Before(deadline) {
		ep.admission.mu.Lock()
		// Wait until owner entered backend Send (execCount incremented by Send won't work since started was closed; sleep briefly after release).
		ep.admission.mu.Unlock()
		time.Sleep(80 * time.Millisecond)
		break
	}

	// Send 20 more duplicates one by one so none hit PeerEntries=8 in a.frames;
	// combined with 20 already waiting in inflight, 40 total > MaxWaitersPerKey (32), so >=8 must be busy.
	for i := range 20 {
		doneCh := make(chan struct{}, 1)
		wc := notifiedWaitCtx{Context: ctx, notify: doneCh}
		ep.dispatch(wc, &pb.ForwardCall{CallId: uint64(100 + i), Command: sendEnv}, reply)
		select {
		case <-doneCh:
		case <-replied:
		case <-time.After(time.Second):
			t.Fatalf("duplicate %d did not reach waiter table or reply", i)
		}
	}

	close(sendRelease)
	// Drain all 41 replies (1 owner + 40 duplicates).
	busyCount := 0
	for range 41 {
		select {
		case rep := <-replies:
			if replyReason(t, clientEndpoint, rep) == "busy" {
				busyCount++
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for replies; saw busyCount=%d", busyCount)
		}
	}
	if busyCount < 8 {
		t.Fatalf("expected at least 8 busy rejections across combined inflight+endpoint tables (limit %d), got %d", deviceauth.MaxWaitersPerKey, busyCount)
	}
}

func TestClaimInflightWaitersWakeOnAdmissionCloseAndNoLeakOnLinkShutdown(t *testing.T) {
	ctx, cancel, ep, _, _, _, blockEntered, blockRelease, sendRelease, sealCmd := setupDualAdmissionFixture(t)
	defer cancel()
	defer close(sendRelease)

	blockerEnv := sealCmd("req-blocker-closewake-1", &pb.Command{ProtocolVersion: protocol.Version, Kind: &pb.Command_Interrupt{Interrupt: &pb.InterruptRequest{SessionId: "s-dual"}}})
	go ep.dispatch(ctx, &pb.ForwardCall{CallId: 1, Command: blockerEnv}, func(*pb.ForwardReply) {})
	<-blockEntered

	sendEnv := sealCmd("req-closewake-1111111111", &pb.Command{ProtocolVersion: protocol.Version, Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: "s-dual", Text: "hello"}}})
	ep.dispatch(context.Background(), &pb.ForwardCall{CallId: 2, Command: sendEnv}, func(*pb.ForwardReply) {})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ep.admission.mu.Lock()
		n := len(ep.admission.inflight)
		ep.admission.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	// Duplicate waiter uses Background context AND join goes through link.Run -> claimInflight;
	// verify cancelling admission (a.cancel) wakes the waiter immediately even while pc.done is still open
	// (e.g. before Scheduler drains or if an owner is stalled).
	ep.dispatch(context.Background(), &pb.ForwardCall{CallId: 3, Command: sendEnv}, func(*pb.ForwardReply) {})
	for time.Now().Before(deadline) {
		ep.admission.mu.Lock()
		w := ep.admission.inflightWaiters
		ep.admission.mu.Unlock()
		if w == 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	ep.admission.mu.Lock()
	ep.admission.cancel()
	ep.admission.mu.Unlock()
	woke := false
	for deadlineWake := time.Now().Add(time.Second); time.Now().Before(deadlineWake); {
		ep.admission.mu.Lock()
		w := ep.admission.inflightWaiters
		ep.admission.mu.Unlock()
		if w == 0 {
			woke = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !woke {
		close(blockRelease)
		t.Fatal("claimInflight waiter did not wake on admission cancel (a.ctx)")
	}

	// Clean up blocker and admission.
	close(blockRelease)
	ep.admission.close()
	ep.link.Close()
	ep.admission.mu.Lock()
	leaked := len(ep.admission.inflight)
	ep.admission.mu.Unlock()
	if leaked != 0 {
		t.Fatalf("expected a.inflight empty after close, got %d entries", leaked)
	}
}

func TestDirectCoalescedInflightPayloadConflict(t *testing.T) {
	ctx, cancel, ep, clientEndpoint, binding, execCount, blockEntered, blockRelease, sendRelease, sealCmd := setupDualAdmissionFixture(t)
	defer cancel()
	close(sendRelease)

	blockerEnv := sealCmd("req-blocker-conflict-1", &pb.Command{ProtocolVersion: protocol.Version, Kind: &pb.Command_Interrupt{Interrupt: &pb.InterruptRequest{SessionId: "s-dual"}}})
	go ep.dispatch(ctx, &pb.ForwardCall{CallId: 1, Command: blockerEnv}, func(*pb.ForwardReply) {})
	<-blockEntered

	reqID := "req-conflict-11111111"
	envA := sealCmd(reqID, &pb.Command{ProtocolVersion: protocol.Version, Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: "s-dual", Text: "first"}}})
	envB := sealCmd(reqID, &pb.Command{ProtocolVersion: protocol.Version, Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: "s-dual", Text: "second"}}})

	ownerCh := make(chan *pb.ForwardReply, 1)
	conflictCh := make(chan *pb.ForwardReply, 1)
	ep.dispatch(ctx, &pb.ForwardCall{CallId: 2, Command: envA}, func(rep *pb.ForwardReply) { ownerCh <- rep })
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ep.admission.mu.Lock()
		n := len(ep.admission.inflight)
		ep.admission.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	var allowDirect atomic.Bool
	allowDirect.Store(true)
	ep.dispatchDirect(ctx, binding, 1, allowDirect.Load, envB, func(rep *pb.ForwardReply) { conflictCh <- rep })

	select {
	case rep := <-conflictCh:
		if rep.Result == nil {
			t.Fatalf("expected signed conflict envelope, got empty result: %+v", rep)
		}
		wire, err := deviceauth.FromEnvelope(rep.Result)
		if err != nil {
			t.Fatal(err)
		}
		got, body, err := clientEndpoint.Open(wire, nil, true, uint64(time.Now().UnixMilli()))
		if err != nil {
			t.Fatal(err)
		}
		body, err = deviceauth.Unpack(got, body)
		if err != nil {
			t.Fatal(err)
		}
		res := &pb.CommandResult{}
		if err := proto.Unmarshal(body, res); err != nil {
			t.Fatal(err)
		}
		if res.Outcome != "conflict" || res.ConflictKind != "identity" {
			t.Fatalf("expected outcome=conflict conflict_kind=identity, got outcome=%q kind=%q", res.Outcome, res.ConflictKind)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for direct inflight payload conflict reply")
	}

	close(blockRelease)
	select {
	case rep := <-ownerCh:
		assertExecutedSendReply(t, clientEndpoint, rep)
	case <-time.After(2 * time.Second):
		t.Fatal("owner command did not finish after conflict rejection")
	}
	if got := execCount.Load(); got != 1 {
		t.Fatalf("expected 1 execution, got %d", got)
	}
	ep.admission.mu.Lock()
	remaining := len(ep.admission.inflight)
	ep.admission.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("expected empty inflight after owner completed, got %d", remaining)
	}
}
