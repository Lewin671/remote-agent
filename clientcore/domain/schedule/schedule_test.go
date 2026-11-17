package schedule

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/gen/timestamppb"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func testBounds() CalendarBounds {
	// 2026-10-10T00:00:00Z = 1791590400000 ms
	today := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC).UnixMilli()
	day := int64(86_400_000)
	return CalendarBounds{
		Cal: lists.Calendar{
			NowMs:            today + 12*3600*1000,
			TodayStartMs:     today,
			YesterdayStartMs: today - day,
			WeekStartMs:      today - 4*day,
			NowYear:          2026,
		},
		LocalTimezone:           "UTC",
		TomorrowStartMs:         today + day,
		DayAfterTomorrowStartMs: today + 2*day,
	}
}

func TestVisibilityAndGrouping(t *testing.T) {
	cases := []struct {
		name      string
		task      *Task
		hasNew    bool
		visible   bool
		group     TaskGroup
		inbox     InboxGroup
		targetMis bool
	}{
		{
			name:    "active task",
			task:    &Task{ID: "t1", Status: "active"},
			visible: true,
			group:   GroupActive,
			inbox:   InboxRecent,
		},
		{
			name:    "user paused",
			task:    &Task{ID: "t2", Status: "paused", Reason: "user_paused"},
			visible: true,
			group:   GroupPaused,
			inbox:   InboxRecent,
		},
		{
			name:    "system paused attention",
			task:    &Task{ID: "t3", Status: "paused", Reason: "relay_session_limit"},
			visible: true,
			group:   GroupAttention,
			inbox:   InboxAttention,
		},
		{
			name:    "user cancelled hidden",
			task:    &Task{ID: "t4", Status: "cancelled", Reason: "user_cancelled"},
			visible: false,
			group:   GroupEnded,
			inbox:   InboxEnded,
		},
		{
			name:    "system cancelled attention",
			task:    &Task{ID: "t5", Status: "cancelled", Reason: "agent_schedule_permissions_changed"},
			visible: true,
			group:   GroupAttention,
			inbox:   InboxAttention,
		},
		{
			name:    "completed no unread -> ended",
			task:    &Task{ID: "t6", Status: "completed"},
			visible: true,
			group:   GroupEnded,
			inbox:   InboxEnded,
		},
		{
			name:    "completed with unread -> recent",
			task:    &Task{ID: "t7", Status: "completed"},
			hasNew:  true,
			visible: true,
			group:   GroupEnded,
			inbox:   InboxRecent,
		},
		{
			name:      "target missing detected across reasons",
			task:      &Task{ID: "t8", Status: "failed", Reason: "execution_session_exited", Spec: Spec{Target: Target{Type: "resume_session", Session: "s1"}}},
			visible:   true,
			group:   GroupAttention,
			inbox:     InboxAttention,
			targetMis: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Visible(tc.task); got != tc.visible {
				t.Fatalf("Visible = %v, want %v", got, tc.visible)
			}
			if got := GroupOf(tc.task); got != tc.group {
				t.Fatalf("GroupOf = %v, want %v", got, tc.group)
			}
			if got := InboxGroupOf(tc.task, tc.hasNew); got != tc.inbox {
				t.Fatalf("InboxGroupOf = %v, want %v", got, tc.inbox)
			}
			if got := TargetMissing(tc.task); got != tc.targetMis {
				t.Fatalf("TargetMissing = %v, want %v", got, tc.targetMis)
			}
		})
	}
}

func TestNameDerivationAndClipping(t *testing.T) {
	longMsg := strings.Repeat("你", 70) + "。后面一句"
	clipped := ClipName(longMsg)
	if len([]rune(clipped)) != TitleMaxRunes {
		t.Fatalf("rune len = %d, want %d", len([]rune(clipped)), TitleMaxRunes)
	}
	if got := NameInput(strings.Repeat("a", 65)); len([]rune(got)) != TitleMaxRunes {
		t.Fatalf("NameInput len = %d, want %d", len([]rune(got)), TitleMaxRunes)
	}
	if got := ResolveName("", "  Check  build\n\nNext line  "); got != "Check build" {
		t.Fatalf("ResolveName newline = %q, want %q", got, "Check build")
	}
	if got := ResolveName("", "第一句！第二句"); got != "第一句" {
		t.Fatalf("ResolveName Chinese punctuation = %q, want %q", got, "第一句")
	}
	if got := ResolveName("", "First sentence. Second sentence."); got != "First sentence" {
		t.Fatalf("ResolveName English punctuation = %q, want %q", got, "First sentence")
	}
	if got := ResolveName("", "   "); got != "" {
		t.Fatalf("ResolveName fallback = %q, want empty", got)
	}
}

func TestZonesAndTimingText(t *testing.T) {
	b := testBounds()
	if got := CanonicalZone("Asia/Chongqing"); got != "Asia/Shanghai" {
		t.Fatalf("CanonicalZone(Asia/Chongqing) = %q", got)
	}
	sameZone := TimingText(Trigger{
		Type:     "calendar",
		Time:     "08:00",
		Timezone: "UTC",
	}, b)
	if sameZone.GetKey() != text.KeyScheduleDailyAt {
		t.Fatalf("same zone key = %q, want %q", sameZone.GetKey(), text.KeyScheduleDailyAt)
	}

	diffZone := TimingText(Trigger{
		Type:     "calendar",
		Time:     "08:00",
		Timezone: "Asia/Chongqing",
	}, b)
	if diffZone.GetKey() != text.KeyScheduleFrequencyInZone {
		t.Fatalf("diff zone key = %q, want %q", diffZone.GetKey(), text.KeyScheduleFrequencyInZone)
	}
	if len(diffZone.GetArgs()) != 2 || diffZone.GetArgs()[1].GetTextValue().GetKey() != text.KeyScheduleBeijingTime {
		t.Fatalf("diff zone args = %+v", diffZone.GetArgs())
	}
}

func TestRunGroupsAndQuietSpans(t *testing.T) {
	b := testBounds()
	base := b.Cal.TodayStartMs
	runs := []*Run{
		{ID: "r5", Status: "completed", Notified: false, FinalSummary: "ok5", EndedAt: FormatRFC3339Ms(base + 5*3600*1000)},
		{ID: "r4", Status: "completed", Notified: false, FinalSummary: "ok4", EndedAt: FormatRFC3339Ms(base + 4*3600*1000)},
		{ID: "r3", Status: "completed", Notified: false, InputPaused: true, FinalSummary: "paused", EndedAt: FormatRFC3339Ms(base + 3*3600*1000)},
		{ID: "r2", Status: "completed", Notified: false, FinalSummary: "ok2", EndedAt: FormatRFC3339Ms(base + 2*3600*1000)},
		{ID: "r1", Status: "completed", Notified: false, FinalSummary: "ok1", EndedAt: FormatRFC3339Ms(base - 2*3600*1000)},
	}
	groups := RunGroups(runs, b)
	// Expected: [r5, r4, r2] same-day quiet folded; [r3] individual (input_paused); [r1] prev day single quiet
	if len(groups) != 3 {
		t.Fatalf("RunGroups len = %d, want 3", len(groups))
	}
	if !groups[0].Quiet || len(groups[0].Runs) != 3 {
		t.Fatalf("groups[0] = %+v, want folded 3 same-day quiet runs", groups[0])
	}
	if groups[1].Quiet || groups[1].Runs[0].ID != "r3" {
		t.Fatalf("groups[1] = %+v, want non-quiet r3", groups[1])
	}
	if groups[2].Quiet || groups[2].Runs[0].ID != "r1" {
		t.Fatalf("groups[2] = %+v, want unfolded single r1", groups[2])
	}

	events := []*v1.Event{
		{Seq: 1, Time: timestamppbNow(base + 1000), Kind: &v1.Event_UserMessage{UserMessage: &v1.UserMessage{Text: "quiet prompt"}}, Origin: &v1.Origin{ScheduledTaskId: "t1", ScheduledRunId: "r-a"}},
		{Seq: 2, Time: timestamppbNow(base + 2000), Kind: &v1.Event_TurnEnded{TurnEnded: &v1.TurnEnded{ScheduledTaskId: "t1", ScheduledRunId: "r-a", Outcome: v1.TurnOutcome_TURN_OUTCOME_COMPLETED}}},
		{Seq: 3, Time: timestamppbNow(base + 3000), Kind: &v1.Event_UserMessage{UserMessage: &v1.UserMessage{Text: "prompt"}}, Origin: &v1.Origin{ScheduledTaskId: "t1", ScheduledRunId: "r-b"}},
		{Seq: 4, Time: timestamppbNow(base + 4000), Kind: &v1.Event_AgentMessage{AgentMessage: &v1.AgentMessage{Phase: "final_answer", Text: "second answer"}}},
		{Seq: 5, Time: timestamppbNow(base + 5000), Kind: &v1.Event_TurnEnded{TurnEnded: &v1.TurnEnded{ScheduledTaskId: "t1", ScheduledRunId: "r-b", ScheduledNotified: true, Outcome: v1.TurnOutcome_TURN_OUTCOME_COMPLETED}}},
	}
	if ans := FinalAnswer("r-b", events); ans != "second answer" {
		t.Fatalf("FinalAnswer(r-b) = %q, want %q", ans, "second answer")
	}
	spans := QuietSpans(events, b)
	if len(spans) != 1 || spans[0].RunID != "r-a" {
		t.Fatalf("QuietSpans = %+v, want only r-a", spans)
	}
}

func TestTimingDraftValidationAndAfterTurn(t *testing.T) {
	b := testBounds()
	nowMs := b.Cal.NowMs

	d := NewTimingDraft(nowMs, "UTC", b)
	if d.Kind != TimingDaily || d.Time != "08:00" || d.Count != "10" {
		t.Fatalf("unexpected default draft: %+v", d)
	}
	tr, errTxt := d.BuildTrigger(TimingBuildInput{NowMs: nowMs})
	if errTxt != nil || tr.Type != "calendar" || tr.Time != "08:00" {
		t.Fatalf("default BuildTrigger = (%+v, %v)", tr, errTxt)
	}

	// Overflow guard on interval
	badInt := d
	badInt.Kind = TimingInterval
	badInt.Unit = UnitDays
	badInt.Interval = "999999999999999"
	if _, errTxt := badInt.BuildTrigger(TimingBuildInput{NowMs: nowMs}); errTxt == nil || errTxt.GetKey() != text.KeyScheduleIosEnterAValidInterval {
		t.Fatalf("expected overflow error, got %v (max/day=%d)", errTxt, int64(math.MaxInt64/86400))
	}

	// After-turn preserves original when untouched
	orig := Trigger{Type: "after_turn", DelaySeconds: 30, Repeat: true}
	atDraft := TimingDraftFromTrigger(orig, nowMs, "UTC", b)
	if !atDraft.AfterTurnUnselected {
		t.Fatalf("expected AfterTurnUnselected = true")
	}
	atDraft.End = EndCount
	atDraft.Count = "3"
	gotAT, errTxt := atDraft.BuildTrigger(TimingBuildInput{NowMs: nowMs})
	if errTxt != nil || gotAT.Type != "after_turn" || gotAT.DelaySeconds != 30 || !gotAT.Repeat || gotAT.MaxRuns != 3 {
		t.Fatalf("BuildTrigger after_turn = (%+v, %v)", gotAT, errTxt)
	}

	// Shell-provided DST gap flag (works consistently on WASM without tzdata)
	onceDraft := d
	onceDraft.Kind = TimingOnce
	onceDraft.Date = "2026-03-08"
	onceDraft.Time = "02:30"
	onceDraft.Zone = "America/New_York"
	if _, errTxt := onceDraft.BuildTrigger(TimingBuildInput{NowMs: nowMs, AtDSTGap: true}); errTxt == nil || errTxt.GetKey() != text.KeyScheduleDaylightSavingTimeSkipsThisTimeInTheSelected {
		t.Fatalf("expected DST gap error, got %v", errTxt)
	}
}

func TestSeenTrackerLifecycle(t *testing.T) {
	tr := NewSeenTracker()
	t1 := &Task{
		ID:     "t1",
		Status: "active",
		Runs: []Run{
			{ID: "r1", Status: "completed", Notified: true, SessionID: "s1", NotificationSessionID: "s1", NotificationSeq: 5},
			{ID: "r0", Status: "completed", Notified: true, SessionID: "s0", NotificationSessionID: "s0", NotificationSeq: 1},
		},
	}

	// Before seen baseline loads, do not flash false unread badges; once baseline loads, only unseen runs are unread.
	tr.Observe("m1", []*Task{t1}, true)
	if tr.HasNewResult("m1", t1) {
		t.Fatalf("expected false before seen loaded")
	}
	tr.LoadSeen("m1", []SeenToken{{TaskID: "t1", RunID: "r0"}}, true)
	if !tr.HasNewResult("m1", t1) {
		t.Fatalf("expected r1 unread after LoadSeen with only r0")
	}

	// Mark read emits Dismiss for task's notified runs; MarkUnread protects r1 against re-marking when polling same run.
	out := tr.MarkRead("m1", t1)
	if tr.HasNewResult("m1", t1) || len(out.Dismiss) != 2 || out.Dismiss[0].RunID != "r1" {
		t.Fatalf("expected read + dismiss r1/r0 after MarkRead, got (%v, %+v)", tr.HasNewResult("m1", t1), out)
	}
	tr.MarkUnread("m1", t1)
	if !tr.HasNewResult("m1", t1) {
		t.Fatalf("expected unread after MarkUnread")
	}
	tr.Observe("m1", []*Task{t1}, true)
	if !tr.HasNewResult("m1", t1) {
		t.Fatalf("unread guard broken on Observe")
	}

	// OpenTask captures entry unread and marks read; adding new notified run r2 emits Dismiss for r2.
	t1.Runs = append(t1.Runs, Run{ID: "r2", Status: "completed", Notified: true, SessionID: "s2", NotificationSessionID: "s2", NotificationSeq: 8})
	tr.Observe("m1", []*Task{t1}, true)
	tr.ShowList()
	out = tr.OpenTask("m1", "t1")
	if len(out.SaveMachines) != 1 || len(out.Dismiss) != 1 || out.Dismiss[0].RunID != "r2" {
		t.Fatalf("OpenTask outcome = %+v, want dismiss r2", out)
	}
	if !tr.DetailUnread("m1", "t1")["r1"] {
		t.Fatalf("expected entryUnread to preserve r1 during detail visit")
	}
	tr.CloseTask("m1", "t1")
	if tr.HasNewResult("m1", t1) {
		t.Fatalf("expected read after CloseTask")
	}

	// Forget clears machine.
	tr.Forget("m1")
	if len(tr.TokensFor("m1")) != 0 {
		t.Fatalf("expected tokens cleared after Forget")
	}
}

func TestJSONCodec(t *testing.T) {
	spec := Spec{
		Notify:  "agent_only",
		Title:   "Check \"build\" \n\t🚀",
		Message: "Line 1\nLine 2 \\ slash",
		Trigger: Trigger{
			Type:            "calendar",
			Time:            "09:30",
			Weekdays:        []int{1, 3, 5},
			Timezone:        "Asia/Shanghai",
			MaxRuns:         5,
			DelaySeconds:    10,
			IntervalSeconds: 60,
			Repeat:          true,
		},
		Target: Target{
			Type:   "new_session",
			Cwd:    "/Users/me/app",
			Agent:  "codex",
			Model:  "gpt-5.4",
			Effort: "high",
			Mode:   "auto",
		},
		LateSeconds: 120,
	}
	encoded, err := EncodeSpec(spec)
	if err != nil {
		t.Fatalf("EncodeSpec: %v", err)
	}
	var stdSpec Spec
	if err := json.Unmarshal(encoded, &stdSpec); err != nil {
		t.Fatalf("stdlib Unmarshal(EncodeSpec): %v", err)
	}
	if !reflect.DeepEqual(spec, stdSpec) {
		t.Fatalf("EncodeSpec roundtrip mismatch: got %+v, want %+v", stdSpec, spec)
	}

	rawList := []byte(`{
		"extra": {"nested": [1, -2.5e+3, true, false, null, "\u4f60\u597d\ud83d\ude80"]},
		"tasks": [
			{
				"id": "t1",
				"revision": 7,
				"status": "active",
				"source": "client",
				"takeover": {"session": "s-t", "via": "local"},
				"manual_request": {"retry_failed": true, "request_id": "mr-1", "requested_at": "2026-10-10T00:00:00Z"},
				"spec": {"title": "\u65e5\u62a5\ud83d\ude80", "message": "go", "trigger": {"type": "calendar", "time": "08:00", "weekdays": [1, 5], "timezone": "UTC"}, "target": {"type": "new_session", "cwd": "/Users/me/app"}},
				"runs": [{"id": "r1", "status": "completed", "notified": true, "notification_seq": 3, "start_seq": 1}],
				"active_run": null,
				"unknown_field": {"k": [1, 2]}
			}
		]
	}`)
	tasks, err := DecodeTaskList(rawList)
	if err != nil || len(tasks) != 1 || tasks[0].Spec.Title != "日报🚀" || tasks[0].Takeover.Via != "local" {
		t.Fatalf("DecodeTaskList = (%+v, %v)", tasks, err)
	}
	if _, err := DecodeTaskList([]byte(`{"other":[]}`)); err == nil {
		t.Fatalf("expected error on missing tasks field")
	}
	if _, err := DecodeTask([]byte(`{"id":"t1"}{"id":"t2"}`)); err == nil {
		t.Fatalf("expected error on trailing JSON")
	}
}

func timestamppbNow(ms int64) *timestamppb.Timestamp {
	return &timestamppb.Timestamp{Seconds: ms / 1000, Nanos: int32((ms % 1000) * 1_000_000)}
}
