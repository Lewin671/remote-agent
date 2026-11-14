package remoteclient

import (
	"context"
	"errors"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
)

// A deterministic wall/elapsed clock and idle scheduler. All accesses occur
// under the registry lock; no real timer or sleep is needed for long polling.
func historyClock(c *Client) (*dispatchRegistry, *int64, *int64, *[]func()) {
	r := c.dispatchRegistry()
	wall, elapsed := time.Now().UnixMilli(), int64(0)
	callbacks := []func(){}
	r.clock = func() (int64, int64) { return wall, elapsed }
	r.schedule = func(run func()) func() { callbacks = append(callbacks, run); return func() {} }
	return r, &wall, &elapsed, &callbacks
}
func pollEnvelope(t *testing.T, c *Client, mid string, deadline int64) *pb.Envelope {
	t.Helper()
	env, err := c.Seal(mid, "s", NewRequestID(), time.UnixMilli(deadline), &pb.Command{Kind: &pb.Command_GetLiveSnapshot{GetLiveSnapshot: &pb.GetLiveSnapshotRequest{SessionId: "s"}}})
	if err != nil {
		t.Fatal(err)
	}
	return env
}
func TestPollingDispatchHistoryIsBoundedAndReapedWhileIdle(t *testing.T) {
	c, _, mid := syntheticTerminalSecurity(t)
	r, wall, elapsed, callbacks := historyClock(c)
	for range 2000 {
		env := pollEnvelope(t, c, mid, *wall+60_000)
		call := c.openCall(env, nil)
		call.delivery().dispatch()
		call.close()
		*wall += 5_000
		*elapsed += 5_000
	}
	limit := int((time.Minute+deviceauth.ResultRetention)/(5*time.Second)) + 1
	if len(r.entries) > limit {
		t.Fatalf("polling retained %d histories, bound %d", len(r.entries), limit)
	}
	if len(*callbacks) == 0 {
		t.Fatal("idle history cleanup was not scheduled")
	}
	*wall += (time.Minute + deviceauth.ResultRetention).Milliseconds()
	*elapsed += (time.Minute + deviceauth.ResultRetention).Milliseconds()
	(*callbacks)[len(*callbacks)-1]()
	if len(r.entries) != 0 || r.cancel != nil {
		t.Fatal("idle registry retained expired polling histories", len(r.entries))
	}
}
func TestDispatchHistoryPreservesWindowAndActiveCalls(t *testing.T) {
	c, _, mid := syntheticTerminalSecurity(t)
	r, wall, elapsed, _ := historyClock(c)
	start := *wall
	deadline := start + 60_000
	window := (time.Minute + deviceauth.ResultRetention).Milliseconds()
	env := pollEnvelope(t, c, mid, deadline)
	first := c.openCall(env, nil)
	first.delivery().dispatch()
	first.close()
	*wall = start + window - 1
	*elapsed = window - 1
	r.sweep()
	if len(r.entries) != 1 {
		t.Fatal("unresolved history removed before recovery window ended")
	}
	active := c.openCall(env, nil)
	refused := active.delivery()
	refused.dispatch()
	refused.notForwarded()
	if !active.uncertain() {
		t.Fatal("matching refusal erased original unresolved attempt")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if len(r.entries) != 1 {
		t.Fatal("Close discarded an outstanding authorization")
	}
	*wall = start + window + 1
	*elapsed = window + 1
	r.sweep()
	if len(r.entries) != 1 {
		t.Fatal("expired history removed with an active call")
	}
	active.close()
	if len(r.entries) != 0 || !active.uncertain() {
		t.Fatal("closed expired history retained or its caller lost evidence")
	}
	// The endpoint is closed, but the low-level provenance model must still
	// preserve the old uncertainty if a late call gets a local/raw failure.
	*wall = start
	reopened := c.openCall(env, []DispatchEvidence{NeverDispatched})
	if !reopened.uncertain() {
		t.Fatal("clock rollback recreated fresh provenance after cleanup")
	}
	reopened.close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Forward(ctx, mid, env, NeverDispatched)
	if !Uncertain(err) || !errors.Is(err, context.Canceled) {
		t.Fatal("raw preflight failure lost expired authorization uncertainty", err)
	}
}
func TestDispatchHistoryCleanupRequiresBothClocks(t *testing.T) {
	c, _, mid := syntheticTerminalSecurity(t)
	r, wall, elapsed, _ := historyClock(c)
	start := *wall
	window := (time.Minute + deviceauth.ResultRetention).Milliseconds()
	original := pollEnvelope(t, c, mid, start+60_000)
	header, err := deviceauth.ParseHeader(original.SecurityHeader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Seal(mid, "s", header.Request, time.UnixMilli(start+30_000), &pb.Command{Kind: &pb.Command_GetLiveSnapshot{GetLiveSnapshot: &pb.GetLiveSnapshotRequest{SessionId: "s"}}}); err != nil {
		t.Fatal(err)
	}
	*wall = start + window + 1
	r.sweep()
	if len(r.entries) != 1 {
		t.Fatal("wall-clock jump prematurely removed history")
	}
	*wall = start
	*elapsed = window + 1
	r.sweep()
	if len(r.entries) != 1 {
		t.Fatal("elapsed time alone prematurely removed history")
	}
	*wall = start + window - 1
	r.sweep()
	if len(r.entries) != 1 {
		t.Fatal("a later copy shortened the known authorization window")
	}
	*wall = start + window + 1
	r.sweep()
	if len(r.entries) != 0 {
		t.Fatal("eligible history was not removed")
	}
}
