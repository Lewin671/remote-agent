import Foundation
import RAProto
import Testing

@testable import RACore

@Suite(.serialized, .enabled(if: GoServer.available, GoServer.missing))
struct RecoveryIngestionTests {
    struct World {
        let remote: Remote
        let fixture: TestSecurity
        let server: String
        let seed: Data
        let account: Account
        let before: PBEnvelope
        let after: PBEnvelope
        let machine: Account
    }
    func world() async throws -> World {
        let server = "https://" + UUID().uuidString.lowercased() + ".example.com"
        let oldSeed = randomBytes(32)
        let root = randomBytes(32)
        let phone = Account(
            server: server, root: root, deviceSeed: oldSeed,
            deviceId: deviceId(publicKey: try SigningKey(seed: oldSeed).publicKey))
        let (fixture, _) = try TestSecurity.initialize(phone)
        let machineSeed = randomBytes(32)
        let enrolled = try fixture.call(
            "enroll", ["Seed": machineSeed.base64EncodedString(), "Kind": 1])
        let machineRecord = try TestSecurity.record(
            JSONSerialization.data(withJSONObject: enrolled["Record"]!))
        let machine = Account(
            server: server, root: Data(), deviceSeed: machineSeed, deviceId: machineRecord.memberID,
            accountId: machineRecord.pin.account)
        let seed = randomBytes(32)
        var header = PBHeader()
        header.kind = .event
        header.machineID = machine.deviceId
        header.sessionID = "s"
        header.seq = 1
        var old = PBEvent()
        old.sessionID = "s"
        old.seq = 1
        old.agentMessage.text = "old history"
        let before = try fixture.seal(machine, header, old, seed: seed)
        let newSeed = randomBytes(32)
        let recovered = try fixture.call("recover", ["Seed": newSeed.base64EncodedString()])
        let record = try TestSecurity.record(
            JSONSerialization.data(withJSONObject: recovered["Record"]!))
        let account = Account(
            server: server, root: Data(), deviceSeed: newSeed, deviceId: record.memberID,
            accountId: record.pin.account)
        let remote = try Remote(
            account: account, http: HTTP(), securityStorage: TestSecurityStore(record))
        await remote.prepareRecoveryIngestion(machine: machine.deviceId, seed: seed)
        header.seq = 2
        var next = PBEvent()
        next.sessionID = "s"
        next.seq = 2
        next.agentMessage.text = "readable suffix"
        let after = try fixture.seal(machine, header, next, seed: seed)
        return World(
            remote: remote, fixture: fixture, server: server, seed: seed, account: account,
            before: before, after: after, machine: machine)
    }
    @Test func unavailablePrefixAllowsReadableSuffixAndLiveEvents() async throws {
        let w = try await world()
        defer {
            w.remote.close()
            TestSecurity.remove(w.server)
        }
        await w.remote.ingestRecoveryTest(seq: 1, envelope: w.before)
        await w.remote.ingestRecoveryTest(seq: 2, envelope: w.after)
        var header = PBHeader()
        header.kind = .event
        header.machineID = w.machine.deviceId
        header.sessionID = "s"
        header.seq = 3
        var event = PBEvent()
        event.sessionID = "s"
        event.seq = 3
        event.agentMessage.text = "live message"
        let live = try w.fixture.seal(w.machine, header, event, seed: w.seed)
        await w.remote.ingestRecoveryTest(seq: 3, envelope: live, history: false)
        let snapshot = await w.remote.recoveryIngestionSnapshot()
        #expect(snapshot.events.count == 3)
        #expect(snapshot.unavailableCount == 1)
        #expect(snapshot.verifiedEvents.map(\.seq) == [2, 3])
        if snapshot.events.count == 3 {
            #expect(snapshot.events[0].kind == nil)
            #expect(snapshot.events[2].agentMessage.text == "live message")
        }
        #expect(snapshot.error == nil)
    }
    @Test func failedOriginIsNeverAnUnavailableHistorySkip() async throws {
        let w = try await world()
        defer {
            w.remote.close()
            TestSecurity.remove(w.server)
        }
        var forged = w.before
        forged.signature[0] ^= 1
        let security = try await w.remote.securitySession()
        do {
            _ = try await security.openHistory(forged, sessionSeed: w.seed)
            Issue.record("forged origin accepted")
        } catch { #expect(error as? DeviceSecurity.Failure == .untrusted) }
        await w.remote.ingestRecoveryTest(seq: 1, envelope: forged)
        let snapshot = await w.remote.recoveryIngestionSnapshot()
        #expect(snapshot.events.isEmpty)
        #expect(snapshot.error != nil)
        #expect(snapshot.unavailableCount == 0)
    }
    @Test func changedRouteAndCurrentCiphertextStillStopIngestion() async throws {
        for current in [false, true] {
            let w = try await world()
            defer {
                w.remote.close()
                TestSecurity.remove(w.server)
            }
            if current { await w.remote.ingestRecoveryTest(seq: 1, envelope: w.before) }
            var envelope = current ? w.after : w.before
            if current { envelope.ciphertext[0] ^= 1 }
            await w.remote.ingestRecoveryTest(seq: 2, envelope: envelope)
            let snapshot = await w.remote.recoveryIngestionSnapshot()
            #expect(snapshot.events.count == (current ? 1 : 0))
            #expect(snapshot.unavailableCount == (current ? 1 : 0))
            #expect(snapshot.error != nil)
        }
    }
}

extension Remote {
    func prepareRecoveryIngestion(machine: String, seed: Data) {
        sessions["s"] = SessionView(id: "s", machineId: machine)
        sessionKeys["s"] = seed
        let buffer = Buffer()
        buffer.usedAt = clock()
        buffers["s"] = buffer
    }
    func ingestRecoveryTest(seq: UInt64, envelope: PBEnvelope, history: Bool = true) async {
        var event = PBStoredEvent()
        event.seq = seq
        event.envelope = envelope
        await ingest("s", [event], fillGaps: false, save: false, history: history)
    }
    func recoveryIngestionSnapshot() -> SessionEvents { eventFeed("s").value }
}
