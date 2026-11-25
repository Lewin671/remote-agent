package app

import (
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	"github.com/Lewin671/remote-agent/clientcore/sync"
)

const (
	notifTapTimerKey  = "notif-tap"
	notifTapTimeoutMs = 3_000
)

func (c *Core) handleResolveNotificationTap(ctx *sync.StepContext, act *corev1.ResolveNotificationTapAction) []*corev1.Effect {
	if act == nil || act.SessionId == "" {
		return nil
	}
	c.m.pendingNotifTap = nil
	c.m.sync.CancelAppTimer(ctx, corev1.TimerKind_TIMER_KIND_UI_TRANSIENT, notifTapTimerKey)

	if !act.Scheduled {
		return []*corev1.Effect{
			{
				Effect: &corev1.Effect_Navigate{
					Navigate: &corev1.NavigateEffect{
						Route:     corev1.NavigateRoute_NAVIGATE_ROUTE_SESSION,
						SessionId: act.SessionId,
					},
				},
			},
		}
	}

	var effects []*corev1.Effect
	if c.tryResolveScheduledNotifTap(ctx, act, &effects) {
		return effects
	}
	c.m.pendingNotifTap = &corev1.ResolveNotificationTapAction{
		SessionId:   act.SessionId,
		Scheduled:   act.Scheduled,
		ScheduleSeq: act.ScheduleSeq,
		ApprovalId:  act.ApprovalId,
	}
	c.m.sync.LookupSessionIfMissing(ctx, act.SessionId)
	c.m.sync.ScheduleAppTimer(ctx, corev1.TimerKind_TIMER_KIND_UI_TRANSIENT, notifTapTimerKey, notifTapTimeoutMs)
	return effects
}

func (c *Core) reconcilePendingNotifTap(effects *[]*corev1.Effect) {
	act := c.m.pendingNotifTap
	if act == nil {
		return
	}
	prev, ctx := c.m.sync.BeginStep()
	resolved := c.tryResolveScheduledNotifTap(ctx, act, effects)
	if resolved {
		c.m.pendingNotifTap = nil
		c.m.sync.CancelAppTimer(ctx, corev1.TimerKind_TIMER_KIND_UI_TRANSIENT, notifTapTimerKey)
	}
	res := c.m.sync.EndStep(prev, ctx)
	*effects = append(*effects, res.Effects...)
}

func (c *Core) handleNotifTapTimeout() []*corev1.Effect {
	if c.m.pendingNotifTap == nil {
		return nil
	}
	c.m.pendingNotifTap = nil
	return []*corev1.Effect{
		{
			Effect: &corev1.Effect_ScheduleNavigate{
				ScheduleNavigate: &corev1.ScheduleNavigateEffect{
					Route: corev1.ScheduleNavigateRoute_SCHEDULE_NAVIGATE_ROUTE_LIST,
				},
			},
		},
	}
}

func (c *Core) tryResolveScheduledNotifTap(
	ctx *sync.StepContext,
	act *corev1.ResolveNotificationTapAction,
	effects *[]*corev1.Effect,
) bool {
	if sess := c.m.sync.SessionEntry(act.SessionId); sess != nil && sess.Info != nil {
		if sess.Info.TaskOwned && sess.Info.ScheduledTaskId != "" && sess.MachineID != "" {
			*effects = append(*effects, &corev1.Effect{
				Effect: &corev1.Effect_ScheduleNavigate{
					ScheduleNavigate: &corev1.ScheduleNavigateEffect{
						Route:     corev1.ScheduleNavigateRoute_SCHEDULE_NAVIGATE_ROUTE_DETAIL,
						MachineId: sess.MachineID,
						TaskId:    sess.Info.ScheduledTaskId,
					},
				},
			})
			c.triggerScheduleRefreshForMachine(ctx, sess.MachineID, effects)
			return true
		}
		*effects = append(*effects, &corev1.Effect{
			Effect: &corev1.Effect_ScheduleNavigate{
				ScheduleNavigate: &corev1.ScheduleNavigateEffect{
					Route:     corev1.ScheduleNavigateRoute_SCHEDULE_NAVIGATE_ROUTE_SESSION,
					SessionId: act.SessionId,
					FirstSeq:  act.ScheduleSeq,
				},
			},
		})
		if sess.MachineID != "" {
			c.triggerScheduleRefreshForMachine(ctx, sess.MachineID, effects)
		}
		return true
	}

	if ss := c.m.state.Schedules; ss != nil {
		for mid, snap := range ss.Machines {
			if snap == nil {
				continue
			}
			for _, task := range snap.Tasks {
				if task != nil && task.Takeover != nil && task.Takeover.Session == act.SessionId {
					*effects = append(*effects, &corev1.Effect{
						Effect: &corev1.Effect_ScheduleNavigate{
							ScheduleNavigate: &corev1.ScheduleNavigateEffect{
								Route:     corev1.ScheduleNavigateRoute_SCHEDULE_NAVIGATE_ROUTE_DETAIL,
								MachineId: mid,
								TaskId:    task.ID,
							},
						},
					})
					c.triggerScheduleRefreshForMachine(ctx, mid, effects)
					return true
				}
			}
		}
	}
	return false
}

func (c *Core) triggerScheduleRefreshForMachine(ctx *sync.StepContext, machineID string, effects *[]*corev1.Effect) {
	if machineID == "" {
		return
	}
	c.m.schedActive = true
	ss := ensureSchedState(c.m.state)
	snap := ensureSchedMachine(ss, machineID)
	snap.PendingRefresh = true
	c.reconcileSchedules(ctx, effects)
}
