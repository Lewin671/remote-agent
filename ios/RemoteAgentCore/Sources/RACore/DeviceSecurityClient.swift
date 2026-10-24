import CryptoKit
import Foundation
import RAProto
import SwiftProtobuf

/// An origin-verified event across a verified recovery key discontinuity.
public struct RecoveryHistoryUnavailable: Error, Sendable {
    public let header: DeviceSecurity.Header
}

/// The client side of the phone-signed security domain. It keeps the local
/// chain, seals and opens envelopes with the current content key, pulls new
/// states from the relay.
public actor DeviceSecurityClient {
    public typealias StateFetch =
        @Sendable (UInt64) async throws -> PBGetSecurityStatesResponse
    private let store: any DeviceSecurity.Storage
    private let memberID: String
    private let signingKey: SigningKey
    private let fetchStates: StateFetch
    private let clock: @Sendable () -> UInt64
    private var current: DeviceSecurity.Record?
    private var syncJob: Task<DeviceSecurity.Record, Error>?
    private var pollJob: Task<Void, Never>?
    private var closed = false
    public private(set) var recoveryPending = false
    public private(set) var recoveryReadyMs: UInt64 = 0
    public private(set) var recoveryStatusStale = true
    public init(
        store: any DeviceSecurity.Storage, memberID: String, signingKey: SigningKey,
        clock: @escaping @Sendable () -> UInt64 = {
            UInt64(Date().timeIntervalSince1970 * 1000)
        },
        fetchStates: @escaping StateFetch
    ) {
        self.store = store
        self.memberID = memberID
        self.signingKey = signingKey
        self.clock = clock
        self.fetchStates = fetchStates
    }
    private func read() throws -> DeviceSecurity.Record {
        try DeviceSecurity.check(!closed, .storage)
        let r = try store.readSecurity()
        try DeviceSecurity.check(
            r.memberID == memberID && deviceId(publicKey: signingKey.publicKey) == memberID,
            .untrusted)
        current = r
        return r
    }
    private func keep(_ r: DeviceSecurity.Record) { current = r }
    public func identity() throws -> (DeviceSecurity.Pin, DeviceSecurity.State) {
        let r = try read()
        return (r.pin, r.state)
    }
    public func historicalMachines() throws -> Set<String> {
        let r=try read();return Set(r.states.values.flatMap{$0.core.members}.filter{$0.kind==1}.map{$0.id})
    }
    /// Proven historical routes only, never permission to control a retired device.
    public func replacementOrigins(target: String, proofs: [PBMachineReplacementProof]) throws -> Set<String> {
        let r = try read()
        try DeviceSecurity.check(proofs.count <= DeviceSecurity.maxPairStates, .untrusted)
        let states = r.states.values.sorted { $0.core.epoch < $1.core.epoch }
        var cursor = target
        var origins = Set<String>()
        for proof in proofs.reversed() {
            let a = try PBPairApproval(serializedBytes: proof.approval)
            try DeviceSecurity.check(proof.approval.count <= 1024 && a.accountID == r.pin.account && a.candidateKind == .machine && !a.replacesDeviceID.isEmpty && deviceId(publicKey: a.candidateDevicePublicKey) == cursor && a.nonce.count == 16 && a.pairingID.range(of:"^[0-9a-f]{32}$",options:.regularExpression) != nil && deviceId(publicKey: a.approverPublicKey) == a.approverDeviceID, .untrusted)
            guard let first = states.first(where: { $0.core.member(cursor) != nil }),
                let member = first.core.member(cursor), let signer = first.core.member(a.approverDeviceID) else { throw DeviceSecurity.Failure.untrusted }
            try DeviceSecurity.check(member.kind == 1 && member.ed == a.candidateDevicePublicKey && first.core.authorityID == a.approverDeviceID && signer.ed == a.approverPublicKey && first.core.removed.contains(a.replacesDeviceID) && a.expiresMs > 0 && UInt64(a.expiresMs) + DeviceSecurity.clockSkewLimit >= first.core.issuedAt, .untrusted)
            try DeviceSecurity.check(states.contains { $0.core.epoch < first.core.epoch && $0.core.member(a.replacesDeviceID)?.kind == 1 }, .untrusted)
            try DeviceSecurity.check(verifyEd25519(publicKey:a.approverPublicKey,message:Data("remote-agent/v1/pair-approval\0".utf8)+proof.approval,signature:proof.signature),.untrusted)
            try DeviceSecurity.check(origins.insert(a.replacesDeviceID).inserted,.untrusted)
            cursor = a.replacesDeviceID
        }
        return origins
    }
    public func leaseExpiration() throws -> UInt64 {
        let r = try read()
        guard let lease = r.lease else { return 0 }
        do { try lease.verify(r.state); return lease.notAfter } catch { return 0 }
    }
    public func headStateHash() throws -> Data { try DeviceSecurity.stateHash(try read().state) }
    public func close() {
        closed = true
        syncJob?.cancel()
        syncJob = nil
        pollJob?.cancel()
        pollJob = nil
    }
    /// Pulls new states now, then every 60 seconds while foreground. A head
    /// that moved calls `onChange`, so the member directory follows it even
    /// without a removal push.
    public func startPolling(
        everyMs: UInt64 = 60_000, onChange: (@Sendable () async -> Void)? = nil
    ) {
        guard pollJob == nil, !closed else { return }
        pollJob = Task { [weak self] in
            while !Task.isCancelled {
                do {
                    _ = try await self?.sync()
                    // The consumer acknowledges successful application, not a
                    // change within this particular pull. Failed/pending work
                    // must get another opportunity on an unchanged head.
                    if let onChange { await onChange() }
                } catch {}
                guard (try? await Task.sleep(nanoseconds: everyMs * 1_000_000)) != nil else {
                    break
                }
            }
        }
    }
    public func stopPolling() {
        pollJob?.cancel()
        pollJob = nil
    }
    public func sync() async throws -> DeviceSecurity.Record {
        if let syncJob { return try await syncJob.value }
        let task = Task { try await self.syncLoop() }
        syncJob = task
        defer { syncJob = nil }
        return try await task.value
    }
    private func syncLoop() async throws -> DeviceSecurity.Record {
        for _ in 0..<DeviceSecurity.maxStates {
            // Persist observed expiry even if fetching fails, before another seal can use the cache.
            let r = try touch(now: clock(), persist: true)
            let localNow = max(clock(), r.floor)
            try DeviceSecurity.check(
                r.state.core.issuedAt <= localNow + DeviceSecurity.clockSkewLimit, .clock)
            let res: PBGetSecurityStatesResponse
            do { res = try await fetchStates(r.headEpoch) }
            catch { recoveryStatusStale = true; throw error }
            recoveryPending = res.recoveryPending
            recoveryReadyMs = res.recoveryReadyMs
            recoveryStatusStale = false
            try DeviceSecurity.check(res.headEpoch >= r.headEpoch, .stale)
            if res.states.isEmpty {
                try DeviceSecurity.check(res.headEpoch == r.headEpoch, .stale)
                try acceptLease(res.lease)
                return try touch(now: clock())
            }
            var states: [DeviceSecurity.State] = []
            var proofs: [DeviceSecurity.RecoveryStatement?] = []
            let now = max(clock(), r.floor)
            for value in res.states {
                let s = try DeviceSecurity.parseState(value.state)
                try DeviceSecurity.check(
                    s.core.issuedAt <= now + DeviceSecurity.clockSkewLimit, .clock)
                let proof: DeviceSecurity.RecoveryStatement? =
                    value.recovery.isEmpty
                    ? nil : try DeviceSecurity.RecoveryStatement.parse(value.recovery)
                states.append(s)
                proofs.append(proof)
            }
            let next = try install(states, proofs)
            if next.headEpoch >= res.headEpoch { try acceptLease(res.lease); return try touch(now: clock()) }
        }
        throw DeviceSecurity.Failure.capacity
    }
    func acceptLease(_ raw:Data) throws {
        if raw.isEmpty { return }
        var r = try read()
        let l = try DeviceSecurity.HeadLease.parse(raw)
        try l.verify(r.state)
        try DeviceSecurity.check(l.issuedAt <= max(clock(),r.floor)+DeviceSecurity.clockSkewLimit,.clock)
        r.lease=l;r.floor=max(clock(),r.floor);r.floorSaved=r.floor
        r = try store.writeSecurity(expectedHead: DeviceSecurity.stateHash(r.state),r)
        keep(r)
    }
    /// Appends a contiguous chain segment and persists it before use.
    func install(
        _ states: [DeviceSecurity.State], _ proofs: [DeviceSecurity.RecoveryStatement?]
    ) throws -> DeviceSecurity.Record {
        try DeviceSecurity.check(!states.isEmpty && states.count == proofs.count)
        var next = try read()
        // Cache every key still derivable before a recovery boundary makes
        // the PrevSecret walk stop; retained devices keep their history.
        if let head = next.states[next.headEpoch] {
            for ep in stride(from: head.core.contentEpoch, through: 1, by: -1) {
                if next.contentKeys[ep] != nil { continue }
                if let key = try? next.contentSecret(ep) { next.contentKeys[ep] = key }
            }
        }
        for (i, s) in states.enumerated() {
            if s.core.epoch <= next.headEpoch {
                guard let old = next.states[s.core.epoch],
                    try DeviceSecurity.stateHash(old) == DeviceSecurity.stateHash(s)
                else { throw DeviceSecurity.Failure.stale }
                continue
            }
            try DeviceSecurity.check(
                s.core.epoch == next.headEpoch + 1 && next.headEpoch < DeviceSecurity.maxStates,
                .stale)
            guard let prev = next.states[next.headEpoch] else {
                throw DeviceSecurity.Failure.storage
            }
            try DeviceSecurity.verifyTransition(parent: prev, next: s, recovery: proofs[i])
            if s.core.member(memberID) != nil {
                let key = try DeviceSecurity.openGrant(
                    next.pin, s, recipient: memberID, x: next.xKey)
                if s.core.contentEpoch != prev.core.contentEpoch {
                    if proofs[i] == nil, let old = next.secret {
                        guard
                            try DeviceSecurity.openPrevSecret(
                                newSecret: key, blob: s.prevSecret) == old
                        else { throw DeviceSecurity.Failure.untrusted }
                    }
                    if let old = next.secret { next.contentKeys[prev.core.contentEpoch] = old }
                    next.secret = key
                    next.member = true
                } else if next.secret != nil {
                    next.secret = key
                    next.member = true
                }
            } else {
                next.member = false
            }
            next.states[s.core.epoch] = s
            if let proof = proofs[i] { next.recovery[s.core.epoch] = proof }
            next.headEpoch = s.core.epoch
            next.lease = nil
        }
        let expected = try DeviceSecurity.stateHash(try read().state)
        next = try store.writeSecurity(expectedHead: expected, next)
        keep(next)
        return next
    }
    private func touch(now: UInt64, persist: Bool = false) throws -> DeviceSecurity.Record {
        var r = try read()
        let n = max(r.floor, max(now, clock()))
        r.floor = n
        if n > r.floorSaved && (persist || n - r.floorSaved >= 1000) {
            r.floorSaved = n
            r = try store.writeSecurity(
                expectedHead: try DeviceSecurity.stateHash(r.state), r)
        }
        keep(r)
        return r
    }
    public func record() throws -> DeviceSecurity.Record { try read() }
    public func installPublication(_ request: PBPutSecurityStateRequest) throws {
        if !request.states.isEmpty {
            _ = try install(request.states.map { try DeviceSecurity.parseState($0.state) },
                request.states.map { $0.recovery.isEmpty ? nil : try DeviceSecurity.RecoveryStatement.parse($0.recovery) })
        }
        try acceptLease(request.lease)
    }
    /// Seals locally with the current content key and a random nonce.
    public func seal(
        purpose: DeviceSecurity.Purpose, machine: String = "", session: String = "",
        request: String = "", seq: UInt64 = 0, deadline: UInt64 = 0, plaintext: Data,
        sessionSeed: Data? = nil, authorityGate: AuthorityGate? = nil, authorityTicket: UInt64 = 0
    ) async throws -> PBEnvelope {
        let r = try read()
        try DeviceSecurity.check(r.member && r.secret != nil, .untrusted)
        let head = r.state
        // All new ciphertext needs a live lease; historical reads are separate.
        let now = max(clock(), r.floor)
        try DeviceSecurity.check(
            head.core.issuedAt <= now + DeviceSecurity.clockSkewLimit, .clock)
        guard let lease = r.lease else { throw DeviceSecurity.Failure.lease }
        try lease.valid(head, now: now)
        guard let secret = r.secret else { throw DeviceSecurity.Failure.untrusted }
        let h = DeviceSecurity.Header(
            account: r.pin.account, domain: r.pin.domain, epoch: head.core.epoch,
            state: try DeviceSecurity.stateHash(head), contentEpoch: head.core.contentEpoch,
            sender: memberID, kind: purpose.kind, purpose: purpose, machine: machine,
            session: session, request: request, seq: seq, deadline: deadline, compression: 0)
        _ = try DeviceSecurity.scope(h, head)
        let make = { try DeviceSecurity.form(
            secret: secret, sessionSeed: sessionSeed, header: h, plaintext: plaintext, signingKey: self.signingKey) }
        let formed = try authorityGate.map { try $0.withPermit(authorityTicket, make) } ?? make()
        return try DeviceSecurity.envelope(DeviceSecurity.Wire(formed: formed))
    }
    private func contentKey(
        _ r: DeviceSecurity.Record, _ h: DeviceSecurity.Header, _ wire: DeviceSecurity.Wire,
        _ state: DeviceSecurity.State, live: Bool
    ) throws -> Data {
        do { return try r.contentSecret(h.contentEpoch) } catch DeviceSecurity.Failure.unavailable {
            _ = try DeviceSecurity.verifyOrigin(
                wire, state: state, head: r.state, live: live, now: max(clock(), r.floor))
            let boundary = r.recovery.keys.contains { epoch in
                guard epoch > h.epoch, let state = r.states[epoch] else { return false }
                return state.core.contentEpoch > h.contentEpoch && state.prevSecret.isEmpty
            }
            if h.purpose == .event && boundary { throw RecoveryHistoryUnavailable(header: h) }
            throw DeviceSecurity.Failure.unavailable
        }
    }
    public func openCurrent(_ envelope: PBEnvelope, sessionSeed: Data? = nil) async throws -> (
        DeviceSecurity.Header, Data
    ) {
        let wire = try DeviceSecurity.fromEnvelope(envelope)
        let h = try DeviceSecurity.parseHeader(wire.formed.header)
        var r = try read()
        if r.states[h.epoch] == nil { r = try await sync() }
        guard r.states[h.epoch] != nil else { throw DeviceSecurity.Failure.unknown }
        if h.purpose.control {guard let lease=r.lease else {throw DeviceSecurity.Failure.lease};try lease.valid(r.state,now:max(clock(),r.floor))}
        let secret = try contentKey(r, h, wire, r.states[h.epoch]!, live: true)
        if r.contentKeys[h.contentEpoch] == nil {
            r.contentKeys[h.contentEpoch] = secret
            keep(r)
        }
        let (header, plain) = try DeviceSecurity.open(
            wire, state: r.states[h.epoch]!, head: r.state, live: true,
            now: max(clock(), r.floor), secret: secret, sessionSeed: sessionSeed)
        return (header, try unpackSecurity(header, plain))
    }
    /// Stored history is verified against its own state, never authorized as
    /// a current control action. Content before a recovery boundary is
    /// unavailable rather than retried.
    public func openHistory(_ envelope: PBEnvelope, sessionSeed: Data? = nil) async throws -> (
        DeviceSecurity.Header, Data
    ) {
        let w = try DeviceSecurity.fromEnvelope(envelope)
        let h = try DeviceSecurity.parseHeader(w.formed.header)
        try DeviceSecurity.check(!h.purpose.control)
        var r = try read()
        if h.epoch > r.headEpoch { r = try await sync() }
        guard let s = r.states[h.epoch] else { throw DeviceSecurity.Failure.stale }
        let key = try contentKey(r, h, w, s, live: false)
        if r.contentKeys[h.contentEpoch] == nil {
            r.contentKeys[h.contentEpoch] = key
            keep(r)
        }
        let (header, plain) = try DeviceSecurity.open(
            w, state: s, head: nil, live: false, now: 0, secret: key, sessionSeed: sessionSeed)
        return (header, try unpackSecurity(header, plain))
    }
    public func openMessage<M: SwiftProtobuf.Message>(
        _ envelope: PBEnvelope, as type: M.Type, history: Bool, sessionSeed: Data? = nil
    ) async throws -> (DeviceSecurity.Header, M) {
        let (h, p) =
            try await
            (history
            ? openHistory(envelope, sessionSeed: sessionSeed)
            : openCurrent(envelope, sessionSeed: sessionSeed))
        do {
            let m = try M(serializedBytes: p)
            try validateSecurityBody(h, m)
            return (h, m)
        } catch { throw DeviceSecurity.Failure.invalid }
    }
}
func unpackSecurity(_ h: DeviceSecurity.Header, _ p: Data) throws -> Data {
    let limit = h.purpose.mutable ? 64 << 10 : 4 << 20
    if h.compression == 0 {
        try DeviceSecurity.check(p.count <= limit, .capacity)
        return p
    }
    try DeviceSecurity.check(h.compression == 1)
    return try unpack(p, max: limit)
}
func validateSecurityBody<M: SwiftProtobuf.Message>(_ h: DeviceSecurity.Header, _ m: M) throws {
    switch h.purpose {
    case .event:
        guard let e = m as? PBEvent, e.sessionID == h.session, e.seq == h.seq, e.kind != nil else {
            throw DeviceSecurity.Failure.invalid
        }
    case .sessionMeta:
        guard let s = m as? PBSession, s.id == h.session else {
            throw DeviceSecurity.Failure.invalid
        }
    case .machineMeta: try DeviceSecurity.check(m is PBMachineMeta)
    case .sessionKey: throw DeviceSecurity.Failure.invalid
    case .deviceMeta: try DeviceSecurity.check(m is PBDeviceMeta)
    case .prefs:
        guard let p = m as? PBProjectPrefs, p.schema == 1 else {
            throw DeviceSecurity.Failure.invalid
        }
    case .command:
        guard let c = m as? PBCommand, c.kind != nil else { throw DeviceSecurity.Failure.invalid }
    case .result: try DeviceSecurity.check(m is PBCommandResult)
    case .image: try DeviceSecurity.check(m is PBImagePayload)
    case .notify:
        guard let n = m as? PBNotifyContent, n.machineID == h.machine, n.sessionID == h.session,
            n.kind == .attention || n.kind == .turnEnded || n.kind == .scheduled
        else { throw DeviceSecurity.Failure.invalid }
    case .terminalInput, .terminalOutput, .terminalResize, .terminalHistory:
        guard let f = m as? PBTerminalFrame else { throw DeviceSecurity.Failure.invalid }
        switch (h.purpose, f.kind) {
        case (.terminalInput, .input), (.terminalResize, .resize),
            (.terminalHistory, .historyRequest),
            (.terminalOutput, .snapshot), (.terminalOutput, .output),
            (.terminalOutput, .historyPage),
            (.terminalOutput, .closed):
            break
        default: throw DeviceSecurity.Failure.invalid
        }
    }
}
