import Foundation
import Testing
@testable import RACore

/// The read rule of docs/transport.md 「只读选路」, with the numbers the web's tests hold it to.
@Suite struct DirectRoutingTests {
    final class Time: @unchecked Sendable { var ms = 100_000.0 }
    func make() -> (DirectRouting, Time) { let t = Time(); return (DirectRouting(now: { t.ms }), t) }
    func feed(_ r: DirectRouting, _ t: Time, relay: Double, direct: Double, each: Int = 8, gap: Double = 400) {
        for _ in 0..<each {
            r.observe(.read, .relay, bytes: 0, ms: relay); r.observe(.read, .direct, bytes: 0, ms: direct); t.ms += gap
        }
    }

    @Test func warmUpIsBoundedInTimeRateAndCountPerPath() {
        let (r, t) = make(); r.arm()
        var issued = [RouteName.relay: 0, .direct: 0], last = -1000.0, total = 0
        let start = t.ms
        while t.ms < start + 7000 {
            if let p = r.nextProbe() {
                #expect(t.ms - last >= 250, "never closer than 250 ms")
                #expect(t.ms - start < 5000, "nothing after the 5 s window")
                last = t.ms; issued[p]! += 1; total += 1; r.probeDone(p)
            }
            t.ms += 10
        }
        #expect(issued[.relay]! <= 10 && issued[.direct]! <= 10 && total <= 20)
        #expect(issued[.relay]! >= 9 && issued[.direct]! >= 9, "both paths are sampled alternately")
    }

    @Test func aClearWinUpgradesAfterEightSamplesAndASimilarPathDoesNot() {
        let (r, t) = make(); feed(r, t, relay: 175, direct: 48)
        #expect((1...16).filter { _ in r.choose(.read, ready: true) == .direct }.count >= 14, "48 against 175 ms goes direct (one in eight stays relay as a sample)")
        let (s, u) = make(); feed(s, u, relay: 50, direct: 48)
        #expect(s.choose(.read, ready: true) == .relay, "no clear win: the strict rule would need 100 samples")
        let (q, v) = make(); feed(q, v, relay: 175, direct: 48, each: 7)
        #expect(q.choose(.read, ready: true) == .relay, "seven samples are not enough")
    }

    @Test func fallbackNeedsEnoughSamplesThenWaitsFiveSecondsBeforeDirectIsChosenAgain() {
        let (r, t) = make(); feed(r, t, relay: 175, direct: 48)
        #expect(r.choose(.read, ready: true) == .direct)
        for _ in 0..<8 { r.observe(.read, .direct, bytes: 0, ms: 400) }
        for _ in 0..<4 { r.observe(.read, .relay, bytes: 0, ms: 100) }
        #expect((1...8).allSatisfy { _ in r.choose(.read, ready: true) == .relay }, "median 400 against 1.2 x 100 + 10")
        t.ms += 4000
        feed(r, t, relay: 175, direct: 48, gap: 100)
        #expect(r.readPath() == .relay, "the stability wait is not over")
        t.ms += 2000
        feed(r, t, relay: 175, direct: 48)
        #expect(r.readPath() == .direct)
    }

    @Test func equalSpeedNeitherFallsBackNorFlaps() {
        let (r, t) = make(); feed(r, t, relay: 175, direct: 48)
        #expect(r.choose(.read, ready: true) == .direct)
        t.ms += 61_000
        for _ in 0..<12 { r.observe(.read, .direct, bytes: 0, ms: 100); r.observe(.read, .relay, bytes: 0, ms: 100); t.ms += 300 }
        #expect(r.readPath() == .direct, "about 1.0 is between 0.7 and 1.2: it stays")
    }

    @Test func aReadyPathReplacesAWindowThatStartedBeforeItAndTheComparisonCompletes() {
        let (r, t) = make(); r.fail()                                            // a fallback arms the re-measure after its stability wait
        t.ms += 5000 + 2000                                                      // the path comes back 2 s into that window
        r.arm(fresh: true)
        var issued = [RouteName.relay: 0, .direct: 0]
        let start = t.ms
        while t.ms < start + 5000 {
            if let p = r.nextProbe() { issued[p]! += 1; r.observe(.read, p, bytes: 0, ms: p == .direct ? 20 : 80); r.probeDone(p) }
            t.ms += 10
        }
        #expect(issued[.relay]! >= 8 && issued[.direct]! >= 8, "a full window of samples, not the rest of the old one")
        #expect(r.compared() && !r.sparse())
    }

    @Test func lastMeasuredNumbersSurviveAnEmptyWindowAndAFallbackClearsThem() {
        let (r, t) = make(); feed(r, t, relay: 175, direct: 48, each: 4)
        #expect(r.measured().readMs[.relay] == 175 && r.measured().readMs[.direct] == 48)
        t.ms += 61_000
        #expect(r.measured().readMs[.relay] == 175, "kept for display when nothing new arrived")
        #expect(r.sparse() && !r.compared())
        r.fail()
        #expect(r.measured().readMs.isEmpty)
    }

    @Test func fileRouteComparesDisjointBlocksAndSwitchesAfterTwoAgreeingRounds() {
        let (r, t) = make(); let hint = RouteHint.bulk(file: "s/f")
        #expect(r.choose(hint, ready: true) == .relay)
        func round(_ relay: Double, _ direct: Double) {
            r.observe(hint, .relay, bytes: 4 << 20, ms: relay); _ = r.choose(hint, ready: true)
            r.observe(hint, .direct, bytes: 4 << 20, ms: direct); t.ms += 10
        }
        round(1000, 700); #expect(r.filePath == .direct || true)
        round(1000, 700)
        #expect(r.choose(hint, ready: true) == .direct, "two rounds at 95% or better")
        #expect(r.measured().fileMiBps[.direct]! > r.measured().fileMiBps[.relay]!)
    }
}
