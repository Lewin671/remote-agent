import Foundation
import RAProto

/// Foreground management workflows, separate from the screens and connection actor.
/// One instance may be re-entered across RPC awaits; durable CAS also serializes
/// different instances sharing a vault. An unknown outcome always retains evidence.
public actor AuthorityCoordinator {
    let account: Account
    let api: Api
    let storage: any AuthorityStorage
    let security: DeviceSecurityClient
    public nonisolated let gate: AuthorityGate
    let token: @Sendable () async throws -> String
    let clock: @Sendable () -> Int64
    var busy = false
    public init(account: Account, api: Api, storage: any AuthorityStorage,
                security: DeviceSecurityClient, gate: AuthorityGate,
                clock: @escaping @Sendable () -> Int64 = { Int64(Date().timeIntervalSince1970 * 1000) },
                token: @escaping @Sendable () async throws -> String) {
        self.account = account; self.api = api; self.storage = storage
        self.security = security; self.gate = gate; self.token = token; self.clock = clock
    }
    func begin() throws -> UInt64 {
        try DeviceSecurity.check(!busy, .conflict)
        let t = try gate.ticket(); busy = true; return t
    }
    func rpc<T: Sendable>(_ t: UInt64, _ body: @escaping @Sendable () async throws -> T) async throws -> T {
        let (id, task) = try gate.launch(t, body)
        defer { gate.untrack(id) }
        let result = try await withTaskCancellationHandler { try await task.value } onCancel: { task.cancel() }
        try gate.check(t)
        return result
    }
    func now(_ r: DeviceSecurity.Record) -> UInt64 {
        max(UInt64(max(clock(), 1)), r.floor, r.state.core.issuedAt,
            r.lease.flatMap { $0.head == (try? DeviceSecurity.stateHash(r.state)) ? $0.issuedAt : nil } ?? 0)
    }
    func authority(_ t: UInt64, head: Data? = nil) throws -> DeviceSecurity.Record {
        try gate.check(t)
        let r = try storage.readSecurity()
        try DeviceSecurity.check(r.memberID == account.deviceId && r.member
            && r.state.core.authorityID == account.deviceId, .untrusted)
        if let head { try DeviceSecurity.check(DeviceSecurity.stateHash(r.state) == head, .stale) }
        return r
    }
    public func isAuthority() throws -> Bool {
        let r = try storage.readSecurity()
        return r.memberID == account.deviceId && r.member && r.state.core.authorityID == account.deviceId
    }
    public func pendingOperation() throws -> SecurityOperation? { try storage.readOperation() }
    public func resumeSecurityOperation() async throws {
        let t = try begin(); defer { busy = false }
        try await resume(t)
    }
    func send(_ request: PBPutSecurityStateRequest, _ t: UInt64) async throws {
        let headers = Api.headers(token: try await rpc(t, token))
        let current = try storage.readSecurity()
        if let raw = request.states.last {
            let target = try DeviceSecurity.parseState(raw.state)
            let head = try DeviceSecurity.stateHash(current.state)
            try DeviceSecurity.check(head == target.core.parent || head == DeviceSecurity.stateHash(target), .conflict)
            if head == target.core.parent { _ = try authority(t, head: head) }
        } else if !request.lease.isEmpty {
            _ = try authority(t)
            try DeviceSecurity.HeadLease.parse(request.lease).verify(current.state)
        }
        try await rpc(t) { [api] in _ = try await api.client.putSecurityState(request: request, headers: headers).value() }
    }
    func advance(_ op: SecurityOperation, to phase: SecurityOperation.Phase) throws -> SecurityOperation {
        var next = op; next.phase = phase
        try storage.replaceOperation(expected: op, next: next); return next
    }
    /// Exact state/delivery bytes survive retries. Only the independent lease
    /// may be replaced after expiry; a competing parent never causes re-signing.
    func resume(_ t: UInt64) async throws {
        guard var op = try storage.readOperation() else { return }
        try gate.check(t)
        guard op.kind == .state || op.kind == .lease else {
            throw CommandError("pending", "请继续账户创建、恢复或接任草稿")
        }
        if op.phase == .delivered {
            if op.kind == .state {
                let request = try PBPutSecurityStateRequest(serializedBytes: op.signed)
                let target = try DeviceSecurity.parseState(request.states.last.unwrap().state)
                let installed = try storage.readSecurity().states[target.core.epoch].unwrap()
                try DeviceSecurity.check(DeviceSecurity.stateHash(installed) == DeviceSecurity.stateHash(target), .conflict)
            }
            try storage.replaceOperation(expected: op, next: nil); return
        }
        if op.kind == .state && op.phase != .prepared {
            let req = try PBPutSecurityStateRequest(serializedBytes: op.signed)
            if let raw = req.states.last {
                let target = try DeviceSecurity.parseState(raw.state)
                if target.core.authorityID != account.deviceId {
                    let record = try storage.readSecurity()
                    try DeviceSecurity.check(record.states[target.core.epoch].map { try DeviceSecurity.stateHash($0) } == DeviceSecurity.stateHash(target), .conflict)
                    try storage.replaceOperation(expected: op, next: nil); return
                }
            }
        }
        _ = try await rpc(t) { [security] in try await security.sync() }
        var r = try storage.readSecurity()
        let hash = try DeviceSecurity.stateHash(r.state)
        var request = try PBPutSecurityStateRequest(serializedBytes: op.signed)
        let target: DeviceSecurity.State
        if op.kind == .lease {
            if hash != op.parent {
                try DeviceSecurity.check(r.states.values.contains { (try? DeviceSecurity.stateHash($0)) == op.parent }, .conflict)
                try storage.replaceOperation(expected: op, next: nil); return
            }
            target = r.state
        } else {
            guard let raw = request.states.last else { throw DeviceSecurity.Failure.invalid }
            target = try DeviceSecurity.parseState(raw.state)
            if let installed = r.states[target.core.epoch] {
                try DeviceSecurity.check(DeviceSecurity.stateHash(installed) == DeviceSecurity.stateHash(target), .conflict)
            } else {
                try DeviceSecurity.check(hash == op.parent, .conflict)
                _ = try authority(t, head: op.parent)
                try DeviceSecurity.verifyTransition(parent: r.state, next: target)
            }
        }
        let installed = r.states[target.core.epoch] != nil
        // Do not publish an obsolete certificate on an already extended head.
        let targetIsHead = hash == (try DeviceSecurity.stateHash(target))
        if !installed || op.kind == .lease || targetIsHead {
            if target.core.authorityID == account.deviceId {
                let prior = request.lease.isEmpty ? nil : try DeviceSecurity.HeadLease.parse(request.lease)
                try prior?.verify(target)
                let committed = targetIsHead ? r.lease : nil
                let validCommitted = committed.flatMap { (try? $0.valid(target, now: now(r))) != nil ? $0 : nil }
                let usable = prior.flatMap { (try? $0.valid(target, now: now(r))) != nil ? $0 : nil }
                if usable == nil || (validCommitted?.issuedAt ?? 0) > (prior?.issuedAt ?? 0) {
                    let cert = try gate.withPermit(t) {
                        if let validCommitted, validCommitted.issuedAt >= (prior?.issuedAt ?? 0),
                            validCommitted.notAfter >= (prior?.notAfter ?? 0) {
                            return validCommitted // Preserve CryptoKit's exact randomized signature.
                        }
                        return try DeviceSecurity.HeadLease.sign(target, signer: SigningKey(seed: account.deviceSeed),
                            now: Int64(clamping: now(r)), floor: max(prior?.issuedAt ?? 0, committed?.issuedAt ?? 0))
                    }
                    var next = op; request.lease = try cert.bytes(); next.signed = try request.serializedData()
                    try storage.replaceOperation(expected: op, next: next); op = next
                }
            } else {
                // Handoff is signed by the retiring authority, never a lease for its successor.
                try DeviceSecurity.check(request.lease.isEmpty, .untrusted)
            }
            try gate.check(t)
            if !installed || op.kind == .lease {
                try await send(request, t)
                try await security.installPublication(request)
                try gate.check(t)
            } else if targetIsHead && !request.lease.isEmpty {
                var renewal = PBPutSecurityStateRequest(); renewal.lease = request.lease
                try await send(renewal, t)
                try await security.installPublication(renewal)
                try gate.check(t)
            }
        }
        // For a handoff, keep the ack as evidence even if the old token was revoked.
        if op.phase == .prepared { op = try advance(op, to: .published) }
        if !op.delivery.isEmpty {
            _ = try authority(t)
            let delivery = try PBApprovePairingRequest(serializedBytes: op.delivery)
            let headers = Api.headers(token: try await rpc(t, token))
            // The exact payload is retained until a positive receipt; missing/expired
            // pair slots require explicit candidate removal, never silent success.
            try await rpc(t) { [api] in _ = try await api.pairing.approvePairing(request: delivery, headers: headers).value() }
            var get = PBGetPairingRequest(); get.pairingID = delivery.pairingID
            let ask = get
            let status = try await rpc(t) { [api] in try await api.pairing.getPairing(request: ask, headers: headers).value() }
            guard status.state == .delivered else {
                if status.state == .canceled { throw PairError("配对已取消；请明确移除已批准候选后重新开始") }
                return
            }
        }
        r = try storage.readSecurity()
        if op.kind == .state {
            guard let installed = r.states[target.core.epoch] else { throw DeviceSecurity.Failure.conflict }
            try DeviceSecurity.check(DeviceSecurity.stateHash(installed) == DeviceSecurity.stateHash(target), .conflict)
        }
        op = try advance(op, to: .delivered)
        try storage.replaceOperation(expected: op, next: nil)
    }
    /// Synchronous builders are invoked inside the vault's empty-slot/head CAS.
    func prepareState(_ t: UInt64, r: DeviceSecurity.Record,
                      build: (DeviceSecurity.Record) throws -> (DeviceSecurity.State, Data, Data?)) throws {
        let hash = try DeviceSecurity.stateHash(r.state)
        _ = try storage.prepareOperation(expectedHead: hash) { committed in
            let r = try committed.unwrap()
            return try gate.withPermit(t) {
                let (state, delivery, suppliedLease) = try build(r)
                try DeviceSecurity.verifyTransition(parent: r.state, next: state)
                var request = PBPutSecurityStateRequest(); var item = PBSecurityState()
                item.state = try DeviceSecurity.stateBytes(state); request.states = [item]
                if let suppliedLease { request.lease = suppliedLease }
                else if state.core.authorityID == account.deviceId {
                    request.lease = try DeviceSecurity.HeadLease.sign(state, signer: SigningKey(seed: account.deviceSeed), now: Int64(clamping: now(r))).bytes()
                }
                return SecurityOperation(kind: .state, parent: hash, signed: try request.serializedData(), delivery: delivery)
            }
        }
    }
}
