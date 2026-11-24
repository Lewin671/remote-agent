package relay_test

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/transport/direct"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"github.com/Lewin671/remote-agent/shared/linkcore"
	"github.com/Lewin671/remote-agent/shared/peerwire"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

type phase3FakeTerminal struct {
	mu           sync.Mutex
	inputs       [][]byte
	resizes      int
	failResize   bool
	failInputAt  int // fail on Nth Input() call (1-based); 0 = never fail
	inputCallNum int
	outCh        chan []byte
	sizeCh       chan struct{}
	doneCh       chan struct{}
	closeOnce    sync.Once
}

func newPhase3FakeTerminal() *phase3FakeTerminal {
	return &phase3FakeTerminal{
		outCh:  make(chan []byte, 64),
		sizeCh: make(chan struct{}, 4),
		doneCh: make(chan struct{}),
	}
}

func (f *phase3FakeTerminal) Snapshot() (*pb.TerminalSnapshot, error) {
	return &pb.TerminalSnapshot{Cols: 80, Rows: 24}, nil
}
func (f *phase3FakeTerminal) History(*pb.TerminalHistoryRequest) (*pb.TerminalHistoryPage, error) {
	return &pb.TerminalHistoryPage{RequestId: "h1"}, nil
}
func (f *phase3FakeTerminal) Output() <-chan []byte       { return f.outCh }
func (f *phase3FakeTerminal) SizeChanged() <-chan struct{} { return f.sizeCh }
func (f *phase3FakeTerminal) Done() <-chan struct{}        { return f.doneCh }
func (f *phase3FakeTerminal) Reason() string               { return "closed" }
func (f *phase3FakeTerminal) Input(b []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputCallNum++
	if f.failInputAt > 0 && f.inputCallNum == f.failInputAt {
		return errors.New("injected tmux write failure")
	}
	f.inputs = append(f.inputs, append([]byte(nil), b...))
	return nil
}
func (f *phase3FakeTerminal) Resize(cols, rows int, fit bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resizes++
	if f.failResize {
		return errors.New("injected resize failure")
	}
	return nil
}
func (f *phase3FakeTerminal) Close() error {
	f.closeOnce.Do(func() { close(f.doneCh) })
	return nil
}
func (f *phase3FakeTerminal) allInputs() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []byte
	for _, b := range f.inputs {
		out = append(out, b...)
	}
	return out
}

type phase3TermBackend struct {
	*fakeBackend
	term          *phase3FakeTerminal
	openTermCalls atomic.Int32
}

func (b *phase3TermBackend) OpenTerminal(string) (relay.Terminal, error) {
	b.openTermCalls.Add(1)
	return b.term, nil
}

type phase3PionHarness struct {
	t         *testing.T
	e         *env
	backend   *phase3TermBackend
	ctx       context.Context
	cancel    context.CancelFunc
	link      *direct.Session
	readPath  *direct.Path
	ep        *deviceauth.Endpoint
	session   string
	sessKey   []byte
	controls  chan peerwire.Opened
	streamWS  *websocket.Conn
	workers   sync.WaitGroup
}

func startPhase3PionHarness(t *testing.T) *phase3PionHarness {
	t.Helper()
	e := setup(t)
	const session = "pion-phase3-session-01"
	e.backend.add(t, session)
	term := newPhase3FakeTerminal()
	backend := &phase3TermBackend{fakeBackend: e.backend, term: term}
	baseNow := time.Now()
	directNow := func() time.Time { return baseNow.Add(time.Since(baseNow) / 6) }
	running := e.startRelayBackend(backend, func(r *relay.Relay) {
		relay.SetDirectLoopbackForTest(r)
		relay.SetDirectClockForTest(r, directNow)
	})
	e.online(t, true)
	e.session(t, session, 0)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	for !relay.DirectFreshForTest(running) {
		select {
		case <-ctx.Done():
			t.Fatal("relay did not become direct-fresh")
		case <-time.After(10 * time.Millisecond):
		}
	}

	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.http.URL, "http")+"/v1/stream", &websocket.DialOptions{HTTPClient: http.DefaultClient})
	if err != nil {
		t.Fatal(err)
	}
	ws.SetReadLimit(1 << 20)
	hello, _ := proto.Marshal(&pb.PeerFrame{Kind: &pb.PeerFrame_Hello{Hello: &pb.StreamHello{Token: e.authority.Token}}})
	if err := ws.Write(ctx, websocket.MessageBinary, hello); err != nil {
		t.Fatal(err)
	}
	if _, raw, err := ws.Read(ctx); err != nil {
		t.Fatal(err)
	} else {
		f := &pb.ServerFrame{}
		if proto.Unmarshal(raw, f) != nil || f.GetReady() == nil {
			t.Fatal("expected Ready")
		}
	}

	authority := e.authority
	head := authority.States[len(authority.States)-1]
	hash, _ := head.Hash()
	codec, err := peerwire.NewCodec(authority.Pin.Account, authority.Pin.Domain, hash, head.Core.Epoch, protocol.PeerVersion)
	if err != nil {
		t.Fatal(err)
	}
	box, err := ecdh.X25519().NewPrivateKey(authority.X)
	if err != nil {
		t.Fatal(err)
	}
	member, _ := head.Core.Member(e.acct.DeviceID)
	peerBox, err := ecdh.X25519().NewPublicKey(member.XPublic[:])
	if err != nil {
		t.Fatal(err)
	}
	self := &direct.Identity{ID: []byte(authority.Member.ID), Role: 1, Sign: authority.Ed, Box: box, Instance: make([]byte, 16), Boot: make([]byte, 32)}
	_, _ = rand.Read(self.Instance)
	_, _ = rand.Read(self.Boot)
	peer := peerwire.Public{ID: []byte(member.ID), Role: 2, Sign: member.EdPublic[:], Box: peerBox.Bytes()}
	connection := make([]byte, 16)
	_, _ = rand.Read(connection)

	out := make(chan direct.Outbound, 128)
	ready := make(chan struct {
		use  uint64
		path *direct.Path
	}, 8)
	controls := make(chan peerwire.Opened, 128)
	link, err := direct.NewSession(ctx, direct.SessionConfig{
		Self: self, Peer: peer, Codec: codec, Connection: connection,
		Deadline: uint64(directNow().Add(9 * time.Second).UnixMilli()),
		Pool: direct.NewPool(4), Allow: func() bool { return true },
		LoopbackOnly: true, Now: directNow,
		Send: func(frame direct.Outbound) bool {
			select {
			case out <- frame:
				return true
			default:
				return false
			}
		},
		Control: func(o peerwire.Opened, _ uint64) (uint64, []byte) {
			select {
			case controls <- o:
			default:
			}
			if o.Type == linkcore.ControlCapabilities || o.Type == linkcore.ControlCapabilitySnapshot || o.Type == linkcore.ControlStreamFailed {
				return 4, peerwire.PB(peerwire.U(4, o.Context.Class), peerwire.U(5, o.Context.Seq), peerwire.B(6, o.Hash), peerwire.U(7, o.Context.Generation), peerwire.U(8, o.Context.Use))
			}
			return 0, nil
		},
		Ready: func(use uint64, path *direct.Path) {
			ready <- struct {
				use  uint64
				path *direct.Path
			}{use, path}
		},
	}, true)
	if err != nil {
		t.Fatal(err)
	}

	h := &phase3PionHarness{
		t:        t,
		e:        e,
		backend:  backend,
		ctx:      ctx,
		cancel:   cancel,
		link:     link,
		session:  session,
		controls: controls,
		streamWS: ws,
	}
	h.workers.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case frame := <-out:
				if !frame.Valid() {
					continue
				}
				fields, err := peerwire.Parse(frame.Raw, 5)
				if err != nil {
					return
				}
				packet, _ := proto.Marshal(&pb.PeerFrame{Kind: &pb.PeerFrame_DirectedPeerFrame{DirectedPeerFrame: &pb.DirectedPeerFrame{ToDevice: fields[2].B, FrameClass: pb.FrameClass(fields[3].U), FrameId: fields[4].B, Sealed: fields[5].B}}})
				if ws.Write(ctx, websocket.MessageBinary, packet) != nil {
					return
				}
			}
		}
	})
	h.workers.Go(func() {
		for {
			_, raw, err := ws.Read(ctx)
			if err != nil {
				return
			}
			f := &pb.ServerFrame{}
			if proto.Unmarshal(raw, f) != nil {
				return
			}
			if f.GetPeerFrameDelivery() != nil {
				if fields, err := peerwire.Parse(raw, 1); err == nil {
					link.Receive(fields[11].B)
				}
			}
			if r := f.GetPeerFrameResult(); r != nil {
				link.Delivery(r.FrameId, uint32(r.Status), uint64(r.RetryAfterMs), func() bool { return ctx.Err() == nil })
			}
		}
	})

	for h.readPath == nil {
		select {
		case p := <-ready:
			if p.use == 1 {
				h.readPath = p.path
			}
		case <-ctx.Done():
			t.Fatal("timed out waiting for Pion direct use=1 path")
		}
	}

	ep, err := deviceauth.InitEndpoint("", authority.Pin, authority.States, authority.Proofs, authority.Member.ID, authority.Ed.Seed(), authority.X, authority.Lease)
	if err != nil {
		t.Fatal(err)
	}
	h.ep = ep
	h.sessKey = readTestSessionKey(t, e.backend.SessionDir(session))

	t.Cleanup(func() {
		cancel()
		ws.CloseNow()
		link.Close()
		link.Wait()
		h.workers.Wait()
		ep.Close()
	})
	return h
}

func readTestSessionKey(t *testing.T, dir string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "relay.key"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (h *phase3PionHarness) dialTermWS(streamID string) *websocket.Conn {
	h.t.Helper()
	ws, _, err := websocket.Dial(h.ctx, "ws"+strings.TrimPrefix(h.e.http.URL, "http")+"/v1/term", &websocket.DialOptions{HTTPClient: http.DefaultClient})
	if err != nil {
		h.t.Fatal(err)
	}
	ws.SetReadLimit(1 << 20)
	hello, _ := proto.Marshal(&pb.TermHello{Token: h.e.authority.Token, StreamId: streamID, MachineId: h.e.acct.DeviceID})
	if err := ws.Write(h.ctx, websocket.MessageBinary, hello); err != nil {
		ws.CloseNow()
		h.t.Fatal(err)
	}
	return ws
}

func (h *phase3PionHarness) openTerminal(streamID string) *websocket.Conn {
	h.t.Helper()
	ws := h.dialTermWS(streamID)
	cmd := &pb.Command{
		ProtocolVersion: protocol.PeerVersion,
		Kind:            &pb.Command_OpenTerminal{OpenTerminal: &pb.OpenTerminalRequest{SessionId: h.session, StreamId: streamID}},
	}
	plain, _ := proto.Marshal(cmd)
	var nonce [16]byte
	_, _ = rand.Read(nonce[:])
	hdr := devicesec.Header{Kind: 5, Purpose: devicesec.Command, Machine: h.e.acct.DeviceID, Session: h.session, Request: hex.EncodeToString(nonce[:]), Deadline: uint64(time.Now().Add(time.Minute).UnixMilli())}
	sealed, err := h.ep.Prepare(hdr, nil, plain)
	if err != nil {
		h.t.Fatal(err)
	}
	env, err := deviceauth.Envelope(sealed)
	if err != nil {
		h.t.Fatal(err)
	}
	packet, _ := proto.Marshal(env)
	if err := h.readPath.Write(h.ctx, packet); err != nil {
		h.t.Fatal(err)
	}
	// Read CommandResult (or initial terminal output frame on direct path if raced).
	for {
		raw, err := h.readPath.Read(h.ctx)
		if err != nil {
			h.t.Fatal(err)
		}
		resEnv := &pb.Envelope{}
		if proto.Unmarshal(raw, resEnv) != nil {
			continue
		}
		w, err := deviceauth.FromEnvelope(resEnv)
		if err != nil {
			continue
		}
		rh, err := devicesec.ParseHeader(w.Header)
		if err != nil {
			continue
		}
		if rh.Purpose == devicesec.Result && rh.Request == hdr.Request {
			break
		}
	}
	return ws
}

func (h *phase3PionHarness) sealTermFrame(streamID string, seq uint64, f *pb.TerminalFrame) []byte {
	h.t.Helper()
	purpose := devicesec.TerminalInput
	if f.GetResize() != nil {
		purpose = devicesec.TerminalResize
	} else if f.GetHistoryRequest() != nil {
		purpose = devicesec.TerminalHistory
	}
	plain, _ := proto.Marshal(f)
	hdr := devicesec.Header{
		Kind:     uint32(pb.PayloadKind_PAYLOAD_KIND_TERMINAL_INPUT),
		Purpose:  purpose,
		Machine:  h.e.acct.DeviceID,
		Session:  h.session,
		Request:  streamID,
		Seq:      seq,
		Deadline: uint64(time.Now().Add(time.Minute).UnixMilli()),
	}
	sealed, err := h.ep.Prepare(hdr, h.sessKey, plain)
	if err != nil {
		h.t.Fatal(err)
	}
	env, err := deviceauth.Envelope(sealed)
	if err != nil {
		h.t.Fatal(err)
	}
	raw, err := proto.Marshal(env)
	if err != nil {
		h.t.Fatal(err)
	}
	return raw
}

func (h *phase3PionHarness) openTermOutput(raw []byte, streamID string) (uint64, *pb.TerminalFrame) {
	h.t.Helper()
	env := &pb.Envelope{}
	if err := proto.Unmarshal(raw, env); err != nil {
		h.t.Fatal(err)
	}
	w, err := deviceauth.FromEnvelope(env)
	if err != nil {
		h.t.Fatal(err)
	}
	rh, plain, err := h.ep.Open(w, h.sessKey, true, uint64(time.Now().UnixMilli()))
	if err != nil {
		h.t.Fatal(err)
	}
	if rh.Request != streamID || rh.Purpose != devicesec.TerminalOutput {
		h.t.Fatalf("unexpected output header: %+v", rh)
	}
	plain, err = deviceauth.Unpack(rh, plain)
	if err != nil {
		h.t.Fatal(err)
	}
	tf := &pb.TerminalFrame{}
	if err := proto.Unmarshal(plain, tf); err != nil {
		h.t.Fatal(err)
	}
	return rh.Seq, tf
}

func (h *phase3PionHarness) waitControl(typ uint64) peerwire.Opened {
	h.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case o := <-h.controls:
			if o.Type == typ {
				return o
			}
		case <-deadline:
			h.t.Fatalf("timed out waiting for control type %d", typ)
		}
	}
}

func (h *phase3PionHarness) waitInputs(want []byte) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if bytes.Equal(h.backend.term.allInputs(), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("tmux inputs mismatch: got %q want %q", h.backend.term.allInputs(), want)
}

func TestPhase3PionTerminalDirectHotStandbyAndFailover(t *testing.T) {
	h := startPhase3PionHarness(t)
	streamID := "0123456789abcdef0123456789abcdef"
	termWS := h.openTerminal(streamID)
	defer termWS.CloseNow()

	// Initial snapshot arrives over relay WS (and mirrored on direct).
	if _, raw, err := termWS.Read(h.ctx); err != nil {
		t.Fatal(err)
	} else {
		seq, tf := h.openTermOutput(raw, streamID)
		if seq != 1 || tf.GetSnapshot() == nil {
			t.Fatalf("expected seq 1 snapshot, got seq=%d frame=%+v", seq, tf)
		}
	}

	// Attach direct leg via Control 2 (AttachPath) — must NOT reopen tmux.
	legDirect := "directleg0000001"
	if !h.link.SendControl(linkcore.ControlAttachPath, linkcore.EncodeAttachPath(linkcore.AttachPathMsg{
		Stream:         streamID,
		Session:        h.session,
		Sender:         h.e.authority.Member.ID,
		LegID:          legDirect,
		PathGeneration: 1,
	})) {
		t.Fatal("failed to queue AttachPath")
	}

	// 1) Dual-send identical sealed Input seq=1 over BOTH Relay WS and Pion Direct use=1 simultaneously.
	in1 := h.sealTermFrame(streamID, 1, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: []byte("echo hi\n")}})
	if err := h.readPath.Write(h.ctx, in1); err != nil {
		t.Fatal(err)
	}
	if err := termWS.Write(h.ctx, websocket.MessageBinary, in1); err != nil {
		t.Fatal(err)
	}
	h.waitInputs([]byte("echo hi\n"))

	// 2) Out-of-order input (seq=3 before seq=2) -> daemon reorders BEFORE Accept and injects in exact order!
	in3 := h.sealTermFrame(streamID, 3, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: []byte("WORLD")}})
	in2 := h.sealTermFrame(streamID, 2, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: []byte("HELLO-")}})
	if err := h.readPath.Write(h.ctx, in3); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if err := termWS.Write(h.ctx, websocket.MessageBinary, in2); err != nil {
		t.Fatal(err)
	}
	h.waitInputs([]byte("echo hi\nHELLO-WORLD"))

	// 3) Small output (<= 4 KiB) is hot-standby mirrored on BOTH direct and relay.
	h.backend.term.outCh <- []byte("small-output")
	if _, raw, err := termWS.Read(h.ctx); err != nil {
		t.Fatal(err)
	} else {
		_, tf := h.openTermOutput(raw, streamID)
		if string(tf.GetOutput()) != "small-output" {
			t.Fatalf("expected mirrored small-output on relay, got %q", tf.GetOutput())
		}
	}
	// Drain direct path until we see the mirrored small-output frame.
	gotDirectSmall := false
	for !gotDirectSmall {
		raw, err := h.readPath.Read(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, tf := h.openTermOutput(raw, streamID)
		if string(tf.GetOutput()) == "small-output" {
			gotDirectSmall = true
		}
	}

	// 4) Drop Relay WS while Pion direct leg is alive -> logical stream stays alive;
	// recover relay via AttachPath (PathGeneration=0) + new /v1/term WS with leg_id as StreamId.
	termWS.CloseNow()
	time.Sleep(50 * time.Millisecond)

	// Send input seq=4 purely over Pion direct while relay WS is down!
	in4 := h.sealTermFrame(streamID, 4, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: []byte("-DIRECT")}})
	if err := h.readPath.Write(h.ctx, in4); err != nil {
		t.Fatal(err)
	}
	h.waitInputs([]byte("echo hi\nHELLO-WORLD-DIRECT"))

	// Re-attach replacement relay leg via Control 2 without calling OpenTerminal!
	newRelayLeg := "r:00000000000002"
	newTermWS := h.dialTermWS(newRelayLeg)
	defer newTermWS.CloseNow()
	if !h.link.SendControl(linkcore.ControlAttachPath, linkcore.EncodeAttachPath(linkcore.AttachPathMsg{
		Stream:         streamID,
		Session:        h.session,
		Sender:         h.e.authority.Member.ID,
		LegID:          newRelayLeg,
		PathGeneration: 2,
	})) {
		t.Fatal("failed to queue relay AttachPath")
	}
	time.Sleep(100 * time.Millisecond)

	// Drop Pion direct leg -> send input seq=5 over the newly attached relay leg!
	h.readPath.Close()
	in5 := h.sealTermFrame(streamID, 5, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: []byte("-RELAY")}})
	if err := newTermWS.Write(h.ctx, websocket.MessageBinary, in5); err != nil {
		t.Fatal(err)
	}
	h.waitInputs([]byte("echo hi\nHELLO-WORLD-DIRECT-RELAY"))

	if calls := h.backend.openTermCalls.Load(); calls != 1 {
		t.Fatalf("OpenTerminal must be called exactly once across direct/relay failover & AttachPath, got %d", calls)
	}
}

func TestPhase3PionTerminalControlsFailStopAndNonBypass(t *testing.T) {
	h := startPhase3PionHarness(t)
	streamID := "fedcba9876543210fedcba9876543210"
	termWS := h.openTerminal(streamID)
	defer termWS.CloseNow()

	// Drain initial snapshot.
	if _, _, err := termWS.Read(h.ctx); err != nil {
		t.Fatal(err)
	}

	// 1) Unknown stream InputStatus returns Available=false (user confirmation gate).
	qUnknown := []byte("queryunknown0001")
	if !h.link.SendControl(linkcore.ControlInputStatus, linkcore.EncodeInputStatus(linkcore.InputStatusMsg{
		Stream:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		QueryID: qUnknown,
		FromSeq: 1,
		Limit:   16,
	})) {
		t.Fatal("failed to send unknown InputStatus")
	}
	o := h.waitControl(linkcore.ControlInputStatusReply)
	rep, err := linkcore.DecodeInputStatusReply(o.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Available || !rep.Complete {
		t.Fatalf("expected Available=false Complete=true for unknown stream, got %+v", rep)
	}

	// 2) Resize failure advances processed as NON_INPUT_FAILED without triggering Input fail-stop.
	h.backend.term.mu.Lock()
	h.backend.term.failResize = true
	h.backend.term.mu.Unlock()
	r1 := h.sealTermFrame(streamID, 1, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Resize{Resize: &pb.TerminalResize{Cols: 100, Rows: 30}}})
	if err := h.readPath.Write(h.ctx, r1); err != nil {
		t.Fatal(err)
	}
	// Follow immediately with Input seq=2 -> MUST still inject into tmux!
	in2 := h.sealTermFrame(streamID, 2, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: []byte("after-resize")}})
	if err := h.readPath.Write(h.ctx, in2); err != nil {
		t.Fatal(err)
	}
	h.waitInputs([]byte("after-resize"))

	// 3) Partial 2nd-chunk tmux Input failure triggers NON-BYPASSABLE fail-stop + StreamFailed (Control 7).
	// seq=2 was inputCallNum 1; a 512-byte Input at seq=3 calls Input() twice (256B + 256B).
	// Fail on call #3 (the 2nd chunk of seq=3) -> definitely_injected=256, possibly_injected=512.
	h.backend.term.mu.Lock()
	h.backend.term.failInputAt = 3
	h.backend.term.mu.Unlock()
	bigInput := bytes.Repeat([]byte("A"), 512)
	in3 := h.sealTermFrame(streamID, 3, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: bigInput}})
	if err := h.readPath.Write(h.ctx, in3); err != nil {
		t.Fatal(err)
	}
	sfFrame := h.waitControl(linkcore.ControlStreamFailed)
	sf, err := linkcore.DecodeStreamFailed(sfFrame.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if sf.Stream != streamID || sf.InputFailure.Seq != 3 || sf.InputFailure.DefinitelyInjected != 256 || sf.InputFailure.PossiblyInjected != 512 {
		t.Fatalf("unexpected StreamFailed payload: %+v", sf)
	}

	// Attempt to bypass fail-stop with seq=4 Input -> MUST NOT inject into tmux!
	in4 := h.sealTermFrame(streamID, 4, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: []byte("MUST-NOT-INJECT")}})
	if err := h.readPath.Write(h.ctx, in4); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if bytes.Contains(h.backend.term.allInputs(), []byte("MUST-NOT-INJECT")) {
		t.Fatal("fail-stop was bypassed: seq=4 injected into tmux after partial Input failure")
	}

	// 4) Query InputStatus -> frozen snapshot reports FailedInputSeq=3, Processed=4, InputSuccess=2.
	qLive := []byte("querylive0000001")
	if !h.link.SendControl(linkcore.ControlInputStatus, linkcore.EncodeInputStatus(linkcore.InputStatusMsg{
		Stream:  streamID,
		QueryID: qLive,
		FromSeq: 1,
		Limit:   16,
	})) {
		t.Fatal("failed to send live InputStatus")
	}
	oLive := h.waitControl(linkcore.ControlInputStatusReply)
	repLive, err := linkcore.DecodeInputStatusReply(oLive.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !repLive.Available || len(repLive.SnapshotToken) != 32 || repLive.State.ToDaemon.FailedInputSeq != 3 || repLive.State.ToDaemon.InputSuccess != 2 {
		t.Fatalf("unexpected live InputStatusReply: %+v", repLive)
	}

	// 5) Barrier with stop_admission=true returns BarrierReply{Final: true, Accepted: true}.
	bid := []byte("barrier000000001")
	if !h.link.SendControl(linkcore.ControlBarrier, linkcore.EncodeBarrier(linkcore.BarrierMsg{
		Stream:        streamID,
		BarrierID:     bid,
		Requested:     repLive.State,
		StopAdmission: true,
	})) {
		t.Fatal("failed to send Barrier")
	}
	oBar := h.waitControl(linkcore.ControlBarrierReply)
	barRep, err := linkcore.DecodeBarrierReply(oBar.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !barRep.Final || !barRep.Accepted || barRep.Stream != streamID {
		t.Fatalf("unexpected BarrierReply: %+v", barRep)
	}
}
