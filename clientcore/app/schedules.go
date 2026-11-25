package app

import (
	"slices"
	"strings"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/outcome"
	"github.com/Lewin671/remote-agent/clientcore/domain/schedule"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/screens"
	"github.com/Lewin671/remote-agent/clientcore/sync"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func isScheduleCmdKind(k cmdKind) bool {
	return k == cmdKindScheduleList ||
		k == cmdKindScheduleCreate ||
		k == cmdKindScheduleOperate ||
		k == cmdKindScheduleUpdate
}

func (m *Model) markScheduleDirty(targets ...corev1.ScreenTarget) {
	for _, t := range targets {
		switch t {
		case corev1.ScreenTarget_SCREEN_TARGET_SCHEDULES:
			m.schedDirty.list = true
		case corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_DETAIL:
			m.schedDirty.detail = true
		case corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_FORM:
			m.schedDirty.form = true
		case corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_SESSION_STRIP:
			m.schedDirty.strip = true
		}
	}
}

func (m *Model) markAllScheduleScreensDirty() {
	m.schedDirty.list = true
	m.schedDirty.detail = true
	m.schedDirty.form = true
	m.schedDirty.strip = true
}

func (m *Model) takeScheduleDirtyTargets() []corev1.ScreenTarget {
	var out []corev1.ScreenTarget
	if m.schedDirty.list {
		out = append(out, corev1.ScreenTarget_SCREEN_TARGET_SCHEDULES)
	}
	if m.schedDirty.detail {
		out = append(out, corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_DETAIL)
	}
	if m.schedDirty.form {
		out = append(out, corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_FORM)
	}
	if m.schedDirty.strip {
		out = append(out, corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_SESSION_STRIP)
	}
	m.schedDirty = scheduleDirty{}
	return out
}

func (m *Model) mergeScheduleRender(effects []*corev1.Effect) []*corev1.Effect {
	extra := m.takeScheduleDirtyTargets()
	if len(extra) == 0 {
		return effects
	}
	for _, eff := range effects {
		if eff == nil {
			continue
		}
		if r := eff.GetRender(); r != nil {
			for _, t := range extra {
				if !slices.Contains(r.Screens, t) {
					r.Screens = append(r.Screens, t)
				}
			}
			return effects
		}
	}
	return append(effects, &corev1.Effect{
		Effect: &corev1.Effect_Render{
			Render: &corev1.RenderEffect{Screens: extra},
		},
	})
}

func (c *Core) viewSchedules(screen *corev1.Screen) (*corev1.ViewModel, bool, error) {
	switch s := screen.Screen.(type) {
	case *corev1.Screen_Schedules:
		var endedLimit int
		if s.Schedules != nil {
			endedLimit = int(s.Schedules.EndedLimit)
		}
		return &corev1.ViewModel{
			View: &corev1.ViewModel_Schedules{
				Schedules: screens.BuildSchedules(c.m.state, endedLimit),
			},
		}, true, nil

	case *corev1.Screen_ScheduleDetail:
		var mid, tid string
		var earlierLimit int
		if s.ScheduleDetail != nil {
			mid = s.ScheduleDetail.MachineId
			tid = s.ScheduleDetail.TaskId
			earlierLimit = int(s.ScheduleDetail.EarlierRunsLimit)
		}
		return &corev1.ViewModel{
			View: &corev1.ViewModel_ScheduleDetail{
				ScheduleDetail: screens.BuildScheduleDetail(c.m.state, mid, tid, earlierLimit),
			},
		}, true, nil

	case *corev1.Screen_ScheduleForm:
		var formKey string
		if s.ScheduleForm != nil {
			formKey = s.ScheduleForm.FormKey
		}
		return &corev1.ViewModel{
			View: &corev1.ViewModel_ScheduleForm{
				ScheduleForm: screens.BuildScheduleForm(c.m.state, formKey),
			},
		}, true, nil

	case *corev1.Screen_ScheduleSessionStrip:
		var sid string
		if s.ScheduleSessionStrip != nil {
			sid = s.ScheduleSessionStrip.SessionId
		}
		return &corev1.ViewModel{
			View: &corev1.ViewModel_ScheduleSessionStrip{
				ScheduleSessionStrip: screens.BuildScheduleSessionStrip(c.m.state, sid),
			},
		}, true, nil

	default:
		return nil, false, nil
	}
}

func ensureSchedState(st *screens.State) *screens.ScheduleState {
	if st.Schedules == nil {
		st.Schedules = screens.NewScheduleState()
	}
	return st.Schedules
}

func ensureSchedMachine(ss *screens.ScheduleState, machineID string) *screens.ScheduleMachineSnapshot {
	snap := ss.Machines[machineID]
	if snap == nil {
		snap = &screens.ScheduleMachineSnapshot{}
		ss.Machines[machineID] = snap
	}
	return snap
}

func ensureSchedMutation(ss *screens.ScheduleState, key string) *screens.ScheduleMutationState {
	mut := ss.Mutations[key]
	if mut == nil {
		mut = &screens.ScheduleMutationState{}
		ss.Mutations[key] = mut
	}
	return mut
}

func clearSchedMutation(mut *screens.ScheduleMutationState) {
	if mut == nil {
		return
	}
	mut.Busy = false
	mut.Frozen = false
	mut.Expired = false
	mut.ErrorText = nil
	mut.NoticeText = nil
}

func findSchedTask(ss *screens.ScheduleState, machineID, taskID string) *schedule.Task {
	if ss == nil {
		return nil
	}
	snap := ss.Machines[machineID]
	if snap == nil {
		return nil
	}
	for _, t := range snap.Tasks {
		if t != nil && t.ID == taskID {
			return t
		}
	}
	return nil
}

func upsertSchedTask(snap *screens.ScheduleMachineSnapshot, task *schedule.Task) {
	if snap == nil || task == nil || task.ID == "" {
		return
	}
	cloned := schedule.CloneTask(task)
	for i, existing := range snap.Tasks {
		if existing != nil && existing.ID == cloned.ID {
			snap.Tasks[i] = cloned
			return
		}
	}
	snap.Tasks = append([]*schedule.Task{cloned}, snap.Tasks...)
}

func seenOutcomeEffects(ss *screens.ScheduleState, out schedule.SeenOutcome) []*corev1.Effect {
	var effects []*corev1.Effect
	for _, mid := range out.SaveMachines {
		toks := ss.Seen.TokensFor(mid)
		protoToks := make([]*corev1.ScheduleSeenToken, 0, len(toks))
		for _, tok := range toks {
			protoToks = append(protoToks, &corev1.ScheduleSeenToken{
				TaskId: tok.TaskID,
				RunId:  tok.RunID,
			})
		}
		effects = append(effects, &corev1.Effect{
			Effect: &corev1.Effect_ScheduleSaveSeen{
				ScheduleSaveSeen: &corev1.ScheduleSaveSeenEffect{
					MachineId: mid,
					Tokens:    protoToks,
				},
			},
		})
	}
	if len(out.Dismiss) > 0 {
		refs := make([]*corev1.ScheduleNotificationRef, 0, len(out.Dismiss))
		for _, d := range out.Dismiss {
			refs = append(refs, &corev1.ScheduleNotificationRef{
				MachineId:       d.MachineID,
				TaskId:          d.TaskID,
				RunId:           d.RunID,
				SessionId:       d.SessionID,
				NotificationSeq: d.NotificationSeq,
			})
		}
		effects = append(effects, &corev1.Effect{
			Effect: &corev1.Effect_ScheduleDismissNotifications{
				ScheduleDismissNotifications: &corev1.ScheduleDismissNotificationsEffect{Runs: refs},
			},
		})
	}
	return effects
}

func schedBounds(st *screens.State) schedule.CalendarBounds {
	var b schedule.CalendarBounds
	if st == nil {
		return b
	}
	b.Cal = st.Cal
	if st.Schedules != nil {
		b.LocalTimezone = st.Schedules.LocalTimezone
		b.TomorrowStartMs = st.Schedules.TomorrowStartMs
		b.DayAfterTomorrowStartMs = st.Schedules.DayAfterTomorrowStartMs
	}
	return b
}

func schedAgentByIDString(id string) v1.Agent {
	switch id {
	case "claude":
		return v1.Agent_AGENT_CLAUDE
	case "agy", "antigravity":
		return v1.Agent_AGENT_ANTIGRAVITY
	case "codex":
		return v1.Agent_AGENT_CODEX
	default:
		return lists.DefaultAgent
	}
}

func schedApprovalFromString(s string) v1.ApprovalMode {
	switch s {
	case "ask":
		return v1.ApprovalMode_APPROVAL_MODE_ASK
	case "auto":
		return v1.ApprovalMode_APPROVAL_MODE_AUTO
	case "full":
		return v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS
	default:
		return v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED
	}
}

func schedAgentIDString(a v1.Agent) string {
	return lists.AgentInfo(lists.EffectiveAgent(a)).ID
}

func timingKindFromProto(k corev1.ScheduleTimingKind) schedule.TimingKind {
	switch k {
	case corev1.ScheduleTimingKind_SCHEDULE_TIMING_KIND_ONCE:
		return schedule.TimingOnce
	case corev1.ScheduleTimingKind_SCHEDULE_TIMING_KIND_DAILY:
		return schedule.TimingDaily
	case corev1.ScheduleTimingKind_SCHEDULE_TIMING_KIND_WEEKLY:
		return schedule.TimingWeekly
	case corev1.ScheduleTimingKind_SCHEDULE_TIMING_KIND_INTERVAL:
		return schedule.TimingInterval
	case corev1.ScheduleTimingKind_SCHEDULE_TIMING_KIND_AFTER_TURN:
		return schedule.TimingAfterTurn
	default:
		return 0
	}
}

func intervalUnitFromProto(u corev1.ScheduleIntervalUnit) schedule.IntervalUnit {
	switch u {
	case corev1.ScheduleIntervalUnit_SCHEDULE_INTERVAL_UNIT_MINUTES:
		return schedule.UnitMinutes
	case corev1.ScheduleIntervalUnit_SCHEDULE_INTERVAL_UNIT_HOURS:
		return schedule.UnitHours
	case corev1.ScheduleIntervalUnit_SCHEDULE_INTERVAL_UNIT_DAYS:
		return schedule.UnitDays
	case corev1.ScheduleIntervalUnit_SCHEDULE_INTERVAL_UNIT_SECONDS:
		return schedule.UnitSeconds
	default:
		return 0
	}
}

func timingEndFromProto(e corev1.ScheduleTimingEnd) schedule.TimingEnd {
	switch e {
	case corev1.ScheduleTimingEnd_SCHEDULE_TIMING_END_NEVER:
		return schedule.EndNever
	case corev1.ScheduleTimingEnd_SCHEDULE_TIMING_END_COUNT:
		return schedule.EndCount
	case corev1.ScheduleTimingEnd_SCHEDULE_TIMING_END_DATE:
		return schedule.EndDate
	default:
		return 0
	}
}

func navEffect(route corev1.ScheduleNavigateRoute, machineID, taskID, sessionID, runID string, firstSeq uint64, formKey string) *corev1.Effect {
	return &corev1.Effect{
		Effect: &corev1.Effect_ScheduleNavigate{
			ScheduleNavigate: &corev1.ScheduleNavigateEffect{
				Route:     route,
				MachineId: machineID,
				TaskId:    taskID,
				SessionId: sessionID,
				RunId:     runID,
				FirstSeq:  firstSeq,
				FormKey:   formKey,
			},
		},
	}
}

func (c *Core) submitScheduleList(ctx *sync.StepContext, machineID string, rev uint64) {
	ss := ensureSchedState(c.m.state)
	snap := ensureSchedMachine(ss, machineID)
	reqID, dispatched := c.m.sync.SubmitCommand(ctx, sync.SubmitCommandParams{
		MachineID: machineID,
		Command: &v1.Command{
			Kind: &v1.Command_Schedule{
				Schedule: &v1.ScheduleRequest{Operation: "list"},
			},
		},
	})
	if reqID != "" {
		c.m.pendingCmdMeta[reqID] = cmdMeta{
			kind:      cmdKindScheduleList,
			machineID: machineID,
			revision:  rev,
		}
	}
	if dispatched {
		snap.Loading = true
		snap.InFlightReqID = reqID
		snap.PendingRefresh = false
		c.m.markScheduleDirty(
			corev1.ScreenTarget_SCREEN_TARGET_SCHEDULES,
			corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_DETAIL,
			corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_SESSION_STRIP,
		)
	}
}

func (c *Core) submitSchedTaskMutation(ctx *sync.StepContext, slotKey, mutKey, machineID, taskID, formKey, opName string, kind cmdKind, cmd *v1.Command) {
	ss := ensureSchedState(c.m.state)
	mut := ensureSchedMutation(ss, mutKey)
	meta := cmdMeta{
		kind:      kind,
		machineID: machineID,
		taskID:    taskID,
		formKey:   formKey,
		opName:    opName,
	}
	c.m.frozenSchedOps[slotKey] = frozenSchedOp{
		machineID: machineID,
		cmd:       cmd,
		meta:      meta,
	}
	reqID, dispatched := c.m.sync.SubmitCommand(ctx, sync.SubmitCommandParams{
		SlotKey:   slotKey,
		MachineID: machineID,
		Command:   cmd,
	})
	if reqID != "" {
		c.m.pendingCmdMeta[reqID] = meta
	}
	if dispatched {
		mut.Busy = true
		mut.ErrorText = nil
		mut.NoticeText = nil
	} else {
		// Frozen/active slot already blocked another dispatch or settled synchronously inside SubmitCommand.
		mut.Busy = false
	}
	c.m.markAllScheduleScreensDirty()
}

// onSyncEventConsumed runs lightweight app-layer hooks for events handled inside sync.Engine.
func (c *Core) onSyncEventConsumed(ev *corev1.Event, effects *[]*corev1.Effect) {
	c.onSharedFilesSyncEvent(ev, effects)
	c.onAttachmentsSyncEvent(ev, effects)
	if lc := ev.GetSyncLifecycle(); lc != nil {
		if lc.Kind == corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_STOP {
			c.m.activeNewSessionPagingKey = ""
		}
	}
	if sw := ev.GetSessionWatch(); sw != nil && sw.Watched && sw.SessionId != "" {
		if sess := c.m.sync.SessionEntry(sw.SessionId); sess != nil && sess.MachineID != "" {
			c.maybeHydrateUsageCache(sess.MachineID, effects)
		}
	}
	if !c.m.schedActive {
		return
	}
	ss := ensureSchedState(c.m.state)
	switch k := ev.Event.(type) {
	case *corev1.Event_SyncLifecycle:
		if k.SyncLifecycle == nil {
			return
		}
		switch k.SyncLifecycle.Kind {
		case corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_BACKGROUND:
			if ss.Detail != nil {
				ss.Seen.HideTask(ss.Detail.MachineID, ss.Detail.TaskID)
				ss.Detail.ResultCaptured = false
				c.m.markScheduleDirty(
					corev1.ScreenTarget_SCREEN_TARGET_SCHEDULES,
					corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_DETAIL,
				)
			}
		case corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_FOREGROUND:
			if ss.Detail != nil && ss.Detail.MachineID != "" && ss.Detail.TaskID != "" {
				out := ss.Seen.OpenTask(ss.Detail.MachineID, ss.Detail.TaskID)
				*effects = append(*effects, seenOutcomeEffects(ss, out)...)
				c.captureDetailResultIfNeeded(ss.Detail.MachineID, ss.Detail.TaskID)
				c.m.markScheduleDirty(
					corev1.ScreenTarget_SCREEN_TARGET_SCHEDULES,
					corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_DETAIL,
					corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_SESSION_STRIP,
				)
			}
		}

	case *corev1.Event_SessionWatch:
		if k.SessionWatch == nil {
			return
		}
		if k.SessionWatch.Watched {
			out := ss.Seen.SetVisibleSession(k.SessionWatch.SessionId)
			*effects = append(*effects, seenOutcomeEffects(ss, out)...)
			if len(ss.Machines) > 0 || len(out.SaveMachines) > 0 || len(out.Dismiss) > 0 {
				c.m.markScheduleDirty(
					corev1.ScreenTarget_SCREEN_TARGET_SCHEDULES,
					corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_DETAIL,
					corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_SESSION_STRIP,
				)
			}
		}
	}
}

func (c *Core) captureDetailResultIfNeeded(machineID, taskID string) {
	ss := ensureSchedState(c.m.state)
	if ss.Detail == nil || ss.Detail.MachineID != machineID || ss.Detail.TaskID != taskID {
		return
	}
	if ss.Detail.ResultCaptured {
		return
	}
	task := findSchedTask(ss, machineID, taskID)
	if task == nil {
		return
	}
	if latest := schedule.LatestNotification(task); latest != nil {
		ss.Detail.PinnedResultID = latest.ID
	}
	ss.Detail.ResultCaptured = true
}

func (c *Core) evictUntrustedScheduleSnapshots() {
	ss := c.m.state.Schedules
	if ss == nil || len(ss.Machines) == 0 {
		return
	}
	evicted := false
	for mid := range ss.Machines {
		if c.m.sync.CanUseMachineCache(mid) {
			continue
		}
		delete(ss.Machines, mid)
		ss.Seen.Forget(mid)
		if ss.Detail != nil && ss.Detail.MachineID == mid {
			ss.Detail = nil
		}
		if ss.SelectedMachineID == mid {
			ss.SelectedMachineID = ""
			ss.SelectedTaskID = ""
		}
		evicted = true
	}
	if evicted {
		c.m.markScheduleDirty(
			corev1.ScreenTarget_SCREEN_TARGET_SCHEDULES,
			corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_DETAIL,
			corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_SESSION_STRIP,
		)
	}
}

func (c *Core) processSchedulesSyncStep(res sync.StepResult, effects *[]*corev1.Effect) {
	c.evictUntrustedScheduleSnapshots()
	hasScheduleSettled := false
	for _, sc := range res.Settled {
		if meta, ok := c.m.pendingCmdMeta[sc.RequestID]; ok && (meta.kind == cmdKindScheduleList ||
			meta.kind == cmdKindScheduleCreate ||
			meta.kind == cmdKindScheduleOperate ||
			meta.kind == cmdKindScheduleUpdate) {
			hasScheduleSettled = true
			break
		}
	}
	if !c.m.schedActive && !hasScheduleSettled {
		return
	}
	st := c.m.state
	ss := ensureSchedState(st)
	var settledRefreshMachines []string
	var configUpdated bool

	for _, sc := range res.Settled {
		if sc.Result != nil && (sc.Result.GetListModels() != nil || sc.Result.GetGetSettings() != nil) {
			configUpdated = true
		}
		meta, ok := c.m.pendingCmdMeta[sc.RequestID]
		if !ok {
			continue
		}
		if meta.kind != cmdKindScheduleList &&
			meta.kind != cmdKindScheduleCreate &&
			meta.kind != cmdKindScheduleOperate &&
			meta.kind != cmdKindScheduleUpdate {
			continue
		}
		delete(c.m.pendingCmdMeta, sc.RequestID)

		switch meta.kind {
		case cmdKindScheduleList:
			snap := ensureSchedMachine(ss, meta.machineID)
			if snap.InFlightReqID != "" && sc.RequestID != "" && snap.InFlightReqID != sc.RequestID {
				continue
			}
			snap.InFlightReqID = ""
			snap.Loading = false
			if sc.Category == outcome.Executed && sc.ErrorCode == "" && sc.Result != nil && sc.Result.GetSchedule() != nil {
				tasks, err := schedule.DecodeTaskList(sc.Result.GetSchedule().GetResultJson())
				if err != nil {
					snap.Error = ""
					snap.ErrorText = text.Plain(err.Error())
				} else {
					snap.Tasks = tasks
					snap.Live = true
					snap.Cached = false
					snap.Error = ""
					snap.ErrorText = nil
					snap.HasFetchedOnce = true
					snap.FetchedRevision = meta.revision
					if mac := c.m.findMachine(meta.machineID); mac != nil && mac.Meta != nil && mac.Meta.ScheduleRevision > snap.FetchedRevision {
						// Queue another fetch if a newer ScheduleRevision arrived while list was in flight.
						snap.PendingRefresh = true
					}
					out := ss.Seen.Observe(meta.machineID, tasks, true)
					*effects = append(*effects, seenOutcomeEffects(ss, out)...)
					if ss.Detail != nil && ss.Detail.MachineID == meta.machineID {
						c.captureDetailResultIfNeeded(meta.machineID, ss.Detail.TaskID)
					}
					if sc.SealedResult != nil && len(sc.Result.GetSchedule().GetResultJson()) > 0 {
						if c.m.sync.CanUseMachineCache(meta.machineID) {
							if sealedBytes, err := sc.SealedResult.Marshal(); err == nil && len(sealedBytes) > 0 {
								c.m.nextScheduleCacheOpID++
								*effects = append(*effects, &corev1.Effect{
									Effect: &corev1.Effect_ScheduleWriteCache{
										ScheduleWriteCache: &corev1.ScheduleWriteCacheEffect{
											OpId:              c.m.nextScheduleCacheOpID,
											MachineId:         meta.machineID,
											RequestId:         sc.RequestID,
											SealedResultBytes: sealedBytes,
										},
									},
								})
							}
						} else {
							*effects = append(*effects, &corev1.Effect{
								Effect: &corev1.Effect_ScheduleDeleteCache{
									ScheduleDeleteCache: &corev1.ScheduleDeleteCacheEffect{MachineId: meta.machineID},
								},
							})
						}
					}
				}
			} else {
				snap.HasFetchedOnce = true
				snap.FetchedRevision = meta.revision
				snap.Error = sc.ErrorCode
				snap.ErrorText = screens.ReasonText(sc.ErrorReason)
				if snap.Error == "" && text.IsEmpty(snap.ErrorText) {
					snap.Error = "failed"
				}
			}
			c.m.markScheduleDirty(
				corev1.ScreenTarget_SCREEN_TARGET_SCHEDULES,
				corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_DETAIL,
				corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_SESSION_STRIP,
			)

		case cmdKindScheduleCreate:
			slotKey := "schedule_create:" + meta.formKey
			mutKey := "form:" + meta.formKey
			mut := ensureSchedMutation(ss, mutKey)
			mut.Busy = false
			form := ss.Forms[meta.formKey]
			if sc.Category == outcome.Executed && sc.ErrorCode == "" {
				delete(c.m.frozenSchedOps, slotKey)
				clearSchedMutation(mut)
				var task *schedule.Task
				if sc.Result != nil && sc.Result.GetSchedule() != nil {
					task, _ = schedule.DecodeTask(sc.Result.GetSchedule().GetResultJson())
				}
				isSession := form != nil && form.Mode == corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_CREATE_FOR_SESSION
				var taskID string
				if task != nil && task.ID != "" {
					taskID = task.ID
					snap := ensureSchedMachine(ss, meta.machineID)
					upsertSchedTask(snap, task)
					out := ss.Seen.Observe(meta.machineID, snap.Tasks, false)
					*effects = append(*effects, seenOutcomeEffects(ss, out)...)
					if !isSession && task.Spec.Target.Type == "resume_session" {
						isSession = true
					}
				}
				if form != nil {
					form.Open = false
					form.Dirty = false
					form.FrozenSpec = nil
					form.SubmittedSpecJSON = ""
				}
				delete(ss.Forms, meta.formKey)
				if ss.ActiveFormKey == meta.formKey {
					ss.ActiveFormKey = ""
				}
				settledRefreshMachines = append(settledRefreshMachines, meta.machineID)
				if isSession || taskID == "" {
					*effects = append(*effects, navEffect(corev1.ScheduleNavigateRoute_SCHEDULE_NAVIGATE_ROUTE_CLOSE_FORM, meta.machineID, taskID, "", "", 0, meta.formKey))
				} else {
					*effects = append(*effects, navEffect(corev1.ScheduleNavigateRoute_SCHEDULE_NAVIGATE_ROUTE_DETAIL, meta.machineID, taskID, "", "", 0, meta.formKey))
				}
			} else if sc.Category == outcome.Unknown {
				mut.Frozen = sc.Frozen || !sc.Expired
				mut.Expired = sc.Expired
				mut.ErrorText = screens.ReasonText(sc.ErrorReason)
				if sc.Expired {
					delete(c.m.frozenSchedOps, slotKey)
					if form != nil {
						form.FrozenSpec = nil
					}
				}
			} else {
				delete(c.m.frozenSchedOps, slotKey)
				mut.Frozen = false
				mut.Expired = false
				mut.ErrorText = screens.ReasonText(sc.ErrorReason)
				if text.IsEmpty(mut.ErrorText) && sc.ErrorCode != "" {
					mut.ErrorText = text.Plain(sc.ErrorCode)
				}
				if form != nil {
					form.FrozenSpec = nil
				}
			}
			c.m.markAllScheduleScreensDirty()

		case cmdKindScheduleOperate, cmdKindScheduleUpdate:
			slotKey := "schedule_op:" + meta.machineID + "/" + meta.taskID
			mutKey := "op:" + meta.machineID + "/" + meta.taskID
			mut := ensureSchedMutation(ss, mutKey)
			mut.Busy = false
			if sc.Category == outcome.Executed && sc.ErrorCode == "" {
				delete(c.m.frozenSchedOps, slotKey)
				clearSchedMutation(mut)
				if meta.opName == "run" {
					mut.NoticeText = text.Msg(text.KeyScheduleRequestedARunNowTheAutomaticScheduleIsUnchanged)
				}
				if sc.Result != nil && sc.Result.GetSchedule() != nil {
					if task, err := schedule.DecodeTask(sc.Result.GetSchedule().GetResultJson()); err == nil && task != nil && task.ID != "" {
						snap := ensureSchedMachine(ss, meta.machineID)
						upsertSchedTask(snap, task)
						out := ss.Seen.Observe(meta.machineID, snap.Tasks, false)
						*effects = append(*effects, seenOutcomeEffects(ss, out)...)
						if meta.formKey != "" {
							if f := ss.Forms[meta.formKey]; f != nil {
								f.BaseTask = schedule.CloneTask(task)
							}
						}
					}
				}
				if meta.formKey != "" {
					if f := ss.Forms[meta.formKey]; f != nil {
						f.Open = false
						f.Dirty = false
						f.FrozenSpec = nil
						f.SubmittedSpecJSON = ""
					}
					delete(ss.Forms, meta.formKey)
					if ss.ActiveFormKey == meta.formKey {
						ss.ActiveFormKey = ""
					}
					*effects = append(*effects, navEffect(corev1.ScheduleNavigateRoute_SCHEDULE_NAVIGATE_ROUTE_CLOSE_FORM, meta.machineID, meta.taskID, "", "", 0, meta.formKey))
				}
				if meta.opName == "cancel" {
					if ss.Detail != nil && ss.Detail.MachineID == meta.machineID && ss.Detail.TaskID == meta.taskID {
						ss.Seen.CloseTask(meta.machineID, meta.taskID)
						ss.Detail = nil
					}
					if ss.SelectedMachineID == meta.machineID && ss.SelectedTaskID == meta.taskID {
						ss.SelectedMachineID = ""
						ss.SelectedTaskID = ""
					}
					*effects = append(*effects, navEffect(corev1.ScheduleNavigateRoute_SCHEDULE_NAVIGATE_ROUTE_LIST, "", "", "", "", 0, ""))
				}
				settledRefreshMachines = append(settledRefreshMachines, meta.machineID)
			} else if sc.Category == outcome.Conflict && sc.Result != nil && sc.Result.ConflictKind == "business" {
				delete(c.m.frozenSchedOps, slotKey)
				mut.Frozen = false
				mut.Expired = false
				mut.ErrorText = text.Msg(text.KeyErrorsTheTaskWasJustChangedUpdatedToTheLatest)
				if meta.formKey != "" {
					if f := ss.Forms[meta.formKey]; f != nil {
						f.Open = false
						f.Dirty = false
						f.FrozenSpec = nil
					}
					delete(ss.Forms, meta.formKey)
					if ss.ActiveFormKey == meta.formKey {
						ss.ActiveFormKey = ""
					}
					*effects = append(*effects, navEffect(corev1.ScheduleNavigateRoute_SCHEDULE_NAVIGATE_ROUTE_CLOSE_FORM, meta.machineID, meta.taskID, "", "", 0, meta.formKey))
				}
				settledRefreshMachines = append(settledRefreshMachines, meta.machineID)
			} else if sc.Category == outcome.Unknown {
				mut.Frozen = sc.Frozen || !sc.Expired
				mut.Expired = sc.Expired
				mut.ErrorText = screens.ReasonText(sc.ErrorReason)
				if sc.Expired {
					delete(c.m.frozenSchedOps, slotKey)
				}
			} else {
				delete(c.m.frozenSchedOps, slotKey)
				mut.Frozen = false
				mut.Expired = false
				mut.ErrorText = screens.ReasonText(sc.ErrorReason)
				if text.IsEmpty(mut.ErrorText) && sc.ErrorCode != "" {
					mut.ErrorText = text.Plain(sc.ErrorCode)
				}
			}
			c.m.markAllScheduleScreensDirty()
		}
	}

	hasSchedContext := len(ss.Machines) > 0
	if !hasSchedContext {
		for _, mac := range st.World.Machines {
			if schedule.Supported(mac, st.World.ClientVersion) {
				hasSchedContext = true
				break
			}
		}
	}
	if (configUpdated || res.Dirty.NewSession || res.Dirty.SyncState) && (ss.ActiveFormKey != "" || len(ss.Forms) > 0) {
		c.m.markScheduleDirty(corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_FORM)
	}
	if res.Dirty.SessionData && ss.Detail != nil {
		c.m.markScheduleDirty(corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_DETAIL)
	}
	if hasSchedContext && (res.Dirty.Home || res.Dirty.Project || res.Dirty.SyncState) {
		c.m.markScheduleDirty(
			corev1.ScreenTarget_SCREEN_TARGET_SCHEDULES,
			corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_DETAIL,
			corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_SESSION_STRIP,
		)
	}

	needReconcile := hasSchedContext || len(settledRefreshMachines) > 0 || configUpdated ||
		ss.Detail != nil || ss.ActiveFormKey != ""
	if needReconcile {
		syncPrev, syncCtx := c.m.sync.BeginStep()
		for _, mid := range settledRefreshMachines {
			if snap := ss.Machines[mid]; snap != nil {
				snap.PendingRefresh = true
			}
		}
		if c.m.schedActive {
			c.reconcileSchedules(syncCtx, effects)
		}
		syncRes := c.m.sync.EndStep(syncPrev, syncCtx)
		*effects = append(*effects, syncRes.Effects...)
	}
}

func (c *Core) reconcileSchedules(ctx *sync.StepContext, effects *[]*corev1.Effect) {
	st := c.m.state
	ss := ensureSchedState(st)

	byID := make(map[string]*lists.Machine, len(st.World.Machines))
	for _, mac := range st.World.Machines {
		if mac != nil {
			byID[mac.ID] = mac
		}
	}

	// Prune removed or unsupported machines and drop their cached/seen data.
	for mid := range ss.Machines {
		mac := byID[mid]
		if mac != nil && (mac.Meta == nil || schedule.Supported(mac, st.World.ClientVersion)) {
			continue
		}
		if mac == nil && st.World.Connection != lists.ConnConnected && len(st.World.Machines) == 0 {
			continue
		}
		delete(ss.Machines, mid)
		ss.Seen.Forget(mid)
		*effects = append(*effects, &corev1.Effect{
			Effect: &corev1.Effect_ScheduleDeleteCache{
				ScheduleDeleteCache: &corev1.ScheduleDeleteCacheEffect{MachineId: mid},
			},
		})
		if ss.Detail != nil && ss.Detail.MachineID == mid {
			ss.Detail = nil
		}
		if ss.SelectedMachineID == mid {
			ss.SelectedMachineID = ""
			ss.SelectedTaskID = ""
		}
		c.m.markScheduleDirty(
			corev1.ScreenTarget_SCREEN_TARGET_SCHEDULES,
			corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_DETAIL,
			corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_SESSION_STRIP,
		)
	}

	// Ensure cache/seen load and revision-driven list fetches for all supported trusted machines.
	for _, mac := range st.World.Machines {
		if mac == nil || !schedule.Supported(mac, st.World.ClientVersion) || !c.m.sync.CanUseMachineCache(mac.ID) {
			continue
		}
		mid := mac.ID
		snap := ensureSchedMachine(ss, mid)
		if !snap.SeenLoaded && !snap.SeenLoading {
			snap.SeenLoading = true
			*effects = append(*effects, &corev1.Effect{
				Effect: &corev1.Effect_ScheduleLoadSeen{
					ScheduleLoadSeen: &corev1.ScheduleLoadSeenEffect{MachineId: mid},
				},
			})
		}
		if !snap.CacheRestored && !snap.CacheReading {
			if c.m.sync.CanUseMachineCache(mid) {
				c.m.nextScheduleCacheOpID++
				opID := c.m.nextScheduleCacheOpID
				snap.CacheReading = true
				snap.CacheReadOpID = opID
				*effects = append(*effects, &corev1.Effect{
					Effect: &corev1.Effect_ScheduleReadCache{
						ScheduleReadCache: &corev1.ScheduleReadCacheEffect{
							OpId:      opID,
							MachineId: mid,
						},
					},
				})
			} else {
				snap.CacheRestored = true
			}
		}

		writable := schedule.Writable(&st.World, mid)
		if snap.WasReachable && !writable {
			snap.HasFetchedOnce = false
			snap.PendingRefresh = false
		}
		snap.WasReachable = writable

		rev := mac.Meta.GetScheduleRevision()
		if writable && !snap.Loading && (!snap.HasFetchedOnce || snap.FetchedRevision != rev || snap.PendingRefresh) {
			c.submitScheduleList(ctx, mid, rev)
		}
	}

	// Load transcript events if the active Detail result card needs final_answer resolution.
	if ss.Detail != nil {
		if task := findSchedTask(ss, ss.Detail.MachineID, ss.Detail.TaskID); task != nil {
			displayed := schedule.LatestNotification(task)
			if ss.Detail.ResultCaptured {
				displayed = nil
				if ss.Detail.PinnedResultID != "" {
					for _, r := range schedule.AllRuns(task) {
						if r.ID == ss.Detail.PinnedResultID {
							displayed = r
							break
						}
					}
				} else {
					displayed = schedule.LatestNotification(task)
				}
			}
			if displayed != nil && displayed.SessionID != "" && !c.m.sync.SessionEventsSettled(displayed.SessionID) {
				c.m.sync.EnsureSessionEventsLoaded(ctx, displayed.SessionID)
			}
		}
	}

	// Keep active form's project config and models/settings in sync while open.
	if ss.ActiveFormKey != "" {
		if form := ss.Forms[ss.ActiveFormKey]; form != nil && form.Open {
			c.reconcileScheduleForm(ctx, form)
		}
	}
}

func (c *Core) reconcileScheduleForm(ctx *sync.StepContext, form *screens.ScheduleFormState) {
	if form == nil {
		return
	}
	st := c.m.state
	if form.Mode == corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_CREATE_NEW && form.ProjectKey != "" {
		var pickMachine string
		if form.MachineChosen || form.ConfigChosen {
			pickMachine = form.Config.MachineID
		}
		var pickAgent v1.Agent
		if form.ConfigChosen {
			pickAgent = form.AgentOverride
			if pickAgent == v1.Agent_AGENT_UNSPECIFIED {
				pickAgent = form.Config.Agent
			}
		}
		res := lists.ResolveProjectStart(&st.World, form.ProjectKey, pickMachine, pickAgent, nil, false)
		if res.Found {
			res.Config.Agent = lists.EffectiveAgent(res.Config.Agent)
			if !form.ConfigInitialized {
				form.Config = res.Config
				form.MachineID = res.Config.MachineID
				form.ConfigInitialized = true
				c.m.markScheduleDirty(corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_FORM)
			}
		}
		form.Config.Agent = lists.EffectiveAgent(form.Config.Agent)
		c.applyScheduleModelDefaults(form)
	}
	effMachineID := form.MachineID
	if effMachineID == "" {
		effMachineID = form.Config.MachineID
	}
	if effMachineID != "" && (form.Mode == corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_CREATE_NEW ||
		form.Mode == corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_EDIT_TARGET ||
		form.Mode == corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_EDIT_MODE) {
		c.m.maybeFetchMachineConfig(ctx, effMachineID, form.Config.Agent)
	}
}

func (c *Core) applyScheduleModelDefaults(form *screens.ScheduleFormState) {
	if form == nil || form.Config.MachineID == "" {
		return
	}
	st := c.m.state
	effAgent := lists.EffectiveAgent(form.Config.Agent)
	me := st.MachineModels[screens.MachineAgentKey{MachineID: form.Config.MachineID, Agent: effAgent}]
	se := st.MachineSettings[form.Config.MachineID]
	var models *v1.ListModelsResponse
	if me != nil && me.Loaded {
		models = me.Response
	}
	var settings *v1.DaemonSettings
	if se != nil && se.Loaded {
		settings = se.Settings
	}
	if form.Config.Model == "" && form.Config.Effort == "" && models != nil {
		if picked := lists.ProjectModel(form.Config, models, settings); picked != nil {
			form.Config = lists.PickModel(form.Config, picked)
		}
	}
	if len(models.GetModels()) > 0 || settings != nil {
		_, effort := lists.EffortChoices(form.Config, models, settings)
		if form.Config.Effort == "" && effort != "" {
			form.Config.Effort = effort
		}
	}
}

func (c *Core) dispatchSchedulesUI(ctx *sync.StepContext, ev *corev1.Event) ([]*corev1.Effect, bool) {
	st := c.m.state
	ss := ensureSchedState(st)
	var effects []*corev1.Effect

	switch k := ev.Event.(type) {
	case *corev1.Event_ScheduleCacheLoaded:
		c.m.schedActive = true
		if k.ScheduleCacheLoaded == nil {
			return nil, true
		}
		act := k.ScheduleCacheLoaded
		snap := ensureSchedMachine(ss, act.MachineId)
		if snap.CacheReadOpID != 0 && act.OpId != 0 && snap.CacheReadOpID != act.OpId {
			return nil, true
		}
		snap.CacheReading = false
		if !c.m.sync.CanUseMachineCache(act.MachineId) {
			snap.CacheRestored = true
			effects = append(effects, &corev1.Effect{
				Effect: &corev1.Effect_ScheduleDeleteCache{
					ScheduleDeleteCache: &corev1.ScheduleDeleteCacheEffect{MachineId: act.MachineId},
				},
			})
			c.m.markScheduleDirty(
				corev1.ScreenTarget_SCREEN_TARGET_SCHEDULES,
				corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_DETAIL,
				corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_SESSION_STRIP,
			)
			return effects, true
		}
		snap.CacheRestored = true
		if act.Error == "" && len(act.ResultJson) > 0 && !snap.Live {
			if tasks, err := schedule.DecodeTaskList(act.ResultJson); err == nil {
				snap.Tasks = tasks
				snap.Cached = true
				out := ss.Seen.Observe(act.MachineId, tasks, false)
				effects = append(effects, seenOutcomeEffects(ss, out)...)
				if ss.Detail != nil && ss.Detail.MachineID == act.MachineId {
					c.captureDetailResultIfNeeded(act.MachineId, ss.Detail.TaskID)
				}
			}
		}
		c.reconcileSchedules(ctx, &effects)
		c.m.markScheduleDirty(
			corev1.ScreenTarget_SCREEN_TARGET_SCHEDULES,
			corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_DETAIL,
			corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_SESSION_STRIP,
		)
		return effects, true

	case *corev1.Event_ScheduleCacheWritten:
		if k.ScheduleCacheWritten != nil && k.ScheduleCacheWritten.MachineId != "" {
			mid := k.ScheduleCacheWritten.MachineId
			if !c.m.sync.CanUseMachineCache(mid) {
				effects = append(effects, &corev1.Effect{
					Effect: &corev1.Effect_ScheduleDeleteCache{
						ScheduleDeleteCache: &corev1.ScheduleDeleteCacheEffect{MachineId: mid},
					},
				})
			}
		}
		return effects, true

	case *corev1.Event_ScheduleSeenLoaded:
		c.m.schedActive = true
		if k.ScheduleSeenLoaded == nil {
			return nil, true
		}
		act := k.ScheduleSeenLoaded
		toks := make([]schedule.SeenToken, 0, len(act.Tokens))
		for _, pt := range act.Tokens {
			if pt != nil {
				toks = append(toks, schedule.SeenToken{TaskID: pt.TaskId, RunID: pt.RunId})
			}
		}
		snap := ensureSchedMachine(ss, act.MachineId)
		snap.SeenLoading = false
		snap.SeenLoaded = true
		out := ss.Seen.LoadSeen(act.MachineId, toks, act.Found)
		effects = append(effects, seenOutcomeEffects(ss, out)...)
		c.m.markScheduleDirty(
			corev1.ScreenTarget_SCREEN_TARGET_SCHEDULES,
			corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_DETAIL,
			corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_SESSION_STRIP,
		)
		return effects, true

	case *corev1.Event_ScheduleLocalContext:
		wasActive := c.m.schedActive
		c.m.schedActive = true
		if k.ScheduleLocalContext != nil {
			lc := k.ScheduleLocalContext
			if lc.LocalTimezone != "" {
				ss.LocalTimezone = lc.LocalTimezone
			}
			if lc.TomorrowStartMs != 0 {
				ss.TomorrowStartMs = lc.TomorrowStartMs
			}
			if lc.DayAfterTomorrowStartMs != 0 {
				ss.DayAfterTomorrowStartMs = lc.DayAfterTomorrowStartMs
			}
		}
		if !wasActive {
			c.reconcileSchedules(ctx, &effects)
		}
		if len(ss.Machines) > 0 || ss.Detail != nil || ss.ActiveFormKey != "" {
			c.m.markAllScheduleScreensDirty()
		}
		return effects, true

	case *corev1.Event_ScheduleRefresh:
		c.m.schedActive = true
		if k.ScheduleRefresh != nil {
			mid := k.ScheduleRefresh.Value
			if mid != "" {
				if snap := ss.Machines[mid]; snap != nil {
					snap.PendingRefresh = true
					snap.HasFetchedOnce = false
				}
			} else {
				for _, snap := range ss.Machines {
					if snap != nil {
						snap.PendingRefresh = true
						snap.HasFetchedOnce = false
					}
				}
			}
			c.reconcileSchedules(ctx, &effects)
			c.m.markScheduleDirty(
				corev1.ScreenTarget_SCREEN_TARGET_SCHEDULES,
				corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_DETAIL,
				corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_SESSION_STRIP,
			)
		}
		return effects, true

	case *corev1.Event_ScheduleSetVisibleSession:
		c.m.schedActive = true
		if k.ScheduleSetVisibleSession != nil {
			out := ss.Seen.SetVisibleSession(k.ScheduleSetVisibleSession.Value)
			effects = append(effects, seenOutcomeEffects(ss, out)...)
			c.m.markScheduleDirty(
				corev1.ScreenTarget_SCREEN_TARGET_SCHEDULES,
				corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_DETAIL,
				corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_SESSION_STRIP,
			)
		}
		return effects, true

	case *corev1.Event_ScheduleListOpen:
		wasActive := c.m.schedActive
		c.m.schedActive = true
		if k.ScheduleListOpen != nil && k.ScheduleListOpen.GetValue() {
			ss.Seen.ShowList()
			if !wasActive {
				c.reconcileSchedules(ctx, &effects)
			}
			c.m.markScheduleDirty(corev1.ScreenTarget_SCREEN_TARGET_SCHEDULES)
		}
		return effects, true

	case *corev1.Event_ScheduleSelectTask:
		if k.ScheduleSelectTask != nil {
			ss.SelectedMachineID = k.ScheduleSelectTask.MachineId
			ss.SelectedTaskID = k.ScheduleSelectTask.TaskId
			c.m.markScheduleDirty(corev1.ScreenTarget_SCREEN_TARGET_SCHEDULES)
		}
		return nil, true

	case *corev1.Event_ScheduleToggleEnded:
		if k.ScheduleToggleEnded != nil {
			if k.ScheduleToggleEnded.Value != nil {
				ss.EndedExpanded = *k.ScheduleToggleEnded.Value
			} else {
				ss.EndedExpanded = !ss.EndedExpanded
			}
			c.m.markScheduleDirty(corev1.ScreenTarget_SCREEN_TARGET_SCHEDULES)
		}
		return nil, true

	case *corev1.Event_ScheduleMarkRead:
		if k.ScheduleMarkRead != nil {
			if k.ScheduleMarkRead.AllFresh {
				for mid, snap := range ss.Machines {
					if snap == nil {
						continue
					}
					for _, t := range snap.Tasks {
						if !schedule.Visible(t) {
							continue
						}
						if ss.Seen.HasNewResult(mid, t) {
							out := ss.Seen.MarkRead(mid, t)
							effects = append(effects, seenOutcomeEffects(ss, out)...)
						}
					}
				}
			} else {
				for _, ref := range k.ScheduleMarkRead.Tasks {
					if ref == nil {
						continue
					}
					if t := findSchedTask(ss, ref.MachineId, ref.TaskId); t != nil {
						out := ss.Seen.MarkRead(ref.MachineId, t)
						effects = append(effects, seenOutcomeEffects(ss, out)...)
					}
				}
			}
			c.m.markScheduleDirty(
				corev1.ScreenTarget_SCREEN_TARGET_SCHEDULES,
				corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_DETAIL,
				corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_SESSION_STRIP,
			)
		}
		return effects, true

	case *corev1.Event_ScheduleMarkUnread:
		if k.ScheduleMarkUnread != nil {
			mid, tid := k.ScheduleMarkUnread.MachineId, k.ScheduleMarkUnread.TaskId
			if t := findSchedTask(ss, mid, tid); t != nil {
				out := ss.Seen.MarkUnread(mid, t)
				effects = append(effects, seenOutcomeEffects(ss, out)...)
			}
			if k.ScheduleMarkUnread.Value && ss.Detail != nil && ss.Detail.MachineID == mid && ss.Detail.TaskID == tid {
				ss.Seen.CloseTask(mid, tid)
				ss.Detail = nil
				effects = append(effects, navEffect(corev1.ScheduleNavigateRoute_SCHEDULE_NAVIGATE_ROUTE_LIST, "", "", "", "", 0, ""))
			}
			c.m.markScheduleDirty(
				corev1.ScreenTarget_SCREEN_TARGET_SCHEDULES,
				corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_DETAIL,
				corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_SESSION_STRIP,
			)
		}
		return effects, true

	case *corev1.Event_ScheduleDetailOpen:
		c.m.schedActive = true
		if k.ScheduleDetailOpen != nil {
			mid, tid := k.ScheduleDetailOpen.MachineId, k.ScheduleDetailOpen.TaskId
			if ss.Detail == nil || ss.Detail.MachineID != mid || ss.Detail.TaskID != tid {
				ss.Detail = &screens.ScheduleDetailState{
					MachineID:           mid,
					TaskID:              tid,
					ExpandedQuietGroups: make(map[string]bool),
				}
			}
			ss.SelectedMachineID = mid
			ss.SelectedTaskID = tid
			out := ss.Seen.OpenTask(mid, tid)
			effects = append(effects, seenOutcomeEffects(ss, out)...)
			c.captureDetailResultIfNeeded(mid, tid)
			c.reconcileSchedules(ctx, &effects)
			c.m.markScheduleDirty(
				corev1.ScreenTarget_SCREEN_TARGET_SCHEDULES,
				corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_DETAIL,
				corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_SESSION_STRIP,
			)
		}
		return effects, true

	case *corev1.Event_ScheduleDetailHide:
		if k.ScheduleDetailHide != nil {
			mid, tid := k.ScheduleDetailHide.MachineId, k.ScheduleDetailHide.TaskId
			ss.Seen.HideTask(mid, tid)
			if ss.Detail != nil && ss.Detail.MachineID == mid && ss.Detail.TaskID == tid {
				ss.Detail.ResultCaptured = false
			}
			c.m.markScheduleDirty(
				corev1.ScreenTarget_SCREEN_TARGET_SCHEDULES,
				corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_DETAIL,
			)
		}
		return nil, true

	case *corev1.Event_ScheduleDetailClose:
		if k.ScheduleDetailClose != nil {
			mid, tid := k.ScheduleDetailClose.MachineId, k.ScheduleDetailClose.TaskId
			ss.Seen.CloseTask(mid, tid)
			if ss.Detail != nil && ss.Detail.MachineID == mid && ss.Detail.TaskID == tid {
				ss.Detail = nil
			}
			c.m.markScheduleDirty(
				corev1.ScreenTarget_SCREEN_TARGET_SCHEDULES,
				corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_DETAIL,
			)
		}
		return nil, true

	case *corev1.Event_ScheduleDetailShowLatestResult:
		if k.ScheduleDetailShowLatestResult != nil {
			mid, tid := k.ScheduleDetailShowLatestResult.MachineId, k.ScheduleDetailShowLatestResult.TaskId
			if t := findSchedTask(ss, mid, tid); t != nil {
				if latest := schedule.LatestNotification(t); latest != nil {
					if ss.Detail == nil || ss.Detail.MachineID != mid || ss.Detail.TaskID != tid {
						ss.Detail = &screens.ScheduleDetailState{
							MachineID:           mid,
							TaskID:              tid,
							ExpandedQuietGroups: make(map[string]bool),
						}
					}
					ss.Detail.PinnedResultID = latest.ID
					ss.Detail.ResultCaptured = true
					c.reconcileSchedules(ctx, &effects)
					c.m.markScheduleDirty(corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_DETAIL)
				}
			}
		}
		return effects, true

	case *corev1.Event_ScheduleDetailToggleQuietGroup:
		if k.ScheduleDetailToggleQuietGroup != nil {
			act := k.ScheduleDetailToggleQuietGroup
			if ss.Detail == nil || ss.Detail.MachineID != act.MachineId || ss.Detail.TaskID != act.TaskId {
				ss.Detail = &screens.ScheduleDetailState{
					MachineID:           act.MachineId,
					TaskID:              act.TaskId,
					ExpandedQuietGroups: make(map[string]bool),
				}
			}
			if ss.Detail.ExpandedQuietGroups == nil {
				ss.Detail.ExpandedQuietGroups = make(map[string]bool)
			}
			ss.Detail.ExpandedQuietGroups[act.Value] = !ss.Detail.ExpandedQuietGroups[act.Value]
			c.m.markScheduleDirty(corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_DETAIL)
		}
		return nil, true

	case *corev1.Event_ScheduleDetailShowAllRuns:
		if k.ScheduleDetailShowAllRuns != nil {
			act := k.ScheduleDetailShowAllRuns
			if ss.Detail == nil || ss.Detail.MachineID != act.MachineId || ss.Detail.TaskID != act.TaskId {
				ss.Detail = &screens.ScheduleDetailState{
					MachineID:           act.MachineId,
					TaskID:              act.TaskId,
					ExpandedQuietGroups: make(map[string]bool),
				}
			}
			ss.Detail.ShowAllRuns = act.Value
			c.m.markScheduleDirty(corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_DETAIL)
		}
		return nil, true

	case *corev1.Event_ScheduleDetailOpenRun:
		if k.ScheduleDetailOpenRun != nil {
			act := k.ScheduleDetailOpenRun
			if t := findSchedTask(ss, act.MachineId, act.TaskId); t != nil {
				for _, r := range schedule.AllRuns(t) {
					if r.ID == act.Value && r.SessionID != "" {
						effects = append(effects, navEffect(
							corev1.ScheduleNavigateRoute_SCHEDULE_NAVIGATE_ROUTE_SESSION,
							act.MachineId, act.TaskId, r.SessionID, r.ID, r.FirstSeq(), "",
						))
						break
					}
				}
			}
		}
		return effects, true

	case *corev1.Event_ScheduleOperate:
		if k.ScheduleOperate != nil {
			c.handleScheduleOperate(ctx, k.ScheduleOperate, &effects)
		}
		return effects, true

	case *corev1.Event_ScheduleRename:
		if k.ScheduleRename != nil {
			c.handleScheduleRename(ctx, k.ScheduleRename)
		}
		return nil, true

	case *corev1.Event_ScheduleSetNotify:
		if k.ScheduleSetNotify != nil {
			c.handleScheduleSetNotify(ctx, k.ScheduleSetNotify)
		}
		return nil, true

	case *corev1.Event_ScheduleUpdateMessage:
		if k.ScheduleUpdateMessage != nil {
			c.handleScheduleUpdateMessage(ctx, k.ScheduleUpdateMessage)
		}
		return nil, true

	case *corev1.Event_ScheduleAbandonMutation:
		if k.ScheduleAbandonMutation != nil {
			c.handleScheduleAbandonMutation(ctx, k.ScheduleAbandonMutation)
		}
		return nil, true

	case *corev1.Event_ScheduleRetryMutation:
		if k.ScheduleRetryMutation != nil {
			c.handleScheduleRetryMutation(ctx, k.ScheduleRetryMutation)
		}
		return nil, true

	case *corev1.Event_ScheduleFormOpen:
		c.m.schedActive = true
		if k.ScheduleFormOpen != nil {
			c.handleScheduleFormOpen(ctx, k.ScheduleFormOpen, &effects)
		}
		return effects, true

	case *corev1.Event_ScheduleFormClose:
		if k.ScheduleFormClose != nil {
			c.handleScheduleFormClose(k.ScheduleFormClose)
		}
		return nil, true

	case *corev1.Event_ScheduleFormSetMessage:
		if k.ScheduleFormSetMessage != nil {
			if f := c.resolveFormByKey(k.ScheduleFormSetMessage.FormKey); f != nil {
				f.Message = k.ScheduleFormSetMessage.Value
				f.Dirty = true
				c.m.markScheduleDirty(corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_FORM)
			}
		}
		return nil, true

	case *corev1.Event_ScheduleFormSetTitle:
		if k.ScheduleFormSetTitle != nil {
			if f := c.resolveFormByKey(k.ScheduleFormSetTitle.FormKey); f != nil {
				f.Title = schedule.NameInput(k.ScheduleFormSetTitle.Value)
				f.Dirty = true
				c.m.markScheduleDirty(corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_FORM)
			}
		}
		return nil, true

	case *corev1.Event_ScheduleFormSetNotify:
		if k.ScheduleFormSetNotify != nil {
			if f := c.resolveFormByKey(k.ScheduleFormSetNotify.FormKey); f != nil {
				f.NotifyOnFinish = k.ScheduleFormSetNotify.Value
				f.Dirty = true
				c.m.markScheduleDirty(corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_FORM)
			}
		}
		return nil, true

	case *corev1.Event_ScheduleFormSelectProject:
		if k.ScheduleFormSelectProject != nil {
			c.handleScheduleFormSelectProject(ctx, k.ScheduleFormSelectProject)
		}
		return nil, true

	case *corev1.Event_ScheduleFormSelectMachine:
		if k.ScheduleFormSelectMachine != nil {
			c.handleScheduleFormSelectMachine(ctx, k.ScheduleFormSelectMachine)
		}
		return nil, true

	case *corev1.Event_ScheduleFormSelectAgent:
		if k.ScheduleFormSelectAgent != nil {
			c.handleScheduleFormSelectAgent(ctx, k.ScheduleFormSelectAgent)
		}
		return nil, true

	case *corev1.Event_ScheduleFormSelectModel:
		if k.ScheduleFormSelectModel != nil {
			c.handleScheduleFormSelectModel(k.ScheduleFormSelectModel)
		}
		return nil, true

	case *corev1.Event_ScheduleFormUpdateTiming:
		if k.ScheduleFormUpdateTiming != nil {
			c.handleScheduleFormUpdateTiming(ctx, k.ScheduleFormUpdateTiming)
		}
		return nil, true

	case *corev1.Event_ScheduleFormSubmit:
		if k.ScheduleFormSubmit != nil {
			c.handleScheduleFormSubmit(ctx, k.ScheduleFormSubmit)
		}
		return nil, true

	case *corev1.Event_ScheduleFormRetryModels:
		if k.ScheduleFormRetryModels != nil {
			if f := c.resolveFormByKey(k.ScheduleFormRetryModels.FormKey); f != nil {
				effMachine := f.MachineID
				if effMachine == "" {
					effMachine = f.Config.MachineID
				}
				if effMachine != "" {
					delete(st.MachineModels, screens.MachineAgentKey{
						MachineID: effMachine,
						Agent:     lists.EffectiveAgent(f.Config.Agent),
					})
					delete(st.MachineSettings, effMachine)
					c.m.maybeFetchMachineConfig(ctx, effMachine, f.Config.Agent)
					c.m.markScheduleDirty(corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_FORM)
				}
			}
		}
		return nil, true
	}

	return nil, false
}

func (c *Core) resolveFormByKey(formKey string) *screens.ScheduleFormState {
	ss := ensureSchedState(c.m.state)
	if formKey == "" {
		formKey = ss.ActiveFormKey
	}
	return ss.Forms[formKey]
}

func (c *Core) handleScheduleOperate(ctx *sync.StepContext, act *corev1.ScheduleOperateAction, effects *[]*corev1.Effect) {
	ss := ensureSchedState(c.m.state)
	t := findSchedTask(ss, act.MachineId, act.TaskId)
	var opName string
	var cmd *v1.Command

	switch act.Operation {
	case corev1.ScheduleOperationKind_SCHEDULE_OPERATION_KIND_PAUSE:
		opName = "pause"
	case corev1.ScheduleOperationKind_SCHEDULE_OPERATION_KIND_RESUME:
		opName = "resume"
	case corev1.ScheduleOperationKind_SCHEDULE_OPERATION_KIND_CANCEL:
		opName = "cancel"
	case corev1.ScheduleOperationKind_SCHEDULE_OPERATION_KIND_RUN:
		opName = "run"
	case corev1.ScheduleOperationKind_SCHEDULE_OPERATION_KIND_RECOVER:
		if t == nil {
			return
		}
		sysCancelled := t.Status == "cancelled" && schedule.Visible(t)
		switch {
		case sysCancelled && schedule.OneShot(t):
			formKey := c.openEditFormForTask(ctx, corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_EDIT_TIMING, act.MachineId, act.TaskId, ss.LocalTimezone)
			if formKey != "" {
				*effects = append(*effects, navEffect(
					corev1.ScheduleNavigateRoute_SCHEDULE_NAVIGATE_ROUTE_FORM_TIMING,
					act.MachineId, act.TaskId, "", "", 0, formKey,
				))
			}
			return
		case sysCancelled:
			opName = "update"
			raw, err := schedule.EncodeSpec(t.Spec)
			if err != nil {
				return
			}
			cmd = &v1.Command{
				Kind: &v1.Command_Schedule{
					Schedule: &v1.ScheduleRequest{
						Operation:        "update",
						TaskId:           act.TaskId,
						SpecJson:         raw,
						ExpectedRevision: t.Revision,
					},
				},
			}
		case schedule.FailedOnce(t):
			opName = "run"
		default:
			opName = "resume"
		}
	default:
		return
	}

	if cmd == nil {
		var expRev uint64
		if t != nil {
			expRev = t.Revision
		}
		cmd = &v1.Command{
			Kind: &v1.Command_Schedule{
				Schedule: &v1.ScheduleRequest{
					Operation:        opName,
					TaskId:           act.TaskId,
					ExpectedRevision: expRev,
				},
			},
		}
	}

	slotKey := "schedule_op:" + act.MachineId + "/" + act.TaskId
	mutKey := "op:" + act.MachineId + "/" + act.TaskId
	c.submitSchedTaskMutation(ctx, slotKey, mutKey, act.MachineId, act.TaskId, "", opName, cmdKindScheduleOperate, cmd)
}

func (c *Core) handleScheduleRename(ctx *sync.StepContext, act *corev1.ScheduleTaskStringAction) {
	ss := ensureSchedState(c.m.state)
	t := findSchedTask(ss, act.MachineId, act.TaskId)
	if t == nil {
		return
	}
	spec, changed := schedule.RenamedSpec(t, act.Value)
	if !changed {
		return
	}
	c.submitTaskUpdateSpec(ctx, act.MachineId, act.TaskId, "", "rename", t.Revision, spec)
}

func (c *Core) handleScheduleSetNotify(ctx *sync.StepContext, act *corev1.ScheduleTaskBoolAction) {
	ss := ensureSchedState(c.m.state)
	t := findSchedTask(ss, act.MachineId, act.TaskId)
	if t == nil {
		return
	}
	spec := t.Spec
	if act.Value {
		spec.Notify = ""
	} else {
		spec.Notify = "agent_only"
	}
	c.submitTaskUpdateSpec(ctx, act.MachineId, act.TaskId, "", "notify", t.Revision, spec)
}

func (c *Core) handleScheduleUpdateMessage(ctx *sync.StepContext, act *corev1.ScheduleTaskStringAction) {
	ss := ensureSchedState(c.m.state)
	t := findSchedTask(ss, act.MachineId, act.TaskId)
	if t == nil || !schedule.MessageValid(act.Value) {
		return
	}
	spec := t.Spec
	spec.Message = act.Value
	c.submitTaskUpdateSpec(ctx, act.MachineId, act.TaskId, "", "message", t.Revision, spec)
}

func (c *Core) submitTaskUpdateSpec(ctx *sync.StepContext, machineID, taskID, formKey, opName string, revision uint64, spec schedule.Spec) {
	raw, err := schedule.EncodeSpec(spec)
	if err != nil {
		return
	}
	slotKey := "schedule_op:" + machineID + "/" + taskID
	mutKey := "op:" + machineID + "/" + taskID
	cmd := &v1.Command{
		Kind: &v1.Command_Schedule{
			Schedule: &v1.ScheduleRequest{
				Operation:        "update",
				TaskId:           taskID,
				SpecJson:         raw,
				ExpectedRevision: revision,
			},
		},
	}
	c.submitSchedTaskMutation(ctx, slotKey, mutKey, machineID, taskID, formKey, opName, cmdKindScheduleUpdate, cmd)
}

func (c *Core) handleScheduleAbandonMutation(ctx *sync.StepContext, act *corev1.ScheduleMutationRef) {
	ss := ensureSchedState(c.m.state)
	if act.FormKey != "" {
		if form := c.resolveFormByKey(act.FormKey); form != nil {
			isCreate := form.Mode == corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_CREATE_NEW ||
				form.Mode == corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_CREATE_FOR_SESSION
			if isCreate {
				slotKey := "schedule_create:" + form.FormKey
				c.m.sync.AbandonAttempt(ctx, slotKey)
				delete(c.m.frozenSchedOps, slotKey)
				clearSchedMutation(ss.Mutations["form:"+form.FormKey])
				form.FrozenSpec = nil
				form.SubmittedSpecJSON = ""
				c.m.markAllScheduleScreensDirty()
				return
			}
			if form.MachineID != "" && form.TaskID != "" {
				slotKey := "schedule_op:" + form.MachineID + "/" + form.TaskID
				c.m.sync.AbandonAttempt(ctx, slotKey)
				delete(c.m.frozenSchedOps, slotKey)
				clearSchedMutation(ss.Mutations["op:"+form.MachineID+"/"+form.TaskID])
				form.FrozenSpec = nil
				c.m.markAllScheduleScreensDirty()
				return
			}
		}
	}
	if act.MachineId != "" && act.TaskId != "" {
		slotKey := "schedule_op:" + act.MachineId + "/" + act.TaskId
		c.m.sync.AbandonAttempt(ctx, slotKey)
		delete(c.m.frozenSchedOps, slotKey)
		clearSchedMutation(ss.Mutations["op:"+act.MachineId+"/"+act.TaskId])
		c.m.markAllScheduleScreensDirty()
	}
}

func (c *Core) handleScheduleRetryMutation(ctx *sync.StepContext, act *corev1.ScheduleMutationRef) {
	ss := ensureSchedState(c.m.state)
	var slotKey, mutKey string
	if act.FormKey != "" {
		if form := c.resolveFormByKey(act.FormKey); form != nil {
			isCreate := form.Mode == corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_CREATE_NEW ||
				form.Mode == corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_CREATE_FOR_SESSION
			if isCreate {
				slotKey = "schedule_create:" + form.FormKey
				mutKey = "form:" + form.FormKey
			} else if form.MachineID != "" && form.TaskID != "" {
				slotKey = "schedule_op:" + form.MachineID + "/" + form.TaskID
				mutKey = "op:" + form.MachineID + "/" + form.TaskID
			}
		}
	}
	if slotKey == "" && act.MachineId != "" && act.TaskId != "" {
		slotKey = "schedule_op:" + act.MachineId + "/" + act.TaskId
		mutKey = "op:" + act.MachineId + "/" + act.TaskId
	}
	frozen, ok := c.m.frozenSchedOps[slotKey]
	if !ok || frozen.cmd == nil {
		if act.FormKey != "" {
			c.handleScheduleFormSubmit(ctx, &corev1.ScheduleFormBoolAction{
				FormKey: act.FormKey,
				Value:   true,
			})
		}
		return
	}
	mut := ensureSchedMutation(ss, mutKey)
	reqID, dispatched := c.m.sync.SubmitCommand(ctx, sync.SubmitCommandParams{
		SlotKey:   slotKey,
		MachineID: frozen.machineID,
		Command:   frozen.cmd,
	})
	if reqID != "" {
		c.m.pendingCmdMeta[reqID] = frozen.meta
	}
	if dispatched {
		mut.Busy = true
		mut.ErrorText = nil
		mut.NoticeText = nil
	}
	c.m.markAllScheduleScreensDirty()
}

func defaultTimingAndTrigger(nowMs int64, zone string, bounds schedule.CalendarBounds) (schedule.TimingDraft, schedule.Trigger) {
	td := schedule.NewTimingDraft(nowMs, zone, bounds)
	tr, _ := td.BuildTrigger(schedule.TimingBuildInput{NowMs: nowMs})
	if tr.Type == "" {
		tr = schedule.Trigger{Type: "daily", At: "08:00", Timezone: td.Zone}
	}
	return td, tr
}

func (c *Core) openEditFormForTask(ctx *sync.StepContext, mode corev1.ScheduleFormMode, machineID, taskID, localZone string) string {
	st := c.m.state
	ss := ensureSchedState(st)
	task := findSchedTask(ss, machineID, taskID)
	if task == nil {
		return ""
	}
	if localZone == "" {
		localZone = ss.LocalTimezone
	}
	formKey := "edit:" + machineID + "/" + taskID
	bounds := schedBounds(st)
	nowMs := c.m.sync.NowMs()
	td := schedule.TimingDraftFromTrigger(task.Spec.Trigger, nowMs, localZone, bounds)

	form := &screens.ScheduleFormState{
		FormKey:        formKey,
		Mode:           mode,
		Open:           true,
		MachineID:      machineID,
		TaskID:         taskID,
		Message:        task.Spec.Message,
		Title:          task.Spec.Title,
		NotifyOnFinish: task.Spec.NotifyOnFinish(),
		Trigger:        task.Spec.Trigger,
		Timing:         td,
		BaseTask:       schedule.CloneTask(task),
	}
	if task.Spec.Target.Type == "resume_session" {
		form.SessionID = task.Spec.Target.Session
	} else {
		form.ProjectKey = task.Spec.Target.Cwd
		form.Config = lists.StartConfig{
			MachineID: machineID,
			Cwd:       task.Spec.Target.Cwd,
			Agent:     schedAgentByIDString(task.Spec.Target.Agent),
			Model:     task.Spec.Target.Model,
			Effort:    task.Spec.Target.Effort,
			Mode:      schedApprovalFromString(task.Spec.Target.Mode),
		}
		form.ConfigInitialized = true
		c.m.maybeFetchMachineConfig(ctx, machineID, form.Config.Agent)
	}
	ss.Forms[formKey] = form
	ss.ActiveFormKey = formKey
	c.m.markScheduleDirty(corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_FORM)
	return formKey
}

func (c *Core) handleScheduleFormOpen(ctx *sync.StepContext, act *corev1.ScheduleFormOpenAction, effects *[]*corev1.Effect) {
	st := c.m.state
	ss := ensureSchedState(st)
	if act.LocalTimezone != "" {
		ss.LocalTimezone = act.LocalTimezone
	}
	localZone := act.LocalTimezone
	if localZone == "" {
		localZone = ss.LocalTimezone
	}
	bounds := schedBounds(st)

	switch act.Mode {
	case corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_CREATE_NEW:
		formKey := "create:new"
		if existing := ss.Forms[formKey]; existing != nil && existing.Open {
			mut := ss.Mutations["form:"+formKey]
			if existing.Dirty || (mut != nil && (mut.Busy || mut.Frozen)) {
				ss.ActiveFormKey = formKey
				c.m.markScheduleDirty(corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_FORM)
				return
			}
		}
		projKey := act.ProjectKey
		if projKey == "" && st.Scope != nil && !st.Scope.AllProjects && st.Scope.Key != "" {
			projKey = st.Scope.Key
		}
		if projKey == "" {
			projKey = st.StandaloneProjectKey
		}
		if projKey == "" {
			projRes := lists.BuildProjects(&st.World, "", st.RememberedScope, st.RememberedScopeToken, nil, st.Scope)
			for _, p := range projRes.Listed {
				if p != nil && !p.Hidden && p.Key != "" {
					projKey = p.Key
					break
				}
			}
		}
		nowMs := c.m.sync.NowMs()
		td, tr := defaultTimingAndTrigger(nowMs, localZone, bounds)
		form := &screens.ScheduleFormState{
			FormKey:     formKey,
			Mode:        corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_CREATE_NEW,
			Open:        true,
			MachineID:   act.MachineId,
			ProjectKey:  projKey,
			Trigger:     tr,
			Timing:      td,
			CreationKey: formKey,
		}
		if projKey != "" {
			psr := lists.ResolveProjectStart(&st.World, projKey, act.MachineId, 0, nil, false)
			if psr.Found {
				form.Config = psr.Config
				form.Config.Agent = lists.EffectiveAgent(form.Config.Agent)
				form.MachineID = form.Config.MachineID
				form.ConfigInitialized = true
				form.MachineChosen = act.MachineId != ""
			} else if act.MachineId != "" {
				form.Config = lists.StartConfig{
					MachineID: act.MachineId,
					Cwd:       projKey,
					Agent:     lists.DefaultAgent,
				}
				form.MachineChosen = true
			}
		}
		ss.Forms[formKey] = form
		ss.ActiveFormKey = formKey
		c.reconcileScheduleForm(ctx, form)
		c.m.markScheduleDirty(corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_FORM)

	case corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_CREATE_FOR_SESSION:
		formKey := "create:session:" + act.SessionId
		if existing := ss.Forms[formKey]; existing != nil && existing.Open {
			mut := ss.Mutations["form:"+formKey]
			if existing.Dirty || (mut != nil && (mut.Busy || mut.Frozen)) {
				ss.ActiveFormKey = formKey
				c.m.markScheduleDirty(corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_FORM)
				return
			}
		}
		mid := act.MachineId
		if mid == "" {
			if s := c.m.sync.SessionEntry(act.SessionId); s != nil {
				mid = s.MachineID
			}
		}
		nowMs := c.m.sync.NowMs()
		td, tr := defaultTimingAndTrigger(nowMs, localZone, bounds)
		form := &screens.ScheduleFormState{
			FormKey:     formKey,
			Mode:        corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_CREATE_FOR_SESSION,
			Open:        true,
			MachineID:   mid,
			SessionID:   act.SessionId,
			Trigger:     tr,
			Timing:      td,
			CreationKey: formKey,
		}
		ss.Forms[formKey] = form
		ss.ActiveFormKey = formKey
		c.m.markScheduleDirty(corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_FORM)

	case corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_EDIT_TIMING,
		corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_EDIT_TARGET,
		corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_EDIT_MODE,
		corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_EDIT_MESSAGE,
		corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_EDIT_RENAME:
		c.openEditFormForTask(ctx, act.Mode, act.MachineId, act.TaskId, localZone)
	}
	_ = effects
}

func (c *Core) handleScheduleFormClose(act *corev1.ScheduleFormBoolAction) {
	ss := ensureSchedState(c.m.state)
	formKey := act.FormKey
	if formKey == "" {
		formKey = ss.ActiveFormKey
	}
	form := ss.Forms[formKey]
	if form == nil {
		return
	}
	if act.Value {
		delete(ss.Forms, formKey)
	} else {
		form.Open = false
	}
	if ss.ActiveFormKey == formKey {
		ss.ActiveFormKey = ""
	}
	c.m.markScheduleDirty(corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_FORM)
}

func (c *Core) handleScheduleFormSelectProject(ctx *sync.StepContext, act *corev1.ScheduleFormSelectProjectAction) {
	st := c.m.state
	form := c.resolveFormByKey(act.FormKey)
	if form == nil {
		return
	}
	locked := form.Mode != corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_CREATE_NEW
	form.ProjectKey = act.ProjectKey
	form.Config.Cwd = act.ProjectKey
	if !locked && act.MachineId != "" {
		form.MachineID = act.MachineId
		form.Config.MachineID = act.MachineId
		form.MachineChosen = true
	}
	var pickMac string
	if locked {
		pickMac = form.MachineID
	} else if form.MachineChosen || form.ConfigChosen {
		pickMac = form.Config.MachineID
	}
	var pickAgent v1.Agent
	if form.ConfigChosen {
		pickAgent = form.AgentOverride
		if pickAgent == v1.Agent_AGENT_UNSPECIFIED {
			pickAgent = form.Config.Agent
		}
	}
	psr := lists.ResolveProjectStart(&st.World, act.ProjectKey, pickMac, pickAgent, nil, false)
	if psr.Found && !form.ConfigChosen {
		form.Config = psr.Config
		form.Config.Agent = lists.EffectiveAgent(form.Config.Agent)
		if locked {
			form.Config.MachineID = form.MachineID
		} else {
			form.MachineID = form.Config.MachineID
		}
		form.ConfigInitialized = true
	} else if locked {
		form.Config.MachineID = form.MachineID
	} else if act.MachineId != "" {
		form.Config.MachineID = act.MachineId
	}
	form.Dirty = true
	c.reconcileScheduleForm(ctx, form)
	c.m.markScheduleDirty(corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_FORM)
}

func (c *Core) handleScheduleFormSelectMachine(ctx *sync.StepContext, act *corev1.ScheduleFormStringAction) {
	form := c.resolveFormByKey(act.FormKey)
	if form == nil || act.Value == "" {
		return
	}
	mac := c.m.findMachine(act.Value)
	var agents []v1.Agent
	if mac != nil {
		agents = lists.MachineAgents(mac.Meta)
	}
	form.MachineID = act.Value
	form.Config = lists.SwitchMachine(form.Config, act.Value, agents)
	form.Config.Agent = lists.EffectiveAgent(form.Config.Agent)
	form.ConfigInitialized = true
	form.ConfigChosen = true
	form.MachineChosen = true
	form.AgentOverride = form.Config.Agent
	form.Dirty = true
	c.applyScheduleModelDefaults(form)
	c.m.maybeFetchMachineConfig(ctx, act.Value, form.Config.Agent)
	c.m.markScheduleDirty(corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_FORM)
}

func (c *Core) handleScheduleFormSelectAgent(ctx *sync.StepContext, act *corev1.ScheduleFormSelectAgentAction) {
	st := c.m.state
	form := c.resolveFormByKey(act.FormKey)
	if form == nil {
		return
	}
	effAgent := lists.EffectiveAgent(act.Agent)
	form.AgentOverride = effAgent
	effMac := form.MachineID
	if effMac == "" {
		effMac = form.Config.MachineID
	}
	psr := lists.ResolveProjectStart(&st.World, form.ProjectKey, effMac, effAgent, nil, false)
	form.Config = lists.SwitchAgent(psr.Config, &form.Config, effAgent)
	form.Config.Agent = lists.EffectiveAgent(form.Config.Agent)
	if effMac != "" {
		form.Config.MachineID = effMac
	}
	if form.ProjectKey != "" {
		form.Config.Cwd = form.ProjectKey
	}
	form.ConfigInitialized = true
	form.ConfigChosen = true
	form.Dirty = true
	c.applyScheduleModelDefaults(form)
	if effMac != "" {
		c.m.maybeFetchMachineConfig(ctx, effMac, form.Config.Agent)
	}
	c.m.markScheduleDirty(corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_FORM)
}

func (c *Core) handleScheduleFormSelectModel(act *corev1.ScheduleFormSelectModelAction) {
	form := c.resolveFormByKey(act.FormKey)
	if form == nil {
		return
	}
	form.Config.Agent = lists.EffectiveAgent(form.Config.Agent)
	form.Config.Model = act.Model
	if act.Effort != "" || act.Model != "" {
		form.Config.Effort = act.Effort
	}
	if act.Mode != nil {
		form.Config.Mode = *act.Mode
	}
	form.ConfigInitialized = true
	form.ConfigChosen = true
	form.Dirty = true
	c.m.markScheduleDirty(corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_FORM)
}

func (c *Core) handleScheduleFormUpdateTiming(ctx *sync.StepContext, act *corev1.ScheduleFormUpdateTimingAction) {
	st := c.m.state
	ss := ensureSchedState(st)
	form := c.resolveFormByKey(act.FormKey)
	if form == nil {
		return
	}
	bounds := schedBounds(st)
	nowMs := c.m.sync.NowMs()
	if nowMs == 0 {
		nowMs = bounds.Cal.NowMs
	}
	if act.ResetDraft {
		if form.Trigger.Type != "" {
			form.Timing = schedule.TimingDraftFromTrigger(form.Trigger, nowMs, ss.LocalTimezone, bounds)
		} else {
			form.Timing = schedule.NewTimingDraft(nowMs, ss.LocalTimezone, bounds)
			form.TimingApplied = false
		}
		form.TimingError = nil
		c.m.markScheduleDirty(corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_FORM)
		return
	}

	d := form.Timing
	fieldEdited := false
	if act.Kind != nil {
		if k := timingKindFromProto(*act.Kind); k != 0 {
			if k == schedule.TimingAfterTurn {
				d.AfterTurnUnselected = false
			} else if d.Kind != k {
				d.AfterTurnUnselected = false
			}
			if k == schedule.TimingOnce && d.Kind != schedule.TimingOnce && d.Date == "" {
				def := schedule.NewTimingDraft(nowMs, d.Zone, bounds)
				d.Date = def.Date
				if d.Time == "" {
					d.Time = def.Time
				}
			}
			d.Kind = k
			fieldEdited = true
		}
	}
	if act.Date != nil {
		d.Date = *act.Date
		fieldEdited = true
	}
	if act.Time != nil {
		d.Time = *act.Time
		fieldEdited = true
	}
	if act.Zone != nil {
		resUntilDate := ""
		if act.UntilDate != nil {
			resUntilDate = *act.UntilDate
		}
		var resUntilMs int64
		if act.UntilMs != nil {
			resUntilMs = *act.UntilMs
		}
		d = d.InZone(*act.Zone, resUntilDate, resUntilMs, bounds)
		fieldEdited = true
	} else {
		if act.UntilDate != nil {
			d.UntilDate = *act.UntilDate
			d.UntilEdited = true
			fieldEdited = true
		}
		if act.UntilMs != nil {
			d.UntilMs = *act.UntilMs
			fieldEdited = true
		}
	}
	if act.Workdays != nil {
		d.Workdays = *act.Workdays
		fieldEdited = true
	}
	if act.ReplaceWeekdays {
		wds := make([]int, 0, len(act.Weekdays))
		for _, w := range act.Weekdays {
			wds = append(wds, int(w))
		}
		d.Weekdays = wds
		fieldEdited = true
	}
	if act.Interval != nil {
		d.Interval = *act.Interval
		fieldEdited = true
	}
	if act.Unit != nil {
		if u := intervalUnitFromProto(*act.Unit); u != 0 {
			d.Unit = u
			fieldEdited = true
		}
	}
	if act.End != nil {
		if e := timingEndFromProto(*act.End); e != 0 {
			if e == schedule.EndCount && strings.TrimSpace(d.Count) == "" {
				d.Count = "10"
			}
			d.End = e
			fieldEdited = true
		}
	}
	if act.Count != nil {
		d.Count = *act.Count
		fieldEdited = true
	}
	if act.JitterMinutes != nil {
		v := *act.JitterMinutes
		if v < 0 {
			v = 0
		}
		d.JitterMinutes = v
		fieldEdited = true
	}

	if fieldEdited && !act.Apply {
		form.TimingError = nil
		form.TimingApplied = false
	}
	form.Timing = d

	if act.Apply {
		tr, errTxt := d.BuildTrigger(schedule.TimingBuildInput{
			NowMs:       nowMs,
			AtMs:        act.AtMs,
			UntilMs:     act.UntilMs,
			ZoneValid:   act.ZoneValid,
			AtDSTGap:    act.GetAtDstGap(),
			UntilDSTGap: act.GetUntilDstGap(),
		})
		if errTxt != nil {
			form.TimingError = errTxt
			form.TimingApplied = false
		} else {
			form.Trigger = tr
			form.TimingError = nil
			form.TimingApplied = true
			form.Dirty = true
		}
	}
	c.m.markScheduleDirty(corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_FORM)
}

func (c *Core) handleScheduleFormSubmit(ctx *sync.StepContext, act *corev1.ScheduleFormBoolAction) {
	st := c.m.state
	ss := ensureSchedState(st)
	form := c.resolveFormByKey(act.FormKey)
	if form == nil {
		return
	}

	effMachineID := form.MachineID
	if effMachineID == "" {
		effMachineID = form.Config.MachineID
	}
	if form.Mode == corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_CREATE_FOR_SESSION && form.SessionID != "" {
		if s := c.m.sync.SessionEntry(form.SessionID); s != nil {
			effMachineID = s.MachineID
		}
	}
	if !schedule.Writable(&st.World, effMachineID) {
		c.m.markScheduleDirty(corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_FORM)
		return
	}

	isCreate := form.Mode == corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_CREATE_NEW ||
		form.Mode == corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_CREATE_FOR_SESSION

	if isCreate {
		slotKey := "schedule_create:" + form.FormKey
		mutKey := "form:" + form.FormKey
		mut := ensureSchedMutation(ss, mutKey)
		if mut.Busy {
			return
		}

		// Reusing frozen spec preserves exact RequestID/AttemptID/Spec identity across unknown retries.
		var rawSpec []byte
		if mut.Frozen && form.SubmittedSpecJSON != "" {
			rawSpec = []byte(form.SubmittedSpecJSON)
		} else {
			var spec schedule.Spec
			if form.FrozenSpec != nil {
				spec = *form.FrozenSpec
			} else {
				name := schedule.ResolveName(form.Title, form.Message)
				notify := form.NotifyOnFinish
				var target schedule.Target
				if form.Mode == corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_CREATE_FOR_SESSION {
					if !schedule.MessageValid(form.Message) {
						return
					}
					target = schedule.Target{
						Type:    "resume_session",
						Session: form.SessionID,
					}
				} else {
					if !schedule.MessageValid(form.Message) || form.ProjectKey == "" {
						return
					}
					mac := c.m.findMachine(effMachineID)
					mode := form.Config.Mode
					if mode == v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED {
						mode = lists.DefaultModeOf(mac, form.Config.Agent).Mode
					}
					if mode == v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED || mode == v1.ApprovalMode_APPROVAL_MODE_PLAN {
						return
					}
					if mode == v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS && !act.Value && !mut.Frozen {
						return
					}
					target = schedule.Target{
						Type:   "new_session",
						Cwd:    form.ProjectKey,
						Agent:  schedAgentIDString(form.Config.Agent),
						Model:  form.Config.Model,
						Effort: form.Config.Effort,
						Mode:   screens.ApprovalModeToScheduleString(mode),
					}
				}
				notifyStr := ""
				if !notify {
					notifyStr = "agent_only"
				}
				spec = schedule.Spec{
					Title:   name,
					Message: form.Message,
					Notify:  notifyStr,
					Trigger: form.Trigger,
					Target:  target,
				}
				specCopy := spec
				form.FrozenSpec = &specCopy
			}
			encoded, err := schedule.EncodeSpec(spec)
			if err != nil {
				mut.ErrorText = text.Plain(err.Error())
				c.m.markScheduleDirty(corev1.ScreenTarget_SCREEN_TARGET_SCHEDULE_FORM)
				return
			}
			rawSpec = encoded
			form.SubmittedSpecJSON = string(encoded)
		}

		cmd := &v1.Command{
			Kind: &v1.Command_Schedule{
				Schedule: &v1.ScheduleRequest{
					Operation: "create",
					SpecJson:  rawSpec,
				},
			},
		}
		c.submitSchedTaskMutation(ctx, slotKey, mutKey, effMachineID, "", form.FormKey, "create", cmdKindScheduleCreate, cmd)
		return
	}

	// Edit existing task modes.
	base := form.BaseTask
	if base == nil {
		base = findSchedTask(ss, effMachineID, form.TaskID)
	}
	if base == nil {
		return
	}
	spec := base.Spec
	switch form.Mode {
	case corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_EDIT_TIMING:
		spec.Trigger = form.Trigger
	case corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_EDIT_MESSAGE:
		if !schedule.MessageValid(form.Message) {
			return
		}
		spec.Message = form.Message
	case corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_EDIT_RENAME:
		renamed, changed := schedule.RenamedSpec(base, form.Title)
		if !changed {
			c.handleScheduleFormClose(&corev1.ScheduleFormBoolAction{FormKey: form.FormKey, Value: true})
			return
		}
		spec = renamed
	case corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_EDIT_TARGET,
		corev1.ScheduleFormMode_SCHEDULE_FORM_MODE_EDIT_MODE:
		mac := c.m.findMachine(effMachineID)
		mode := form.Config.Mode
		if mode == v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED {
			mode = lists.DefaultModeOf(mac, form.Config.Agent).Mode
		}
		if mode == v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED || mode == v1.ApprovalMode_APPROVAL_MODE_PLAN {
			return
		}
		if mode == v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS && !act.Value {
			mut := ss.Mutations["op:"+effMachineID+"/"+form.TaskID]
			if mut == nil || !mut.Frozen {
				return
			}
		}
		spec.Target = schedule.Target{
			Type:   "new_session",
			Cwd:    form.ProjectKey,
			Agent:  schedAgentIDString(form.Config.Agent),
			Model:  form.Config.Model,
			Effort: form.Config.Effort,
			Mode:   screens.ApprovalModeToScheduleString(mode),
		}
	}
	c.submitTaskUpdateSpec(ctx, effMachineID, form.TaskID, form.FormKey, "update", base.Revision, spec)
}
