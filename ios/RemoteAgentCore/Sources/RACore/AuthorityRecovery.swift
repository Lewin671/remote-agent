import Foundation
import RAProto

/// Display-only material. No recovery seed or text is Codable or persisted.
public struct PreparedRecovery: Sendable, CustomStringConvertible, CustomDebugStringConvertible, CustomReflectable {
    public let code: String
    public let publicKey: Data
    public let parent: Data
    public var description: String { "PreparedRecovery" }
    public var debugDescription: String { description }
    public var customMirror: Mirror { Mirror(self, children: [:]) }
}

extension AuthorityCoordinator {
    public func prepareRecovery() async throws -> PreparedRecovery {
        let t = try begin(); defer { busy = false }
        try await resume(t)
        _ = try await rpc(t) { [security] in try await security.sync() }
        let r = try authority(t)
        return try gate.withPermit(t) {
            let recovery = try Authority.newRecovery()
            return try PreparedRecovery(code: RecoveryCode.encode(seed: recovery.seed, genesis: r.pin.genesis),
                publicKey: recovery.publicKey, parent: DeviceSecurity.stateHash(r.state))
        }
    }
    /// Also the explicit veto path. Confirmation precedes any signed successor.
    public func publishRecovery(_ prepared: PreparedRecovery, codeSaved: Bool) async throws {
        try DeviceSecurity.check(codeSaved, .untrusted)
        let t = try begin(); defer { busy = false }
        try await resume(t)
        _ = try await rpc(t) { [security] in try await security.sync() }
        let r = try authority(t, head: prepared.parent)
        try prepareState(t, r: r) { r in
            (try Authority().veto(parent: r.state, newRecoveryEd: prepared.publicKey, secret: r.secret!,
                signer: SigningKey(seed: account.deviceSeed), now: Int64(clamping: now(r))), Data(), nil)
        }
        try await resume(t)
    }
    public func prepareHandoffCode() throws -> PreparedRecovery {
        let t = try gate.ticket()
        let r = try storage.readSecurity()
        try DeviceSecurity.check(r.member && r.memberID == account.deviceId && r.state.core.member(account.deviceId)?.kind == 2, .untrusted)
        _ = try storage.publicProfile(pin: r.pin).unwrap()
        return try gate.withPermit(t) {
            let recovery = try Authority.newRecovery()
            return try PreparedRecovery(code: RecoveryCode.encode(seed: recovery.seed, genesis: r.pin.genesis),
                publicKey: recovery.publicKey, parent: DeviceSecurity.stateHash(r.state))
        }
    }
    /// Publishes only a public recovery key in this member's self-signed metadata.
    /// The receipt survives a restart without falsely claiming the code can be shown again.
    public func offerHandoff(_ prepared: PreparedRecovery, codeSaved: Bool) async throws {
        try DeviceSecurity.check(codeSaved, .untrusted)
        let t = try begin(); defer { busy = false }
        if let pending = try storage.readOperation() {
            try DeviceSecurity.check(pending.kind == .handoffOffer, .conflict)
        } else {
            let r = try storage.readSecurity()
            try DeviceSecurity.check(r.member && r.memberID == account.deviceId && prepared.parent == DeviceSecurity.stateHash(r.state), .stale)
            _ = try storage.publicProfile(pin: r.pin).unwrap()
            _ = try storage.prepareOperation(expectedHead: prepared.parent) { committed in
                try gate.withPermit(t) { SecurityOperation(kind: .handoffOffer, parent: prepared.parent, signed: prepared.publicKey) }
            }
        }
        try await resumeHandoffOffer(t)
    }
    public func resumeHandoffOffer() async throws {
        let t = try begin(); defer { busy = false }; try await resumeHandoffOffer(t)
    }
    func resumeHandoffOffer(_ t: UInt64) async throws {
        guard var op = try storage.readOperation(), op.kind == .handoffOffer else { throw DeviceSecurity.Failure.stale }
        try DeviceSecurity.check(op.codeSaved && op.signed.count == 32, .untrusted)
        for _ in 0..<5 {
            let r = try await rpc(t) { [security] in try await security.sync() }
            try DeviceSecurity.check(r.member && r.memberID == account.deviceId && DeviceSecurity.stateHash(r.state) == op.parent, .stale)
            let headers = Api.headers(token: try await rpc(t, token))
            let rows = try await rpc(t) { [api] in try await api.client.listDevices(request: .init(), headers: headers).value() }
            guard let own = rows.devices.first(where: { $0.id == account.deviceId }) else { throw DeviceSecurity.Failure.untrusted }
            var meta = PBDeviceMeta()
            if own.hasMeta {
                let (h, old) = try await rpc(t) { [security] in try await security.openMessage(own.meta, as: PBDeviceMeta.self, history: true) }
                try DeviceSecurity.check(h.sender == account.deviceId && h.request == account.deviceId && h.seq == own.metaRevision, .untrusted)
                meta = old
            }
            if meta.handoffRecoveryEd == op.signed {
                op = try advance(op, to: .delivered); try storage.replaceOperation(expected: op, next: nil); return
            }
            meta.handoffRecoveryEd = op.signed
            let request: PBPutDeviceMetaRequest
            if op.result.isEmpty {
                let bytes = try meta.serializedData()
                let env = try await rpc(t) { [security, gate, account] in
                    try await security.seal(purpose: .deviceMeta, request: account.deviceId, seq: own.metaRevision + 1,
                        plaintext: bytes, authorityGate: gate, authorityTicket: t)
                }
                try DeviceSecurity.check(DeviceSecurity.stateHash(storage.readSecurity().state) == op.parent, .stale)
                var ask = PBPutDeviceMetaRequest(); ask.deviceID = account.deviceId; ask.expectedRevision = own.metaRevision; ask.meta = env
                var next = op; next.result = try ask.serializedData()
                try storage.replaceOperation(expected: op, next: next); op = next
                request = ask
            } else { request = try PBPutDeviceMetaRequest(serializedBytes: op.result) }
            try DeviceSecurity.check(DeviceSecurity.stateHash(storage.readSecurity().state) == op.parent, .stale)
            let result = try await rpc(t) { [api] in try await api.client.putDeviceMeta(request: request, headers: headers).value() }
            if result.applied {
                op = try advance(op, to: .delivered); try storage.replaceOperation(expected: op, next: nil); return
            }
            // An unrelated metadata CAS won. Keep the exact failed request; the
            // owner can explicitly discard this unconsumed offer and try again.
            throw DeviceSecurity.Failure.conflict
        }
        throw DeviceSecurity.Failure.conflict
    }
    /// Metadata must have been opened from self-signed envelopes by Remote.
    public func handoffCandidates(_ devices: [DeviceView]) throws -> [DeviceView] {
        let r = try storage.readSecurity()
        return devices.filter { $0.id != account.deviceId && r.state.core.member($0.id)?.kind == 2
            && $0.meta?.handoffRecoveryEd.count == 32 && $0.meta?.handoffRecoveryEd != r.state.core.recoveryEd }
    }
    func handoffAuthority(incoming: String, offer: Data, verifiedHead: Data) async throws {
        let t = try begin(); defer { busy = false }
        try await resume(t)
        _ = try await rpc(t) { [security] in try await security.sync() }
        let r = try authority(t, head: verifiedHead)
        try DeviceSecurity.check(offer.count == 32, .untrusted)
        try prepareState(t, r: r) { r in
            (try Authority().handoff(parent: r.state, incoming: incoming, recoveryEd: offer, oldSecret: r.secret!,
                newSecret: Authority.newSecret(), signer: SigningKey(seed: account.deviceSeed), now: Int64(clamping: now(r))), Data(), nil)
        }
        try await resume(t)
    }
    /// A definitively rejected/obsolete offer can be explicitly abandoned. State
    /// publications and uncertain recovery/creation can never use this shortcut.
    public func discardHandoffOffer() throws {
        if let op = try storage.readOperation() {
            try DeviceSecurity.check(op.kind == .handoffOffer, .untrusted)
            try storage.replaceOperation(expected: op, next: nil)
        }
    }
}
extension Optional {
    func unwrap() throws -> Wrapped {
        guard let self else { throw DeviceSecurity.Failure.unavailable }; return self
    }
}
