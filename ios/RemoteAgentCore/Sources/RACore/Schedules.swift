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
    public var foldedSummary: String { (finalSummary ?? "").isEmpty ? L10n.scheduleNoNotification() : finalSummary! }
    public var notificationLabel: String {
        switch notificationSource { case "agent": L10n.scheduleIosAgentNotification(); case "daemon": L10n.scheduleNotifyOnCompletion(); case "agent_and_daemon": L10n.scheduleIosAgentNotificationCompletionNotification(); case "undeliverable": L10n.scheduleIosCannotRequestNotification(); default: "" }
    }
    public var result: String {
        if notified && !["failed", "skipped", "uncertain", "interrupted", "cancelled"].contains(status) { return L10n.scheduleNotifiedYou() }
        switch status {
        case "failed": return L10n.attachmentFailed()
        case "skipped": return L10n.scheduleSkipped()
        case "uncertain": return L10n.scheduleUnconfirmed()
        case "interrupted": return L10n.sessionInterrupted()
        case "cancelled": return L10n.queueCanceled()
        case "dispatching": return L10n.scheduleStarting()
        case "queued": return L10n.scheduleWaitingForTheCurrentTurnToEnd()
        case "running": return L10n.scheduleRunning()
        case "completed": return notified ? L10n.scheduleNotifiedYou() : L10n.scheduleNoNotification()
        default: return L10n.scheduleWaitingForResults()
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
    public var creator: String { source == "agent" ? L10n.scheduleCreatedByTheAgentInAConversation() : L10n.scheduleCreatedByYou() }
    public var history: [ScheduleRun] { ((runs ?? []) + (active.map { [$0] } ?? [])).sorted { $0.date > $1.date } }
    public var latestNotification: ScheduleRun? {
        history.filter(\.notified).max { $0.notificationTime < $1.notificationTime }
    }
    public func notificationPreview(now: Date = Date(), zone: TimeZone = .current, shortReason: Bool = false) -> String {
        if group == .attention { return scheduleReason(reason, short: shortReason) }
        guard let run = latestNotification else { return lastResult(now: now, zone: zone) }
        // A stopped run shows its readable reason, as on web and Android, not the daemon's notice text.
        if ["failed", "skipped", "uncertain", "interrupted", "cancelled"].contains(run.status) { return scheduleReason(run.reason, short: shortReason) }
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
        if let active, ["dispatching", "queued", "running"].contains(active.status) { return L10n.listStateRunning() }
        switch group {
        case .attention: return L10n.scheduleStopped()
        case .paused: return L10n.queuePaused()
        case .ended: return L10n.listStateEnded()
        case .active:
            guard let nextRunAt, let d = scheduleDate(nextRunAt) else { return L10n.scheduleNextRunPending() }
            return L10n.scheduleNext(value: String(describing: scheduleMoment(d, now: now, zone: zone)))
        }
    }
    public func lastResult(now: Date = Date(), zone: TimeZone = .current) -> String {
        if group == .attention { return scheduleReason(reason) }
        let history = history
        var cal = Calendar(identifier: .gregorian); cal.timeZone = zone
        let today = history.filter { cal.isDate($0.date, inSameDayAs: now) }
        if today.count > 1 && today.allSatisfy(\.silent) && group != .attention {
            return L10n.scheduleRunsTodayNoneNotifiedYou(count: Int(today.count))
        }
        guard let last = history.first else { return group == .attention ? scheduleReason(reason) : L10n.scheduleHasNotRunYet() }
        let abnormal = ["failed", "skipped", "uncertain", "interrupted", "cancelled"].contains(last.status)
        let detail = (group == .attention && !(reason ?? "").isEmpty) ? scheduleReason(reason) : (last.notified || abnormal ? last.detail : "")
        let result = last.result
        let time = scheduleMoment(last.date, now: now, zone: zone)
        return detail.isEmpty ? L10n.scheduleLastResult(result: result, time: time) : L10n.scheduleLastResultDetail(detail: detail, result: result, time: time)
    }
}
public enum ScheduleGroup: Int, CaseIterable, Sendable {
    case attention, active, paused, ended
    public var title: String { [L10n.scheduleIosNeedsYourAttention(), L10n.homeLive(), L10n.queuePaused(), L10n.listStateEnded()][rawValue] }
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
    public var title: String { [L10n.scheduleIosNeedsYourAttention(), L10n.scheduleIosRecent(), L10n.listStateEnded()][rawValue] }
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
    public var attentionWord: String { oneShot && status == "failed" ? L10n.scheduleCouldNotRun() : L10n.scheduleStopped() }
    public func listPreview(now: Date = Date(), zone: TimeZone = .current) -> String {
        if awaitingFirstResult { return L10n.scheduleNoResultsYet() }
        if history.isEmpty && group != .attention {
            return nextRunAt.flatMap(scheduleDate).map { L10n.scheduleNoRunsNext(time: scheduleMoment($0, now: now, zone: zone)) } ?? L10n.scheduleHasNotRunYet()
        }
        return notificationPreview(now: now, zone: zone)
    }
}
/// Only schedule rows use this clock; conversation clocks retain their own format.
public func scheduleRowTime(_ date: Date, now: Date = Date(), zone: TimeZone = .current) -> String {
    var cal = Calendar(identifier: .gregorian); cal.timeZone = zone
    if cal.isDate(date, inSameDayAs: now) { return scheduleTime(date, zone: zone) }
    let days = cal.dateComponents([.day], from: cal.startOfDay(for: date), to: cal.startOfDay(for: now)).day ?? 0
    if days == 1 { return L10n.timeYesterday() }
    if (2..<7).contains(days) {
        return [L10n.timeWeekday(day: "sun"), L10n.timeWeekday(day: "mon"), L10n.timeWeekday(day: "tue"), L10n.timeWeekday(day: "wed"), L10n.timeWeekday(day: "thu"), L10n.timeWeekday(day: "fri"), L10n.timeWeekday(day: "sat")][cal.component(.weekday, from: date) - 1]
    }
    return scheduleDay(date, now: now, zone: zone)
}
public func scheduleListFrequency(_ trigger: ScheduleTrigger, zone: TimeZone = .current, now: Date = Date()) -> String {
    let name = canonicalScheduleZone(trigger.timezone ?? zone.identifier)
    let taskZone = TimeZone(identifier: name) ?? zone
    let frequency = scheduleFrequency(trigger, zone: taskZone, now: now)
    guard name != canonicalScheduleZone(zone.identifier) else { return frequency }
    let cities = ["Asia/Shanghai": L10n.scheduleIosBeijing(), "Asia/Hong_Kong": L10n.scheduleIosHongKong(), "Asia/Tokyo": L10n.scheduleIosTokyo(), "Asia/Singapore": L10n.scheduleIosSingapore(),
                  "America/Los_Angeles": L10n.scheduleIosLosAngeles(), "America/New_York": L10n.scheduleIosNewYork(), "Europe/London": L10n.scheduleIosLondon(), "Europe/Berlin": L10n.scheduleIosBerlin(), "Australia/Sydney": L10n.scheduleIosSydney()]
    let city = cities[name] ?? name.split(separator: "/").last.map { String($0).replacingOccurrences(of: "_", with: " ") } ?? name
    return L10n.scheduleIosTime(detail: String(describing: city), value: String(describing: frequency))
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
    let f = DateFormatter(); f.locale = Locale(identifier: MessageLocale.appleTags[MessageLocale.current]!); f.timeZone = zone; f.dateFormat = "HH:mm"; return f.string(from: date)
}
public func scheduleDay(_ date: Date, now: Date = Date(), zone: TimeZone = .current) -> String {
    var cal = Calendar(identifier: .gregorian); cal.timeZone = zone
    if cal.isDate(date, inSameDayAs: now) { return L10n.timeToday() }
    if let tomorrow = cal.date(byAdding: .day, value: 1, to: now), cal.isDate(date, inSameDayAs: tomorrow) { return L10n.timeTomorrow() }
    if let yesterday = cal.date(byAdding: .day, value: -1, to: now), cal.isDate(date, inSameDayAs: yesterday) { return L10n.timeYesterday() }
    let f = DateFormatter(); f.locale = Locale(identifier: MessageLocale.appleTags[MessageLocale.current]!); f.calendar = cal; f.timeZone = zone
    f.dateFormat = cal.component(.year, from: date) == cal.component(.year, from: now) ? L10n.scheduleIosMmmD() : L10n.scheduleIosMmmDYyyy()
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
    case "at": return t.at.flatMap(scheduleDate).map { L10n.scheduleOnceAt(time: String(describing: scheduleMoment($0, now: now, zone: zone))) } ?? L10n.scheduleOnceSchedules()
    case "interval":
        let n = t.intervalSeconds ?? 0
        if n == 3600 { return L10n.scheduleHourly() }
        if n % 86400 == 0 { return L10n.scheduleEveryDays(count: Int(n / 86400)) }
        if n % 3600 == 0 { return L10n.scheduleEveryHr(count: Int(n / 3600)) }
        if n % 60 == 0 { return L10n.scheduleEveryMin(count: Int(n / 60)) }
        return L10n.scheduleEverySec(count: Int(n))
    case "after_turn": return t.repeatAfterTurn == true ? L10n.scheduleAfterEachTurn(count: Int(t.delaySeconds ?? 0)) : L10n.scheduleAfterCurrentTurn(count: Int(t.delaySeconds ?? 0))
    default:
        let days = (t.weekdays ?? []).sorted()
        let frequency: String
        let time = t.time ?? "08:00"
        if days.isEmpty || days == Array(1...7) { frequency = L10n.scheduleDailyAt(time: time) }
        else if days == Array(1...5) { frequency = L10n.scheduleWorkdaysAt(time: time) }
        else {
            let names = days.compactMap { (1...7).contains($0) ? [L10n.scheduleMon(), L10n.scheduleTue(), L10n.scheduleWed(), L10n.scheduleThu(), L10n.scheduleFri(), L10n.scheduleSat(), L10n.scheduleSun()][$0 - 1] : nil }.joined(separator: L10n.commonIosListSeparator())
            frequency = L10n.scheduleWeeklyAt(days: names, time: time)
        }
        let tz = t.timezone ?? zone.identifier
        return frequency + (includeZone && canonicalScheduleZone(tz) != canonicalScheduleZone(zone.identifier) ? " · " + tz : "")
    }
}
public func scheduleReason(_ reason: String?, short: Bool = false) -> String {
    if short {
        switch reason {
        case "execution_failed": return L10n.scheduleExecutionFailed()
        case "execution_interrupted": return L10n.scheduleExecutionInterrupted()
        case "message_cancelled": return L10n.scheduleTheScheduledMessageWasCanceledBeforeDelivery()
        case "agent_schedule_permissions_changed": return L10n.scheduleTaskCreationPermissionsChanged()
        case "agent_schedule_interval_below_minimum": return L10n.scheduleTheAgentCreatedTaskIntervalIsTooShort()
        default: break
        }
    }
    let r = reason ?? ""
    let known = [
        "relay_session_limit": L10n.scheduleTheRelaySSessionLimitHasBeenReached(), "execution_failed": L10n.scheduleExecutionFailedTheTaskStopped(),
        "execution_interrupted": L10n.scheduleExecutionInterruptedTheTaskStopped(), "execution_session_exited": L10n.scheduleTheRunningConversationEnded(),
        "execution_session_missing": L10n.scheduleTheRunningConversationCouldNotBeFound(), "message_delivery_failed": L10n.scheduleTheMessageWasNotDeliveredToTheCli(),
        "could_not_queue_message": L10n.scheduleTheMessageCouldNotBeQueued(), "message_cancelled": L10n.scheduleTheScheduledMessageWasCanceledBeforeDeliveryTheTask(),
        "execution_input_paused": L10n.scheduleTheScheduledMessageIsPausedResumeOrCancelIt(), "restart_dispatching": L10n.scheduleDeliveryBeforeTheComputerRestartedCouldNotBeConfirmed(),
        "agent_schedule_permissions_changed": L10n.scheduleTaskCreationPermissionsChangedTheTaskStopped(),
        "agent_schedule_interval_below_minimum": L10n.scheduleTheAgentCreatedTaskIntervalIsTooShortThe(), "completed_while_paused": L10n.schedulePausedTheScheduleExpiredInTheMeantime(), "catch_up_window_expired": L10n.scheduleTheCatchUpWindowExpiredThisRunWasSkipped()
    ]
    return known[r] ?? (r.isEmpty ? L10n.scheduleExecutionOutcomeIsUnconfirmed() : L10n.scheduleReasonReturnedByTheComputer(value: String(describing: r)))
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
    let state = first.task.stateText(online: online, now: now, zone: zone)
    return tasks.count == 1
        ? L10n.scheduleStripSingle(frequency: scheduleFrequency(first.task.spec.trigger, zone: zone, now: now), state: state)
        : L10n.scheduleStripCount(count: tasks.count, state: state)
}

public let scheduleMessageLimit = 32 * 1024
public func scheduleMessageValid(_ message: String) -> Bool {
    !message.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && message.utf8.count <= scheduleMessageLimit
}
public var scheduleOfflineRow: String { L10n.listStateUnlinked() }
public var scheduleOffline: String { L10n.scheduleNotConnectedShowingThePreviousState() }
public var scheduleEmpty: String { L10n.scheduleTellTheAgentInAConversationCheckForMe() }
public var scheduleIntervalHint: String { L10n.scheduleTheFirstRunIsAfterAFullIntervalTo() }
public func scheduleLimits(_ trigger: ScheduleTrigger, now: Date = Date(), zone: TimeZone = .current) -> String {
    let count = trigger.maxRuns.flatMap { $0 > 0 ? Int($0) : nil }
    let time = trigger.until.flatMap(scheduleDate).map { scheduleMoment($0, now: now, zone: zone) }
    if let count, let time { return L10n.scheduleLimitsBoth(count: count, time: time) }
    if let count { return L10n.scheduleRunsTotal(count: count) }
    return time.map { L10n.scheduleUntil(value: $0) } ?? ""
}

/// The 定时 page's sentence (design-system §6 「大标题」「定时列表」): how many
/// tasks stopped, else how many have new results, and the other kind
/// counted under it. `count` is the number drawn orange in the title.
public struct ScheduleHeadline: Equatable, Sendable {
    public var title: String
    public var count: Int?
    public var sub: String
}

public func scheduleHeadline(stopped: Int, fresh: Int, visible: Int, loading: Bool) -> ScheduleHeadline {
    if visible == 0 { return ScheduleHeadline(title: loading ? L10n.scheduleReadingTasks() : L10n.scheduleNoScheduledTasksYet(), count: nil, sub: "") }
    if stopped > 0 {
        return ScheduleHeadline(title: L10n.scheduleStoppedCount(count: Int(stopped), total: String(describing: stopped)), count: stopped, sub: fresh > 0 ? L10n.scheduleMoreNewResults(count: Int(fresh)) : "")
    }
    return ScheduleHeadline(title: fresh > 0 ? L10n.scheduleNewResults(count: Int(fresh)) : L10n.scheduleNoNewResults(), count: nil, sub: "")
}
