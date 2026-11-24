package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"google.golang.org/protobuf/proto"
)

type phase2FaultBackend struct {
	Backend
	mu        sync.Mutex
	counts    map[string]int
	sessions  []string
	blockOrd  chan struct{}
	blockSend chan struct{}
	entered   chan string
}

type phase2FaultLocal struct {
	remoteagentv1connect.UnimplementedLocalServiceHandler
	b *phase2FaultBackend
}

func (b *phase2FaultBackend) Local() remoteagentv1connect.LocalServiceHandler {
	return phase2FaultLocal{b: b}
}

func (b *phase2FaultBackend) SessionIDs() []string {
	if len(b.sessions) > 0 {
		return b.sessions
	}
	return []string{"s-phase2"}
}

func (b *phase2FaultBackend) record(tag string) {
	b.mu.Lock()
	b.counts[tag]++
	b.mu.Unlock()
}

func (l phase2FaultLocal) CreateSession(_ context.Context, req *connect.Request[pb.CreateSessionRequest]) (*connect.Response[pb.CreateSessionResponse], error) {
	l.b.record("create:" + req.Msg.Cwd)
	return connect.NewResponse(&pb.CreateSessionResponse{Session: &pb.Session{Id: "created-" + req.Msg.Cwd}}), nil
}

func (l phase2FaultLocal) Send(ctx context.Context, req *connect.Request[pb.SendRequest]) (*connect.Response[pb.SendResponse], error) {
	if l.b.blockSend != nil && req.Msg.Text == "hold" {
		if l.b.entered != nil {
			l.b.entered <- "input"
		}
		select {
		case <-l.b.blockSend:
			return connect.NewResponse(&pb.SendResponse{}), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	l.b.record("send:" + req.Msg.Text)
	return connect.NewResponse(&pb.SendResponse{}), nil
}

func (l phase2FaultLocal) Interrupt(_ context.Context, req *connect.Request[pb.InterruptRequest]) (*connect.Response[pb.InterruptResponse], error) {
	l.b.record("interrupt:" + req.Msg.SessionId)
	return connect.NewResponse(&pb.InterruptResponse{}), nil
}

func (l phase2FaultLocal) ResolveApproval(_ context.Context, req *connect.Request[pb.ResolveApprovalRequest]) (*connect.Response[pb.ResolveApprovalResponse], error) {
	l.b.record("approval:" + req.Msg.ApprovalId)
	return connect.NewResponse(&pb.ResolveApprovalResponse{}), nil
}

func (b *phase2FaultBackend) LiveSnapshot(_ context.Context, _ string) (*pb.LiveSnapshot, error) {
	return &pb.LiveSnapshot{}, nil
}

func (l phase2FaultLocal) GetSettings(ctx context.Context, _ *connect.Request[pb.GetSettingsRequest]) (*connect.Response[pb.GetSettingsResponse], error) {
	if l.b.blockOrd != nil {
		if l.b.entered != nil {
			l.b.entered <- "ordinary"
		}
		select {
		case <-l.b.blockOrd:
			return connect.NewResponse(&pb.GetSettingsResponse{}), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return connect.NewResponse(&pb.GetSettingsResponse{}), nil
}

func (l phase2FaultLocal) PutImageChunk(_ context.Context, _ *connect.Request[pb.PutImageChunkRequest]) (*connect.Response[pb.PutImageChunkResponse], error) {
	return connect.NewResponse(&pb.PutImageChunkResponse{}), nil
}

func openCommandResult(t *testing.T, client *deviceauth.Endpoint, rep *pb.ForwardReply) *pb.CommandResult {
	t.Helper()
	if rep == nil || rep.Result == nil {
		return nil
	}
	wire, err := deviceauth.FromEnvelope(rep.Result)
	if err != nil {
		t.Fatal(err)
	}
	h, plain, err := client.Open(wire, nil, true, uint64(time.Now().UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	plain, err = deviceauth.Unpack(h, plain)
	if err != nil {
		t.Fatal(err)
	}
	res := &pb.CommandResult{}
	if err := proto.Unmarshal(plain, res); err != nil {
		t.Fatal(err)
	}
	return res
}

// TestPhase2CrossPathFaultInjection10000 exercises >=10,000 cross-path fault-injection
// deliveries across CreateSession, Send, Interrupt, and ResolveApproval on both
// Relay and Direct (use=1) legs, verifying:
//   - zero duplicate side effects across concurrent/ordered/re-sealed dual-path deliveries
//   - ciphertext-hash dedup/coalescing before signature verification for identical bytes
//   - forged frames are never cached and never block subsequent valid frames
//   - payload conflicts on the same RequestID are rejected without re-execution
//   - bulk commands are rejected on direct use=1 (must stay on use=2)
func TestPhase2CrossPathFaultInjection10000(t *testing.T) {
	const totalIterations = 10_000
	backend := &phase2FaultBackend{
		counts:   map[string]int{},
		sessions: []string{"s-phase2", "s-phase2-0", "s-phase2-1", "s-phase2-2"},
	}
	ep := testEndpoint(t, New(backend))
	client := endpointClient(t, ep)
	defer func() {
		ep.link.DrainCalls()
		ep.admissionOwner().close()
	}()

	_, head, selfID := ep.security.endpoint.Snapshot()
	selfMember, _ := head.Core.Member(selfID)
	peerMember, _ := head.Core.Member(head.Core.AuthorityID)
	headHash, err := head.Hash()
	if err != nil {
		t.Fatal(err)
	}
	binding := deviceauth.PeerBinding{Self: selfMember, Peer: peerMember, Head: headHash}

	adm := ep.admissionOwner()
	origAuth := adm.authenticate
	var authCalls atomic.Uint64
	adm.authenticate = func(w devicesec.Wire) (*deviceauth.Authenticated, error) {
		authCalls.Add(1)
		return origAuth(w)
	}

	seal := func(reqID, session string, cmd *pb.Command) *pb.Envelope {
		cmd.ProtocolVersion = protocol.PeerVersion
		body, err := proto.Marshal(cmd)
		if err != nil {
			t.Fatal(err)
		}
		wire, err := client.Prepare(devicesec.Header{
			Purpose:  devicesec.Command,
			Kind:     5,
			Machine:  ep.acct.DeviceID,
			Session:  session,
			Request:  reqID,
			Deadline: uint64(time.Now().Add(5 * time.Minute).UnixMilli()),
		}, nil, body)
		if err != nil {
			t.Fatal(err)
		}
		env, err := deviceauth.Envelope(wire)
		if err != nil {
			t.Fatal(err)
		}
		return env
	}

	// Verify bulk chunk is still rejected on direct use=1 (path_unavailable).
	bulkEnv := seal("bulk-use1-check-0001", "", &pb.Command{
		Kind: &pb.Command_PutImageChunk{PutImageChunk: &pb.PutImageChunkRequest{TransferId: "t1", Data: []byte("x")}},
	})
	bulkCh := make(chan *pb.ForwardReply, 1)
	ep.dispatchDirect(context.Background(), binding, 1, func() bool { return true }, bulkEnv, func(r *pb.ForwardReply) { bulkCh <- r })
	bulkRep := <-bulkCh
	if res := openCommandResult(t, client, bulkRep); res == nil || res.Reason != "path_unavailable" {
		t.Fatalf("expected PutImageChunk on direct use=1 to return path_unavailable, got %+v", res)
	}

	// Verify forged frame is never cached and does not poison a subsequent valid frame.
	validReqID := "forge-guard-request-0001"
	validEnv := seal(validReqID, "s-phase2", &pb.Command{
		Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: "s-phase2", Text: "forge-ok"}},
	})
	forgedEnv := proto.Clone(validEnv).(*pb.Envelope)
	forgedEnv.Signature = append([]byte(nil), validEnv.Signature...)
	forgedEnv.Signature[0] ^= 0xff
	forgeCh := make(chan *pb.ForwardReply, 1)
	ep.dispatchDirect(context.Background(), binding, 1, func() bool { return true }, forgedEnv, func(r *pb.ForwardReply) { forgeCh <- r })
	if rep := <-forgeCh; !rep.EntryFailure || rep.Reason != "authentication_failed" {
		t.Fatalf("expected forged frame authentication_failed, got %+v", rep)
	}
	validCh := make(chan *pb.ForwardReply, 1)
	ep.dispatch(context.Background(), &pb.ForwardCall{CallId: 1, AttemptId: "valid", Command: validEnv}, func(r *pb.ForwardReply) { validCh <- r })
	if res := openCommandResult(t, client, <-validCh); res == nil || res.Outcome != "executed" {
		t.Fatalf("expected valid frame after forged frame to execute, got %+v", res)
	}

	// Run 10,000 fault-injected cross-path deliveries across 3 concurrent workers
	// (keeping concurrent frames <= 6, strictly below the single-peer PeerEntries=8
	// cap on both frames and results queues, while still exercising concurrent
	// dual-path arrival and verified-only ciphertext dedup).
	const workers = 3
	perWorker := totalIterations / workers
	extra := totalIterations % workers
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error
	recordErr := func(err error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		errMu.Unlock()
	}

	var expectedKeys []string
	var keysMu sync.Mutex

	for w := range workers {
		wg.Add(1)
		startIdx := w*perWorker + min(w, extra)
		count := perWorker
		if w < extra {
			count++
		}
		go func(workerID, start, n int) {
			defer wg.Done()
			ctx := context.Background()
			localKeys := make([]string, 0, n)
			for i := range n {
				idx := start + i
				reqID := fmt.Sprintf("p2-fi-%02d-%06d-req", workerID, i)
				var cmd *pb.Command
				var key, sess string
				switch idx % 4 {
				case 0:
					tag := fmt.Sprintf("w%d-%d", workerID, i)
					cmd = &pb.Command{Kind: &pb.Command_CreateSession{CreateSession: &pb.CreateSessionRequest{Cwd: tag, Agent: pb.Agent_AGENT_CODEX}}}
					key = "create:" + tag
				case 1:
					tag := fmt.Sprintf("s%d-%d", workerID, i)
					sess = fmt.Sprintf("s-phase2-%d", workerID)
					cmd = &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: sess, Text: tag}}}
					key = "send:" + tag
				case 2:
					sess = fmt.Sprintf("s-phase2-%d", workerID)
					cmd = &pb.Command{Kind: &pb.Command_Interrupt{Interrupt: &pb.InterruptRequest{SessionId: sess}}}
					key = "interrupt:" + sess
				case 3:
					tag := fmt.Sprintf("a%d-%d", workerID, i)
					sess = fmt.Sprintf("s-phase2-%d", workerID)
					cmd = &pb.Command{Kind: &pb.Command_ResolveApproval{ResolveApproval: &pb.ResolveApprovalRequest{SessionId: sess, ApprovalId: tag, Allow: true}}}
					key = "approval:" + tag
				}
				if idx%4 != 2 {
					localKeys = append(localKeys, key)
				}
				env := seal(reqID, sess, cmd)
				mode := idx % 7
				switch mode {
				case 0, 1:
					// Simultaneous dual-send (identical ciphertext on direct + relay).
					rCh := make(chan *pb.ForwardReply, 1)
					dCh := make(chan *pb.ForwardReply, 1)
					if mode == 0 {
						ep.dispatchDirect(ctx, binding, 1, func() bool { return true }, env, func(r *pb.ForwardReply) { dCh <- r })
						ep.dispatch(ctx, &pb.ForwardCall{CallId: uint64(idx + 10), AttemptId: "r", Command: env}, func(r *pb.ForwardReply) { rCh <- r })
					} else {
						ep.dispatch(ctx, &pb.ForwardCall{CallId: uint64(idx + 10), AttemptId: "r", Command: env}, func(r *pb.ForwardReply) { rCh <- r })
						ep.dispatchDirect(ctx, binding, 1, func() bool { return true }, env, func(r *pb.ForwardReply) { dCh <- r })
					}
					r1 := <-rCh
					r2 := <-dCh
					if r1 == nil || r1.Result == nil || r2 == nil || r2.Result == nil {
						recordErr(fmt.Errorf("iter %d: nil dual reply r1=%v r2=%v", idx, r1, r2))
						return
					}
				case 2:
					// Direct first, then relay arrives with identical sealed bytes (hits verified ciphertext dedup cache).
					dCh := make(chan *pb.ForwardReply, 1)
					ep.dispatchDirect(ctx, binding, 1, func() bool { return true }, env, func(r *pb.ForwardReply) { dCh <- r })
					if r := <-dCh; r == nil || r.Result == nil {
						recordErr(fmt.Errorf("iter %d: direct failed %v", idx, r))
						return
					}
					rCh := make(chan *pb.ForwardReply, 1)
					ep.dispatch(ctx, &pb.ForwardCall{CallId: uint64(idx + 10), AttemptId: "r", Command: env}, func(r *pb.ForwardReply) { rCh <- r })
					if r := <-rCh; r == nil || r.Result == nil {
						recordErr(fmt.Errorf("iter %d: relay replay failed %v", idx, r))
						return
					}
				case 3:
					// Re-sealed same RequestID + same CommandBytes (simulates key-rotation retry) across paths.
					rCh := make(chan *pb.ForwardReply, 1)
					ep.dispatch(context.Background(), &pb.ForwardCall{CallId: uint64(idx + 10), AttemptId: "r1", Command: env}, func(r *pb.ForwardReply) { rCh <- r })
					if r := <-rCh; r == nil || r.Result == nil {
						recordErr(fmt.Errorf("iter %d: initial relay failed %v", idx, r))
						return
					}
					resealed := seal(reqID, sess, cmd)
					dCh := make(chan *pb.ForwardReply, 1)
					ep.dispatchDirect(ctx, binding, 1, func() bool { return true }, resealed, func(r *pb.ForwardReply) { dCh <- r })
					if r := <-dCh; r == nil || r.Result == nil {
						recordErr(fmt.Errorf("iter %d: resealed direct failed %v", idx, r))
						return
					}
				case 4:
					// Direct reply dropped (allow=false -> dispatchDirect drops before enqueue), relay succeeds.
					ep.dispatchDirect(ctx, binding, 1, func() bool { return false }, env, func(*pb.ForwardReply) {})
					rCh := make(chan *pb.ForwardReply, 1)
					ep.dispatch(ctx, &pb.ForwardCall{CallId: uint64(idx + 10), AttemptId: "r", Command: env}, func(r *pb.ForwardReply) { rCh <- r })
					if r := <-rCh; r == nil || r.Result == nil {
						recordErr(fmt.Errorf("iter %d: relay after direct drop failed %v", idx, r))
						return
					}
				case 5:
					// Valid execution followed by conflicting payload on the SAME RequestKey (same session + reqID) via Direct.
					rCh := make(chan *pb.ForwardReply, 1)
					ep.dispatch(ctx, &pb.ForwardCall{CallId: uint64(idx + 10), AttemptId: "r", Command: env}, func(r *pb.ForwardReply) { rCh <- r })
					if r := <-rCh; r == nil || r.Result == nil {
						recordErr(fmt.Errorf("iter %d: initial conflict-case relay failed %v", idx, r))
						return
					}
					var conflictCmd *pb.Command
					if sess == "" {
						conflictCmd = &pb.Command{Kind: &pb.Command_CreateSession{CreateSession: &pb.CreateSessionRequest{Cwd: "conflicting-payload", Agent: pb.Agent_AGENT_CODEX}}}
					} else {
						conflictCmd = &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: sess, Text: "conflicting-payload"}}}
					}
					conflictEnv := seal(reqID, sess, conflictCmd)
					dCh := make(chan *pb.ForwardReply, 1)
					ep.dispatchDirect(ctx, binding, 1, func() bool { return true }, conflictEnv, func(r *pb.ForwardReply) { dCh <- r })
					rep := <-dCh
					if rep == nil || rep.Result == nil {
						recordErr(fmt.Errorf("iter %d: missing conflict reply", idx))
						return
					}
					if res := openCommandResult(t, client, rep); res == nil || res.Outcome != "conflict" || res.ConflictKind != "identity" {
						recordErr(fmt.Errorf("iter %d: expected outcome=conflict conflict_kind=identity, got %+v", idx, res))
						return
					}
				case 6:
					// Direct-only arrival (relay dropped in transit).
					dCh := make(chan *pb.ForwardReply, 1)
					ep.dispatchDirect(ctx, binding, 1, func() bool { return true }, env, func(r *pb.ForwardReply) { dCh <- r })
					if r := <-dCh; r == nil || r.Result == nil {
						recordErr(fmt.Errorf("iter %d: direct-only failed %v", idx, r))
						return
					}
				}
			}
			keysMu.Lock()
			expectedKeys = append(expectedKeys, localKeys...)
			keysMu.Unlock()
		}(w, startIdx, count)
	}
	wg.Wait()
	if firstErr != nil {
		t.Fatal(firstErr)
	}

	// Assert ZERO duplicate side effects across all 10,000 iterations!
	backend.mu.Lock()
	defer backend.mu.Unlock()
	for _, k := range expectedKeys {
		if got := backend.counts[k]; got != 1 {
			t.Fatalf("duplicate or missing side effect for %q: executed %d times (expected 1)", k, got)
		}
	}
	if got := backend.counts["send:conflicting-payload"] + backend.counts["create:conflicting-payload"]; got != 0 {
		t.Fatalf("conflicting payload executed %d times (expected 0)", got)
	}
	interruptTotal := backend.counts["interrupt:s-phase2-0"] + backend.counts["interrupt:s-phase2-1"] + backend.counts["interrupt:s-phase2-2"]
	if interruptTotal != totalIterations/4 {
		t.Fatalf("interrupt executed %d times, expected %d", interruptTotal, totalIterations/4)
	}
	coalesced := adm.authCoalesced.Load()
	if coalesced == 0 {
		t.Fatalf("expected ciphertext-hash verification dedup/coalescing to fire on dual-path identical envelopes")
	}
	t.Logf("PASS Phase 2 fault injection: %d iterations, 0 duplicate side effects, auth_calls=%d, ciphertext_coalesced=%d", totalIterations, authCalls.Load(), coalesced)
}

// TestPhase2ApprovalSchedulingP99UnderSaturation verifies that GetLiveSnapshot -> ResolveApproval
// slot acquisition stays at p99 <= 100ms under Ordinary + Bulk + long Input + Snapshot saturation
// while dual-path identical command envelopes arrive concurrently.
func TestPhase2ApprovalSchedulingP99UnderSaturation(t *testing.T) {
	sessions := []string{"approval-session"}
	for i := range 16 {
		sessions = append(sessions, fmt.Sprintf("input-%d", i))
	}
	backend := &phase2FaultBackend{
		counts:    map[string]int{},
		sessions:  sessions,
		blockOrd:  make(chan struct{}),
		blockSend: make(chan struct{}),
		entered:   make(chan string, 64),
	}
	ep := testEndpoint(t, New(backend))
	client := endpointClient(t, ep)
	defer func() {
		close(backend.blockOrd)
		close(backend.blockSend)
		ep.link.DrainCalls()
		ep.admissionOwner().close()
	}()

	_, head, selfID := ep.security.endpoint.Snapshot()
	selfMember, _ := head.Core.Member(selfID)
	peerMember, _ := head.Core.Member(head.Core.AuthorityID)
	headHash, _ := head.Hash()
	binding := deviceauth.PeerBinding{Self: selfMember, Peer: peerMember, Head: headHash}

	var mu sync.Mutex
	var delays []float64
	granted := 0
	ep.r.observeApproval = func(acquired bool, d time.Duration) {
		mu.Lock()
		delays = append(delays, float64(d)/float64(time.Millisecond))
		if acquired {
			granted++
		}
		mu.Unlock()
	}

	seal := func(session string, cmd *pb.Command) *pb.Envelope {
		var nonce [16]byte
		_, _ = rand.Read(nonce[:])
		cmd.ProtocolVersion = protocol.PeerVersion
		body, err := proto.Marshal(cmd)
		if err != nil {
			t.Fatal(err)
		}
		wire, err := client.Prepare(devicesec.Header{
			Purpose:  devicesec.Command,
			Kind:     5,
			Machine:  ep.acct.DeviceID,
			Session:  session,
			Request:  hex.EncodeToString(nonce[:]),
			Deadline: uint64(time.Now().Add(time.Minute).UnixMilli()),
		}, nil, body)
		if err != nil {
			t.Fatal(err)
		}
		env, err := deviceauth.Envelope(wire)
		if err != nil {
			t.Fatal(err)
		}
		return env
	}

	// Saturate all 4 Ordinary slots.
	for range 4 {
		ep.dispatch(context.Background(), &pb.ForwardCall{Command: seal("", &pb.Command{Kind: &pb.Command_GetSettings{GetSettings: &pb.GetSettingsRequest{}}})}, func(*pb.ForwardReply) {})
		select {
		case <-backend.entered:
		case <-time.After(2 * time.Second):
			t.Fatal("ordinary load did not start")
		}
	}
	// Hold 1 Input slot on input-0 and flood additional Input + Bulk commands.
	ep.dispatch(context.Background(), &pb.ForwardCall{Command: seal("input-0", &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: "input-0", Text: "hold"}}})}, func(*pb.ForwardReply) {})
	select {
	case <-backend.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("input load did not start")
	}
	for i := range 32 {
		s := fmt.Sprintf("input-%d", i%16)
		ep.dispatch(context.Background(), &pb.ForwardCall{Command: seal(s, &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: s, Text: "flood"}}})}, func(*pb.ForwardReply) {})
	}
	for i := range 16 {
		ep.dispatchDirect(context.Background(), binding, 2, func() bool { return true }, seal("", &pb.Command{Kind: &pb.Command_PutImageChunk{PutImageChunk: &pb.PutImageChunkRequest{TransferId: fmt.Sprintf("b%d", i), Data: make([]byte, 1024)}}}), func(*pb.ForwardReply) {})
	}
	// Let the pre-loop flood drain through authentication into the scheduler queues
	// so sample 0 measures scheduler saturation rather than single-peer auth-queue overflow.
	adm := ep.admissionOwner()
	deadlineDrain := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadlineDrain) {
		adm.mu.Lock()
		n := adm.frames.Len()
		adm.mu.Unlock()
		if n == 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	const samples = 200
	completed := 0
	for i := range samples {
		// Step 1: GetLiveSnapshot (direct use=1)
		snapCh := make(chan *pb.ForwardReply, 1)
		snapEnv := seal("approval-session", &pb.Command{Kind: &pb.Command_GetLiveSnapshot{GetLiveSnapshot: &pb.GetLiveSnapshotRequest{SessionId: "approval-session"}}})
		ep.dispatchDirect(context.Background(), binding, 1, func() bool { return true }, snapEnv, func(r *pb.ForwardReply) { snapCh <- r })
		<-snapCh

		// Step 2: Dual-send ResolveApproval on BOTH direct use=1 and relay;
		// wait for both legs (if one leg got auth-queue busy, sibling completes).
		appEnv := seal("approval-session", &pb.Command{Kind: &pb.Command_ResolveApproval{ResolveApproval: &pb.ResolveApprovalRequest{SessionId: "approval-session", ApprovalId: fmt.Sprintf("app-%d", i), Allow: true}}})
		dCh := make(chan *pb.ForwardReply, 1)
		rCh := make(chan *pb.ForwardReply, 1)
		ep.dispatchDirect(context.Background(), binding, 1, func() bool { return true }, appEnv, func(r *pb.ForwardReply) { dCh <- r })
		ep.dispatch(context.Background(), &pb.ForwardCall{CallId: uint64(i + 1), AttemptId: "r", Command: appEnv}, func(r *pb.ForwardReply) { rCh <- r })

		var dRep, rRep *pb.ForwardReply
		select {
		case dRep = <-dCh:
		case <-time.After(2 * time.Second):
			t.Fatalf("approval sample %d direct leg timed out", i)
		}
		select {
		case rRep = <-rCh:
		case <-time.After(2 * time.Second):
			t.Fatalf("approval sample %d relay leg timed out", i)
		}
		dRes := openCommandResult(t, client, dRep)
		rRes := openCommandResult(t, client, rRep)
		if (dRes != nil && dRes.Outcome == "executed") || (rRes != nil && rRes.Outcome == "executed") {
			completed++
		}
	}

	mu.Lock()
	sorted := append([]float64(nil), delays...)
	gotGranted := granted
	mu.Unlock()
	sort.Float64s(sorted)
	pct := func(p float64) float64 {
		if len(sorted) == 0 {
			return 0
		}
		idx := int(math.Ceil(p*float64(len(sorted)))) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sorted) {
			idx = len(sorted) - 1
		}
		return sorted[idx]
	}
	p50, p95, p99 := pct(0.50), pct(0.95), pct(0.99)
	t.Logf("PASS Phase 2 approval scheduling under saturation: samples=%d completed=%d granted=%d p50=%.2fms p95=%.2fms p99=%.2fms", samples, completed, gotGranted, p50, p95, p99)
	if completed < samples {
		t.Fatalf("expected all %d approval samples to complete, got %d", samples, completed)
	}
	if p99 > 100.0 {
		t.Fatalf("approval scheduling p99=%.2fms exceeded 100ms budget", p99)
	}
}

// TestPhase2DeterministicVerifiedCacheAndRestart verifies M9 & L3:
//   - true concurrent dual-path barrier race executes exactly once and returns both replies
//   - verifiedCache 10s TTL expiry with deterministic injected clock re-verifies after 10s
//   - per-peer verifiedCache stays strictly bounded at <=4096 entries and <=4 MiB (FIFO eviction)
//   - restarting commandAdmission (simulating daemon restart over durable accept log) re-verifies
//     late duplicates and returns stored result with 0 duplicate side effects
func TestPhase2DeterministicVerifiedCacheAndRestart(t *testing.T) {
	backend := &phase2FaultBackend{
		counts:   map[string]int{},
		sessions: []string{"s-phase2"},
	}
	ep := testEndpoint(t, New(backend))
	client := endpointClient(t, ep)
	defer func() {
		ep.link.DrainCalls()
		ep.admissionOwner().close()
	}()

	_, head, selfID := ep.security.endpoint.Snapshot()
	selfMember, _ := head.Core.Member(selfID)
	peerMember, _ := head.Core.Member(head.Core.AuthorityID)
	headHash, err := head.Hash()
	if err != nil {
		t.Fatal(err)
	}
	binding := deviceauth.PeerBinding{Self: selfMember, Peer: peerMember, Head: headHash}

	var clockMu sync.Mutex
	fakeNow := time.Now()
	nowFn := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return fakeNow
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		fakeNow = fakeNow.Add(d)
		clockMu.Unlock()
	}

	adm := ep.admissionOwner()
	adm.nowFn = nowFn
	origAuth := adm.authenticate
	var authCalls atomic.Uint64
	adm.authenticate = func(w devicesec.Wire) (*deviceauth.Authenticated, error) {
		authCalls.Add(1)
		return origAuth(w)
	}

	seal := func(reqID, text string) *pb.Envelope {
		cmd := &pb.Command{
			ProtocolVersion: protocol.PeerVersion,
			Kind:            &pb.Command_Send{Send: &pb.SendRequest{SessionId: "s-phase2", Text: text}},
		}
		body, err := proto.Marshal(cmd)
		if err != nil {
			t.Fatal(err)
		}
		wire, err := client.Prepare(devicesec.Header{
			Purpose:  devicesec.Command,
			Kind:     5,
			Machine:  ep.acct.DeviceID,
			Session:  "s-phase2",
			Request:  reqID,
			Deadline: uint64(nowFn().Add(5 * time.Minute).UnixMilli()),
		}, nil, body)
		if err != nil {
			t.Fatal(err)
		}
		env, err := deviceauth.Envelope(wire)
		if err != nil {
			t.Fatal(err)
		}
		return env
	}

	// 1) True concurrent barrier race across relay + direct.
	raceEnv := seal("barrier-race-req-0001", "barrier-race")
	start := make(chan struct{})
	rCh := make(chan *pb.ForwardReply, 1)
	dCh := make(chan *pb.ForwardReply, 1)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		ep.dispatch(context.Background(), &pb.ForwardCall{CallId: 1, AttemptId: "r", Command: raceEnv}, func(r *pb.ForwardReply) { rCh <- r })
	}()
	go func() {
		defer wg.Done()
		<-start
		ep.dispatchDirect(context.Background(), binding, 1, func() bool { return true }, raceEnv, func(r *pb.ForwardReply) { dCh <- r })
	}()
	close(start)
	wg.Wait()
	r1, r2 := <-rCh, <-dCh
	if openCommandResult(t, client, r1).Outcome != "executed" || openCommandResult(t, client, r2).Outcome != "executed" {
		t.Fatalf("expected both barrier race legs to return executed")
	}
	if got := backend.counts["send:barrier-race"]; got != 1 {
		t.Fatalf("expected exactly 1 execution under concurrent barrier race, got %d", got)
	}

	// 2) Deterministic 10s TTL check: at +5s hits cache (no extra auth), at +11s expires (re-verifies, 0 dup exec).
	before5s := authCalls.Load()
	advance(5 * time.Second)
	ch5 := make(chan *pb.ForwardReply, 1)
	ep.dispatch(context.Background(), &pb.ForwardCall{CallId: 2, AttemptId: "r5", Command: raceEnv}, func(r *pb.ForwardReply) { ch5 <- r })
	if res := openCommandResult(t, client, <-ch5); res == nil || res.Outcome != "executed" {
		t.Fatalf("expected cached replay at +5s to return executed, got %+v", res)
	}
	if got := authCalls.Load(); got != before5s {
		t.Fatalf("expected verifiedCache hit at +5s without extra authenticate call (before=%d after=%d)", before5s, got)
	}

	advance(6 * time.Second) // total +11s > 10s TTL
	ch11 := make(chan *pb.ForwardReply, 1)
	ep.dispatch(context.Background(), &pb.ForwardCall{CallId: 3, AttemptId: "r11", Command: raceEnv}, func(r *pb.ForwardReply) { ch11 <- r })
	if res := openCommandResult(t, client, <-ch11); res == nil || res.Outcome != "executed" {
		t.Fatalf("expected expired replay at +11s to return stored executed result, got %+v", res)
	}
	if got := authCalls.Load(); got != before5s+1 {
		t.Fatalf("expected verifiedCache expiry at +11s to re-authenticate once (before=%d after=%d)", before5s, got)
	}
	if got := backend.counts["send:barrier-race"]; got != 1 {
		t.Fatalf("expired replay must still execute 0 additional times, got %d", got)
	}

	// 3) Per-peer verifiedCache hard bounds: 4,150 unique verified wires must cap at <=4,096 entries and <=4 MiB.
	raceWire, err := deviceauth.FromEnvelope(raceEnv)
	if err != nil {
		t.Fatal(err)
	}
	validAuth, err := origAuth(raceWire)
	if err != nil {
		t.Fatal(err)
	}
	adm.authenticate = func(devicesec.Wire) (*deviceauth.Authenticated, error) {
		return validAuth, nil
	}
	for i := range 4150 {
		w := raceWire
		w.Origin = []byte{byte(i >> 24), byte(i >> 16), byte(i >> 8), byte(i)}
		if _, err := adm.authenticateWithDedup(context.Background(), peerMember.ID, w); err != nil {
			t.Fatalf("iter %d authenticateWithDedup failed: %v", i, err)
		}
	}
	adm.authMu.Lock()
	pc := adm.verifiedPeers[peerMember.ID]
	entryCount := len(pc.entries)
	byteCount := pc.bytes
	adm.authMu.Unlock()
	if entryCount > 4096 {
		t.Fatalf("verifiedCache exceeded per-peer 4096 entry cap: %d", entryCount)
	}
	if byteCount > verifiedCacheMaxBytesPerPeer {
		t.Fatalf("verifiedCache exceeded per-peer 4 MiB byte cap: %d", byteCount)
	}

	// 4) Simulated daemon restart: drop in-memory admission/cache while retaining durable deviceauth accept log.
	adm.close()
	ep.admission = nil
	ep.admissionOnce = sync.Once{}
	newAdm := ep.admissionOwner()
	newAdm.nowFn = nowFn
	var postRestartAuth atomic.Uint64
	origPost := newAdm.authenticate
	newAdm.authenticate = func(w devicesec.Wire) (*deviceauth.Authenticated, error) {
		postRestartAuth.Add(1)
		return origPost(w)
	}
	chRestart := make(chan *pb.ForwardReply, 1)
	ep.dispatch(context.Background(), &pb.ForwardCall{CallId: 4, AttemptId: "restart", Command: raceEnv}, func(r *pb.ForwardReply) { chRestart <- r })
	if res := openCommandResult(t, client, <-chRestart); res == nil || res.Outcome != "executed" {
		t.Fatalf("expected post-restart replay to return durable stored executed result, got %+v", res)
	}
	if postRestartAuth.Load() != 1 {
		t.Fatalf("expected post-restart replay to re-verify signature once, got %d", postRestartAuth.Load())
	}
	if got := backend.counts["send:barrier-race"]; got != 1 {
		t.Fatalf("post-restart replay caused duplicate side effect: count=%d", got)
	}
}
