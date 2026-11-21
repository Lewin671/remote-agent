import Foundation
import RAProto
import SwiftProtobuf

@testable import RACore

/// Canonical request identity and payload hashes matching `shared/devicesec/identity.go`.
func testRequestIdentity(pin: DeviceSecurity.Pin, header: PBHeader, plaintext: Data) -> (key: Data, payload: Data) {
    func u32be(_ v: UInt32) -> Data {
        var b = v.bigEndian
        return Data(bytes: &b, count: 4)
    }
    func u64be(_ v: UInt64) -> Data {
        var b = v.bigEndian
        return Data(bytes: &b, count: 8)
    }
    func lp(_ d: Data) -> Data {
        u32be(UInt32(d.count)) + d
    }
    func str(_ s: String) -> Data {
        lp(Data(s.utf8))
    }

    var req = Data()
    req.append(str(pin.account))
    req.append(pin.domain)
    req.append(str(header.sender))
    req.append(u32be(UInt32(header.kind.rawValue)))
    req.append(str(header.machineID))
    req.append(str(header.sessionID))
    req.append(str(header.requestID))
    req.append(u64be(header.seq))
    let keyHash = sha256(Data("ra4/request-key\u{0}".utf8), req)

    var pay = Data()
    pay.append(lp(keyHash))
    pay.append(u32be(UInt32(header.kind.rawValue)))
    pay.append(u64be(UInt64(bitPattern: header.deadlineMs)))
    pay.append(lp(plaintext))
    let payloadHash = sha256(Data("ra4/authorized-payload\u{0}".utf8), pay)
    return (keyHash, payloadHash)
}

private final class XPrivateBox: @unchecked Sendable {
    private let lock = NSLock()
    private var raw: Data?
    func get() -> Data? { lock.withLock { raw } }
    func set(_ value: Data) { lock.withLock { raw = value } }
}

/// Test driver backed by real Go `CoreClient` + `AuthoritySecurity` + `SecurityExecutor`.
final class TestCoreSecurity: @unchecked Sendable {
    let authority: AuthoritySecurity
    private let store: any DeviceSecurity.Storage
    private let clock: @Sendable () -> UInt64
    private let runner: CoreSecurityRunner
    private let xPrivateCache = XPrivateBox()
    private let lock = NSLock()
    private var closed = false

    init(
        store: any DeviceSecurity.Storage,
        memberID: String,
        signingKey: SigningKey,
        clock: @escaping @Sendable () -> UInt64 = { UInt64(Date().timeIntervalSince1970 * 1000) },
        fetchStates: @escaping AuthoritySecurity.StateFetch = { _ in PBGetSecurityStatesResponse() }
    ) throws {
        self.store = store
        self.clock = clock
        let cache = self.xPrivateCache
        let exec = SecurityExecutor(
            signer: { signingKey },
            xPrivate: { cache.get() ?? (try? store.readSecurity().xPrivateRaw) }
        )
        self.runner = try CoreSecurityRunner(executor: exec)
        self.authority = AuthoritySecurity(
            store: store,
            memberID: memberID,
            signingKey: signingKey,
            clock: clock,
            fetchStates: fetchStates
        )
    }

    func close() async {
        lock.withLock { closed = true }
        await authority.close()
        runner.close()
    }

    func identity() async throws -> (DeviceSecurity.Pin, DeviceSecurity.State) {
        try await authority.identity()
    }

    func record() async throws -> DeviceSecurity.Record {
        try await authority.record()
    }

    @discardableResult
    func sync() async throws -> DeviceSecurity.Record {
        let rec = try await authority.sync()
        xPrivateCache.set(rec.xPrivateRaw)
        try runner.syncRecord(rec, nowMs: Int64(bitPattern: clock()))
        return rec
    }

    func seal(
        purpose: DeviceSecurity.Purpose,
        machine: String = "",
        session: String = "",
        request: String = "",
        seq: UInt64 = 0,
        deadline: UInt64 = 0,
        plaintext: Data,
        sessionSeed: Data? = nil
    ) async throws -> PBEnvelope {
        guard !lock.withLock({ closed }) else { throw DeviceSecurity.Failure.storage }
        let rec = try store.readSecurity()
        xPrivateCache.set(rec.xPrivateRaw)
        let nowMs = Int64(bitPattern: clock())
        try runner.syncRecord(rec, nowMs: nowMs)
        let res = try runner.runOp {
            $0.sealEnvelope = .with {
                $0.purpose = purpose.rawValue
                $0.machineID = machine
                $0.sessionID = session
                $0.requestID = request
                $0.seq = seq
                $0.deadlineMs = Int64(bitPattern: deadline)
                $0.plaintext = plaintext
                if let sessionSeed, !sessionSeed.isEmpty {
                    $0.useSessionSeed = true
                    $0.overrideSessionSeed = sessionSeed
                }
            }
        }
        guard case .sealed(let sealed)? = res.result else {
            throw DeviceSecurity.Failure.invalid
        }
        return sealed.envelope
    }

    func openHistoryWithMeta(
        _ envelope: PBEnvelope,
        expectedPurpose: DeviceSecurity.Purpose? = nil,
        machine: String = "",
        session: String = "",
        request: String = "",
        seq: UInt64 = 0,
        sessionSeed: Data? = nil
    ) async throws -> PBSecurityOpenEnvelopeResult {
        try await openBatch(
            [envelope],
            expectedPurpose: expectedPurpose,
            machine: machine,
            session: session,
            request: request,
            seq: seq,
            sessionSeed: sessionSeed
        )[0].get()
    }

    func openHistory(
        _ envelope: PBEnvelope,
        expectedPurpose: DeviceSecurity.Purpose? = nil,
        machine: String = "",
        session: String = "",
        request: String = "",
        seq: UInt64 = 0,
        sessionSeed: Data? = nil
    ) async throws -> (PBSecurityOpenEnvelopeResult, Data) {
        let res = try await openHistoryWithMeta(
            envelope,
            expectedPurpose: expectedPurpose,
            machine: machine,
            session: session,
            request: request,
            seq: seq,
            sessionSeed: sessionSeed
        )
        return (res, res.plaintext)
    }

    func openHistoryBatch(
        _ envelopes: [PBEnvelope],
        sessionSeed: Data? = nil
    ) async throws -> [Result<(PBSecurityOpenEnvelopeResult, Data), Error>] {
        let rawResults = try await openBatch(envelopes, sessionSeed: sessionSeed)
        return rawResults.map { result in
            result.map { ($0, $0.plaintext) }
        }
    }

    private func openBatch(
        _ envelopes: [PBEnvelope],
        expectedPurpose: DeviceSecurity.Purpose? = nil,
        machine: String = "",
        session: String = "",
        request: String = "",
        seq: UInt64 = 0,
        sessionSeed: Data? = nil
    ) async throws -> [Result<PBSecurityOpenEnvelopeResult, Error>] {
        guard !lock.withLock({ closed }) else { throw DeviceSecurity.Failure.storage }
        if envelopes.isEmpty { return [] }
        if envelopes.count > 128 { throw DeviceSecurity.Failure.capacity }
        var rec = try store.readSecurity()
        xPrivateCache.set(rec.xPrivateRaw)
        try runner.syncRecord(rec, nowMs: Int64(bitPattern: clock()))

        var syncedThisBatch = false
        var syncFailure: Error?
        var out: [Result<PBSecurityOpenEnvelopeResult, Error>] = []
        out.reserveCapacity(envelopes.count)

        func runOne(_ env: PBEnvelope) -> Result<PBSecurityOpenEnvelopeResult, Error> {
            do {
                let res = try runner.runOp {
                    $0.openEnvelope = .with {
                        $0.envelope = env
                        if let expectedPurpose { $0.expectedPurpose = expectedPurpose.rawValue }
                        if !machine.isEmpty { $0.machineID = machine }
                        if !session.isEmpty { $0.sessionID = session }
                        if !request.isEmpty { $0.requestID = request }
                        if seq != 0 { $0.seq = seq }
                        $0.history = true
                        if let sessionSeed, !sessionSeed.isEmpty {
                            $0.overrideSessionSeed = sessionSeed
                        }
                    }
                }
                guard case .opened(let opened)? = res.result else {
                    return .failure(DeviceSecurity.Failure.invalid)
                }
                return .success(opened)
            } catch {
                return .failure(error)
            }
        }

        for env in envelopes {
            let first = runOne(env)
            if case .failure(let err) = first, (err as? DeviceSecurity.Failure) == .stale {
                if !syncedThisBatch {
                    syncedThisBatch = true
                    do {
                        rec = try await authority.sync()
                        xPrivateCache.set(rec.xPrivateRaw)
                        try runner.syncRecord(rec, nowMs: Int64(bitPattern: clock()))
                    } catch {
                        syncFailure = error
                    }
                }
                if let syncFailure {
                    out.append(.failure(syncFailure))
                    continue
                }
                out.append(runOne(env))
            } else {
                out.append(first)
            }
        }
        return out
    }
}
