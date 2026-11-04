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
            r.start()
            _ = try await r.awaitState("current session") { $0.sessions["s1"]?.info != nil }
            await r.loadEvents("s1")
            #expect(r.events("s1").value.error == retiredHistoryError)
            #expect(r.events("s1").value.events.isEmpty)
            #expect(await cache.events(sessionId: "s1").count == 1)
            #expect(try await r.image("s1", imageId: "retired") == .corrupt(why: retiredHistoryError))
        }
    }

    @Test func retiredSessionRemainsVisibleWithTheSameHistoryError() async throws {
        try await World.run(setUp: { w in try await w.daemon.putMachine("fixture-machine") }) { w in
            let r = try await w.client()
            try await r.refreshTrustedDirectory()
            var header = PBHeader(); header.version = 1; header.kind = .sessionKey
            var row = PBRemoteSession(); row.sessionID = "retired"; row.machineID = w.daemon.account.deviceId
            row.key.header = try header.serializedData()
            await r.putSession(row, save: false)
            #expect(r.current.sessions["retired"]?.historyError == retiredHistoryError)
            await r.loadEvents("retired")
            #expect(r.events("retired").value.error == retiredHistoryError)
            #expect(r.events("retired").value.events.isEmpty)
        }
    }
}

@Suite struct RetiredHistoryTests {
    @Test func oldRecordKindsHaveIdenticalErrors() throws {
        for kind: PBPayloadKind in [.sessionKey, .sessionMeta, .event, .image] {
            var header = PBHeader(); header.version = 1; header.kind = kind
            var envelope = PBEnvelope(); envelope.header = try header.serializedData()
            do { try rejectRetiredHistory(envelope); Issue.record("accepted old history") }
            catch { #expect(errorText(error) == retiredHistoryError) }
        }
    }
}
