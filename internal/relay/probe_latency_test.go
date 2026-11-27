package relay_test

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"github.com/Lewin671/remote-agent/internal/transport"
	"google.golang.org/protobuf/proto"
)

type probeLatencyBackend struct {
	*fakeBackend
	onGetSettings func(context.Context) error
}

type probeLatencyLocal struct {
	remoteagentv1connect.LocalServiceHandler
	b *probeLatencyBackend
}

func (b *probeLatencyBackend) Local() remoteagentv1connect.LocalServiceHandler {
	return probeLatencyLocal{b.fakeBackend.Local(), b}
}

func (l probeLatencyLocal) GetSettings(ctx context.Context, _ *connect.Request[pb.GetSettingsRequest]) (*connect.Response[pb.GetSettingsResponse], error) {
	if l.b.onGetSettings != nil {
		if err := l.b.onGetSettings(ctx); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(&pb.GetSettingsResponse{
		Settings: &pb.DaemonSettings{ApprovalTimeoutSeconds: 600},
	}), nil
}

func (l probeLatencyLocal) UpdateSettings(_ context.Context, req *connect.Request[pb.UpdateSettingsRequest]) (*connect.Response[pb.UpdateSettingsResponse], error) {
	return connect.NewResponse(&pb.UpdateSettingsResponse{
		Settings: req.Msg.GetSettings(),
	}), nil
}

// TestProbeLatencyEchoBypassesFullOrdinaryPoolAndReportsTiming verifies:
//  1. Echo mode (work=false) bypasses a saturated Ordinary scheduler pool and
//     reports exact deterministic queue_us, execute_us == 0, and process_us under
//     the injected clock.
//  2. Work mode (work=true) queues behind the saturated Ordinary pool (confirmed
//     via scheduler queue depth, without wall-clock sleeps), executes the
//     GetSettings read path, and self-reports deterministic queue_us, execute_us,
//     and process_us.
//  3. Non-empty session_id is rejected as invalid_argument, and existing GetSettings
//     responses remain unaffected.
func TestProbeLatencyEchoBypassesFullOrdinaryPoolAndReportsTiming(t *testing.T) {
	e := setup(t)
	e.backend.add(t, "sess1")

	base := time.Now().Truncate(time.Millisecond)
	var offsetUs atomic.Int64
	var stepOnNowUs atomic.Int64
	clockNow := func() time.Time {
		if step := stepOnNowUs.Load(); step != 0 {
			return base.Add(time.Duration(offsetUs.Add(step)) * time.Microsecond)
		}
		return base.Add(time.Duration(offsetUs.Load()) * time.Microsecond)
	}

	var settingsCalls atomic.Int32
	pbBackend := &probeLatencyBackend{
		fakeBackend: e.backend,
		onGetSettings: func(context.Context) error {
			settingsCalls.Add(1)
			offsetUs.Add(35_000) // simulate 35ms backend GetSettings read
			return nil
		},
	}

	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	e.backend.normalResult = func(ctx context.Context) error {
		entered <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	var runningRelay *relay.Relay
	e.startRelayBackend(pbBackend, func(r *relay.Relay) {
		runningRelay = r
		relay.SetSchedulerClockForTest(r, clockNow, nil)
	})
	s := e.session(t, "sess1", 0)
	e.online(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Fill all 4 Ordinary scheduler slots.
	ordinaryDone := make(chan error, 4)
	for i := range 4 {
		env, err := e.client.Seal(s.MachineID, s.ID, remoteclient.NewRequestID(), time.Now().Add(time.Minute), &pb.Command{
			Kind: &pb.Command_RenameSession{RenameSession: &pb.RenameSessionRequest{SessionId: s.ID, Title: fmt.Sprintf("fill-%d", i)}},
		})
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			_, err := e.client.Forward(ctx, s.MachineID, env)
			ordinaryDone <- err
		}()
	}
	for range 4 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("ordinary slots did not fill")
		}
	}

	// 1. Echo probe must return immediately while all 4 Ordinary slots are blocked.
	// With all 4 fillers parked inside the backend, the echo triggers 5 deterministic
	// admission clock reads (dispatchRoute receivedAt, authenticateLoop authStartAt,
	// authenticateWithDedup before/after auth, and replyResult now). Stepping +2000us
	// per read pins queue_us=2000, execute_us=0, process_us=8000 deterministically.
	stepOnNowUs.Store(2_000)
	echoEnv, err := e.client.Seal(s.MachineID, "", remoteclient.NewRequestID(), time.Now().Add(time.Minute), &pb.Command{
		Kind: &pb.Command_ProbeLatency{ProbeLatency: &pb.ProbeLatencyRequest{Work: false}},
	})
	if err != nil {
		t.Fatal(err)
	}
	echoRes, err := e.client.Forward(ctx, s.MachineID, echoEnv)
	stepOnNowUs.Store(0)
	if err != nil {
		t.Fatalf("echo probe blocked or failed while Ordinary pool full: %v", err)
	}
	if echoRes.Outcome != "executed" || echoRes.ErrorCode != "" {
		t.Fatalf("unexpected echo result: %+v", echoRes)
	}
	echoPayload := echoRes.GetProbeLatency()
	if echoPayload == nil {
		t.Fatalf("expected ProbeLatencyResponse on echo, got %+v", echoRes)
	}
	if echoPayload.QueueUs != 2_000 || echoPayload.ExecuteUs != 0 || echoPayload.ProcessUs != 8_000 {
		t.Fatalf("expected deterministic echo timing (queue=2000, execute=0, process=8000), got %+v", echoPayload)
	}
	if got := settingsCalls.Load(); got != 0 {
		t.Fatalf("echo probe must not invoke GetSettings, got %d calls", got)
	}

	// 2. Work probe must queue behind the saturated Ordinary pool, then report queue + execute timing.
	workEnv, err := e.client.Seal(s.MachineID, "", remoteclient.NewRequestID(), time.Now().Add(time.Minute), &pb.Command{
		Kind: &pb.Command_ProbeLatency{ProbeLatency: &pb.ProbeLatencyRequest{Work: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	type workOut struct {
		res *pb.CommandResult
		err error
	}
	workCh := make(chan workOut, 1)
	go func() {
		r, err := e.client.Forward(ctx, s.MachineID, workEnv)
		workCh <- workOut{r, err}
	}()

	// Wait for the work probe to be enqueued in the Ordinary scheduler queue (no wall-clock sleep).
	for relay.QueuedCallsForTest(runningRelay)[transport.Ordinary] < 1 {
		select {
		case out := <-workCh:
			t.Fatalf("work probe returned while Ordinary pool was full: res=%+v err=%v", out.res, out.err)
		case <-ctx.Done():
			t.Fatal("timed out waiting for work probe to enter Ordinary scheduler queue")
		default:
			runtime.Gosched()
		}
	}
	select {
	case out := <-workCh:
		t.Fatalf("work probe returned before Ordinary pool released: res=%+v err=%v", out.res, out.err)
	default:
	}

	// Advance deterministic clock by 120ms while the work probe is queued, then release Ordinary slots.
	offsetUs.Add(120_000)
	once.Do(func() { close(release) })

	for range 4 {
		if err := <-ordinaryDone; err != nil {
			t.Fatalf("ordinary filler failed: %v", err)
		}
	}
	out := <-workCh
	if out.err != nil {
		t.Fatalf("work probe failed: %v", out.err)
	}
	if out.res.Outcome != "executed" || out.res.ErrorCode != "" {
		t.Fatalf("unexpected work result: %+v", out.res)
	}
	wp := out.res.GetProbeLatency()
	if wp == nil {
		t.Fatalf("expected ProbeLatencyResponse on work probe, got %+v", out.res)
	}
	if wp.QueueUs != 120_000 {
		t.Fatalf("expected work queue_us == 120000 (slot wait), got %d", wp.QueueUs)
	}
	if wp.ExecuteUs != 35_000 {
		t.Fatalf("expected work execute_us == 35000, got %d", wp.ExecuteUs)
	}
	if wp.ProcessUs != 155_000 {
		t.Fatalf("expected work process_us == 155000, got %d", wp.ProcessUs)
	}
	if got := settingsCalls.Load(); got != 1 {
		t.Fatalf("expected work probe to invoke GetSettings once, got %d", got)
	}

	// 3. Machine-scoped probe_latency with a session ID is rejected cleanly as invalid_argument.
	badEchoEnv, err := e.client.Seal(s.MachineID, s.ID, remoteclient.NewRequestID(), time.Now().Add(time.Minute), &pb.Command{
		Kind: &pb.Command_ProbeLatency{ProbeLatency: &pb.ProbeLatencyRequest{Work: false}},
	})
	if err != nil {
		t.Fatal(err)
	}
	badEchoRes, err := e.client.Forward(ctx, s.MachineID, badEchoEnv)
	if err == nil || badEchoRes.GetErrorCode() != "invalid_argument" || badEchoRes.GetProbeLatency() != nil {
		t.Fatalf("expected invalid_argument for session-scoped probe_latency, got res=%+v err=%v", badEchoRes, err)
	}

	// 4. Existing GetSettings command still returns clean GetSettingsResponse (no unknown fields, old clients unaffected).
	gsEnv, err := e.client.Seal(s.MachineID, "", remoteclient.NewRequestID(), time.Now().Add(time.Minute), &pb.Command{
		Kind: &pb.Command_GetSettings{GetSettings: &pb.GetSettingsRequest{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	gsRes, err := e.client.Forward(ctx, s.MachineID, gsEnv)
	if err != nil {
		t.Fatalf("GetSettings failed: %v", err)
	}
	if gsRes.GetGetSettings() == nil || gsRes.GetProbeLatency() != nil {
		t.Fatalf("unexpected GetSettings result: %+v", gsRes)
	}
	raw, err := proto.Marshal(gsRes)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip pb.CommandResult
	if err := proto.Unmarshal(raw, &roundTrip); err != nil || len(roundTrip.ProtoReflect().GetUnknown()) != 0 {
		t.Fatalf("GetSettings round-trip failed or had unknown fields: err=%v", err)
	}
}

// TestProbeLatencyEchoNegativeAdmissionCases verifies that probe_latency(work=false)
// still enforces full security and channel admission guards:
//  1. Removed member is rejected with member_removed / permission_denied.
//  2. Expired deadline is refused and never executes echo.
//  3. Reusing a previously accepted request_id with a different payload (echo) returns identity_conflict.
//  4. Echo arriving on the bulk direct channel (directUse=2) is rejected with path_unavailable.
func TestProbeLatencyEchoNegativeAdmissionCases(t *testing.T) {
	ctx := context.Background()

	t.Run("removed_member", func(t *testing.T) {
		h := relay.NewProbeEchoHarnessForTest(t, &probeLatencyBackend{fakeBackend: newFakeBackend(t)})
		h.RemoveSecondMember(t)

		env := h.SealCommand(t, h.SecondClient(), "echo-removed-member-0001", "", time.Now().Add(time.Minute), &pb.Command{
			Kind: &pb.Command_ProbeLatency{ProbeLatency: &pb.ProbeLatencyRequest{Work: false}},
		})
		rep := h.DispatchRelay(ctx, env)
		res := h.OpenResult(t, h.AuthorityClient(), rep)
		if res == nil || res.Outcome != "rejected" || res.Reason != "member_removed" || res.ErrorCode != "permission_denied" || res.GetProbeLatency() != nil {
			t.Fatalf("expected member_removed/permission_denied for removed member echo, got rep=%+v res=%+v", rep, res)
		}
	})

	t.Run("expired_deadline", func(t *testing.T) {
		h := relay.NewProbeEchoHarnessForTest(t, &probeLatencyBackend{fakeBackend: newFakeBackend(t)})

		env := h.SealCommand(t, h.AuthorityClient(), "echo-expired-deadline-0001", "", time.Now().Add(-time.Minute), &pb.Command{
			Kind: &pb.Command_ProbeLatency{ProbeLatency: &pb.ProbeLatencyRequest{Work: false}},
		})
		rep := h.DispatchRelay(ctx, env)
		res := h.OpenResult(t, h.AuthorityClient(), rep)
		if res == nil || res.Outcome == "executed" || res.GetProbeLatency() != nil {
			t.Fatalf("expected expired echo to be refused without executing, got rep=%+v res=%+v", rep, res)
		}
		if res.Outcome != "unknown" || res.Reason != "outcome_unknown" {
			t.Fatalf("expected unknown/outcome_unknown for expired echo, got %+v", res)
		}
	})

	t.Run("same_request_id_replay", func(t *testing.T) {
		h := relay.NewProbeEchoHarnessForTest(t, &probeLatencyBackend{fakeBackend: newFakeBackend(t)})
		const sharedReqID = "echo-replay-req-id-0001"

		// First execute a mutating machine-scoped command so sharedReqID is persisted in Accepted.
		mutEnv := h.SealCommand(t, h.AuthorityClient(), sharedReqID, "", time.Now().Add(time.Minute), &pb.Command{
			Kind: &pb.Command_UpdateSettings{UpdateSettings: &pb.UpdateSettingsRequest{
				Settings: &pb.DaemonSettings{ApprovalTimeoutSeconds: 300},
			}},
		})
		mutRep := h.DispatchRelay(ctx, mutEnv)
		mutRes := h.OpenResult(t, h.AuthorityClient(), mutRep)
		if mutRes == nil || mutRes.Outcome != "executed" {
			t.Fatalf("expected initial UpdateSettings to execute, got rep=%+v res=%+v", mutRep, mutRes)
		}

		// Replay the same request_id with an echo payload -> must fail with identity_conflict.
		echoEnv := h.SealCommand(t, h.AuthorityClient(), sharedReqID, "", time.Now().Add(time.Minute), &pb.Command{
			Kind: &pb.Command_ProbeLatency{ProbeLatency: &pb.ProbeLatencyRequest{Work: false}},
		})
		echoRep := h.DispatchRelay(ctx, echoEnv)
		echoRes := h.OpenResult(t, h.AuthorityClient(), echoRep)
		if echoRes == nil || echoRes.Outcome != "conflict" || echoRes.Reason != "identity_conflict" || echoRes.ConflictKind != "identity" || echoRes.GetProbeLatency() != nil {
			t.Fatalf("expected conflict/identity_conflict on same request_id echo replay, got rep=%+v res=%+v", echoRep, echoRes)
		}
	})

	t.Run("bulk_direct_channel", func(t *testing.T) {
		h := relay.NewProbeEchoHarnessForTest(t, &probeLatencyBackend{fakeBackend: newFakeBackend(t)})

		bulkEchoEnv := h.SealCommand(t, h.AuthorityClient(), "echo-bulk-direct-0001", "", time.Now().Add(time.Minute), &pb.Command{
			Kind: &pb.Command_ProbeLatency{ProbeLatency: &pb.ProbeLatencyRequest{Work: false}},
		})
		bulkRep := h.DispatchDirect(ctx, 2, bulkEchoEnv)
		bulkRes := h.OpenResult(t, h.AuthorityClient(), bulkRep)
		if bulkRes == nil || bulkRes.Outcome != "unknown" || bulkRes.Reason != "path_unavailable" || bulkRes.GetProbeLatency() != nil {
			t.Fatalf("expected unknown/path_unavailable for echo on bulk direct channel (use=2), got rep=%+v res=%+v", bulkRep, bulkRes)
		}

		// Sanity check: echo on read/control direct channel (use=1) succeeds.
		readEchoEnv := h.SealCommand(t, h.AuthorityClient(), "echo-read-direct-0001", "", time.Now().Add(time.Minute), &pb.Command{
			Kind: &pb.Command_ProbeLatency{ProbeLatency: &pb.ProbeLatencyRequest{Work: false}},
		})
		readRep := h.DispatchDirect(ctx, 1, readEchoEnv)
		readRes := h.OpenResult(t, h.AuthorityClient(), readRep)
		if readRes == nil || readRes.Outcome != "executed" || readRes.GetProbeLatency() == nil {
			t.Fatalf("expected executed for echo on control direct channel (use=1), got rep=%+v res=%+v", readRep, readRes)
		}
	})
}
