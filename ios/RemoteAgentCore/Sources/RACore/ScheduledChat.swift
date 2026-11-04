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
public func silentScheduledGroups(_ events: some Sequence<PBEvent>, zone: TimeZone = .current) -> [SilentScheduledGroup] {
    var fold = ScheduledFold()
    return fold.update(PagedList(events), zone: zone)
}

/// `silentScheduledGroups` as the history grows: while what is given only
/// adds to what was given before, only the new events are read; the same
/// history again costs nothing. A later event that makes an already folded
/// run unsafe (a notification or a failed end for it), another history or
/// another zone reads everything again.
public struct ScheduledFold: Sendable {
    private struct Round: Sendable {
        var start: UInt64
        var time: Int64
        var run: String
        var safe = true
        /// The round's tool starts; it folds only when each completed before it ended.
        var tools: [UInt64] = []
    }

    private var previous = PagedList<PBEvent>()
    private var zone: TimeZone?
    private var cal = Calendar(identifier: .gregorian)
    private var reducer = EventReducer(agent: defaultAgent)
    /// Runs a notification or an end other than a silent completion names, in what was read.
    private var unsafeRuns = Set<String>()
    /// Runs of rounds that joined a streak: one becoming unsafe changes what came before.
    private var foldedRuns = Set<String>()
    private var round: Round?
    private var streak = [SilentScheduledGroup]()
    private var out = [SilentScheduledGroup]()
    private var groups = [SilentScheduledGroup]()
    /// Events read, and passes that read from the first event: what the work cost.
    private(set) var stepped = 0
    private(set) var replays = 0

    public init() {}

    public mutating func update(_ events: PagedList<PBEvent>, zone: TimeZone = .current) -> [SilentScheduledGroup] {
        var append = self.zone == zone && events.extends(previous)
        if append && events.count == previous.count { return groups }
        if append {
            let runs = Self.unsafeRuns(events[previous.count...])
            if runs.isDisjoint(with: foldedRuns) {
                if let r = round, runs.contains(r.run) { round?.safe = false }
                unsafeRuns.formUnion(runs)
                for e in events[previous.count...] { step(e) }
            } else {
                append = false
            }
        }
        if !append {
            let (stepped, replays) = (self.stepped, self.replays)
            self = ScheduledFold()
            self.stepped = stepped
            self.replays = replays + 1
            self.zone = zone
            cal.timeZone = zone
            unsafeRuns = Self.unsafeRuns(events)
            for e in events { step(e) }
        }
        previous = events
        groups = out
        if streak.count >= 2, let merged = merged() { groups.append(merged) }
        return groups
    }

    private static func unsafeRuns(_ events: some Sequence<PBEvent>) -> Set<String> {
        Set(events.compactMap { event -> String? in
            switch event.kind {
            case .scheduledNotification(let n): return n.runID.isEmpty ? nil : n.runID
            case .turnEnded(let end) where end.outcome != .completed || end.scheduledNotified:
                return end.scheduledRunID.isEmpty ? nil : end.scheduledRunID
            default: return nil
            }
        })
    }

    private func merged() -> SilentScheduledGroup? {
        guard let first = streak.first, let last = streak.last else { return nil }
        return .init(firstSeq: first.firstSeq, lastSeq: last.lastSeq, times: streak.flatMap(\.times))
    }

    private mutating func finish() {
        if streak.count >= 2, let merged = merged() { out.append(merged) }
        streak.removeAll()
    }

    private mutating func step(_ e: PBEvent) {
        stepped += 1
        // What the event itself completes or changes counts from here on.
        reducer.append(CollectionOfOne(e))
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
            case .scheduledNotification, .approvalRequested, .questionAsked, .questionAnswered, .settingsChanged, .planProposed, .imageGenerated, .sessionForked:
                round?.safe = false
            case .toolCall(let t): if !toolOk(t) || wentToBackground(t) { round?.safe = false }
            case .toolStarted: round?.tools.append(e.seq)
            case .turnContext: if reducer.changedInTerminal(e.seq) { round?.safe = false }
            case .inputState(let input):
                if input.paused || [.failed, .needsTerminal, .unconfirmed, .canceled, .heldByCli].contains(input.status) { round?.safe = false }
            case .sessionState(let state): if state.status == .exited || state.status == .blocked { round?.safe = false }
            case .turnEnded(let end):
                // A start completes once, so one not completed by now never completes before this end.
                if let r = round, r.safe, r.tools.allSatisfy({ reducer.completed($0) }), end.outcome == .completed, !end.scheduledNotified, end.scheduledRunID == r.run {
                    streak.append(.init(firstSeq: r.start, lastSeq: e.seq, times: [r.time]))
                    foldedRuns.insert(r.run)
                } else { finish() }
                round = nil
            default: break
            }
        } else {
            switch e.kind {
            case .agentMessage, .reasoning, .toolStarted, .toolCall, .scheduledNotification, .approvalRequested, .questionAsked, .questionAnswered, .planProposed, .imageGenerated, .sessionForked, .settingsChanged, .turnContext, .turnEnded:
                finish()
            case .inputState(let input):
                if input.paused || [.failed, .needsTerminal, .unconfirmed, .canceled, .heldByCli].contains(input.status) { finish() }
            case .sessionState(let state) where state.status == .exited:
                finish()
            default: break
            }
        }
    }
}
