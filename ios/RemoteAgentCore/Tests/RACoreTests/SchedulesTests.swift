import Foundation
import Testing
import RAProto
@testable import RACore

@Suite struct SchedulesTests {
    @Test func compactReasonsUseCodesAndLeaveUnknownReasonsIntact() {
        #expect(scheduleReason("execution_failed", short: true) == L10n.scheduleExecutionFailed())
        #expect(scheduleReason("execution_failed") == L10n.scheduleExecutionFailedTheTaskStopped())
        #expect(scheduleReason("raw，任务已停下", short: true) == "电脑返回的原因：raw，任务已停下")
        let failure = CommandError.unknownBecause("raw reason")
        #expect(failure.unknown)
        #expect(failure.outcomeDetail == "raw reason")
        #expect(failure.message == "不确定电脑有没有执行：raw reason")
    }

    let now = scheduleDate("2026-10-24T17:00:00Z")!
    let zone = TimeZone(identifier: "America/Los_Angeles")!
    func task(_ status: String = "scheduled", reason: String? = nil) -> ScheduleTask {
        var task = ScheduleTask(id: "task", spec: ScheduleSpec(title: "检查", message: "检查回复。没有变化就简短说一句", trigger: ScheduleTrigger(type: "calendar"), target: ScheduleTarget(type: "new_session")), status: status)
        task.reason = reason; return task
    }
    @Test func messageLimitCountsUTF8BytesAndKeepsOriginalWhitespace() {
        #expect(!scheduleMessageValid(" \n\t"))
        #expect(scheduleMessageValid(String(repeating: "a", count: 32768)))
        #expect(!scheduleMessageValid(String(repeating: "a", count: 32769)))
        #expect(scheduleMessageValid(String(repeating: "中", count: 10922)))
        #expect(!scheduleMessageValid(String(repeating: "中", count: 10923)))
    }
    @Test func switchingAllTimingKindsRetainsSelectionsAndRejectsEmptyWeek() throws {
        var edit = ScheduleTiming(now: now, zone: zone)
        let chosen = now.addingTimeInterval(7200)
        edit.date = chosen; edit.until = now.addingTimeInterval(86400 * 5)
        edit.end = .count; edit.count = 7; edit.weekdays = [2, 4]
        for kind in [ScheduleTiming.Kind.once, .weekly, .interval, .daily, .weekly] {
            edit.kind = kind
            #expect(edit.date == chosen && edit.count == 7 && edit.end == .count && edit.weekdays == [2, 4])
        }
        edit.weekdays = []
        #expect(throws: UserError(L10nMessage.scheduleSelectAtLeastOneDay())) { try edit.trigger(now: now) }
        edit.weekdays = [2, 4]
        #expect(try edit.trigger(now: now).weekdays == [2, 4])
    }
    @Test func missingTargetIsOnlyTheOriginalConversationTask() {
        var t = task("failed", reason: "execution_session_missing")
        #expect(!t.missingTarget)
        t.spec.target.type = "resume_session"; #expect(t.missingTarget)
        t.reason = "execution_failed"; #expect(!t.missingTarget)
    }
    @Test func queuedScheduledOriginKeepsItsFirstTimestampOnUpdates() {
        var queued = PBEvent(); queued.seq = 1; queued.time.seconds = 1800000000
        queued.origin.scheduledRunID = "run"; queued.inputState.requestID = "request"
        queued.inputState.text = "检查"; queued.inputState.status = .queued; queued.inputState.mode = .queue
        var update = queued; update.seq = 2; update.time.seconds += 60
        let pending = pendingInputs(derive([queued, update]))
        #expect(pending.count == 1 && pending[0].time == 1800000000000)
        #expect(pending[0].origin?.scheduledRunID == "run")
    }
    @Test func repeatedWallTimesChooseFirstForOnceAndEditedCutoff() throws {
        for (name, wall, first, second) in [
            ("America/Los_Angeles", "2027-11-07T01:30:00Z", "2027-11-07T08:30:00Z", "2027-11-07T09:30:00Z"),
            ("Europe/Berlin", "2027-10-31T02:30:00Z", "2027-10-31T00:30:00Z", "2027-10-31T01:30:00Z")
        ] {
            var at = ScheduleTrigger(type: "at"); at.at = wall; at.timezone = "UTC"
            var once = ScheduleTiming(trigger: at, now: now, zone: zone); once.timezone = name
            #expect(try once.trigger(now: now).at == first)
            var cutoff = ScheduleTiming(now: now, zone: TimeZone(identifier: name)!)
            cutoff.end = .date; cutoff.until = scheduleDate(second)!
            #expect(cutoff.until == scheduleDate(first)!)
            #expect(try cutoff.trigger(now: now).until == first)
            // Selecting the default cutoff without editing it follows the same rule.
            let before = scheduleDate(second)!.addingTimeInterval(-86400)
            var defaultCutoff = ScheduleTiming(now: before, zone: TimeZone(identifier: name)!)
            defaultCutoff.end = .date
            #expect(defaultCutoff.until == scheduleDate(first)!)
            #expect(try defaultCutoff.trigger(now: before).until == first)
            var saved = ScheduleTrigger(type: "calendar"); saved.time = "08:00"; saved.timezone = name; saved.until = second
            let unchanged = ScheduleTiming(trigger: saved, now: now, zone: zone)
            #expect(try unchanged.trigger(now: now).until == second)
        }
    }
    @Test func timestampOffsetsDoNotBecomeCalendarZoneNames() throws {
        for at in ["2027-10-24T15:00:00Z","2027-10-24T15:00:00+08:00"] {
            var t=ScheduleTrigger(type:"at");t.at=at
            var edit=ScheduleTiming(trigger:t,now:now,zone:zone);edit.kind = .daily
            #expect(try edit.trigger(now:now).timezone == zone.identifier)
        }
    }
    @Test func dualLimitsKeepTheirAbsoluteDeadlineWhenOnlyTimezoneChanges() throws {
        var t=ScheduleTrigger(type:"calendar");t.time="08:00";t.timezone="America/Los_Angeles";t.maxRuns=5;t.until="2027-10-30T16:00:00+08:00"
        var edit=ScheduleTiming(trigger:t,now:now,zone:zone)
        #expect(edit.end == .count);edit.timezone="Asia/Shanghai"
        let changed=try edit.trigger(now:now)
        #expect(changed.until == t.until && changed.maxRuns == 5 && changed.timezone == "Asia/Shanghai")
    }
    @Test func cutoffDisplayTracksAbsoluteInstantAcrossZoneChanges() throws {
        var trigger=ScheduleTrigger(type:"calendar");trigger.time="08:00";trigger.timezone="Asia/Shanghai";trigger.until="2027-10-30T00:15:29Z"
        var edit=ScheduleTiming(trigger:trigger,now:now,zone:zone)
        for next in ["America/Los_Angeles","UTC","Asia/Tokyo"] {
            edit.timezone=next
            #expect(edit.until == scheduleDate(trigger.until!)!)
            #expect(try edit.trigger(now:now).until == trigger.until)
        }
        var repeated=trigger;repeated.timezone="UTC";repeated.until="2026-11-01T06:30:00Z"
        var overlap=ScheduleTiming(trigger:repeated,now:now,zone:zone);overlap.timezone="America/New_York"
        #expect(overlap.until == scheduleDate(repeated.until!)!)
        #expect(try overlap.trigger(now:now).until == repeated.until)
        let chosen=scheduleDate("2027-11-01T12:34:00Z")!
        edit.until=chosen;edit.timezone="America/Los_Angeles"
        #expect(edit.until == chosen)
        #expect(try edit.trigger(now:now).until == scheduleISO(chosen))
    }
    @Test func finalSharedClientContractUsesActualStateProjection() throws {
        struct Fixture: Decodable { let now: String; let timezone: String; let tasks: [ScheduleTask]; let empty: String; let offline: String; let offlineRow: String; let titleFromMessage: [[String]]; let groups: [String: [String]] }
        let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        let f = try JSONDecoder().decode(Fixture.self, from: Data(contentsOf: root.appendingPathComponent("testdata/schedules/clients.json")))
        #expect(scheduleEmpty == f.empty && scheduleOffline == f.offline && scheduleOfflineRow == f.offlineRow)
        for pair in f.titleFromMessage { #expect(scheduleTitle("", message: pair[0]) == pair[1]) }
        for group in ScheduleGroup.allCases {
            #expect(Set(f.tasks.filter { $0.visible && $0.group == group }.map(\.id)) == Set(f.groups[group.title] ?? []))
        }
        let attention = try #require(f.tasks.first { $0.id == "attention" })
        #expect(attention.lastResult() == L10n.scheduleTaskCreationPermissionsChangedTheTaskStopped())
        #expect(attention.history[0].result == L10n.scheduleNoNotification())
        #expect(attention.history[0].foldedSummary == L10n.scheduleNoNotification())
        var author = attention; #expect(author.creator == L10n.scheduleCreatedByYou()); author.source = "agent"; #expect(author.creator == L10n.scheduleCreatedByTheAgentInAConversation())
        var limits = attention.spec.trigger; limits.maxRuns = 5; limits.until = "2026-10-30T08:00:00Z"
        #expect(scheduleLimits(limits, now: scheduleDate(f.now)!, zone: TimeZone(identifier:f.timezone)!) == "共 5 次，或到 10月30日 01:00")
        #expect(scheduleReason("completed_while_paused") == L10n.schedulePausedTheScheduleExpiredInTheMeantime())
        var a=task("paused",reason:"user_paused"), b=a
        a.id="old";a.updatedAt="2026-10-24T10:00:00Z";a.nextRunAt="2026-10-24T20:00:00Z"
        b.id="new";b.updatedAt="2026-10-24T11:00:00Z";b.nextRunAt="2026-10-25T20:00:00Z"
        #expect(scheduleInbox([.init(machineID:"m",task:a),.init(machineID:"m",task:b)],group:.recent,hasNew:{ _ in false }).map { $0.task.id } == ["old","new"])
        var run=ScheduleRun(id:"r",startedAt:"",status:"completed");run.startSeq=42;run.afterSeq=9
        #expect(run.firstSeq==43);run.startSeq=0;#expect(run.firstSeq==10)
    }
    @Test func timezonesPreserveWallClockAndIntervalEditsKeepPhaseAndUntouchedLimits() throws {
        var calendar=ScheduleTrigger(type:"calendar");calendar.time="08:15";calendar.timezone="Asia/Shanghai"
        var draft=ScheduleTiming(trigger:calendar,now:now,zone:zone);draft.timezone="America/Los_Angeles"
        #expect(try draft.trigger(now:now).time=="08:15")
        var once=ScheduleTrigger(type:"at");once.at="2026-10-26T00:15:00Z";once.timezone="Asia/Shanghai"
        var one=ScheduleTiming(trigger:once,now:now,zone:zone);one.timezone="America/Los_Angeles"
        #expect(try one.trigger(now:now).at=="2026-10-26T15:15:00Z")
        var original=ScheduleTrigger(type:"interval");original.intervalSeconds=900;original.at="2026-10-01T08:00:00Z";original.delaySeconds=29;original.maxRuns=4;original.until="2026-10-30T08:15:00Z"
        var editing=ScheduleTiming(trigger:original,now:now,zone:zone);editing.count=5
        let result=try editing.trigger(now:now)
        #expect(result.at==original.at && result.delaySeconds==29 && result.until==original.until && result.maxRuns==5)
        #expect(try ScheduleTiming(trigger:original,now:now,zone:zone).trigger(now:now)==original)
        editing.kind = .daily; editing.end = .date;editing.timezone="Asia/Shanghai"
        editing.until=scheduleDate("2026-10-30T00:15:00Z")!;editing.timezone="America/Los_Angeles"
        #expect(try editing.trigger(now:now).until=="2026-10-30T00:15:00Z")
        var gap = ScheduleTrigger(type:"at"); gap.at="2027-03-14T02:30:00Z"; gap.timezone="UTC"
        var missing = ScheduleTiming(trigger:gap,now:now,zone:zone); missing.timezone="America/Los_Angeles"
        #expect(throws: UserError(L10nMessage.scheduleDaylightSavingTimeSkipsThisTimeInTheSelected())) { try missing.trigger(now:now) }
        var fresh=ScheduleTiming(now:now);fresh.kind = .interval;fresh.interval=15;fresh.unit=60
        #expect(try fresh.trigger(now:now).delaySeconds==900)
    }
    @Test func indivisibleMinuteIntervalsKeepExactIntegerSeconds() throws {
        for seconds: Int64 in [31,62,123] {
            var trigger=ScheduleTrigger(type:"interval");trigger.intervalSeconds=seconds;trigger.delaySeconds=29;trigger.at="2026-10-01T08:00:00Z"
            let edit=ScheduleTiming(trigger:trigger,now:now,zone:zone)
            #expect(edit.unit == 1 && edit.interval == Int(seconds))
            #expect(try edit.trigger(now:now) == trigger)
        }
    }
    @Test func groupingAndVisibility() {
        #expect(task("failed").group == .attention)
        #expect(task("uncertain").group == .attention)
        #expect(task("paused", reason: "agent_schedule_permissions_changed").group == .attention)
        #expect(task("paused", reason: "user_paused").group == .paused)
        #expect(task("cancelled", reason: "message_cancelled").group == .attention)
        #expect(!task("cancelled", reason: "user_cancelled").visible)
        #expect(task("completed").group == .ended)
        #expect(task().group == .active)
    }
    @Test func listSortsByNextOccurrenceAcrossMachines() {
        var a = task(), b = task(); a.nextRunAt = "2026-10-24T18:00:00Z"; b.nextRunAt = "2026-10-24T17:00:00Z"
        let entries = [MachineSchedule(machineID: "a", task: a), MachineSchedule(machineID: "b", task: b)]
        #expect(scheduleInbox(entries, group: .recent, hasNew: { _ in false }).map(\.machineID) == ["b", "a"])
    }
    @Test func inboxIncludesPausedAndUnreadEndedTasksAndSortsStopsByLatestRun() {
        var old = task("failed"), new = task("failed")
        old.id = "old"; new.id = "new"
        old.runs = [ScheduleRun(id: "notice", startedAt: "2026-10-20T08:00:00Z", status: "completed", notified: true),
                    ScheduleRun(id: "stop", startedAt: "2026-10-25T08:00:00Z", status: "failed")]
        new.updatedAt = "2026-10-24T08:00:00Z"
        let stops = [old, new].map { MachineSchedule(machineID: "m", task: $0) }
        #expect(scheduleInbox(stops, group: .attention, hasNew: { _ in false }).map { $0.task.id } == ["old", "new"])
        var paused = task("paused"); paused.id = "paused"; paused.nextRunAt = "2026-10-26T08:00:00Z"
        var fresh = task("completed"); fresh.id = "fresh"; fresh.runs = [old.runs![0]]
        var ended = fresh; ended.id = "ended"
        var never = task(); never.id = "never"; never.nextRunAt = "2026-10-25T08:00:00Z"
        var deleted = task("cancelled", reason: "user_cancelled"); deleted.id = "deleted"
        let entries = [paused, fresh, ended, never, deleted].map { MachineSchedule(machineID: "m", task: $0) }
        let unread: (MachineSchedule) -> Bool = { $0.task.id == "fresh" }
        #expect(scheduleInbox(entries, group: .recent, hasNew: unread).map { $0.task.id } == ["fresh", "never", "paused"])
        #expect(scheduleInbox(entries, group: .ended, hasNew: unread).map { $0.task.id } == ["ended"])
        #expect(scheduleInbox(entries, group: .recent, hasNew: { _ in false }).map { $0.task.id } == ["never", "paused"])
    }
    @Test func endedTasksUseUpdatedOrCreatedTimeAfterNotificationOrdering() {
        func ended(_ id: String, updated: String?, created: String, notice: String? = nil) -> MachineSchedule {
            var t = task("completed"); t.id = id; t.updatedAt = updated; t.createdAt = created
            t.nextRunAt = id == "old" ? "2026-01-01T00:00:00Z" : "2027-01-01T00:00:00Z"
            if let notice { t.runs = [ScheduleRun(id: "notice", startedAt: notice, status: "completed", notified: true)] }
            return .init(machineID: "m", task: t)
        }
        let old = ended("old", updated: "2026-10-20T00:00:00Z", created: "2026-10-01T00:00:00Z")
        let updated = ended("updated", updated: "2026-10-24T00:00:00Z", created: "2026-10-01T00:00:00Z")
        let created = ended("created", updated: nil, created: "2026-10-23T00:00:00Z")
        let notice = ended("notice", updated: nil, created: "2026-10-01T00:00:00Z", notice: "2026-10-10T00:00:00Z")
        #expect(scheduleInbox([old, created, updated, notice], group: .ended, hasNew: { _ in false }).map { $0.task.id } == ["notice", "updated", "created", "old"])
        var sameNoticeOld = old, sameNoticeNew = updated
        sameNoticeOld.task.runs = notice.task.runs; sameNoticeNew.task.runs = notice.task.runs
        #expect(scheduleInbox([sameNoticeOld, sameNoticeNew], group: .ended, hasNew: { _ in false }).map { $0.task.id } == ["updated", "old"])
    }
    @Test func notificationTimeDecodesAndFallsBackWithoutChangingTheHistoryDate() throws {
        let data = Data(#"{"id":"notice","started_at":"2026-10-24T17:00:00Z","ended_at":"2026-10-24T20:00:00Z","scheduled_at":"2026-10-24T16:00:00Z","status":"completed","notified":true,"start_seq":0,"after_seq":0}"#.utf8)
        var run = try JSONDecoder().decode(ScheduleRun.self, from: data)
        let started = try #require(scheduleDate(run.startedAt))
        #expect(run.notificationTime == scheduleDate("2026-10-24T20:00:00Z"))
        #expect(run.date == started)
        #expect(try JSONDecoder().decode(ScheduleRun.self, from: JSONEncoder().encode(run)) == run)
        run.endedAt = nil
        #expect(run.notificationTime == started && run.date == started)
        run.endedAt = "" // Missing/invalid times use the next available timestamp.
        #expect(run.notificationTime == started)
        run.startedAt = ""
        #expect(run.notificationTime == scheduleDate("2026-10-24T16:00:00Z"))
        #expect(run.date == .distantPast)
        run.scheduledAt = nil
        #expect(run.notificationTime == .distantPast && run.date == .distantPast)
    }
    @Test func notificationsUseEndTimeForSelectionPreviewAndEveryGroupSort() throws {
        var earlyStart = ScheduleRun(id: "early-start", startedAt: "2026-10-24T17:00:00Z", status: "completed", notified: true)
        earlyStart.endedAt = "2026-10-24T20:00:00Z"; earlyStart.notificationText = "开始早，最后才结束"
        var lateStart = ScheduleRun(id: "late-start", startedAt: "2026-10-24T18:00:00Z", status: "completed", notified: true)
        lateStart.endedAt = "2026-10-24T19:00:00Z"; lateStart.notificationText = "开始晚，先结束"
        var combined = task(); combined.runs = [earlyStart, lateStart]
        #expect(combined.history.map(\.id) == ["late-start", "early-start"])
        #expect(combined.latestNotification?.id == "early-start")
        #expect(combined.notificationPreview() == earlyStart.notificationText)
        let selected = try #require(combined.latestNotification)
        let notificationMS = Int64(selected.notificationTime.timeIntervalSince1970 * 1000)
        let now = try #require(scheduleDate("2026-10-24T21:30:00Z"))
        #expect(whenText(notificationMS, now: Int64(now.timeIntervalSince1970 * 1000), zone: TimeZone(secondsFromGMT: 0)!) == "20:00")
        for (status, group) in [("failed", ScheduleGroup.attention), ("active", .active), ("paused", .paused), ("completed", .ended)] {
            var earlyTask = task(status); earlyTask.id = "early-task"; earlyTask.runs = [earlyStart]
            var lateTask = task(status); lateTask.id = "late-task"; lateTask.runs = [lateStart]
            #expect(scheduleInbox([.init(machineID: "m", task: lateTask), .init(machineID: "m", task: earlyTask)], group: group == .attention ? .attention : .recent, hasNew: { _ in true }).map { $0.task.id } == ["early-task", "late-task"])
        }
    }
    @Test func notificationPreviewUsesNoticeThenFinalSummaryAndAttentionReason() {
        var t = task()
        #expect(t.latestNotification == nil && t.notificationPreview() == L10n.scheduleHasNotRunYet())
        var r = ScheduleRun(id: "notice", startedAt: "2026-10-24T08:00:00Z", status: "completed", notified: true)
        r.notificationText = "新回复"; r.finalSummary = "检查完了"
        t.runs = [r, ScheduleRun(id: "silent", startedAt: "2026-10-25T08:00:00Z", status: "completed")]
        #expect(t.latestNotification?.id == "notice" && t.notificationPreview() == "新回复")
        t.runs?[0].notificationText = "  "; #expect(t.notificationPreview() == "检查完了")
        t.runs?[0].finalSummary = nil; #expect(t.notificationPreview() == L10n.scheduleNotifiedYou())
        t.active = ScheduleRun(id: "active-notice", startedAt: "2026-10-26T08:00:00Z", status: "running", notified: true)
        t.active?.notificationText = "运行中有变化"
        #expect(t.latestNotification?.id == "active-notice" && t.notificationPreview() == "运行中有变化")
        t.active?.status = "interrupted"; t.active?.reason = "execution_interrupted"
        #expect(t.notificationPreview() == L10n.scheduleExecutionInterruptedTheTaskStopped())
        t.status = "failed"; t.reason = "relay_session_limit"
        #expect(t.notificationPreview() == L10n.scheduleTheRelaySSessionLimitHasBeenReached())
    }
    @Test func firstRunningTaskHasNoResultPreviewAndLaterRunsKeepTheLastResult() {
        var t = task(); t.nextRunAt = "2026-10-24T18:05:00Z"
        #expect(t.listPreview(now: now, zone: zone) == "还没有运行 · 下次 今天 11:05")
        for status in ["dispatching", "queued", "running"] {
            t.active = ScheduleRun(id: "first", startedAt: "2026-10-24T17:00:00Z", status: status)
            #expect(t.awaitingFirstResult && t.listPreview(now: now, zone: zone) == L10n.scheduleNoResultsYet())
            #expect(!t.listPreview(now: now, zone: zone).contains(L10n.scheduleRunning()) && t.latestNotification == nil)
        }
        t.runs = [ScheduleRun(id: "silent", startedAt: "2026-10-23T17:00:00Z", status: "completed")]
        #expect(t.listPreview(now: now, zone: zone) == L10n.scheduleNoResultsYet())
        var result = ScheduleRun(id: "result", startedAt: "2026-10-23T17:00:00Z", status: "completed", notified: true)
        result.notificationText = "有新回复"; result.finalSummary = "回复全文的摘要"
        result.endedAt = "2026-10-23T17:03:00Z"; t.runs?.append(result)
        #expect(!t.awaitingFirstResult && t.listPreview(now: now, zone: zone) == "有新回复")
        #expect(t.latestNotification?.notificationTime == scheduleDate("2026-10-23T17:03:00Z"))
        t.runs?[1].notificationText = nil
        #expect(t.listPreview(now: now, zone: zone) == "回复全文的摘要")
    }
    @Test func resultTitlesRequireADistinctAgentNotificationWithoutChangingListPreviews() {
        for status in ["completed", "failed", "interrupted", "cancelled"] {
            for source in [nil, "", "daemon", "undeliverable", "agent", "agent_and_daemon"] as [String?] {
                var run = ScheduleRun(id: "result", startedAt: "2026-10-24T17:00:00Z", status: status, notified: true)
                run.notificationSource = source; run.notificationText = "  新的变化 \n"; run.finalSummary = "完整结果的摘要"
                #expect(run.resultTitle == (["agent", "agent_and_daemon"].contains(source ?? "") ? "新的变化" : nil))
                run.notificationText = " \n完整结果的摘要\t"
                #expect(run.resultTitle == nil)
                run.finalSummary = "  完整结果的摘要\n"
                #expect(run.resultTitle == nil)
                run.notificationText = " \n\t"; #expect(run.resultTitle == nil)
                run.notificationText = nil; #expect(run.resultTitle == nil)
            }
        }
        var run = ScheduleRun(id: "notice", startedAt: "2026-10-24T17:00:00Z", status: "completed", notified: true)
        run.notificationSource = "daemon"; run.notificationText = "完成通知摘要"; run.finalSummary = "结果摘要"
        var t = task(); t.runs = [run]
        #expect(run.resultTitle == nil && t.listPreview() == "完成通知摘要")
        #expect(scheduleRunSummary(run) == "完成通知摘要") // Earlier rows use one summary, not a separate title.
    }
    @Test func earlierRunsAreEmptyUntilThereIsAnotherHistoricalRun() {
        var t = task()
        #expect(t.earlierRuns(resultID: nil).isEmpty)
        let result = ScheduleRun(id: "latest", startedAt: "2026-10-24T17:00:00Z", status: "completed", notified: true)
        t.runs = [result]; #expect(t.earlierRuns(resultID: result.id).isEmpty)
        t.active = ScheduleRun(id: "active", startedAt: "2026-10-25T17:00:00Z", status: "running")
        #expect(t.earlierRuns(resultID: result.id).isEmpty)
        t.runs?.append(ScheduleRun(id: "older", startedAt: "2026-10-23T17:00:00Z", status: "completed"))
        #expect(t.earlierRuns(resultID: result.id).map(\.id) == ["older"])
        t.runs?.append(ScheduleRun(id: "newer", startedAt: "2026-10-25T18:00:00Z", status: "completed", notified: true))
        #expect(t.earlierRuns(resultID: result.id).map(\.id) == ["older"])
    }
    @Test func localTimeAndOfflineState() {
        var t = task(); t.nextRunAt = "2026-10-24T18:05:00Z"
        #expect(t.stateText(online: true, now: now, zone: zone) == "下次 今天 11:05")
        #expect(t.stateText(online: false, now: now, zone: zone) == scheduleOffline)
        t.active = ScheduleRun(id: "r", startedAt: "2026-10-24T17:00:00Z", status: "running")
        #expect(t.stateText(online: false) == scheduleOffline)
        #expect(t.stateText(online: true) == L10n.listStateRunning())
    }
    @Test func scheduleDatesUseSharedChineseLocalCalendarWording() {
        for (at, expected) in [
            ("2026-10-24T19:59:00Z", "今天 12:59"),
            ("2026-10-25T15:00:00Z", "明天 08:00"),
            ("2026-10-26T18:54:00Z", "10月26日 11:54"),
            ("2026-10-23T18:54:00Z", "昨天 11:54"),
            ("2027-10-25T18:54:00Z", "2027年10月25日 11:54"),
        ] {
            let date = scheduleDate(at)!
            #expect(scheduleMoment(date, now: now, zone: zone) == expected)
            var t = task(); t.nextRunAt = at
            #expect(t.stateText(online: true, now: now, zone: zone) == "下次 " + expected)
            var once = ScheduleTrigger(type: "at"); once.at = at
            #expect(scheduleFrequency(once, zone: zone, now: now) == expected + "，一次")
        }
        // UTC is already the next day; local calendar boundaries still decide Today/Tomorrow.
        let midnight = scheduleDate("2026-10-25T06:59:00Z")!
        #expect(scheduleMoment(scheduleDate("2026-10-25T07:01:00Z")!, now: midnight, zone: zone) == "明天 00:01")
        let yearEnd = scheduleDate("2026-12-31T20:00:00Z")!
        #expect(scheduleMoment(scheduleDate("2027-01-01T20:00:00Z")!, now: yearEnd, zone: zone) == "明天 12:00")
        let dst = scheduleDate("2026-10-31T17:00:00Z")!
        #expect(scheduleMoment(scheduleDate("2026-11-01T18:00:00Z")!, now: dst, zone: zone) == "明天 10:00")
    }
    @Test func frequenciesMatchSharedClientLabels() {
        for (seconds, expected) in [(3600, "每小时"), (172800, "每 2 天"), (7200, "每 2 小时"), (300, "每 5 分钟"), (30, "每 30 秒")] {
            var t = ScheduleTrigger(type: "interval"); t.intervalSeconds = Int64(seconds)
            #expect(scheduleFrequency(t) == expected)
        }
        var after = ScheduleTrigger(type: "after_turn"); after.delaySeconds = 60
        #expect(scheduleFrequency(after) == "当前轮结束 60 秒后")
        after.repeatAfterTurn = true; #expect(scheduleFrequency(after) == "每轮结束 60 秒后")
    }
    @Test func namesNormalizeWhitespaceAndCountCodepoints() {
        #expect(scheduleTitle("  检查 \n PR  ", message: "无关") == "检查 PR")
        #expect(scheduleTitle("", message: "检查 PR。没有变化就停\n第二行") == "检查 PR")
        #expect(scheduleTitle("", message: "Check replies! Next sentence") == "Check replies")
        #expect(scheduleTitle("", message: "\n  Check v0.45.7. Then stop") == "Check v0.45.7")
        #expect(scheduleTitle("", message: "。检查新回复") == "。检查新回复")
        #expect(scheduleTitle(String(repeating: "a", count: 70), message: "").count == 60)
    }
    @Test func historyAggregationAndFailurePriority() {
        var t = task()
        let a = ScheduleRun(id: "a", startedAt: "2026-10-24T15:00:00Z", status: "completed")
        let b = ScheduleRun(id: "b", startedAt: "2026-10-24T16:00:00Z", status: "completed")
        t.runs = [a, b]
        var one = a; one.finalSummary = "没有新变化"; t.runs = [one]
        #expect(t.lastResult(now: now, zone: zone) == "今天 08:00 没有通知")
        t.runs = [a, b]
        #expect(t.lastResult(now: now, zone: zone) == "今天 2 次，都没有通知")
        #expect(scheduleHistoryGroups(t.history, zone: zone).count == 1)
        var bad = ScheduleRun(id: "bad", startedAt: "2026-10-24T16:30:00Z", status: "failed", notified: true)
        bad.notificationText = "旧的通知"; bad.finalSummary = "误导的摘要"; bad.reason = "relay_session_limit"
        t.runs?.append(bad)
        var ongoing = bad; ongoing.status = "running"
        #expect(ongoing.result == L10n.scheduleNotifiedYou())
        #expect(bad.result == L10n.attachmentFailed())
        #expect(bad.detail == L10n.scheduleTheRelaySSessionLimitHasBeenReached())
        #expect(t.lastResult(now: now, zone: zone).contains("失败：中继上的会话数已满"))
        #expect(scheduleHistoryGroups(t.history, zone: zone).count == 2)
    }
    @Test func calendarTimezoneAndAlias() {
        var tr = ScheduleTrigger(type: "calendar"); tr.time = "09:49"; tr.timezone = "US/Pacific"
        #expect(scheduleFrequency(tr, zone: zone, includeZone: true) == "每天 09:49")
        tr.timezone = "Asia/Shanghai"
        #expect(scheduleFrequency(tr, zone: zone, includeZone: true) == "每天 09:49 · Asia/Shanghai")
        tr.weekdays = [1, 2, 3, 4, 5]
        #expect(scheduleFrequency(tr, zone: zone) == "工作日 09:49")
    }
    @Test func timingRoundtripsAndValidates() throws {
        var draft = ScheduleTiming(now: now, zone: zone)
        #expect(draft.until == now.addingTimeInterval(86400))
        draft.kind = .weekly; draft.weekdays = [2, 4]; draft.end = .count; draft.count = 6
        let tr = try draft.trigger(now: now)
        #expect(tr.type == "calendar" && tr.weekdays == [2, 4] && tr.maxRuns == 6 && tr.timezone == zone.identifier)
        #expect(try ScheduleTiming(trigger: tr, now: now, zone: zone).trigger(now: now) == tr)
        draft.kind = .once; draft.date = now.addingTimeInterval(-1)
        #expect(throws: (any Error).self) { try draft.trigger(now: now) }
        draft.kind = .interval; draft.interval = 0
        #expect(throws: (any Error).self) { try draft.trigger(now: now) }
        draft.interval = 5; draft.unit = 60; draft.end = .date; draft.until = now.addingTimeInterval(600)
        #expect(try draft.trigger(now: now).intervalSeconds == 300)
        var after = ScheduleTrigger(type: "after_turn"); after.delaySeconds = 0; after.repeatAfterTurn = true; after.maxRuns = 6
        var preserved = ScheduleTiming(trigger: after, now: now, zone: zone)
        preserved.kind = .interval; preserved.interval = 10; preserved.unit = 60
        preserved.kind = .afterTurn
        #expect(try preserved.trigger(now: now) == after)
        preserved.end = .never
        #expect(try preserved.trigger(now: now).maxRuns == nil)
    }
    @Test func commandCarriesCreationIdentityAndExpectedRevision() throws {
        let cmd = try scheduleCommand(operation: "create", spec: task().spec, revision: 7, creationKey: "stable-key", sessionID: "source")
        #expect(cmd.schedule.requestID == "stable-key" && cmd.schedule.expectedRevision == 7 && cmd.schedule.sessionID == "source")
        #expect(try JSONDecoder().decode(ScheduleSpec.self, from: cmd.schedule.specJson) == task().spec)
    }
    func round(_ seq: UInt64, at: String = "2026-10-24T15:00:00Z", run: String) -> [PBEvent] {
        var user = PBEvent(); user.seq = seq; user.time.seconds = Int64(scheduleDate(at)!.timeIntervalSince1970); user.userMessage.text = "检查"; user.origin.scheduledRunID = run
        var reply = PBEvent(); reply.seq = seq + 1; reply.agentMessage.text = "没有新回复"
        var end = PBEvent(); end.seq = seq + 2; end.turnEnded.outcome = .completed; end.turnEnded.scheduledRunID = run
        return [user, reply, end]
    }
    @Test func visibleInterRoundEventsAreFoldingBoundaries() {
        var model=PBEvent();model.settingsChanged.model="new-model"
        var mode=PBEvent();mode.settingsChanged.mode = .auto
        var compact=PBEvent();compact.settingsChanged.compacted=true
        var answer=PBEvent();answer.questionAnswered.questionID="q"
        var context=PBEvent();context.turnContext.model="new-model"
        var ended=PBEvent();ended.turnEnded.durationMs=1000
        var image=PBEvent();image.imageGenerated.revisedPrompt="image"
        var exit=PBEvent();exit.sessionState.status = .exited
        for var event in [model,mode,compact,answer,context,ended,image,exit] {
            event.seq=4
            #expect(silentScheduledGroups(round(1,run:"a")+[event]+round(5,run:"b"),zone:zone).isEmpty)
        }
    }
    @Test func failedToolsAndProtectedInRoundRowsRemainVisible() {
        var failed=PBEvent();failed.toolCall.title="failed";failed.toolCall.status="failed"
        var nonzero=PBEvent();nonzero.toolCall.title="nonzero";nonzero.toolCall.exitCode=7
        var background=PBEvent();background.toolCall.title="background";background.toolCall.background = .stillRunning
        var pending=PBEvent();pending.toolStarted.callID="pending";pending.toolStarted.title="pending"
        var setting=PBEvent();setting.settingsChanged.model="other-model"
        var answer=PBEvent();answer.questionAnswered.questionID="q"
        for var e in [failed,nonzero,background,pending,setting,answer] {
            e.seq=2;var first=round(1,run:"a");first[1]=e
            #expect(silentScheduledGroups(first+round(4,run:"b"),zone:zone).isEmpty)
        }
        var success=PBEvent();success.seq=2;success.toolCall.status="completed";success.toolCall.exitCode=0
        var first=round(1,run:"a");first[1]=success
        #expect(silentScheduledGroups(first+round(4,run:"b"),zone:zone).count == 1)
    }
    @Test func terminalSettingChangesInsideRoundsRemainVisible() {
        var first=round(1,run:"a")
        var initial=PBEvent();initial.seq=0;initial.turnContext.model="old-model"
        first[1].turnContext.model="new-model"
        let events=[initial]+first+round(4,run:"b")
        #expect(derive(events).terminalChanges[2] != nil)
        #expect(silentScheduledGroups(events,zone:zone).isEmpty)
    }
    @Test func lateFailedToolResultCannotHideAtTheEarlierStart() {
        var second=round(4,run:"b");second[1].toolStarted.title="late";second[1].toolStarted.callID="late"
        var third=round(7,run:"c");third[1].toolCall.title="late";third[1].toolCall.callID="late";third[1].toolCall.status="failed";third[1].toolCall.exitCode=7
        let events=round(1,run:"a")+second+third
        #expect(derive(events).completedAt[5] == 8)
        #expect(silentScheduledGroups(events,zone:zone).isEmpty)
    }
    @Test func foldsOnlyConsecutiveSuccessfulSilentScheduledRounds() {
        let first = round(1, run: "a"), second = round(4, at: "2026-10-24T16:00:00Z", run: "b")
        #expect(silentScheduledGroups(first, zone: zone).isEmpty)
        var undated = first; undated[0].clearTime()
        #expect(silentScheduledGroups(undated + second, zone: zone).isEmpty)
        let groups = silentScheduledGroups(first + second, zone: zone)
        #expect(groups.count == 1 && groups[0].firstSeq == 1 && groups[0].lastSeq == 6)
        var laterNotice = PBEvent(); laterNotice.seq = 7; laterNotice.scheduledNotification.runID = "b"; laterNotice.scheduledNotification.text = "后来发现了变化"
        #expect(silentScheduledGroups(first + second + [laterNotice], zone: zone).isEmpty)
        var laterFailure = PBEvent(); laterFailure.seq = 7; laterFailure.turnEnded.outcome = .aborted; laterFailure.turnEnded.scheduledRunID = "b"
        #expect(silentScheduledGroups(first + second + [laterFailure], zone: zone).isEmpty)
        for unsafe in ["notify", "skeleton", "pending", "failed", "autonomous", "human", "next-day"] {
            var changed = second
            switch unsafe {
            case "notify": changed[1].scheduledNotification.text = "有变化"
            case "skeleton": changed[1].agentMessage.text = skeletonNote
            case "pending": changed[1].approvalRequested.approvalID = "approval"
            case "failed": changed[2].turnEnded.outcome = .aborted
            case "human": changed[0].origin.scheduledRunID = ""
            case "next-day": changed = round(4, at: "2026-10-25T16:00:00Z", run: "b")
            default:
                var auto = PBEvent(); auto.seq = 3; auto.agentMessage.text = "自主继续的回复"
                #expect(silentScheduledGroups(first + [auto] + second, zone: zone).isEmpty); continue
            }
            #expect(silentScheduledGroups(first + changed, zone: zone).isEmpty, "must preserve \(unsafe)")
        }
    }
}

@Suite struct ScheduleCompletionNotificationTests {
    @Test func completionPolicyDefaultsOnAndRoundTripsOptOut() throws {
        let raw = Data(#"{"title":"Check","message":"Look","trigger":{"type":"interval","interval_seconds":3600},"target":{"type":"new_session"}}"#.utf8)
        var spec = try JSONDecoder().decode(ScheduleSpec.self, from: raw)
        #expect(spec.notifyOnFinish)
        spec.notifyOnFinish = false
        spec.title = "Renamed"
        let decoded = try JSONDecoder().decode(ScheduleSpec.self, from: JSONEncoder().encode(spec))
        #expect(decoded.notify == "agent_only" && !decoded.notifyOnFinish)
        var run = ScheduleRun(id: "r", startedAt: "", status: "completed", notified: true)
        run.notificationSource = "daemon"
        #expect(run.notificationLabel == L10n.scheduleNotifyOnCompletion())
        run.notificationSource = "undeliverable"; run.notified = false
        #expect(run.notificationLabel == L10n.scheduleIosCannotRequestNotification() && run.result == L10n.scheduleNoNotification())
    }
}

@Suite struct ScheduleInboxLogicTests {
    @Test func scheduleRowUsesCalendarDaysAndYearsRatherThanElapsedHours() {
        let zone = TimeZone(identifier: "America/Los_Angeles")!
        let now = scheduleDate("2026-11-02T08:15:00Z")!
        for (stamp, expected) in [("2026-11-02T08:01:00Z", "00:01"), ("2026-11-01T07:30:00Z", "昨天"),
                                  ("2026-10-31T17:00:00Z", "周六"), ("2026-10-26T17:00:00Z", "10月26日"),
                                  ("2025-11-01T07:30:00Z", "2025年11月1日")] {
            #expect(scheduleRowTime(scheduleDate(stamp)!, now: now, zone: zone) == expected)
        }
        #expect(scheduleRowTime(scheduleDate("2025-12-31T20:00:00Z")!, now: scheduleDate("2026-01-02T20:00:00Z")!, zone: zone) == "周三")
    }
    @Test func frequencyKeepsTaskWallClockAndUsesCityLabels() {
        var trigger = ScheduleTrigger(type: "calendar"); trigger.time = "08:00"; trigger.timezone = "Asia/Shanghai"
        #expect(scheduleListFrequency(trigger, zone: TimeZone(identifier: "America/Los_Angeles")!) == "每天 08:00（北京时间）")
        #expect(scheduleListFrequency(trigger, zone: TimeZone(identifier: "Asia/Shanghai")!) == "每天 08:00")
        trigger.timezone = "America/Argentina/Buenos_Aires"
        #expect(scheduleListFrequency(trigger, zone: TimeZone(identifier: "Asia/Shanghai")!) == "每天 08:00（Buenos Aires时间）")
    }
    @Test func finalAnswerUsesLastFinalAndStopsAtEveryDaemonBoundary() {
        func user(_ run: String = "", kind: PBOriginKind = .system) -> PBEvent {
            .with { $0.userMessage.text = "检查"; $0.origin.scheduledRunID = run; $0.origin.kind = kind }
        }
        func reply(_ text: String, phase: String = "final_answer") -> PBEvent { .with { $0.agentMessage.text = text; $0.agentMessage.phase = phase } }
        let prefix = [reply("以前"), user("run"), reply("过程", phase: "commentary"), reply("初稿"), reply("## 完整结果\n\n| 列 | 值 |\n| --- | --- |\n| a | b |")]
        let expected = prefix.last!.agentMessage.text
        let boundaries: [PBEvent] = [user(kind: .client), user(kind: .local), user("other"),
            .with { $0.sessionState.detail = "resumed"; $0.origin.kind = .local }, .with { $0.sessionState.status = .exited }]
        for boundary in boundaries { #expect(scheduleFinalAnswer(prefix + [boundary, reply("不属于本次")], runID: "run") == expected) }
        #expect(scheduleFinalAnswer(prefix + [reply("后续最终回复")], runID: "run") == "后续最终回复")
        #expect(scheduleFinalAnswer([reply("没有对应输入")], runID: "run") == nil)
        #expect(scheduleFinalAnswer([user("run"), reply("过程", phase: "commentary")], runID: "run") == nil)
        #expect(scheduleFinalAnswer([user("run"), reply("  ")], runID: "run") == nil)
    }
}
