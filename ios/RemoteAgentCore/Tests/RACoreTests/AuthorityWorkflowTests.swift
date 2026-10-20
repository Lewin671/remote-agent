import Connect
import CryptoKit
import Foundation
import RAProto
import Testing
import os

@testable import RACore

/// Serializes every receipt/state CAS, with power-loss injection on either side
/// of every durable mutation. No oracle or synthetic signing authority is involved.
final class AuthorityTestVault: AuthorityStorage, @unchecked Sendable {
    let lock = NSRecursiveLock()
    var record: DeviceSecurity.Record?
    var op: SecurityOperation?
    var profile: AccountPublicProfile?
    var account: Account?
    var writes = 0
    var crashAt: Int?
    var crashAfter = false
    var committedReceipts: [SecurityOperation] = []
    func mutate<T>(_ body: () throws -> T) throws -> T {
        writes += 1
        if writes == crashAt && !crashAfter { throw TestFailure("injected before storage") }
        let value = try body()
        if writes == crashAt && crashAfter { throw TestFailure("injected after storage") }
        return value
    }
    func inject(_ write: Int?, after: Bool = false) { lock.withLock { crashAt = write; crashAfter = after; writes = 0 } }
    func existingSecurity() throws -> DeviceSecurity.Record? { lock.withLock { record } }
    func readSecurity() throws -> DeviceSecurity.Record { try lock.withLock { try record.unwrap() } }
    func initializeSecurity(_ record: DeviceSecurity.Record) throws {
        try lock.withLock {
            try mutate {
                if let old = self.record { try DeviceSecurity.check(old.includesInstallation(record), .conflict) }
                else { self.record = record }
            }
        }
    }
    func writeSecurity(expectedHead: Data, _ record: DeviceSecurity.Record) throws -> DeviceSecurity.Record {
        try lock.withLock {
            try mutate {
                let old = try self.record.unwrap()
                try DeviceSecurity.check(DeviceSecurity.stateHash(old.state) == expectedHead, .stale)
                let merged = try record.mergingCommitted(old)
                self.record = merged; return merged
            }
        }
    }
    func readOperation() throws -> SecurityOperation? { lock.withLock { op } }
    func prepareOperation(expectedHead: Data?, _ build: (DeviceSecurity.Record?) throws -> SecurityOperation) throws -> SecurityOperation {
        try lock.withLock {
            try DeviceSecurity.check(op == nil, .conflict)
            if let expectedHead { try DeviceSecurity.check(DeviceSecurity.stateHash(record.unwrap().state) == expectedHead, .stale) }
            return try mutate {
                let next = try build(record); op = next; committedReceipts.append(next); return next
            }
        }
    }
    func replaceOperation(expected: SecurityOperation, next: SecurityOperation?) throws {
        try lock.withLock {
            try DeviceSecurity.check(op == expected, .stale)
            try next?.validateProgress(from: expected)
            try mutate { op = next; if let next { committedReceipts.append(next) } }
        }
    }
    func savePublicProfile(_ profile: AccountPublicProfile) throws { try lock.withLock { try mutate { self.profile = profile } } }
    func publicProfile(pin: DeviceSecurity.Pin) throws -> AccountPublicProfile? {
        try lock.withLock { if let profile { try DeviceSecurity.check(profile.pin == pin, .untrusted) }; return profile }
    }
    func adoptOperation(_ expected: SecurityOperation, account: Account, record: DeviceSecurity.Record, profile: AccountPublicProfile) throws {
        try lock.withLock {
            try DeviceSecurity.check(op == expected, .stale)
            try mutate { self.profile = profile }
            try mutate {
                if let old = self.record {
                    if expected.kind == .recovery {
                        try DeviceSecurity.check(old.pin == record.pin && record.states[old.headEpoch].map { try DeviceSecurity.stateHash($0) } == DeviceSecurity.stateHash(old.state), .conflict)
                        if try old.memberID != record.memberID || !old.includesInstallation(record) { self.record = record }
                    } else { try DeviceSecurity.check(old.includesInstallation(record), .conflict) }
                } else { self.record = record }
            }
            try mutate { self.account = account }
        }
    }
}

private func enabledGate() -> AuthorityGate {
    let gate = AuthorityGate(); gate.set(active: true, protectedDataAvailable: true); return gate
}
private func createAuthority(_ server: ManagementRelay, vault: AuthorityTestVault = AuthorityTestVault(), http: HTTP = HTTP()) async throws
    -> (Account, AuthorityTestVault, String) {
    let bootstrap = AuthorityBootstrap(storage: vault, gate: enabledGate(), http: http)
    let code = try await bootstrap.prepareCreation(server: server.url)
    try await bootstrap.confirmRecoveryCodeSaved()
    let account = try await bootstrap.continueCreation(setupToken: server.setupToken)
    try await bootstrap.finishCreation()
    return (account, vault, code)
}

@Suite(.serialized, .enabled(if: GoServer.available, GoServer.missing))
struct AuthorityWorkflowTests {

    @Test func restartsCreationAtEveryStorageBoundaryWithoutCompetingGenesis() async throws {
        for after in [false, true] {
            // Covers initial draft, confirmation, actual request, pin phase,
            // profile/security/account adoption, certificate receipt/install, clear.
            for crash in 1...13 {
                let server = try await ManagementRelay(); defer { server.close() }
                let vault = AuthorityTestVault(); vault.inject(crash, after: after)
                let gate = enabledGate()
                let bootstrap = AuthorityBootstrap(storage: vault, gate: gate)
                do {
                    _ = try await bootstrap.prepareCreation(server: server.url)
                    try await bootstrap.confirmRecoveryCodeSaved()
                    _ = try await bootstrap.continueCreation(setupToken: server.setupToken)
                    try await bootstrap.finishCreation()
                } catch {}
                let before = try vault.readOperation()
                vault.inject(nil)
                let restarted = AuthorityBootstrap(storage: vault, gate: gate)
                if let before {
                    let hash = before.parent
                    try await restarted.confirmRecoveryCodeSaved()
                    let acct = try await restarted.continueCreation(setupToken: server.setupToken)
                    try await restarted.finishCreation()
                    #expect(try vault.readSecurity().pin.genesis == hash)
                    #expect(acct.deviceId == vault.account?.deviceId)
                    let registrations = vault.committedReceipts.filter { $0.kind == .creation }
                    #expect(Set(registrations.map(\.parent)).count == 1)
                    #expect(Set(try registrations.map { try PBRegisterDeviceRequest(serializedBytes: $0.signed).registration }).count == 1)
                    #expect(Set(try registrations.map { try PBRegisterDeviceRequest(serializedBytes: $0.signed).signature }).count == 1)
                } else if vault.account == nil {
                    _ = try await restarted.prepareCreation(server: server.url)
                    try await restarted.confirmRecoveryCodeSaved()
                    _ = try await restarted.continueCreation(setupToken: server.setupToken)
                    try await restarted.finishCreation()
                }
                #expect(try vault.readSecurity().member)
            }
        }
    }

    @Test func ackLostAfterPinAndLeaseRetriesSameRegistrationAndCertificate() async throws {
        let server = try await ManagementRelay(); defer { server.close() }
        let gate = enabledGate(), vault = AuthorityTestVault()
        let cut = Signal()
        let http = Intercept.http(for: server.url, answered: { req in
            if req.url?.path.hasSuffix("/PinSecurityDomain") == true, !cut.isSet {
                cut.set(); gate.set(active: false, protectedDataAvailable: false)
            }
        }) { $0 }
        let bootstrap = AuthorityBootstrap(storage: vault, gate: gate, http: http)
        _ = try await bootstrap.prepareCreation(server: server.url)
        try await bootstrap.confirmRecoveryCodeSaved()
        do { _ = try await bootstrap.continueCreation(setupToken: server.setupToken); Issue.record("ignored lock") } catch {}
        let old = try vault.readOperation().unwrap()
        gate.set(active: true, protectedDataAvailable: true)
        let restart = AuthorityBootstrap(storage: vault, gate: gate)
        _ = try await restart.continueCreation(setupToken: "correctable-unsigned-token")
        try await restart.finishCreation()
        #expect(try vault.readSecurity().pin.genesis == old.parent)
        #expect(try vault.readSecurity().lease != nil)
    }



}

/// Per-test relay/database. Advancing the recovery deadline is an explicit test
/// fixture action; production code still enforces the unmodified 48-hour delay.
final class ManagementRelay: @unchecked Sendable {
    let url: String
    let setupToken = "authority-workflow-test-token"
    let directory: URL
    let process: Process
    init(signup: String = "token") async throws {
        let port = freePort()
        url = "http://127.0.0.1:\(port)"
        directory = FileManager.default.temporaryDirectory.appendingPathComponent("ra-management-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        process = Process(); process.executableURL = try GoServer.binary.get()
        process.arguments = ["-min-free-bytes", "0", "-listen", "127.0.0.1:\(port)", "-db", directory.appendingPathComponent("ra.db").path, "-signup", signup]
        var environment = ProcessInfo.processInfo.environment; environment["RA_SETUP_TOKEN"] = setupToken
        process.environment = environment
        process.standardOutput = FileHandle.nullDevice; process.standardError = FileHandle.nullDevice
        try process.run()
        let session = URLSession(configuration: .ephemeral)
        defer { session.invalidateAndCancel() }
        for _ in 0..<400 {
            if (try? await session.data(from: URL(string: url + "/healthz")!)) != nil { return }
            try await realSleep(25)
        }
        close(); throw TestFailure("management relay did not start")
    }
    func close() {
        if process.isRunning {
            process.terminate()
            for _ in 0..<500 { if !process.isRunning { break }; usleep(10_000) }
            if process.isRunning { kill(process.processIdentifier, SIGKILL) }
        }
        try? FileManager.default.removeItem(at: directory)
    }
}

extension AuthorityWorkflowTests {

    @Test func openSignupUsesPowAndCorrectedTokenKeepsExactRegistration() async throws {
        for mode in ["token", "open"] {
            let server = try await ManagementRelay(signup: mode); defer { server.close() }
            let vault = AuthorityTestVault(), bootstrap = AuthorityBootstrap(storage: vault, gate: enabledGate())
            _ = try await bootstrap.prepareCreation(server: server.url)
            let before = try vault.readOperation().unwrap()
            try await bootstrap.confirmRecoveryCodeSaved()
            if mode == "token" {
                do { _ = try await bootstrap.continueCreation(setupToken: "wrong"); Issue.record("accepted wrong token") } catch {}
            }
            _ = try await bootstrap.continueCreation(setupToken: mode == "token" ? server.setupToken : "")
            let saved = try vault.readOperation().unwrap()
            let a = try PBRegisterDeviceRequest(serializedBytes: before.signed)
            let b = try PBRegisterDeviceRequest(serializedBytes: saved.signed)
            #expect(a.registration == b.registration && a.signature == b.signature)
            if mode == "open" { #expect(b.hasSignupProof) }
            try await bootstrap.finishCreation()
        }
    }


}

extension AuthorityWorkflowTests {
}

/// Opt-in real native CLI acceptance. Kept out of routine tests because it uses
/// the installed Codex login and makes one model request. All relay/daemon/tmux
/// state and working files belong to this temporary fixture.
extension AuthorityWorkflowTests {
}

extension AuthorityWorkflowTests {
    @Test func everyCreationMutationRpcCanLoseItsRequestOrAckWithoutNewGenesis() async throws {
        for path in ["/RegisterDevice", "/PinSecurityDomain", "/PutSecurityState"] {
            for after in [false, true] {
                let server = try await ManagementRelay(); defer { server.close() }
                let vault = AuthorityTestVault(), gate = enabledGate(), cut = Signal()
                let http = Intercept.http(for: server.url, answered: { request in
                    if after && request.url?.path.hasSuffix(path) == true && !cut.isSet {
                        cut.set(); gate.set(active: false, protectedDataAvailable: false)
                    }
                }) { request in
                    if !after && request.url?.path.hasSuffix(path) == true && !cut.isSet {
                        cut.set(); gate.set(active: false, protectedDataAvailable: false)
                        throw URLError(.networkConnectionLost)
                    }
                    return request
                }
                let bootstrap = AuthorityBootstrap(storage: vault, gate: gate, http: http)
                _ = try await bootstrap.prepareCreation(server: server.url)
                try await bootstrap.confirmRecoveryCodeSaved()
                let before = try vault.readOperation().unwrap()
                do { _ = try await bootstrap.continueCreation(setupToken: server.setupToken); Issue.record("missing injected failure") } catch {}
                #expect(cut.isSet)
                gate.set(active: true, protectedDataAvailable: true)
                let restart = AuthorityBootstrap(storage: vault, gate: gate)
                _ = try await restart.continueCreation(setupToken: server.setupToken)
                try await restart.finishCreation()
                #expect(try vault.readSecurity().pin.genesis == before.parent)
                #expect(Set(try vault.committedReceipts.map { try PBRegisterDeviceRequest(serializedBytes: $0.signed).signature }).count == 1)
            }
        }
    }


}

extension AuthorityWorkflowTests {
}
