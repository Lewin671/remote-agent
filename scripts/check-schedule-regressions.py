#!/usr/bin/env python3
"""Temporarily reintroduce reviewed defects and require assertion failures.

Run from this checkout with no concurrent compiler reading these sources.
The original bytes are restored even when a check fails. No build error counts
as a detected mutation. --clients also checks the three attention projections.
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
go("02-manual-date", "internal/daemon/schedule.go", 'if t.Spec.Trigger.Type == "calendar" && !t.RunNow {\n\t\t\t\t\t\tt.LastCalendarDate', 'if t.Spec.Trigger.Type == "calendar" {\n\t\t\t\t\t\tt.LastCalendarDate', "TestCalendarManualRunPreservesAutomaticOccurrence")
go("02-manual-cursor", "internal/daemon/schedule.go", "t.NextRunAt = t.AutomaticNextRunAt", "t.NextRunAt = nil", "TestCalendarManualRunPreservesAutomaticOccurrence")
go("02-manual-count", "internal/daemon/schedule.go", 'if t.Spec.Trigger.Type != "calendar" || !t.RunNow {', 'if true {', "TestCalendarManualRunPreservesAutomaticOccurrence")
go("02-manual-limit-command", "internal/daemon/schedule.go", 't.Spec.Trigger.Type != "calendar" && t.Spec.Trigger.MaxRuns > 0', 't.Spec.Trigger.MaxRuns > 0', "TestCalendarManualRunPreservesAutomaticOccurrence")
go("02-manual-limit-dispatch", "internal/daemon/schedule.go", '!t.RunNow && t.Spec.Trigger.MaxRuns > 0', 't.Spec.Trigger.MaxRuns > 0', "TestCalendarManualRunPreservesAutomaticOccurrence")
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
go("09-uncertain", "internal/daemon/schedule_events.go", "!uncertain[s.meta.ID] && ", "", "TestUncertainRunNeverClosesAutomatically")
go("10-revision", "internal/daemon/schedule.go", "if !bytes.Equal(visible, d.scheduleVisible) {", "if true || !bytes.Equal(visible, d.scheduleVisible) {", "TestScheduleRevisionIgnoresProgressCursors")
go("12-summary", "internal/daemon/schedule.go", "run.FinalSummary = finalRunSummary(msg.Text)", "run.FinalSummary = summary(msg.Text, 200)", "TestRunSummaryUsesWholeFinalAnswer")
go("13-minimum", "internal/daemon/schedule_security.go", "\t{\n\t\ttr := t.Spec.Trigger", "\tif freeze {\n\t\ttr := t.Spec.Trigger", "TestMCPRunAndResumeRecheckMinimumIntervals")
go("14-daemon-quota", "internal/daemon/backend.go", 'if reason != "relay_session_limit" {', 'if true || reason != "relay_session_limit" {', "TestRelaySessionLimitFailsScheduledExecutionDurably")
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
go("r2-03-manual-result", "internal/daemon/schedule.go", 'if manual {', 'if false && manual {', "TestRecurringResumeAndManualFailures")

go("r2-04-skipped-manual", "internal/daemon/schedule.go", 't.RunNow = false\n\t\t\t\t\td.restoreManualSchedule(t, now)', 'd.restoreManualSchedule(t, now)', "TestSkippedManualTrialRestoresAutomaticDispatch")

go("r2-05-current-turn", "internal/daemon/schedule_events.go", 'task.Spec.Target.Type == "new_session" && run != nil', 'run != nil', "TestMCPRestrictionUsesCurrentTurnNotQueue")
go("r2-05-queued-human", "internal/daemon/schedule_events.go", 's.claimOwnershipLocked()\n\t}\n\tif st :=', 's.takeOverLocked()\n\t}\n\tif st :=', "TestMCPRestrictionUsesCurrentTurnNotQueue")

go("r2-06-admission-retry", "internal/daemon/inject.go", '|| retries >= 1', '|| retries < 0', "TestHumanAdmissionBoundsRunningResumeRetry")

go("r2-07-before-identity", "internal/daemon/backend.go", 't.ID == taskID && t.Active.ID == runID ||', 't.ID == taskID && t.Active.ID == runID && false ||', "TestPublicationRefusalBeforeAndAfterAdmission")
go("r2-07-cancel-queue", "internal/daemon/backend.go", '_ = s.cancelInputBy(run.RequestID, originSystem)', '_ = s.cancelInputBy("missing", originSystem)', "TestPublicationRefusalBeforeAndAfterAdmission")
go("r2-07-interrupt", "internal/daemon/backend.go", 's.interruptRunBy(originSystem, run.ID)', 's.interruptRunBy(originSystem, "missing")', "TestPublicationRefusalBeforeAndAfterAdmission")

go("r2-08-minimum-reason", "internal/daemon/schedule.go", 't.Reason = "agent_schedule_interval_below_minimum"', 't.Reason = "agent_schedule_permissions_changed"', "TestMCPDispatchReportsMinimumIntervalReason")

clients = [
    ("11-web-attention", "web/src/views/Home.tsx", "return [...remote.sessions.values()]\n    .filter((s) => needsAttention", "return [...remote.sessions.values()].filter(s=>!s.info?.taskOwned)\n    .filter((s) => needsAttention", ["pnpm","--dir","web","exec","vitest","run","src/views/flows.test.tsx","-t","keeps task-owned approvals"], "AssertionError"),
    ("11-web-count", "web/src/views/Projects.tsx", "for (const s of sessions) {", "for (const s of sessions) { if(s.info?.taskOwned)continue;", ["pnpm","--dir","web","exec","vitest","run","src/views/flows.test.tsx","-t","keeps task-owned approvals"], "AssertionError"),
    ("11-android", "android/core/src/main/kotlin/io/github/lewin671/remoteagent/core/Timeline.kt", "val all = state.sessions.values", "val all = state.sessions.values.filter { it.info?.taskOwned != true }", ["./gradlew",":core:test","--tests","*TimelineTest.taskOwnedApprovalsStillNeedAttention"], "taskOwnedApprovalsStillNeedAttention"),
    ("11-ios", "ios/RemoteAgentCore/Sources/RACore/Timeline.swift", "let all = state.sessions.values", "let all = state.sessions.values.filter { $0.info?.taskOwned != true }", ["swift","test","--filter","taskOwnedApprovalsStillNeedAttention"], "failed"),

    ("11-web-project", "web/src/model.ts", "!s.info?.taskOwned || needsAttention(s)", "!s.info?.taskOwned", ["pnpm","--dir","web","exec","vitest","run","src/views/flows.test.tsx","-t","keeps task-owned approvals"], "AssertionError"),
    ("11-android-project", "android/core/src/main/kotlin/io/github/lewin671/remoteagent/core/Model.kt", "it.info?.taskOwned != true || needsAttention(it)", "it.info?.taskOwned != true", ["./gradlew",":core:test","--tests","*TimelineTest.taskOwnedApprovalsStillNeedAttention"], "taskOwnedApprovalsStillNeedAttention"),
    ("11-ios-project", "ios/RemoteAgentCore/Sources/RACore/Projects.swift", "s.info?.taskOwned != true || needsAttention(s, machine: state.machines[s.machineId])", "s.info?.taskOwned != true", ["swift","test","--filter","taskOwnedApprovalsStillNeedAttention"], "Expectation failed"),

]

def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument("--clients", action="store_true")
    p.add_argument("--only", default="")
    args=p.parse_args()
    selected=checks+(clients if args.clients else [])
    for name,file,before,after,cmd,marker in selected:
        if args.only and args.only not in name: continue
        path=ROOT/file
        original=path.read_text()
        if before not in original: raise SystemExit("Missing mutation target: "+name)
        cwd=ROOT
        if name.startswith("11-android"):cwd=ROOT/"android"
        if name.startswith("11-ios"):cwd=ROOT/"ios/RemoteAgentCore"
        try:
            # 06 intentionally removes both pre-append and replay projections.
            path.write_text(original.replace(before,after) if name=="06-interjection" else original.replace(before,after,1))
            run=subprocess.run(cmd,cwd=cwd,text=True,stdout=subprocess.PIPE,stderr=subprocess.STDOUT)
            if run.returncode==0 or marker not in run.stdout or "[build failed]" in run.stdout:
                print(run.stdout[-6000:],flush=True)
                raise SystemExit("Mutation was not detected by assertions: "+name)
            print("PASS "+name,flush=True)
        finally:path.write_text(original)

if __name__=="__main__":main()
