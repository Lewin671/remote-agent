package relay_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
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

// TestProbeLatencyEchoBypassesFullOrdinaryPoolAndReportsTiming verifies:
//  1. Echo mode (work=false) bypasses a saturated Ordinary scheduler pool and
//     returns immediately with execute_us == 0 and process_us >= queue_us.
//  2. Work mode (work=true) waits in the Ordinary pool, executes the GetSettings
//     read path, and self-reports queue_us, execute_us, and process_us >= queue_us + execute_us
//     under an injected deterministic clock.
//  3. Non-empty session_id is rejected as invalid_argument, and existing GetSettings
//     responses remain unaffected.
func TestProbeLatencyEchoBypassesFullOrdinaryPoolAndReportsTiming(t *testing.T) {
	e := setup(t)
	e.backend.add(t, "sess1")

	base := time.Now().Truncate(time.Millisecond)
	var offsetUs atomic.Int64
	clockNow := func() time.Time {
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

	e.startRelayBackend(pbBackend, func(r *relay.Relay) {
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
	echoEnv, err := e.client.Seal(s.MachineID, "", remoteclient.NewRequestID(), time.Now().Add(time.Minute), &pb.Command{
		Kind: &pb.Command_ProbeLatency{ProbeLatency: &pb.ProbeLatencyRequest{Work: false}},
	})
	if err != nil {
		t.Fatal(err)
	}
	echoRes, err := e.client.Forward(ctx, s.MachineID, echoEnv)
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
	if echoPayload.ExecuteUs != 0 {
		t.Fatalf("expected echo execute_us=0, got %d", echoPayload.ExecuteUs)
	}
	if echoPayload.ProcessUs < echoPayload.QueueUs || echoPayload.QueueUs < 0 {
		t.Fatalf("invalid echo timing invariants: %+v", echoPayload)
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

	// Wait briefly to confirm work probe is queued and cannot complete before slots release.
	select {
	case out := <-workCh:
		t.Fatalf("work probe returned while Ordinary pool was full: res=%+v err=%v", out.res, out.err)
	case <-time.After(120 * time.Millisecond):
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
	if wp.QueueUs < 120_000 {
		t.Fatalf("expected work queue_us >= 120000 (slot wait), got %d", wp.QueueUs)
	}
	if wp.ExecuteUs != 35_000 {
		t.Fatalf("expected work execute_us == 35000, got %d", wp.ExecuteUs)
	}
	if wp.ProcessUs < wp.QueueUs+wp.ExecuteUs {
		t.Fatalf("expected process_us (%d) >= queue_us (%d) + execute_us (%d)", wp.ProcessUs, wp.QueueUs, wp.ExecuteUs)
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
