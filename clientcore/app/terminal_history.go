package app

import (
	"github.com/Lewin671/remote-agent/clientcore/domain/termhistory"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	"github.com/Lewin671/remote-agent/clientcore/sync"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func (m *Model) ensureTermHistory(terminalID string) *termhistory.Machine {
	if m.termHistories == nil {
		m.termHistories = make(map[string]*termhistory.Machine)
	}
	h := m.termHistories[terminalID]
	if h == nil {
		h = &termhistory.Machine{}
		m.termHistories[terminalID] = h
	}
	return h
}

func (c *Core) handleTerminalHistoryEvent(ctx *sync.StepContext, ev *corev1.TerminalHistoryEvent) []*corev1.Effect {
	if ev == nil || ev.TerminalId == "" {
		return nil
	}
	tid := ev.TerminalId
	if ev.Kind == corev1.TerminalHistoryEventKind_TERMINAL_HISTORY_EVENT_KIND_DISPOSED {
		if c.m.termHistories != nil {
			if h := c.m.termHistories[tid]; h != nil {
				if h.Pending != "" {
					c.m.sync.CancelAppTimer(ctx, corev1.TimerKind_TIMER_KIND_TERMINAL_HISTORY_TIMEOUT, tid)
				}
				delete(c.m.termHistories, tid)
			}
		}
		return nil
	}

	h := c.m.ensureTermHistory(tid)
	nextID := func() string { return c.m.sync.NextID(ctx) }

	var tr termhistory.Transition
	switch ev.Kind {
	case corev1.TerminalHistoryEventKind_TERMINAL_HISTORY_EVENT_KIND_OPEN:
		tr = h.Open(nextID)
	case corev1.TerminalHistoryEventKind_TERMINAL_HISTORY_EVENT_KIND_LOAD_EARLIER:
		tr = h.LoadEarlier(nextID)
	case corev1.TerminalHistoryEventKind_TERMINAL_HISTORY_EVENT_KIND_RETRY:
		tr = h.Retry(nextID)
	case corev1.TerminalHistoryEventKind_TERMINAL_HISTORY_EVENT_KIND_CLOSE:
		tr = h.Close()
	case corev1.TerminalHistoryEventKind_TERMINAL_HISTORY_EVENT_KIND_DISCONNECTED:
		tr = h.Disconnected()
	case corev1.TerminalHistoryEventKind_TERMINAL_HISTORY_EVENT_KIND_PAGE_RECEIVED:
		tr = h.ReceivePage(ev.Page)
	default:
		return nil
	}
	return c.applyTermHistoryTransition(ctx, tid, h, tr)
}

func (c *Core) handleTerminalHistoryTimeout(ctx *sync.StepContext, terminalID string) []*corev1.Effect {
	if terminalID == "" || c.m.termHistories == nil {
		return nil
	}
	h := c.m.termHistories[terminalID]
	if h == nil {
		return nil
	}
	tr := h.Timeout()
	return c.applyTermHistoryTransition(ctx, terminalID, h, tr)
}

func (c *Core) applyTermHistoryTransition(ctx *sync.StepContext, tid string, h *termhistory.Machine, tr termhistory.Transition) []*corev1.Effect {
	if !tr.Changed {
		return nil
	}
	if tr.CancelTimer {
		c.m.sync.CancelAppTimer(ctx, corev1.TimerKind_TIMER_KIND_TERMINAL_HISTORY_TIMEOUT, tid)
	}
	if tr.StartTimer {
		c.m.sync.ScheduleAppTimer(ctx, corev1.TimerKind_TIMER_KIND_TERMINAL_HISTORY_TIMEOUT, tid, termhistory.TimeoutMs)
	}
	effects := make([]*corev1.Effect, 0, 2)
	if tr.SendRequest {
		effects = append(effects, &corev1.Effect{
			Effect: &corev1.Effect_TerminalHistorySendRequest{
				TerminalHistorySendRequest: &corev1.TerminalHistorySendRequestEffect{
					TerminalId: tid,
					RequestId:  tr.RequestID,
					Cursor:     tr.Cursor,
				},
			},
		})
	}
	status := h.Status()
	effects = append(effects, &corev1.Effect{
		Effect: &corev1.Effect_TerminalHistoryUpdated{
			TerminalHistoryUpdated: &corev1.TerminalHistoryUpdatedEffect{
				TerminalId:     tid,
				Phase:          h.Phase,
				Status:         status,
				StatusText:     terminalHistoryStatusText(status),
				CanRetry:       h.CanRetry(),
				CanLoadEarlier: h.CanLoadEarlier(),
				HasMore:        h.Cursor != "",
				Pending:        h.Pending != "",
				MaxCells:       h.MaxCells,
				ResetPages:     tr.ResetPages,
				AddedPage:      tr.AddedPage,
			},
		},
	})
	return effects
}

func terminalHistoryStatusText(status corev1.TerminalHistoryStatus) *corev1.Text {
	switch status {
	case corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_OPENING:
		return text.Msg(text.KeyTerminalReading)
	case corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_OPENING_FAILED,
		corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_FAILED:
		return text.Msg(text.KeyTerminalReadFailed)
	case corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_LOADING_EARLIER:
		return text.Msg(text.KeyTerminalEarlier)
	case corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_EXPIRED:
		return text.Msg(text.KeyTerminalHistoryExpired)
	case corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_DISCONNECTED:
		return text.Msg(text.KeyTerminalHistoryClosed)
	case corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_START:
		return text.Msg(text.KeyTerminalHistoryStart)
	default:
		return nil
	}
}
