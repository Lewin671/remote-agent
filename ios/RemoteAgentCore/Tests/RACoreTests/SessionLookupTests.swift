import Foundation
import Testing

@testable import RACore

/// Android's core SessionLookupTest: looking a session up by its ID
/// (architecture §8 「按 ID 取」): when it counts as missing, and what a
/// server that cannot be asked means.
@Suite struct SessionLookupTests {
    private struct Down: Error {
        let message: String
    }

    private enum Answer {
        case held(Bool)
        case fails(String)
    }

    private struct Run {
        var waits = [Int64]()
        var reports = [SessionLookup]()
        var asked = 0
    }

    private func go(_ answers: [Answer]) async throws -> Run {
        var r = Run()
        try await lookUpSession(
            fetch: {
                defer { r.asked += 1 }
                switch answers[r.asked] {
                case .held(let b): return b
                case .fails(let why): throw Down(message: why)
                }
            },
            wait: { r.waits.append($0) },
            describe: { ($0 as? Down)?.message ?? "\($0)" },
            report: { r.reports.append($0) })
        return r
    }

    @Test func aSessionTheServerHasIsFoundWithoutSayingAnything() async throws {
        let r = try await go([.held(true)])
        #expect(r.asked == 1)
        #expect(r.reports == [])
    }

    @Test func aSessionJustStartedIsNotMissingUntilTheServerSaidSoForTenSeconds() async throws {
        // Not uploaded yet for the first three asks, then there.
        let started = try await go([.held(false), .held(false), .held(false), .held(true)])
        #expect(started.waits == [2_500, 2_500, 2_500])
        #expect(started.reports == [.reading, .reading, .reading])
        // Never there: missing after the fifth ask, ten seconds after the first.
        let none = try await go(Array(repeating: .held(false), count: 5))
        #expect(none.asked == 5)
        #expect(none.waits.reduce(0, +) == 10_000)
        #expect(none.reports.last == .missing)
    }

    @Test func aServerThatCannotBeAskedIsNoAnswerAndIsAskedAgainEverMoreSlowly() async throws {
        let r = try await go(Array(repeating: .fails("连不上"), count: 6) + [.held(false), .fails("连不上"), .held(true)])
        // Said why each time, never as missing; the waits double up to half a minute and start over after an answer.
        #expect(r.waits == [3_000, 6_000, 12_000, 24_000, 30_000, 30_000, 2_500, 3_000])
        #expect(r.reports.first == .failed(reason: "连不上"))
        #expect(r.reports.filter { $0 == .missing }.isEmpty)
        // What the pages say meanwhile.
        #expect(lookupNote(.failed(reason: "连不上")) == "没能读取这个会话：连不上")
        #expect(lookupNote(.missing) == "找不到这个会话")
        #expect(lookupNote(.reading) == nil)
        #expect(lookupNote(nil) == nil)
    }

    // --- not in Android's tests ---

    @Test func cancellationEndsItInsteadOfCountingAsAFailure() async {
        var reports = [SessionLookup]()
        await #expect(throws: CancellationError.self) {
            try await lookUpSession(fetch: { throw CancellationError() }, wait: { _ in }, describe: { "\($0)" }, report: { reports.append($0) })
        }
        #expect(reports.isEmpty)
    }
}
