// Standalone terminal design model. It reuses the existing business envelope;
// the tmux adapter and the two paths are fault-injectable in-memory doubles.
package main

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"example.com/transport-sdk-lab/labwire"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"google.golang.org/protobuf/proto"
)

const base = uint64(1700000001000)

func must(e error) {
	if e != nil {
		panic(e)
	}
}
func check(ok bool, label string) {
	if !ok {
		panic(label)
	}
}
func digest(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }

type Security struct {
	state                   deviceauth.State
	lease                   deviceauth.LeaseCertificate
	secret, seed            []byte
	client, machine         deviceauth.Member
	clientEd, machineEd     ed25519.PrivateKey
	clientWire, machineWire labwire.Identity
}

func security() *Security {
	s := &Security{secret: bytes.Repeat([]byte{0x55}, 32), seed: bytes.Repeat([]byte{0x66}, 32)}
	makeMember := func(seed byte, kind deviceauth.Kind) (deviceauth.Member, ed25519.PrivateKey, *ecdh.PrivateKey) {
		ed := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, 32))
		x, e := ecdh.X25519().NewPrivateKey(bytes.Repeat([]byte{seed + 1}, 32))
		must(e)
		m, e := deviceauth.NewMember(ed.Public().(ed25519.PublicKey), x.PublicKey().Bytes(), kind)
		must(e)
		return m, ed, x
	}
	var cx, mx *ecdh.PrivateKey
	s.client, s.clientEd, cx = makeMember(0x11, deviceauth.Client)
	s.machine, s.machineEd, mx = makeMember(0x44, deviceauth.Machine)
	domain := deviceauth.Hash(sha256.Sum256([]byte("lab/terminal-domain")))
	recovery := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x22}, 32))
	var recoveryPublic [32]byte
	copy(recoveryPublic[:], recovery.Public().(ed25519.PublicKey))
	old := bytes.Repeat([]byte{0x33}, 32)
	g, e := deviceauth.BuildGenesis("0123456789abcdef0123456789abcdef", domain, s.client, recoveryPublic, old, s.clientEd, base-10)
	must(e)
	s.state, e = deviceauth.BuildMembershipState(g, []deviceauth.Member{s.client, s.machine}, nil, old, s.secret, s.clientEd, base-1)
	must(e)
	must(deviceauth.VerifyTransition(g, s.state, nil))
	s.clientWire = labwire.Identity{ID: []byte(s.client.ID), Instance: bytes.Repeat([]byte{1}, 16), Boot: bytes.Repeat([]byte{2}, 32), Role: 1, Sign: s.clientEd, Box: cx}
	s.lease, e = deviceauth.BuildLease(s.state, s.clientEd, base)
	must(e)
	s.machineWire = labwire.Identity{ID: []byte(s.machine.ID), Instance: bytes.Repeat([]byte{3}, 16), Boot: bytes.Repeat([]byte{4}, 32), Role: 2, Sign: s.machineEd, Box: mx}
	head, e := s.state.Hash()
	must(e)
	labwire.Account = []byte(s.state.Core.Account)
	labwire.AccountDomain = s.state.Core.Domain[:]
	labwire.Head = head[:]
	labwire.MemberEpoch = s.state.Core.Epoch
	return s
}

type Envelope struct {
	seq, deadline uint64
	raw, plain    []byte
	header        deviceauth.Header
	identity      string
	purpose       deviceauth.Purpose
	bytes         int
}

func (s *Security) seal(stream string, seq, now uint64, purpose deviceauth.Purpose, data []byte) Envelope {
	message := &pb.TerminalFrame{}
	switch purpose {
	case deviceauth.TerminalInput:
		message.Kind = &pb.TerminalFrame_Input{Input: data}
	case deviceauth.TerminalResize:
		message.Kind = &pb.TerminalFrame_Resize{Resize: &pb.TerminalResize{Cols: 80, Rows: 24, Fit: true}}
	case deviceauth.TerminalHistory:
		message.Kind = &pb.TerminalFrame_HistoryRequest{HistoryRequest: &pb.TerminalHistoryRequest{}}
	case deviceauth.TerminalOutput:
		message.Kind = &pb.TerminalFrame_Output{Output: data}
	default:
		panic("purpose")
	}
	raw, e := proto.Marshal(message)
	must(e)
	head, e := s.state.Hash()
	must(e)
	sender, writer := s.client.ID, s.clientEd
	if purpose == deviceauth.TerminalOutput {
		sender, writer = s.machine.ID, s.machineEd
	}
	kind := uint32(purpose)
	if purpose == deviceauth.TerminalResize || purpose == deviceauth.TerminalHistory {
		kind = 8
	}
	h := deviceauth.Header{Account: s.state.Core.Account, Domain: s.state.Core.Domain, Epoch: s.state.Core.Epoch, State: head, ContentEpoch: s.state.Core.ContentEpoch, Sender: sender, Kind: kind, Purpose: purpose, Machine: s.machine.ID, Session: "session-lab-00001", Request: stream, Seq: seq, Deadline: now + 60000}
	c, e := deviceauth.Seal(s.secret, s.seed, h, raw, writer)
	must(e)
	encoded, e := c.MarshalBinary()
	must(e)
	_, identity, e := deviceauth.RequestIdentity(h, raw)
	must(e)
	return Envelope{seq, h.Deadline, encoded, raw, h, identity.String(), purpose, len(data)}
}
func (s *Security) open(raw []byte, stream string, now uint64) (deviceauth.Header, *pb.TerminalFrame, error) {
	if e := s.lease.Valid(s.state, now); e != nil {
		return deviceauth.Header{}, nil, e
	}
	w, e := deviceauth.ParseWire(raw)
	if e != nil {
		return deviceauth.Header{}, nil, e
	}
	c := deviceauth.Ciphertext(w)
	h, plain, e := c.Open(s.secret, s.seed, s.state, s.state, true, now)
	if e != nil {
		return h, nil, e
	}
	if h.Request != stream || h.Machine != s.machine.ID || h.Session != "session-lab-00001" || now > h.Deadline {
		return h, nil, errors.New("scope/expiry")
	}
	expected := s.client.ID
	if h.Purpose == deviceauth.TerminalOutput {
		expected = s.machine.ID
	}
	if h.Sender != expected {
		return h, nil, errors.New("sender")
	}
	m := &pb.TerminalFrame{}
	e = proto.Unmarshal(plain, m)
	return h, m, e
}

type Fragment struct {
	id, hash            string
	index, count, total int
	pathGen             uint64
	body                []byte
}

func fragments(e Envelope, gen uint64) []Fragment {
	const size = 16*1024 - 256
	n := (len(e.raw) + size - 1) / size
	var out []Fragment
	for i := 0; i < n; i++ {
		end := (i + 1) * size
		if end > len(e.raw) {
			end = len(e.raw)
		}
		out = append(out, Fragment{digest(e.raw)[:32], digest(e.raw), i, n, len(e.raw), gen, append([]byte(nil), e.raw[i*size:end]...)})
	}
	return out
}

type Assembly struct {
	count, total int
	hash         string
	parts        map[int][]byte
	started      uint64
}
type Detail struct {
	Seq      uint64 `json:"seq"`
	Hash     string `json:"authorized_payload_hash"`
	Outcome  string `json:"outcome"`
	Known    int    `json:"known_prefix"`
	Possible int    `json:"possible_prefix"`
	Total    int    `json:"input_bytes"`
}
type Status struct {
	Available                       bool `json:"available"`
	Final                           bool `json:"final"`
	Processed, InputSuccess, Failed uint64
	Details                         []Detail
}
type queued struct {
	header   deviceauth.Header
	body     *pb.TerminalFrame
	hash     string
	rawBytes int
}
type Stream struct {
	sec                                              *Security
	id, instance                                     string
	now                                              uint64
	legs                                             map[string]uint64
	assemblies                                       map[string]*Assembly
	assemblyBytes                                    int
	processed, inputSuccess, failed                  uint64
	details                                          map[uint64]Detail
	pending                                          map[uint64]queued
	pendingBytes                                     int
	gap                                              uint64
	accepted                                         []uint64
	tmuxConnections, openCalls, injectedChunks, acks int
	lastACK                                          uint64
	dirtyACK                                         int
	failInput, failResize, hold                      bool
	inflight                                         *queued
	waitingStatus                                    []func(Status)
	barrierWaiting                                   bool
	barrierReply                                     func(Status)
	stopped, networkRecovery, paused                 bool
	rendered                                         uint64
	output                                           map[uint64]queued
	renderReady                                      *queued
	replay                                           []Envelope
	replayAt                                         []uint64
	replayBytes                                      int
	controlSeq                                       uint64
	controlPackets, controlBytes                     int
	inputCache                                       map[uint64]Envelope
	inputAt                                          map[uint64]uint64
	inputCacheBytes                                  int
	retiredAt                                        uint64
}

func newStream(sec *Security, id string) *Stream {
	return &Stream{sec: sec, id: id, instance: "daemon-instance-a", now: base, legs: map[string]uint64{"relay": 1, "direct": 1}, assemblies: map[string]*Assembly{}, details: map[uint64]Detail{}, pending: map[uint64]queued{}, output: map[uint64]queued{}, inputCache: map[uint64]Envelope{}, inputAt: map[uint64]uint64{}, tmuxConnections: 1, openCalls: 1}
}
func (s *Stream) receive(path, instance string, f Fragment) error {
	if s.legs[path] != f.pathGen || instance != s.instance || s.stopped {
		return errors.New("stale path/instance")
	}
	if f.count <= 0 || f.count > 128 || f.index < 0 || f.index >= f.count || f.total <= 0 || f.total > 1<<20 || len(f.body) > 16*1024 {
		return errors.New("fragment bounds")
	}
	key := fmt.Sprintf("%s/%d/%s", path, f.pathGen, f.id)
	a := s.assemblies[key]
	if a == nil {
		if len(s.assemblies) >= 128 || s.assemblyBytes+f.total > 2<<20 {
			return errors.New("reassembly budget")
		}
		a = &Assembly{f.count, f.total, f.hash, map[int][]byte{}, s.now}
		s.assemblies[key] = a
		s.assemblyBytes += f.total
	}
	if a.count != f.count || a.total != f.total || a.hash != f.hash {
		return errors.New("fragment identity")
	}
	if prev, ok := a.parts[f.index]; ok {
		if !bytes.Equal(prev, f.body) {
			return errors.New("fragment conflict")
		}
		return nil
	}
	a.parts[f.index] = f.body
	if len(a.parts) != a.count {
		return nil
	}
	var raw []byte
	for i := 0; i < a.count; i++ {
		raw = append(raw, a.parts[i]...)
	}
	delete(s.assemblies, key)
	s.assemblyBytes -= a.total
	if len(raw) != a.total || digest(raw) != a.hash {
		return errors.New("reassembly hash")
	}
	h, m, e := s.sec.open(raw, s.id, s.now)
	if e != nil {
		return e
	}
	w, e := deviceauth.ParseWire(raw)
	must(e)
	_, plain, e := deviceauth.Ciphertext(w).Open(s.sec.secret, s.sec.seed, s.sec.state, s.sec.state, true, s.now)
	must(e)
	_, identity, e := deviceauth.RequestIdentity(h, plain)
	must(e)
	q := queued{h, m, identity.String(), len(raw)}
	if h.Purpose == deviceauth.TerminalOutput {
		return s.outputFrame(q)
	}
	if old, ok := s.details[h.Seq]; ok {
		if old.Hash != q.hash {
			return errors.New("logical conflict")
		}
		s.acks++
		return nil
	}
	if p, ok := s.pending[h.Seq]; ok {
		if p.hash != q.hash {
			return errors.New("pending conflict")
		}
		return nil
	}
	if s.inflight != nil && s.inflight.header.Seq == h.Seq {
		if s.inflight.hash != q.hash {
			return errors.New("inflight conflict")
		}
		return nil
	}
	if h.Seq != s.processed+1 || s.inflight != nil {
		if len(s.pending) >= 128 || s.pendingBytes+q.rawBytes > 1<<20 {
			return errors.New("gap budget")
		}
		s.pending[h.Seq] = q
		s.pendingBytes += q.rawBytes
		if s.gap == 0 {
			s.gap = s.now + 10000
		}
		return nil
	}
	return s.apply(q)
}
func (s *Stream) apply(q queued) error {
	if s.now > q.header.Deadline {
		s.networkRecovery = true
		return errors.New("expired gap")
	}
	if len(s.details) >= 128 {
		return errors.New("input detail backpressure")
	}
	// This is the first point at which Accept/backend may be called.
	s.accepted = append(s.accepted, q.header.Seq)
	if s.hold && q.header.Purpose == deviceauth.TerminalInput {
		s.inflight = &q
		return nil
	}
	s.finish(q)
	return nil
}
func (s *Stream) finish(q queued) {
	d := Detail{Seq: q.header.Seq, Hash: q.hash, Outcome: "applied", Total: len(q.body.GetInput())}
	switch q.header.Purpose {
	case deviceauth.TerminalInput:
		if s.failed != 0 {
			d.Outcome = "blocked"
		} else if s.failInput {
			n := len(q.body.GetInput())
			d.Outcome = "partial_or_unknown"
			d.Known = min(n, 256)
			d.Possible = min(n, 512)
			s.injectedChunks++
			s.failed = q.header.Seq
			s.acks++
		} else {
			s.injectedChunks += (len(q.body.GetInput()) + 255) / 256
			s.inputSuccess = q.header.Seq
			d.Known = len(q.body.GetInput())
			d.Possible = d.Known
		}
	case deviceauth.TerminalResize, deviceauth.TerminalHistory:
		if s.failResize {
			d.Outcome = "non_input_failed"
		}
	}
	s.processed = q.header.Seq
	s.details[q.header.Seq] = d
	s.dirtyACK++
	if s.dirtyACK >= 16 {
		s.emitACK()
	}
	if s.inflight != nil {
		s.inflight = nil
	}
	for _, reply := range s.waitingStatus {
		reply(s.status(false))
	}
	s.waitingStatus = nil
	if s.barrierWaiting {
		s.barrierWaiting = false
		s.stopped = true
		s.retiredAt = s.now
		if s.barrierReply != nil {
			s.barrierReply(s.status(true))
			s.barrierReply = nil
		}
		return
	}
	if next, ok := s.pending[s.processed+1]; ok {
		delete(s.pending, s.processed+1)
		s.pendingBytes -= next.rawBytes
		_ = s.apply(next)
	}
	if len(s.pending) == 0 {
		s.gap = 0
	}
}
func (s *Stream) emitACK() {
	if s.dirtyACK > 0 {
		s.acks++
		s.lastACK = s.now
		s.dirtyACK = 0
	}
}
func (s *Stream) release() {
	check(s.inflight != nil, "no inflight")
	q := *s.inflight
	s.hold = false
	s.finish(q)
}
func (s *Stream) status(final bool) Status {
	r := Status{Available: true, Final: final, Processed: s.processed, InputSuccess: s.inputSuccess, Failed: s.failed}
	for _, v := range s.details {
		r.Details = append(r.Details, v)
	}
	sort.Slice(r.Details, func(i, j int) bool { return r.Details[i].Seq < r.Details[j].Seq })
	return r
}
func (s *Stream) query(reply func(Status)) {
	if s.inflight != nil {
		s.waitingStatus = append(s.waitingStatus, reply)
	} else {
		reply(s.status(false))
	}
}
func (s *Stream) barrier() Status {
	if s.inflight != nil {
		s.barrierWaiting = true
		return Status{}
	}
	s.stopped = true
	s.retiredAt = s.now
	return s.status(true)
}
func (s *Stream) attach(path, instance string, gen uint64) error {
	if instance != s.instance || s.stopped || gen <= s.legs[path] {
		return errors.New("attach binding")
	}
	s.legs[path] = gen
	return nil
}
func (s *Stream) closeLeg(path string, gen uint64) {
	if s.legs[path] == gen {
		delete(s.legs, path)
	}
	if len(s.legs) == 0 {
		s.networkRecovery = true
	}
}
func (s *Stream) outputFrame(q queued) error {
	if q.header.Seq <= s.rendered {
		return nil
	}
	if _, ok := s.output[q.header.Seq]; ok {
		return nil
	}
	if len(s.output) >= 128 {
		return errors.New("output gap")
	}
	s.output[q.header.Seq] = q
	if s.renderReady == nil {
		if next, ok := s.output[s.rendered+1]; ok {
			copy := next
			s.renderReady = &copy
		}
	}
	return nil
}
func (s *Stream) render() {
	check(s.renderReady != nil, "no render ready")
	s.rendered = s.renderReady.header.Seq
	delete(s.output, s.rendered)
	s.renderReady = nil
	s.dirtyACK++
	if s.dirtyACK >= 16 {
		s.emitACK()
	}
	if next, ok := s.output[s.rendered+1]; ok {
		copy := next
		s.renderReady = &copy
	}
}
func (s *Stream) rememberOutput(e Envelope) {
	s.replay = append(s.replay, e)
	s.replayAt = append(s.replayAt, s.now)
	s.replayBytes += len(e.raw)
	s.pruneReplay()
}
func (s *Stream) pruneReplay() {
	for len(s.replay) > 0 && (len(s.replay) > 512 || s.replayBytes > 2<<20 || s.now-s.replayAt[0] >= 30000) {
		s.replayBytes -= len(s.replay[0].raw)
		s.replay = s.replay[1:]
		s.replayAt = s.replayAt[1:]
	}
}
func (s *Stream) replayFrom(seq uint64) ([]Envelope, error) {
	s.pruneReplay()
	if len(s.replay) == 0 || seq < s.replay[0].seq {
		s.networkRecovery = true
		return nil, errors.New("output window")
	}
	var out []Envelope
	for _, e := range s.replay {
		if e.seq >= seq {
			out = append(out, e)
		}
	}
	return out, nil
}
func (s *Stream) cacheInput(e Envelope) error {
	charge := len(e.raw) + len(e.plain) + 128
	if s.paused || s.inputCacheBytes+charge > 64<<10 || len(s.inputCache) >= 128 {
		s.paused = true
		return errors.New("input cache backpressure")
	}
	s.inputCache[e.seq] = e
	s.inputAt[e.seq] = s.now
	s.inputCacheBytes += charge
	return nil
}
func (s *Stream) acknowledge(st Status) {
	for seq, e := range s.inputCache {
		for _, d := range st.Details {
			if seq == d.Seq && d.Hash == e.identity && (d.Outcome == "applied" || d.Outcome == "non_input_failed") {
				s.inputCacheBytes -= len(e.raw) + len(e.plain) + 128
				delete(s.inputCache, seq)
				delete(s.inputAt, seq)
			}
		}
	}
}
func (s *Stream) advance(ms uint64) {
	check(base+ms >= s.now, "clock rollback")
	s.now = base + ms
	for key, a := range s.assemblies {
		if s.now-a.started >= 5000 {
			s.assemblyBytes -= a.total
			delete(s.assemblies, key)
		}
	}
	if s.gap != 0 && s.now >= s.gap {
		s.networkRecovery = true
	}
	for seq, e := range s.inputCache {
		if s.now-s.inputAt[seq] >= 30000 || s.now > e.deadline {
			s.paused = true
		}
	}
	if s.now-s.lastACK >= 20 {
		s.emitACK()
	}
	s.pruneReplay()
}
func send(s *Stream, path string, e Envelope, reverse bool) error {
	f := fragments(e, s.legs[path])
	if reverse {
		for i, j := 0, len(f)-1; i < j; i, j = i+1, j-1 {
			f[i], f[j] = f[j], f[i]
		}
	}
	for _, part := range f {
		if err := s.receive(path, s.instance, part); err != nil {
			return err
		}
	}
	return nil
}

// Controls cross the same two opaque fake paths using the real draft codec.
// The direct method is invoked only after signature, decryption and binding.
func (s *Stream) control(path string, typ uint64, body []byte, apply func([]byte) error) error {
	if path != "relay" && path != "direct" {
		return errors.New("control path")
	}
	if e := s.sec.lease.Valid(s.sec.state, s.now); e != nil {
		return e
	}
	s.controlSeq++
	conn := labwire.Hash([]byte(s.id))[:16]
	c := labwire.NewContext(s.sec.clientWire, s.sec.machineWire.Public(), conn, 0, 1, s.controlSeq, s.now, 2, false)
	packet, e := labwire.Seal(s.sec.clientWire, s.sec.machineWire.Public(), c, typ, body, nil, nil, nil)
	if e != nil {
		return e
	}
	check(!bytes.Contains(packet, body) || len(body) < 16, "control plaintext on path")
	s.controlPackets++
	s.controlBytes += len(packet)
	opened, e := labwire.Open(packet, s.sec.clientWire.Public(), s.sec.machineWire, s.now)
	if e != nil {
		return e
	}
	if opened.Context.Class != 2 || opened.Type != typ || !bytes.Equal(opened.Context.Connection, conn) {
		return errors.New("control binding")
	}
	return apply(opened.Payload)
}
func (s *Stream) reseal(e Envelope) Envelope {
	h := e.header
	head, err := s.sec.state.Hash()
	must(err)
	h.Epoch = s.sec.state.Core.Epoch
	h.ContentEpoch = s.sec.state.Core.ContentEpoch
	h.State = head
	c, err := deviceauth.Seal(s.sec.secret, s.sec.seed, h, e.plain, s.sec.clientEd)
	must(err)
	raw, err := c.MarshalBinary()
	must(err)
	_, identity, err := deviceauth.RequestIdentity(h, e.plain)
	must(err)
	check(identity.String() == e.identity, "rekey changed logical identity")
	e.raw = raw
	e.header = h
	return e
}

func main() {
	check(strings.HasPrefix(os.Getenv("REMOTE_AGENT_HOME"), "/tmp/"), "isolation")
	sec := security()
	results := map[string]any{}
	s := newStream(sec, "stream-lab-000001")
	one := sec.seal(s.id, 1, s.now, deviceauth.TerminalInput, []byte("first"))
	two := sec.seal(s.id, 2, s.now, deviceauth.TerminalInput, []byte("second"))
	must(send(s, "direct", two, true))
	check(len(s.accepted) == 0, "Accept before missing seq")
	must(send(s, "relay", one, false))
	must(send(s, "direct", one, false))
	check(s.processed == 2 && s.injectedChunks == 2 && len(s.accepted) == 2, "duplicate input")
	results["reorder_and_duplicate"] = s.status(false)
	conflict := sec.seal(s.id, 1, s.now, deviceauth.TerminalInput, []byte("different"))
	check(send(s, "relay", conflict, false) != nil && s.injectedChunks == 2, "conflict")
	results["conflict_no_inject"] = true
	s = newStream(sec, "stream-lab-000002")
	s.failResize = true
	resize := sec.seal(s.id, 1, s.now, deviceauth.TerminalResize, nil)
	input := sec.seal(s.id, 2, s.now, deviceauth.TerminalInput, []byte("after resize"))
	must(send(s, "relay", resize, false))
	must(send(s, "direct", input, false))
	check(s.failed == 0 && s.inputSuccess == 2 && s.details[1].Outcome == "non_input_failed", "resize fail stop")
	results["non_input_failure"] = s.status(false)
	s = newStream(sec, "stream-lab-000003")
	s.failInput = true
	partial := sec.seal(s.id, 1, s.now, deviceauth.TerminalInput, bytes.Repeat([]byte{'x'}, 600))
	must(send(s, "relay", partial, true))
	must(send(s, "direct", partial, false))
	must(send(s, "direct", sec.seal(s.id, 2, s.now, deviceauth.TerminalInput, []byte("blocked")), false))
	check(s.failed == 1 && s.processed == 2 && s.inputSuccess == 0 && s.injectedChunks == 1 && s.details[2].Outcome == "blocked", "partial input")
	results["partial_fail_stop"] = s.status(false)
	s = newStream(sec, "stream-lab-000004")
	s.hold = true
	must(send(s, "relay", sec.seal(s.id, 1, s.now, deviceauth.TerminalInput, []byte("held")), false))
	var observed *Status
	s.query(func(v Status) { observed = &v })
	early := s.barrier()
	check(observed == nil && !early.Final && !s.stopped, "barrier overtook injection")
	s.release()
	check(s.stopped && observed != nil && observed.Processed == 1, "serial status/barrier")
	results["status_and_barrier_serial"] = s.status(true)
	s = newStream(sec, "stream-lab-000005")
	must(s.attach("relay", s.instance, 2))
	s.closeLeg("relay", 1)
	check(s.legs["relay"] == 2 && s.tmuxConnections == 1 && s.openCalls == 1, "Attach opened tmux or stale close")
	s.closeLeg("relay", 2)
	s.closeLeg("direct", 1)
	check(s.networkRecovery && s.failed == 0, "network fail stop")
	results["attach_no_new_tmux"] = true
	s = newStream(sec, "stream-lab-000006")
	big := sec.seal(s.id, 1, s.now, deviceauth.TerminalOutput, bytes.Repeat([]byte{'o'}, 64<<10))
	must(send(s, "direct", big, true))
	check(s.rendered == 0 && s.acks == 0, "output ACK before render")
	s.render()
	s.advance(20)
	check(s.rendered == 1 && s.acks == 1, "render ACK")
	results["output_reassembly_render_ack"] = true
	s = newStream(sec, "stream-lab-000007")
	f := fragments(big, 1)[0]
	must(s.receive("direct", s.instance, f))
	s.advance(5000)
	check(len(s.assemblies) == 0 && s.assemblyBytes == 0 && s.processed == 0, "reassembly expiry")
	results["fragment_expiry"] = true
	s = newStream(sec, "stream-lab-000008")
	gap := sec.seal(s.id, 2, s.now, deviceauth.TerminalInput, []byte("gap"))
	must(send(s, "relay", gap, false))
	s.advance(10000)
	check(s.networkRecovery && s.failed == 0 && len(s.accepted) == 0, "gap caused fail stop")
	results["gap_network_recovery"] = true
	s = newStream(sec, "stream-lab-000009")
	pending := sec.seal(s.id, 1, s.now, deviceauth.TerminalInput, bytes.Repeat([]byte{'u'}, 1024))
	must(s.cacheInput(pending))
	s.rememberOutput(sec.seal(s.id, 1, s.now, deviceauth.TerminalOutput, []byte("old output")))
	s.advance(30000)
	_, err := s.replayFrom(1)
	check(err != nil && s.failed == 0 && len(s.inputCache) == 1 && s.paused, "output eviction lost input evidence")
	results["separate_replay_and_input_audit"] = true
	s = newStream(sec, "stream-lab-000010")
	for n := uint64(1); n < 100; n++ {
		e := sec.seal(s.id, n, s.now, deviceauth.TerminalInput, bytes.Repeat([]byte{'b'}, 2048))
		if s.cacheInput(e) != nil {
			break
		}
	}
	check(s.paused && s.inputCacheBytes <= 64<<10, "input budget")
	results["input_cache_backpressure"] = map[string]int{"bytes": s.inputCacheBytes, "frames": len(s.inputCache)}
	s = newStream(sec, "stream-lab-000011")
	e := sec.seal(s.id, 1, s.now, deviceauth.TerminalInput, []byte("retired"))
	s.instance = "daemon-instance-b"
	old := fragments(e, 1)[0]
	check(s.receive("relay", "daemon-instance-a", old) != nil && s.injectedChunks == 0, "old instance applied")
	s.paused = true
	results["instance_change_requires_confirmation"] = true
	s = newStream(sec, "stream-lab-000012")
	tampered := sec.seal(s.id, 1, s.now, deviceauth.TerminalInput, []byte("tamper"))
	tampered.raw[len(tampered.raw)-1] ^= 1
	check(send(s, "direct", tampered, false) != nil && s.processed == 0 && len(s.accepted) == 0, "unauthenticated watermark")
	results["authentication_before_accept"] = true
	s = newStream(sec, "stream-lab-000013")
	original := sec.seal(s.id, 1, s.now, deviceauth.TerminalInput, []byte("rekey once"))
	must(send(s, "relay", original, false))
	oldState := sec.state
	nextSecret := bytes.Repeat([]byte{0x77}, 32)
	extraEd := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x88}, 32))
	extraX, _ := ecdh.X25519().NewPrivateKey(bytes.Repeat([]byte{0x89}, 32))
	extraMember, makeErr := deviceauth.NewMember(extraEd.Public().(ed25519.PublicKey), extraX.PublicKey().Bytes(), deviceauth.Client)
	must(makeErr)
	members := append(append([]deviceauth.Member(nil), sec.state.Core.Members...), extraMember)
	sec.state, err = deviceauth.BuildMembershipState(sec.state, members, nil, sec.secret, nextSecret, sec.clientEd, s.now)
	must(err)
	must(deviceauth.VerifyTransition(oldState, sec.state, nil))
	sec.secret = nextSecret
	sec.lease, err = deviceauth.BuildLease(sec.state, sec.clientEd, s.now)
	must(err)
	newHead, err := sec.state.Hash()
	must(err)
	labwire.Head = newHead[:]
	labwire.MemberEpoch = sec.state.Core.Epoch
	resealed := s.reseal(original)
	check(!bytes.Equal(resealed.raw, original.raw) && bytes.Equal(resealed.plain, original.plain), "rekey did not preserve raw business bytes")
	must(s.attach("relay", s.instance, 2))
	must(send(s, "relay", resealed, false))
	check(s.injectedChunks == 1 && s.processed == 1, "rekey repeated input")
	results["rekey_same_authorized_hash"] = true
	s = newStream(sec, "stream-lab-000014")
	s.hold = true
	held := sec.seal(s.id, 1, s.now, deviceauth.TerminalInput, []byte("network cannot cancel injection"))
	must(send(s, "relay", held, false))
	s.closeLeg("relay", 1)
	s.closeLeg("direct", 1)
	check(s.inflight != nil && s.tmuxConnections == 1, "path canceled dispatcher")
	s.release()
	check(s.failed == 0 && s.inputSuccess == 1, "network made input unknown")
	results["physical_loss_preserves_execution"] = true
	s = newStream(sec, "stream-lab-000015")
	e = sec.seal(s.id, 1, s.now, deviceauth.TerminalInput, []byte("status on new relay"))
	must(s.cacheInput(e))
	must(send(s, "direct", e, false))
	var status Status
	body := labwire.PB(labwire.B(1, []byte(s.id)), labwire.B(2, bytes.Repeat([]byte{8}, 16)), labwire.U(3, 1), labwire.U(4, 128), labwire.B(5, nil))
	must(s.control("relay", 3, body, func(raw []byte) error {
		fields, err := labwire.Parse(raw, 5)
		if err != nil || !bytes.Equal(fields[1].B, []byte(s.id)) {
			return errors.New("status scope")
		}
		s.query(func(v Status) { status = v })
		return nil
	}))
	s.acknowledge(status)
	check(status.Processed == 1 && len(s.inputCache) == 0, "InputStatus did not settle original input")
	must(s.control("direct", 6, labwire.PB(labwire.B(1, []byte(s.id)), labwire.B(2, bytes.Repeat([]byte{9}, 16)), labwire.U(4, 1)), func(raw []byte) error {
		fields, err := labwire.Parse(raw, 3)
		if err != nil || !bytes.Equal(fields[1].B, []byte(s.id)) {
			return errors.New("barrier scope")
		}
		status = s.barrier()
		return nil
	}))
	check(status.Final && s.stopped, "authenticated barrier")
	check(send(s, "relay", sec.seal(s.id, 2, s.now, deviceauth.TerminalInput, []byte("late")), false) != nil, "closed stream admitted late input")
	results["authenticated_controls"] = map[string]int{"packets": s.controlPackets, "bytes": s.controlBytes}
	recoveryCases(sec, results)
	encoded, err := json.MarshalIndent(map[string]any{"evidence": "two fake paths; real existing business envelope; fake tmux", "scenarios": results}, "", "  ")
	must(err)
	fmt.Println(string(encoded))
}
