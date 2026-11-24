package app

import (
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func seedTermHistoryCore() *Core {
	c := New()
	c.Dispatch(&corev1.Event{
		RandomIds: []string{"th-1", "th-2", "th-3", "th-4", "th-5", "th-6", "th-7", "th-8"},
	})
	return c
}

func findTermHistorySendReq(batch *corev1.EffectBatch) *corev1.TerminalHistorySendRequestEffect {
	for _, ef := range batch.GetEffects() {
		if s := ef.GetTerminalHistorySendRequest(); s != nil {
			return s
		}
	}
	return nil
}

func findTermHistoryUpdated(batch *corev1.EffectBatch) *corev1.TerminalHistoryUpdatedEffect {
	for _, ef := range batch.GetEffects() {
		if u := ef.GetTerminalHistoryUpdated(); u != nil {
			return u
		}
	}
	return nil
}

func findTermHistoryScheduleTimer(batch *corev1.EffectBatch, kind corev1.TimerKind, key string) *corev1.ScheduleTimerEffect {
	for _, ef := range batch.GetEffects() {
		if t := ef.GetScheduleTimer(); t != nil && t.Kind == kind && t.Key == key {
			return t
		}
	}
	return nil
}

func findTermHistoryCancelTimer(batch *corev1.EffectBatch, kind corev1.TimerKind, key string) *corev1.CancelTimerEffect {
	for _, ef := range batch.GetEffects() {
		if t := ef.GetCancelTimer(); t != nil && t.Kind == kind && t.Key == key {
			return t
		}
	}
	return nil
}

func TestCoreTerminalHistoryLifecycleAndTimeout(t *testing.T) {
	c := seedTermHistoryCore()
	const tid = "term-42"

	// 1. Open -> emits send request, 10s timer, and OPENING status text.
	b := c.Dispatch(&corev1.Event{
		Event: &corev1.Event_TerminalHistory{
			TerminalHistory: &corev1.TerminalHistoryEvent{
				TerminalId: tid,
				Kind:       corev1.TerminalHistoryEventKind_TERMINAL_HISTORY_EVENT_KIND_OPEN,
			},
		},
	})
	req := findTermHistorySendReq(b)
	upd := findTermHistoryUpdated(b)
	tm := findTermHistoryScheduleTimer(b, corev1.TimerKind_TIMER_KIND_TERMINAL_HISTORY_TIMEOUT, tid)
	if req == nil || req.TerminalId != tid || req.RequestId != "th-1" || req.Cursor != "" {
		t.Fatalf("unexpected send request: %#v", req)
	}
	if tm == nil || tm.DelayMs != 10_000 {
		t.Fatalf("expected 10s timeout timer, got %#v", tm)
	}
	if upd == nil || upd.Phase != corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_OPENING ||
		upd.StatusText.GetKey() != text.KeyTerminalReading || !upd.ResetPages || !upd.Pending {
		t.Fatalf("unexpected updated effect: %#v", upd)
	}

	// 2. Receive first page -> cancels timer, parses SGR runs once, opens reader.
	b = c.Dispatch(&corev1.Event{
		Event: &corev1.Event_TerminalHistory{
			TerminalHistory: &corev1.TerminalHistoryEvent{
				TerminalId: tid,
				Kind:       corev1.TerminalHistoryEventKind_TERMINAL_HISTORY_EVENT_KIND_PAGE_RECEIVED,
				Page: &v1.TerminalHistoryPage{
					RequestId:  "th-1",
					Lines:      []string{"\x1b[31mred\x1b[0m 中文"},
					NextCursor: "cap:200",
				},
			},
		},
	})
	if findTermHistoryCancelTimer(b, corev1.TimerKind_TIMER_KIND_TERMINAL_HISTORY_TIMEOUT, tid) == nil {
		t.Fatal("expected timeout timer to be cancelled")
	}
	upd = findTermHistoryUpdated(b)
	if upd == nil || upd.Phase != corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_OPEN ||
		!upd.CanLoadEarlier || upd.MaxCells != 8 || upd.AddedPage == nil || len(upd.AddedPage.Lines) != 1 {
		t.Fatalf("unexpected open update: %#v", upd)
	}
	runs := upd.AddedPage.Lines[0].Runs
	if len(runs) != 2 || runs[0].Text != "red" || runs[0].FgValue != 1 || runs[1].Text != " 中文" {
		t.Fatalf("unexpected parsed runs: %#v", runs)
	}

	// 3. Load earlier -> timer fires -> FAILED + retryable.
	b = c.Dispatch(&corev1.Event{
		Event: &corev1.Event_TerminalHistory{
			TerminalHistory: &corev1.TerminalHistoryEvent{
				TerminalId: tid,
				Kind:       corev1.TerminalHistoryEventKind_TERMINAL_HISTORY_EVENT_KIND_LOAD_EARLIER,
			},
		},
	})
	req = findTermHistorySendReq(b)
	tm = findTermHistoryScheduleTimer(b, corev1.TimerKind_TIMER_KIND_TERMINAL_HISTORY_TIMEOUT, tid)
	if req == nil || req.Cursor != "cap:200" || tm == nil {
		t.Fatalf("expected load earlier request & timer: req=%#v tm=%#v", req, tm)
	}
	b = c.Dispatch(&corev1.Event{
		Event: &corev1.Event_TimerFired{
			TimerFired: &corev1.TimerFiredEvent{
				TimerId: tm.TimerId,
				Kind:    corev1.TimerKind_TIMER_KIND_TERMINAL_HISTORY_TIMEOUT,
				Key:     tid,
			},
		},
	})
	upd = findTermHistoryUpdated(b)
	if upd == nil || upd.Status != corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_FAILED ||
		upd.StatusText.GetKey() != text.KeyTerminalReadFailed || !upd.CanRetry || upd.AddedPage != nil {
		t.Fatalf("unexpected timeout update: %#v", upd)
	}

	// 4. Retry reuses cursor, then disposed cancels pending timer and cleans map entry.
	b = c.Dispatch(&corev1.Event{
		Event: &corev1.Event_TerminalHistory{
			TerminalHistory: &corev1.TerminalHistoryEvent{
				TerminalId: tid,
				Kind:       corev1.TerminalHistoryEventKind_TERMINAL_HISTORY_EVENT_KIND_RETRY,
			},
		},
	})
	req = findTermHistorySendReq(b)
	if req == nil || req.Cursor != "cap:200" || req.RequestId != "th-3" {
		t.Fatalf("expected retry with cap:200, got %#v", req)
	}

	b = c.Dispatch(&corev1.Event{
		Event: &corev1.Event_TerminalHistory{
			TerminalHistory: &corev1.TerminalHistoryEvent{
				TerminalId: tid,
				Kind:       corev1.TerminalHistoryEventKind_TERMINAL_HISTORY_EVENT_KIND_DISPOSED,
			},
		},
	})
	if findTermHistoryCancelTimer(b, corev1.TimerKind_TIMER_KIND_TERMINAL_HISTORY_TIMEOUT, tid) == nil {
		t.Fatal("expected disposed to cancel active timer")
	}
	if _, exists := c.m.termHistories[tid]; exists {
		t.Fatal("expected disposed terminal history entry to be removed")
	}
}
