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
	e.clearDirectTimerTracking()
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

// ScheduleAppTimer schedules or replaces an application-managed timer.
func (e *Engine) ScheduleAppTimer(ctx *StepContext, kind corev1.TimerKind, key string, delayMs int64) uint64 {
	return e.scheduleTimer(ctx, kind, key, delayMs)
}

// CancelAppTimer cancels an application-managed timer if currently scheduled.
func (e *Engine) CancelAppTimer(ctx *StepContext, kind corev1.TimerKind, key string) {
	e.cancelTimer(ctx, kind, key)
}

// ConsumeAppTimer validates and consumes a fired application-managed timer.
func (e *Engine) ConsumeAppTimer(ev *corev1.TimerFiredEvent) bool {
	if ev == nil {
		return false
	}
	tk := timerKey{kind: ev.Kind, key: ev.Key}
	cur, ok := e.timers[tk]
	if !ok || cur.id != ev.TimerId {
		return false
	}
	delete(e.timers, tk)
	return true
}

func isAppTimerKind(k corev1.TimerKind) bool {
	return k == corev1.TimerKind_TIMER_KIND_SESSION_UNDO || k == corev1.TimerKind_TIMER_KIND_SESSION_STEER_GRACE
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
		if !e.running || !e.w.LivePushConnected || e.liveness == nil {
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
		if ev.Key == coldStartGraceTimerKey {
			if e.coldStartGraceUntilMs != 0 {
				e.coldStartGraceUntilMs = 0
				e.coldStartGraceExpired = true
				ctx.Dirty.MarkAll()
			}
			return
		}
		if e.running && !e.w.LivePushConnected && e.networkAvailable {
			e.w.World.Connection = lists.ConnOffline
			if e.w.DisconnectedAtMs == 0 && e.nowMs > 0 {
				e.w.DisconnectedAtMs = e.nowMs
			}
			ctx.Dirty.Add(true, true, true)
		}
	case corev1.TimerKind_TIMER_KIND_LIST_RETRY:
		if e.running && e.w.LivePushConnected {
			e.startRefresh(ctx)
		}
	case corev1.TimerKind_TIMER_KIND_PREFS_RETRY:
		if e.running && e.w.LivePushConnected && !e.prefs.Loaded {
			e.startLoadPrefs(ctx)
		}
	case corev1.TimerKind_TIMER_KIND_HISTORY_SWEEP:
		e.trimHistory(ctx)
	case corev1.TimerKind_TIMER_KIND_LIVE_POLL:
		e.onLivePollTimer(ctx, ev.Key)
	case corev1.TimerKind_TIMER_KIND_COMMAND_RETRY:
		e.onCommandRetryTimer(ctx, ev.Key)
	case corev1.TimerKind_TIMER_KIND_DIRECT_ENGINE:
		e.directEngineScheduled = false
		e.emitCoordEffects(ctx, e.ensureTransportCoord().OnEngineTimer(e.nowMs))
	case corev1.TimerKind_TIMER_KIND_DIRECT_SESSION:
		e.directSessionScheduled = false
		e.emitCoordEffects(ctx, e.ensureTransportCoord().OnSessionTimer(e.nowMs))
	case corev1.TimerKind_TIMER_KIND_TERMINAL_REOPEN:
		e.directTermScheduled = false
		e.emitCoordEffects(ctx, e.ensureTransportCoord().OnTerminalTimer(e.nowMs))
	}
}
