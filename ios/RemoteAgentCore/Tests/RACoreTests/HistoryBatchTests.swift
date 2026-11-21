import Foundation
import RAProto
import Testing

@testable import RACore

extension DeviceSecurityTests {
    private func historyEnvelope(_ state: S.State, chain: [S.State], seq: UInt64 = 1) async throws -> PBEnvelope {
        let signer = try SigningKey(seed: hex("machine_seed"))
        let machine = deviceId(publicKey: signer.publicKey)
        let machineX = try hex("machine_x")
        let p = try pin()
        let secret = try S.openGrant(p, state, recipient: machine, xPrivate: machineX)
        let statesMap = Dictionary(uniqueKeysWithValues: chain.map { ($0.core.epoch, $0) })
        let machineRecord = try S.Record(
            pin: p, states: statesMap, memberID: machine, xPrivate: machineX, member: true,
            secret: secret, lease: signedLease(state))
        let machineSdk = try TestCoreSecurity(
            store: MemorySecurityStore(machineRecord), memberID: machine, signingKey: signer,
            clock: { state.core.issuedAt })
        defer { Task { await machineSdk.close() } }
        var event = PBEvent(); event.sessionID = "batch-session"; event.seq = seq
        event.agentMessage = .with { $0.text = "verified history" }
        return try await machineSdk.seal(
            purpose: .event, machine: machine, session: "batch-session", seq: seq,
            plaintext: event.serializedData(), sessionSeed: Data(repeating: 7, count: 32))
    }

    @Test func historyBatchReadsOnceButVerifiesEveryEnvelopeAndRefreshesNextBatch() async throws {
        let genesis = try state("genesis"), added = try state("added"), renewal = try state("renewal")
        let record = try S.Record(pin: pin(), states: [1: genesis, 2: added, 3: renewal],
            memberID: genesis.core.authorityID, xPrivate: hex("phone_x"), member: true,
            secret: hex("content_secret_2"))
        let store = HistoryBatchStore(record)
        let sdk = try TestCoreSecurity(store: store, memberID: record.memberID,
            signingKey: try SigningKey(seed: hex("phone_seed")),
            fetchStates: { _ in throw S.Failure.unknown })
        let old = try await historyEnvelope(added, chain: [genesis, added])
        let recent = try await historyEnvelope(renewal, chain: [genesis, added, renewal], seq: 2)
        var signature = old; signature.signature[signature.signature.startIndex] ^= 1
        var cipher = old; cipher.ciphertext[cipher.ciphertext.startIndex] ^= 1
        var route = old
        if !route.securityHeader.isEmpty { route.securityHeader[route.securityHeader.endIndex - 1] ^= 1 }
        let seed = Data(repeating: 7, count: 32)
        let results = try await sdk.openHistoryBatch([old, signature, cipher, route, recent], sessionSeed: seed)
        #expect(store.reads == 1)
        #expect(try results[0].get().0.epoch == added.core.epoch)
        for i in 1...3 { #expect(throws: (any Error).self) { try results[i].get() } }
        #expect(try results[4].get().0.epoch == renewal.core.epoch)
        #expect(try results[0].get().1 == (try await sdk.openHistory(old, sessionSeed: seed).1))
        // A previous successful batch must not hide a locked, erased or corrupt store.
        store.failReads()
        await #expect(throws: S.Failure.storage) { try await sdk.openHistoryBatch([old], sessionSeed: seed) }
        #expect(try await sdk.openHistoryBatch([]).isEmpty)
        await #expect(throws: S.Failure.capacity) { try await sdk.openHistoryBatch(Array(repeating: old, count: 129)) }
        await sdk.close()
        await #expect(throws: S.Failure.storage) { try await sdk.openHistoryBatch([old], sessionSeed: seed) }
    }

    @Test func historyBatchSynchronizesAFutureEpochAndKeepsIndividualFailures() async throws {
        let genesis = try state("genesis"), added = try state("added"), renewal = try state("renewal")
        let record = try S.Record(pin: pin(), states: [1: genesis, 2: added],
            memberID: genesis.core.authorityID, xPrivate: hex("phone_x"), member: true,
            secret: hex("content_secret_2"))
        let raw = try S.stateBytes(renewal)
        let sdk = try TestCoreSecurity(store: MemorySecurityStore(record), memberID: record.memberID,
            signingKey: try SigningKey(seed: hex("phone_seed")), clock: { renewal.core.issuedAt },
            fetchStates: { after in
                #expect(after == 2)
                return .with { $0.headEpoch = 3; $0.states = [.with { $0.state = raw }] }
            })
        defer { Task { await sdk.close() } }
        let seed = Data(repeating: 7, count: 32)
        let old = try await historyEnvelope(added, chain: [genesis, added])
        let future = try await historyEnvelope(renewal, chain: [genesis, added, renewal])
        let results = try await sdk.openHistoryBatch([old, future, old], sessionSeed: seed)
        for result in results { #expect(try !result.get().1.isEmpty) }
        #expect(try await sdk.identity().1.core.epoch == 3)

        let attempts = Counter()
        let offline = try TestCoreSecurity(store: MemorySecurityStore(record), memberID: record.memberID,
            signingKey: try SigningKey(seed: hex("phone_seed")),
            fetchStates: { _ in attempts.increment(); throw S.Failure.unavailable })
        defer { Task { await offline.close() } }
        let partial = try await offline.openHistoryBatch([old, future, future, old], sessionSeed: seed)
        #expect(try !partial[0].get().1.isEmpty)
        #expect(throws: S.Failure.unavailable) { try partial[1].get() }
        #expect(throws: S.Failure.unavailable) { try partial[2].get() }
        #expect(try !partial[3].get().1.isEmpty)
        // A failed sync is not repeated for every later envelope of the batch.
        #expect(attempts.value == 1)
    }

    @Test func historyBatchCannotAuthorizeControl() async throws {
        let genesis = try state("genesis"), added = try state("added")
        let phone = try SigningKey(seed: hex("phone_seed"))
        let store = MemorySecurityStore(try S.Record(pin: pin(), states: [1: genesis, 2: added],
            memberID: genesis.core.authorityID, xPrivate: hex("phone_x"), member: true,
            secret: hex("content_secret_2")))
        let sdk = try TestCoreSecurity(store: store, memberID: genesis.core.authorityID,
            signingKey: phone, clock: { added.core.issuedAt }, fetchStates: { _ in throw S.Failure.unknown })
        defer { Task { await sdk.close() } }
        var cmd = PBCommand(); cmd.interrupt.sessionID = "s"
        let command = try await sdk.seal(purpose: .command,
            machine: added.core.members.first { $0.kind == 1 }!.id,
            request: "batch-control-request", deadline: added.core.issuedAt + S.lease,
            plaintext: cmd.serializedData())
        let results = try await sdk.openHistoryBatch([command])
        #expect(throws: S.Failure.self) { try results[0].get() }
    }
}

private final class HistoryBatchStore: DeviceSecurity.Storage, @unchecked Sendable {
    private let lock = NSLock()
    private let record: DeviceSecurity.Record
    private var failed = false
    private var count = 0
    init(_ record: DeviceSecurity.Record) { self.record = record }
    var reads: Int { lock.lock(); defer { lock.unlock() }; return count }
    func failReads() { lock.lock(); defer { lock.unlock() }; failed = true }
    func readSecurity() throws -> DeviceSecurity.Record {
        lock.lock(); defer { lock.unlock() }; count += 1
        if failed { throw DeviceSecurity.Failure.storage }
        return record
    }
    func initializeSecurity(_ record: DeviceSecurity.Record) throws { throw DeviceSecurity.Failure.conflict }
    func writeSecurity(expectedHead: Data, _ record: DeviceSecurity.Record) throws -> DeviceSecurity.Record { throw DeviceSecurity.Failure.storage }
}
