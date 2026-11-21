import Foundation
import RAProto

/// Pure signing builders. Callers must serialize authority operations, persist
/// the exact result before publishing, and enforce the active/unlocked gate.
public struct Authority: Sendable {
    typealias S = DeviceSecurity
    private let ephemeral: @Sendable () -> Data
    private let nonce: @Sendable (Int) -> Data

    public init() {
        self.init(ephemeral: { randomBytes(keySize) }, nonce: { randomBytes($0) })
    }

    init(
        ephemeral: @escaping @Sendable () -> Data,
        nonce: @escaping @Sendable (Int) -> Data
    ) {
        self.ephemeral = ephemeral
        self.nonce = nonce
    }

    public static func newSecret() -> Data { randomBytes(keySize) }

    public static func newRecovery() throws -> (seed: Data, publicKey: Data) {
        let seed = randomBytes(keySize)
        return (seed, try SigningKey(seed: seed).publicKey)
    }

    public static func newMember(signer: SigningKey, xPrivate: Data, kind: DeviceSecurity.Kind) throws
        -> DeviceSecurity.Member
    {
        try memberWithX(edPublic: signer.publicKey, xPrivate: xPrivate, kind: kind)
    }

    public static func memberWithX(edPublic: Data, xPrivate: Data, kind: DeviceSecurity.Kind) throws
        -> DeviceSecurity.Member
    {
        try S.check(xPrivate.count == keySize)
        let xPublic = try SecCall.x25519PublicKey(xPrivate)
        return try S.member(ed: edPublic, x: xPublic, kind: kind)
    }

    public static func needsRenewal(_ lease: DeviceSecurity.HeadLease?, now: Int64) -> Bool {
        guard let lease else { return true }
        let at = UInt64(max(now, 1))
        guard at >= lease.issuedAt else { return false }
        let remaining = at >= lease.notAfter ? 0 : lease.notAfter - at
        return remaining < 24 * 60 * 60 * 1000 && at - lease.issuedAt >= 12 * 60 * 60 * 1000
    }

    private func signBuiltState(_ call: PBSecBuildStateCall, signer: SigningKey) throws -> S.State {
        let (unsigned, signBytes) = try SecCall.buildState(call)
        let signature = signer.sign(signBytes)
        return try SecCall.marshalState(
            unsignedCore: unsigned.unsignedCore,
            grants: unsigned.grants.map(SecCall.grantFromProto),
            prevSecret: unsigned.prevSecret,
            signature: signature
        ).1
    }

    public func genesis(
        account: String, domain: Data, member: DeviceSecurity.Member,
        recoveryEd: Data, secret: Data, signer: SigningKey, now: Int64
    ) throws -> DeviceSecurity.State {
        try S.check(
            member.kind == S.Kind.client.rawValue && member.ed == signer.publicKey
                && domain.count == keySize && secret.count == keySize && recoveryEd.count == keySize)
        let kems = try SecCall.encapsulateMembers([member], ephemeral: ephemeral)
        let state = try signBuiltState(.with {
            $0.kind = 1
            $0.account = account
            $0.domain = domain
            $0.member = SecCall.toProto(member)
            $0.recoveryEd = recoveryEd
            $0.secret = secret
            $0.nowMs = UInt64(max(now, 1))
            $0.kems = kems
        }, signer: signer)
        try S.verifyGenesis(S.pin(for: state), state)
        return state
    }

    public func membership(
        parent: DeviceSecurity.State, members: [DeviceSecurity.Member], removed: [String],
        oldSecret: Data, newSecret: Data, signer: SigningKey, now: Int64
    ) throws -> DeviceSecurity.State {
        try S.check(oldSecret.count == keySize && newSecret.count == keySize)
        let sortedMembers = members.sorted { $0.id < $1.id }
        let kems = try SecCall.encapsulateMembers(sortedMembers, ephemeral: ephemeral)
        let parentBytes = try S.stateBytes(parent)
        let prevNonce = nonce(12)
        let next = try signBuiltState(.with {
            $0.kind = 2
            $0.parentState = parentBytes
            $0.members = sortedMembers.map(SecCall.toProto)
            $0.removed = removed.sorted()
            $0.oldSecret = oldSecret
            $0.secret = newSecret
            $0.prevSecretNonce = prevNonce
            $0.nowMs = UInt64(max(now, 1))
            $0.kems = kems
        }, signer: signer)
        try S.verifyTransition(parent: parent, next: next)
        return next
    }

    public func pair(
        parent: DeviceSecurity.State, candidate: DeviceSecurity.Member, oldSecret: Data,
        newSecret: Data, signer: SigningKey, now: Int64
    ) throws -> DeviceSecurity.State {
        try S.check(
            parent.core.member(candidate.id) == nil && !parent.core.removed.contains(candidate.id))
        return try membership(
            parent: parent, members: parent.core.members + [candidate], removed: parent.core.removed,
            oldSecret: oldSecret, newSecret: newSecret, signer: signer, now: now)
    }

    public func remove(
        parent: DeviceSecurity.State, targets: [String], oldSecret: Data,
        newSecret: Data, signer: SigningKey, now: Int64
    ) throws -> DeviceSecurity.State {
        let unique = Array(Set(targets))
        try S.check(
            !unique.isEmpty && !unique.contains(parent.core.authorityID)
                && unique.allSatisfy { parent.core.member($0) != nil })
        return try membership(
            parent: parent, members: parent.core.members.filter { !unique.contains($0.id) },
            removed: Array(Set(parent.core.removed + unique)), oldSecret: oldSecret,
            newSecret: newSecret, signer: signer, now: now)
    }

    public func rotateX(
        parent: DeviceSecurity.State, target: String, newXPublic: Data, oldSecret: Data,
        newSecret: Data, signer: SigningKey, now: Int64
    ) throws -> DeviceSecurity.State {
        guard let old = parent.core.member(target) else { throw S.Failure.invalid }
        try S.check(
            newXPublic.count == keySize && newXPublic != old.x && !parent.core.removed.contains(target))
        let members = parent.core.members.map { m in
            var m = m
            if m.id == target { m.x = newXPublic }
            return m
        }
        return try membership(
            parent: parent, members: members, removed: parent.core.removed,
            oldSecret: oldSecret, newSecret: newSecret, signer: signer, now: now)
    }

    /// Rotating the recovery credential vetoes pending recovery without a content epoch.
    public func veto(
        parent: DeviceSecurity.State, newRecoveryEd: Data, secret: Data,
        signer: SigningKey, now: Int64
    ) throws -> DeviceSecurity.State {
        try S.check(newRecoveryEd.count == keySize && secret.count == keySize)
        let kems = try SecCall.encapsulateMembers(parent.core.members, ephemeral: ephemeral)
        let parentBytes = try S.stateBytes(parent)
        let next = try signBuiltState(.with {
            $0.kind = 6
            $0.parentState = parentBytes
            $0.recoveryEd = newRecoveryEd
            $0.secret = secret
            $0.nowMs = UInt64(max(now, 1))
            $0.kems = kems
        }, signer: signer)
        try S.verifyTransition(parent: parent, next: next)
        return next
    }

    public func handoff(
        parent: DeviceSecurity.State, incoming: String, recoveryEd: Data,
        oldSecret: Data, newSecret: Data, signer: SigningKey, now: Int64
    ) throws -> DeviceSecurity.State {
        try S.check(
            incoming != parent.core.authorityID
                && parent.core.member(incoming)?.kind == S.Kind.client.rawValue
                && recoveryEd.count == keySize
                && recoveryEd != parent.core.recoveryEd
                && oldSecret.count == keySize && newSecret.count == keySize)
        let remaining = parent.core.members.filter { $0.id != parent.core.authorityID }
        let kems = try SecCall.encapsulateMembers(remaining, ephemeral: ephemeral)
        let parentBytes = try S.stateBytes(parent)
        let prevNonce = nonce(12)
        let next = try signBuiltState(.with {
            $0.kind = 4
            $0.parentState = parentBytes
            $0.incomingID = incoming
            $0.recoveryEd = recoveryEd
            $0.oldSecret = oldSecret
            $0.secret = newSecret
            $0.prevSecretNonce = prevNonce
            $0.nowMs = UInt64(max(now, 1))
            $0.kems = kems
        }, signer: signer)
        try S.verifyTransition(parent: parent, next: next)
        return next
    }

    public func recovery(
        parent: DeviceSecurity.State, newMember: DeviceSecurity.Member,
        members: [DeviceSecurity.Member],
        secret: Data, newSigner: SigningKey, oldRecoverySeed: Data, newRecoveryEd: Data,
        now: Int64
    ) throws -> (state: DeviceSecurity.State, statement: DeviceSecurity.RecoveryStatement) {
        try S.check(
            newMember.kind == S.Kind.client.rawValue && newMember.ed == newSigner.publicKey
                && newRecoveryEd.count == keySize && secret.count == keySize
                && !members.contains { $0.id == parent.core.authorityID })
        let sortedMembers = members.sorted { $0.id < $1.id }
        var seen: Set<String> = []
        let distinct = (sortedMembers + [newMember]).filter { seen.insert($0.id).inserted }
        let kems = try SecCall.encapsulateMembers(distinct, ephemeral: ephemeral)
        let parentBytes = try S.stateBytes(parent)
        let state = try signBuiltState(.with {
            $0.kind = 5
            $0.parentState = parentBytes
            $0.member = SecCall.toProto(newMember)
            $0.members = sortedMembers.map(SecCall.toProto)
            $0.recoveryEd = newRecoveryEd
            $0.secret = secret
            $0.nowMs = UInt64(max(now, 1))
            $0.kems = kems
        }, signer: newSigner)
        let nextBytes = try S.stateBytes(state)
        var (unsigned, signBytes) = try SecCall.newRecoveryStatementUnsigned(
            parentBytes: parentBytes,
            nextBytes: nextBytes,
            nonce: nonce(32)
        )
        unsigned.signature = try SigningKey(seed: oldRecoverySeed).sign(signBytes)
        let (_, statement) = try SecCall.marshalRecoveryStatement(unsigned)
        try SecCall.verifyRecoveryStatement(
            statementBytes: try statement.bytes(),
            parentBytes: parentBytes,
            nextBytes: nextBytes
        )
        try S.verifyTransition(parent: parent, next: state, recovery: statement)
        return (state, statement)
    }
}
