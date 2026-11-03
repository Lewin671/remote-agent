import Foundation
import RAProto
import Testing

@testable import RACore

/// `silentScheduledGroups` as it was before it went incremental: every pass
/// read the whole history. The incremental fold must answer exactly as this.
private struct LegacyRound { var start: UInt64; var time: Int64; var run: String; var safe = true; var toolsThrough: UInt64 = 0 }
private func legacyGroups(_ events: some Sequence<PBEvent>, zone: TimeZone) -> [SilentScheduledGroup] {
    let history = Array(events)
    let derived = derive(history)
    let unsafeRuns = Set(history.compactMap { event -> String? in
        switch event.kind {
        case .scheduledNotification(let n): return n.runID.isEmpty ? nil : n.runID
        case .turnEnded(let end) where end.outcome != .completed || end.scheduledNotified:
            return end.scheduledRunID.isEmpty ? nil : end.scheduledRunID
        default: return nil
        }
    })
    var round: LegacyRound?
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
                round = LegacyRound(start: e.seq, time: eventTime(e), run: e.origin.scheduledRunID, safe: !unsafeRuns.contains(e.origin.scheduledRunID))
            } else { round = nil; finish() }
        }
        if round != nil {
            if isSkeleton(e) { round?.safe = false }
            switch e.kind {
            case .scheduledNotification, .approvalRequested, .questionAsked, .questionAnswered, .settingsChanged, .planProposed, .imageGenerated, .sessionForked:
                round?.safe = false
            case .toolCall(let t): if !toolOk(t) || wentToBackground(t) { round?.safe = false }
            case .toolStarted:
                if derived.completedAt[e.seq] == nil { round?.safe = false }
                let through = max(round?.toolsThrough ?? 0, derived.completedAt[e.seq] ?? 0)
                round?.toolsThrough = through
            case .turnContext: if derived.terminalChanges[e.seq] != nil { round?.safe = false }
            case .inputState(let input):
                if input.paused || [.failed, .needsTerminal, .unconfirmed, .canceled, .heldByCli].contains(input.status) { round?.safe = false }
            case .sessionState(let state): if state.status == .exited || state.status == .blocked { round?.safe = false }
            case .turnEnded(let end):
                if let r = round, r.safe, r.toolsThrough <= e.seq, end.outcome == .completed, !end.scheduledNotified, end.scheduledRunID == r.run {
                    streak.append(.init(firstSeq: r.start, lastSeq: e.seq, times: [r.time]))
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
    finish()
    return out
}

/// A fixed sequence of pseudo-random numbers, the same on every run.
private struct SplitMix {
    var state: UInt64
    mutating func next(_ n: Int) -> Int {
        state &+= 0x9E37_79B9_7F4A_7C15
        var z = state
        z = (z ^ (z >> 30)) &* 0xBF58_476D_1CE4_E5B9
        z = (z ^ (z >> 27)) &* 0x94D0_49BB_1331_11EB
        return Int((z ^ (z >> 31)) % UInt64(n))
    }
}

/// Scheduled rounds, mostly silent, mixed with what keeps them visible: human
/// replies, failures, interrupts, notifications (some after their round),
/// late tool results, terminal setting changes, unfinished rounds and new days.
private func history(seed: UInt64, rounds: Int) -> [PBEvent] {
    var rng = SplitMix(state: seed)
    var out = [PBEvent]()
    var time: Int64 = 1_793_000_000_000
    var runs = [String]()
    var lateCalls = [String]()
    func add(_ change: (inout PBEvent) -> Void) {
        var e = PBEvent()
        e.seq = UInt64(out.count + 1)
        change(&e)
        out.append(e)
    }
    add { $0.turnContext.model = "model-a" }
    for i in 0..<rounds {
        time += rng.next(10) == 0 ? 86_400_000 : 3_600_000
        let run = "run-\(i)"
        runs.append(run)
        let human = rng.next(12) == 0
        add {
            $0.time.seconds = time / 1000
            $0.userMessage.text = human ? "看一下" : "检查"
            if !human { $0.origin.scheduledRunID = run }
        }
        switch rng.next(14) {
        case 0: add { $0.toolStarted.callID = "c\(i)"; $0.toolStarted.title = "check" }
            add { $0.toolCall.callID = "c\(i)"; $0.toolCall.status = "completed" }
        case 1: add { $0.toolStarted.callID = "c\(i)"; $0.toolStarted.title = "check" }
            lateCalls.append("c\(i)")
        case 2: add { $0.toolCall.status = "failed" }
        case 3: add { $0.scheduledNotification.runID = run; $0.scheduledNotification.text = "有变化" }
        case 4: add { $0.turnContext.model = "model-\(i)" }
        case 5: add { $0.agentMessage.text = skeletonNote }
        case 6: add { $0.inputState.requestID = "r\(i)"; $0.inputState.status = .failed }
        default: add { $0.agentMessage.text = "没有新回复" }
        }
        switch rng.next(16) {
        case 0: continue // never ends
        case 1: add { $0.turnEnded.outcome = .aborted; $0.turnEnded.scheduledRunID = run }
        case 2: add { $0.turnEnded.outcome = .completed; $0.turnEnded.scheduledRunID = run; $0.turnEnded.scheduledNotified = true }
        case 3: add { $0.turnEnded.outcome = .completed; $0.turnEnded.scheduledRunID = "other" }
        default: add { $0.turnEnded.outcome = .completed; $0.turnEnded.scheduledRunID = run }
        }
        switch rng.next(12) {
        case 0: if let id = lateCalls.popLast() { add { $0.toolCall.callID = id; $0.toolCall.status = "completed" } }
        case 1: let late = runs[rng.next(runs.count)]
            add { $0.scheduledNotification.runID = late; $0.scheduledNotification.text = "后来发现了变化" }
        case 2: add { $0.agentMessage.text = "自主继续的回复" }
        case 3: add { $0.tokenUsage.contextWindow = 100 }
        default: break
        }
    }
    return out
}

@Suite struct ScheduledFoldTests {
    let zone = TimeZone(identifier: "Asia/Shanghai")!

    @Test(arguments: [UInt64(1), 2, 3, 4, 5, 6, 7, 8])
    func growingHistoryFoldsExactlyAsAFullRead(seed: UInt64) {
        let all = history(seed: seed, rounds: 60)
        #expect(legacyGroups(all, zone: zone).count > 0, "the history folds somewhere")
        var rng = SplitMix(state: seed &* 31)
        var fold = ScheduledFold()
        var list = PagedList<PBEvent>()
        while list.count < all.count {
            let n = min(all.count - list.count, 1 + rng.next(4))
            list = list.appending(Array(all[list.count..<list.count + n]))
            let groups = fold.update(list, zone: zone)
            #expect(groups == legacyGroups(list, zone: zone), "after \(list.count) events")
            #expect(silentScheduledGroups(list, zone: zone) == groups)
        }
    }

    @Test func theSameHistoryAgainReadsNothing() {
        let all = PagedList(history(seed: 11, rounds: 40))
        var fold = ScheduledFold()
        let first = fold.update(all, zone: zone)
        let read = fold.stepped
        #expect(read == all.count && fold.replays == 1)
        for _ in 0..<5 { #expect(fold.update(all, zone: zone) == first) }
        #expect(fold.stepped == read && fold.replays == 1)
    }

    @Test func appendedEventsAreReadOnceWhileNoFoldedRunTurnsUnsafe() {
        let zone = zone
        func round(_ seq: UInt64, hour: Int64, run: String) -> [PBEvent] {
            var user = PBEvent(); user.seq = seq; user.time.seconds = 1_793_000_000 + hour * 3600
            user.userMessage.text = "检查"; user.origin.scheduledRunID = run
            var reply = PBEvent(); reply.seq = seq + 1; reply.agentMessage.text = "没有新回复"
            var end = PBEvent(); end.seq = seq + 2; end.turnEnded.outcome = .completed; end.turnEnded.scheduledRunID = run
            return [user, reply, end]
        }
        var fold = ScheduledFold()
        var list = PagedList(round(1, hour: 0, run: "a"))
        _ = fold.update(list, zone: zone)
        for i in 1..<6 {
            list = list.appending(round(UInt64(3 * i + 1), hour: Int64(i), run: "r\(i)"))
            let groups = fold.update(list, zone: zone)
            #expect(groups == [SilentScheduledGroup(firstSeq: 1, lastSeq: UInt64(3 * i + 3), times: groups.first?.times ?? [])])
            #expect(groups.first?.times.count == i + 1)
        }
        #expect(fold.stepped == list.count && fold.replays == 1)
        // A notification for a run already folded: everything before is read again, and the run shows.
        var late = PBEvent(); late.seq = UInt64(list.count + 1); late.scheduledNotification.runID = "r3"
        list = list.appending([late])
        let after = fold.update(list, zone: zone)
        #expect(after == legacyGroups(list, zone: zone))
        #expect(after.map(\.lastSeq) == [9, 18])
        #expect(fold.replays == 2)
        // A notification for a run that is still going needs no second read.
        var open = round(UInt64(list.count + 1), hour: 7, run: "open")
        open.removeLast()
        list = list.appending(open)
        _ = fold.update(list, zone: zone)
        var notice = PBEvent(); notice.seq = UInt64(list.count + 1); notice.scheduledNotification.runID = "open"
        list = list.appending([notice])
        #expect(fold.update(list, zone: zone) == legacyGroups(list, zone: zone))
        #expect(fold.replays == 2)
    }

    @Test func anotherHistoryOrZoneIsReadAgain() {
        let all = history(seed: 5, rounds: 30)
        let base = PagedList(Array(all.prefix(40)))
        var fold = ScheduledFold()
        _ = fold.update(base.appending(Array(all[40..<60])), zone: zone)
        // A sibling with another tail is not a continuation.
        var other = all[40]
        other.agentMessage.text = "另一段"
        let sibling = base.appending([other] + Array(all[41..<60]))
        #expect(fold.update(sibling, zone: zone) == legacyGroups(sibling, zone: zone))
        #expect(fold.replays == 2)
        let utc = TimeZone(identifier: "UTC")!
        #expect(fold.update(sibling, zone: utc) == legacyGroups(sibling, zone: utc))
        #expect(fold.replays == 3)
    }
}
