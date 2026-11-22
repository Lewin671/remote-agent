import CryptoKit
import Foundation
import RAProto
import SwiftProtobuf
import os

@testable import RACore

struct FixtureRecord: Sendable {
    let version: Int
    let lease: Data
    let account: String
    let domain: Data
    let genesis: Data
    let member: String
    let xPrivate: Data
    let states: [Data]
    let proofs: [Data?]
}

func testEdPublicKey(_ seed: Data) throws -> Data {
    try Curve25519.Signing.PrivateKey(rawRepresentation: seed).publicKey.rawRepresentation
}

func testEdSign(_ seed: Data, _ msg: Data) throws -> Data {
    try Curve25519.Signing.PrivateKey(rawRepresentation: seed).signature(for: msg)
}

func testXPublicKey(_ priv: Data) throws -> Data {
    try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: priv).publicKey.rawRepresentation
}

func testParseLeaseIssuedAt(_ lease: Data) -> Int64 {
    guard lease.count >= 86 else { return 0 }
    var v: UInt64 = 0
    for b in lease[78..<86] {
        v = (v << 8) | UInt64(b)
    }
    return Int64(bitPattern: v)
}

private func appendU32BE(_ out: inout Data, _ val: UInt32) {
    var v = val.bigEndian
    withUnsafeBytes(of: &v) { out.append(contentsOf: $0) }
}

private func appendU64BE(_ out: inout Data, _ val: UInt64) {
    var v = val.bigEndian
    withUnsafeBytes(of: &v) { out.append(contentsOf: $0) }
}

private func appendLP(_ out: inout Data, _ data: Data) {
    appendU32BE(&out, UInt32(data.count))
    out.append(data)
}

private func appendLPStr(_ out: inout Data, _ str: String) {
    appendLP(&out, Data(str.utf8))
}

func encodeV4RecordBytes(
    account: String,
    domain: Data,
    genesis: Data,
    memberId: String,
    member: Bool = true,
    server: String = "https://ra.example.com",
    accountPublicKey: Data = Data(),
    states: [Data],
    proofs: [Data?] = [],
    lease: Data = Data(),
    floorMs: Int64? = nil,
    contentKeys: [(Int64, Data)] = [],
    needsRecoveryRotation: Bool = false,
    recoveryObjectsPending: Bool = false
) throws -> Data {
    guard domain.count == keySize, genesis.count == keySize else {
        throw TestFailure("invalid domain or genesis size")
    }
    var out = Data("RA4REC\u{0}".utf8)
    appendU32BE(&out, 4)
    appendLPStr(&out, account)
    out.append(domain)
    out.append(genesis)
    appendLPStr(&out, memberId)
    out.append(member ? 1 : 0)
    appendLPStr(&out, server)
    appendLP(&out, accountPublicKey)
    appendU32BE(&out, UInt32(states.count))
    for i in states.indices {
        appendLP(&out, states[i])
        appendLP(&out, proofs.indices.contains(i) ? (proofs[i] ?? Data()) : Data())
    }
    appendLP(&out, lease)
    appendU64BE(&out, UInt64(bitPattern: floorMs ?? testParseLeaseIssuedAt(lease)))
    appendU32BE(&out, UInt32(contentKeys.count))
    for (ep, sec) in contentKeys {
        guard sec.count == keySize else { throw TestFailure("bad content key length") }
        appendU64BE(&out, UInt64(bitPattern: ep))
        out.append(sec)
    }
    out.append(needsRecoveryRotation ? 1 : 0)
    out.append(recoveryObjectsPending ? 1 : 0)
    return out
}

func encodeV4NotifyRecordBytes(
    account: String,
    domain: Data,
    genesis: Data,
    headState: Data,
    purposeKey: Data
) throws -> Data {
    guard domain.count == keySize, genesis.count == keySize, purposeKey.count == keySize else {
        throw TestFailure("invalid notify record params")
    }
    var out = Data("RA4NTF\u{0}".utf8)
    appendU32BE(&out, 4)
    appendLPStr(&out, account)
    out.append(domain)
    out.append(genesis)
    appendLP(&out, headState)
    out.append(purposeKey)
    return out
}

func deriveTestNotifyPurposeKey(
    contentSecret: Data,
    account: String,
    domain: Data,
    contentEpoch: UInt64
) -> Data {
    var info = Data("ra3/purpose-key\u{0}".utf8)
    appendLPStr(&info, account)
    info.append(domain)
    appendU64BE(&info, contentEpoch)
    appendU32BE(&info, 10)
    return hkdf(secret: contentSecret, salt: nil, info: info, size: keySize)
}

func testRequestIdentity(
    account: String,
    domain: Data,
    header: PBHeader,
    plaintext: Data
) -> (key: Data, payload: Data) {
    var req = Data("ra4/request-key\u{0}".utf8)
    appendLPStr(&req, account)
    req.append(domain)
    appendLPStr(&req, header.sender)
    appendU32BE(&req, UInt32(header.kind.rawValue))
    appendLPStr(&req, header.machineID)
    appendLPStr(&req, header.sessionID)
    appendLPStr(&req, header.requestID)
    appendU64BE(&req, header.seq)
    let keyHash = sha256(req)

    var pay = Data("ra4/authorized-payload\u{0}".utf8)
    appendLP(&pay, keyHash)
    appendU32BE(&pay, UInt32(header.kind.rawValue))
    appendU64BE(&pay, UInt64(bitPattern: header.deadlineMs))
    appendLP(&pay, plaintext)
    let payloadHash = sha256(pay)
    return (keyHash, payloadHash)
}

/// Separate Go process owning a synthetic authority chain for integration tests.
final class TestSecurity: @unchecked Sendable {
    private static let registry = OSAllocatedUnfairLock<[String: TestSecurity]>(initialState: [:])
    private static let built = GoServer.binary.map {
        $0.deletingLastPathComponent().appendingPathComponent("securityfixture")
    }
    private let lock = NSLock()
    private let process: Process
    private let exited = DispatchSemaphore(value: 0)
    private let input: FileHandle
    private let output: FileHandle
    private var buffer = Data()
    private let serverUrl: String
    private let stateLock = NSLock()
    private var stores: [String: MemorySecurityStoreV4] = [:]
    var deviceSeeds: [String: Data] = [:]
    var xPrivates: [String: Data] = [:]
    var accountRoots: [String: Data] = [:]
    var domains: [String: Data] = [:]

    private init(serverUrl: String) throws {
        self.serverUrl = serverUrl
        let process = Process()
        process.executableURL = try Self.built.get()
        let input = Pipe()
        let output = Pipe()
        process.standardInput = input
        process.standardOutput = output
        process.standardError = FileHandle.standardError
        let exited = self.exited
        process.terminationHandler = { _ in exited.signal() }
        try process.run()
        self.process = process
        self.input = input.fileHandleForWriting
        self.output = output.fileHandleForReading
    }

    func call(_ op: String, _ fields: [String: Any] = [:]) throws -> [String: Any] {
        lock.lock()
        defer { lock.unlock() }
        var request = fields
        request["Op"] = op
        let data = try JSONSerialization.data(withJSONObject: request) + Data([10])
        try input.write(contentsOf: data)
        while !buffer.contains(10) {
            let next = output.availableData
            guard !next.isEmpty else { throw TestFailure("synthetic security fixture exited") }
            buffer.append(next)
        }
        let end = buffer.firstIndex(of: 10)!
        let line = buffer[..<end]
        buffer.removeSubrange(...end)
        guard let reply = try JSONSerialization.jsonObject(with: Data(line)) as? [String: Any]
        else { throw TestFailure("malformed synthetic fixture response") }
        if let error = reply["Error"] as? String {
            throw TestFailure("synthetic security fixture: " + error)
        }
        return reply
    }

    func bytes(_ reply: [String: Any], _ key: String) throws -> Data {
        guard let s = reply[key] as? String, let data = Data(base64Encoded: s) else {
            throw TestFailure("missing synthetic fixture bytes")
        }
        return data
    }

    static func parseRecord(_ reply: [String: Any]) throws -> FixtureRecord {
        guard let o = reply["Record"] as? [String: Any],
              let version = o["version"] as? Int, version == 3,
              let account = o["account"] as? String,
              let member = o["member"] as? String,
              let xString = o["x"] as? String, let x = Data(base64Encoded: xString),
              let domainString = o["domain"] as? String, let domain = Data(base64Encoded: domainString),
              let genesisString = o["genesis"] as? String, let genesis = Data(base64Encoded: genesisString),
              let leaseString = o["lease"] as? String, let lease = Data(base64Encoded: leaseString),
              let stateStrings = o["states"] as? [String],
              let proofEntries = o["proofs"] as? [Any],
              stateStrings.count == proofEntries.count
        else { throw TestFailure("malformed synthetic record") }
        let states = try stateStrings.map { raw -> Data in
            guard let b = Data(base64Encoded: raw) else { throw TestFailure("bad state") }
            return b
        }
        let proofs = try proofEntries.map { entry -> Data? in
            guard let s = entry as? String else { return nil }
            guard let b = Data(base64Encoded: s) else { throw TestFailure("bad proof") }
            return b
        }
        return FixtureRecord(
            version: version, lease: lease, account: account, domain: domain, genesis: genesis,
            member: member, xPrivate: x, states: states, proofs: proofs
        )
    }

    @discardableResult
    func seedMemberStore(
        _ rec: FixtureRecord,
        edSeed: Data,
        store existing: MemorySecurityStoreV4? = nil
    ) throws -> MemorySecurityStoreV4 {
        let edPub = try testEdPublicKey(edSeed)
        let xPub = try testXPublicKey(rec.xPrivate)
        let rootSeed = stateLock.withLock { () -> Data? in
            deviceSeeds[rec.member] = edSeed
            xPrivates[rec.member] = rec.xPrivate
            domains[rec.account] = rec.domain
            return accountRoots[rec.account]
        }
        let accPub = try rootSeed.map { try testEdPublicKey($0) } ?? Data()
        let recBytes = try encodeV4RecordBytes(
            account: rec.account,
            domain: rec.domain,
            genesis: rec.genesis,
            memberId: rec.member,
            member: true,
            server: serverUrl,
            accountPublicKey: accPub,
            states: rec.states,
            proofs: rec.proofs,
            lease: rec.lease
        )
        let target: MemorySecurityStoreV4 = stateLock.withLock {
            let s = existing ?? stores[rec.member] ?? MemorySecurityStoreV4()
            stores[rec.member] = s
            return s
        }
        target.seedTestRecord(
            edPrivate: edSeed,
            edPublic: edPub,
            xPrivate: rec.xPrivate,
            xPublic: xPub,
            recordBytes: recBytes
        )
        return target
    }

    func enroll(seed: Data, kind: PBDeviceKind) throws -> (FixtureRecord, Data) {
        let reply = try call("enroll", ["Seed": seed.base64EncodedString(), "Kind": kind.rawValue])
        return try (Self.parseRecord(reply), bytes(reply, "State"))
    }

    func record(member: String) throws -> FixtureRecord {
        try Self.parseRecord(call("record", ["Member": member]))
    }

    func syncStoresForAll() {
        let snapshot = stateLock.withLock { (Array(stores), deviceSeeds) }
        for (memberId, store) in snapshot.0 {
            guard let seed = snapshot.1[memberId], let rec = try? record(member: memberId) else { continue }
            _ = try? seedMemberStore(rec, edSeed: seed, store: store)
        }
    }

    func remove(targets: [String]) throws -> Data {
        let reply = try call("remove", ["Targets": targets])
        return try bytes(reply, "State")
    }

    func seal(_ account: Account, _ header: PBHeader, _ plain: Data, seed: Data? = nil) throws -> PBEnvelope {
        let reply = try call(
            "seal",
            [
                "Member": account.deviceId,
                "Header": try header.serializedData().base64EncodedString(),
                "Plain": plain.base64EncodedString(),
                "SessionSeed": (seed ?? Data()).base64EncodedString(),
            ]
        )
        return try PBEnvelope(serializedBytes: bytes(reply, "Envelope"))
    }

    func seal<M: SwiftProtobuf.Message>(_ account: Account, _ header: PBHeader, _ message: M, seed: Data? = nil) throws -> PBEnvelope {
        try seal(account, header, try message.serializedData(), seed: seed)
    }

    func open(_ account: Account, _ envelope: PBEnvelope, seed: Data? = nil) throws -> (PBHeader, Data) {
        let reply = try call(
            "open",
            [
                "Member": account.deviceId,
                "Envelope": try envelope.serializedData().base64EncodedString(),
                "SessionSeed": (seed ?? Data()).base64EncodedString(),
            ]
        )
        return try (PBHeader(serializedBytes: bytes(reply, "Header")), bytes(reply, "Plain"))
    }

    func store(_ id: String) -> MemorySecurityStoreV4? {
        stateLock.withLock { stores[id] }
    }

    func domain(account: String) -> Data? {
        stateLock.withLock { domains[account] }
    }

    static func find(_ server: String) -> TestSecurity? { registry.withLock { $0[server] } }
    static func get(_ server: String) throws -> TestSecurity {
        guard let fixture = find(server) else { throw TestFailure("synthetic authority missing") }
        return fixture
    }

    static func initialize(_ account: Account, edSeed: Data, rootSeed: Data? = nil, recovery: Data? = nil) throws -> (TestSecurity, Data) {
        let fixture = try TestSecurity(serverUrl: account.server)
        let accountId: String
        if !account.accountId.isEmpty {
            accountId = account.accountId
        } else if let rootSeed {
            accountId = deviceId(publicKey: try testEdPublicKey(rootSeed))
        } else {
            accountId = account.deviceId
        }
        if let rootSeed {
            fixture.stateLock.withLock { fixture.accountRoots[accountId] = rootSeed }
        }
        let response = try fixture.call(
            "init",
            [
                "Account": accountId,
                "Seed": edSeed.base64EncodedString(),
                "Recovery": (recovery ?? Data()).base64EncodedString(),
            ]
        )
        let rec = try parseRecord(response)
        try fixture.seedMemberStore(rec, edSeed: edSeed)
        registry.withLock { $0[account.server] = fixture }
        return try (fixture, fixture.bytes(response, "Genesis"))
    }

    static func remove(_ server: String) {
        let removed = registry.withLock { $0.removeValue(forKey: server) }
        removed?.close()
    }

    private func close() {
        lock.lock()
        defer { lock.unlock() }
        try? input.close()
        if exited.wait(timeout: .now() + 5) == .timedOut, process.isRunning {
            kill(process.processIdentifier, SIGKILL)
            exited.wait()
        }
        try? output.close()
        stateLock.withLock {
            stores.removeAll()
            deviceSeeds.removeAll()
            xPrivates.removeAll()
            accountRoots.removeAll()
            domains.removeAll()
        }
    }
}

func fixtureRemote(
    account: Account,
    http: HTTP,
    cache: (any CipherCache)? = nil,
    log: @escaping @Sendable (String) -> Void = { _ in },
    clock: @escaping @Sendable () -> Int64 = { Int64(Date().timeIntervalSince1970 * 1000) },
    sleep: @escaping Sleeper = realSleep,
    jitter: @escaping @Sendable () -> Double = { Double.random(in: 0..<1) },
    frameQueue: Int = 1024,
    frameQueueBytes: Int = 32 << 20,
    streamOpenMs: Int64 = 15_000,
    securityStore: (any SecurityStoreV4)? = nil
) throws -> Remote {
    try Remote(
        account: account,
        http: http,
        cache: cache,
        log: log,
        clock: clock,
        sleep: sleep,
        jitter: jitter,
        frameQueue: frameQueue,
        frameQueueBytes: frameQueueBytes,
        streamOpenMs: streamOpenMs,
        securityStore: securityStore
            ?? TestSecurity.find(account.server)?.store(account.deviceId)
            ?? MemorySecurityStoreV4(),
        allowPlainHttp: true
    )
}
