import Foundation
import RAProto
import Testing
@testable import RACore

extension ServerTests {
    @Test func retiredHeaderCannotEraseVerifiedRevisionOrAllowMetadataReplay() async throws {
        try await World.run(setUp: { w in try await w.daemon.putMachine("fixture-machine") }) { w in
            let r = try await w.client()
            try await r.refreshTrustedDirectory()
            let security = try TestSecurity.get(w.server.url)
            var h = PBHeader(); h.machineID = w.daemon.id; h.sessionID = "known"; h.seq = 1
            var older = PBRemoteSession(); older.sessionID = "known"; older.machineID = w.daemon.id
            h.kind = .sessionKey
            older.key = try security.seal(w.daemon.account, h, randomBytes(32))
            h.kind = .sessionMeta
            var info = PBSession(); info.id = "known"; info.title = "older title"
            older.meta = try security.seal(w.daemon.account, h, info)
            var current = older; h.seq = 2; info.title = "current title"
            current.meta = try security.seal(w.daemon.account, h, info)
            await r.putSession(current, save: false)
            #expect(r.current.sessions["known"]?.metaRev == 2)
            for kind: PBPayloadKind in [.sessionKey, .sessionMeta] {
                var header = PBHeader(); header.version = 1; header.kind = kind
                var retired = PBEnvelope(); retired.header = try header.serializedData()
                var forged = older
                if kind == .sessionKey { forged.key = retired } else { forged.meta = retired }
                await r.putSession(forged, save: false)
                #expect(r.current.error == retiredHistoryError)
                #expect(r.current.sessions["known"]?.metaRev == 2)
                #expect(r.current.sessions["known"]?.machineId == w.daemon.id)
                #expect(r.current.sessions["known"]?.info?.title == "current title")
                await r.putSession(older, save: false)
                #expect(r.current.sessions["known"]?.metaRev == 2)
                #expect(r.current.sessions["known"]?.info?.title == "current title")
            }
        }
    }

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
