import Corebind
import Foundation
import RAProto
import SwiftProtobuf

/// Stateless bridge from Swift security/authority helpers to Go `clientcore` `SecCall`.
enum SecCall {
    static func failure(from code: String) -> DeviceSecurity.Failure {
        switch code {
        case "invalid": .invalid
        case "untrusted": .untrusted
        case "stale": .stale
        case "conflict": .conflict
        case "pending": .pending
        case "capacity": .capacity
        case "storage": .storage
        case "unknown": .unknown
        case "unavailable": .unavailable
        case "lease": .lease
        case "clock": .clock
        default: .invalid
        }
    }

    static func hkdfSha256(secret: Data, salt: Data, info: Data, size: Int) throws -> Data {
        var err: NSError?
        let out = CorebindHkdfSha256(secret, salt, info, Int32(size), &err)
        if let err { throw err }
        return out ?? Data()
    }

    static func ed25519PublicKey(_ seed: Data) throws -> Data {
        var err: NSError?
        let out = CorebindEd25519PublicKey(seed, &err)
        if let err { throw err }
        guard let out else { throw CoreClientError.unavailable }
        return out
    }

    static func ed25519Sign(_ seed: Data, _ message: Data) throws -> Data {
        var err: NSError?
        let out = CorebindEd25519Sign(seed, message, &err)
        if let err { throw err }
        guard let out else { throw CoreClientError.unavailable }
        return out
    }

    static func ed25519Verify(_ publicKey: Data, _ message: Data, _ signature: Data) -> Bool {
        CorebindEd25519Verify(publicKey, message, signature)
    }

    static func x25519PublicKey(_ privateKey: Data) throws -> Data {
        var err: NSError?
        let out = CorebindX25519PublicKey(privateKey, &err)
        if let err { throw err }
        guard let out else { throw CoreClientError.unavailable }
        return out
    }

    static func x25519Shared(_ privateKey: Data, _ peerPublic: Data) throws -> Data {
        var err: NSError?
        let out = CorebindX25519Shared(privateKey, peerPublic, &err)
        if let err { throw err }
        guard let out else { throw CoreClientError.unavailable }
        return out
    }

    private static func call(_ req: PBSecCallRequest) throws -> PBSecCallResponse {
        var err: NSError?
        let raw = CorebindSecCall(try req.serializedData(), &err)
        if let err { throw err }
        let resp = try PBSecCallResponse(serializedBytes: raw ?? Data())
        if !resp.error.isEmpty {
            throw failure(from: resp.errorCode.isEmpty ? "invalid" : resp.errorCode)
        }
        return resp
    }

    static func toProto(_ p: DeviceSecurity.Pin) -> PBSecPin {
        PBSecPin.with {
            $0.account = p.account
            $0.domain = p.domain
            $0.genesis = p.genesis
        }
    }

    static func pinFromProto(_ p: PBSecPin) -> DeviceSecurity.Pin {
        DeviceSecurity.Pin(account: p.account, domain: p.domain, genesis: p.genesis)
    }

    static func toProto(_ m: DeviceSecurity.Member) -> PBSecMember {
        PBSecMember.with {
            $0.id = m.id
            $0.edPublic = m.ed
            $0.xPublic = m.x
            $0.kind = m.kind
        }
    }

    static func memberFromProto(_ m: PBSecMember) -> DeviceSecurity.Member {
        DeviceSecurity.Member(id: m.id, ed: m.edPublic, x: m.xPublic, kind: m.kind)
    }

    static func toProto(_ g: DeviceSecurity.Grant) -> PBSecGrant {
        PBSecGrant.with {
            $0.recipient = g.recipient
            $0.enc = g.enc
            $0.cipher = g.cipher
        }
    }

    static func grantFromProto(_ g: PBSecGrant) -> DeviceSecurity.Grant {
        DeviceSecurity.Grant(recipient: g.recipient, enc: g.enc, cipher: g.cipher)
    }

    static func stateFromProto(_ s: PBSecParsedState) -> DeviceSecurity.State {
        DeviceSecurity.State(
            core: DeviceSecurity.Core(
                account: s.account,
                domain: s.domain,
                authorityID: s.authorityID,
                recoveryEd: s.recoveryEd,
                parent: s.parent,
                epoch: s.epoch,
                contentEpoch: s.contentEpoch,
                issuedAt: s.issuedAt,
                members: s.members.map(memberFromProto),
                removed: s.removed
            ),
            grants: s.grants.map(grantFromProto),
            prevSecret: s.prevSecret,
            signature: s.signature
        )
    }

    static func leaseFromProto(_ l: PBSecParsedLease) -> DeviceSecurity.HeadLease {
        DeviceSecurity.HeadLease(
            domain: l.domain,
            head: l.head,
            issuedAt: l.issuedAt,
            notAfter: l.notAfter,
            signature: l.signature
        )
    }

    static func recoveryFromProto(_ r: PBSecParsedRecoveryStatement) -> DeviceSecurity.RecoveryStatement {
        DeviceSecurity.RecoveryStatement(
            account: r.account,
            domain: r.domain,
            parent: r.parent,
            newAuthorityID: r.newAuthorityID,
            newRecoveryEd: r.newRecoveryEd,
            newMember: memberFromProto(r.newMember),
            issuedAt: r.issuedAt,
            stateHash: r.stateHash,
            nonce: r.nonce,
            signature: r.signature
        )
    }

    static func parseState(_ raw: Data) throws -> (DeviceSecurity.State, Data) {
        let resp = try call(.with { $0.parseState = raw })
        let state = stateFromProto(resp.parsedState)
        let hash = resp.parsedState.stateHash
        DeviceSecurity.cacheState(state, raw: raw, hash: hash)
        return (state, hash)
    }

    static func verifyChain(
        pin: DeviceSecurity.Pin,
        states: [Data],
        recoveryProofs: [Data?] = [],
        lease: Data? = nil,
        nowMs: Int64 = 0,
        memberId: String = "",
        checkLeaseWindow: Bool = false
    ) throws -> DeviceSecurity.State {
        let resp = try call(.with { req in
            req.verifyChain = .with {
                $0.pin = toProto(pin)
                $0.states = states
                $0.recoveryProofs = recoveryProofs.map { $0 ?? Data() }
                if let lease, !lease.isEmpty { $0.lease = lease }
                $0.nowMs = nowMs
                $0.memberID = memberId
                $0.checkLeaseWindow = checkLeaseWindow
            }
        })
        let head = stateFromProto(resp.parsedState)
        if let headRaw = states.last {
            DeviceSecurity.cacheState(head, raw: headRaw, hash: resp.parsedState.stateHash)
        }
        return head
    }

    static func verifyTransition(parentBytes: Data, nextBytes: Data, recoveryBytes: Data?) throws {
        _ = try call(.with { req in
            req.verifyTransition = .with {
                $0.parentState = parentBytes
                $0.nextState = nextBytes
                if let recoveryBytes, !recoveryBytes.isEmpty {
                    $0.recoveryProof = recoveryBytes
                }
            }
        })
    }

    static func grantChallenge(pin: DeviceSecurity.Pin, stateBytes: Data, memberId: String) throws -> Data {
        try call(.with {
            $0.grantChallenge = .with {
                $0.pin = toProto(pin)
                $0.state = stateBytes
                $0.memberID = memberId
            }
        }).data
    }

    static func openGrantWithDH(pin: DeviceSecurity.Pin, stateBytes: Data, memberId: String, dh: Data) throws -> Data {
        try call(.with {
            $0.openGrantWithDh = .with {
                $0.pin = toProto(pin)
                $0.state = stateBytes
                $0.memberID = memberId
                $0.dh = dh
            }
        }).data
    }

    static func openPrevSecret(newSecret: Data, blob: Data) throws -> Data {
        try call(.with {
            $0.openPrevSecret = .with {
                $0.newSecret = newSecret
                $0.blob = blob
            }
        }).data
    }

    static func parseLease(_ raw: Data) throws -> DeviceSecurity.HeadLease {
        let resp = try call(.with { $0.parseLease = raw })
        return leaseFromProto(resp.parsedLease)
    }

    static func verifyLease(leaseBytes: Data, headStateBytes: Data, nowMs: Int64, checkWindow: Bool) throws {
        _ = try call(.with {
            $0.verifyLease = .with {
                $0.lease = leaseBytes
                $0.headState = headStateBytes
                $0.nowMs = nowMs
                $0.checkWindow = checkWindow
            }
        })
    }

    static func newLeaseUnsigned(headStateBytes: Data, nowMs: UInt64) throws -> (PBSecParsedLease, Data) {
        let resp = try call(.with {
            $0.newLeaseUnsigned = .with {
                $0.headState = headStateBytes
                $0.nowMs = nowMs
            }
        })
        return (resp.parsedLease, resp.data)
    }

    static func marshalLease(
        domain: Data,
        headHash: Data,
        issuedAt: UInt64,
        notAfter: UInt64,
        signature: Data
    ) throws -> (Data, DeviceSecurity.HeadLease) {
        let resp = try call(.with {
            $0.marshalLease = .with {
                $0.domain = domain
                $0.head = headHash
                $0.issuedAt = issuedAt
                $0.notAfter = notAfter
                $0.signature = signature
            }
        })
        return (resp.data, leaseFromProto(resp.parsedLease))
    }

    static func parseRecoveryStatement(_ raw: Data) throws -> DeviceSecurity.RecoveryStatement {
        let resp = try call(.with { $0.parseRecoveryStatement = raw })
        return recoveryFromProto(resp.parsedRecovery)
    }

    static func verifyRecoveryStatement(statementBytes: Data, parentBytes: Data, nextBytes: Data) throws {
        _ = try call(.with {
            $0.verifyRecoveryStatement = .with {
                $0.statement = statementBytes
                $0.parentState = parentBytes
                $0.nextState = nextBytes
            }
        })
    }

    static func newRecoveryStatementUnsigned(
        parentBytes: Data,
        nextBytes: Data,
        nonce: Data
    ) throws -> (DeviceSecurity.RecoveryStatement, Data) {
        let resp = try call(.with {
            $0.newRecoveryStatementUnsigned = .with {
                $0.parentState = parentBytes
                $0.nextState = nextBytes
                $0.nonce = nonce
            }
        })
        return (recoveryFromProto(resp.parsedRecovery), resp.data)
    }

    static func marshalRecoveryStatement(
        _ stmt: DeviceSecurity.RecoveryStatement
    ) throws -> (Data, DeviceSecurity.RecoveryStatement) {
        let resp = try call(.with {
            $0.marshalRecoveryStatement = .with {
                $0.account = stmt.account
                $0.domain = stmt.domain
                $0.parent = stmt.parent
                $0.newAuthorityID = stmt.newAuthorityID
                $0.newRecoveryEd = stmt.newRecoveryEd
                $0.newMember = toProto(stmt.newMember)
                $0.issuedAt = stmt.issuedAt
                $0.stateHash = stmt.stateHash
                $0.nonce = stmt.nonce
                $0.signature = stmt.signature
            }
        })
        return (resp.data, recoveryFromProto(resp.parsedRecovery))
    }

    static func encapsulateMembers(
        _ members: [DeviceSecurity.Member],
        ephemeral: @Sendable () -> Data = { randomBytes(keySize) }
    ) throws -> [PBSecKemPair] {
        try members.map { member in
            let eph = ephemeral()
            let enc = try x25519PublicKey(eph)
            let dh = try x25519Shared(eph, member.x)
            return PBSecKemPair.with {
                $0.memberID = member.id
                $0.enc = enc
                $0.dh = dh
            }
        }
    }

    static func buildState(_ callReq: PBSecBuildStateCall) throws -> (PBSecUnsignedState, Data) {
        let resp = try call(.with { $0.buildState = callReq })
        return (resp.unsignedState, resp.data)
    }

    static func marshalState(
        unsignedCore: Data,
        grants: [DeviceSecurity.Grant],
        prevSecret: Data,
        signature: Data
    ) throws -> (Data, DeviceSecurity.State) {
        let resp = try call(.with {
            $0.marshalState = .with {
                $0.unsignedCore = unsignedCore
                $0.grants = grants.map(toProto)
                $0.prevSecret = prevSecret
                $0.signature = signature
            }
        })
        let parsed = stateFromProto(resp.parsedState)
        DeviceSecurity.cacheState(parsed, raw: resp.data, hash: resp.parsedState.stateHash)
        return (resp.data, parsed)
    }

    static func verifyPairApproval(
        approval: Data,
        signature: Data,
        expectedAccount: String = "",
        expectedCandidateId: String = ""
    ) throws -> PBSecParsedApproval {
        try call(.with {
            $0.verifyPairApproval = .with {
                $0.approval = approval
                $0.signature = signature
                $0.expectedAccount = expectedAccount
                $0.expectedCandidateID = expectedCandidateId
            }
        }).parsedApproval
    }

    static func machineReplacementOrigins(
        account: String,
        targetMachineId: String,
        states: [Data],
        proofs: [PBMachineReplacementProof]
    ) throws -> Set<String> {
        let resp = try call(.with {
            $0.machineReplacementOrigins = .with {
                $0.account = account
                $0.targetMachineID = targetMachineId
                $0.states = states
                $0.proofs = proofs
            }
        })
        return Set(resp.strings)
    }

    static func pairTrustDescriptor(pin: DeviceSecurity.Pin, approver: String) throws -> Data {
        try call(.with {
            $0.pairTrustDescriptor = .with {
                $0.pin = toProto(pin)
                $0.approver = approver
            }
        }).data
    }

    static func parsePairTrustDescriptor(_ raw: Data) throws -> (DeviceSecurity.Pin, String) {
        let resp = try call(.with { $0.parsePairTrustDescriptor = raw })
        return (pinFromProto(resp.pin), resp.text)
    }

    static func pairDomainCommit(initiatorPublic: Data, trustDescriptor: Data) throws -> Data {
        try call(.with {
            $0.pairDomainCommit = .with {
                $0.initiator = initiatorPublic
                $0.trustDescriptor = trustDescriptor
            }
        }).data
    }

    static func pairTranscript(
        pairingId: String,
        initiatorPublic: Data,
        joinerPublic: Data,
        joinerKind: Int
    ) throws -> Data {
        try call(.with {
            $0.pairTranscript = .with {
                $0.pairingID = pairingId
                $0.initiatorPublic = initiatorPublic
                $0.joinerPublic = joinerPublic
                $0.joinerKind = Int32(truncatingIfNeeded: joinerKind)
            }
        }).data
    }

    static func pairIdentityTranscript(
        pairingId: String,
        initiatorPublic: Data,
        joinerPublic: Data,
        deviceEdPublic: Data,
        joinerKind: Int
    ) throws -> Data {
        try call(.with {
            $0.pairIdentityTranscript = .with {
                $0.pairingID = pairingId
                $0.initiatorPublic = initiatorPublic
                $0.joinerPublic = joinerPublic
                $0.deviceEdPublic = deviceEdPublic
                $0.joinerKind = Int32(truncatingIfNeeded: joinerKind)
            }
        }).data
    }

    static func pairDomainTranscript(
        pairingId: String,
        initiatorPublic: Data,
        joinerPublic: Data,
        deviceEdPublic: Data,
        deviceXPublic: Data,
        joinerKind: Int,
        trustDescriptor: Data
    ) throws -> Data {
        try call(.with {
            $0.pairDomainTranscript = .with {
                $0.pairingID = pairingId
                $0.initiatorPublic = initiatorPublic
                $0.joinerPublic = joinerPublic
                $0.deviceEdPublic = deviceEdPublic
                $0.deviceXPublic = deviceXPublic
                $0.joinerKind = Int32(truncatingIfNeeded: joinerKind)
                $0.trustDescriptor = trustDescriptor
            }
        }).data
    }

    static func derivePairSecrets(shared: Data, transcript: Data) throws -> (key: Data, sas: Data, emoji: [String]) {
        let resp = try call(.with {
            $0.derivePairSecrets = .with {
                $0.shared = shared
                $0.transcript = transcript
            }
        })
        return (resp.data, resp.secondaryData, resp.strings)
    }

    static func inflateBounded(compressed: Data, limit: Int) throws -> Data {
        try call(.with {
            $0.inflateBounded = .with {
                $0.compressed = compressed
                $0.limit = UInt32(clamping: max(limit, 0))
            }
        }).data
    }

    static func derivePurposeKey(
        secret: Data,
        accountId: String,
        domain: Data,
        contentEpoch: UInt64,
        purpose: UInt32
    ) throws -> Data {
        try call(.with {
            $0.derivePurposeKey = .with {
                $0.secret = secret
                $0.accountID = accountId
                $0.domain = domain
                $0.contentEpoch = contentEpoch
                $0.purpose = purpose
            }
        }).data
    }

    static func openNotifyEnvelope(
        pin: DeviceSecurity.Pin,
        states: [Data],
        envelope: PBEnvelope,
        purposeKey: Data? = nil,
        memberId: String = "",
        dh: Data? = nil,
        contentSecret: Data? = nil
    ) throws -> PBNotifyContent {
        let resp = try call(.with {
            $0.openNotifyEnvelope = .with {
                $0.pin = toProto(pin)
                $0.states = states
                $0.envelope = envelope
                if let purposeKey { $0.purposeKey = purposeKey }
                if !memberId.isEmpty { $0.memberID = memberId }
                if let dh { $0.dh = dh }
                if let contentSecret { $0.contentSecret = contentSecret }
            }
        })
        return try PBNotifyContent(serializedBytes: resp.data)
    }
}
