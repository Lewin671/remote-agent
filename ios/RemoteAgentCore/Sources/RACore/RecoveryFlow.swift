import Connect
import CryptoKit
import Foundation
import RAProto

public struct RecoveryContext: Sendable {
    public let profile: AccountPublicProfile
    public let states: [DeviceSecurity.State]
    public let proofs: [UInt64: DeviceSecurity.RecoveryStatement]
    public let phase: PBGetRecoveryContextResponse.Phase
    public let readyMs: UInt64
    public let recoveryID: String
    public var head: DeviceSecurity.State { states.last! }
    let raw: Data
    init(_ response: PBGetRecoveryContextResponse, genesisPrefix: Data?, now: UInt64) throws {
        try DeviceSecurity.check(!response.states.isEmpty && response.states.count <= DeviceSecurity.maxStates, .capacity)
        states = try response.states.map { try DeviceSecurity.parseState($0.state) }
        let pin = try DeviceSecurity.pin(for: states[0])
        profile = try AccountPublicProfile(publicKey: response.accountPublicKey, pin: pin)
        try DeviceSecurity.check(response.accountID == pin.account, .untrusted)
        if let genesisPrefix { try DeviceSecurity.check(genesisPrefix.count == 10 && pin.genesis.prefix(10) == genesisPrefix, .untrusted) }
        let parsed = try response.states.map { $0.recovery.isEmpty ? nil : try DeviceSecurity.RecoveryStatement.parse($0.recovery) }
        try DeviceSecurity.verifyChain(pin: pin, states: states, proofs: parsed)
        try DeviceSecurity.check(states.allSatisfy { $0.core.issuedAt <= now + DeviceSecurity.clockSkewLimit }, .clock)
        var proofs: [UInt64: DeviceSecurity.RecoveryStatement] = [:]
        for (i, state) in states.enumerated() { if let proof = parsed[i] { proofs[state.core.epoch] = proof } }
        self.proofs = proofs; phase = response.phase; readyMs = response.readyMs
        recoveryID = response.recoveryID; raw = try response.serializedData()
    }
}

extension AuthorityBootstrap {
    func context(api: Api, recoveryEd: Data, signerSeed: Data, recoveryID: String = "",
                 genesisPrefix: Data?, ticket t: UInt64) async throws -> RecoveryContext {
        let challenge = try await rpc(t) { try await api.recovery.getRecoveryChallenge(request: .init()).value().challenge }
        try DeviceSecurity.check(challenge.count == 32 && (recoveryEd.isEmpty || recoveryEd.count == 32), .untrusted)
        var ask = PBGetRecoveryContextRequest(); ask.recoveryEd = recoveryEd; ask.recoveryID = recoveryID; ask.challenge = challenge
        ask.signature = try gate.withPermit(t) { try SigningKey(seed: signerSeed).sign(
            Data("ra3/recovery-context\0".utf8) + challenge + (recoveryEd.isEmpty ? Data(repeating: 0, count: 32) : recoveryEd) + Data(recoveryID.utf8)) }
        let request = ask
        let response = try await rpc(t) { try await api.recovery.getRecoveryContext(request: request).value() }
        return try RecoveryContext(response, genesisPrefix: genesisPrefix, now: UInt64(max(clock(), 1)))
    }
    public func recoveryContext(server: String, code: String) async throws -> RecoveryContext {
        let t = try begin(); defer { busy = false }
        guard let decoded = RecoveryCode.decode(code) else { throw PairError("恢复码无效") }
        let api = try Api(server: server, http: http)
        return try await context(api: api, recoveryEd: SigningKey(seed: decoded.seed).publicKey,
            signerSeed: decoded.seed, genesisPrefix: decoded.genesis, ticket: t)
    }
    /// This path queries only challenge/context. It does not submit recovery or persist the seed.
    public func completeAccountPublicProfile(server: String, code: String) async throws {
        let t = try begin(); defer { busy = false }
        guard let decoded = RecoveryCode.decode(code) else { throw PairError("恢复码无效") }
        let api = try Api(server: server, http: http)
        let chain = try await context(api: api, recoveryEd: SigningKey(seed: decoded.seed).publicKey,
            signerSeed: decoded.seed, genesisPrefix: decoded.genesis, ticket: t)
        let local = try storage.readSecurity()
        try DeviceSecurity.check(local.pin == chain.profile.pin && chain.states.count >= local.states.count
            && DeviceSecurity.stateHash(chain.states[Int(local.headEpoch) - 1]) == DeviceSecurity.stateHash(local.state), .conflict)
        try storage.savePublicProfile(chain.profile)
    }
    /// Persist the exact signed recovery, device/X keys and new public recovery key;
    /// neither the old nor new recovery seed is persisted. Submit can be resumed.
    public func prepareEmergencyRecovery(server: String, code: String) async throws -> String {
        let t = try begin(); defer { busy = false }
        try DeviceSecurity.check(storage.readOperation() == nil, .conflict)
        guard let decoded = RecoveryCode.decode(code) else { throw PairError("恢复码无效") }
        let api = try Api(server: server, http: http)
        let chain = try await context(api: api, recoveryEd: SigningKey(seed: decoded.seed).publicKey,
            signerSeed: decoded.seed, genesisPrefix: decoded.genesis, ticket: t)
        try DeviceSecurity.check([.unspecified, .pending, .ready].contains(chain.phase), .untrusted)
        // A local pin, if present, may only be extended, never reset by recovery.
        if let local = try storage.existingSecurity() {
            try DeviceSecurity.check(local.pin == chain.profile.pin && chain.states.count >= local.states.count
                && DeviceSecurity.stateHash(chain.states[Int(local.headEpoch) - 1]) == DeviceSecurity.stateHash(local.state), .conflict)
        }
        let floor = try storage.existingSecurity()?.floor ?? 0
        var displayed = ""
        _ = try storage.prepareOperation(expectedHead: nil) { committed in
            if let committed {
                try DeviceSecurity.check(committed.pin == chain.profile.pin && chain.states.count >= committed.states.count
                    && DeviceSecurity.stateHash(chain.states[Int(committed.headEpoch) - 1]) == DeviceSecurity.stateHash(committed.state), .conflict)
            }
            return try gate.withPermit(t) {
                let seed = randomBytes(32), x = randomBytes(32), recovery = try Authority.newRecovery()
                let signer = try SigningKey(seed: seed)
                let member = try Authority.newMember(signer: signer, xPrivate: x, kind: .client)
                let members = chain.head.core.members.filter { $0.id != chain.head.core.authorityID } + [member]
                let result = try Authority().recovery(parent: chain.head, newMember: member, members: members,
                    secret: Authority.newSecret(), newSigner: signer, oldRecoverySeed: decoded.seed,
                    newRecoveryEd: recovery.publicKey, now: Int64(clamping: max(UInt64(max(clock(), 1)), floor, committed?.floor ?? 0)))
                var req = PBSubmitRecoveryRequest(); req.statement = try result.statement.bytes(); req.state = try DeviceSecurity.stateBytes(result.state)
                let material = AuthorityMaterial(server: api.server, root: Data(), seed: seed, x: x,
                    publicKey: chain.profile.publicKey, chain: chain.raw)
                displayed = try RecoveryCode.encode(seed: recovery.seed, genesis: chain.profile.pin.genesis)
                return SecurityOperation(kind: .recovery, parent: try DeviceSecurity.stateHash(chain.head),
                    signed: try req.serializedData(), local: try material.bytes(), codeSaved: false)
            }
        }
        return displayed
    }
    public func submitRecovery() async throws -> AuthorityBootstrapStatus {
        let t = try begin(); defer { busy = false }
        try await submitRecovery(t)
        return try status().unwrap()
    }
    func submitRecovery(_ t: UInt64) async throws {
        guard var op = try storage.readOperation(), op.kind == .recovery else { throw DeviceSecurity.Failure.stale }
        if !op.result.isEmpty { return }
        let material = try AuthorityMaterial.parse(op.local), api = try Api(server: material.server, http: http)
        let request = try PBSubmitRecoveryRequest(serializedBytes: op.signed)
        let response = try await rpc(t) { try await api.recovery.submitRecovery(request: request).value() }
        var next = op; next.result = try response.serializedData(); next.phase = .published
        try storage.replaceOperation(expected: op, next: next); op = next
    }
    public func resumeRecovery() async throws -> RecoveryContext {
        let t = try begin(); defer { busy = false }
        try await submitRecovery(t)
        let op = try storage.readOperation().unwrap(), material = try AuthorityMaterial.parse(op.local)
        let result = try PBSubmitRecoveryResponse(serializedBytes: op.result)
        let original = try RecoveryContext(PBGetRecoveryContextResponse(serializedBytes: material.chain), genesisPrefix: nil,
            now: UInt64(max(clock(), 1)))
        return try await context(api: Api(server: material.server, http: http), recoveryEd: Data(), signerSeed: material.seed,
            recoveryID: result.recoveryID, genesisPrefix: original.profile.pin.genesis.prefix(10), ticket: t)
    }
    /// Only an authenticated, definitively rejected request may be abandoned for a new application.
    public func discardRejectedRecovery(confirmedAfterDefinitiveRejection: Bool = false) async throws {
        let t = try begin(); defer { busy = false }
        guard let op = try storage.readOperation(), op.kind == .recovery else { throw DeviceSecurity.Failure.stale }
        if op.result.isEmpty {
            // Submit's ack can be lost and then vetoed before the retry. There is
            // no recovery ID to poll. Only an explicit exit after a definitive
            // rejection of the exact saved submission may release this draft.
            try DeviceSecurity.check(confirmedAfterDefinitiveRejection, .untrusted)
            let material = try AuthorityMaterial.parse(op.local), api = try Api(server: material.server, http: http)
            let request = try PBSubmitRecoveryRequest(serializedBytes: op.signed)
            do {
                let response = try await rpc(t) { try await api.recovery.submitRecovery(request: request).value() }
                var next = op; next.result = try response.serializedData(); next.phase = .published
                try storage.replaceOperation(expected: op, next: next)
                throw DeviceSecurity.Failure.stale
            } catch let rejected as ConnectError where [.permissionDenied, .failedPrecondition, .notFound].contains(rejected.code) {
                try storage.replaceOperation(expected: op, next: nil); return
            }
        }
        let material = try AuthorityMaterial.parse(op.local), result = try PBSubmitRecoveryResponse(serializedBytes: op.result)
        let original = try PBGetRecoveryContextResponse(serializedBytes: material.chain)
        let pin = try DeviceSecurity.pin(for: DeviceSecurity.parseState(original.states[0].state))
        let chain = try await context(api: Api(server: material.server, http: http), recoveryEd: Data(), signerSeed: material.seed,
            recoveryID: result.recoveryID, genesisPrefix: pin.genesis.prefix(10), ticket: t)
        try DeviceSecurity.check(chain.phase == .rejected, .untrusted)
        try storage.replaceOperation(expected: op, next: nil)
    }
    public func completeRecovery() async throws -> Account {
        let t = try begin(); defer { busy = false }
        try await submitRecovery(t)
        var op = try storage.readOperation().unwrap()
        let material = try AuthorityMaterial.parse(op.local), result = try PBSubmitRecoveryResponse(serializedBytes: op.result)
        let api = try Api(server: material.server, http: http)
        let original = try PBGetRecoveryContextResponse(serializedBytes: material.chain)
        let pin = try DeviceSecurity.pin(for: DeviceSecurity.parseState(original.states[0].state))
        var request = PBCompleteRecoveryRequest(); request.recoveryID = result.recoveryID
        // CryptoKit signatures are randomized: persist the exact complete request too.
        if op.completion.isEmpty {
            request.signature = try gate.withPermit(t) { try SigningKey(seed: material.seed).sign(Data("ra3/recovery-complete\0".utf8) + Data(result.recoveryID.utf8)) }
            var next = op; next.completion = try request.serializedData()
            try storage.replaceOperation(expected: op, next: next); op = next
        }
        let ask = try PBCompleteRecoveryRequest(serializedBytes: op.completion)
        let response = try await rpc(t) { try await api.recovery.completeRecovery(request: ask).value() }
        let chain = try await context(api: api, recoveryEd: Data(), signerSeed: material.seed,
            recoveryID: result.recoveryID, genesisPrefix: pin.genesis.prefix(10), ticket: t)
        let target = try DeviceSecurity.parseState(PBSubmitRecoveryRequest(serializedBytes: op.signed).state)
        let signer = try SigningKey(seed: material.seed), id = deviceId(publicKey: signer.publicKey)
        try DeviceSecurity.check(chain.phase == .published && chain.profile.publicKey == material.publicKey
            && response.accountPublicKey == material.publicKey && response.deviceID == id
            && chain.head.core.authorityID == id && DeviceSecurity.stateHash(chain.head) == DeviceSecurity.stateHash(target), .conflict)
        let secret = try DeviceSecurity.openGrant(pin, chain.head, recipient: id,
            x: Curve25519.KeyAgreement.PrivateKey(rawRepresentation: material.x))
        let record = try DeviceSecurity.Record(pin: pin, states: Dictionary(uniqueKeysWithValues: chain.states.map { ($0.core.epoch, $0) }),
            memberID: id, xPrivate: material.x, member: true, floor: max(UInt64(max(clock(), 1)), target.core.issuedAt),
            secret: secret, recovery: chain.proofs)
        let account = Account(server: material.server, root: Data(), deviceSeed: material.seed, deviceId: id, accountId: pin.account)
        try storage.adoptOperation(op, account: account, record: record, profile: chain.profile)
        let token = try await rpc(t) { try await login(api: api, account: account) }
        try await publishBootstrapLease(op, account: account, api: api, token: token, ticket: t)
        op = try storage.readOperation().unwrap()
        var next = op; next.phase = .delivered
        try storage.replaceOperation(expected: op, next: next)
        // Retain the delivered receipt until C acknowledges adoption and codeSaved.
        return account
    }
    public func finishRecovery(acknowledgeMissingCodeBackup: Bool = false) throws {
        guard let op = try storage.readOperation(), op.kind == .recovery, op.phase == .delivered else { throw DeviceSecurity.Failure.stale }
        try DeviceSecurity.check(op.codeSaved || acknowledgeMissingCodeBackup, .untrusted)
        let material = try AuthorityMaterial.parse(op.local)
        let r = try storage.readSecurity()
        try DeviceSecurity.check(r.memberID == deviceId(publicKey: SigningKey(seed: material.seed).publicKey), .untrusted)
        try storage.replaceOperation(expected: op, next: nil)
    }
}
