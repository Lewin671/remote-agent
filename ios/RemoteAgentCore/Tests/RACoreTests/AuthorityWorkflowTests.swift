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
private func coordinator(_ account: Account, _ vault: AuthorityTestVault, http: HTTP = HTTP(),
                         gate: AuthorityGate = enabledGate(), clock: @escaping @Sendable () -> Int64 = { Int64(Date().timeIntervalSince1970 * 1000) }) throws -> AuthorityCoordinator {
    let api = try Api(server: account.server, http: http)
    let security = DeviceSecurityClient(store: vault, memberID: account.deviceId, signingKey: try SigningKey(seed: account.deviceSeed),
        clock: { UInt64(max(clock(), 1)) }, fetchStates: { after in
            let token = try await login(api: api, account: account)
            var request = PBGetSecurityStatesRequest(); request.afterEpoch = after; request.limit = 8
            return try await api.client.getSecurityStates(request: request, headers: Api.headers(token: token)).value()
        })
    return AuthorityCoordinator(account: account, api: api, storage: vault, security: security, gate: gate, clock: clock,
        token: { try await login(api: api, account: account) })
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

    @Test func pairingDeliveryRemovalAndPlatformIndependentHandoff() async throws {
        let server = try await ManagementRelay(); defer { server.close() }
        var step = "create"
        do {
        let (account, vault, _) = try await createAuthority(server)
        let authority = try coordinator(account, vault)
        step = "start"
        let initiation = try await authority.startPairing()
        #expect(PairLink.parse(initiation.link.url(web: "https://example.com")) == initiation.link)
        let joinedVault = AuthorityTestVault(), candidateConfirmed = Signal()
        let receipt = AuthorityRegistrationReceipt()
        let joining = Task {
            try await joinByPairing(link: initiation.link, http: HTTP(), onEmoji: { _ in }, stopped: { false }, sleep: { _ in try await realSleep(10) },
                persist: { account, record in
                    try joinedVault.initializeSecurity(record)
                    try joinedVault.savePublicProfile(receipt.load().publicProfile)
                    joinedVault.lock.withLock { joinedVault.account = account }
                }, persistRegistration: { try receipt.save($0) }, persistIdentity: { _ in },
                confirm: { _ in candidateConfirmed.set(); return true })
        }
        defer { joining.cancel() }
        var candidate: PairCandidate?
        for _ in 0..<500 {
            candidate = try await authority.pairingCandidate(initiation)
            if candidate != nil { break }; try await realSleep(10)
        }
        step = "candidate"
        let incoming = try candidate.unwrap()
        for _ in 0..<500 { if candidateConfirmed.isSet { break }; try await realSleep(10) }
        #expect(candidateConfirmed.isSet)
        step = "approve"
        try await authority.approvePairing(incoming, sasConfirmed: true)
        step = "join"
        let joined = try await joining.value
        // Exact durable delivery stays until DELIVERED, including if the joiner finished meanwhile.
        step = "receipt"
        try await authority.resumeSecurityOperation()
        #expect(try vault.readOperation() == nil)
        let newAuthority = try coordinator(joined, joinedVault)
        step = "prepare handoff"
        let code = try await newAuthority.prepareHandoffCode()
        do { try await newAuthority.offerHandoff(code, codeSaved: false); Issue.record("offer without backup") } catch {}
        step = "offer"
        try await newAuthority.offerHandoff(code, codeSaved: true)
        let remote = try Remote(account: account, http: HTTP(), securityStorage: vault)
        step = "targets"
        let targets = try await remote.handoffCandidates()
        #expect(targets.contains { $0.id == joined.deviceId }) // missing platform is accepted
        let prior = try vault.readSecurity()
        let other = try Authority.newMember(signer: SigningKey(seed: randomBytes(32)), xPrivate: randomBytes(32), kind: .machine)
        var fake = RAProto.PBDeviceMeta(); fake.platform = "android"; fake.handoffRecoveryEd = randomBytes(32)
        #expect(try await authority.handoffCandidates([DeviceView(id: other.id, kind: .machine, createdMs: 0, meta: fake)]).isEmpty)
        step = "handoff"
        try await authority.handoffAuthority(incoming: joined.deviceId, offer: code.publicKey, verifiedHead: DeviceSecurity.stateHash(prior.state))
        #expect(try !vault.readSecurity().member)
        step = "new lease"
        try await newAuthority.renewLeaseIfNeeded()
        let adopted = try joinedVault.readSecurity()
        #expect(adopted.state.core.authorityID == joined.deviceId)
        #expect(adopted.lease?.head == (try DeviceSecurity.stateHash(adopted.state)))
        #expect(try DeviceSecurity.openPrevSecret(newSecret: adopted.secret!, blob: adopted.state.prevSecret) == prior.secret)
        } catch { throw TestFailure("pair workflow at \(step): \(error)") }
    }

    @Test func expiredDeliveryHasExplicitRemovalExit() async throws {
        let server = try await ManagementRelay(); defer { server.close() }
        let (account, vault, _) = try await createAuthority(server)
        let c = try coordinator(account, vault)
        // An approved member with a deliberately unavailable delivery slot.
        let r = try vault.readSecurity(), signer = try SigningKey(seed: account.deviceSeed)
        let member = try Authority.newMember(signer: SigningKey(seed: randomBytes(32)), xPrivate: randomBytes(32), kind: .client)
        let state = try Authority().pair(parent: r.state, candidate: member, oldSecret: r.secret!, newSecret: randomBytes(32), signer: signer, now: Int64(r.floor))
        var approval = PBPairApproval(); approval.candidateDevicePublicKey = member.ed
        var delivery = PBApprovePairingRequest(); delivery.pairingID = String(repeating: "a", count: 32); delivery.approval = try approval.serializedData()
        var req = PBPutSecurityStateRequest(); var value = PBSecurityState(); value.state = try DeviceSecurity.stateBytes(state)
        req.states = [value]; req.lease = try DeviceSecurity.HeadLease.sign(state, signer: signer, now: Int64(r.floor)).bytes()
        _ = try vault.prepareOperation(expectedHead: DeviceSecurity.stateHash(r.state)) { _ in
            SecurityOperation(kind: .state, parent: state.core.parent, signed: try req.serializedData(), delivery: try delivery.serializedData())
        }
        do { try await c.resumeSecurityOperation(); Issue.record("missing pair slot accepted") } catch {}
        #expect(try vault.readOperation()?.phase == .published)
        #expect(try vault.readSecurity().state.core.member(member.id) != nil)
        try await c.removeDevice(member.id, abandonPairDelivery: true)
        #expect(try vault.readOperation() == nil)
        #expect(try vault.readSecurity().state.core.removed.contains(member.id))
    }

}

private final class AuthorityRegistrationReceipt: @unchecked Sendable {
    let lock = NSLock()
    var receipt: RegistrationIntent?
    func save(_ receipt: RegistrationIntent) throws { lock.withLock { self.receipt = receipt } }
    func load() throws -> RegistrationIntent { try lock.withLock { try receipt.unwrap() } }
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

    @Test func stateAckLossBeyond72HoursRenewsOnlyIndependentLeaseAndClockRollbackUsesFloor() async throws {
        let server = try await ManagementRelay(); defer { server.close() }
        let vault = AuthorityTestVault(), gate = enabledGate()
        let past = Int64(Date().timeIntervalSince1970 * 1000) - 4 * 24 * 60 * 60 * 1000
        let old = AuthorityBootstrap(storage: vault, gate: gate, clock: { past })
        _ = try await old.prepareCreation(server: server.url)
        try await old.confirmRecoveryCodeSaved()
        let bootstrap = AuthorityBootstrap(storage: vault, gate: gate)
        let account = try await bootstrap.continueCreation(setupToken: server.setupToken)
        try await bootstrap.finishCreation()
        let r = try vault.readSecurity(), signer = try SigningKey(seed: account.deviceSeed)
        let recovery = try Authority.newRecovery()
        let target = try Authority().veto(parent: r.state, newRecoveryEd: recovery.publicKey, secret: r.secret!, signer: signer, now: past)
        var req = PBPutSecurityStateRequest(); var state = PBSecurityState(); state.state = try DeviceSecurity.stateBytes(target)
        req.states = [state]; req.lease = try DeviceSecurity.HeadLease.sign(target, signer: signer, now: past).bytes()
        let original = try req.serializedData()
        _ = try vault.prepareOperation(expectedHead: DeviceSecurity.stateHash(r.state)) { _ in
            SecurityOperation(kind: .state, parent: target.core.parent, signed: original)
        }
        let cut = Signal()
        let http = Intercept.http(for: server.url, answered: { ask in
            if ask.url?.path.hasSuffix("/PutSecurityState") == true, !cut.isSet {
                cut.set(); gate.set(active: false, protectedDataAvailable: false)
            }
        }) { $0 }
        let authority = try coordinator(account, vault, http: http, gate: gate)
        do { try await authority.resumeSecurityOperation(); Issue.record("ack should be lost") } catch {}
        let pending = try vault.readOperation().unwrap()
        #expect(try PBPutSecurityStateRequest(serializedBytes: pending.signed).states == req.states)
        #expect(pending.signed != original)
        gate.set(active: true, protectedDataAvailable: true)
        let restarted = try coordinator(account, vault, gate: gate, clock: { 1 })
        try await restarted.resumeSecurityOperation()
        #expect(try vault.readOperation() == nil)
        #expect(try DeviceSecurity.stateHash(vault.readSecurity().state) == DeviceSecurity.stateHash(target))
        let installed = try vault.readSecurity()
        #expect(installed.lease!.issuedAt >= r.floor)
        #expect(installed.lease!.issuedAt >= target.core.issuedAt)
    }

    @Test func competingVerifiedParentStopsOriginalStateWithoutResigning() async throws {
        let server = try await ManagementRelay(); defer { server.close() }
        let (account, vault, _) = try await createAuthority(server)
        let r = try vault.readSecurity(), signer = try SigningKey(seed: account.deviceSeed)
        let a = try Authority().veto(parent: r.state, newRecoveryEd: randomBytes(32), secret: r.secret!, signer: signer, now: Int64(r.floor))
        let b = try Authority().veto(parent: r.state, newRecoveryEd: randomBytes(32), secret: r.secret!, signer: signer, now: Int64(r.floor))
        var first = PBPutSecurityStateRequest(); var item = PBSecurityState(); item.state = try DeviceSecurity.stateBytes(a); first.states = [item]
        let receipt = try vault.prepareOperation(expectedHead: DeviceSecurity.stateHash(r.state)) { _ in
            SecurityOperation(kind: .state, parent: a.core.parent, signed: try first.serializedData())
        }
        let api = try Api(server: server.url, http: HTTP()), token = try await login(api: api, account: account)
        var competing = PBPutSecurityStateRequest(); item.state = try DeviceSecurity.stateBytes(b); competing.states = [item]
        competing.lease = try DeviceSecurity.HeadLease.sign(b, signer: signer, now: Int64(r.floor)).bytes()
        _ = try await api.client.putSecurityState(request: competing, headers: Api.headers(token: token)).value()
        let authority = try coordinator(account, vault)
        do { try await authority.resumeSecurityOperation(); Issue.record("competing successor accepted") } catch {}
        #expect(try vault.readOperation() == receipt)
        #expect(try DeviceSecurity.stateHash(vault.readSecurity().state) == DeviceSecurity.stateHash(b))
    }
}

extension AuthorityWorkflowTests {
}

/// Opt-in real native CLI acceptance. Kept out of routine tests because it uses
/// the installed Codex login and makes one model request. All relay/daemon/tmux
/// state and working files belong to this temporary fixture.
extension AuthorityWorkflowTests {
    @Test(.enabled(if: ProcessInfo.processInfo.environment["RA_TEST_REAL_CODEX"] == "1"))
    func realDaemonCliRoundTripAndRemovedMachineArchiveRejoin() async throws {
        let server = try await ManagementRelay(); defer { server.close() }
        let (account, vault, _) = try await createAuthority(server)
        let remote = try Remote(account: account, http: HTTP(), securityStorage: vault)
        defer { remote.close() }
        remote.authorityGate.set(active: true, protectedDataAvailable: true)
        let authority = try await remote.authorityCoordinator()
        let cli = try ManagementCLI(directory: server.directory)
        defer { cli.close() }
        func pair(replaces: String = "") async throws -> String {
            let start = try await authority.startPairing(replacesDeviceID: replaces)
            let process = try cli.launch(["account", "join", "--server", server.url, "--code", start.link.code], input: "y\n")
            var candidate: PairCandidate?
            for _ in 0..<1000 {
                candidate = try await authority.pairingCandidate(start)
                if candidate != nil { break }; try await realSleep(10)
            }
            let value = try candidate.unwrap()
            #expect(value.kind == .machine)
            // The native CLI reads the fixture's explicit local SAS confirmation.
            try await realSleep(100)
            try await authority.approvePairing(value, sasConfirmed: true)
            try await cli.wait(process)
            try await authority.resumeSecurityOperation()
            return value.member.id
        }
        let machine = try await pair()
        try cli.startDaemon()
        remote.start()
        for _ in 0..<1000 {
            _ = await remote.refresh()
            if remote.state.value.machines[machine]?.online == true { break }; try await realSleep(10)
        }
        try #require(remote.state.value.machines[machine]?.online == true)
        var create = PBCreateSessionRequest(); create.cwd = cli.work.path; create.agent = .codex
        create.prompt = "Reply exactly RA_AUTHORITY_ROUNDTRIP_OK. Do not run tools or change files."
        var command = PBCommand(); command.createSession = create
        let result = try await remote.machineCommand(machine, command)
        let sid = result.createSession.session.id
        #expect(!sid.isEmpty)
        let feed = remote.events(sid)
        let follow = Task { for await _ in feed.updates() { if Task.isCancelled { break } } }
        defer { follow.cancel() }
        var answered = false
        for _ in 0..<1200 {
            remote.retryEvents(sid)
            if feed.value.events.contains(where: { $0.agentMessage.text.contains("RA_AUTHORITY_ROUNDTRIP_OK") }) { answered = true; break }
            try await realSleep(100)
        }
        #expect(answered, "real Codex response must return through daemon and relay to Swift Remote")
        let seq = remote.state.value.sessions[sid]?.lastSeq ?? 0
        try await authority.removeDevice(machine)
        let api = try Api(server: server.url, http: HTTP()), tok = try await remote.currentToken()
        var get = PBListRemoteSessionsRequest(); get.sessionIds = [sid]
        let archive = try await api.client.listRemoteSessions(request: get, headers: Api.headers(token: tok)).value()
        #expect(archive.sessions.contains { $0.sessionID == sid })
        cli.stopDaemon()
        let replacement = try await pair(replaces: machine)
        #expect(replacement != machine)
        try cli.startDaemon()
        var retained = false
        for _ in 0..<1000 {
            _ = await remote.refresh()
            if remote.state.value.sessions[sid]?.machineId == replacement { retained = true; break }
            try await realSleep(25)
        }
        #expect(retained)
        #expect((remote.state.value.sessions[sid]?.lastSeq ?? 0) >= seq)
        try await authority.removeDevice(replacement)
        try await authority.deleteMachineRecords(replacement)
        let deleted = try await api.client.listRemoteSessions(request: get, headers: Api.headers(token: tok)).value()
        #expect(!deleted.sessions.contains { $0.sessionID == sid })
    }
}

private final class ManagementCLI {
    let directory: URL
    let work: URL
    let home: URL
    let binary: URL
    let tmux = "ra-management-" + UUID().uuidString
    var children: [Process] = []
    var daemon: Process?
    init(directory: URL) throws {
        self.directory = directory; work = directory.appendingPathComponent("work")
        home = URL(fileURLWithPath: "/tmp/ra-m-" + String(UUID().uuidString.prefix(8)))
        try FileManager.default.createDirectory(at: work, withIntermediateDirectories: true)
        binary = directory.appendingPathComponent("remote-agent")
        let build = Process(); build.executableURL = URL(fileURLWithPath: try GoServer.go.unwrap())
        build.currentDirectoryURL = repoRoot; build.arguments = ["build", "-o", binary.path, "./cmd/remote-agent"]
        build.standardOutput = FileHandle.nullDevice; build.standardError = FileHandle.nullDevice
        try build.run(); build.waitUntilExit()
        guard build.terminationStatus == 0 else { throw TestFailure("could not build native management CLI") }
    }
    func launch(_ arguments: [String], input: String = "") throws -> Process {
        let p = Process(); p.executableURL = binary; p.arguments = arguments; p.currentDirectoryURL = work
        var env = ProcessInfo.processInfo.environment; env["REMOTE_AGENT_HOME"] = home.path; env["REMOTE_AGENT_TMUX_SOCKET"] = tmux
        p.environment = env
        let log = directory.appendingPathComponent("cli-\(children.count).log")
        FileManager.default.createFile(atPath: log.path, contents: nil)
        let handle = try FileHandle(forWritingTo: log)
        p.standardOutput = handle; p.standardError = handle
        let pipe = Pipe(); p.standardInput = pipe
        try p.run(); children.append(p)
        try pipe.fileHandleForWriting.write(contentsOf: Data(input.utf8)); try pipe.fileHandleForWriting.close()
        return p
    }
    func wait(_ p: Process) async throws {
        for _ in 0..<3000 {
            if !p.isRunning { guard p.terminationStatus == 0 else { throw TestFailure("native CLI failed; inspect isolated test log") }; return }
            try await realSleep(10)
        }
        throw TestFailure("native CLI timed out")
    }
    func startDaemon() throws { daemon = try launch(["daemon", "--codex", try executable("codex")]) }
    func stopDaemon() { if let daemon, daemon.isRunning { daemon.terminate(); daemon.waitUntilExit() }; daemon = nil }
    func executable(_ name: String) throws -> String {
        for root in (ProcessInfo.processInfo.environment["PATH"] ?? "").split(separator: ":") {
            let candidate = String(root) + "/" + name
            if FileManager.default.isExecutableFile(atPath: candidate) { return candidate }
        }
        throw TestFailure("missing native executable")
    }
    func close() {
        stopDaemon()
        try? FileManager.default.removeItem(at: home)
        for p in children where p.isRunning { p.terminate() }
        if let path = try? executable("tmux") {
            let p = Process(); p.executableURL = URL(fileURLWithPath: path); p.arguments = ["-L", tmux, "kill-server"]
            p.standardOutput = FileHandle.nullDevice; p.standardError = FileHandle.nullDevice
            try? p.run(); p.waitUntilExit()
        }
    }
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
    @Test func sameHeadRenewalAtIdenticalMillisecondReusesTheCommittedCertificate() async throws {
        let server = try await ManagementRelay(); defer { server.close() }
        let (account, vault, _) = try await createAuthority(server)
        let committed = try vault.readSecurity().lease.unwrap()
        let authority = try coordinator(account, vault, clock: { Int64(committed.issuedAt) })
        try await authority.renewLeaseNow()
        #expect(try vault.readOperation() == nil)
        #expect(try vault.readSecurity().lease?.bytes() == committed.bytes())
        let r = try vault.readSecurity()
        var req = PBPutSecurityStateRequest()
        req.lease = try DeviceSecurity.HeadLease.sign(r.state, signer: SigningKey(seed: account.deviceSeed),
            now: Int64(committed.issuedAt) - 1).bytes()
        // An obsolete independent lease receipt reconciles to a newer committed
        // certificate rather than signing a same-time competing certificate.
        _ = try vault.prepareOperation(expectedHead: committed.head) { _ in
            SecurityOperation(kind: .lease, parent: committed.head, signed: try req.serializedData())
        }
        try await authority.resumeSecurityOperation()
        #expect(try vault.readOperation() == nil)
        #expect(try vault.readSecurity().lease?.bytes() == committed.bytes())
    }
}
