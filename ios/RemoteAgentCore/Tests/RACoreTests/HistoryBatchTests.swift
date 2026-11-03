import Foundation
import RAProto
import Testing

@testable import RACore

extension DeviceSecurityTests {
    private func historyEnvelope(_ state: S.State, seq: UInt64 = 1) throws -> PBEnvelope {
        let signer = try SigningKey(seed: hex("machine_seed"))
        let machine = deviceId(publicKey: signer.publicKey)
        let header = S.Header(
            account: state.core.account, domain: state.core.domain, epoch: state.core.epoch,
            state: try S.stateHash(state), contentEpoch: state.core.contentEpoch,
            sender: machine, kind: S.Purpose.event.kind, purpose: .event, machine: machine,
            session: "batch-session", request: "", seq: seq, deadline: 0, compression: 0)
        var event = PBEvent(); event.sessionID = header.session; event.seq = seq
        event.agentMessage = .with { $0.text = "verified history" }
        return try S.envelope(.init(formed: S.form(
            secret: hex("content_secret_2"), sessionSeed: Data(repeating: 7, count: 32),
            header: header, plaintext: event.serializedData(), signingKey: signer)))
    }

    @Test func historyBatchReadsOnceButVerifiesEveryEnvelopeAndRefreshesNextBatch() async throws {
        let genesis = try state("genesis"), added = try state("added"), renewal = try state("renewal")
        let record = try S.Record(pin: pin(), states: [1: genesis, 2: added, 3: renewal],
            memberID: genesis.core.authorityID, xPrivate: hex("phone_x"), member: true,
            secret: hex("content_secret_2"))
        let store = HistoryBatchStore(record)
        let sdk = DeviceSecurityClient(store: store, memberID: record.memberID,
            signingKey: try SigningKey(seed: hex("phone_seed")),
            fetchStates: { _ in throw S.Failure.unknown })
        let old = try historyEnvelope(added), recent = try historyEnvelope(renewal, seq: 2)
        var signature = old; signature.signature[signature.signature.startIndex] ^= 1
        var cipher = old; cipher.ciphertext[cipher.ciphertext.startIndex] ^= 1
        var route = old; var projected = try PBHeader(serializedBytes: route.header)
        projected.seq += 1; route.header = try projected.serializedData()
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
        let sdk = DeviceSecurityClient(store: MemorySecurityStore(record), memberID: record.memberID,
            signingKey: try SigningKey(seed: hex("phone_seed")), clock: { renewal.core.issuedAt },
            fetchStates: { after in
                #expect(after == 2)
                return .with { $0.headEpoch = 3; $0.states = [.with { $0.state = raw }] }
            })
        let seed = Data(repeating: 7, count: 32)
        let old = try historyEnvelope(added), future = try historyEnvelope(renewal)
        let results = try await sdk.openHistoryBatch([old, future, old], sessionSeed: seed)
        for result in results { #expect(try !result.get().1.isEmpty) }
        #expect(try await sdk.identity().1.core.epoch == 3)

        let attempts = Counter()
        let offline = DeviceSecurityClient(store: MemorySecurityStore(record), memberID: record.memberID,
            signingKey: try SigningKey(seed: hex("phone_seed")),
            fetchStates: { _ in attempts.increment(); throw S.Failure.unavailable })
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
        let sdk = DeviceSecurityClient(store: store, memberID: genesis.core.authorityID,
            signingKey: phone, clock: { added.core.issuedAt }, fetchStates: { _ in throw S.Failure.unknown })
        let command = try await sdk.seal(purpose: .command,
            machine: added.core.members.first { $0.kind == 1 }!.id,
            request: "batch-control-request", deadline: added.core.issuedAt + S.lease,
            plaintext: Data([1]))
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
