#!/usr/bin/env python3
"""Temporarily reintroduce reviewed defects and require assertion failures.

Run from this checkout with no concurrent compiler reading these sources.
The original bytes are restored even when a check fails. No build error counts
as a detected mutation. App checks require macOS/Xcode and an existing iPhone 18 Pro. --clients also checks client attention and push visibility.
"""
import argparse
import json
import tempfile
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

go("schedule-client-environment", "internal/daemon/schedule.go", 'shell := t.SourceSession == ""', 'shell := false', "TestScheduledClientEnvironmentSurvivesDaemonRestart")

go("title-update-state", "internal/daemon/schedule.go", 'if onlyScheduleTitleOrNotifyChanged(t.Spec, updateSpec) {', 'if false && onlyScheduleTitleOrNotifyChanged(t.Spec, updateSpec) {', "TestTitleOnlyUpdatePreservesExecutionState")
go("title-update-late-run", "internal/daemon/schedule.go", 't.Spec.Title, t.Spec.Notify = updateSpec.Title, updateSpec.Notify', 'delete(d.lateRuns, t.ID); t.Spec.Title, t.Spec.Notify = updateSpec.Title, updateSpec.Notify', "TestTitleOnlyUpdatePreservesExecutionState")
go("title-update-definition", "internal/daemon/schedule.go", 'return err == nil && bytes.Equal(a, b)', 'return err == nil && (bytes.Equal(a, b) || true)', "TestOtherDefinitionChangesKeepUpdateSemantics")
go("title-update-delegation", "internal/daemon/schedule.go", 'd.restrictMCPTask(schedule.Clone(t), true)', 'd.restrictMCPTask(t, true)', "TestTitleOnlyUpdatePreservesDelegation")

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

# Final client review: assert real task projections, editor requests and signed cache reads.
_feedback_web = ["pnpm","--dir","web","exec","vitest","run","src/schedules.test.ts","src/views/schedules.test.tsx","src/schedule-remote.test.ts"]
_feedback_core = ["./gradlew",":core:test","--tests","*SchedulesTest","--tests","*RemoteTest.scheduledSnapshotsKeepSignedRepliesAndVerifyThemAgainOffline"]
_feedback_app = ["./gradlew",":app:testDebugUnitTest","--tests","*Schedule*Test"]
_feedback_swift = ["swift","test","--filter","SchedulesTests"]
_feedback_ios_app = ["xcodebuild","-quiet","-project","ios/RemoteAgent.xcodeproj","-scheme","RemoteAgent","-destination","platform=iOS Simulator,name=iPhone 18 Pro","-derivedDataPath","/tmp/ra-schedule-client-feedback","-only-testing:RemoteAgentTests/ScheduleStoreTests","-only-testing:RemoteAgentTests/ScheduleAlignmentTests","-only-testing:RemoteAgentTests/ScheduleJumpObserverTests","-collect-test-diagnostics","never","test"]
def feedback(name, file, before, after, command, marker):
    clients.append(("final-"+name,file,before,after,command,marker))
def fw(name,file,before,after): feedback("web-"+name,"web/src/"+file,before,after,_feedback_web,"AssertionError")
def fa(name,file,before,after,ui=False): feedback("android-"+name,"android/"+file,before,after,_feedback_app if ui else _feedback_core,"java.lang.AssertionError|org.junit.ComparisonFailure")
def fi(name,file,before,after,app=False): feedback("ios-"+name,"ios/"+file,before,after,_feedback_ios_app if app else _feedback_swift,"Expectation failed")
fw("submitted","schedules.ts",'(k.case === "inputState" && (k.value.paused || [InputStatus.FAILED, InputStatus.UNCONFIRMED, InputStatus.CANCELED, InputStatus.NEEDS_TERMINAL, InputStatus.HELD_BY_CLI].includes(k.value.status)))','(k.case === "inputState")')
fw("trim","schedules.ts",'.replace(/\\s+/g, " ").trim()', '.replace(/\\s+/g, " ")')
fw("removed-cache","remote.ts",'this.keepValue("schedules:" + id, undefined);','// removed computer retains its cache')
fw("resolve-target","remote.ts",'void Promise.allSettled([...new Set(tasks.filter(t => t.spec.target.type === "resume_session").map(t => t.spec.target.session).filter((sid): sid is string => !!sid && !this.sessions.get(sid)?.info))].map(sid => this.ensureSession(sid)));','// target session fetch omitted')
fw("paused-expiry","schedules.ts",'!["user_paused", "completed_while_paused"].includes(t.reason)','t.reason !== "user_paused"')
fw("stopped-result","schedules.ts",'if (taskGroup(t) === "需要你看一下") return reasonText(t.reason);','// render the completed run instead of the stop cause')
fw("manual-guard","views/Schedules.tsx",'|| !!task.manual_request','')
fw("interval-phase","views/Schedules.tsx",'if (trigger.type === "interval" && seconds === trigger.interval_seconds)','if (false)')
fw("interval-delay","views/Schedules.tsx",'delay_seconds: seconds,','delay_seconds: 0,')
fw("dual-limits","views/Schedules.tsx",'if (end !== initialEnd || kind === "at")','if (endChanged || kind === "at")')
fw("source","views/Schedules.tsx",'sessionId: session?.id,','sessionId: session?.id ?? [...remote.sessions.keys()][0],')
fw("quiet-summary","views/Schedules.tsx",'g.runs[0].final_summary || "没有通知"','g.runs[0].final_summary || ""')
fa("deleted","core/src/main/kotlin/io/github/lewin671/remoteagent/core/Schedules.kt",'it.machine !in removed && scheduleVisible(it) &&','it.machine !in removed &&')
fa("cursor","core/src/main/kotlin/io/github/lewin671/remoteagent/core/Schedules.kt",'(json.long("start_seq").takeIf { it > 0 } ?: json.long("after_seq")) + 1','(json.long("start_seq").takeIf { it > 0 } ?: json.long("after_seq"))')
fa("cursor-fallback","core/src/main/kotlin/io/github/lewin671/remoteagent/core/Schedules.kt",'json.long("start_seq").takeIf { it > 0 } ?: json.long("after_seq")','json.long("start_seq")')
fa("phase","core/src/main/kotlin/io/github/lewin671/remoteagent/core/ScheduleTime.kt",'original?.text("type") == "interval" && original.long("interval_seconds") == seconds','original?.text("type") == "mutated" && original.long("interval_seconds") == seconds')
fa("dual-limits","core/src/main/kotlin/io/github/lewin671/remoteagent/core/ScheduleTime.kt",'before != null && end == before.end','before != null && end == before.end && count == before.count')
fa("signature","core/src/main/kotlin/io/github/lewin671/remoteagent/core/DeviceSecurity.kt",'verifyEd25519(m.ed, originBytes(h, sha256(w.formed.header), commit), w.formed.origin)', 'true')
fa("paused-expiry","core/src/main/kotlin/io/github/lewin671/remoteagent/core/Schedules.kt",'setOf("", "user_paused", "completed_while_paused")','setOf("", "user_paused")')
fa("stopped-result","core/src/main/kotlin/io/github/lewin671/remoteagent/core/Schedules.kt",'if (taskGroup(t) == TaskGroup.ATTENTION) return scheduleReason(t.reason)','// render the last successful run instead of the stop cause')
fa("revision","app/src/main/java/io/github/lewin671/remoteagent/ui/Schedules.kt",'if (spec != null) editBase ?: task else task','task',True)
fa("spec-snapshot","app/src/main/java/io/github/lewin671/remoteagent/ui/Schedules.kt",'val base = editBase ?: task','val base = task',True)
fa("delete-back","app/src/main/java/io/github/lewin671/remoteagent/ui/Schedules.kt",'if (op == "cancel") nav.back()','if (false) nav.back()',True)
fa("run-guard","core/src/main/kotlin/io/github/lewin671/remoteagent/core/Schedules.kt",'&& t.json["manual_request"] !is JsonObject','',False)
fi("wall-time","RemoteAgentCore/Sources/RACore/ScheduleTiming.swift",'public var timezone: String','public var timezone: String { didSet { var old = Calendar(identifier:.gregorian); old.timeZone = TimeZone(identifier:oldValue) ?? .current; if let d = old.date(from:dateParts) { dateParts = Self.parts(d,zone:calendar.timeZone) } } }')
fi("phase","RemoteAgentCore/Sources/RACore/ScheduleTiming.swift",'if let original, original.type == "interval", original.intervalSeconds == seconds { t = original }','if false { t = ScheduleTrigger(type:"interval") }')
fi("paused-expiry","RemoteAgentCore/Sources/RACore/Schedules.swift",'["", "user_paused", "completed_while_paused"].contains(reason ?? "")','["", "user_paused"].contains(reason ?? "")')
fi("stopped-result","RemoteAgentCore/Sources/RACore/Schedules.swift",'if group == .attention { return scheduleReason(reason) }','// render the last successful run instead of the stop cause')
fi("cursor-fallback","RemoteAgentCore/Sources/RACore/Schedules.swift",'(startSeq == 0 ? afterSeq : startSeq) + 1','startSeq + 1')
fi("quiet-summary","RemoteAgentCore/Sources/RACore/Schedules.swift",'(finalSummary ?? "").isEmpty ? "没有通知" : finalSummary!','finalSummary ?? ""')
fi("plan","RemoteAgent/UI/Schedules/NewScheduleScreen.swift",'guard creationIssue(session: session, state: state) == nil else { return nil }','// silently map plan to ask',True)
fi("revision","RemoteAgent/UI/Schedules/ScheduleDetailScreen.swift",'spec: spec, revision: task.revision)','spec: spec, revision: 0)',True)
fi("serialize","RemoteAgent/UI/Schedules/ScheduleStore.swift",'while let job = jobs[id] { await job.value }','// refresh does not wait for the revision-triggered request',True)
fi("jump","RemoteAgent/UI/Session/ChatPane.swift",'Input(rowIDs: rowIDs, loaded: loaded, jump: jump)','Input(rowIDs: rowIDs, loaded: false, jump: jump)',True)
fi("user-pause-banner","RemoteAgent/UI/Schedules/ScheduleEditAccess.swift",'showsReason = task.group == .attention','showsReason = task.group == .attention || task.group == .paused',True)

# Quiet Xcode output names a failed Swift Testing case without its assertion text.
# Check the structured result, and require the intended case's expectation failure.
_IOS_ASSERTIONS = {
    "final-ios-plan": "ScheduleStoreTests/planDefaultsRemainPlanAndCannotCreateAnExecutionTask()",
    "final-ios-revision": "ScheduleStoreTests/anEditorKeepsItsOpeningRevisionAndCompleteSpecAcrossARefresh()",
    "final-ios-serialize": "ScheduleStoreTests/concurrentManualAndRevisionReadsShareOneMachineSlot()",
    "final-ios-jump": "ScheduleJumpObserverTests/retriesWhenLoadingFinishesWithoutChangingAnyRowID()",
    "final-ios-user-pause-banner": "ScheduleAlignmentTests/onlySystemStopsAndExpiredPausesShowAReasonBanner()",
}

def run_check(cmd, cwd, name):
    if cmd[0] != "xcodebuild":
        return subprocess.run(cmd, cwd=cwd, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    with tempfile.TemporaryDirectory(prefix="ra-schedule-mutation-") as directory:
        bundle = Path(directory)/"result.xcresult"
        run = subprocess.run([cmd[0], "-resultBundlePath", str(bundle), *cmd[1:]],
                             cwd=cwd, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        run.assertion_detected = False
        if not (bundle/"Info.plist").is_file(): return run
        report = subprocess.run(["xcrun", "xcresulttool", "get", "test-results", "summary", "--path", str(bundle)],
                                text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        if report.returncode != 0:
            run.stdout += "\nUnable to read structured test result: " + report.stdout
            if run.returncode == 0: run.returncode = 1
            return run
        summary = json.loads(report.stdout)
        if run.returncode == 0 and (summary.get("result") != "Passed" or summary.get("totalTestCount", 0) < 1):
            run.returncode = 1; run.stdout += "\nNo successful selected test execution"
        failures = summary.get("testFailures", [])
        expected = _IOS_ASSERTIONS[name]
        assertion = {
            "final-ios-jump": "Issue recorded: never happened: loaded history retries jump",
            "final-ios-r2-conflict-dismiss": "Issue recorded: never happened: conflict dismisses stale editor",
            "final-ios-r2-target-read-slot-ios": "Issue recorded: never happened: target does not block refresh",
        }.get(name, "Expectation failed")
        run.assertion_detected = any(f.get("testIdentifierString") == expected and
                                    assertion in f.get("failureText", "") for f in failures)
        run.stdout += "\n" + "\n".join(f.get("testIdentifierString", "") + ": " + f.get("failureText", "") for f in failures)
        return run

# Final re-review: failures must be assertions in real derivations or rendered editors.
fw("r2-machine-defaults", "views/Schedules.tsx", 'useNewSession(remote, cfg.cwd, () => undefined, machineId)', 'useNewSession(remote, cfg.cwd, () => undefined)')
fw("r2-dual-end", "views/Schedules.tsx", 'trigger.max_runs ? "count" : trigger.until ? "date" : "none"', 'trigger.until ? "date" : trigger.max_runs ? "count" : "none"')
fw("r2-target-placeholder", "views/Schedules.tsx", '(targetSession?.info ? titleOf(targetSession) : "对话")', '(targetSession?.info ? titleOf(targetSession) : "未知项目")')
fw("r2-conflict-close", "views/Schedules.tsx", 'if (errorText(e).includes("任务刚被修改"))', 'if (false)')
fw("r2-target-read-slot", "remote.ts", 'void Promise.allSettled([...new Set(tasks.filter', 'await Promise.allSettled([...new Set(tasks.filter')
fa("r2-editor-zone", "core/src/main/kotlin/io/github/lewin671/remoteagent/core/Schedules.kt", 'else ZoneId.systemDefault()', 'else ZoneId.of("Z")')
fa("r2-lock-machine", "app/src/main/java/io/github/lewin671/remoteagent/ui/Project.kt", 'if (lockMachine) null else ({ page = "machine" })', '({ page = "machine" })', True)
fa("r2-machine-defaults", "app/src/main/java/io/github/lewin671/remoteagent/ui/SchedulePresentation.kt", 'saved.prefs.settingsList.filter { it.machineId == machine }', 'saved.prefs.settingsList', True)
fa("r2-resolve-mode", "app/src/main/java/io/github/lewin671/remoteagent/ui/SchedulePresentation.kt", 'cfg.mode ?: defaultModeOf(state.machines[cfg.machineId], cfg.agent).mode', 'cfg.mode', True)
fa("r2-placeholder", "app/src/main/java/io/github/lewin671/remoteagent/ui/SchedulePresentation.kt", '?.let(::titleOf) ?: "对话"', '?.let(::titleOf) ?: "当前对话"', True)
fa("r2-rename-close", "app/src/main/java/io/github/lewin671/remoteagent/ui/Schedules.kt", 'if (e is ScheduleConflictException) { edit = ""; editBase = null; error = errorText(e) }', 'if (e is ScheduleConflictException) { error = errorText(e) }', True)
fi("r2-cutoff-instant", "RemoteAgentCore/Sources/RACore/ScheduleTiming.swift", 'if original.until != nil && untilEdited {', 'if original.until != nil && (untilEdited || timezone != original.timezone) {')
fi("r2-machine-defaults", "RemoteAgent/UI/Schedules/ScheduleTargetSheet.swift", 'prefs.prefs.settings.filter { $0.machineID == machine }', 'prefs.prefs.settings', True)
fi("r2-placeholder", "RemoteAgent/UI/Schedules/ScheduleTargetSheet.swift", 'session.info != nil else { return "对话" }', 'session.info != nil else { return "当前对话" }', True)
fi("r2-conflict-dismiss", "RemoteAgent/UI/Schedules/ScheduleDetailScreen.swift", '.onChange(of: mutation.conflicts) { _, _ in dismiss() }', '.onChange(of: mutation.conflicts) { _, _ in }', True)
fi("r2-target-read-slot-ios", "RemoteAgent/UI/Schedules/ScheduleStore.swift", 'resolutions[id, default: [:]][sid] = Task {', 'await calls.resolve(sid); resolutions[id, default: [:]][sid] = Task {', True)
fw("r2-project-machine", "views/Schedules.tsx", 'fixedMachine={machineId}', '')
fw("r2-clear-rejected-attempt", "remote.ts", 'attempt.abandon(); // definite schedule revision conflict', '// retain rejected revision')
fi("r2-unknown-permission", "RemoteAgent/UI/Schedules/NewScheduleScreen.swift", 'if mode == nil {', 'if false {', True)
fi("r2-daemon-conflict", "RemoteAgent/UI/Schedules/ScheduleStore.swift", 'command.fromDaemon, command.code == "aborted"', 'command.code == "aborted"', True)
feedback("r2-design", "docs/design/client-v52.html", '<div class="mc">中继上的会话数已满</div>', '<div class="mc">今天 08:00 失败：中继上的会话数已满</div>', ["pnpm","--dir","web","exec","vitest","run","src/views/schedule-design.test.ts"], "AssertionError")
fw("r2-task-settings-only", "views/NewSession.tsx", 'const taskEditing = scheduled && lockMachine;', 'const taskEditing = false;')
fa("r2-task-settings-only", "app/src/main/java/io/github/lewin671/remoteagent/ui/Project.kt", 'if (!lockMachine) {', 'if (true) {', True)
_IOS_ASSERTIONS.update({
 "final-ios-r2-unknown-permission": "ScheduleStoreTests/creationResolvesKnownDefaultsAndRequiresAnExplicitChoiceWhenUnknown()",
 "final-ios-r2-daemon-conflict": "ScheduleAlignmentTests/conflictDismissalObservesTheMutationSignal()",
 "final-ios-r2-target-read-slot-ios": "ScheduleStoreTests/targetResolutionDoesNotHoldTheListOrRefreshSlot()",
 "final-ios-r2-machine-defaults": "ScheduleAlignmentTests/taskEditorIgnoresOtherComputerDefaults()",
 "final-ios-r2-placeholder": "ScheduleAlignmentTests/destinationUsesSharedPlaceholderUntilMetadataLoads()",
 "final-ios-r2-conflict-dismiss": "ScheduleAlignmentTests/conflictDismissalObservesTheMutationSignal()",
})

fa("r3-cancelled-recovery", "app/src/main/java/io/github/lewin671/remoteagent/ui/Schedules.kt", 'if (systemCancelled) "update" else if (onceFailed) "run" else "resume"', 'if (onceFailed) "run" else "resume"', ui=True)
fa("r3-cancelled-delete", "app/src/main/java/io/github/lewin671/remoteagent/ui/Schedules.kt", 'color = pal.red, enabled = enabled)', 'color = pal.red, enabled = enabled && task.status != "cancelled")', ui=True)

fa("r3-selected-cli", "app/src/main/java/io/github/lewin671/remoteagent/ui/Schedules.kt", 'it.agent == op.agent', 'it.agent == cfg.agent', ui=True)
fa("r3-return-cli", "app/src/main/java/io/github/lewin671/remoteagent/ui/Project.kt", 'agent = cur, sets = listOf(c.agentSettings())', 'agent = agent, sets = edited.values.map { it.agentSettings() }', ui=True)

fw("r3-integral-seconds", "views/Schedules.tsx", 'const seconds = trigger.type === "interval" && minutes === String(trigger.interval_seconds! / 60)\n          ? trigger.interval_seconds!\n          : Math.abs(rawSeconds - rounded) <= Number.EPSILON * Math.max(1, Math.abs(rawSeconds)) * 2 ? rounded : rawSeconds;', 'const seconds = rawSeconds;')
fw("r3-visible-boundaries", "schedules.ts", '          "questionAnswered",\n          "turnContext",\n          "turnEnded",\n', '')
fa("r3-visible-boundaries", "app/src/main/java/io/github/lewin671/remoteagent/ui/ScheduledChat.kt", 'ended.single().seq, eventTime(first)', 'body.last().seq, eventTime(first)', ui=True)
fi("r3-visible-boundaries", "RemoteAgentCore/Sources/RACore/ScheduledChat.swift", '.questionAsked, .questionAnswered, .planProposed, .imageGenerated, .sessionForked, .settingsChanged, .turnContext, .turnEnded:', '.questionAsked, .planProposed, .imageGenerated, .sessionForked:')
fw("r3-cutoff-display", "views/Schedules.tsx", 'setUntil(localInput(untilInstant, nextZone));', 'void untilInstant;')
fi("r3-cutoff-display", "RemoteAgentCore/Sources/RACore/ScheduleTiming.swift", 'public var timezone: String', 'public var timezone: String { didSet { var old = Calendar(identifier:.gregorian); old.timeZone = TimeZone(identifier:oldValue) ?? .current; let parts = old.dateComponents([.year,.month,.day,.hour,.minute,.second],from:until); until = calendar.date(from:parts) ?? until } }')

fa("r3-cutoff-display", "core/src/main/kotlin/io/github/lewin671/remoteagent/core/ScheduleTime.kt", 'until = local ?: until, deadline = if (deadline.isNotEmpty()) deadline else instant?.toString().orEmpty()', 'until = until, deadline = ""')
fa("r3-cutoff-zone-ui", "app/src/main/java/io/github/lewin671/remoteagent/ui/Schedules.kt", 'value.inZone(it.trim())', 'value.copy(zone = it.trim(), deadline = "")', ui=True)

go('last-review-input-dedup', 'internal/daemon/inject.go', 'if requestID != "" && s.queue.seen(requestID) {', 'if false && requestID != "" && s.queue.seen(requestID) {', 'TestAcceptedInputRetryHasNoLifecycleSideEffects')
go('last-review-input-ready-order', 'internal/daemon/inject.go', '\tif requestID != "" && s.queue.seen(requestID) {\n\t\tif !s.meta.Exited && !s.closing {\n\t\t\ts.poke()\n\t\t}\n\t\treturn 0, nil\n\t}\n\tif s.meta.Exited || s.closing {\n\t\treturn 0, errExited\n\t}', '\tif s.meta.Exited || s.closing { return 0, errExited }\n\tif requestID != "" && s.queue.seen(requestID) { s.poke(); return 0, nil }', 'TestAcceptedInputRetryHasNoLifecycleSideEffects')
_last_swift = ["swift","test","--filter","ScheduleRevocationTests"]
def last_swift(name,file,before,after): feedback("ios-last-review-"+name,"ios/RemoteAgentCore/Sources/RACore/"+file,before,after,_last_swift,"Expectation failed")
fi('last-review-members', 'RemoteAgent/UI/Schedules/ScheduleStore.swift', 'state.currentMachines.contains($0.id) && ', '', True)
last_swift('published-members', 'Remote.swift', 's.currentMachines = Set(trustedMembers.values.filter { $0.kind == 1 }.map(\\.id))', 's.currentMachines = Set(machines.keys)')
last_swift('cache-members', 'RemoteSchedules.swift', '        guard currentScheduleMember(machineID) else { await cache?.removeBlob(name: "schedules/" + machineID); return nil }\n', '')
last_swift('cache-live-prune', 'RemoteSecurityDirectory.swift', '        for id in archiveMachines where trustedMembers[id]?.kind != 1 { await cache?.removeBlob(name: "schedules/" + id) }', '// retain archive task snapshot')
last_swift('cache-cold-prune', 'Remote.swift', '            for id in archiveMachines where trustedMembers[id]?.kind != 1 { await cache.removeBlob(name: "schedules/" + id) }', '// retain archive task snapshot')
last_swift('cache-write', 'RemoteSchedules.swift', 'if !currentScheduleMember(machine) { await cache?.removeBlob(name: "schedules/" + machine) }', '// retain late snapshot')
_last_cipher_app = [x for x in _feedback_ios_app if not x.startswith("-only-testing:")] + ["-only-testing:RemoteAgentTests/CipherStoreTests"]
feedback("ios-last-review-delete-blob","ios/RemoteAgent/Data/CipherStore.swift",'await write { db in try db.execute(sql: "DELETE FROM blobs WHERE name = ?", arguments: [name]) }',"// do not delete",_last_cipher_app,"Expectation failed")
fw('last-review-cache-read', 'remote.ts', 'if (!this.machines.has(id) || !this.supportsSchedules(id) || this.removed) return;', '// accept a revoked cached result')
fw('last-review-cache-write', 'remote.ts', 'if (!this.machines.has(id) || !this.supportsSchedules(id) || this.removed)\n      await this.keepValue("schedules:" + id, undefined);', '// accept a late cached result')
feedback("android-last-review-cache-write","android/core/src/main/kotlin/io/github/lewin671/remoteagent/core/Remote.kt",'if (!currentScheduleMember(p.machineId)) cache?.removeBlob("schedule:${p.machineId}")',"// accept late snapshot",["./gradlew",":core:test","--tests","*RemoteTest.scheduledSnapshotCannotReturnAfterRevocationDuringCacheWrite"],"java.lang.AssertionError")
fa('last-review-cache-error', 'core/src/main/kotlin/io/github/lewin671/remoteagent/core/Schedules.kt', '} catch (_: Exception) { if (id !in removed) set(id)', '} catch (_: Exception) { set(id)')
fa('last-review-list-error', 'core/src/main/kotlin/io/github/lewin671/remoteagent/core/Schedules.kt', '} catch (e: Exception) { if (id !in removed) set(id)', '} catch (e: Exception) { set(id)')
fw('last-review-failed-tools', 'schedules.ts', '(k.case === "toolCall" && (!toolOk(k.value) || isBackground(k.value))) ||', '')
fa('last-review-failed-tools', 'app/src/main/java/io/github/lewin671/remoteagent/ui/ScheduledChat.kt', 'it.hasToolCall() && (!toolOk(it.toolCall) || io.github.lewin671.remoteagent.core.wentToBackground(it.toolCall)) ||', '', True)
fi('last-review-failed-tools', 'RemoteAgentCore/Sources/RACore/ScheduledChat.swift', 'if !toolOk(t) || wentToBackground(t) { round?.safe = false }', 'if false { round?.safe = false }')
fw('last-review-pending-tools', 'schedules.ts', '(k.case === "toolStarted" && typeof derived.toolEnds.get(e.seq) !== "bigint") ||', '')
fa('last-review-pending-tools', 'app/src/main/java/io/github/lewin671/remoteagent/ui/ScheduledChat.kt', 'it.hasToolStarted() && (derived.completedAt[it.seq] ?: Long.MAX_VALUE) > ended.single().seq ||', '', True)
fi('last-review-pending-tools', 'RemoteAgentCore/Sources/RACore/ScheduledChat.swift', 'if derived.completedAt[e.seq] == nil { round?.safe = false }', 'if false { round?.safe = false }')
fw('last-review-terminal-row', 'schedules.ts', '(k.case === "turnContext" && derived.terminalChanges.has(e.seq)) ||', '')
fa('last-review-terminal-row', 'app/src/main/java/io/github/lewin671/remoteagent/ui/ScheduledChat.kt', 'it.hasTurnContext() && derived.terminalChanges.containsKey(it.seq) ||', '', True)
fi('last-review-terminal-row', 'RemoteAgentCore/Sources/RACore/ScheduledChat.swift', 'case .turnContext: if derived.terminalChanges[e.seq] != nil { round?.safe = false }', 'case .turnContext: break')
fw('last-review-visible-row', 'schedules.ts', '          "questionAnswered",\n          "settingsChanged",\n          "imageGenerated",\n', '')
fa('last-review-visible-row', 'app/src/main/java/io/github/lewin671/remoteagent/ui/ScheduledChat.kt', 'it.hasSettingsChanged() || it.hasQuestionAnswered() || it.hasImageGenerated() || it.hasSessionForked() ||', '', True)
fi('last-review-visible-row', 'RemoteAgentCore/Sources/RACore/ScheduledChat.swift', 'case .scheduledNotification, .approvalRequested, .questionAsked, .questionAnswered, .settingsChanged, .planProposed, .imageGenerated, .sessionForked:', 'case .scheduledNotification, .approvalRequested, .questionAsked, .planProposed, .imageGenerated, .sessionForked:')
_IOS_ASSERTIONS.update({"final-ios-last-review-members":"ScheduleStoreTests/revokedArchiveDropsTasksBadgeAndPendingRead()","final-ios-last-review-delete-blob":"CipherStoreTests/blobsAreKeptByName()"})

fw("last-review-late-tool", "schedules.ts", 'toolsThrough <= e.seq &&', 'true &&')
fa("last-review-late-tool", "app/src/main/java/io/github/lewin671/remoteagent/ui/ScheduledChat.kt", '(derived.completedAt[it.seq] ?: Long.MAX_VALUE) > ended.single().seq', 'derived.completedAt[it.seq] == null', True)

fi("last-review-late-tool", "RemoteAgentCore/Sources/RACore/ScheduledChat.swift", 'r.toolsThrough <= e.seq, ', '')

feedback("android-last-review-cache-read","android/core/src/main/kotlin/io/github/lewin671/remoteagent/core/Remote.kt",'require(reply.hasSchedule() && reply.errorCode.isEmpty())\n        if (!currentScheduleMember(machine)) { cache?.removeBlob("schedule:$machine"); return@withContext null }','require(reply.hasSchedule() && reply.errorCode.isEmpty())',["./gradlew",":core:test","--tests","*RemoteTest.scheduledSnapshotCannotReturnAfterRevocationDuringCacheRead"],"java.lang.AssertionError")

feedback("android-creation-machine-config", "android/app/src/main/java/io/github/lewin671/remoteagent/ui/Project.kt", 'if (lockMachine || scheduling) PrefsOp.SetSettings', 'if (lockMachine) PrefsOp.SetSettings', ["./gradlew", ":app:testDebugUnitTest", "--tests", "*ScheduleCreationMachineTest.completingAfterSwitchAndCliSelectionReturnsExactlyTheDisplayedConfig"], "java.lang.AssertionError")
fi("creation-machine-config", "RemoteAgent/UI/Schedules/ScheduleConfigSheet.swift", 'config = switchMachine(c, machineId: id, agents: machineAgents(state.machines[id]))', 'config = projectStart(state, key: c.cwd, pick: c.agent, machine: id)?.config ?? NewConfig(machineId: id, cwd: c.cwd, agent: c.agent)', True)
fw("creation-machine-config", "model.ts", 'agent: agents.includes(cfg.agent) ? cfg.agent : agents[0],\n    model: "",\n    effort: "",\n    mode: ApprovalMode.UNSPECIFIED,', 'agent: agents.includes(cfg.agent) ? cfg.agent : agents[0],\n    model: cfg.model,\n    effort: cfg.effort,\n    mode: cfg.mode,')
_IOS_ASSERTIONS["final-ios-creation-machine-config"] = "ScheduleStoreTests/switchingCreationComputerUsesOnlyItsDisplayedDefaults(agents:)"

# DST wall-time review: missing local times must fail, repeated ones choose first.
fa("dst-once-gap", "core/src/main/kotlin/io/github/lewin671/remoteagent/core/ScheduleTime.kt", 'val timestamp = absolute(LocalDate.parse(date).atTime(clock), tz)', 'val timestamp = LocalDate.parse(date).atTime(clock).atZone(tz).toInstant()')
fa("dst-cutoff-gap", "core/src/main/kotlin/io/github/lewin671/remoteagent/core/ScheduleTime.kt", 'private fun cutoff(tz: ZoneId): Instant = absolute(LocalDate.parse(until).atTime(23, 59, 59), tz)', 'private fun cutoff(tz: ZoneId): Instant = LocalDate.parse(until).plusDays(1).atStartOfDay(tz).toInstant().minusSeconds(1)')
fa("dst-first-occurrence", "core/src/main/kotlin/io/github/lewin671/remoteagent/core/ScheduleTime.kt", 'local.toInstant(offsets.first())', 'local.toInstant(offsets.last())')
fw("dst-first-occurrence", "schedules.ts", 'Math.min(...candidates)', 'Math.max(...candidates)')
fi("dst-cutoff-first-occurrence", "RemoteAgentCore/Sources/RACore/ScheduleTiming.swift", '            until = calendar.date(from: Self.parts(until, zone: calendar.timeZone)) ?? until\n', '')

fi("dst-default-cutoff-first-occurrence", "RemoteAgentCore/Sources/RACore/ScheduleTiming.swift", '        if trigger?.until == nil { until = calendar.date(from: Self.parts(until, zone: calendar.timeZone)) ?? until }\n', '')

# Task-level completion notices and user takeover explanations.
go("schedule-notify-fallback", "internal/daemon/schedule_events.go", "func (d *Daemon) finishScheduledNotification(taskID string, run *schedule.Run) bool {", "func (d *Daemon) finishScheduledNotification(taskID string, run *schedule.Run) bool { return false;", "TestScheduledFinishNotification")
go("schedule-notify-opt-out", "internal/daemon/schedule_events.go", 'if !run.NotifyOnFinish || run.Notified || run.NotificationSource == "undeliverable" || run.EndedAt == nil {', 'if run.Notified || run.NotificationSource == "undeliverable" || run.EndedAt == nil {', "TestScheduledFinishNotification")
go("schedule-notify-failure-body", "internal/daemon/schedule_events.go", 'text = "定时任务运行失败：" + run.Reason', 'text = "定时任务运行完成。"', "TestScheduledFinishNotification")
go("schedule-notify-event-authority", "internal/daemon/schedule_events.go", "if run.Notified {\n\t\treturn run.NotificationSeq != before", "if false && run.Notified {\n\t\treturn run.NotificationSeq != before", "TestScheduledFinishRetryUsesDurableEventAsAuthority")
go("schedule-notify-policy", "internal/daemon/schedule.go", 'NotifyOnFinish: t.Spec.Notify != "agent_only"', 'NotifyOnFinish: false', "TestScheduledNotificationPolicySnapshot")
go("schedule-notify-takeover-list", "internal/daemon/schedule.go", 'if t.Takeover != nil && t.Takeover.Session == scope {', 'if false && t.Takeover != nil && t.Takeover.Session == scope {', "TestMCPTakenOverTasksVisibleButReadOnly")
go("schedule-notify-takeover-error", "internal/daemon/schedule_security.go", 'if t.Takeover != nil && t.Takeover.Session == scope {', 'if false && t.Takeover != nil && t.Takeover.Session == scope {', "TestMCPTakenOverTasksVisibleButReadOnly")

go("leader-notify-snapshot-consumption", "internal/daemon/schedule_events.go", "func (d *Daemon) finishScheduledNotification(taskID string, run *schedule.Run) bool {", 'func (d *Daemon) finishScheduledNotification(taskID string, run *schedule.Run) bool { run.NotifyOnFinish = d.tasks[taskID].Spec.Notify != "agent_only";', "TestScheduledNotificationPolicySnapshot")

go("leader-notify-light-update", "internal/daemon/schedule.go", 'if onlyScheduleTitleOrNotifyChanged(t.Spec, updateSpec) {', 'if false && onlyScheduleTitleOrNotifyChanged(t.Spec, updateSpec) {', "TestTitleNotifyOnlyUpdatePreservesPlanAndTakesOver")
go("leader-notify-undeliverable", "internal/daemon/schedule_events.go", 'run.NotificationSource = "undeliverable"', 'run.NotificationSource = ""', "TestScheduledFinishUndeliverableStopsRetrying")
go("leader-notify-relay-route", "internal/daemon/schedule_events.go", 'if sid == "" || run.Reason == "relay_session_limit" {', 'if sid == "" {', "TestScheduledFinishRelayRefusalUsesOnlySource")

def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument("--clients", action="store_true")
    p.add_argument("--only", default="")
    args=p.parse_args()
    selected=checks+(clients if args.clients else [])
    # Check all locators before paying for any test baseline or changing source.
    for name, file, before, _, _, _ in selected:
        if args.only and args.only not in name: continue
        if before not in (ROOT/file).read_text(): raise SystemExit("Missing mutation target: "+name)
    baselines=set()
    for name,file,before,after,cmd,marker in selected:
        if args.only and args.only not in name: continue
        path=ROOT/file
        original=path.read_text()
        if before not in original: raise SystemExit("Missing mutation target: "+name)
        cwd=ROOT
        if file.startswith("android/"):cwd=ROOT/"android"
        if file.startswith("ios/") and cmd[0] == "swift":cwd=ROOT/"ios/RemoteAgentCore"
        baseline=(str(cwd),tuple(cmd))
        if baseline not in baselines:
            result=run_check(cmd,cwd,name)
            if result.returncode!=0:
                print(result.stdout[-6000:],flush=True)
                raise SystemExit("Original test must pass before mutation: "+name)
            baselines.add(baseline)
        try:
            # These checks remove all projections of one invariant, including both sides of an await.
            path.write_text(original.replace(before,after) if name in {"06-interjection", "final-ios-last-review-cache-members"} else original.replace(before,after,1))
            run=run_check(cmd,cwd,name)
            if run.returncode==0 or not getattr(run, "assertion_detected", any(part in run.stdout for part in marker.split("|"))) or "[build failed]" in run.stdout or "Compilation error" in run.stdout or "compileTestKotlin FAILED" in run.stdout or "emit-module command failed" in run.stdout:
                print(run.stdout[-6000:],flush=True)
                raise SystemExit("Mutation was not detected by assertions: "+name)
            print("PASS "+name,flush=True)
        finally:path.write_text(original)

if __name__=="__main__":main()
