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
        #expect(t.stateText(online: true, now: now, zone: zone) == "下次 11:05")
        #expect(t.stateText(online: false, now: now, zone: zone) == "未连接")
        t.active = ScheduleRun(id: "r", startedAt: "2026-10-24T17:00:00Z", status: "running")
        #expect(t.stateText(online: false) == "未连接")
        #expect(t.stateText(online: true) == "正在运行")
    }
    @Test func namesNormalizeWhitespaceAndCountCodepoints() {
        #expect(scheduleTitle("  检查 \n PR  ", message: "无关") == "检查 PR")
        #expect(scheduleTitle("", message: "检查 PR。没有变化就停\n第二行") == "检查 PR")
        #expect(scheduleTitle("", message: "Check replies! Next sentence") == "Check replies")
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
