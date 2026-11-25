package screens

import (
	"strings"
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/gen/timestamppb"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func TestTimelineCacheReuseAndInvalidation(t *testing.T) {
	st := NewState(nil)
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
	in := world.SessionSnapshot{
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
	st := NewState(nil)
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
	in := world.SessionSnapshot{
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
	st := NewState(nil)
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
		vm := BuildSessionView(world.SessionSnapshot{
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
	BuildSessionView(world.SessionSnapshot{Found: true, Session: ls, EventRev: 0}, st, req, ui)
	if ui.HasTimelineCache() {
		t.Fatalf("expected EventRev=0 to release timeline cache")
	}
}

// Receipts of scheduled pushes: who wrote them decides what the line shows,
// and a push sent after the turn ended still sits before its 「本轮用时」.
func TestNotifyReceipts(t *testing.T) {
	const today = int64(1_700_000_000_000)
	ls := &lists.Session{
		ID:        "s1",
		MachineID: "m1",
		Info:      &v1.Session{Id: "s1", Agent: v1.Agent_AGENT_CODEX, Status: v1.SessionStatus_SESSION_STATUS_IDLE},
	}
	system := &v1.Origin{Kind: v1.OriginKind_ORIGIN_KIND_SYSTEM}
	at := func(ms int64) *timestamppb.Timestamp { return &timestamppb.Timestamp{Seconds: ms / 1000} }
	notice := func(seq uint64, origin *v1.Origin, ms int64, body string) *v1.Event {
		return &v1.Event{Seq: seq, TurnId: "t1", Origin: origin, Time: at(ms), Kind: &v1.Event_ScheduledNotification{
			ScheduledNotification: &v1.ScheduledNotification{RunId: "r1", Text: body},
		}}
	}
	turn := func(reply string, tail ...*v1.Event) []*v1.Event {
		evs := []*v1.Event{
			{Seq: 1, TurnId: "t1", Kind: &v1.Event_UserMessage{UserMessage: &v1.UserMessage{Text: "check"}}},
			{Seq: 2, TurnId: "t1", Kind: &v1.Event_TurnStarted{TurnStarted: &v1.TurnStarted{}}},
		}
		if reply != "" {
			evs = append(evs, &v1.Event{Seq: 3, TurnId: "t1", Kind: &v1.Event_AgentMessage{AgentMessage: &v1.AgentMessage{Text: reply}}})
		}
		evs = append(evs, &v1.Event{Seq: 4, TurnId: "t1", Kind: &v1.Event_TurnEnded{TurnEnded: &v1.TurnEnded{Outcome: v1.TurnOutcome_TURN_OUTCOME_COMPLETED, DurationMs: 90_000}}})
		return append(evs, tail...)
	}
	cjk20 := strings.Repeat("通", 20)
	reply := "## 结论\n\n- 最近 24 小时 **13 次**运行全部成功\n- 没有中途 `malformed`\n\n详见 [日志](https://example.com/log)。"
	for _, tc := range []struct {
		name                 string
		events               []*v1.Event
		preview, body        string
		expandable, withDate bool
	}{
		// The computer sends the reply's first 200 characters with the line breaks dropped.
		{name: "computer's copy of a Markdown reply shows only the time", events: turn(reply, notice(5, system, today, string([]rune(strings.ReplaceAll(reply, "\n", ""))[:30])))},
		{name: "computer's fixed sentence after a reply keeps its text", events: turn(reply, notice(5, system, today, "定时任务运行失败：execution_session_exited")), preview: "定时任务运行失败：execution_session_exited", body: "定时任务运行失败：execution_session_exited", expandable: true},
		{name: "computer's push for a turn without reply keeps its text", events: turn("", notice(5, system, today, "定时任务运行已中断。")), preview: "定时任务运行已中断。"},
		{name: "short single line is the whole text", events: turn(reply, notice(5, nil, today, cjk20)), preview: cjk20},
		{name: "over 40 columns can open", events: turn(reply, notice(5, nil, today, cjk20+"知")), preview: cjk20 + "知", body: cjk20 + "知", expandable: true},
		{name: "a line break can open and previews the first line", events: turn(reply, notice(5, nil, today, "2 runs hit malformed\nOne took 2h 40m.\n")), preview: "2 runs hit malformed", body: "2 runs hit malformed\nOne took 2h 40m.", expandable: true},
		{name: "not today writes the date", events: turn(reply, notice(5, system, today-2*86_400_000, "## 结论")), withDate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := NewState(nil)
			st.Cal = lists.Calendar{NowMs: today + 60_000, TodayStartMs: today - 3_600_000, NowYear: 2023, UTCOffsetMinutes: 0}
			vm := BuildSessionView(world.SessionSnapshot{Found: true, Session: ls, Loaded: true, EventRev: 1, Events: tc.events},
				st, &corev1.SessionScreenRequest{SessionId: "s1"}, NewSessionUIState())
			items := vm.GetTimeline().GetItems()
			last := items[len(items)-1]
			if last.GetTurnEnd() == nil {
				t.Fatalf("last item = %v, want the turn's end line after the receipt", last.Kind)
			}
			r := items[len(items)-2].GetNotifyReceipt()
			if r == nil {
				t.Fatalf("item before the turn end = %v, want the receipt", items[len(items)-2].Kind)
			}
			if items[len(items)-2].Seq != last.Seq {
				t.Fatalf("receipt seq = %d, want the turn end's %d to keep paging in order", items[len(items)-2].Seq, last.Seq)
			}
			if r.Label.GetKey() != text.KeySessionNotifyReceipt || r.Preview != tc.preview || r.Body != tc.body || r.Expandable != tc.expandable || r.ScheduledRunId != "r1" {
				t.Fatalf("receipt = %+v, want preview %q body %q expandable %v", r, tc.preview, tc.body, tc.expandable)
			}
			style := corev1.TimestampStyle_TIMESTAMP_STYLE_TIME
			if tc.withDate {
				style = corev1.TimestampStyle_TIMESTAMP_STYLE_MONTH_DAY_TIME
			}
			if got := r.Time.GetTimestamp().GetStyle(); got != style {
				t.Fatalf("time style = %v, want %v", got, style)
			}
		})
	}
}

func TestBuildSessionMenuInterruptReachabilityUnknown(t *testing.T) {
	hasInterrupt := func(items []*corev1.SessionMenuItemView) bool {
		for _, it := range items {
			if it != nil && it.Kind == corev1.SessionMenuItemKind_SESSION_MENU_ITEM_KIND_INTERRUPT {
				return true
			}
		}
		return false
	}

	// Reachability UNKNOWN (e.g. during cold-start / reconnect grace) must keep INTERRUPT menu item visible.
	unknownItems := buildSessionMenuItems(
		true,
		&corev1.SessionHeaderView{
			Exited:               false,
			ComputerOnline:       false,
			ComputerUnlinked:     true,
			ComputerReachability: corev1.ComputerReachability_COMPUTER_REACHABILITY_UNKNOWN,
		},
		&corev1.SessionActionsView{CanInterrupt: false},
	)
	if !hasInterrupt(unknownItems) {
		t.Fatalf("expected INTERRUPT menu item when ComputerReachability == UNKNOWN")
	}

	// Confirmed OFFLINE must hide INTERRUPT menu item.
	offlineItems := buildSessionMenuItems(
		true,
		&corev1.SessionHeaderView{
			Exited:               false,
			ComputerOnline:       false,
			ComputerUnlinked:     false,
			ComputerReachability: corev1.ComputerReachability_COMPUTER_REACHABILITY_OFFLINE,
		},
		&corev1.SessionActionsView{CanInterrupt: false},
	)
	if hasInterrupt(offlineItems) {
		t.Fatalf("expected INTERRUPT menu item omitted when ComputerReachability == OFFLINE")
	}

	// Exited session must hide INTERRUPT menu item even when reachability is UNKNOWN.
	exitedItems := buildSessionMenuItems(
		true,
		&corev1.SessionHeaderView{
			Exited:               true,
			ComputerOnline:       false,
			ComputerReachability: corev1.ComputerReachability_COMPUTER_REACHABILITY_UNKNOWN,
		},
		&corev1.SessionActionsView{CanInterrupt: false},
	)
	if hasInterrupt(exitedItems) {
		t.Fatalf("expected INTERRUPT menu item omitted for exited session")
	}
}
