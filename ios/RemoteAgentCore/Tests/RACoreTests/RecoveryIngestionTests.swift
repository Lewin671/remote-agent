import Foundation
import RAProto
import Testing

@testable import RACore

@Suite(.serialized, .enabled(if: GoServer.available, GoServer.missing), .timeLimit(.minutes(2)))
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
        await remote.prepareRecoveryIngestion(machine: machine.deviceId, seed: seed, fixture: fixture, machineAccount: machine)
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
        await w.remote.prepareRecoveryIngestion(machine: w.machine.deviceId, seed: w.seed, fixture: w.fixture, machineAccount: w.machine)
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
        let items = await [
            w.remote.decodeRecoveryEvent(seq: 1, machine: w.machine.deviceId, envelope: w.before),
            w.remote.decodeRecoveryEvent(seq: 2, machine: w.machine.deviceId, envelope: w.after),
            w.remote.decodeRecoveryEvent(seq: 3, machine: w.machine.deviceId, envelope: live, history: false),
        ]
        #expect(items.count == 3)
        #expect(items[0].unavailable && items[0].hasEvent && items[0].event.kind == nil)
        #expect(items[1].verified && !items[1].unavailable && items[1].seq == 2)
        #expect(items[2].verified && items[2].event.agentMessage.text == "live message")
    }
    @Test func failedOriginIsNeverAnUnavailableHistorySkip() async throws {
        let w = try await world()
        defer {
            w.remote.close()
            TestSecurity.remove(w.server)
        }
        var forged = w.before
        forged.signature[0] ^= 1
        do {
            _ = try await w.remote.openEnvelopeOp(
                envelope: forged,
                expectedPurpose: .event,
                machineId: w.machine.deviceId,
                sessionId: "s",
                seq: 1,
                history: true,
                overrideSessionSeed: w.seed
            )
            Issue.record("forged origin accepted")
        } catch { #expect(error as? DeviceSecurity.Failure == .untrusted) }
        let decoded = await w.remote.decodeRecoveryEvent(seq: 1, machine: w.machine.deviceId, envelope: forged)
        #expect(decoded.broken && !decoded.unavailable)
    }
    @Test func changedRouteAndCurrentCiphertextStillStopIngestion() async throws {
        for current in [false, true] {
            let w = try await world()
            defer {
                w.remote.close()
                TestSecurity.remove(w.server)
            }
            var envelope = current ? w.after : w.before
            if current { envelope.ciphertext[0] ^= 1 }
            let decoded = await w.remote.decodeRecoveryEvent(seq: current ? 2 : 1, machine: w.machine.deviceId, envelope: envelope)
            if current {
                #expect(decoded.broken && !decoded.unavailable)
            } else {
                #expect(decoded.unavailable)
            }
        }
    }
}

extension Remote {
    func prepareRecoveryIngestion(machine: String, seed: Data, fixture: TestSecurity, machineAccount: Account) async {
        sessionMachines["s"] = machine
        var kh = PBHeader()
        kh.kind = .sessionKey
        kh.machineID = machine
        kh.sessionID = "s"
        kh.seq = 1
        if let env = try? fixture.seal(machineAccount, kh, seed) {
            _ = try? await openEnvelopeOp(
                envelope: env,
                expectedPurpose: .sessionKey,
                machineId: machine,
                sessionId: "s",
                history: true
            )
        }
    }
    func decodeRecoveryEvent(seq: UInt64, machine: String, envelope: PBEnvelope, history: Bool = true) async -> PBCorePlainEvent {
        var out = PBCorePlainEvent()
        out.seq = seq
        do {
            let res = try await openEnvelopeOp(
                envelope: envelope,
                expectedPurpose: .event,
                machineId: machine,
                sessionId: "s",
                seq: seq,
                history: history
            )
            let pb = try PBEvent(serializedBytes: res.plaintext)
            out.event = pb
            out.verified = true
        } catch DeviceSecurity.Failure.unavailable {
            var slot = PBEvent()
            slot.seq = seq
            slot.sessionID = "s"
            out.event = slot
            out.unavailable = true
        } catch is UnsupportedHistoryError {
            out.retired = true
        } catch {
            out.broken = true
        }
        return out
    }
}
