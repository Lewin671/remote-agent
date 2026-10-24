#!/usr/bin/env python3
"""Temporarily reintroduce reviewed defects and require assertion failures.

Run from this checkout with no concurrent compiler reading these sources.
The original bytes are restored even when a check fails. No build error counts
as a detected mutation. --clients also checks client attention and push visibility.
"""
import argparse
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DAEMON = "./internal/daemon"
checks = []

def go(name, file, before, after, test, package=DAEMON):
    checks.append((name, file, before, after, ["go", "test", package, "-count=1", "-run", "^" + test + "$"], "--- FAIL:"))

go("01-occurrence-date", "internal/daemon/schedule.go", "calendarDate(t.Spec.Trigger, t.Active.ScheduledAt)", "calendarDate(t.Spec.Trigger, now)", "TestCalendarCatchupOccupiesOccurrenceDateOnly")
go("01-skipped-date", "internal/daemon/schedule.go", "func nextCalendar(t *schedule.Task, now time.Time) time.Time {", "func nextCalendar(t *schedule.Task, now time.Time) time.Time { if len(t.Runs)>0 {return t.Spec.Trigger.NextCalendar(now.Add(24*time.Hour))};", "TestCalendarCatchupOccupiesOccurrenceDateOnly")
go("03-create-until", "internal/daemon/schedule.go", "if tr.Until != nil && now.After(*tr.Until) {", "if false && tr.Until != nil && now.After(*tr.Until) {", "TestCalendarDeadlineAndEditBoundaries")
go("03-run-until", "internal/daemon/schedule.go", "if until := t.Spec.Trigger.Until; until != nil && now.After(*until) {", "if until := t.Spec.Trigger.Until; false && until != nil && now.After(*until) {", "TestCalendarDeadlineAndEditBoundaries")
go("03-edit-next", "internal/daemon/schedule.go", "at := nextCalendar(t, now)", "at := tr.NextCalendar(now)", "TestCalendarDeadlineAndEditBoundaries")
go("04-followup", "internal/daemon/schedule_events.go", 'if end := e.GetTurnEnded(); end != nil {\n\t\tif end.ScheduledTaskId', 'if end := e.GetTurnEnded(); end != nil {\n s.scheduledTask,s.scheduledRun,s.scheduledNotifications="","",0\n\t\tif end.ScheduledTaskId', "TestScheduledBackgroundFollowupKeepsContext")
go("05-origin", "internal/daemon/schedule_events.go", "} else if humanOrigin(e.Origin) {", "} else if e.Origin.GetKind()!=pb.OriginKind_ORIGIN_KIND_SYSTEM {", "TestTakeoverNeedsVerifiedHumanOrExplicitResume")
go("05-resume", "internal/daemon/rpc.go", "h.d.resumeContextBy(ctx, s.meta.ID, originOf(ctx))", "h.d.resumeContext(ctx, s.meta.ID)", "TestExplicitResumeRPCClaimsScheduledSession")
go("06-interjection", "internal/daemon/schedule_events.go", "if humanOrigin(e.Origin) {", "if false && humanOrigin(e.Origin) {", "TestHumanInterjectionMakesScheduledTurnOrdinary")
go("07-read", "internal/daemon/session.go", "if !s.readRollout() {\n\t\t\treturn nil", "if false && !s.readRollout() {\n\t\t\treturn nil", "TestScheduledClosureWaitsForReadableTranscript")
go("08-input-order", "internal/daemon/inject.go", "if !humanOrigin(origin) {", "if true || !humanOrigin(origin) {", "TestScheduledCloseAndHumanAdmissionHaveOneOrder")
go("08-backoff", "internal/daemon/schedule_events.go", "&& !time.Now().Before(s.scheduledCloseAfter)", "", "TestScheduledCloseBacksOffUntilNewEvent")
go("09-uncertain", "internal/daemon/schedule_events.go", "uncertain[key] != s.meta.ID && ", "", "TestUncertainRunNeverClosesAutomatically")
go("10-revision", "internal/daemon/schedule.go", "if !bytes.Equal(visible, d.scheduleVisible) {", "if true || !bytes.Equal(visible, d.scheduleVisible) {", "TestScheduleRevisionIgnoresProgressCursors")
go("12-summary", "internal/daemon/schedule.go", "run.FinalSummary = finalRunSummary(msg.Text)", "run.FinalSummary = summary(msg.Text, 200)", "TestRunSummaryUsesWholeFinalAnswer")
go("13-minimum", "internal/daemon/schedule_security.go", "\t{\n\t\ttr := t.Spec.Trigger", "\tif freeze {\n\t\ttr := t.Spec.Trigger", "TestMCPRunAndResumeRecheckMinimumIntervals")
go("14-relay-quota", "internal/relay/sync.go", 'strings.HasPrefix(refused.Message(), "relay_session_limit:")', 'false && strings.HasPrefix(refused.Message(), "relay_session_limit:")', "TestRelaySessionLimitReachesBackend", "./internal/relay")
go("14-server-quota", "internal/server/accounts.go", "relay_session_limit: this account has reached its session limit on this relay", "this account has reached its session limit on this relay", "TestHiddenSessionsConsumeTrialLimitWithExplicitReason", "./internal/server")
for doc in ["scope", "architecture"]:
    file = "docs/"+doc+".md"
    disclosure = next(line for line in (ROOT/file).read_text().splitlines()
                      if "NOTIFY_KIND_SCHEDULED" in line and "TURN_ENDED" in line and "server 可以据此推断" in line)
    go("15-disclosure-"+doc, file, disclosure+"\n", "", "TestScheduledPushMetadataDisclosure", "./internal/protocol")
go("16-creation", "internal/daemon/daemon.go", 'ScheduledTaskID: origin.GetScheduledTaskId(), TaskOwned: origin.GetScheduledTaskId() != "",', 'ScheduledTaskID: "", TaskOwned: false,', "TestScheduledCreationWritesOwnership")
go("16-loop", "internal/daemon/schedule.go", "for _, s := range d.scheduledClosures() {", "for _, s := range []*session{} {", "TestRealScheduleLoopClosesCompletedCLI")

go("r2-01-replay-write", "internal/daemon/schedule_events.go", "if s.meta.TaskOwned {\n\t\ts.meta.TaskOwned = false", "if true {\n\t\ts.meta.TaskOwned = false", "TestOrdinaryHumanReplayDoesNotWriteMetadata")

go("r2-02-cold-list", "internal/server/store.go", "AND ended_ms = 0 ORDER BY", "AND ended_ms = 0 AND NOT EXISTS (SELECT 1 FROM task_owned_sessions AS owned WHERE owned.account_id=sessions.account_id AND owned.session_id=sessions.session_id) ORDER BY", "TestColdListIncludesLiveTaskOwnedApprovals", "./internal/server")

go("r2-03-resume", "internal/daemon/schedule.go", 't.Spec.Trigger.Type == "interval" || t.Spec.Trigger.Type == "calendar" {', 'false {', "TestRecurringResumeAndManualFailures")


go("r2-05-current-turn", "internal/daemon/schedule_events.go", 'task.ID == s.meta.ScheduledTaskID && run != nil && run.ID == s.meta.ScheduledRunID', 'run != nil', "TestMCPRestrictionUsesCurrentTurnNotQueue")
go("r2-05-queued-human", "internal/daemon/schedule_events.go", 's.claimOwnershipLocked()\n\t}\n\tif st :=', 's.takeOverLocked()\n\t}\n\tif st :=', "TestMCPRestrictionUsesCurrentTurnNotQueue")

go("r2-06-admission-retry", "internal/daemon/inject.go", '|| retries >= 1', '|| retries < 0', "TestHumanAdmissionBoundsRunningResumeRetry")


go("r2-08-minimum-reason", "internal/daemon/schedule.go", 't.Reason = "agent_schedule_interval_below_minimum"', 't.Reason = "agent_schedule_permissions_changed"', "TestMCPDispatchReportsMinimumIntervalReason")

go("r2-09-followup-id", "internal/daemon/schedule.go", 'for i := range t.Runs {\n\t\tchanged = d.watchScheduledFollowup', 'for i := len(t.Runs)-1; i<len(t.Runs); i++ {\n\t\tchanged = d.watchScheduledFollowup', "TestFollowupSurvivesLaterSkippedRun")
go("r2-09-terminal-followup", "internal/daemon/schedule.go", '!hasPendingScheduledFollowups(t)', '(len(t.Runs)==0 || t.Runs[len(t.Runs)-1].Status!="completed" || t.Runs[len(t.Runs)-1].FollowupsClosed)', "TestFollowupSurvivesLaterSkippedRun")

go("r2-03-manual-late", "internal/daemon/schedule.go", 'if !run.Manual {\n\t\t\t\t\td.lateRuns', 'if true {\n\t\t\t\t\td.lateRuns', "TestManualTrialFailureBoundaries")



# Third review: replace shadow-state mutations with independent-request invariants.
go("r3-A-request-plan", "internal/daemon/schedule.go", 't.ManualRequest = &schedule.ManualRequest{', 't.Status="scheduled";t.NextRunAt=&now; t.ManualRequest = &schedule.ManualRequest{', "TestManualExecutionLifecycleMatrix")
go("r3-A-count", "internal/daemon/schedule.go", 'request := t.ManualRequest', 't.Dispatches++; request := t.ManualRequest', "TestManualExecutionLifecycleMatrix")
go("r3-A-date", "internal/daemon/schedule.go", 'request := t.ManualRequest', 't.LastCalendarDate="manual-date"; request := t.ManualRequest', "TestManualExecutionLifecycleMatrix")
go("r3-A-results", "internal/daemon/schedule.go", 'if manual {', 'if false && manual {', "TestManualExecutionLifecycleMatrix")
go("r3-A-queue-plan", "internal/daemon/schedule.go", '!run.Manual && t.Status != "paused"', 't.Status != "paused"', "TestManualExecutionLifecycleMatrix")
go("r3-A-queue-cursor", "internal/daemon/schedule.go", 'if !run.Manual {\n\t\tt.NextRunAt = nil', 'if true {\n\t\tt.NextRunAt = nil', "TestManualExecutionLifecycleMatrix")
go("r3-A-restart", "internal/daemon/schedule.go", 'if !manual {', 'if true || !manual {', "TestManualExecutionLifecycleMatrix")
go("r3-A-pause", "internal/daemon/schedule.go", 't.Reason = "user_paused"\n\t\tt.ManualRequest = nil', 't.Reason = "user_paused"', "TestManualPendingControlsAndAdmission")
go("r3-A-cancel", "internal/daemon/schedule.go", 't.Reason = "user_cancelled"\n\t\tt.ManualRequest = nil', 't.Reason = "user_cancelled"', "TestManualPendingControlsAndAdmission")
go("r3-A-serialize", "internal/daemon/schedule.go", 'if t.Active == nil && t.ManualRequest != nil {', 'if t.ManualRequest != nil {', "TestManualAndAutomaticSerialize")
go("r3-A-reject-state", "internal/daemon/schedule.go", 't.Status == "uncertain" || t.Status == "failed" && !oneShotTrigger(t.Spec.Trigger) || t.Terminal() ||', '', "TestManualPendingControlsAndAdmission")
go("r3-A-consume-request", "internal/daemon/schedule.go", 'request := t.ManualRequest\n\tt.ManualRequest = nil', 'request := t.ManualRequest', "TestManualExecutionLifecycleMatrix")
go("r3-A-permission", "internal/daemon/schedule.go", 'run.Status, run.Reason = "failed", "agent_schedule_permissions_changed"', 't.Status="paused";run.Status, run.Reason = "failed", "agent_schedule_permissions_changed"', "TestManualPermissionRejectionDoesNotPausePlan")

go("r3-B-association", "internal/daemon/backend.go", 'run.ID == runID &&', 'run.ID == runID && run.SessionID != "" &&', "TestPublicationRefusalUsesScheduledClosure")
go("r3-B-route", "internal/daemon/schedule_events.go", 'rejected[key] = run.SessionID', '// quota closure route removed', "TestPublicationRefusalUsesScheduledClosure")
go("r3-B-busy", "internal/daemon/schedule_events.go", 's.scheduledCloseRejected ||', 'false ||', "TestPublicationRefusalUsesScheduledClosure")
go("r3-B-read", "internal/daemon/session.go", 'if !s.readRollout() {\n\t\t\treturn nil', 'if false && !s.readRollout() {\n\t\t\treturn nil', "TestQuotaClosureReusesReadAndTakeoverSafety")
go("r3-B-owner", "internal/daemon/schedule_events.go", 'return s.meta.TaskOwned &&', 'return true &&', "TestQuotaClosureReusesReadAndTakeoverSafety")
go("r3-B-resume", "internal/daemon/backend.go", 'run.ID == runID &&', 'true &&', "TestRelayQuotaIgnoresResumeSession")
go("r3-B-no-synchronous-close", "internal/daemon/backend.go", 'now := time.Now().UTC()', '_, _ = s.interruptBy(originSystem); now := time.Now().UTC()', "TestQuotaClosureDoesNotBlockScheduleLoop")

go("r3-C-phase", "internal/daemon/schedule.go", 'next := nextInterval(t, now)', 'next := now.Add(time.Duration(tr.IntervalSeconds)*time.Second)', "TestIntervalResumePreservesPhase")
go("r3-C-future", "internal/daemon/schedule.go", 'if t.NextRunAt != nil && t.NextRunAt.After(now) && (last == nil || t.NextRunAt.After(last.ScheduledAt)) {', 'if false && t.NextRunAt != nil && t.NextRunAt.After(now) && (last == nil || t.NextRunAt.After(last.ScheduledAt)) {', "TestIntervalResumePreservesPhase")
go("r3-C-initial-delay", "internal/daemon/schedule.go", 't.IntervalPhase = &at', 'tr.At=&at; t.IntervalPhase=&at', "TestIntervalInitialDelayPhaseSurvivesEditAndRestart")

go("r3-C-ignore-manual-history", "internal/daemon/schedule.go", 'if run.Manual || run.Status == "skipped" {', 'if run.Status == "skipped" {', "TestIntervalResumePreservesPhase")

go("r3-A-late-admission", "internal/daemon/schedule.go", 'q.Operation != "run" && d.reconfirmScheduledRun(t, now)', 'd.reconfirmScheduledRun(t, now)', "TestManualAdmissionDoesNotReconfirmAutomaticRun")

go("r3-B-recovered-association", "internal/daemon/backend.go", 'run.ID != runID || run.SessionID != "" && run.SessionID != id', 'run.ID != runID || run.SessionID != id', "TestPublicationRefusalUsesScheduledClosure")

go("r4-01-definition", "internal/daemon/schedule.go", 't.IntervalPhase = &at', 'tr.At=&at; t.IntervalPhase=&at', "TestIntervalDefinitionRoundTripDoesNotRedispatch")
go("r4-01-update", "internal/daemon/schedule.go", 'at := nextInterval(t, now)', 'at := *t.IntervalPhase', "TestIntervalDefinitionRoundTripDoesNotRedispatch")
go("r4-01-guard", "internal/daemon/schedule.go", 'last != nil && last.ScheduledAt.Equal(*t.NextRunAt)', 'false && last != nil && last.ScheduledAt.Equal(*t.NextRunAt)', "TestAutomaticDispatchRejectsConsumedOccurrence")

go("r4-02-wait-cursor", "internal/daemon/schedule.go", 'd.lateRuns[t.ID] != "" || t.Status == "uncertain"', 't.Status == "uncertain"', "TestPendingManualWaitsForAutomaticConfirmation")
go("r4-02-wait-state", "internal/daemon/schedule.go", 'd.lateRuns[t.ID] != "" || t.Status == "uncertain"', 'd.lateRuns[t.ID] != ""', "TestPendingManualWaitsForAutomaticConfirmation")
go("r4-02-drop", "internal/daemon/schedule.go", 'case t.Status == "failed" && !(request.RetryFailed && oneShotTrigger(t.Spec.Trigger)) || t.Terminal():', 'case false:', "TestPendingManualSkipsWhenAutomaticStops")
go("r4-02-id", "internal/daemon/schedule.go", 'last := &t.Runs[index]', 'index=len(t.Runs)-1; last := &t.Runs[index]', "TestLateConfirmationFindsRunByID")
go("r4-02-rollback", "internal/daemon/schedule.go", 'd.lateRuns[t.ID] = lateRun', 'd.lateRuns[t.ID] = before.Runs[len(before.Runs)-1].ID; _=lateRun', "TestLateConfirmationRollbackKeepsOriginalRunID")

go("r4-03-lifecycle", "internal/daemon/schedule.go", 'if snapshot.Spec.Target.Type == "resume_session" {', 'if false && snapshot.Spec.Target.Type == "resume_session" {', "TestScheduledResumeRechecksAfterAutomaticClose")

go("r4-04-accepted", "internal/daemon/commit.go", 's.claimQueuedHumanOwnershipLocked()\n\tdefer s.poke()', 'defer s.poke()', "TestAcceptedHumanQueueOwnsSessionWithoutQueuedEvent")
go("r4-04-replaced", "internal/daemon/commit.go", 's.claimQueuedHumanOwnershipLocked()\n\t\t\ts.poke()', 's.poke()', "TestAcceptedHumanQueueOwnsSessionWithoutQueuedEvent")
go("r4-04-restore", "internal/daemon/session.go", 's.claimQueuedHumanOwnershipLocked()', '// canonical takeover omitted', "TestCanonicalHumanQueueRepairsOwnershipOnRestore")

go("r4-05-resume-error", "internal/daemon/schedule.go", 'if t.Status == "failed" && oneShotTrigger(t.Spec.Trigger) {', 'if false && t.Status == "failed" && oneShotTrigger(t.Spec.Trigger) {', "TestFailedOneShotResumeRequiresExplicitRetry")
go("r4-05-intent", "internal/daemon/schedule.go", 'RetryFailed: t.Status == "failed"', 'RetryFailed: false', "TestFailedOneShotResumeRequiresExplicitRetry")
go("r4-05-old-request", "internal/daemon/schedule.go", 'request.RetryFailed && oneShotTrigger(t.Spec.Trigger)', 'oneShotTrigger(t.Spec.Trigger)', "TestOneShotPendingBeforeFailureIsNotExplicitRetry")

go("r5-01-recurring-only", "internal/daemon/schedule.go", 'recurringTrigger(t.Spec.Trigger) && last != nil', 'last != nil', "TestFailedAtDefinitionUpdateRetriesSameOccurrence")

go("r5-02-result", "internal/daemon/schedule.go", 'if retryFailed && oneShotTrigger(t.Spec.Trigger)', 'if false && retryFailed && oneShotTrigger(t.Spec.Trigger)', "TestFailedOneShotResumeRequiresExplicitRetry")
go("r5-02-persist-intent", "internal/daemon/schedule.go", 'RetryFailed: request.RetryFailed', 'RetryFailed: false', "TestFailedOneShotRetryOutcome")
go("r5-02-success-only", "internal/daemon/schedule.go", '&& status == "completed" {', '{', "TestFailedOneShotRetryOutcome")

go("r5-03-watch-status", "internal/daemon/schedule.go", 'if !pendingScheduledFollowup(run) {', 'if run.Status != "completed" || !pendingScheduledFollowup(run) {', "TestNonCompletedScheduledFollowups")
go("r5-03-pending-status", "internal/daemon/schedule.go", 'if pendingScheduledFollowup(&run) {', 'if run.Status == "completed" && pendingScheduledFollowup(&run) {', "TestNonCompletedScheduledFollowups")

go("r5-03-context-start", "internal/daemon/schedule.go", 'return run.MessageSeen && !run.FollowupsClosed', 'return !run.FollowupsClosed', "TestScheduleLateConfirmationResumesRun")

go("r7-closure-definition", "internal/daemon/schedule_events.go", 'for _, task := range d.tasks {\n\t\tfor _, run := range task.Runs {', 'for _, task := range d.tasks {\n if task.Spec.Target.Type != "new_session" {continue}\n\t\tfor _, run := range task.Runs {', "TestRejectedCreationSurvivesTargetEdit")
go("r7-publication-definition", "internal/daemon/backend.go", 'if t == nil || runID == "" {', 'if t == nil || t.Spec.Target.Type != "new_session" || runID == "" {', "TestLatePublicationRefusalAfterTargetEdit")
go("r7-creation-key", "internal/daemon/schedule_events.go", 'key := scheduledRunKey{s.meta.ScheduledTaskID, s.meta.ScheduledRunID}', 'key := scheduledRunKey{s.meta.ScheduledTaskID, "later-resume-run"}', "TestClosureUsesCreationRunIdentity")
go("r7-creation-uncertainty", "internal/daemon/schedule_events.go", 'uncertain[key] != s.meta.ID', 'len(uncertain) == 0', "TestClosureUsesCreationRunIdentity")
go("r7-pre-delivery", "internal/daemon/schedule_events.go", 'task.ID == s.meta.ScheduledTaskID && run != nil && run.ID == s.meta.ScheduledRunID', 'task.Spec.Target.Type == "new_session" && run != nil', "TestPreDeliveryRestrictionUsesCreationIdentity")

go("r8-notify-read", "internal/daemon/schedule_events.go", 'if !s.readRolloutLocked() {', 'if false && !s.readRolloutLocked() {', "TestNotifyUserReadsTranscriptBeforeAdmission")
go("r8-notify-read-failure", "internal/daemon/schedule_events.go", 'if !s.readRolloutLocked() {', 'if !s.readRolloutLocked() && false {', "TestNotifyUserRequiresCommittedTranscript")

go("r9-schedule-admission", "internal/daemon/schedule.go", 'bound.admitScheduledContext(q.Operation,', 'func(_ string, decide func() error) error { return decide() }(q.Operation,', "TestMCPAdmissionReadsTranscriptBeforeEveryMutation")
go("r9-admission-read", "internal/daemon/schedule_events.go", 'if !s.readRolloutLocked() {', 'if false && !s.readRolloutLocked() {', "TestMCPAdmissionReadsTranscriptBeforeEveryMutation")
go("r9-admission-failure", "internal/daemon/schedule_events.go", 'if !s.readRolloutLocked() {', 'if !s.readRolloutLocked() && false {', "TestContextDependentScheduleOperationsRequireCommittedAdmission")
go("r9-interrupt-admission", "internal/daemon/session.go", 's.withLatestOrCachedContext("interrupt",', 'func(_ string, decide func() error) error { return decide() }("interrupt",', "TestContextRequestPathsShareAdmission")
go("r9-hook-admission", "internal/daemon/session.go", 's.withLatestOrCachedContext("hook",', 'func(_ string, decide func() error) error { return decide() }("hook",', "TestContextRequestPathsShareAdmission")
go("r9-exit-admission", "internal/daemon/session.go", 'func (s *session) settleExited() error {', 'func (s *session) settleExited() error { if !s.readRollout() {return nil};', "TestContextRequestPathsShareAdmission")

go("r9-permission-admission", "internal/daemon/session.go", 's.withLatestOrCachedContext("permission request",', 'func(_ string, decide func() error) error { return decide() }("permission request",', "TestHooksRefreshOrUseCachedContext")
go("r9-hook-start-admission", "internal/daemon/session.go", 's.withLatestOrCachedContext("session start hook",', 'func(_ string, decide func() error) error { return decide() }("session start hook",', "TestHooksRefreshOrUseCachedContext")

go("r10-01-interrupt-fault", "internal/daemon/session.go", 's.withLatestOrCachedContext("interrupt",', 's.admitScheduledContext("interrupt",', "TestInterruptSurvivesTranscriptFault")

go("r10-02-approval-fault", "internal/daemon/session.go", 's.withLatestOrCachedContext("permission request",', 's.admitScheduledContext("permission request",', "TestRemoteApprovalSurvivesTranscriptReadFault")

go("r10-03-safety-operations", "internal/daemon/schedule.go", 'bound != nil && requiresScheduleAdmission(q.Operation)', 'bound != nil', "TestScheduleSafetyOperationsBypassTranscript")
go("r10-03-read-lock", "internal/daemon/schedule.go", 'return d.scheduleCommandAdmittedContext(ctx, q, origin, scope)', 'if bound != nil {bound.readMu.Lock(); defer bound.readMu.Unlock()}; return d.scheduleCommandAdmittedContext(ctx, q, origin, scope)', "TestScheduleSaveReleasesTranscriptLock")

go("r10-04-input-origin", "internal/daemon/daemon.go", 'd.resumeSessionLifetimeLocked(ctx, s, originSystem, true)', 'd.resumeSessionLifetimeLocked(ctx, s, originLocal, true)', "TestImplicitResumeWaitsForDurableInputOwnership")
go("r10-04-rearm", "internal/daemon/daemon.go", 's.meta.TaskCLIClosed = !pendingInput', 's.meta.TaskCLIClosed = true', "TestImplicitResumeWaitsForDurableInputOwnership")

go("r10-05-web-tag", "internal/server/push.go", 'sessionID + "/" + eventKey + "/" + kind.String()', 'sessionID + "/" + kind.String()', "TestScheduledPushDisplayIdentity", "./internal/server")

go("r10-05-fcm-event", "internal/server/push.go", 'data["event"] = dedupeKey', 'data["event"] = ""', "TestScheduledFCMIncludesEventIdentity", "./internal/server")

go("r11-explicit-running-takeover", "internal/daemon/daemon.go", 'errors.Is(err, errRunning) && humanOrigin(origin)', 'false && errors.Is(err, errRunning) && humanOrigin(origin)', "TestExplicitResumeClaimsRejectedImplicitLifetime")

go("notify-title-resume", "internal/daemon/schedule_events.go", 'in.Title = s.infoLocked().Title', 'in.Title = source.TaskTitle', "TestScheduledNotifyTitleUsesExecutionSource")
go("notify-title-source", "internal/daemon/schedule_events.go", 'switch source.TargetType {', 'switch d.tasks[s.scheduledTask].Spec.Target.Type {', "TestScheduledNotifyTitleUsesExecutionSource")
go("notify-title-task-name", "internal/daemon/schedule_events.go", 'in.Title = source.TaskTitle', 'in.Title = d.tasks[s.scheduledTask].Spec.Title', "TestScheduledNotifyTitleUsesExecutionSource")
go("notify-title-explicit", "internal/daemon/schedule_events.go", 'if in.Title == "" {', 'if true {', "TestScheduledNotifyTitleUsesExecutionSource")
go("notify-title-auto-snapshot", "internal/daemon/schedule.go", 'Status: "dispatching", TargetType: t.Spec.Target.Type, TaskTitle: t.Spec.Title}', 'Status: "dispatching", TargetType: "", TaskTitle: ""}', "TestScheduledNotifyTitleUsesExecutionSource")
go("notify-title-manual-snapshot", "internal/daemon/schedule.go", 'Manual: true, RetryFailed: request.RetryFailed, Status: "dispatching", TargetType: t.Spec.Target.Type, TaskTitle: t.Spec.Title}', 'Manual: true, RetryFailed: request.RetryFailed, Status: "dispatching", TargetType: "", TaskTitle: ""}', "TestScheduledNotifyTitleUsesExecutionSource")
go("notify-title-sealed", "internal/relay/sync.go", 'nc.Title, nc.Text = k.ScheduledNotification.Title, k.ScheduledNotification.Text', 'nc.Title, nc.Text = info.GetTitle(), k.ScheduledNotification.Text', "TestScheduledNotificationTitleSurvivesColdDelivery", "./internal/relay")

clients = [
    ("11-web-attention", "web/src/views/Home.tsx", "return [...remote.sessions.values()]\n    .filter((s) => needsAttention", "return [...remote.sessions.values()].filter(s=>!s.info?.taskOwned)\n    .filter((s) => needsAttention", ["pnpm","--dir","web","exec","vitest","run","src/views/flows.test.tsx","-t","keeps task-owned approvals"], "AssertionError"),
    ("11-web-count", "web/src/views/Projects.tsx", "for (const s of sessions) {", "for (const s of sessions) { if(s.info?.taskOwned)continue;", ["pnpm","--dir","web","exec","vitest","run","src/views/flows.test.tsx","-t","keeps task-owned approvals"], "AssertionError"),
    ("11-android", "android/core/src/main/kotlin/io/github/lewin671/remoteagent/core/Timeline.kt", "val all = state.sessions.values", "val all = state.sessions.values.filter { it.info?.taskOwned != true }", ["./gradlew",":core:test","--tests","*TimelineTest.taskOwnedApprovalsStillNeedAttention"], "taskOwnedApprovalsStillNeedAttention"),
    ("11-ios", "ios/RemoteAgentCore/Sources/RACore/Timeline.swift", "let all = state.sessions.values", "let all = state.sessions.values.filter { $0.info?.taskOwned != true }", ["swift","test","--filter","taskOwnedApprovalsStillNeedAttention"], "failed"),

    ("11-web-project", "web/src/model.ts", "!s.info?.taskOwned || needsAttention(s)", "!s.info?.taskOwned", ["pnpm","--dir","web","exec","vitest","run","src/views/flows.test.tsx","-t","keeps task-owned approvals"], "AssertionError"),
    ("11-android-project", "android/core/src/main/kotlin/io/github/lewin671/remoteagent/core/Model.kt", "it.info?.taskOwned != true || needsAttention(it)", "it.info?.taskOwned != true", ["./gradlew",":core:test","--tests","*TimelineTest.taskOwnedApprovalsStillNeedAttention"], "taskOwnedApprovalsStillNeedAttention"),
    ("11-ios-project", "ios/RemoteAgentCore/Sources/RACore/Projects.swift", "s.info?.taskOwned != true || needsAttention(s, machine: state.machines[s.machineId])", "s.info?.taskOwned != true", ["swift","test","--filter","taskOwnedApprovalsStillNeedAttention"], "Expectation failed"),

]

# The same projections, now exercised after encrypted cold listing and refresh.
for name,file,before,after,cmd,marker in list(clients):
    if name in ("11-web-attention", "11-web-project"):
        clients.append((name.replace("11-", "r2-client-"),file,before,after,
            ["pnpm","--dir","web","exec","vitest","run","src/paging.test.ts","-t","cold listing retains"],marker))
    if name.startswith(("11-android", "r2-client-android")):
        clients.append((name.replace("11-", "r2-client-"),file,before,after,
            ["./gradlew",":core:test","--tests","*DirectoryRefreshTest.coldListingRetainsTaskOwnedApprovalThroughRefresh"],"java.lang.AssertionError"))
    if name.startswith(("11-ios", "r2-client-ios")):
        clients.append((name.replace("11-", "r2-client-"),file,before,after,
            ["swift","test","--filter","coldListingRetainsTaskOwnedApprovalThroughRefresh"],"Expectation failed"))

clients.extend([
 ("r3-android-count", "android/core/src/main/kotlin/io/github/lewin671/remoteagent/core/Timeline.kt", 'it.info?.taskOwned != true && !exited(it)', '!exited(it)', ["./gradlew",":core:test","--tests","*TimelineTest.allProjectCountsExcludeQuietTasksButIncludeApprovals"], "java.lang.AssertionError"),
 ("r3-ios-count", "ios/RemoteAgentCore/Sources/RACore/Timeline.swift", 's.info?.taskOwned != true && !exited(s)', '!exited(s)', ["swift","test","--filter","allProjectCountsExcludeQuietTasksButIncludeApprovals"], "Expectation failed"),
])


clients.extend([
 ("r6-android-foreground", "android/app/src/main/java/io/github/lewin671/remoteagent/push/Notifications.kt", 'foreground && (kind != NotifyKind.NOTIFY_KIND_SCHEDULED || app.visibleSession == session)', 'foreground', ["./gradlew",":app:testDebugUnitTest","--tests","*ForegroundPushTest"], "java.lang.AssertionError"),
 ("r6-android-viewed", "android/app/src/main/java/io/github/lewin671/remoteagent/push/Notifications.kt", 'app.visibleSession == session', 'false', ["./gradlew",":app:testDebugUnitTest","--tests","*ForegroundPushTest"], "java.lang.AssertionError"),
 ("r6-android-background", "android/app/src/main/java/io/github/lewin671/remoteagent/push/Notifications.kt", 'if (foreground && (kind !=', 'if ((kind !=', ["./gradlew",":app:testDebugUnitTest","--tests","*ForegroundPushTest"], "java.lang.AssertionError"),
 ("r6-android-lifecycle", "android/app/src/main/java/io/github/lewin671/remoteagent/push/VisiblePushSession.kt", 'onStopOrDispose { app.visibleSession = null }', 'onStopOrDispose { }', ["./gradlew",":app:testDebugUnitTest","--tests","*VisiblePushSessionTest"], "java.lang.AssertionError"),
 ("r6-web-foreground", "web/public/sw.js", '(!scheduled || viewingSession(w, d.session))', 'true', ["pnpm","--dir","web","exec","vitest","run","src/sw.test.ts"], "AssertionError"),
 ("r6-web-viewed", "web/public/sw.js", 'viewingSession(w, d.session)', 'false', ["pnpm","--dir","web","exec","vitest","run","src/sw.test.ts"], "AssertionError"),
 ("r6-web-focus", "web/public/sw.js", 'w.focused && (!scheduled', '(!scheduled', ["pnpm","--dir","web","exec","vitest","run","src/sw.test.ts"], "AssertionError"),
 ("r6-web-visible", "web/public/sw.js", 'w.visibilityState === "visible" && w.focused', 'w.focused', ["pnpm","--dir","web","exec","vitest","run","src/sw.test.ts"], "AssertionError"),
])

clients.append(("r10-05-android-tag", "android/app/src/main/java/io/github/lewin671/remoteagent/push/Notifications.kt", '"scheduled/$session/$eventKey"', '"scheduled/$session"', ["./gradlew",":app:testDebugUnitTest","--tests","*ScheduledPushIdentityTest"], "java.lang.AssertionError"))

def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument("--clients", action="store_true")
    p.add_argument("--only", default="")
    args=p.parse_args()
    selected=checks+(clients if args.clients else [])
    baselines=set()
    for name,file,before,after,cmd,marker in selected:
        if args.only and args.only not in name: continue
        path=ROOT/file
        original=path.read_text()
        if before not in original: raise SystemExit("Missing mutation target: "+name)
        cwd=ROOT
        if file.startswith("android/"):cwd=ROOT/"android"
        if file.startswith("ios/"):cwd=ROOT/"ios/RemoteAgentCore"
        baseline=(str(cwd),tuple(cmd))
        if baseline not in baselines:
            result=subprocess.run(cmd,cwd=cwd,text=True,stdout=subprocess.PIPE,stderr=subprocess.STDOUT)
            if result.returncode!=0:
                print(result.stdout[-6000:],flush=True)
                raise SystemExit("Original test must pass before mutation: "+name)
            baselines.add(baseline)
        try:
            # 06 intentionally removes both pre-append and replay projections.
            path.write_text(original.replace(before,after) if name=="06-interjection" else original.replace(before,after,1))
            run=subprocess.run(cmd,cwd=cwd,text=True,stdout=subprocess.PIPE,stderr=subprocess.STDOUT)
            if run.returncode==0 or marker not in run.stdout or "[build failed]" in run.stdout or "Compilation error" in run.stdout or "compileTestKotlin FAILED" in run.stdout or "emit-module command failed" in run.stdout:
                print(run.stdout[-6000:],flush=True)
                raise SystemExit("Mutation was not detected by assertions: "+name)
            print("PASS "+name,flush=True)
        finally:path.write_text(original)

if __name__=="__main__":main()
