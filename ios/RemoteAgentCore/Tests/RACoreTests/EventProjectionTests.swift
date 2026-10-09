import Foundation
import RAProto
import Testing

@testable import RACore

/// Android's core EventProjectionTest. Kotlin compares the fields of two
/// Derived objects by reflection; here Derived is a value and == does it.
@Suite struct EventProjectionTests {
    private func ev(_ seq: UInt64, _ kind: PBEvent.OneOf_Kind) -> PBEvent {
        var e = Mk.ev(seq, kind)
        e.sessionID = "s"
        return e
    }

    @Test func incrementalResultsMatchReplayAcrossLateResultsAndCliChanges() {
        let history = [
            ev(1, Mk.turnStarted),
            ev(2, Mk.inputState("i", .queued) { $0.text = "hello" }),
            ev(3, Mk.user("hello")),
            ev(4, Mk.inputState("i", .submitted)),
            ev(5, .toolStarted(.with { $0.callID = "c"; $0.title = "sleep" })),
            ev(6, Mk.turnEnded(.aborted)),
            ev(7, .toolCall(.with { $0.callID = "c"; $0.status = "completed" })),
            ev(8, .approvalRequested(.with { $0.approvalID = "a" })),
            ev(9, .approvalResolved(.with { $0.approvalID = "a"; $0.decision = .allow })),
            ev(10, Mk.exited),
        ]
        var p = EventProjection()
        for agent in [PBAgent.codex, .claude] {
            for n in 0...history.count {
                let prefix = Array(history.prefix(n))
                let result = p.update(prefix, agent: agent)
                #expect(result == derive(prefix, agent: agent))
                #expect(p.update(prefix, agent: agent) == result)
            }
        }
        #expect(p.update(Array(history.prefix(3))) == derive(history.prefix(3)))
        var replaced = history
        replaced[2].userMessage = .with { $0.text = "changed" }
        #expect(p.update(replaced) == derive(replaced))
    }

    @Test func publishedCollectionsStayUnchangedAfterMoreEvents() {
        var p = EventProjection()
        let first = ev(1, Mk.user("local"))
        let old = p.update([first], agent: .claude)
        let copy = old
        let next = p.update([first, ev(2, Mk.inputState("i", .submitted))], agent: .claude)
        #expect(old.typedLocally.contains(1))
        #expect(!next.typedLocally.contains(1))
        #expect(old == copy)
    }

    // --- not in Android's tests ---

    @Test func aChangedEventInTheMiddleStartsOverThoughTheCountOnlyGrew() {
        // Kotlin tells an unchanged prefix by object identity; here by value, which storage sharing makes as cheap.
        var p = EventProjection()
        let a = ev(1, Mk.inputState("x", .queued))
        let b = ev(2, Mk.inputState("y", .queued))
        _ = p.update([a, b])
        let other = ev(1, Mk.inputState("z", .queued))
        let grown = [other, b, ev(3, Mk.inputState("y", .canceled))]
        #expect(queued(p.update(grown)) == ["z"])
        // Any random-access collection will do, e.g. a slice.
        #expect(p.update(grown[0...1]) == derive(grown[0...1]))
    }

    @Test func aPutAgainKeyKeepsItsPlaceAndARemovedOneGoesLast() {
        var m = OrderedMap<String, Int>()
        m["b"] = 1
        m["a"] = 2
        m["b"] = 3
        #expect(m.keys == ["b", "a"])
        #expect(m.values == [3, 2])
        m.removeValue(forKey: "b")
        m["b"] = 4
        #expect(m.keys == ["a", "b"])
        m["a"] = nil
        #expect(m.keys == ["b"] && m.count == 1 && !m.contains("a"))
    }
}
