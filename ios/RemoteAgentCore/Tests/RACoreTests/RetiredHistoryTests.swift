import Foundation
import RAProto
import Testing
@testable import RACore

extension ServerTests {
    @Test func retiredCachedEventAndImageKeepTheirError() async throws {
        try await World.run(setUp: { w in
            try await w.daemon.putMachine("fixture-machine")
            try await w.daemon.putSession("s1", session("/fixture", "current session"))
        }) { w in
            let cache = MemoryCache()
            var header = PBHeader(); header.version = 1; header.kind = .event
            var event = PBStoredEvent(); event.seq = 1; event.envelope.header = try header.serializedData()
            await cache.putEvents(sessionId: "s1", events: [event])
            header.kind = .image
            var image = PBEnvelope(); image.header = try header.serializedData()
            await cache.putImage(key: "s1/retired", envelope: try image.serializedData())
            let r = try await w.client(cache: cache)
            try await r.hydrateExecutorState()
            let events = await r.executeCacheRead(PBCoreCacheReadEffect.with {
                $0.events = .with { $0.opID = 1; $0.sessionID = "s1"; $0.limit = 50 }
            }).cacheEventsLoaded
            #expect(events.rawEvents.count == 1)
            let decodedEvent = try PBStoredEvent(serializedBytes: events.rawEvents[0])
            #expect(throws: UnsupportedHistoryError.self) {
                try rejectRetiredHistory(decodedEvent.envelope)
            }
            #expect(await cache.events(sessionId: "s1").count == 1)
            #expect(try await r.image("s1", imageId: "retired") == .retiredHistory)
        }
    }
}

@Suite(.timeLimit(.minutes(2))) struct RetiredHistoryTests {
    @Test func oldRecordKindsHaveIdenticalErrors() throws {
        for kind: PBPayloadKind in [.sessionKey, .sessionMeta, .event, .image] {
            var header = PBHeader(); header.version = 1; header.kind = kind
            var envelope = PBEnvelope(); envelope.header = try header.serializedData()
            do { try rejectRetiredHistory(envelope); Issue.record("accepted old history") }
            catch { #expect(errorText(error) == retiredHistoryError) }
        }
    }
}
