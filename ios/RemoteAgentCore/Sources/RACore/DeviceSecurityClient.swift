import CryptoKit
import Foundation
import RAProto
import SwiftProtobuf

/// The client side of the phone-signed security domain. It keeps the local
/// chain, seals and opens envelopes with the current content key, pulls new
/// states from the relay, and reserves counter blocks before use.
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
        if let current { return current }
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
                let before = try? await self?.headStateHash()
                _ = try? await self?.sync()
                if let onChange, let before, let after = try? await self?.headStateHash(),
                    after != before
                {
                    await onChange()
                }
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
            let r = try read()
            let localNow = max(clock(), r.floor)
            try DeviceSecurity.check(
                r.state.core.issuedAt <= localNow + DeviceSecurity.clockSkewLimit, .clock)
            let res = try await fetchStates(r.headEpoch)
            try DeviceSecurity.check(res.headEpoch >= r.headEpoch, .stale)
            if res.states.isEmpty {
                try DeviceSecurity.check(res.headEpoch == r.headEpoch, .stale)
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
            if next.headEpoch >= res.headEpoch { return try touch(now: clock()) }
        }
        throw DeviceSecurity.Failure.capacity
    }
    /// Appends a contiguous chain segment and persists it before use.
    private func install(
        _ states: [DeviceSecurity.State], _ proofs: [DeviceSecurity.RecoveryStatement?]
    ) throws -> DeviceSecurity.Record {
        try DeviceSecurity.check(!states.isEmpty && states.count == proofs.count)
        var next = try read()
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
                    next.counter = 0
                    next.blockEnd = 0
                    next.counterEpoch = s.core.contentEpoch
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
        }
        let expected = try DeviceSecurity.stateHash(try read().state)
        next = try store.writeSecurity(expectedHead: expected, next)
        keep(next)
        return next
    }
    private func touch(now: UInt64) throws -> DeviceSecurity.Record {
        var r = try read()
        let n = max(now, clock())
        if n <= r.floor { return r }
        r.floor = n
        if n - r.floorSaved >= 1000 {
            r.floorSaved = n
            r = try store.writeSecurity(
                expectedHead: try DeviceSecurity.stateHash(r.state), r)
            keep(r)
        }
        return r
    }
    /// Seals one envelope with the current head and content key. A counter
    /// block is written durably before any of its values are used.
    public func seal(
        purpose: DeviceSecurity.Purpose, machine: String = "", session: String = "",
        request: String = "", seq: UInt64 = 0, deadline: UInt64 = 0, plaintext: Data,
        sessionSeed: Data? = nil
    ) async throws -> PBEnvelope {
        var r = try read()
        try DeviceSecurity.check(r.member && r.secret != nil, .untrusted)
        let head = r.state
        if purpose.control {
            // Control pauses when the lease ends; signing anyway would only
            // produce an envelope the executor refuses.
            let now = max(clock(), r.floor)
            try DeviceSecurity.check(head.core.issuedAt <= now + DeviceSecurity.clockSkewLimit, .clock)
            try DeviceSecurity.check(head.core.notAfter >= now, .lease)
        }
        if head.core.contentEpoch != r.counterEpoch {
            r.counter = 0
            r.blockEnd = 0
            r.counterEpoch = head.core.contentEpoch
        }
        if r.counter >= r.blockEnd {
            try DeviceSecurity.check(
                r.blockEnd <= UInt64.max - DeviceSecurity.counterBlock, .capacity)
            // Persist the whole block before any value in it is handed out;
            // the in-memory counter moves on without another write.
            r.blockEnd += DeviceSecurity.counterBlock
            r = try store.writeSecurity(
                expectedHead: try DeviceSecurity.stateHash(head), r)
        }
        r.counter += 1
        keep(r)
        guard let secret = r.secret else { throw DeviceSecurity.Failure.untrusted }
        let h = DeviceSecurity.Header(
            account: r.pin.account, domain: r.pin.domain, epoch: head.core.epoch,
            state: try DeviceSecurity.stateHash(head), contentEpoch: head.core.contentEpoch,
            sender: memberID, kind: purpose.kind, purpose: purpose, machine: machine,
            session: session, request: request, seq: seq, deadline: deadline, compression: 0,
            counter: r.counter)
        _ = try DeviceSecurity.scope(h, head)
        let formed = try DeviceSecurity.form(
            secret: secret, sessionSeed: sessionSeed, header: h, plaintext: plaintext,
            signingKey: signingKey)
        return try DeviceSecurity.envelope(DeviceSecurity.Wire(formed: formed))
    }
    public func openCurrent(_ envelope: PBEnvelope, sessionSeed: Data? = nil) async throws -> (
        DeviceSecurity.Header, Data
    ) {
        let wire = try DeviceSecurity.fromEnvelope(envelope)
        let h = try DeviceSecurity.parseHeader(wire.formed.header)
        var r = try read()
        if r.states[h.epoch] == nil { r = try await sync() }
        guard r.states[h.epoch] != nil else { throw DeviceSecurity.Failure.unknown }
        let secret = try r.contentSecret(h.contentEpoch)
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
        let key = try r.contentSecret(h.contentEpoch)
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
            n.kind == .attention || n.kind == .turnEnded
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
