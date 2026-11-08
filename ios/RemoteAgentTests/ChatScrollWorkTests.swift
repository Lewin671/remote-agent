import Foundation
import Testing

@testable import RemoteAgent

@MainActor
@Suite struct ChatScrollWorkTests {
    @Test func correctionsLeaveLayoutAndCoalesceToTheLatestRequest() async throws {
        let work = ChatScrollWork()
        var calls: [Int] = []
        for n in 0..<100 { work.schedule { calls.append(n) } }
        #expect(calls.isEmpty, "never scroll synchronously from a geometry callback")
        try await Task.sleep(for: .milliseconds(80))
        #expect(calls == [99])
    }

    @Test func cancelingAnOldCorrectionDoesNotCancelItsReplacement() async throws {
        let work = ChatScrollWork()
        var calls: [Int] = []
        work.schedule { calls.append(1) }
        work.cancel()
        work.schedule { calls.append(2) }
        try await Task.sleep(for: .milliseconds(80))
        #expect(calls == [2])
        work.schedule { calls.append(3) }
        work.cancel()
        try await Task.sleep(for: .milliseconds(80))
        #expect(calls == [2])
    }

    @Test func aCorrectionThatCausesMoreGeometryCannotReenterItself() async throws {
        let work = ChatScrollWork()
        var calls = 0
        work.schedule {
            calls += 1
            work.schedule { calls += 1 }
            #expect(calls == 1)
        }
        try await Task.sleep(for: .milliseconds(100))
        #expect(calls == 2)
    }

    @Test func queuedFollowRechecksGestureRestorationAndLatestGeometry() async throws {
        let tracker = ChatScrollTracker()
        _ = tracker.viewport(height: 2_000, offset: 1_100, floor: 700, count: 10)
        var calls = 0
        tracker.follow { calls += 1 }
        _ = tracker.interaction(true)
        try await Task.sleep(for: .milliseconds(80))
        #expect(calls == 0)
        _ = tracker.interaction(false)
        tracker.follow { calls += 1 }
        tracker.keep()
        try await Task.sleep(for: .milliseconds(80))
        #expect(calls == 0)
        tracker.kept()
        tracker.follow { calls += 1 }
        _ = tracker.viewport(height: 2_000, offset: 1_300, floor: 700, count: 10)
        try await Task.sleep(for: .milliseconds(80))
        #expect(calls == 0, "the system already placed the bottom while the request waited")
        _ = tracker.viewport(height: 2_200, offset: 1_300, floor: 700, count: 10)
        tracker.follow { calls += 1 }
        try await Task.sleep(for: .milliseconds(80))
        #expect(calls == 1)
    }
}
