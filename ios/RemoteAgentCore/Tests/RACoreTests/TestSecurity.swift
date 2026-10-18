import Foundation
import RAProto
import SwiftProtobuf
import os

@testable import RACore

/// A separate Go process owns synthetic authority keys. Test messages pass
/// the real protocol and server; no product bypass or real key store is used.
final class TestSecurity: @unchecked Sendable {
    private static let registry = OSAllocatedUnfairLock<[String: TestSecurity]>(initialState: [:])
    private static let built: Result<URL, TestFailure> = Result {
        guard let go = GoServer.go else { throw TestFailure("Go toolchain missing") }
        let dir = try GoServer.binary.get().deletingLastPathComponent()
        let path = dir.appendingPathComponent("security-fixture")
        let process = Process()
        process.executableURL = URL(fileURLWithPath: go)
        process.arguments = ["build", "-o", path.path, "./internal/testutil/securityfixture"]
        process.currentDirectoryURL = repoRoot
        let output = Pipe()
        process.standardOutput = output
        process.standardError = output
        try process.run()
        let log = output.fileHandleForReading.readDataToEndOfFile()
        while process.isRunning { usleep(10000) }
        guard process.terminationStatus == 0 else {
            throw TestFailure(
                "synthetic fixture build failed: "
                    + String(decoding: log.suffix(2000), as: UTF8.self))
        }
        return path
    }.mapError { TestFailure(String(describing: $0)) }
    private let lock = NSLock()
    private let process: Process
    private let input: FileHandle
    private let output: FileHandle
    private var buffer = Data()
    private let storeLock = NSLock()
    private var stores: [String: TestSecurityStore] = [:]
    private init() throws {
        let process = Process()
        process.executableURL = try Self.built.get()
        let input = Pipe()
        let output = Pipe()
        process.standardInput = input
        process.standardOutput = output
        process.standardError = FileHandle.standardError
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
    func seal(_ account: Account, _ header: PBHeader, _ plain: Data, seed: Data? = nil) throws
        -> PBEnvelope
    {
        let reply = try call(
            "seal",
            [
                "Member": account.deviceId, "Header": header.serializedData().base64EncodedString(),
                "Plain": plain.base64EncodedString(),
                "SessionSeed": (seed ?? Data()).base64EncodedString(),
            ])
        return try PBEnvelope(serializedBytes: bytes(reply, "Envelope"))
    }
    func seal<M: SwiftProtobuf.Message>(
        _ account: Account, _ header: PBHeader, _ message: M, seed: Data? = nil
    ) throws -> PBEnvelope { try seal(account, header, message.serializedData(), seed: seed) }
    func open(_ account: Account, _ envelope: PBEnvelope, seed: Data? = nil) throws -> (
        PBHeader, Data
    ) {
        let reply = try call(
            "open",
            [
                "Member": account.deviceId,
                "Envelope": envelope.serializedData().base64EncodedString(),
                "SessionSeed": (seed ?? Data()).base64EncodedString(),
            ])
        return try (PBHeader(serializedBytes: bytes(reply, "Header")), bytes(reply, "Plain"))
    }
    func rpc(_ capsule: Data) throws -> Data {
        try bytes(call("rpc", ["Capsule": capsule.base64EncodedString()]), "Capsule")
    }
    func store(_ id: String) -> TestSecurityStore? {
        storeLock.lock()
        defer { storeLock.unlock() }
        return stores[id]
    }
    func install(_ record: DeviceSecurity.Record) {
        storeLock.lock()
        defer { storeLock.unlock() }
        stores[record.memberID] = TestSecurityStore(record)
    }
    static func find(_ server: String) -> TestSecurity? { registry.withLock { $0[server] } }
    static func get(_ server: String) throws -> TestSecurity {
        guard let fixture = find(server) else { throw TestFailure("synthetic authority missing") }
        return fixture
    }
    static func initialize(_ account: Account) throws -> (TestSecurity, Data) {
        let fixture = try TestSecurity()
        let keys = try Keys.derive(root: account.root)
        let response = try fixture.call(
            "init",
            [
                "Account": deviceId(publicKey: keys.account.publicKey),
                "Seed": account.deviceSeed.base64EncodedString(),
            ])
        try fixture.install(DeviceSecurity.Record.parse(fixture.bytes(response, "Record")))
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
        let deadline = Date().addingTimeInterval(5)
        while process.isRunning {
            if Date() > deadline { kill(process.processIdentifier, SIGKILL) }
            usleep(10000)
        }
        try? output.close()
    }
}

final class TestSecurityStore: DeviceSecurity.Storage, @unchecked Sendable {
    private let lock = NSLock()
    private var record: DeviceSecurity.Record
    private var history: [UInt64: DeviceSecurity.Historical] = [:]
    private var removals: Data?
    init(_ record: DeviceSecurity.Record) { self.record = record }
    func readSecurity() throws -> DeviceSecurity.Record {
        lock.lock()
        defer { lock.unlock() }
        return record
    }
    func initializeSecurity(_ record: DeviceSecurity.Record) throws {
        throw DeviceSecurity.Failure.conflict
    }
    func reserveSecurity(expectedState: Data) throws -> DeviceSecurity.Record {
        lock.lock()
        defer { lock.unlock() }
        guard try DeviceSecurity.stateHash(record.state) == expectedState else {
            throw DeviceSecurity.Failure.stale
        }
        guard record.counter < UInt64.max else { throw DeviceSecurity.Failure.capacity }
        record.counter += 1
        return record
    }
    func advanceSecurity(expectedState: Data, next: DeviceSecurity.State) throws
        -> DeviceSecurity.Record
    {
        lock.lock()
        defer { lock.unlock() }
        guard try DeviceSecurity.stateHash(record.state) == expectedState else {
            throw DeviceSecurity.Failure.stale
        }
        history[record.state.core.epoch] = try DeviceSecurity.Historical(
            state: record.state, secret: record.secret())
        record = try record.advanced(next)
        return record
    }
    func readSecurityHistory(epoch: UInt64) throws -> DeviceSecurity.Historical? {
        lock.lock()
        defer { lock.unlock() }
        return history[epoch]
    }
    func cacheSecurityHistory(_ value: DeviceSecurity.Historical) throws {
        lock.lock()
        defer { lock.unlock() }
        try DeviceSecurity.verifyState(record.pin, value.state)
        history[value.state.core.epoch] = value
    }
    func readRemovalOperations() throws -> Data? {
        lock.lock()
        defer { lock.unlock() }
        return removals
    }
    func writeRemovalOperations(_ data: Data) throws {
        lock.lock()
        defer { lock.unlock() }
        removals = data
    }
}

func fixtureRemote(
    account: Account, http: HTTP, cache: (any CipherCache)? = nil,
    log: @escaping @Sendable (String) -> Void = { _ in },
    clock: @escaping @Sendable () -> Int64 = { Int64(Date().timeIntervalSince1970 * 1000) },
    sleep: @escaping Sleeper = realSleep,
    jitter: @escaping @Sendable () -> Double = { Double.random(in: 0..<1) }, frameQueue: Int = 1024,
    historyIdleMs: Int64 = 60000, historyIdleCount: Int = 8,
    historyIdleBytes: Int64 = 16 * 1024 * 1024, streamOpenMs: Int64 = 15000,
    linkGraceMs: Int64 = 10000, securityStorage: (any DeviceSecurity.Storage)? = nil
) throws -> Remote {
    try Remote(
        account: account, http: http, cache: cache, log: log, clock: clock, sleep: sleep,
        jitter: jitter, frameQueue: frameQueue, historyIdleMs: historyIdleMs,
        historyIdleCount: historyIdleCount, historyIdleBytes: historyIdleBytes,
        streamOpenMs: streamOpenMs, linkGraceMs: linkGraceMs,
        securityStorage: securityStorage
            ?? TestSecurity.find(account.server)?.store(account.deviceId))
}
