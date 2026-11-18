package screens

import (
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

func TestTimelineCacheReuseAndInvalidation(t *testing.T) {
	st := NewState()
	st.Cal = lists.Calendar{NowMs: 1_700_000_000_000}
	ls := &lists.Session{
		ID:        "s1",
		MachineID: "m1",
		Info:      &v1.Session{Id: "s1", Agent: v1.Agent_AGENT_CODEX, Status: v1.SessionStatus_SESSION_STATUS_RUNNING},
	}
	events := []*v1.Event{
		{Seq: 1, SessionId: "s1", Kind: &v1.Event_UserMessage{UserMessage: &v1.UserMessage{Text: "hello"}}},
		{Seq: 2, SessionId: "s1", Kind: &v1.Event_AgentMessage{AgentMessage: &v1.AgentMessage{Text: "world"}}},
	}
	in := SessionViewInput{
		Found:    true,
		Session:  ls,
		Loaded:   true,
		EventRev: 1,
		Events:   events,
	}
	ui := NewSessionUIState()
	req := &corev1.SessionScreenRequest{SessionId: "s1", Window: &corev1.SessionWindowRequest{Limit: 80}}

	vm1 := BuildSessionView(in, st, req, ui)
	if len(vm1.Timeline.Items) != 2 || ui.timeline == nil {
		t.Fatalf("expected 2 timeline items and populated cache")
	}
	firstItemPtr := ui.timeline.rawItems[0]

	// Same EventRev and inputs must reuse cached rawItems without rebuilding.
	vm2 := BuildSessionView(in, st, req, ui)
	if ui.timeline.rawItems[0] != firstItemPtr || len(vm2.Timeline.Items) != 2 {
		t.Fatalf("expected cached rawItems pointer to be reused on unchanged EventRev")
	}

	// UnavailableCount change must invalidate cache even if EventRev were unchanged.
	inUnavail := in
	inUnavail.UnavailableCount = 1
	BuildSessionView(inUnavail, st, req, ui)
	if ui.timeline.rawItems[0] == firstItemPtr {
		t.Fatalf("expected UnavailableCount change to invalidate timeline cache")
	}

	// EventRev bump must invalidate cache and reflect updated events.
	firstItemPtr2 := ui.timeline.rawItems[0]
	inRev2 := in
	inRev2.EventRev = 2
	inRev2.Events = append(events, &v1.Event{Seq: 3, SessionId: "s1", Kind: &v1.Event_AgentMessage{AgentMessage: &v1.AgentMessage{Text: "next"}}})
	vm3 := BuildSessionView(inRev2, st, req, ui)
	if ui.timeline.rawItems[0] == firstItemPtr2 || len(vm3.Timeline.Items) != 3 {
		t.Fatalf("expected EventRev bump to rebuild timeline items (len=%d)", len(vm3.Timeline.Items))
	}
}

func TestBuildTimelineResolvesCompletedToolOnTailSlice(t *testing.T) {
	st := NewState()
	st.Cal = lists.Calendar{NowMs: 1_700_000_000_000}
	ls := &lists.Session{
		ID:        "s1",
		MachineID: "m1",
		Info:      &v1.Session{Id: "s1", Agent: v1.Agent_AGENT_CODEX, Status: v1.SessionStatus_SESSION_STATUS_RUNNING},
	}
	// Tail-first slice starting at seq 501 (index 0 != seq-1): exercises binary search in findEventBySeq.
	events := []*v1.Event{
		{Seq: 501, SessionId: "s1", Kind: &v1.Event_ToolStarted{ToolStarted: &v1.ToolStarted{CallId: "c1", Title: "rg"}}},
		{Seq: 502, SessionId: "s1", Kind: &v1.Event_AgentMessage{AgentMessage: &v1.AgentMessage{Text: "mid"}}},
		{Seq: 503, SessionId: "s1", Kind: &v1.Event_ToolCall{ToolCall: &v1.ToolCall{CallId: "c1", Title: "rg", Output: "ok"}}},
	}
	in := SessionViewInput{
		Found:         true,
		Session:       ls,
		TailRequested: true,
		EventRev:      1,
		Events:        events,
		Projection: &corev1.ProjectionState{
			CompletedAt: map[uint64]uint64{501: 503},
		},
	}
	vm := BuildSessionView(in, st, &corev1.SessionScreenRequest{SessionId: "s1"}, NewSessionUIState())
	items := vm.GetTimeline().GetItems()
	if len(items) != 2 {
		t.Fatalf("expected 2 items (completed tool placed at 501 + message 502), got %d", len(items))
	}
	if items[0].GetToolCall() == nil || items[0].GetToolCall().CallId != "c1" || items[0].Seq != 503 {
		t.Fatalf("expected completed tool c1 placed at start index, got %+v", items[0])
	}
}

func TestTimelineReportsOfflineForOfflineAndNoNetworkAndClearsCacheOnZeroRev(t *testing.T) {
	st := NewState()
	st.Cal = lists.Calendar{NowMs: 1_700_000_000_000}
	ls := &lists.Session{
		ID:        "s1",
		MachineID: "m1",
		Info:      &v1.Session{Id: "s1", Agent: v1.Agent_AGENT_CODEX, Status: v1.SessionStatus_SESSION_STATUS_RUNNING},
	}
	events := []*v1.Event{
		{Seq: 1, SessionId: "s1", Kind: &v1.Event_UserMessage{UserMessage: &v1.UserMessage{Text: "hi"}}},
	}
	req := &corev1.SessionScreenRequest{SessionId: "s1"}
	ui := NewSessionUIState()

	for _, tc := range []struct {
		conn lists.ConnectionKind
		want bool
	}{
		{lists.ConnOffline, true},
		{lists.ConnNoNetwork, true},
		{lists.ConnConnecting, false},
		{lists.ConnConnected, false},
	} {
		vm := BuildSessionView(SessionViewInput{
			Found:         true,
			Session:       ls,
			Conn:          tc.conn,
			TailRequested: true,
			EventRev:      1,
			Events:        events,
		}, st, req, ui)
		if got := vm.GetTimeline().GetOffline(); got != tc.want {
			t.Fatalf("conn=%v: Offline=%v, want %v", tc.conn, got, tc.want)
		}
		if !vm.GetTimeline().GetTailRequested() {
			t.Fatalf("conn=%v: expected TailRequested=true", tc.conn)
		}
	}
	if !ui.HasTimelineCache() {
		t.Fatalf("expected timeline cache populated for EventRev=1")
	}

	// EventRev == 0 (no active EventBuffer) must clear any stale retained timeline cache.
	BuildSessionView(SessionViewInput{Found: true, Session: ls, EventRev: 0}, st, req, ui)
	if ui.HasTimelineCache() {
		t.Fatalf("expected EventRev=0 to release timeline cache")
	}
}
