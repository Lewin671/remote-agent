import Foundation
import RAProto

public struct SilentScheduledGroup: Equatable, Sendable, Identifiable {
    public var firstSeq: UInt64
    public var lastSeq: UInt64
    public var times: [Int64]
    public var id: String { "scheduled-\(firstSeq)" }
    public var text: String { times.map { scheduleTime(Date(timeIntervalSince1970: Double($0) / 1000)) }.joined(separator: "、") + " 定时检查 · 没有新回复" }
}
/// Only explicit, completed scheduled rounds fold. Content outside those boundaries stays visible,
/// including autonomous followups, a later notification and oversized-content placeholders.
private struct Round { var start: UInt64; var time: Int64; var run: String; var safe = true }
public func silentScheduledGroups(_ events: some Sequence<PBEvent>, zone: TimeZone = .current) -> [SilentScheduledGroup] {
    let history = Array(events)
    let unsafeRuns = Set(history.compactMap { event -> String? in
        switch event.kind {
        case .scheduledNotification(let n): return n.runID.isEmpty ? nil : n.runID
        case .turnEnded(let end) where end.outcome != .completed || end.scheduledNotified:
            return end.scheduledRunID.isEmpty ? nil : end.scheduledRunID
        default: return nil
        }
    })
    var round: Round?
    var streak = [SilentScheduledGroup]()
    var out = [SilentScheduledGroup]()
    var cal = Calendar(identifier: .gregorian); cal.timeZone = zone
    func finish() {
        if streak.count >= 2, let first = streak.first, let last = streak.last {
            out.append(.init(firstSeq: first.firstSeq, lastSeq: last.lastSeq, times: streak.flatMap(\.times)))
        }
        streak.removeAll()
    }
    for e in history {
        if case .userMessage = e.kind {
            if round != nil { finish() }
            if !e.origin.scheduledRunID.isEmpty && eventTime(e) > 0 {
                if let last = streak.last, !cal.isDate(Date(timeIntervalSince1970: Double(last.times[0]) / 1000), inSameDayAs: Date(timeIntervalSince1970: Double(eventTime(e)) / 1000)) { finish() }
                round = Round(start: e.seq, time: eventTime(e), run: e.origin.scheduledRunID, safe: !unsafeRuns.contains(e.origin.scheduledRunID))
            } else { round = nil; finish() }
        }
        if round != nil {
            if isSkeleton(e) { round?.safe = false }
            switch e.kind {
            case .scheduledNotification, .approvalRequested, .questionAsked, .planProposed, .imageGenerated, .sessionForked:
                round?.safe = false
            case .toolCall(let t): if !toolOk(t) { round?.safe = false }
            case .inputState(let input):
                if input.paused || [.failed, .needsTerminal, .unconfirmed, .canceled, .heldByCli].contains(input.status) { round?.safe = false }
            case .sessionState(let state): if state.status == .exited || state.status == .blocked { round?.safe = false }
            case .turnEnded(let end):
                if let r = round, r.safe, end.outcome == .completed, !end.scheduledNotified, end.scheduledRunID == r.run {
                    streak.append(.init(firstSeq: r.start, lastSeq: e.seq, times: [r.time]))
                } else { finish() }
                round = nil
            default: break
            }
        } else {
            switch e.kind {
            case .agentMessage, .reasoning, .toolStarted, .toolCall, .scheduledNotification, .approvalRequested, .questionAsked, .planProposed, .imageGenerated, .sessionForked:
                finish()
            default: break
            }
        }
    }
    finish()
    return out
}
