import Foundation
import RAProto

/// JSON carried only inside the encrypted Schedule machine command.
public struct ScheduleSpec: Codable, Equatable, Sendable {
    public var title: String
    public var message: String
    public var trigger: ScheduleTrigger
    public var target: ScheduleTarget
    public var lateSeconds: Int64?
    public var notify: String?
    public var notifyOnFinish: Bool {
        get { notify != "agent_only" }
        set { notify = newValue ? "finish" : "agent_only" }
    }
    enum CodingKeys: String, CodingKey { case title, message, trigger, target, notify; case lateSeconds = "late_seconds" }
    public init(title: String, message: String, trigger: ScheduleTrigger, target: ScheduleTarget) {
        self.title = title; self.message = message; self.trigger = trigger; self.target = target
    }
}
public struct ScheduleTrigger: Codable, Equatable, Sendable {
    public var type: String
    public var time: String?
    public var weekdays: [Int]?
    public var timezone: String?
    public var until: String?
    public var at: String?
    public var delaySeconds: Int64?
    public var intervalSeconds: Int64?
    public var repeatAfterTurn: Bool?
    public var maxRuns: Int64?
    enum CodingKeys: String, CodingKey {
        case type, time, weekdays, timezone, until, at
        case delaySeconds = "delay_seconds", intervalSeconds = "interval_seconds", repeatAfterTurn = "repeat", maxRuns = "max_runs"
    }
    public init(type: String) { self.type = type }
}
public struct ScheduleTarget: Codable, Equatable, Sendable {
    public var type: String
    public var session: String?
    public var cwd: String?
    public var agent: String?
    public var model: String?
    public var effort: String?
    public var mode: String?
    public init(type: String) { self.type = type }
}
public struct ScheduleRun: Codable, Equatable, Sendable, Identifiable {
    public var id: String
    public var sessionID: String?
    public var startedAt: String
    public var endedAt: String?
    public var scheduledAt: String?
    public var status: String
    public var reason: String?
    public var manual: Bool?
    public var notified: Bool
    public var notificationText: String?
    public var notificationSource: String?
    public var finalSummary: String?
    public var startSeq: UInt64
    public var afterSeq: UInt64
    public var inputPaused: Bool?
    public var firstSeq: UInt64 { (startSeq == 0 ? afterSeq : startSeq) + 1 }
    enum CodingKeys: String, CodingKey {
        case id, status, reason, manual, notified
        case notificationSource = "notification_source"
        case endedAt = "ended_at", scheduledAt = "scheduled_at"
        case sessionID = "session_id", startedAt = "started_at", notificationText = "notification_text", finalSummary = "final_summary"
        case startSeq = "start_seq", afterSeq = "after_seq", inputPaused = "input_paused"
    }
    public init(id: String, startedAt: String, status: String, notified: Bool = false, sessionID: String? = nil) {
        self.id = id; self.startedAt = startedAt; self.status = status; self.notified = notified; self.sessionID = sessionID
        startSeq = 0; afterSeq = 0
    }
    public var date: Date { scheduleDate(startedAt) ?? .distantPast }
    /// Notification ordering is independent of the history's start-time ordering.
    public var notificationTime: Date {
        endedAt.flatMap(scheduleDate) ?? scheduleDate(startedAt) ?? scheduledAt.flatMap(scheduleDate) ?? .distantPast
    }
    /// Only a distinct notification written by the agent is a result-card title.
    /// Daemon completion notices repeat the summary and remain list previews only.
    public var resultTitle: String? {
        guard notificationSource == "agent" || notificationSource == "agent_and_daemon" else { return nil }
        let title = (notificationText ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        let summary = (finalSummary ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        return title.isEmpty || title == summary ? nil : title
    }
    public var silent: Bool { status == "completed" && !notified && inputPaused != true }
    public var foldedSummary: String { (finalSummary ?? "").isEmpty ? "没有通知" : finalSummary! }
    public var notificationLabel: String {
        switch notificationSource { case "agent": "agent 通知"; case "daemon": "完成后通知"; case "agent_and_daemon": "agent 通知、完成后通知"; case "undeliverable": "无法请求通知"; default: "" }
    }
    public var result: String {
        if notified && !["failed", "skipped", "uncertain", "interrupted", "cancelled"].contains(status) { return "通知了你" }
        switch status {
        case "failed": return "失败"
        case "skipped": return "跳过"
        case "uncertain": return "无法确认"
        case "interrupted": return "已中断"
        case "cancelled": return "已取消"
        case "dispatching": return "正在启动"
        case "queued": return "等待当前轮结束"
        case "running": return "正在运行"
        case "completed": return notified ? "通知了你" : "没有通知"
        default: return "等待结果"
        }
    }
    public var detail: String {
        if ["failed", "skipped", "uncertain", "interrupted", "cancelled"].contains(status) || inputPaused == true {
            return scheduleReason(reason)
        }
        return (notified ? notificationText : finalSummary) ?? ""
    }
}
public struct ScheduleTask: Codable, Equatable, Sendable, Identifiable {
    public var id: String
    public var revision: UInt64
    public var spec: ScheduleSpec
    public var status: String
    public var reason: String?
    public var createdAt: String
    public var updatedAt: String?
    public var source: String
    public var nextRunAt: String?
    public var runs: [ScheduleRun]?
    public var active: ScheduleRun?
    public var manualRequest: Manual?
    public struct Manual: Codable, Equatable, Sendable { public var requestID: String; enum CodingKeys: String, CodingKey { case requestID = "request_id" } }
    enum CodingKeys: String, CodingKey {
        case id, revision, spec, status, reason, source, runs
        case updatedAt = "updated_at", createdAt = "created_at", nextRunAt = "next_run_at", active = "active_run", manualRequest = "manual_request"
    }
    public init(id: String, spec: ScheduleSpec, status: String = "active", revision: UInt64 = 1, createdAt: String = "", source: String = "client") {
        self.id = id; self.spec = spec; self.status = status; self.revision = revision; self.createdAt = createdAt; self.source = source
    }
    public var creator: String { source == "agent" ? "Agent 在对话里创建" : "你创建" }
    public var history: [ScheduleRun] { ((runs ?? []) + (active.map { [$0] } ?? [])).sorted { $0.date > $1.date } }
    public var latestNotification: ScheduleRun? {
        history.filter(\.notified).max { $0.notificationTime < $1.notificationTime }
    }
    public func notificationPreview(now: Date = Date(), zone: TimeZone = .current) -> String {
        if group == .attention { return scheduleReason(reason) }
        guard let run = latestNotification else { return lastResult(now: now, zone: zone) }
        // A stopped run shows its readable reason, as on web and Android, not the daemon's notice text.
        if ["failed", "skipped", "uncertain", "interrupted", "cancelled"].contains(run.status) { return scheduleReason(run.reason) }
        for text in [run.notificationText, run.finalSummary] {
            if let text, !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { return text }
        }
        return run.result
    }
    public var group: ScheduleGroup {
        switch status {
        case "failed", "uncertain": return .attention
        case "paused": return ["", "user_paused", "completed_while_paused"].contains(reason ?? "") ? .paused : .attention
        case "cancelled": return (reason ?? "").isEmpty || reason == "user_cancelled" ? .ended : .attention
        case "completed": return .ended
        default: return .active
        }
    }
    public var visible: Bool { status != "cancelled" || (!(reason ?? "").isEmpty && reason != "user_cancelled") }
    public var missingTarget: Bool { spec.target.type == "resume_session" && ["source_session_missing", "execution_session_missing"].contains(reason ?? "") }
    public var oneShot: Bool { spec.trigger.type == "at" || (spec.trigger.type == "after_turn" && spec.trigger.repeatAfterTurn != true) }
    public func stateText(online: Bool, now: Date = Date(), zone: TimeZone = .current) -> String {
        guard online else { return scheduleOffline }
        if let active, ["dispatching", "queued", "running"].contains(active.status) { return "运行中" }
        switch group {
        case .attention: return "已停下"
        case .paused: return "已暂停"
        case .ended: return "已结束"
        case .active:
            guard let nextRunAt, let d = scheduleDate(nextRunAt) else { return "下次待定" }
            return "下次 " + scheduleMoment(d, now: now, zone: zone)
        }
    }
    public func lastResult(now: Date = Date(), zone: TimeZone = .current) -> String {
        if group == .attention { return scheduleReason(reason) }
        let history = history
        var cal = Calendar(identifier: .gregorian); cal.timeZone = zone
        let today = history.filter { cal.isDate($0.date, inSameDayAs: now) }
        if today.count > 1 && today.allSatisfy(\.silent) && group != .attention {
            return "今天 \(today.count) 次，都没有通知"
        }
        guard let last = history.first else { return group == .attention ? scheduleReason(reason) : "还没有运行" }
        let abnormal = ["failed", "skipped", "uncertain", "interrupted", "cancelled"].contains(last.status)
        let detail = (group == .attention && !(reason ?? "").isEmpty) ? scheduleReason(reason) : (last.notified || abnormal ? last.detail : "")
        let result = last.result
        return scheduleMoment(last.date, now: now, zone: zone) + " " + result + (detail.isEmpty ? "" : "：" + detail)
    }
}
public enum ScheduleGroup: Int, CaseIterable, Sendable {
    case attention, active, paused, ended
    public var title: String { ["需要你看一下", "进行中", "已暂停", "已结束"][rawValue] }
}
public struct MachineSchedule: Equatable, Sendable, Identifiable {
    public var machineID: String
    public var task: ScheduleTask
    public var id: String { machineID + "/" + task.id }
    public init(machineID: String, task: ScheduleTask) { self.machineID = machineID; self.task = task }
}
/// Inbox grouping is separate from the task's execution state.
public enum ScheduleInboxGroup: Int, CaseIterable, Sendable {
    case attention, recent, ended
    public var title: String { ["需要你看一下", "最近", "已结束"][rawValue] }
}
public func scheduleInbox(_ tasks: [MachineSchedule], group: ScheduleInboxGroup, hasNew: (MachineSchedule) -> Bool) -> [MachineSchedule] {
    tasks.filter {
        guard $0.task.visible else { return false }
        let actual: ScheduleInboxGroup = $0.task.group == .attention ? .attention :
            ($0.task.group == .ended && !hasNew($0) ? .ended : .recent)
        return actual == group
    }.sorted {
        let l = $0.task, r = $1.task
        if group == .attention {
            return l.stoppedAt == r.stoppedAt ? $0.id < $1.id : l.stoppedAt > r.stoppedAt
        }
        let ln = l.latestNotification, rn = r.latestNotification
        if (ln != nil) != (rn != nil) { return ln != nil }
        if let ln, let rn, ln.notificationTime != rn.notificationTime { return ln.notificationTime > rn.notificationTime }
        if group == .ended {
            let ld = scheduleDate(l.updatedAt ?? l.createdAt) ?? .distantPast
            let rd = scheduleDate(r.updatedAt ?? r.createdAt) ?? .distantPast
            return ld == rd ? $0.id < $1.id : ld > rd
        }
        let ld = l.nextRunAt.flatMap(scheduleDate) ?? .distantFuture
        let rd = r.nextRunAt.flatMap(scheduleDate) ?? .distantFuture
        return ld == rd ? $0.id < $1.id : ld < rd
    }
}
extension ScheduleTask {
    public var stoppedAt: Date {
        history.first?.notificationTime ?? updatedAt.flatMap(scheduleDate) ?? .distantPast
    }
    public var running: ScheduleRun? {
        active.flatMap { ["dispatching", "queued", "running"].contains($0.status) ? $0 : nil }
    }
    public func earlierRuns(resultID: String?) -> [ScheduleRun] {
        let result = history.first { $0.id == resultID }
        return history.filter { run in
            run.id != resultID && run.id != running?.id &&
                (!run.notified || result == nil || run.notificationTime <= result!.notificationTime)
        }
    }
    public var awaitingFirstResult: Bool { running != nil && latestNotification == nil }
    public var attentionWord: String { oneShot && status == "failed" ? "没能运行" : "已停下" }
    public func listPreview(now: Date = Date(), zone: TimeZone = .current) -> String {
        if awaitingFirstResult { return "还没有结果" }
        if history.isEmpty && group != .attention {
            return "还没有运行" + (nextRunAt.flatMap(scheduleDate).map { " · 下次 " + scheduleMoment($0, now: now, zone: zone) } ?? "")
        }
        return notificationPreview(now: now, zone: zone)
    }
}
/// Only schedule rows use this clock; conversation clocks retain their own format.
public func scheduleRowTime(_ date: Date, now: Date = Date(), zone: TimeZone = .current) -> String {
    var cal = Calendar(identifier: .gregorian); cal.timeZone = zone
    if cal.isDate(date, inSameDayAs: now) { return scheduleTime(date, zone: zone) }
    let days = cal.dateComponents([.day], from: cal.startOfDay(for: date), to: cal.startOfDay(for: now)).day ?? 0
    if days == 1 { return "昨天" }
    if (2..<7).contains(days) {
        return ["周日", "周一", "周二", "周三", "周四", "周五", "周六"][cal.component(.weekday, from: date) - 1]
    }
    return scheduleDay(date, now: now, zone: zone)
}
public func scheduleListFrequency(_ trigger: ScheduleTrigger, zone: TimeZone = .current, now: Date = Date()) -> String {
    let name = canonicalScheduleZone(trigger.timezone ?? zone.identifier)
    let taskZone = TimeZone(identifier: name) ?? zone
    let frequency = scheduleFrequency(trigger, zone: taskZone, now: now)
    guard name != canonicalScheduleZone(zone.identifier) else { return frequency }
    let cities = ["Asia/Shanghai": "北京", "Asia/Hong_Kong": "香港", "Asia/Tokyo": "东京", "Asia/Singapore": "新加坡",
                  "America/Los_Angeles": "洛杉矶", "America/New_York": "纽约", "Europe/London": "伦敦", "Europe/Berlin": "柏林", "Australia/Sydney": "悉尼"]
    let city = cities[name] ?? name.split(separator: "/").last.map { String($0).replacingOccurrences(of: "_", with: " ") } ?? name
    return frequency + "（" + city + "时间）"
}
public func scheduleRunSummary(_ run: ScheduleRun) -> String {
    for text in [run.notificationText, run.finalSummary] {
        if let text, !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { return text }
    }
    return (run.reason ?? "").isEmpty ? run.result : scheduleReason(run.reason)
}
/// Match the daemon's follow-up boundary: human input/resume, another scheduled
/// user message or an exited session closes this run. Never borrow a later reply.
public func scheduleFinalAnswer<S: Sequence>(_ events: S, runID: String) -> String? where S.Element == PBEvent {
    var found = false
    var answer: String?
    for event in events {
        if !found {
            if case .userMessage = event.kind, event.origin.scheduledRunID == runID { found = true }
            continue
        }
        let human = event.origin.kind == .client || event.origin.kind == .local
        if case .sessionState(let state) = event.kind {
            if state.status == .exited || (human && state.detail == "resumed") { break }
        }
        if case .userMessage = event.kind,
           human || (!event.origin.scheduledRunID.isEmpty && event.origin.scheduledRunID != runID) { break }
        if case .agentMessage(let message) = event.kind, message.phase == "final_answer" { answer = message.text }
    }
    return answer.flatMap { $0.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? nil : $0 }
}
public func scheduleTitle(_ typed: String, message: String) -> String {
    func clean(_ s: String) -> String { s.split(whereSeparator: \.isWhitespace).joined(separator: " ") }
    let entered = clean(typed)
    let line = Array((message.trimmingCharacters(in: .whitespacesAndNewlines).components(separatedBy: .newlines).first ?? ""))
    let end = line.indices.first { i in
        "。？！!?".contains(line[i]) || (line[i] == "." && (i == line.count - 1 || line[i + 1].isWhitespace))
    } ?? line.count
    let sentence = String(line.prefix(end))
    let first = sentence.isEmpty ? message.trimmingCharacters(in: .whitespacesAndNewlines) : sentence
    let fallback = String(clean(first).reversed().drop(while: { "，,:：;；".contains($0) }).reversed())
    return String(String.UnicodeScalarView((entered.isEmpty ? fallback : entered).unicodeScalars.prefix(60)))
}
public func scheduleDate(_ text: String) -> Date? {
    let f = ISO8601DateFormatter(); f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    if let d = f.date(from: text) { return d }
    f.formatOptions = [.withInternetDateTime]; return f.date(from: text)
}
public func scheduleISO(_ date: Date) -> String { ISO8601DateFormatter().string(from: date) }
public func scheduleTime(_ date: Date, zone: TimeZone = .current) -> String {
    let f = DateFormatter(); f.locale = Locale(identifier: "zh_CN"); f.timeZone = zone; f.dateFormat = "HH:mm"; return f.string(from: date)
}
public func scheduleDay(_ date: Date, now: Date = Date(), zone: TimeZone = .current) -> String {
    var cal = Calendar(identifier: .gregorian); cal.timeZone = zone
    if cal.isDate(date, inSameDayAs: now) { return "今天" }
    if let tomorrow = cal.date(byAdding: .day, value: 1, to: now), cal.isDate(date, inSameDayAs: tomorrow) { return "明天" }
    if let yesterday = cal.date(byAdding: .day, value: -1, to: now), cal.isDate(date, inSameDayAs: yesterday) { return "昨天" }
    let f = DateFormatter(); f.locale = Locale(identifier: "zh_CN"); f.calendar = cal; f.timeZone = zone
    f.dateFormat = cal.component(.year, from: date) == cal.component(.year, from: now) ? "M月d日" : "yyyy年M月d日"
    return f.string(from: date)
}
public func scheduleMoment(_ date: Date, now: Date = Date(), zone: TimeZone = .current) -> String {
    scheduleDay(date, now: now, zone: zone) + " " + scheduleTime(date, zone: zone)
}

public func canonicalScheduleZone(_ name: String) -> String {
    scheduleZoneAliases[name] ?? name
}

public func scheduleFrequency(_ t: ScheduleTrigger, zone: TimeZone = .current, includeZone: Bool = false, now: Date = Date()) -> String {
    switch t.type {
    case "at": return t.at.flatMap(scheduleDate).map { scheduleMoment($0, now: now, zone: zone) + "，一次" } ?? "一次"
    case "interval":
        let n = t.intervalSeconds ?? 0
        if n == 3600 { return "每小时" }
        if n % 86400 == 0 { return "每 \(n / 86400) 天" }
        if n % 3600 == 0 { return "每 \(n / 3600) 小时" }
        if n % 60 == 0 { return "每 \(n / 60) 分钟" }
        return "每 \(n) 秒"
    case "after_turn": return (t.repeatAfterTurn == true ? "每轮" : "当前轮") + "结束 \(t.delaySeconds ?? 0) 秒后"
    default:
        let days = (t.weekdays ?? []).sorted()
        let day: String
        if days.isEmpty || days == Array(1...7) { day = "每天" }
        else if days == Array(1...5) { day = "工作日" }
        else { day = "每周" + days.compactMap { (1...7).contains($0) ? ["一", "二", "三", "四", "五", "六", "日"][$0 - 1] : nil }.joined(separator: "、") }
        let tz = t.timezone ?? zone.identifier
        return day + " " + (t.time ?? "08:00") + (includeZone && canonicalScheduleZone(tz) != canonicalScheduleZone(zone.identifier) ? " · " + tz : "")
    }
}
public func scheduleReason(_ reason: String?) -> String {
    let r = reason ?? ""
    let known = [
        "relay_session_limit": "中继上的会话数已满", "execution_failed": "运行失败，任务已停下",
        "execution_interrupted": "运行被中断，任务已停下", "execution_session_exited": "运行中的对话已结束",
        "execution_session_missing": "找不到运行的对话", "message_delivery_failed": "消息没有送达 CLI",
        "could_not_queue_message": "消息没能加入待发队列", "message_cancelled": "定时消息在送达前被取消，任务已停下",
        "execution_input_paused": "定时消息已暂停，请先在对话里恢复或取消这条消息", "restart_dispatching": "电脑重启前的发送结果无法确认",
        "agent_schedule_permissions_changed": "创建任务时的权限已改变，任务已停下",
        "agent_schedule_interval_below_minimum": "Agent 创建的任务间隔过短，任务已停下", "completed_while_paused": "已暂停：期间计划已到期", "catch_up_window_expired": "已超过补跑时间，本次跳过"
    ]
    return known[r] ?? (r.isEmpty ? "运行结果无法确认" : "电脑返回的原因：" + r)
}
public struct ScheduleHistoryGroup: Identifiable, Equatable, Sendable {
    public var id: String
    public var runs: [ScheduleRun]
    public var folded: Bool { runs.count > 1 && runs.allSatisfy(\.silent) }
}
public func scheduleHistoryGroups(_ runs: [ScheduleRun], zone: TimeZone = .current) -> [ScheduleHistoryGroup] {
    var cal = Calendar(identifier: .gregorian); cal.timeZone = zone
    var out = [ScheduleHistoryGroup]()
    for r in runs.sorted(by: { $0.notificationTime > $1.notificationTime }) {
        if r.silent, let i = out.firstIndex(where: { $0.runs.allSatisfy(\.silent) && cal.isDate($0.runs[0].notificationTime, inSameDayAs: r.notificationTime) }) {
            out[i].runs.append(r)
        } else { out.append(.init(id: r.id, runs: [r])) }
    }
    return out
}
public func sessionSchedules(_ tasks: [MachineSchedule], session: SessionView) -> [MachineSchedule] {
    tasks.filter { $0.machineID == session.machineId && $0.task.spec.target.type == "resume_session" && $0.task.spec.target.session == session.id && $0.task.group != .ended }
}
public func scheduleBarText(_ tasks: [MachineSchedule], online: Bool, now: Date = Date(), zone: TimeZone = .current) -> String {
    guard let first = tasks.sorted(by: { ($0.task.nextRunAt ?? "~") < ($1.task.nextRunAt ?? "~") }).first else { return "" }
    let prefix = tasks.count == 1 ? "定时 · " + scheduleFrequency(first.task.spec.trigger, zone: zone, now: now) : "\(tasks.count) 个定时"
    return prefix + (tasks.count == 1 ? "，" : " · ") + first.task.stateText(online: online, now: now, zone: zone)
}

public let scheduleMessageLimit = 32 * 1024
public func scheduleMessageValid(_ message: String) -> Bool {
    !message.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && message.utf8.count <= scheduleMessageLimit
}
public let scheduleOfflineRow = "未连接"
public let scheduleOffline = "未连接，显示的是上次的状态"
public let scheduleEmpty = "在对话里跟 agent 说「每天 8 点帮我看看…」，它会替你建好；也可以点右上角的 ＋ 自己建。"
public let scheduleIntervalHint = "第一次会在一个完整间隔后运行，想现在执行可以创建后点“立即运行一次”。"
public func scheduleLimits(_ trigger: ScheduleTrigger, now: Date = Date(), zone: TimeZone = .current) -> String {
    [trigger.maxRuns.flatMap { $0 > 0 ? "共 \($0) 次" : nil }, trigger.until.flatMap(scheduleDate).map { "到 " + scheduleMoment($0, now: now, zone: zone) }].compactMap { $0 }.joined(separator: "，或")
}
