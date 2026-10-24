import Foundation
import Testing
import RAProto
@testable import RACore

@Suite struct SchedulesTests {
    let now = scheduleDate("2026-10-24T17:00:00Z")!
    let zone = TimeZone(identifier: "America/Los_Angeles")!
    func task(_ status: String = "scheduled", reason: String? = nil) -> ScheduleTask {
        var task = ScheduleTask(id: "task", spec: ScheduleSpec(title: "检查", message: "检查回复。没有变化就简短说一句", trigger: ScheduleTrigger(type: "calendar"), target: ScheduleTarget(type: "new_session")), status: status)
        task.reason = reason; return task
    }
    @Test func timestampOffsetsDoNotBecomeCalendarZoneNames() throws {
        for at in ["2027-10-24T15:00:00Z","2027-10-24T15:00:00+08:00"] {
            var t=ScheduleTrigger(type:"at");t.at=at
            var edit=ScheduleTiming(trigger:t,now:now,zone:zone);edit.kind = .daily
            #expect(try edit.trigger(now:now).timezone == zone.identifier)
        }
    }
    @Test func dualLimitsKeepTheirAbsoluteDeadlineWhenOnlyTimezoneChanges() throws {
        var t=ScheduleTrigger(type:"calendar");t.time="08:00";t.timezone="America/Los_Angeles";t.maxRuns=5;t.until="2027-10-30T08:00:00Z"
        var edit=ScheduleTiming(trigger:t,now:now,zone:zone)
        #expect(edit.end == .count);edit.timezone="Asia/Shanghai"
        let changed=try edit.trigger(now:now)
        #expect(changed.until == t.until && changed.maxRuns == 5 && changed.timezone == "Asia/Shanghai")
    }
    @Test func finalSharedClientContractUsesActualStateProjection() throws {
        struct Fixture: Decodable { let now: String; let timezone: String; let tasks: [ScheduleTask]; let empty: String; let offline: String; let groups: [String: [String]] }
        let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        let f = try JSONDecoder().decode(Fixture.self, from: Data(contentsOf: root.appendingPathComponent("testdata/schedules/clients.json")))
        #expect(scheduleEmpty == f.empty && scheduleOffline == f.offline)
        for group in ScheduleGroup.allCases {
            #expect(sortedSchedules(f.tasks.map { MachineSchedule(machineID: "computer", task: $0) }, group: group).map { $0.task.id } == f.groups[group.title])
        }
        let attention = try #require(f.tasks.first { $0.id == "attention" })
        #expect(attention.lastResult() == "创建任务时的权限已改变，任务已暂停")
        #expect(attention.history[0].result == "没有通知")
        #expect(attention.history[0].foldedSummary == "没有通知")
        var author = attention; #expect(author.creator == "你创建"); author.source = "agent"; #expect(author.creator == "Agent 在对话里创建")
        var limits = attention.spec.trigger; limits.maxRuns = 5; limits.until = "2026-10-30T08:00:00Z"
        #expect(scheduleLimits(limits, now: scheduleDate(f.now)!, zone: TimeZone(identifier:f.timezone)!) == "共 5 次，或到 10月30日 01:00")
        #expect(scheduleReason("completed_while_paused") == "已暂停：期间计划已到期")
        var a=task("paused",reason:"user_paused"), b=a
        a.id="old";a.updatedAt="2026-10-24T10:00:00Z";a.nextRunAt="2026-10-24T20:00:00Z"
        b.id="new";b.updatedAt="2026-10-24T11:00:00Z";b.nextRunAt="2026-10-25T20:00:00Z"
        #expect(sortedSchedules([.init(machineID:"m",task:a),.init(machineID:"m",task:b)],group:.paused).map { $0.task.id } == ["new","old"])
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
        #expect(try editing.trigger(now:now).until=="2026-10-30T15:15:00Z")
        var gap = ScheduleTrigger(type:"at"); gap.at="2027-03-14T02:30:00Z"; gap.timezone="UTC"
        var missing = ScheduleTiming(trigger:gap,now:now,zone:zone); missing.timezone="America/Los_Angeles"
        #expect(throws:(any Error).self) { try missing.trigger(now:now) }
        var fresh=ScheduleTiming(now:now);fresh.kind = .interval;fresh.interval=15;fresh.unit=60
        #expect(try fresh.trigger(now:now).delaySeconds==900)
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
        #expect(sortedSchedules(entries, group: .active).map(\.machineID) == ["b", "a"])
    }
    @Test func localTimeAndOfflineState() {
        var t = task(); t.nextRunAt = "2026-10-24T18:05:00Z"
        #expect(t.stateText(online: true, now: now, zone: zone) == "下次 今天 11:05")
        #expect(t.stateText(online: false, now: now, zone: zone) == scheduleOffline)
        t.active = ScheduleRun(id: "r", startedAt: "2026-10-24T17:00:00Z", status: "running")
        #expect(t.stateText(online: false) == scheduleOffline)
        #expect(t.stateText(online: true) == "运行中")
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
        #expect(t.lastResult(now: now, zone: zone) == "今天 2 次都没有通知")
        #expect(scheduleHistoryGroups(t.history, zone: zone).count == 1)
        var bad = ScheduleRun(id: "bad", startedAt: "2026-10-24T16:30:00Z", status: "failed", notified: true)
        bad.notificationText = "旧的通知"; bad.finalSummary = "误导的摘要"; bad.reason = "relay_session_limit"
        t.runs?.append(bad)
        var ongoing = bad; ongoing.status = "running"
        #expect(ongoing.result == "通知了你")
        #expect(bad.result == "失败")
        #expect(bad.detail == "中继上的会话数已满")
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
