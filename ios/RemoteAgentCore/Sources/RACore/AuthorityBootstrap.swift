import Connect
import CryptoKit
import Foundation
import RAProto

/// Completion keys only. The recovery credential displayed to the user is absent.
struct AuthorityMaterial: Codable, Sendable {
    let server: String
    let root: Data
    let seed: Data
    let x: Data
    let publicKey: Data
    let chain: Data
    func bytes() throws -> Data { try JSONEncoder().encode(self) }
    static func parse(_ data: Data) throws -> Self { try JSONDecoder().decode(Self.self, from: data) }
}
public struct AuthorityBootstrapStatus: Sendable {
    public let kind: SecurityOperation.Kind
    public let phase: SecurityOperation.Phase
    public let codeSaved: Bool
    public let readyMs: UInt64
    public let recoveryID: String
}

/// Available before login. C callers resume this receipt before ordinary pairing
/// registration/account loading; they never regenerate an already published draft.
public actor AuthorityBootstrap {
    let storage: any AuthorityStorage
    public nonisolated let gate: AuthorityGate
    let http: HTTP
    let clock: @Sendable () -> Int64
    var busy = false
    public init(storage: any AuthorityStorage, gate: AuthorityGate, http: HTTP = HTTP(),
                clock: @escaping @Sendable () -> Int64 = { Int64(Date().timeIntervalSince1970 * 1000) }) {
        self.storage = storage; self.gate = gate; self.http = http; self.clock = clock
    }
    func begin() throws -> UInt64 {
        try DeviceSecurity.check(!busy, .conflict)
        let t = try gate.ticket(); busy = true; return t
    }
    func rpc<T: Sendable>(_ t: UInt64, _ body: @escaping @Sendable () async throws -> T) async throws -> T {
        let (id, task) = try gate.launch(t, body)
        defer { gate.untrack(id) }
        let value = try await withTaskCancellationHandler { try await task.value } onCancel: { task.cancel() }
        try gate.check(t); return value
    }
    public func status() throws -> AuthorityBootstrapStatus? {
        guard let op = try storage.readOperation() else { return nil }
        var id = ""; var ready: UInt64 = 0
        if op.kind == .recovery && !op.result.isEmpty {
            let response = try PBSubmitRecoveryResponse(serializedBytes: op.result)
            id = response.recoveryID; ready = response.readyMs
        }
        return AuthorityBootstrapStatus(kind: op.kind, phase: op.phase, codeSaved: op.codeSaved, readyMs: ready, recoveryID: id)
    }
    /// Saves a fresh genesis and the exact account-signed registration before networking.
    /// Returns a code once. On restart continueCreation uses the same genesis.
    public func prepareCreation(server: String) throws -> String {
        let t = try begin(); defer { busy = false }
        try DeviceSecurity.check(storage.existingSecurity() == nil, .conflict)
        let checked = try Secret.checkServer(server)
        var displayed = ""
        _ = try storage.prepareOperation(expectedHead: nil) { committed in
            try DeviceSecurity.check(committed == nil, .conflict)
            return try gate.withPermit(t) {
                let root = randomBytes(32), seed = randomBytes(32), x = randomBytes(32)
                let keys = try Keys.derive(root: root), signer = try SigningKey(seed: seed)
                let accountID = deviceId(publicKey: keys.account.publicKey)
                let member = try Authority.newMember(signer: signer, xPrivate: x, kind: .client)
                let recovery = try Authority.newRecovery(), secret = Authority.newSecret()
                let genesis = try Authority().genesis(account: accountID, domain: randomBytes(32), member: member,
                    recoveryEd: recovery.publicKey, secret: secret, signer: signer, now: clock())
                let pin = try DeviceSecurity.pin(for: genesis)
                let record = try DeviceSecurity.Record(pin: pin, states: [1: genesis], memberID: member.id,
                    xPrivate: x, member: true, floor: genesis.core.issuedAt, secret: secret)
                var reg = PBDeviceRegistration(); reg.accountPublicKey = keys.account.publicKey
                reg.devicePublicKey = signer.publicKey; reg.kind = .client; reg.createdMs = Int64(clamping: genesis.core.issuedAt)
                var request = PBRegisterDeviceRequest(); request.registration = try reg.serializedData()
                request.signature = keys.account.sign(registerMessage(request.registration)); request.protocolVersion = protocolVersion
                let material = AuthorityMaterial(server: checked, root: root, seed: seed, x: x,
                    publicKey: keys.account.publicKey, chain: try record.bytes())
                displayed = try RecoveryCode.encode(seed: recovery.seed, genesis: pin.genesis)
                return SecurityOperation(kind: .creation, parent: pin.genesis, signed: try request.serializedData(),
                    local: try material.bytes(), codeSaved: false)
            }
        }
        return displayed
    }
    public func confirmRecoveryCodeSaved() throws {
        let t = try gate.ticket(); try gate.check(t)
        guard var op = try storage.readOperation(), op.kind == .creation || op.kind == .recovery else { throw DeviceSecurity.Failure.stale }
        let old = op; op.codeSaved = true
        try storage.replaceOperation(expected: old, next: op)
    }
    /// Signup credentials may be corrected, but the signed registration never changes.
    public func continueCreation(setupToken: String = "") async throws -> Account {
        let t = try begin(); defer { busy = false }
        guard var op = try storage.readOperation(), op.kind == .creation else { throw DeviceSecurity.Failure.stale }
        let material = try AuthorityMaterial.parse(op.local)
        let record = try DeviceSecurity.Record.parse(material.chain)
        let account = Account(server: material.server, root: material.root, deviceSeed: material.seed,
            deviceId: record.memberID, accountId: record.pin.account)
        let api = try Api(server: material.server, http: http)
        let token: String
        do { token = try await rpc(t) { try await login(api: api, account: account) } }
        catch let error as ConnectError where [.permissionDenied, .notFound, .unauthenticated].contains(error.code) {
            var req = try PBRegisterDeviceRequest(serializedBytes: op.signed)
            req.setupToken = setupToken; req.clearSignupProof()
            var next = op; next.signed = try req.serializedData()
            try storage.replaceOperation(expected: op, next: next); op = next
            do {
                let request = req
                let response = try await rpc(t) { try await api.auth.registerDevice(request: request).value() }
                try DeviceSecurity.check(response.deviceID == account.deviceId, .untrusted)
            } catch let failure as ConnectError where failure.code == .failedPrecondition {
                let proof = try await solveSignup(api: api, ticket: t)
                req.signupProof = proof
                var next = op; next.signed = try req.serializedData()
                try storage.replaceOperation(expected: op, next: next); op = next
                let request = req
                let response = try await rpc(t) { try await api.auth.registerDevice(request: request).value() }
                try DeviceSecurity.check(response.deviceID == account.deviceId, .untrusted)
            }
            token = try await rpc(t) { try await login(api: api, account: account) }
        }
        var pin = PBPinSecurityDomainRequest(); pin.genesisState = try DeviceSecurity.stateBytes(record.states[1]!)
        let request = pin
        try await rpc(t) { _ = try await api.client.pinSecurityDomain(request: request, headers: Api.headers(token: token)).value() }
        if op.phase == .prepared {
            var next = op; next.phase = .published
            try storage.replaceOperation(expected: op, next: next); op = next
        }
        // A relaunched draft cannot show the lost recovery seed. Adopt the original
        // genesis, expose codeSaved=false, and let C require an immediate rotation.
        try gate.check(t)
        try storage.adoptOperation(op, account: account, record: record,
            profile: AccountPublicProfile(publicKey: material.publicKey, pin: record.pin))
        try await publishBootstrapLease(op, account: account, api: api, token: token, ticket: t)
        return account
    }
    func publishBootstrapLease(_ saved: SecurityOperation, account: Account, api: Api, token: String, ticket t: UInt64) async throws {
        var op = saved
        let r = try storage.readSecurity()
        try DeviceSecurity.check(r.member && r.state.core.authorityID == account.deviceId, .untrusted)
        let target = try op.kind == .creation ? DeviceSecurity.Record.parse(AuthorityMaterial.parse(op.local).chain).state
            : DeviceSecurity.parseState(PBSubmitRecoveryRequest(serializedBytes: op.signed).state)
        try DeviceSecurity.check(DeviceSecurity.stateHash(r.state) == DeviceSecurity.stateHash(target), .conflict)
        let now = max(UInt64(max(clock(), 1)), r.floor, r.state.core.issuedAt, r.lease?.issuedAt ?? 0)
        let old = op.certificate.isEmpty ? nil : try DeviceSecurity.HeadLease.parse(op.certificate)
        if old == nil || now >= old!.notAfter {
            let lease = try gate.withPermit(t) { try DeviceSecurity.HeadLease.sign(r.state,
                signer: SigningKey(seed: account.deviceSeed), now: Int64(clamping: now), floor: old?.issuedAt ?? 0) }
            var next = op; next.certificate = try lease.bytes()
            try storage.replaceOperation(expected: op, next: next); op = next
        }
        var req = PBPutSecurityStateRequest(); req.lease = op.certificate
        let request = req
        try await rpc(t) { _ = try await api.client.putSecurityState(request: request, headers: Api.headers(token: token)).value() }
        var next = try storage.readSecurity()
        try DeviceSecurity.check(DeviceSecurity.stateHash(next.state) == DeviceSecurity.stateHash(target), .conflict)
        next.lease = try DeviceSecurity.HeadLease.parse(op.certificate); next.floor = max(next.floor, now)
        _ = try storage.writeSecurity(expectedHead: DeviceSecurity.stateHash(target), next)
    }
    public func finishCreation() throws {
        guard var op = try storage.readOperation(), op.kind == .creation, op.phase.rawValue >= SecurityOperation.Phase.published.rawValue else { throw DeviceSecurity.Failure.stale }
        let r = try storage.readSecurity()
        try DeviceSecurity.check(r.pin.genesis == op.parent, .conflict)
        // UI must either confirm the backup or explicitly acknowledge a lost display
        // and rotate immediately using finishCreationWithoutCodeBackup.
        try DeviceSecurity.check(op.codeSaved, .untrusted)
        let old = op; op.phase = .delivered
        try storage.replaceOperation(expected: old, next: op); try storage.replaceOperation(expected: op, next: nil)
    }
    public func finishCreationWithoutCodeBackup(acknowledgeRotationRequired: Bool) throws {
        try DeviceSecurity.check(acknowledgeRotationRequired, .untrusted)
        guard let op = try storage.readOperation(), op.kind == .creation, op.phase.rawValue >= SecurityOperation.Phase.published.rawValue else { throw DeviceSecurity.Failure.stale }
        let r = try storage.readSecurity(); try DeviceSecurity.check(r.pin.genesis == op.parent, .conflict)
        try storage.replaceOperation(expected: op, next: nil)
    }
    func solveSignup(api: Api, ticket t: UInt64) async throws -> PBSignupProof {
        let challenge = try await rpc(t) { try await api.auth.getSignupChallenge(request: .init()).value() }
        try DeviceSecurity.check(challenge.challenge.count == 32 && challenge.difficultyBits <= 28, .capacity)
        let task = Task.detached { [clock, gate] in
            var attempt: UInt64 = 0
            while true {
                if attempt & 0xffff == 0 {
                    try Task.checkCancellation(); try gate.check(t)
                    try DeviceSecurity.check(clock() < challenge.expiresMs, .stale)
                }
                var n = attempt.bigEndian
                let nonce = withUnsafeBytes(of: &n) { Data($0) }
                let hash = sha256(challenge.challenge + nonce)
                var zeros = 0
                for byte in hash { if byte == 0 { zeros += 8 } else { zeros += byte.leadingZeroBitCount; break } }
                if zeros >= challenge.difficultyBits {
                    var proof = PBSignupProof(); proof.challenge = challenge.challenge; proof.nonce = nonce; return proof
                }
                attempt &+= 1
            }
        }
        return try await rpc(t) { try await withTaskCancellationHandler { try await task.value } onCancel: { task.cancel() } }
    }
}
