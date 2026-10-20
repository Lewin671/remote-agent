import Foundation
import RAProto

/// A single private receipt. Recovery-code seeds/text must never enter this value.
public struct SecurityOperation: Codable, Equatable, Sendable, CustomStringConvertible,
    CustomDebugStringConvertible, CustomReflectable {
    public enum Kind: String, Codable, Sendable { case state, lease, creation, recovery, handoffOffer, deleteRecords }
    public enum Phase: Int, Codable, Sendable { case prepared, published, delivered }
    public let id: UUID
    public let kind: Kind
    public let parent: Data
    public var signed: Data
    public let local: Data
    public let delivery: Data
    public var phase: Phase
    public var result = Data()
    public var certificate = Data()
    public var completion = Data()
    public var codeSaved: Bool
    public init(kind: Kind, parent: Data, signed: Data, local: Data = Data(),
                delivery: Data = Data(), codeSaved: Bool = true) {
        id = UUID(); self.kind = kind; self.parent = parent; self.signed = signed
        self.local = local; self.delivery = delivery; phase = .prepared; self.codeSaved = codeSaved
    }
    public var description: String { "SecurityOperation(\(kind),\(phase))" }
    public var debugDescription: String { description }
    public var customMirror: Mirror { Mirror(self, children: [:]) }
    public func bytes() throws -> Data {
        let encoder = JSONEncoder(); encoder.outputFormatting = [.sortedKeys]
        return try encoder.encode(self)
    }
    /// Called with the committed receipt under the same storage lock as CAS.
    public func validateProgress(from old: Self) throws {
        try DeviceSecurity.check(id == old.id && kind == old.kind && parent == old.parent
            && local == old.local && delivery == old.delivery && phase.rawValue >= old.phase.rawValue
            && (old.completion.isEmpty || completion == old.completion)
            && (!old.codeSaved || codeSaved) && (old.result.isEmpty || result == old.result), .stale)
        if certificate != old.certificate {
            if kind == .handoffOffer || kind == .deleteRecords {
                // An independent renewal shares the receipt without replacing
                // its exact metadata/deletion request or advancing its phase.
                if !certificate.isEmpty {
                    let next = try DeviceSecurity.HeadLease.parse(certificate)
                    if !old.certificate.isEmpty {
                        let prior = try DeviceSecurity.HeadLease.parse(old.certificate)
                        if next.head == prior.head {
                            try DeviceSecurity.check(next.issuedAt >= prior.issuedAt && next.notAfter >= prior.notAfter
                                && (next.issuedAt != prior.issuedAt || next.notAfter == prior.notAfter), .stale)
                        }
                    }
                }
            } else {
                try DeviceSecurity.check(kind == .creation || kind == .recovery, .untrusted)
                let material = try AuthorityMaterial.parse(local)
                let target = try kind == .creation ? DeviceSecurity.Record.parse(material.chain).state
                    : DeviceSecurity.parseState(PBSubmitRecoveryRequest(serializedBytes: signed).state)
                let next = try DeviceSecurity.HeadLease.parse(certificate); try next.verify(target)
                if !old.certificate.isEmpty {
                    let prior = try DeviceSecurity.HeadLease.parse(old.certificate); try prior.verify(target)
                    try DeviceSecurity.check(next.issuedAt >= prior.issuedAt && next.notAfter >= prior.notAfter
                        && (next.issuedAt != prior.issuedAt || next.notAfter == prior.notAfter), .stale)
                }
            }
        }
        if signed == old.signed { return }
        // Only independent certificates and unsigned signup credentials may change.
        switch kind {
        case .state, .lease:
            var before = try PBPutSecurityStateRequest(serializedBytes: old.signed)
            var after = try PBPutSecurityStateRequest(serializedBytes: signed)
            let previous = before.lease; let next = after.lease
            before.lease = Data(); after.lease = Data()
            try DeviceSecurity.check(before == after && !next.isEmpty, .conflict)
            let cert = try DeviceSecurity.HeadLease.parse(next)
            if kind == .state {
                guard let raw = after.states.last else { throw DeviceSecurity.Failure.invalid }
                try cert.verify(DeviceSecurity.parseState(raw.state))
            } else { try DeviceSecurity.check(cert.head == parent, .untrusted) }
            if !previous.isEmpty {
                let prior = try DeviceSecurity.HeadLease.parse(previous)
                try DeviceSecurity.check(cert.issuedAt >= prior.issuedAt && cert.notAfter >= prior.notAfter
                    && (cert.issuedAt != prior.issuedAt || cert.notAfter == prior.notAfter), .stale)
            }
        case .creation:
            var before = try PBRegisterDeviceRequest(serializedBytes: old.signed)
            var after = try PBRegisterDeviceRequest(serializedBytes: signed)
            before.setupToken = ""; before.clearSignupProof()
            after.setupToken = ""; after.clearSignupProof()
            try DeviceSecurity.check(before == after, .conflict)
        default: throw DeviceSecurity.Failure.conflict
        }
    }
}

/// prepareOperation runs the synchronous signing builder while holding the platform
/// storage lock, after checking the head and empty slot. All instances share that lock.
public protocol AuthorityStorage: DeviceSecurity.Storage {
    func readOperation() throws -> SecurityOperation?
    func existingSecurity() throws -> DeviceSecurity.Record?
    func prepareOperation(expectedHead: Data?, _ build: (DeviceSecurity.Record?) throws -> SecurityOperation) throws -> SecurityOperation
    func replaceOperation(expected: SecurityOperation, next: SecurityOperation?) throws
    func savePublicProfile(_ profile: AccountPublicProfile) throws
    func publicProfile(pin: DeviceSecurity.Pin) throws -> AccountPublicProfile?
    /// Idempotent adoption, gated by the receipt under the platform storage lock.
    func adoptOperation(_ expected: SecurityOperation, account: Account, record: DeviceSecurity.Record,
                        profile: AccountPublicProfile) throws
}

/// Synchronous revocation prevents actor reentrancy/queued lifecycle messages from
/// allowing a signature after the scene became inactive or protected data locked.
public final class AuthorityGate: @unchecked Sendable {
    private let lock = NSLock()
    private var generation: UInt64 = 0
    private var allowed = false
    private var jobs: [UUID: @Sendable () -> Void] = [:]
    public init() {}
    public func set(active: Bool, protectedDataAvailable: Bool) {
        lock.lock()
        generation &+= 1; allowed = active && protectedDataAvailable
        let cancel = Array(jobs.values); jobs.removeAll()
        lock.unlock()
        cancel.forEach { $0() }
    }
    public func ticket() throws -> UInt64 {
        lock.lock(); defer { lock.unlock() }
        try DeviceSecurity.check(allowed, .untrusted)
        return generation
    }
    public func check(_ ticket: UInt64) throws {
        try withPermit(ticket) {}
    }
    public func withPermit<T>(_ ticket: UInt64, _ body: () throws -> T) throws -> T {
        lock.lock(); defer { lock.unlock() }
        try Task.checkCancellation()
        try DeviceSecurity.check(allowed && generation == ticket, .untrusted)
        return try body()
    }
    func launch<T: Sendable>(_ ticket: UInt64, _ body: @escaping @Sendable () async throws -> T) throws -> (UUID, Task<T, Error>) {
        lock.lock(); defer { lock.unlock() }
        try Task.checkCancellation()
        try DeviceSecurity.check(allowed && generation == ticket, .untrusted)
        let id = UUID()
        let task = Task { try self.check(ticket); return try await body() }
        jobs[id] = { task.cancel() }
        return (id, task)
    }
    public func track(_ ticket: UInt64, cancel: @escaping @Sendable () -> Void) throws -> UUID {
        lock.lock(); defer { lock.unlock() }
        try DeviceSecurity.check(allowed && generation == ticket, .untrusted)
        let id = UUID(); jobs[id] = cancel; return id
    }
    public func untrack(_ id: UUID) { lock.lock(); jobs[id] = nil; lock.unlock() }
}
