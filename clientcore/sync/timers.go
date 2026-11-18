package sync

import (
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
)

func (e *Engine) scheduleTimer(ctx *StepContext, kind corev1.TimerKind, key string, delayMs int64) uint64 {
	if delayMs < 0 {
		delayMs = 0
	}
	tk := timerKey{kind: kind, key: key}
	if prev, ok := e.timers[tk]; ok {
		delete(e.timers, tk)
		ctx.emit(&corev1.Effect{
			Effect: &corev1.Effect_CancelTimer{
				CancelTimer: &corev1.CancelTimerEffect{
					TimerId: prev.id,
					Kind:    prev.kind,
					Key:     prev.key,
				},
			},
		})
	}
	e.nextTimerSeq++
	id := e.nextTimerSeq
	e.timers[tk] = timerEntry{id: id, kind: kind, key: key}
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_ScheduleTimer{
			ScheduleTimer: &corev1.ScheduleTimerEffect{
				TimerId: id,
				Kind:    kind,
				Key:     key,
				DelayMs: delayMs,
			},
		},
	})
	return id
}

func (e *Engine) cancelTimer(ctx *StepContext, kind corev1.TimerKind, key string) {
	tk := timerKey{kind: kind, key: key}
	prev, ok := e.timers[tk]
	if !ok {
		return
	}
	delete(e.timers, tk)
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_CancelTimer{
			CancelTimer: &corev1.CancelTimerEffect{
				TimerId: prev.id,
				Kind:    prev.kind,
				Key:     prev.key,
			},
		},
	})
}

func (e *Engine) cancelAllTimers(ctx *StepContext) {
	for tk, entry := range e.timers {
		delete(e.timers, tk)
		ctx.emit(&corev1.Effect{
			Effect: &corev1.Effect_CancelTimer{
				CancelTimer: &corev1.CancelTimerEffect{
					TimerId: entry.id,
					Kind:    entry.kind,
					Key:     entry.key,
				},
			},
		})
	}
}

func (e *Engine) isTimerActive(kind corev1.TimerKind, key string) bool {
	_, ok := e.timers[timerKey{kind: kind, key: key}]
	return ok
}

func (e *Engine) handleTimerFired(ctx *StepContext, ev *corev1.TimerFiredEvent) {
	if ev == nil {
		return
	}
	tk := timerKey{kind: ev.Kind, key: ev.Key}
	cur, ok := e.timers[tk]
	if !ok || cur.id != ev.TimerId {
		return
	}
	delete(e.timers, tk)

	switch ev.Kind {
	case corev1.TimerKind_TIMER_KIND_RECONNECT:
		if e.running && !e.removed && e.networkAvailable {
			e.beginConnect(ctx)
		}
	case corev1.TimerKind_TIMER_KIND_STREAM_OPEN:
		if e.running && e.connecting && e.legID > 0 {
			legID := e.legID
			e.emitCloseStream(ctx, legID)
			e.onLegDropped(ctx, legID, false, false, "", "stream open timeout")
		}
	case corev1.TimerKind_TIMER_KIND_LIVENESS:
		if !e.running || !e.streamOpen || e.liveness == nil {
			return
		}
		if e.liveness.Suspect(e.monoMs) {
			legID := e.legID
			e.emitCloseStream(ctx, legID)
			e.onLegDropped(ctx, legID, false, false, "", "stream liveness timeout")
			return
		}
		e.scheduleTimer(ctx, corev1.TimerKind_TIMER_KIND_LIVENESS, "", LivenessTickMs)
	case corev1.TimerKind_TIMER_KIND_LINK_GRACE:
		if ev.Key == displayGraceTimerKey {
			if e.displayGraceUntilMs != 0 {
				e.displayGraceUntilMs = 0
				ctx.Dirty.MarkAll()
			}
			return
		}
		if e.running && !e.streamOpen && e.networkAvailable {
			e.conn = lists.ConnOffline
			if e.offlineSinceMs == 0 && e.nowMs > 0 {
				e.offlineSinceMs = e.nowMs
			}
			ctx.Dirty.Add(true, true, true)
		}
	case corev1.TimerKind_TIMER_KIND_LIST_RETRY:
		if e.running && e.streamOpen {
			e.startRefresh(ctx)
		}
	case corev1.TimerKind_TIMER_KIND_PREFS_RETRY:
		if e.running && e.streamOpen && !e.prefs.Loaded {
			e.startLoadPrefs(ctx)
		}
	case corev1.TimerKind_TIMER_KIND_HISTORY_SWEEP:
		e.trimHistory(ctx)
	case corev1.TimerKind_TIMER_KIND_LIVE_POLL:
		e.onLivePollTimer(ctx, ev.Key)
	case corev1.TimerKind_TIMER_KIND_COMMAND_RETRY:
		e.onCommandRetryTimer(ctx, ev.Key)
	}
}
