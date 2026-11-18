// Package direct adapts authenticated, negotiated WebRTC data channels to an
// opaque transport path. It has no authority to decrypt or execute commands.
package direct

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"net"
	"strings"
	"sync"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/transport/peerwire"
	"github.com/pion/webrtc/v4"
	"google.golang.org/protobuf/proto"
)

const maxFrame = 16 << 10
const fragmentPayload = maxFrame - 128

// MaxSingleFragmentBytes is the maximum payload size that fits in a single direct Fragment.
const MaxSingleFragmentBytes = fragmentPayload

var ErrClosed = errors.New("direct_closed")
var ErrLimit = errors.New("direct_limit")

type Config struct {
	Owner string
	Use   uint64

	Peer       string
	Generation uint64
	Pool       *Pool
	STUN       []string
	// Allow is checked before SDK work, every application frame, and every send.
	// It must read the process owner's current membership, lease and policy gate.
	Allow        func() bool
	LoopbackOnly bool // isolated integration tests only; never enabled by discovery
	// Port, when set, gives the host candidates one fixed UDP port.
	Port      *PortMux
	Candidate func(*webrtc.ICECandidateInit)
	State     func(webrtc.PeerConnectionState)
	// Why names the gate that currently refuses the path (diagnostics only).
	Why func() string
	// Ended is called once, after the path's own end line (diagnostics only).
	Ended func()
}
type partial struct {
	count, length uint32
	hash          []byte
	parts         map[uint32][]byte
	held, frames  int
	deadline      time.Time
}
type message struct{ data []byte }
type Path struct {
	cfg          Config
	pc           *webrtc.PeerConnection
	data, probe  *webrtc.DataChannel
	ctx          context.Context
	cancel       context.CancelFunc
	once         sync.Once
	mu           sync.Mutex
	confirmed    bool
	confirmOnce  sync.Once
	confirmation chan struct{}
	pending      map[string]*partial
	received     chan message
	ready, low   chan struct{}
	closed       chan struct{}
	sendMu       chan struct{}
	writers      sync.WaitGroup
	diag         pathDiag
}

func New(ctx context.Context, cfg Config) (*Path, error) {
	if len(cfg.STUN) > 2 {
		return nil, ErrLimit
	}
	for _, address := range cfg.STUN {
		if !strings.HasPrefix(address, "stun:") || strings.Contains(address, "?") && !strings.HasSuffix(address, "?transport=udp") {
			return nil, ErrLimit
		}
	}
	if cfg.Pool == nil || cfg.Allow == nil || !cfg.Allow() || cfg.Generation == 0 || cfg.Use < 1 || cfg.Use > 2 || !cfg.Pool.acquire(cfg.Peer, cfg.Owner, cfg.Use) {
		return nil, ErrLimit
	}
	ctx, cancel := context.WithCancel(ctx)
	p := &Path{diag: pathDiag{born: time.Now()}, cfg: cfg, ctx: ctx, cancel: cancel, pending: map[string]*partial{}, received: make(chan message, 128), ready: make(chan struct{}, 1), low: make(chan struct{}, 1), sendMu: make(chan struct{}, 1), closed: make(chan struct{}), confirmation: make(chan struct{})}
	se := webrtc.SettingEngine{}
	se.LoggerFactory = silentLogger{}
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6})
	se.SetSCTPMaxMessageSize(maxFrame)
	se.SetSCTPMaxReceiveBufferSize(1 << 20)
	if cfg.LoopbackOnly {
		se.SetIncludeLoopbackCandidate(true)
		se.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
	}
	if mux := cfg.Port.current(); mux != nil {
		se.SetICEUDPMux(mux)
	}
	servers := []webrtc.ICEServer{}
	if len(cfg.STUN) > 0 {
		servers = append(servers, webrtc.ICEServer{URLs: cfg.STUN})
	}
	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(se)).NewPeerConnection(webrtc.Configuration{ICEServers: servers})
	if err != nil {
		cancel()
		cfg.Pool.release(cfg.Peer, cfg.Use)
		return nil, err
	}
	p.pc = pc
	pc.OnDataChannel(func(dc *webrtc.DataChannel) { _ = dc.Close(); p.closeWhy("unexpected_data_channel") })
	pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) { p.diag.ice(s.String()) })
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if !p.valid() {
			return
		}
		if cfg.Candidate != nil {
			if c == nil {
				cfg.Candidate(nil)
			} else {
				v := c.ToJSON()
				cfg.Candidate(&v)
			}
		}
	})
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if cfg.State != nil {
			cfg.State(state)
		}
		p.diag.conn(state.String())
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed || state == webrtc.PeerConnectionStateDisconnected {
			p.closeWhy("pc_" + state.String())
		}
	})
	for id := uint16(0); id < 2; id++ {
		negotiated, ordered, zero := true, id == 0, uint16(0)
		init := &webrtc.DataChannelInit{ID: &id, Negotiated: &negotiated, Ordered: &ordered}
		if id == 1 {
			init.MaxRetransmits = &zero
		}
		dc, err := pc.CreateDataChannel("remote-agent", init)
		if err != nil {
			p.closeWhy("create_data_channel")
			return nil, err
		}
		name := "dc" + string(rune('0'+id))
		onErr, onClose := name+"_failed", name+"_closed"
		dc.OnError(func(error) { p.closeWhy(onErr) })
		dc.OnClose(func() { p.closeWhy(onClose) })
		if id == 0 {
			p.data = dc
			dc.SetBufferedAmountLowThreshold(32 << 10)
			dc.OnBufferedAmountLow(func() {
				select {
				case p.low <- struct{}{}:
				default:
				}
			})
			dc.OnOpen(func() {
				p.diag.open()
				select {
				case p.ready <- struct{}{}:
				default:
				}
			})
			dc.OnMessage(p.receive)
		} else {
			p.probe = dc
			dc.OnMessage(func(m webrtc.DataChannelMessage) {
				// Probe channel accepts only fixed-size opaque echoes, never fragments or
				// commands. Its response cannot amplify a request.
				if m.IsString || len(m.Data) != 17 || m.Data[0] != 0 {
					return
				}
				if !p.businessReady() {
					p.diag.probe(false)
					return
				}
				p.diag.probe(true)
				reply := append([]byte(nil), m.Data...)
				reply[0] = 1
				_ = dc.Send(reply)
			})
		}
	}
	go func() {
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				p.closeWhy("owner_closed")
				return
			case now := <-t.C:
				if !p.valid() {
					p.closeWhy(p.gateReason())
					return
				}
				p.expire(now)
			}
		}
	}()
	return p, nil
}

// closeWhy closes the path and, if it is the first to do so, records why.
func (p *Path) closeWhy(reason string) {
	p.diag.why(reason)
	p.Close()
}

// gateReason names the gate that made the path invalid.
func (p *Path) gateReason() string {
	if p.ctx.Err() != nil {
		return "owner_closed"
	}
	if p.cfg.Why != nil {
		if r := p.cfg.Why(); r != "" {
			return r
		}
	}
	return "not_allowed"
}
func (p *Path) valid() bool { return p.ctx.Err() == nil && p.cfg.Allow() }
func (p *Path) businessReady() bool {
	p.mu.Lock()
	confirmed := p.confirmed
	p.mu.Unlock()
	return confirmed && p.valid()
}

// Confirm is called only after both SDP hashes and fingerprints have passed the
// authenticated peer signal gate and DTLS has verified the remote certificate.
func (p *Path) Confirm() {
	p.diag.confirm()
	p.mu.Lock()
	p.confirmed = true
	p.mu.Unlock()
	p.confirmOnce.Do(func() { close(p.confirmation) })
}
func (p *Path) Description(offer, restart bool) ([]byte, error) {
	if !p.valid() {
		return nil, ErrClosed
	}
	var s webrtc.SessionDescription
	var err error
	if offer {
		s, err = p.pc.CreateOffer(&webrtc.OfferOptions{ICERestart: restart})
	} else {
		s, err = p.pc.CreateAnswer(nil)
	}
	if err != nil {
		return nil, err
	}
	raw := []byte(s.SDP)
	if err = peerwire.ValidateSDP(raw); err != nil {
		return nil, err
	}
	// Capture the SDK original before gathering starts. Never use or trim the
	// candidate-bearing LocalDescription getter to construct an authenticated SDP.
	if !p.valid() {
		return nil, ErrClosed
	}
	if err = p.pc.SetLocalDescription(s); err != nil {
		return nil, err
	}
	return raw, nil
}
func (p *Path) Remote(raw []byte, offer bool) error {
	if !p.valid() {
		return ErrClosed
	}
	if err := peerwire.ValidateSDP(raw); err != nil {
		return err
	}
	kind := webrtc.SDPTypeAnswer
	if offer {
		kind = webrtc.SDPTypeOffer
	}
	return p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: kind, SDP: string(raw)})
}
func (p *Path) AddCandidate(c webrtc.ICECandidateInit) error {
	if !p.valid() {
		return ErrClosed
	}
	return p.pc.AddICECandidate(c)
}
func (p *Path) Read(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.ctx.Done():
		return nil, ErrClosed
	case <-p.confirmation:
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.ctx.Done():
		return nil, ErrClosed
	case m := <-p.received:
		p.cfg.Pool.free(p.cfg.Peer, 1, len(m.data), 1)
		if !p.businessReady() {
			return nil, ErrClosed
		}
		return m.data, nil
	}
}

// reserveWrite accounts for every waiting writer before starting any goroutine.
func (p *Path) reserveWrite(raw []byte) ([]byte, func(), error) {
	if len(raw) == 0 || len(raw) > 1<<20 || !p.businessReady() {
		return nil, nil, ErrClosed
	}
	count := (len(raw) + fragmentPayload - 1) / fragmentPayload
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ctx.Err() != nil {
		return nil, nil, ErrClosed
	}
	if !p.cfg.Pool.reserve(p.cfg.Peer, 0, len(raw), count) {
		return nil, nil, ErrLimit
	}
	p.writers.Add(1)
	return append([]byte(nil), raw...), func() { p.cfg.Pool.free(p.cfg.Peer, 0, len(raw), count); p.writers.Done() }, nil
}
func (p *Path) Write(ctx context.Context, raw []byte) error {
	frozen, release, err := p.reserveWrite(raw)
	if err != nil {
		return err
	}
	defer release()
	return p.write(ctx, frozen)
}

// Done returns a channel closed when the path is canceled or closed.
func (p *Path) Done() <-chan struct{} { return p.ctx.Done() }

// TryWriteLow sends a single-fragment low-priority payload without blocking or
// waiting on channel backpressure. Any busy writer, buffered backlog above
// maxBuffered, pool limit, or oversize payload is dropped immediately.
func (p *Path) TryWriteLow(raw []byte, maxBuffered uint64) bool {
	if len(raw) == 0 || len(raw) > fragmentPayload || !p.businessReady() {
		return false
	}
	if p.data == nil || p.data.ReadyState() != webrtc.DataChannelStateOpen || p.data.BufferedAmount() > maxBuffered {
		return false
	}
	select {
	case p.sendMu <- struct{}{}:
	default:
		return false
	}
	defer func() { <-p.sendMu }()
	if !p.businessReady() || p.data.ReadyState() != webrtc.DataChannelStateOpen || p.data.BufferedAmount() > maxBuffered {
		return false
	}
	frozen, release, err := p.reserveWrite(raw)
	if err != nil {
		return false
	}
	defer release()
	var id [16]byte
	_, _ = rand.Read(id[:])
	hash := sha256.Sum256(frozen)
	frame, err := proto.Marshal(&pb.Fragment{
		MessageId:        id[:],
		Index:            0,
		Count:            1,
		CiphertextLength: uint32(len(frozen)),
		CiphertextSha256: hash[:],
		PathGeneration:   p.cfg.Generation,
		Fragment:         frozen,
	})
	if err != nil || len(frame) > maxFrame {
		return false
	}
	if sctp := p.pc.SCTP(); sctp != nil {
		if caps := sctp.GetCapabilities(); caps.MaxMessageSize > 0 && uint32(len(frame)) > caps.MaxMessageSize {
			return false
		}
	}
	return p.data.Send(frame) == nil
}

// TryWrite is nonblocking result-path admission. The original deadline and
// process gate are checked again by the physical writer; a slow direct reader
// never occupies the shared result/approval workers.
func (p *Path) TryWrite(raw []byte, deadline time.Time) bool {
	ctx, cancel := context.WithDeadline(p.ctx, deadline)
	frozen, release, err := p.reserveWrite(raw)
	if err != nil {
		cancel()
		return false
	}
	go func() { defer cancel(); defer release(); _ = p.write(ctx, frozen) }()
	return true
}
func (p *Path) write(ctx context.Context, raw []byte) error {
	count := (len(raw) + fragmentPayload - 1) / fragmentPayload
	// One application write per PC. Reservations above include all waiting
	// callers, so waiting for this lock cannot hide an unbounded send queue.
	select {
	case p.sendMu <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-p.ctx.Done():
		return ErrClosed
	}
	defer func() { <-p.sendMu }()
	var id [16]byte
	_, _ = rand.Read(id[:])
	hash := sha256.Sum256(raw)
	for i := 0; i < count; i++ {
		for p.data.ReadyState() != webrtc.DataChannelStateOpen || p.data.BufferedAmount() > 64<<10 {
			if !p.businessReady() {
				return ErrClosed
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-p.ctx.Done():
				return ErrClosed
			case <-p.ready:
			case <-p.low:
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !p.businessReady() {
			return ErrClosed
		}
		frame, err := proto.Marshal(&pb.Fragment{MessageId: id[:], Index: uint32(i), Count: uint32(count), CiphertextLength: uint32(len(raw)), CiphertextSha256: hash[:], PathGeneration: p.cfg.Generation, Fragment: raw[i*fragmentPayload : min(len(raw), (i+1)*fragmentPayload)]})
		if err != nil {
			return err
		}
		if len(frame) > maxFrame || uint32(len(frame)) > p.pc.SCTP().GetCapabilities().MaxMessageSize {
			return ErrLimit
		}
		if err = p.data.Send(frame); err != nil {
			return err
		}
	}
	return nil
}
func (p *Path) receive(m webrtc.DataChannelMessage) {
	if !p.valid() || m.IsString || len(m.Data) > maxFrame {
		return
	}
	if _, err := peerwire.NormalizeMessage(m.Data, "Fragment"); err != nil {
		return
	}
	f := &pb.Fragment{}
	if proto.Unmarshal(m.Data, f) != nil || len(f.ProtoReflect().GetUnknown()) > 0 || len(f.MessageId) != 16 || len(f.CiphertextSha256) != 32 || f.PathGeneration != p.cfg.Generation || f.Count == 0 || f.Count > 128 || f.Index >= f.Count || f.CiphertextLength == 0 || f.CiphertextLength > 1<<20 || len(f.Fragment) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ctx.Err() != nil {
		return
	}
	key := string(f.MessageId)
	part := p.pending[key]
	if part == nil {
		part = &partial{count: f.Count, length: f.CiphertextLength, hash: f.CiphertextSha256, parts: map[uint32][]byte{}, deadline: time.Now().Add(5 * time.Second)}
	}
	if part.count != f.Count || part.length != f.CiphertextLength || !bytes.Equal(part.hash, f.CiphertextSha256) {
		return
	}
	if old, ok := part.parts[f.Index]; ok {
		if !bytes.Equal(old, f.Fragment) {
			p.diag.why("fragment_conflict")
			p.cancel()
		}
		return
	}
	if part.held+len(f.Fragment) > int(part.length) || !p.cfg.Pool.reserve(p.cfg.Peer, 1, len(f.Fragment), 1) {
		return
	}
	part.parts[f.Index] = f.Fragment
	part.held += len(f.Fragment)
	part.frames++
	p.pending[key] = part
	if len(part.parts) != int(part.count) {
		return
	}
	if part.held != int(part.length) || !p.cfg.Pool.reserve(p.cfg.Peer, 1, part.held, 1) {
		p.drop(key, part)
		return
	}
	raw := make([]byte, 0, part.held)
	for i := uint32(0); i < part.count; i++ {
		raw = append(raw, part.parts[i]...)
	}
	hash := sha256.Sum256(raw)
	valid := bytes.Equal(hash[:], part.hash)
	p.drop(key, part)
	if valid {
		select {
		case p.received <- message{raw}:
			return
		default:
		}
	}
	p.cfg.Pool.free(p.cfg.Peer, 1, len(raw), 1)
}
func (p *Path) drop(key string, part *partial) {
	delete(p.pending, key)
	p.cfg.Pool.free(p.cfg.Peer, 1, part.held, part.frames)
}
func (p *Path) expire(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, part := range p.pending {
		if !now.Before(part.deadline) {
			p.drop(key, part)
		}
	}
}
func (p *Path) Close() {
	p.once.Do(func() {
		p.cancel()
		p.diag.end(p.cfg.Use, p.cfg.Generation, p.cfg.Peer)
		if p.cfg.Ended != nil {
			p.cfg.Ended()
		}
		p.mu.Lock()
		for key, part := range p.pending {
			p.drop(key, part)
		}
		// Read competes for these messages without holding p.mu. The last
		// message can disappear at any moment; never wait for another one.
	drain:
		for {
			select {
			case m := <-p.received:
				p.cfg.Pool.free(p.cfg.Peer, 1, len(m.data), 1)
			default:
				break drain
			}
		}
		p.mu.Unlock()
		// Pion callbacks can call Close. Do not wait for that callback from inside
		// PeerConnection.Close; the reservation stays held until SDK shutdown ends.
		go func() {
			_ = p.pc.Close()
			p.writers.Wait()
			p.cfg.Pool.release(p.cfg.Peer, p.cfg.Use)
			close(p.closed)
		}()
	})
}
func (p *Path) Finish() { p.Close() }

func (p *Path) Wait() { <-p.closed }
