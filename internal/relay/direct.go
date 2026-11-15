package relay

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/paths"
	"github.com/Lewin671/remote-agent/internal/transport"
	"github.com/Lewin671/remote-agent/internal/transport/direct"
	"github.com/Lewin671/remote-agent/internal/transport/peerwire"
	"google.golang.org/protobuf/proto"
)

type directPeer struct {
	paths            [2]*direct.Path
	nextCapabilities uint64
	policyVersion    directPolicyVersion
	processValid     atomic.Bool
	session          *direct.Session
	peer             peerwire.Public // Routing hint only; Session proves the bootstrap correlation.
	binding          deviceauth.PeerBinding
	codec            *peerwire.Codec
	connection       []byte
	expires          uint64
}
type peerInput struct {
	peer string
	raw  []byte
}
type peerOutput struct {
	peer  string
	class int
	data  []byte
	out   direct.Outbound
	until time.Time
}
type directedUsage struct{ frames, bytes [2]int }
type directedTokens struct {
	at            time.Time
	frames, bytes float64
}
type directManager struct {
	authReservations map[string]peerReservation
	authCounts       map[string]int
	inboundPeers     map[string]*peerRates
	inboundAccount   [2]directedTokens

	closeOnce sync.Once

	d                   *dispatcher
	self                peerwire.Identity
	policy              *directPolicyStore
	pool                *direct.Pool
	ctx                 context.Context
	cancel              context.CancelFunc
	workers             sync.WaitGroup
	mu                  sync.Mutex
	closed              bool
	sessions            map[string]*directPeer
	active              map[string]*directPeer
	incoming            transport.FairQueue[*peerInput]
	outgoing            [3]transport.FairQueue[*peerOutput]
	queued              map[string]*directedUsage
	queuedBytes         [2]int
	tokens              [2]directedTokens
	readWake, writeWake chan struct{}
	path                transport.Path
	pathGeneration      uint64
	announced           uint64
	stun                []string
	loopback            bool
}

func newDirectManager(d *dispatcher) (*directManager, error) {
	if d.acct.Security == nil || len(d.acct.DeviceSeed) != 32 {
		return nil, deviceauth.ErrUntrusted
	}
	box, err := ecdh.X25519().NewPrivateKey(d.acct.Security.XPrivate)
	if err != nil {
		return nil, err
	}
	sign := ed25519.NewKeyFromSeed(d.acct.DeviceSeed)
	_, head, id := d.security.endpoint.Snapshot()
	member, ok := head.Core.Member(id)
	if !ok || member.Kind != deviceauth.Machine || !bytes.Equal(member.EdPublic[:], sign.Public().(ed25519.PublicKey)) || !bytes.Equal(member.XPublic[:], box.PublicKey().Bytes()) {
		return nil, deviceauth.ErrUntrusted
	}
	policy, err := openDirectPolicy(filepath.Join(paths.Dir(), "direct-policy-"+id+".json"))
	if err != nil {
		return nil, err
	}
	instance, boot := make([]byte, 16), make([]byte, 32)
	_, _ = rand.Read(instance)
	_, _ = rand.Read(boot)
	ctx, cancel := context.WithCancel(d.ctx)
	m := &directManager{d: d, self: peerwire.Identity{ID: []byte(id), Role: 2, Sign: sign, Box: box, Instance: instance, Boot: boot}, policy: policy, pool: direct.NewPool(8), ctx: ctx, cancel: cancel, sessions: map[string]*directPeer{}, active: map[string]*directPeer{}, queued: map[string]*directedUsage{}, readWake: make(chan struct{}, 2), writeWake: make(chan struct{}, 1)}
	// The configured relay's own UDP service is the default. Deployments may
	// explicitly select their own STUN host; no third-party service is used.
	if address := os.Getenv("REMOTE_AGENT_STUN_URL"); address != "" {
		m.stun = []string{address}
	} else if server, err := url.Parse(d.acct.Server); err == nil && server.Hostname() != "" {
		m.stun = []string{"stun:" + net.JoinHostPort(server.Hostname(), "3478")}
	}
	m.loopback = d.endpoint.r.directLoopback
	if d.endpoint.r.directSTUN != nil {
		m.stun = append([]string{}, d.endpoint.r.directSTUN...)
	}
	m.incoming = transport.FairQueue[*peerInput]{Entries: 64, Bytes: 4 << 20, PeerEntries: 8, PeerBytes: 1 << 20}
	for i := range m.outgoing {
		m.outgoing[i] = transport.FairQueue[*peerOutput]{Entries: 4096, Bytes: 32 << 20, PeerEntries: 512, PeerBytes: 4 << 20}
	}
	for range 2 {
		m.workers.Go(m.readLoop)
	}
	m.workers.Go(m.writeLoop)
	m.workers.Go(m.maintenance)
	return m, nil
}
func (m *directManager) close() {
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.closed = true
		m.cancel()
		var sessions []*direct.Session
		for _, p := range m.sessions {
			p.session.Close()
			sessions = append(sessions, p.session)
		}
		m.path = nil
		m.mu.Unlock()
		for _, s := range sessions {
			s.Wait()
		}
		m.workers.Wait()
	})
}
func (m *directManager) attach(path transport.Path) (uint64, func()) {
	m.mu.Lock()
	m.pathGeneration++
	generation := m.pathGeneration
	m.path = path
	m.mu.Unlock()
	notify(m.writeWake)
	return generation, func() {
		m.mu.Lock()
		if m.pathGeneration == generation {
			m.path = nil
		}
		m.mu.Unlock()
	}
}
func (m *directManager) receive(raw []byte) {
	f, err := peerwire.Parse(raw, 5)
	if err != nil || len(f) != 5 || !f[1].Bytes || len(f[1].B) != 32 || !bytes.Equal(f[2].B, m.self.ID) {
		return
	}
	peer := string(f[1].B)
	if !m.mu.TryLock() {
		return
	}
	if !m.closed && !f[3].Bytes && m.allowIncoming(peer, int(f[3].U)-1, len(raw), time.Now()) {
		frame := &peerInput{peer, append([]byte(nil), raw...)}
		if m.incoming.Push(frame.peer, frame.peer, len(raw), frame) {
			notify(m.readWake)
		}
	}
	m.mu.Unlock()
}
func (m *directManager) delivery(result *pb.PeerFrameResult, generation uint64) {
	if len(result.FrameId) != 16 {
		return
	}
	if !m.mu.TryLock() {
		return
	}
	defer m.mu.Unlock()
	if generation != m.pathGeneration {
		return
	}
	for _, p := range m.sessions {
		p.session.Delivery(result.FrameId, uint32(result.Status), uint64(result.RetryAfterMs))
	}
}
func (m *directManager) scope(id string) (deviceauth.PeerBinding, *peerwire.Codec, peerwire.Public, bool) {
	pin, head, selfID := m.d.security.endpoint.Snapshot()
	self, own := head.Core.Member(selfID)
	peer, ok := head.Core.Member(id)
	hash, err := head.Hash()
	binding := deviceauth.PeerBinding{Self: self, Peer: peer, Head: hash}
	if err != nil || !own || !ok || peer.Kind != deviceauth.Client || !m.d.security.directCurrent(self, peer, hash) {
		return binding, nil, peerwire.Public{}, false
	}
	codec, err := peerwire.NewCodec(pin.Account, pin.Domain, hash, head.Core.Epoch)
	if err != nil {
		return binding, nil, peerwire.Public{}, false
	}
	box, err := ecdh.X25519().NewPublicKey(peer.XPublic[:])
	if err != nil {
		return binding, nil, peerwire.Public{}, false
	}
	return binding, codec, peerwire.Public{ID: []byte(peer.ID), Role: 1, Sign: append([]byte(nil), peer.EdPublic[:]...), Box: box}, true
}
func (m *directManager) readLoop() {
	for {
		m.mu.Lock()
		input, ok := m.incoming.Pop(func(*peerInput) bool { return true })
		if m.incoming.Len() > 0 {
			notify(m.readWake)
		}
		m.mu.Unlock()
		if !ok {
			select {
			case <-m.ctx.Done():
				return
			case <-m.readWake:
				continue
			}
		}
		if m.ctx.Err() != nil {
			return
		}
		binding, codec, peer, ok := m.scope(input.peer)
		if !ok {
			continue
		}
		now := uint64(time.Now().UnixMilli())
		// Bootstrap authenticates with member public keys; a formal packet also
		// needs the process pair already learned by its connection's gate.
		opened, err := codec.Open(input.raw, peer, m.self, now)
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return
		}
		var target *directPeer
		if err == nil && opened.Context.Class == 1 && (opened.Type == 1 || opened.Type == 2) {
			if _, err := peerwire.KnownPayload(opened.Context.Class, opened.Type, opened.Payload); err != nil || !m.reserveAuthentication(input.peer, opened, time.Now()) {
				m.mu.Unlock()
				continue
			}
			target = m.sessions[string(opened.Context.Connection)]
			if target == nil && opened.Type == 1 {
				peer.Instance = append([]byte(nil), opened.Context.SenderInstance...)
				peer.Boot = append([]byte(nil), opened.Context.SenderBoot...)
				target = m.createLocked(binding, codec, peer, opened.Context.Connection, false, now, opened.Context.Deadline)
			}
			if target != nil && target.binding == binding {
				target.peer.Instance = append([]byte(nil), opened.Context.SenderInstance...)
				target.peer.Boot = append([]byte(nil), opened.Context.SenderBoot...)
			}
		} else {
			for _, p := range m.sessions {
				if p.binding != binding {
					continue
				}
				if candidate, e := codec.Open(input.raw, p.peer, m.self, now); e == nil && bytes.Equal(candidate.Context.Connection, p.connection) {
					if _, err := peerwire.KnownPayload(candidate.Context.Class, candidate.Type, candidate.Payload); err == nil && m.reserveAuthentication(input.peer, candidate, time.Now()) {
						target = p
					}
					break
				}
			}
		}
		if target != nil && target.binding == binding {
			target.session.Receive(input.raw)
		}
		m.mu.Unlock()
	}
}
func (m *directManager) createLocked(binding deviceauth.PeerBinding, codec *peerwire.Codec, peer peerwire.Public, connection []byte, initiate bool, now, deadline uint64) *directPeer {
	deadline = min(deadline, now+10000)
	id := binding.Peer.ID
	if _, exists := m.active[id]; !exists && len(m.active) >= 8 {
		return nil
	}
	count := 0
	for _, p := range m.sessions {
		if p.binding.Peer.ID == id {
			count++
		}
	}
	if count >= 32 || len(m.sessions) >= 256 {
		return nil
	}
	// Preserve receipt-only retirement in the signal gate. An explicit new peer
	// process invalidates the old connection rather than installing its nonce.
	if old := m.active[id]; old != nil {
		if len(old.peer.Boot) > 0 && len(peer.Boot) > 0 && !bytes.Equal(old.peer.Boot, peer.Boot) {
			old.processValid.Store(false)
			old.session.Close()
		} else if old.session.Established() {
			old.session.Retire()
		}
	}
	p := &directPeer{policyVersion: m.policy.version(id), binding: binding, codec: codec, peer: peer, connection: append([]byte(nil), connection...), expires: now + 10000 + deviceauth.MaxClockSkew}
	p.processValid.Store(true)
	allow := func() bool {
		return p.processValid.Load() && m.ctx.Err() == nil && m.d.security.directCurrent(binding.Self, binding.Peer, binding.Head)
	}
	_, head, _ := m.d.security.endpoint.Snapshot()
	authority := head.Core.AuthorityID
	s, err := direct.NewSession(m.ctx, direct.SessionConfig{Self: m.self, Peer: peer, Codec: codec, Connection: connection, Deadline: deadline, Pool: m.pool, Allow: allow, Policy: func() bool { return m.policy.matches(id, p.policyVersion) }, STUN: m.stun, LoopbackOnly: m.loopback,
		Send:   func(out direct.Outbound) bool { return m.send(id, out) },
		Chosen: func() { m.choose(p) }, BeforeOffer: func() { m.choose(p) },
		Control: func(o peerwire.Opened, now uint64) (uint64, []byte) {
			if o.Type == 9 {
				body, err := m.policy.apply(o, authority)
				if err == nil {
					return 13, body
				}
				return 0, nil
			}
			if o.Type == 1 {
				return 4, peerwire.PB(peerwire.U(4, o.Context.Class), peerwire.U(5, o.Context.Seq), peerwire.B(6, o.Hash), peerwire.U(7, o.Context.Generation), peerwire.U(8, o.Context.Use))
			}
			return 0, nil
		},
		Ready: func(use uint64, path *direct.Path) {
			m.mu.Lock()
			if m.closed {
				m.mu.Unlock()
				path.Close()
				return
			}
			p.paths[use-1] = path
			p.nextCapabilities = 0
			m.workers.Go(func() { m.readPath(p, use, path) })
			m.mu.Unlock()
		},
	}, initiate)
	if err != nil {
		return nil
	}
	p.session = s
	m.sessions[string(connection)] = p
	m.active[id] = p
	return p
}
func (m *directManager) readPath(peer *directPeer, use uint64, path *direct.Path) {
	defer func() { path.Close(); path.Wait() }()
	// This local gate has no endpoint lock: AcceptOptions invokes it while the
	// endpoint atomically checks the full peer binding under its admission lock.
	allow := func() bool {
		fresh := m.d.security.directFresh.Load()
		return peer.processValid.Load() && peer.session.Active() && m.ctx.Err() == nil && fresh != nil && fresh.head == peer.binding.Head && m.d.security.now() < min(fresh.until, fresh.leaseUntil) && !m.d.security.blocked.Load() && m.policy.matches(peer.binding.Peer.ID, peer.policyVersion)
	}
	for {
		raw, err := path.Read(m.ctx)
		if err != nil {
			return
		}
		env := &pb.Envelope{}
		if proto.Unmarshal(raw, env) != nil {
			continue
		}
		wire, err := deviceauth.FromEnvelope(env)
		if err != nil {
			continue
		}
		header, err := deviceauth.ParseHeader(wire.Header)
		if err != nil || header.Sender != peer.binding.Peer.ID || header.Machine != m.d.acct.DeviceID || header.Purpose != deviceauth.Command {
			continue
		}
		deadline := time.UnixMilli(int64(header.Deadline))
		m.d.endpoint.dispatchDirect(m.ctx, peer.binding, use, allow, env, func(reply *pb.ForwardReply) {
			if reply.Result == nil || !allow() || m.d.security.endpoint.CheckPeer(peer.binding.Self, peer.binding.Peer, peer.binding.Head, m.d.security.now()) != nil {
				return
			}
			raw, err := proto.Marshal(reply.Result)
			if err == nil {
				path.TryWrite(raw, deadline)
			}
		})
	}
}
func (m *directManager) send(peer string, out direct.Outbound) bool {
	f, err := peerwire.Parse(out.Raw, 5)
	if err != nil || len(f) != 5 {
		return false
	}
	class := int(f[3].U) - 1
	if class < 0 || class > 1 {
		return false
	}
	data, err := proto.Marshal(&pb.PeerFrame{Kind: &pb.PeerFrame_DirectedPeerFrame{DirectedPeerFrame: &pb.DirectedPeerFrame{ToDevice: f[2].B, FrameClass: pb.FrameClass(f[3].U), FrameId: f[4].B, Sealed: f[5].B}}})
	if err != nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.path == nil {
		return false
	}
	usage := m.queued[peer]
	if usage == nil {
		usage = &directedUsage{}
	}
	frames, maxBytes := 256, 4<<20
	if class == 1 {
		frames, maxBytes = 512, 1<<20
	}
	if usage.frames[class] >= frames || usage.bytes[class]+len(data) > maxBytes || m.queuedBytes[0]+m.queuedBytes[1]+len(data) > 32<<20 || class == 0 && m.queuedBytes[0]+len(data) > 24<<20 {
		return false
	}
	queue := 1
	if class == 1 {
		queue = 0
	} else if !out.Critical {
		queue = 2
	}
	until := minTime(time.Now().Add(time.Second), time.UnixMilli(int64(out.Deadline)))
	q := &peerOutput{peer, class, data, out, until}
	if !m.outgoing[queue].Push(peer, peer, len(data), q) {
		return false
	}
	usage.frames[class]++
	usage.bytes[class] += len(data)
	m.queued[peer] = usage
	m.queuedBytes[class] += len(data)
	notify(m.writeWake)
	return true
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func (m *directManager) reserveRate(class, n int, now time.Time) bool {
	fps, bps, burst := 256.0, float64(4<<20), 256.0
	if class == 1 {
		fps, bps, burst = 512, 512<<10, 1024
	}
	b := m.tokens[class]
	if b.at.IsZero() {
		b = directedTokens{at: now, frames: burst, bytes: bps}
	}
	dt := max(0, now.Sub(b.at).Seconds())
	b.at = now
	b.frames = min(burst, b.frames+dt*fps)
	b.bytes = min(bps, b.bytes+dt*bps)
	if b.frames < 1 || b.bytes < float64(n) {
		m.tokens[class] = b
		return false
	}
	b.frames--
	b.bytes -= float64(n)
	m.tokens[class] = b
	return true
}
func (m *directManager) releaseOutput(q *peerOutput) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releaseOutputLocked(q)
}
func (m *directManager) releaseOutputLocked(q *peerOutput) {
	u := m.queued[q.peer]
	u.frames[q.class]--
	u.bytes[q.class] -= len(q.data)
	m.queuedBytes[q.class] -= len(q.data)
	if u.frames == [2]int{} {
		delete(m.queued, q.peer)
	}
}
func (m *directManager) writeLoop() {
	for {
		m.mu.Lock()
		var q *peerOutput
		found := false
		queue := 0
		for i := range m.outgoing {
			if q, found = m.outgoing[i].Pop(func(*peerOutput) bool { return true }); found {
				queue = i
				break
			}
		}
		path := m.path
		allowed := found && (!time.Now().Before(q.until) || m.reserveRate(q.class, len(q.data), time.Now()))
		if found && !allowed {
			m.outgoing[queue].Push(q.peer, q.peer, len(q.data), q)
		}
		m.mu.Unlock()
		if m.ctx.Err() != nil {
			return
		}
		if !found {
			select {
			case <-m.ctx.Done():
				return
			case <-m.writeWake:
				continue
			}
		}
		if !allowed {
			timer := time.NewTimer(2 * time.Millisecond)
			select {
			case <-m.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		if path != nil && time.Now().Before(q.until) && q.out.Valid != nil && q.out.Valid() {
			ctx, cancel := context.WithDeadline(m.ctx, q.until)
			_ = path.Write(ctx, q.data)
			cancel()
		}
		m.releaseOutput(q)
	}
}
func (m *directManager) maintenance() {
	t := time.NewTicker(25 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-t.C:
		}
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return
		}
		now := uint64(time.Now().UnixMilli())
		for key, p := range m.sessions {
			select {
			case <-p.session.Done():
				if m.active[p.binding.Peer.ID] == p {
					delete(m.active, p.binding.Peer.ID)
				}
			default:
			}
			if now > p.session.RetainUntil() {
				select {
				case <-p.session.Done():
					delete(m.sessions, key)
					if m.active[p.binding.Peer.ID] == p {
						delete(m.active, p.binding.Peer.ID)
					}
				default:
				}
			}
		}
		for i := range m.outgoing {
			for {
				q, ok := m.outgoing[i].Pop(func(q *peerOutput) bool { return !time.Now().Before(q.until) })
				if !ok {
					break
				}
				m.releaseOutputLocked(q)
			}
		}
		for _, p := range m.active {
			if p.session.Established() && p.session.Active() && now >= p.nextCapabilities {
				var id [16]byte
				_, _ = rand.Read(id[:])
				body := peerwire.PB(peerwire.B(1, id[:]), peerwire.U(2, 1), peerwire.U(2, 4), peerwire.U(2, 9), peerwire.U(2, 10), peerwire.U(2, 13), peerwire.U(3, 1), peerwire.U(3, 2), peerwire.U(3, 3), peerwire.U(3, 4), peerwire.U(3, 5), peerwire.U(3, 6), peerwire.U(3, 7), peerwire.U(3, 8), peerwire.U(3, 9), peerwire.U(4, now+60000), peerwire.U(5, 1), peerwire.U(6, 1<<20))
				if p.session.SendControl(1, body) {
					p.nextCapabilities = now + 55000
				}
			}
		}
		pathGeneration := m.pathGeneration
		announce := m.path != nil && m.announced != pathGeneration
		m.mu.Unlock()
		if !announce || m.d.security.directFresh.Load() == nil {
			continue
		}
		_, head, _ := m.d.security.endpoint.Snapshot()
		for _, member := range head.Core.Members {
			if member.Kind != deviceauth.Client {
				continue
			}
			binding, codec, peer, ok := m.scope(member.ID)
			if !ok {
				continue
			}
			m.mu.Lock()
			if m.active[member.ID] == nil && len(m.active) < 8 {
				connection := make([]byte, 16)
				_, _ = rand.Read(connection)
				m.createLocked(binding, codec, peer, connection, true, now, now+10000)
			} else if active := m.active[member.ID]; active != nil {
				active.session.Discovered()
			}
			m.mu.Unlock()
		}
		m.mu.Lock()
		if m.pathGeneration == pathGeneration {
			m.announced = pathGeneration
		}
		m.mu.Unlock()
	}
}

// Only the fixed Offerer may create an offer. Once its first PC reserves the
// pair, both uses belong to that connection. The other role follows a verified
// Offer; concurrent bootstraps therefore cannot steal one PC use each.
func (m *directManager) choose(chosen *directPeer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	for _, p := range m.sessions {
		if p != chosen && p.binding.Peer.ID == chosen.binding.Peer.ID {
			p.session.Retire()
		}
	}
	m.active[chosen.binding.Peer.ID] = chosen
}
