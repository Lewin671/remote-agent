import Foundation
import RAProto

/// What listeners are told about chain syncs and local installations.
public struct SyncObserver: Sendable {
    var synced: @Sendable (DeviceSecurity.Record, UInt64) async -> Void = { _, _ in }
    var failed: @Sendable () -> Void = {}
    var installed: @Sendable (DeviceSecurity.Record) async -> Void = { _ in }
    public init() {}
    init(
        synced: @escaping @Sendable (DeviceSecurity.Record, UInt64) async -> Void,
        failed: @escaping @Sendable () -> Void,
        installed: @escaping @Sendable (DeviceSecurity.Record) async -> Void
    ) {
        self.synced = synced
        self.failed = failed
        self.installed = installed
    }
}

/// Authority and chain security manager backed by `DeviceSecurity.Storage` and Go `SecCall`.
/// All envelope sealing, opening, origin verification, and body validation live in `clientcore/security`.
public actor AuthoritySecurity {
    public typealias StateFetch = @Sendable (UInt64) async throws -> PBGetSecurityStatesResponse

    private struct Pull: Sendable {
        let id: UUID
        let generation: UInt64
        let task: Task<Void, Never>
        let result: SharedResult<DeviceSecurity.Record>
    }

    private let store: any DeviceSecurity.Storage
    private let memberID: String
    private let signingKey: SigningKey
    private let fetchStates: StateFetch
    private let clock: @Sendable () -> UInt64
    private let pullTimeoutMs: Int64
    private let observer: SyncObserver

    private var syncJob: Pull?
    private var syncGeneration: UInt64 = 0
    private var closed = false
    public private(set) var recoveryPending = false
    public private(set) var recoveryReadyMs: UInt64 = 0
    public private(set) var recoveryStatusStale = true

    public init(
        store: any DeviceSecurity.Storage,
        memberID: String,
        signingKey: SigningKey,
        clock: @escaping @Sendable () -> UInt64 = { UInt64(Date().timeIntervalSince1970 * 1000) },
        pullTimeoutMs: Int64 = 15_000,
        observer: SyncObserver = SyncObserver(),
        fetchStates: @escaping StateFetch
    ) {
        self.store = store
        self.memberID = memberID
        self.signingKey = signingKey
        self.clock = clock
        self.pullTimeoutMs = pullTimeoutMs
        self.observer = observer
        self.fetchStates = fetchStates
    }

    private func read() throws -> DeviceSecurity.Record {
        try DeviceSecurity.check(!closed, .storage)
        let r = try store.readSecurity()
        try DeviceSecurity.check(
            r.memberID == memberID && deviceId(publicKey: signingKey.publicKey) == memberID,
            .untrusted
        )
        return r
    }

    public func identity() throws -> (DeviceSecurity.Pin, DeviceSecurity.State) {
        let r = try read()
        return (r.pin, r.state)
    }

    public func record() throws -> DeviceSecurity.Record { try read() }

    public func historicalMachines() throws -> Set<String> {
        let r = try read()
        return Set(r.states.values.flatMap(\.core.members).filter { $0.kind == 1 }.map(\.id))
    }

    public func leaseExpiration() throws -> UInt64 {
        let r = try read()
        guard let lease = r.lease else { return 0 }
        do {
            try lease.verify(r.state)
            return lease.notAfter
        } catch {
            return 0
        }
    }

    public func headStateHash() throws -> Data {
        try DeviceSecurity.stateHash(try read().state)
    }

    public func close() {
        closed = true
        syncGeneration &+= 1
        syncJob?.task.cancel()
        syncJob = nil
    }

    public func networkChanged() {
        syncGeneration &+= 1
        syncJob?.task.cancel()
        syncJob = nil
    }

    public func touchAfterWake() throws {
        _ = try touch(now: clock(), persist: true)
    }

    public func sync() async throws -> DeviceSecurity.Record {
        let started = clock()
        do {
            let record = try await pullShared()
            await observer.synced(record, started)
            return record
        } catch {
            if !(error is CancellationError) { observer.failed() }
            throw error
        }
    }

    private func pullShared() async throws -> DeviceSecurity.Record {
        while true {
            try Task.checkCancellation()
            let flight: Pull
            if let existing = syncJob {
                flight = existing
            } else {
                let generation = syncGeneration
                let id = UUID()
                let result = SharedResult<DeviceSecurity.Record>()
                let task = Task {
                    let outcome: Result<DeviceSecurity.Record, Error>
                    do {
                        let rec = try await withTimeout(
                            self.pullTimeoutMs,
                            sleep: { try await Task.sleep(for: .milliseconds($0)) }
                        ) {
                            try await self.syncLoop(generation)
                        }
                        outcome = .success(rec)
                    } catch {
                        outcome = .failure(error)
                    }
                    if self.syncJob?.id == id { self.syncJob = nil }
                    result.resolve(outcome)
                }
                flight = Pull(id: id, generation: generation, task: task, result: result)
                syncJob = flight
            }
            do {
                return try await flight.result.value
            } catch {
                if flight.generation != syncGeneration && !Task.isCancelled && !closed { continue }
                throw error
            }
        }
    }

    private func syncLoop(_ generation: UInt64) async throws -> DeviceSecurity.Record {
        for _ in 0..<DeviceSecurity.maxStates {
            let r = try touch(now: clock(), persist: true)
            let localNow = max(clock(), r.floor)
            try DeviceSecurity.check(
                r.state.core.issuedAt <= localNow + DeviceSecurity.clockSkewLimit,
                .clock
            )
            let res: PBGetSecurityStatesResponse
            do {
                res = try await fetchStates(r.headEpoch)
            } catch {
                if generation == syncGeneration { recoveryStatusStale = true }
                throw error
            }
            try Task.checkCancellation()
            guard generation == syncGeneration else { throw CancellationError() }
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
                try DeviceSecurity.check(s.core.issuedAt <= now + DeviceSecurity.clockSkewLimit, .clock)
                let proof = value.recovery.isEmpty ? nil : try DeviceSecurity.RecoveryStatement.parse(value.recovery)
                states.append(s)
                proofs.append(proof)
            }
            let next = try await install(states, proofs)
            if next.headEpoch >= res.headEpoch {
                try acceptLease(res.lease)
                return try touch(now: clock())
            }
        }
        throw DeviceSecurity.Failure.capacity
    }

    func acceptLease(_ raw: Data) throws {
        if raw.isEmpty { return }
        var r = try read()
        let l = try DeviceSecurity.HeadLease.parse(raw)
        try l.verify(r.state)
        try DeviceSecurity.check(
            l.issuedAt <= max(clock(), r.floor) + DeviceSecurity.clockSkewLimit,
            .clock
        )
        r.lease = l
        r.floor = max(clock(), r.floor)
        r.floorSaved = r.floor
        r = try store.writeSecurity(expectedHead: try DeviceSecurity.stateHash(r.state), r)
    }

    func install(
        _ states: [DeviceSecurity.State],
        _ proofs: [DeviceSecurity.RecoveryStatement?],
        notifyObserver: Bool = true
    ) async throws -> DeviceSecurity.Record {
        let next = try installSync(states, proofs)
        if notifyObserver { await observer.installed(next) }
        return next
    }

    private func installSync(
        _ states: [DeviceSecurity.State],
        _ proofs: [DeviceSecurity.RecoveryStatement?]
    ) throws -> DeviceSecurity.Record {
        try DeviceSecurity.check(!states.isEmpty && states.count == proofs.count)
        var next = try read()
        if let head = next.states[next.headEpoch] {
            for ep in stride(from: head.core.contentEpoch, through: 1, by: -1) {
                if next.contentKeys[ep] != nil { continue }
                if let key = try? next.contentSecret(ep) { next.contentKeys[ep] = key }
            }
        }
        var chain: [DeviceSecurity.State] = []
        var chainProofs: [DeviceSecurity.RecoveryStatement?] = []
        chain.reserveCapacity(Int(next.headEpoch) + states.count)
        chainProofs.reserveCapacity(Int(next.headEpoch) + states.count)
        for ep in 1...next.headEpoch {
            guard let prev = next.states[ep] else { throw DeviceSecurity.Failure.storage }
            chain.append(prev)
            chainProofs.append(next.recovery[ep])
        }
        var sawNew = false
        var expectedEpoch = next.headEpoch + 1
        var pendingIndices: [Int] = []
        pendingIndices.reserveCapacity(states.count)
        for (i, s) in states.enumerated() {
            if !sawNew && s.core.epoch <= next.headEpoch {
                guard let old = next.states[s.core.epoch],
                      try DeviceSecurity.stateHash(old) == DeviceSecurity.stateHash(s)
                else { throw DeviceSecurity.Failure.stale }
                continue
            }
            sawNew = true
            try DeviceSecurity.check(
                s.core.epoch == expectedEpoch && next.headEpoch < DeviceSecurity.maxStates
                    && expectedEpoch <= DeviceSecurity.maxStates,
                .stale
            )
            chain.append(s)
            chainProofs.append(proofs[i])
            pendingIndices.append(i)
            expectedEpoch += 1
        }
        if !pendingIndices.isEmpty {
            try DeviceSecurity.verifyChain(pin: next.pin, states: chain, proofs: chainProofs)
        }
        for i in pendingIndices {
            let s = states[i]
            guard let prev = next.states[next.headEpoch] else {
                throw DeviceSecurity.Failure.storage
            }
            if s.core.member(memberID) != nil {
                let key = try DeviceSecurity.openGrant(next.pin, s, recipient: memberID, xPrivate: next.xPrivateRaw)
                if s.core.contentEpoch != prev.core.contentEpoch {
                    if proofs[i] == nil, let old = next.secret {
                        guard try DeviceSecurity.openPrevSecret(newSecret: key, blob: s.prevSecret) == old else {
                            throw DeviceSecurity.Failure.untrusted
                        }
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
        return try store.writeSecurity(expectedHead: expected, next)
    }

    private func touch(now: UInt64, persist: Bool = false) throws -> DeviceSecurity.Record {
        var r = try read()
        let n = max(r.floor, max(now, clock()))
        r.floor = n
        if n > r.floorSaved && (persist || n - r.floorSaved >= 1000) {
            r.floorSaved = n
            r = try store.writeSecurity(expectedHead: try DeviceSecurity.stateHash(r.state), r)
        }
        return r
    }

    public func installPublication(_ request: PBPutSecurityStateRequest) async throws {
        if !request.states.isEmpty {
            _ = try installSync(
                try request.states.map { try DeviceSecurity.parseState($0.state) },
                try request.states.map { $0.recovery.isEmpty ? nil : try DeviceSecurity.RecoveryStatement.parse($0.recovery) }
            )
        }
        try acceptLease(request.lease)
        await observer.installed(try read())
    }
}
