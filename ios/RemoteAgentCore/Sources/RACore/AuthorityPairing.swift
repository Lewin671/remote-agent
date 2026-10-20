import Connect
import Foundation
import RAProto

/// Five-minute SAS context only; after approval the durable operation owns delivery.
public struct PairInitiation: Sendable, CustomStringConvertible, CustomDebugStringConvertible, CustomReflectable {
    public let link: PairLink
    public let pairingID: String
    public let expiresMs: Int64
    let key: PairKey
    let trust: Data
    let parent: Data
    let replacesDeviceID: String
    public var description: String { "PairInitiation" }
    public var debugDescription: String { description }
    public var customMirror: Mirror { Mirror(self, children: [:]) }
}
public struct PairCandidate: Sendable {
    public let emoji: [String]
    public let kind: PBDeviceKind
    public let member: DeviceSecurity.Member
    let initiation: PairInitiation
    let secrets: PairSecrets
}

extension AuthorityCoordinator {
    public func startPairing(replacesDeviceID: String = "") async throws -> PairInitiation {
        let t = try begin(); defer { busy = false }
        try await resume(t)
        try DeviceSecurity.check(storage.readOperation() == nil, .conflict)
        _ = try await rpc(t) { [security] in try await security.sync() }
        let r = try authority(t)
        _ = try storage.publicProfile(pin: r.pin).unwrap()
        try DeviceSecurity.check(r.states.count < DeviceSecurity.maxPairStates, .capacity)
        if !replacesDeviceID.isEmpty {
            try DeviceSecurity.check(r.state.core.member(replacesDeviceID) == nil && r.state.core.removed.contains(replacesDeviceID)
                && r.states.values.contains { $0.core.member(replacesDeviceID)?.kind == 1 }, .untrusted)
            let headers = Api.headers(token: try await rpc(t, token))
            let rows = try await rpc(t) { [api] in try await api.client.listMachines(request: .init(), headers: headers).value() }
            try DeviceSecurity.check(rows.machines.contains { $0.id == replacesDeviceID }, .untrusted)
        }
        let key = try PairKey()
        let descriptor = try DeviceSecurity.pairTrustDescriptor(r.pin, approver: account.deviceId)
        let commit = try DeviceSecurity.pairDomainCommit(ephemeral: key.publicKey, trust: descriptor)
        var req = PBCreatePairingRequest(); req.commit = commit
        let request = req
        let headers = Api.headers(token: try await rpc(t, token))
        _ = try authority(t, head: DeviceSecurity.stateHash(r.state))
        let created = try await rpc(t) { [api] in try await api.pairing.createPairing(request: request, headers: headers).value() }
        _ = try authority(t, head: DeviceSecurity.stateHash(r.state))
        try DeviceSecurity.check(created.code.count == 8 && created.code.utf8.allSatisfy { $0 >= 48 && $0 <= 57 }
            && created.approverPublicKey == SigningKey(seed: account.deviceSeed).publicKey, .untrusted)
        return PairInitiation(link: PairLink(code: created.code, server: account.server, commit: commit),
            pairingID: created.pairingID, expiresMs: created.expiresMs, key: key, trust: descriptor,
            parent: try DeviceSecurity.stateHash(r.state), replacesDeviceID: replacesDeviceID)
    }
    public func pairingCandidate(_ initiation: PairInitiation) async throws -> PairCandidate? {
        let t = try begin(); defer { busy = false }
        _ = try authority(t, head: initiation.parent)
        try DeviceSecurity.check(clock() < initiation.expiresMs, .stale)
        let headers = Api.headers(token: try await rpc(t, token))
        var get = PBGetPairingRequest(); get.pairingID = initiation.pairingID
        let request = get
        let st = try await rpc(t) { [api] in try await api.pairing.getPairing(request: request, headers: headers).value() }
        if st.state == .canceled { throw PairError("配对已取消") }
        guard st.state.rawValue >= PBPairingState.joined.rawValue else { return nil }
        let kind = try joinerKind(st)
        try DeviceSecurity.check(initiation.replacesDeviceID.isEmpty || kind == .machine, .untrusted)
        let member = try DeviceSecurity.member(ed: st.devicePublicKey, x: st.deviceHpkePublicKey,
            kind: kind == .machine ? .machine : .client)
        let transcript = Pairing.domainTranscript(pairingId: initiation.pairingID,
            initiatorPublic: initiation.key.publicKey, joinerPublic: st.joinerPublic,
            devicePublic: st.devicePublicKey, hpkePublic: st.deviceHpkePublicKey,
            joinerKind: kind.rawValue, trust: initiation.trust)
        let secrets = try initiation.key.derive(otherPublic: st.joinerPublic, transcript: transcript)
        _ = try authority(t, head: initiation.parent)
        var reveal = PBRevealPairingRequest(); reveal.pairingID = initiation.pairingID
        reveal.initiatorPublic = initiation.key.publicKey; reveal.trust = initiation.trust
        let ask = reveal
        try await rpc(t) { [api] in _ = try await api.pairing.revealPairing(request: ask, headers: headers).value() }
        _ = try authority(t, head: initiation.parent)
        return PairCandidate(emoji: secrets.emoji, kind: kind, member: member, initiation: initiation, secrets: secrets)
    }
    public func approvePairing(_ candidate: PairCandidate, sasConfirmed: Bool) async throws {
        try DeviceSecurity.check(sasConfirmed, .untrusted)
        let t = try begin(); defer { busy = false }
        if let op = try storage.readOperation() {
            let delivery = try PBApprovePairingRequest(serializedBytes: op.delivery)
            try DeviceSecurity.check(delivery.pairingID == candidate.initiation.pairingID, .conflict)
            try await resume(t); return
        }
        try DeviceSecurity.check(clock() < candidate.initiation.expiresMs, .stale)
        _ = try await rpc(t) { [security] in try await security.sync() }
        let r = try authority(t, head: candidate.initiation.parent)
        let profile = try storage.publicProfile(pin: r.pin).unwrap()
        try prepareState(t, r: r) { r in
            let signer = try SigningKey(seed: account.deviceSeed)
            let next: DeviceSecurity.State
            if let existing = r.state.core.member(candidate.member.id) {
                try DeviceSecurity.check(existing.kind == candidate.member.kind && existing.ed == candidate.member.ed, .untrusted)
                next = try Authority().rotateX(parent: r.state, target: candidate.member.id, newXPublic: candidate.member.x,
                    oldSecret: r.secret!, newSecret: Authority.newSecret(), signer: signer, now: Int64(clamping: now(r)))
            } else {
                next = try Authority().pair(parent: r.state, candidate: candidate.member, oldSecret: r.secret!,
                    newSecret: Authority.newSecret(), signer: signer, now: Int64(clamping: now(r)))
            }
            let certificate = try DeviceSecurity.HeadLease.sign(next, signer: signer, now: Int64(clamping: now(r))).bytes()
            var approval = PBPairApproval()
            approval.accountID = r.pin.account; approval.pairingID = candidate.initiation.pairingID
            approval.approverDeviceID = account.deviceId; approval.approverPublicKey = signer.publicKey
            approval.candidateDevicePublicKey = candidate.member.ed; approval.candidateKind = candidate.kind
            approval.expiresMs = candidate.initiation.expiresMs; approval.nonce = randomBytes(16)
            approval.replacesDeviceID = candidate.initiation.replacesDeviceID
            let bytes = try approval.serializedData()
            let signature = signer.sign(Data("remote-agent/v1/pair-approval\0".utf8) + bytes)
            var payload = PBPairingPayload(); payload.server = account.server
            payload.accountPublicKey = profile.publicKey; payload.approval = bytes; payload.approvalSignature = signature
            payload.lease = certificate
            payload.securityStates = try (r.states.values.sorted { $0.core.epoch < $1.core.epoch } + [next]).map { state in
                var item = PBSecurityState(); item.state = try DeviceSecurity.stateBytes(state)
                item.recovery = try r.recovery[state.core.epoch]?.bytes() ?? Data(); return item
            }
            try DeviceSecurity.check(payload.securityStates.count <= DeviceSecurity.maxPairStates
                && payload.securityStates.reduce(0, { $0 + $1.state.count + $1.recovery.count }) <= DeviceSecurity.maxPairBytes, .capacity)
            var h = PBHeader(); h.kind = .pairing; h.requestID = candidate.initiation.pairingID; h.sender = account.deviceId
            let env = try Envelopes.seal(key: candidate.secrets.key, header: h, message: payload)
            var delivery = PBApprovePairingRequest(); delivery.pairingID = candidate.initiation.pairingID
            delivery.payload = env; delivery.approval = bytes; delivery.approvalSignature = signature
            return (next, try delivery.serializedData(), certificate)
        }
        try await resume(t)
    }
    public func cancelPairing(_ initiation: PairInitiation) async throws {
        let t = try begin(); defer { busy = false }
        try DeviceSecurity.check(storage.readOperation() == nil, .conflict)
        _ = try authority(t)
        let headers = Api.headers(token: try await rpc(t, token))
        var ask = PBCancelPairingRequest(); ask.pairingID = initiation.pairingID
        let request = ask
        try await rpc(t) { [api] in _ = try await api.pairing.cancelPairing(request: request, headers: headers).value() }
    }
    public func finishPairingOperation(pairingID: String) async throws {
        let t = try begin(); defer { busy = false }
        guard var op = try storage.readOperation() else { return }
        try DeviceSecurity.check(op.kind == .state && !op.delivery.isEmpty, .untrusted)
        let delivery = try PBApprovePairingRequest(serializedBytes: op.delivery)
        try DeviceSecurity.check(delivery.pairingID == pairingID, .untrusted)
        let approval = try PBPairApproval(serializedBytes: delivery.approval)
        _ = try await rpc(t) { [security] in try await security.sync() }
        let r = try authority(t)
        let target = try DeviceSecurity.parseState(PBPutSecurityStateRequest(serializedBytes: op.signed).states.last.unwrap().state)
        try DeviceSecurity.check(r.states[target.core.epoch].map { try DeviceSecurity.stateHash($0) } == DeviceSecurity.stateHash(target), .conflict)
        let headers = Api.headers(token: try await rpc(t, token))
        var get = PBGetPairingRequest(); get.pairingID = pairingID
        let request = get
        var closed = false
        do {
            let status = try await rpc(t) { [api] in try await api.pairing.getPairing(request: request, headers: headers).value() }
            if status.state == .delivered { /* positive candidate receipt */ }
            else if status.state == .canceled { closed = true }
            else { return } // A live pairing must wait for DELIVERED.
        } catch let error as ConnectError where error.code == .notFound { closed = true }
        if closed {
            // Android's closed-pairing reconciliation: an expired ephemeral slot
            // is not proof of failed registration. Verify registration AND the
            // exact approved chain before releasing its delivery evidence.
            let rows = try await rpc(t) { [api] in try await api.client.listDevices(request: .init(), headers: headers).value() }
            let candidate = deviceId(publicKey: approval.candidateDevicePublicKey)
            try DeviceSecurity.check(rows.devices.contains { $0.id == candidate }
                && r.state.core.member(candidate)?.ed == approval.candidateDevicePublicKey, .untrusted)
        }
        op = try advance(op, to: .delivered)
        try storage.replaceOperation(expected: op, next: nil)
    }
}
