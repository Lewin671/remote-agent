package direct

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Lewin671/remote-agent/shared/linkcore"
	"github.com/Lewin671/remote-agent/shared/peerwire"
	"github.com/pion/webrtc/v4"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func captureLog(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return buf
}

// A path that loses its gate says which gate, once, with stage times and no
// identifiers.
func TestPathEndNamesTheGateThatRefusedIt(t *testing.T) {
	buf := captureLog(t)
	var allowed atomic.Bool
	allowed.Store(true)
	p, err := New(context.Background(), Config{Use: 2, Owner: "owner-secret", Peer: "peer-secret", Generation: 3, Pool: NewPool(8), Allow: allowed.Load, LoopbackOnly: true,
		Why: func() string {
			if allowed.Load() {
				return ""
			}
			return "round_deadline"
		}})
	if err != nil {
		t.Fatal(err)
	}
	allowed.Store(false)
	select {
	case <-p.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("path did not end")
	}
	p.Close()
	out := buf.String()
	if strings.Count(out, "direct path ended") != 1 || !strings.Contains(out, "peer=…cret") || !strings.Contains(out, "reason=round_deadline") || !strings.Contains(out, "use=2") || !strings.Contains(out, "generation=3") {
		t.Fatalf("log = %q", out)
	}
	for _, secret := range []string{"owner-secret", "peer-secret", "candidate", "ufrag"} {
		if strings.Contains(out, secret) {
			t.Fatalf("log leaks %q: %s", secret, out)
		}
	}
}

func TestRoundEndIncludesShortPeerIDWithoutFullID(t *testing.T) {
	buf := captureLog(t)
	s := &Session{born: time.Now(), cfg: SessionConfig{}}
	s.cfg.Peer.ID = []byte("client-device-secret-9f2a")
	s.endFor("not_allowed")
	s.logEnd(false, [2]string{"------", "------"})
	out := buf.String()
	if !strings.Contains(out, "direct round ended") || !strings.Contains(out, "peer=…9f2a") || !strings.Contains(out, "reason=not_allowed") {
		t.Fatalf("log = %q", out)
	}
	if strings.Contains(out, "client-device-secret-9f2a") {
		t.Fatalf("log leaks full peer ID: %s", out)
	}
}

// The first reason is kept; a later close cannot rewrite it.
func TestPathEndKeepsTheFirstReason(t *testing.T) {
	buf := captureLog(t)
	p, err := New(context.Background(), Config{Use: 1, Owner: "o", Peer: "p", Generation: 1, Pool: NewPool(8), Allow: func() bool { return true }, LoopbackOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	p.closeWhy("policy")
	p.closeWhy("pc_failed")
	p.Close()
	p.Wait()
	if out := buf.String(); !strings.Contains(out, "reason=policy") || strings.Contains(out, "pc_failed") {
		t.Fatalf("log = %q", out)
	}
}

// The round's log keeps a bounded trace of signal kinds; acknowledgements
// are left out and the trace stops growing.
func TestRoundTraceIsBounded(t *testing.T) {
	s := &Session{born: time.Now()}
	for i := 0; i < 3*maxTrace; i++ {
		s.traceAdd("rx", 5, 1, true)
	}
	s.traceAdd("tx", 4, 0, true) // an acknowledgement: not recorded
	s.traceMu.Lock()
	n := len(s.trace)
	s.traceMu.Unlock()
	if n != maxTrace {
		t.Fatalf("trace length = %d, want %d", n, maxTrace)
	}
	s2 := &Session{born: time.Now()}
	s2.traceAdd("tx", 4, 0, true)
	s2.traceAdd("tx", 7, 1, false)
	if len(s2.trace) != 1 || !strings.HasPrefix(s2.trace[0], "tx7.1!@") {
		t.Fatalf("trace = %v", s2.trace)
	}
}

func TestSessionAllowReasonRefinesLeaseOrProcess(t *testing.T) {
	buf := captureLog(t)
	var allowed atomic.Bool
	allowed.Store(true)
	s := &Session{
		born: time.Now(),
		ctx:  context.Background(),
		cfg: SessionConfig{
			Peer:         peerwire.Public{ID: []byte("peer-reason-1234")},
			Connection:   []byte("conn-reason"),
			Deadline:     uint64(time.Now().Add(time.Minute).UnixMilli()),
			Pool:         NewPool(8),
			Allow:        allowed.Load,
			AllowReason:  func() string { return "sync_failed" },
			LoopbackOnly: true,
		},
		events: make(chan linkcore.Event, 8),
		paths:  map[uint64]*Path{},
		open:   map[uint64]*Path{},
	}
	s.generation[0].Store(1)
	s.current[0].Store(1)
	if !s.createPC(linkcore.CreatePC{ID: 1, Use: 1, Generation: 1}) {
		t.Fatal("createPC failed")
	}
	p := s.paths[1]
	allowed.Store(false)
	select {
	case <-p.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("path did not close after Allow became false")
	}
	p.Close()
	if out := buf.String(); !strings.Contains(out, "reason=sync_failed") || strings.Contains(out, "reason=lease_or_process") {
		t.Fatalf("log = %q, want reason=sync_failed", out)
	}
}

func TestFormatPairTagMasksAddressesAndPorts(t *testing.T) {
	if got := formatPairTag(nil); got != "none" {
		t.Fatalf("formatPairTag(nil) = %q, want none", got)
	}
	pair := webrtc.NewICECandidatePair(
		&webrtc.ICECandidate{Address: "2001:db8:1::5%en0", Port: 41478, Typ: webrtc.ICECandidateTypeHost},
		&webrtc.ICECandidate{Address: "2001:db8:2::9", Port: 54321, Typ: webrtc.ICECandidateTypePrflx},
	)
	if got := formatPairTag(pair); got != "v6host-v6prflx" {
		t.Fatalf("formatPairTag(v6) = %q, want v6host-v6prflx", got)
	}
	pair4 := webrtc.NewICECandidatePair(
		&webrtc.ICECandidate{Address: "198.51.100.7", Port: 41478, Typ: webrtc.ICECandidateTypeSrflx},
		&webrtc.ICECandidate{Address: "192.168.1.20", Port: 54321, Typ: webrtc.ICECandidateTypeHost},
	)
	if got := formatPairTag(pair4); got != "v4srflx-v4host" {
		t.Fatalf("formatPairTag(v4) = %q, want v4srflx-v4host", got)
	}
}

func TestPathEndAppendsEchoSCTPTxRxAndPairFields(t *testing.T) {
	buf := captureLog(t)
	a, b, ctx := localPair(t)

	// Send one dc0 payload from a -> b so a records dc0_tx_frames/bytes and b records last_rx_ms.
	payload := bytes.Repeat([]byte{0x5a}, 512)
	done := make(chan error, 1)
	go func() { done <- a.Write(ctx, payload) }()
	if _, err := b.Read(ctx); err != nil {
		t.Fatalf("b.Read: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("a.Write: %v", err)
	}

	// Send one dc1 liveness probe from b -> a and await echo so a records probes_echoed=1 + last_rx_ms + echo buffers.
	echoed := make(chan []byte, 1)
	b.probe.OnMessage(func(m webrtc.DataChannelMessage) {
		if len(m.Data) == 17 && m.Data[0] == 1 {
			select {
			case echoed <- append([]byte(nil), m.Data...):
			default:
			}
		}
	})
	probeOpen := make(chan struct{})
	b.probe.OnOpen(func() { close(probeOpen) })
	if b.probe.ReadyState() != webrtc.DataChannelStateOpen {
		select {
		case <-probeOpen:
		case <-time.After(5 * time.Second):
			t.Fatal("probe channel did not open")
		}
	}
	req := make([]byte, 17)
	req[0] = 0
	req[1] = 0x77
	if err := b.probe.Send(req); err != nil {
		t.Fatalf("b.probe.Send: %v", err)
	}
	select {
	case <-echoed:
	case <-time.After(5 * time.Second):
		t.Fatal("did not receive probe echo")
	}

	// Exercise SCTP cwnd sampling deterministically before closing.
	a.sampleSCTP()

	a.closeWhy("dc0_closed")
	a.Wait()

	out := buf.String()
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "direct path ended") && strings.Contains(l, "reason=dc0_closed") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("missing direct path ended line for a in:\n%s", out)
	}
	for _, want := range []string{
		"probes_echoed=1",
		"echo_send_err=0",
		"dc1_buffered=",
		"dc0_buffered=",
		"dc1_buffered_max=",
		"dc0_buffered_max=",
		"sctp_srtt_ms=",
		"sctp_cwnd=",
		"sctp_rwnd=",
		"sctp_cwnd_min=",
		"dc0_tx_frames=1",
		"dc0_tx_bytes=",
		"last_rx_ms=",
		"pair=",
		"pair_changes=",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("log line missing %q:\n%s", want, line)
		}
	}
	if strings.Contains(line, "dc0_tx_bytes=0") || strings.Contains(line, "last_rx_ms=-1") || strings.Contains(line, "pair=none") {
		t.Errorf("expected non-zero tx bytes, measured last_rx_ms, and non-none pair:\n%s", line)
	}
	for _, forbidden := range []string{"127.0.0.1", "::1", "ufrag", "candidate:"} {
		if strings.Contains(line, forbidden) {
			t.Errorf("log line leaks %q:\n%s", forbidden, line)
		}
	}
	t.Logf("DAEMON_LOG_LINE: %s", line)
}
