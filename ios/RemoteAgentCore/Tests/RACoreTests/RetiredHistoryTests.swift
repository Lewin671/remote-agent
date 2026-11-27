import Foundation
import RAProto
import Testing
@testable import RACore

extension ServerTests {
    @Test func retiredHeaderCannotEraseVerifiedRevisionOrAllowMetadataReplay() async throws {
        try await World.run(setUp: { w in try await w.daemon.putMachine("fixture-machine") }) { w in
            let r = try await w.client()
            _ = try await r.testSyncSecurity()
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
            _ = try await r.openEnvelopeOp(
                envelope: current.key,
                expectedPurpose: .sessionKey,
                machineId: current.machineID,
                sessionId: current.sessionID,
                history: true
            )
            let currentMeta = try await r.openEnvelopeOp(
                envelope: current.meta,
                expectedPurpose: .sessionMeta,
                machineId: current.machineID,
                sessionId: current.sessionID,
                history: true
            )
            #expect(currentMeta.seq == 2)
            for kind: PBPayloadKind in [.sessionKey, .sessionMeta] {
                var header = PBHeader(); header.version = 1; header.kind = kind
                var retired = PBEnvelope(); retired.header = try header.serializedData()
                let expectedPurpose: SecurityPurpose = kind == .sessionKey ? .sessionKey : .sessionMeta
                await #expect(throws: UnsupportedHistoryError.self) {
                    _ = try await r.openEnvelopeOp(
                        envelope: retired,
                        expectedPurpose: expectedPurpose,
                        machineId: older.machineID,
                        sessionId: older.sessionID,
                        history: true
                    )
                }
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
            _ = try await r.testSyncSecurity()
            let tok = r.securityStore.loadLoginToken(server: r.account.server, deviceId: r.account.deviceId)?.token ?? ""
            let reqBytes = try PBListRemoteSessionsRequest.with { $0.limit = 50 }.serializedData()
            let listRes = await SecurityExecutor.executeRelayRpcUnary(
                http: r.http,
                method: .listRemoteSessions,
                server: r.origin.server,
                authToken: tok,
                requestBytes: reqBytes,
                allowPlainHttp: r.allowPlainHttp,
                opID: 3
            )
            let listed = try PBListRemoteSessionsResponse(serializedBytes: listRes?.responseBytes ?? Data())
            for rs in listed.sessions {
                if rs.hasKey {
                    _ = try await r.openEnvelopeOp(
                        envelope: rs.key,
                        expectedPurpose: .sessionKey,
                        machineId: rs.machineID,
                        sessionId: rs.sessionID,
                        history: true
                    )
                }
            }
            await r.setWatchedSessions([
                PBWatchedSessionRoute.with { $0.sessionID = "s1"; $0.machineID = w.daemon.account.deviceId }
            ])
            let events = await r.executeCacheRead(PBCoreCacheReadEffect.with {
                $0.events = .with { $0.opID = 1; $0.sessionID = "s1"; $0.limit = 50 }
            }).cacheEventsLoaded
            #expect(events.rawEvents.count == 1)
            let decodedEvent = try PBStoredEvent(serializedBytes: events.rawEvents[0])
            await #expect(throws: UnsupportedHistoryError.self) {
                _ = try await r.openEnvelopeOp(
                    envelope: decodedEvent.envelope,
                    expectedPurpose: .event,
                    machineId: w.daemon.account.deviceId,
                    sessionId: "s1",
                    seq: 1,
                    history: true
                )
            }
            #expect(await cache.events(sessionId: "s1").count == 1)
            #expect(try await r.image("s1", imageId: "retired") == .retiredHistory)
        }
    }

    @Test func retiredSessionRemainsVisibleWithTheSameHistoryError() async throws {
        try await World.run(setUp: { w in try await w.daemon.putMachine("fixture-machine") }) { w in
            let r = try await w.client()
            _ = try await r.testSyncSecurity()
            var header = PBHeader(); header.version = 1; header.kind = .sessionKey
            var row = PBRemoteSession(); row.sessionID = "retired"; row.machineID = w.daemon.account.deviceId
            row.key.header = try header.serializedData()
            await #expect(throws: UnsupportedHistoryError.self) {
                _ = try await r.openEnvelopeOp(
                    envelope: row.key,
                    expectedPurpose: .sessionKey,
                    machineId: row.machineID,
                    sessionId: row.sessionID,
                    history: true
                )
            }
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
