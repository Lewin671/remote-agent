package sync

import (
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

func (e *Engine) canPollLive(sid string) bool {
	if !e.running || !e.foreground || e.conn != lists.ConnConnected {
		return false
	}
	if !e.watchedSessions[sid] {
		return false
	}
	s := e.sessions[sid]
	if s == nil || lists.Exited(&lists.Session{Info: s.Meta}) {
		return false
	}
	m := e.machines[s.MachineID]
	return m != nil && e.machineReachable(m)
}

func (e *Engine) updateLivePollers(ctx *StepContext) {
	// Cancel pollers that are no longer eligible.
	for sid := range e.watchedSessions {
		if !e.canPollLive(sid) {
			e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_LIVE_POLL, sid)
		}
	}
	for tk := range e.timers {
		if tk.kind == corev1.TimerKind_TIMER_KIND_LIVE_POLL && !e.canPollLive(tk.key) {
			e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_LIVE_POLL, tk.key)
		}
	}

	// Start immediate poll for newly eligible watched sessions without active poller or request.
	for sid := range e.watchedSessions {
		if !e.canPollLive(sid) {
			continue
		}
		if e.isTimerActive(corev1.TimerKind_TIMER_KIND_LIVE_POLL, sid) {
			continue
		}
		if _, busy := e.livePollInFlight[sid]; busy {
			continue
		}
		e.dispatchLivePoll(ctx, sid)
	}
}

func (e *Engine) onLivePollTimer(ctx *StepContext, sid string) {
	if !e.canPollLive(sid) {
		return
	}
	if _, busy := e.livePollInFlight[sid]; busy {
		e.scheduleTimer(ctx, corev1.TimerKind_TIMER_KIND_LIVE_POLL, sid, LivePollIntervalMs)
		return
	}
	e.dispatchLivePoll(ctx, sid)
}

func (e *Engine) dispatchLivePoll(ctx *StepContext, sid string) {
	s := e.sessions[sid]
	if s == nil || !e.canPollLive(sid) {
		return
	}
	cmd := &v1.Command{
		ProtocolVersion: ProtocolVersion,
		Kind: &v1.Command_GetLiveSnapshot{
			GetLiveSnapshot: &v1.GetLiveSnapshotRequest{SessionId: sid},
		},
	}
	reqID, ok := e.SubmitCommand(ctx, SubmitCommandParams{
		SlotKey:   "",
		MachineID: s.MachineID,
		SessionID: sid,
		Command:   cmd,
	})
	if !ok {
		if e.canPollLive(sid) {
			e.scheduleTimer(ctx, corev1.TimerKind_TIMER_KIND_LIVE_POLL, sid, LivePollIntervalMs)
		}
		return
	}
	e.livePollInFlight[sid] = reqID
	e.livePollReqToSid[reqID] = sid
}

// ApplyLiveSnapshot stores a fresh LiveSnapshot checkpoint for sid and marks affected views dirty.
func (e *Engine) ApplyLiveSnapshot(ctx *StepContext, sid string, snap *v1.LiveSnapshot) {
	if snap == nil || sid == "" {
		return
	}
	s := e.sessions[sid]
	if s == nil {
		return
	}
	s.Checkpoint = snap.Clone()
	s.CheckpointInvalid = false
	if snap.Session != nil {
		if snap.Session.LastSeq > s.LastSeq {
			s.LastSeq = snap.Session.LastSeq
		}
		s.Meta = snap.Session.Clone()
	}
	e.refreshSessionPending(s)
	ctx.Dirty.Add(true, true, false)
	ctx.Dirty.MarkSessionData()
}

func (e *Engine) onLivePollSettled(ctx *StepContext, reqID string, res *v1.CommandResult) {
	sid, ok := e.livePollReqToSid[reqID]
	if !ok {
		return
	}
	delete(e.livePollReqToSid, reqID)
	if e.livePollInFlight[sid] == reqID {
		delete(e.livePollInFlight, sid)
	}
	if res != nil {
		e.ApplyLiveSnapshot(ctx, sid, res.GetLiveSnapshot())
	}
	if e.canPollLive(sid) {
		e.scheduleTimer(ctx, corev1.TimerKind_TIMER_KIND_LIVE_POLL, sid, LivePollIntervalMs)
	}
}
