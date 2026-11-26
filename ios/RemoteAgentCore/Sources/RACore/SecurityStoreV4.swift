import CryptoKit
import Foundation
import RAProto

public struct SecurityStoreBucketKey: Hashable, Codable, Sendable {
    public var bucket: Int32
    public var key: String

    public init(bucket: Int32, key: String) {
        self.bucket = bucket
        self.key = key
    }
}

public struct SecurityStoreStoredBucket: Codable, Equatable, Sendable {
    public var bucket: Int32
    public var key: String
    public var revision: String
    public var data: Data

    public init(bucket: Int32, key: String, revision: String, data: Data) {
        self.bucket = bucket
        self.key = key
        self.revision = revision
        self.data = data
    }
}

public struct SecurityStoreKeyId: Hashable, Codable, Sendable {
    public var kind: Int32
    public var slot: String

    public init(kind: Int32, slot: String) {
        self.kind = kind
        self.slot = slot
    }
}

public struct SecurityStoreStoredKey: Codable, Equatable, Sendable {
    public var kind: Int32
    public var slot: String
    public var privateKey: Data
    public var publicKey: Data

    public init(kind: Int32 = 0, slot: String = "", privateKey: Data, publicKey: Data) {
        self.kind = kind
        self.slot = slot
        self.privateKey = privateKey
        self.publicKey = publicKey
    }
}

/// Abstracts OS-backed Keychain (app) vs in-memory (SwiftPM tests) persistence for S6 v4 state.
public protocol SecurityStoreV4: Sendable {
    func hasPersistedState() -> Bool
    func generateKey(kind: PBSecurityKeyKind, slot: String, persist: Bool) throws -> Data
    func signKey(kind: PBSecurityKeyKind, slot: String, message: Data) throws -> Data
    func agreeKey(kind: PBSecurityKeyKind, slot: String, peerPublic: Data) throws -> Data
    func encapsulate(recipients: [PBSecurityEncapsulateRecipient]) throws -> [PBSecurityEncapsulatedPair]
    func discardKey(kind: PBSecurityKeyKind, slot: String)
    func executeStorage(_ effect: PBSecurityStorageEffect) -> PBSecurityStorageResultEvent
    func dismissLegacyRepairNotice()
    func clearMemoryCache()
    func loadLoginToken(server: String, deviceId: String) -> SavedLogin?
    func saveLoginToken(_ token: SavedLogin, server: String, deviceId: String)
    func clearLoginToken(server: String, deviceId: String)
    func readNotifyBlob() -> Data?
}

public extension SecurityStoreV4 {
    func readNotifyBlob() -> Data? { nil }
}

public protocol SecurityStoreV4Proxy: SecurityStoreV4 {
    var securityCore: SecurityStoreV4Core { get }
}

public extension SecurityStoreV4Proxy {
    func hasPersistedState() -> Bool { securityCore.hasPersistedState() }
    func generateKey(kind: PBSecurityKeyKind, slot: String, persist: Bool) throws -> Data { try securityCore.generateKey(kind: kind, slot: slot, persist: persist) }
    func signKey(kind: PBSecurityKeyKind, slot: String, message: Data) throws -> Data { try securityCore.signKey(kind: kind, slot: slot, message: message) }
    func agreeKey(kind: PBSecurityKeyKind, slot: String, peerPublic: Data) throws -> Data { try securityCore.agreeKey(kind: kind, slot: slot, peerPublic: peerPublic) }
    func encapsulate(recipients: [PBSecurityEncapsulateRecipient]) throws -> [PBSecurityEncapsulatedPair] { try securityCore.encapsulate(recipients: recipients) }
    func discardKey(kind: PBSecurityKeyKind, slot: String) { securityCore.discardKey(kind: kind, slot: slot) }
    func executeStorage(_ effect: PBSecurityStorageEffect) -> PBSecurityStorageResultEvent { securityCore.executeStorage(effect) }
    func dismissLegacyRepairNotice() { securityCore.dismissLegacyRepairNotice() }
    func clearMemoryCache() { securityCore.clearMemoryCache() }
    func loadLoginToken(server: String, deviceId: String) -> SavedLogin? { securityCore.loadLoginToken(server: server, deviceId: deviceId) }
    func saveLoginToken(_ token: SavedLogin, server: String, deviceId: String) { securityCore.saveLoginToken(token, server: server, deviceId: deviceId) }
    func clearLoginToken(server: String, deviceId: String) { securityCore.clearLoginToken(server: server, deviceId: deviceId) }
    func readNotifyBlob() -> Data? { securityCore.readNotifyBlob() }
}

public enum SecurityStoreError: Error, LocalizedError, Sendable {
    case unsupportedKeyKind
    case missingSigningKey
    case missingAgreementKey
    case invalidPeerPublicKey
    case locked
    case storage(String)

    public var errorDescription: String? {
        switch self {
        case .unsupportedKeyKind: return "unsupported key kind"
        case .missingSigningKey: return "missing signing key"
        case .missingAgreementKey: return "missing agreement key"
        case .invalidPeerPublicKey: return "invalid peer public key"
        case .locked: return "key store locked"
        case .storage(let msg): return msg
        }
    }
}

public enum SecurityStoreCodec {
    private static func encode<T: Encodable>(_ value: T) throws -> Data {
        try PropertyListEncoder.binary.encode(value)
    }

    public static func encodeBuckets(_ buckets: [SecurityStoreBucketKey: SecurityStoreStoredBucket]) throws -> Data {
        try encode(buckets.values.sorted { ($0.bucket, $0.key) < ($1.bucket, $1.key) })
    }

    public static func decodeBuckets(_ data: Data) throws -> [SecurityStoreBucketKey: SecurityStoreStoredBucket] {
        guard !data.isEmpty else { return [:] }
        let rows = try PropertyListDecoder().decode([SecurityStoreStoredBucket].self, from: data)
        return Dictionary(uniqueKeysWithValues: rows.map { (SecurityStoreBucketKey(bucket: $0.bucket, key: $0.key), $0) })
    }

    public static func encodeKeys(_ keys: [SecurityStoreKeyId: SecurityStoreStoredKey]) throws -> Data {
        let rows = keys.map { keyId, e in
            SecurityStoreStoredKey(kind: keyId.kind, slot: keyId.slot, privateKey: e.privateKey, publicKey: e.publicKey)
        }.sorted { ($0.kind, $0.slot) < ($1.kind, $1.slot) }
        return try encode(rows)
    }

    public static func decodeKeys(_ data: Data) throws -> [SecurityStoreKeyId: SecurityStoreStoredKey] {
        guard !data.isEmpty else { return [:] }
        let rows = try PropertyListDecoder().decode([SecurityStoreStoredKey].self, from: data)
        return Dictionary(uniqueKeysWithValues: rows.map { (SecurityStoreKeyId(kind: $0.kind, slot: $0.slot), $0) })
    }

    public static func encodeLoginToken(_ token: SavedLogin, server: String, deviceId: String) throws -> Data {
        try encode(StoredLoginRecord(server: server, deviceId: deviceId, token: token.token, expiresMs: token.expiresMs))
    }

    public static func decodeLoginToken(_ data: Data, server: String, deviceId: String) -> SavedLogin? {
        guard !data.isEmpty,
              let record = try? PropertyListDecoder().decode(StoredLoginRecord.self, from: data),
              record.server == server,
              record.deviceId == deviceId,
              !record.token.isEmpty
        else { return nil }
        return SavedLogin(token: record.token, expiresMs: record.expiresMs)
    }

    private struct StoredLoginRecord: Codable {
        var server: String
        var deviceId: String
        var token: String
        var expiresMs: Int64
    }
}

private extension PropertyListEncoder {
    static let binary: PropertyListEncoder = {
        let enc = PropertyListEncoder()
        enc.outputFormat = .binary
        return enc
    }()
}

public protocol SecurityStoreV4Backend: AnyObject, Sendable {
    func readBuckets() throws -> [SecurityStoreBucketKey: SecurityStoreStoredBucket]
    func writeBuckets(_ buckets: [SecurityStoreBucketKey: SecurityStoreStoredBucket]) throws
    func readKeys() throws -> [SecurityStoreKeyId: SecurityStoreStoredKey]
    func writeKeys(_ keys: [SecurityStoreKeyId: SecurityStoreStoredKey]) throws
    func readNotify() throws -> Data?
    func writeNotify(_ data: Data) throws
    func deleteNotify() throws
    func readLoginTokenRaw() throws -> Data?
    func writeLoginTokenRaw(_ data: Data) throws
    func deleteLoginToken() throws
    func hasUpgradeNotice() throws -> Bool
    func setUpgradeNotice(_ on: Bool) throws
    func detectAndClearLegacy() throws -> Bool
    func clearAllBacking() throws
    func onAfterClearAll()
}

public extension SecurityStoreV4Backend {
    func onAfterClearAll() {}
}

/// Shared key/storage execution core parameterized over any locked backend adapter so Keychain
/// (`Vault`) and `MemorySecurityStoreV4` never duplicate CAS, key-promotion, or bootstrap logic.
public final class SecurityStoreV4Core: @unchecked Sendable {
    private let lock: NSRecursiveLock
    private let backend: any SecurityStoreV4Backend
    private var ephemeralKeys: [SecurityStoreKeyId: SecurityStoreStoredKey] = [:]

    public init(lock: NSRecursiveLock = NSRecursiveLock(), backend: any SecurityStoreV4Backend) {
        self.lock = lock
        self.backend = backend
    }

    public func hasPersistedState() -> Bool {
        lock.withLock {
            let buckets = (try? backend.readBuckets()) ?? [:]
            return buckets.values.contains { !$0.data.isEmpty }
        }
    }

    public func generateKey(kind: PBSecurityKeyKind, slot: String, persist: Bool) throws -> Data {
        let pair = try Self.generatePair(kind: kind, slot: slot)
        let id = SecurityStoreKeyId(kind: Int32(kind.rawValue), slot: slot)
        if !Self.isPersistent(kind: kind, persist: persist) {
            lock.withLock { ephemeralKeys[id] = pair }
            return pair.publicKey
        }
        try lock.withLock {
            ephemeralKeys.removeValue(forKey: id)
            var keys = try backend.readKeys()
            keys[id] = pair
            try backend.writeKeys(keys)
        }
        return pair.publicKey
    }

    public func signKey(kind: PBSecurityKeyKind, slot: String, message: Data) throws -> Data {
        let effective = kind == .unspecified ? .deviceEd : kind
        guard let row = try lock.withLock({ try loadKeyLocked(kind: effective, slot: slot) }) else {
            throw SecurityStoreError.missingSigningKey
        }
        let priv = try Curve25519.Signing.PrivateKey(rawRepresentation: row.privateKey)
        return try priv.signature(for: message)
    }

    public func agreeKey(kind: PBSecurityKeyKind, slot: String, peerPublic: Data) throws -> Data {
        guard peerPublic.count == keySize else { throw SecurityStoreError.invalidPeerPublicKey }
        let effective = kind == .unspecified ? .deviceX : kind
        guard let row = try lock.withLock({ try loadKeyLocked(kind: effective, slot: slot) }) else {
            throw SecurityStoreError.missingAgreementKey
        }
        let priv = try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: row.privateKey)
        let peer = try Curve25519.KeyAgreement.PublicKey(rawRepresentation: peerPublic)
        let shared = try priv.sharedSecretFromKeyAgreement(with: peer)
        return shared.withUnsafeBytes { Data($0) }
    }

    public func encapsulate(recipients: [PBSecurityEncapsulateRecipient]) throws -> [PBSecurityEncapsulatedPair] {
        try recipients.map { rec in
            guard rec.xPublic.count == keySize else { throw SecurityStoreError.invalidPeerPublicKey }
            let eph = Curve25519.KeyAgreement.PrivateKey()
            let peer = try Curve25519.KeyAgreement.PublicKey(rawRepresentation: rec.xPublic)
            let dh = try eph.sharedSecretFromKeyAgreement(with: peer)
            return PBSecurityEncapsulatedPair.with {
                $0.memberID = rec.memberID
                $0.enc = eph.publicKey.rawRepresentation
                $0.dh = dh.withUnsafeBytes { Data($0) }
            }
        }
    }

    public func discardKey(kind: PBSecurityKeyKind, slot: String) {
        let id = SecurityStoreKeyId(kind: Int32(kind.rawValue), slot: slot)
        lock.withLock {
            ephemeralKeys.removeValue(forKey: id)
            guard kind != .ephemeralX else { return }
            guard var keys = try? backend.readKeys(), keys.removeValue(forKey: id) != nil else { return }
            try? backend.writeKeys(keys)
        }
    }

    public func clearMemoryCache() {
        lock.withLock {
            ephemeralKeys.removeAll()
        }
    }

    public func dismissLegacyRepairNotice() {
        lock.withLock {
            try? backend.setUpgradeNotice(false)
        }
    }

    public func readNotifyBlob() -> Data? {
        lock.withLock { try? backend.readNotify() }
    }

    public func loadLoginToken(server: String, deviceId: String) -> SavedLogin? {
        lock.withLock {
            guard !server.isEmpty, !deviceId.isEmpty else { return nil }
            if let buckets = try? backend.readBuckets(), buckets.isEmpty {
                try? backend.deleteLoginToken()
                return nil
            }
            guard let raw = try? backend.readLoginTokenRaw() else { return nil }
            return SecurityStoreCodec.decodeLoginToken(raw, server: server, deviceId: deviceId)
        }
    }

    public func saveLoginToken(_ token: SavedLogin, server: String, deviceId: String) {
        lock.withLock {
            guard let data = try? SecurityStoreCodec.encodeLoginToken(token, server: server, deviceId: deviceId) else { return }
            try? backend.writeLoginTokenRaw(data)
        }
    }

    public func clearLoginToken(server: String, deviceId: String) {
        lock.withLock {
            guard let raw = try? backend.readLoginTokenRaw() else { return }
            if SecurityStoreCodec.decodeLoginToken(raw, server: server, deviceId: deviceId) != nil {
                try? backend.deleteLoginToken()
            }
        }
    }

    public func clearAll() throws {
        try lock.withLock {
            ephemeralKeys.removeAll()
            try backend.clearAllBacking()
            backend.onAfterClearAll()
        }
    }

    private static func isCasConflict(
        cur: SecurityStoreStoredBucket?,
        createOnly: Bool,
        expectedRevision: String,
        remove: Bool = false
    ) -> Bool {
        if remove {
            return !expectedRevision.isEmpty && cur != nil && cur?.revision != expectedRevision
        }
        return (createOnly && cur != nil) || (!expectedRevision.isEmpty && (cur == nil || cur?.revision != expectedRevision))
    }

    private func resolvePromoteKey(
        kind: PBSecurityKeyKind,
        slot: String,
        keys: [SecurityStoreKeyId: SecurityStoreStoredKey]
    ) -> (candId: SecurityStoreKeyId, key: SecurityStoreStoredKey)? {
        let candId = SecurityStoreKeyId(kind: Int32(kind.rawValue), slot: slot)
        return (ephemeralKeys[candId] ?? keys[candId]).map { (candId, $0) }
    }

    public func executeStorage(_ effect: PBSecurityStorageEffect) -> PBSecurityStorageResultEvent {
        lock.withLock {
            var out = PBSecurityStorageResultEvent()
            out.opID = effect.opID
            do {
                switch effect.op {
                case .clearAll:
                    ephemeralKeys.removeAll()
                    try backend.clearAllBacking()
                    backend.onAfterClearAll()
                    return out

                case .read:
                    let buckets = try backend.readBuckets()
                    let keys = try backend.readKeys()
                    if let row = buckets[SecurityStoreBucketKey(bucket: Int32(effect.bucket.rawValue), key: effect.key)] {
                        out.found = true; out.revision = row.revision; out.data = row.data
                    }
                    Self.populateIdentityPubs(&out, keys: keys)
                    return out

                case .readBootstrap:
                    var buckets = try backend.readBuckets()
                    var keys = try backend.readKeys()
                    let v4StateBuckets: Set<Int32> = [
                        Int32(PBSecurityStoreBucket.record.rawValue),
                        Int32(PBSecurityStoreBucket.operation.rawValue),
                        Int32(PBSecurityStoreBucket.registrationIntent.rawValue),
                    ]
                    let hasV4State = buckets.values.contains { !$0.data.isEmpty && v4StateBuckets.contains($0.bucket) }
                    let legacyCleared: Bool
                    if !hasV4State {
                        legacyCleared = try backend.detectAndClearLegacy()
                        if legacyCleared { buckets = try backend.readBuckets(); keys = try backend.readKeys() }
                    } else {
                        if try backend.hasUpgradeNotice() { try backend.setUpgradeNotice(false) }
                        legacyCleared = false
                    }
                    var entries = buckets.values.compactMap { row -> PBSecurityStorageEntry? in
                        let bEnum = PBSecurityStoreBucket(rawValue: Int(row.bucket)) ?? .unspecified
                        guard effect.bucket == .unspecified || bEnum == effect.bucket else { return nil }
                        return PBSecurityStorageEntry.with {
                            $0.bucket = bEnum; $0.key = row.key; $0.revision = row.revision; $0.data = row.data
                        }
                    }
                    entries.sort { ($0.bucket.rawValue, $0.key) < ($1.bucket.rawValue, $1.key) }
                    out.entries = entries
                    out.legacyCleared = legacyCleared
                    Self.populateIdentityPubs(&out, keys: keys)
                    return out

                case .writeCas:
                    var buckets = try backend.readBuckets()
                    let keys = try backend.readKeys()
                    let id = SecurityStoreBucketKey(bucket: Int32(effect.bucket.rawValue), key: effect.key)
                    if Self.isCasConflict(cur: buckets[id], createOnly: effect.createOnly, expectedRevision: effect.expectedRevision) {
                        out.conflict = true
                        Self.populateIdentityPubs(&out, keys: keys)
                        return out
                    }
                    buckets[id] = SecurityStoreStoredBucket(
                        bucket: Int32(effect.bucket.rawValue), key: effect.key, revision: effect.nextRevision, data: effect.data
                    )
                    try backend.writeBuckets(buckets)
                    if effect.bucket == .notifyRecord {
                        if effect.data.isEmpty { try backend.deleteNotify() } else { try backend.writeNotify(effect.data) }
                    }
                    if effect.bucket == .record, (try backend.hasUpgradeNotice()) { try backend.setUpgradeNotice(false) }
                    out.conflict = false; out.revision = effect.nextRevision
                    Self.populateIdentityPubs(&out, keys: keys)
                    return out

                case .deleteCas:
                    var buckets = try backend.readBuckets()
                    let keys = try backend.readKeys()
                    let id = SecurityStoreBucketKey(bucket: Int32(effect.bucket.rawValue), key: effect.key)
                    let conflict = Self.isCasConflict(cur: buckets[id], createOnly: false, expectedRevision: effect.expectedRevision, remove: true)
                    if !conflict, buckets.removeValue(forKey: id) != nil {
                        try backend.writeBuckets(buckets)
                        if effect.bucket == .notifyRecord { try backend.deleteNotify() }
                    }
                    out.conflict = conflict
                    Self.populateIdentityPubs(&out, keys: keys)
                    return out

                case .batchCommit:
                    var buckets = try backend.readBuckets()
                    var keys = try backend.readKeys()
                    if effect.batch.contains(where: {
                        Self.isCasConflict(
                            cur: buckets[SecurityStoreBucketKey(bucket: Int32($0.bucket.rawValue), key: $0.key)],
                            createOnly: $0.createOnly, expectedRevision: $0.expectedRevision, remove: $0.remove
                        )
                    }) {
                        out.conflict = true
                        Self.populateIdentityPubs(&out, keys: keys)
                        return out
                    }
                    var promotedEd: (candId: SecurityStoreKeyId, key: SecurityStoreStoredKey)?
                    if !effect.promoteEdSlot.isEmpty {
                        guard let ed = resolvePromoteKey(kind: .candidateEd, slot: effect.promoteEdSlot, keys: keys) else {
                            out.error = "missing promote ed slot"; Self.populateIdentityPubs(&out, keys: keys); return out
                        }
                        promotedEd = ed
                    }
                    var promotedX: (candId: SecurityStoreKeyId, key: SecurityStoreStoredKey)?
                    if !effect.promoteXSlot.isEmpty {
                        guard let x = resolvePromoteKey(kind: .candidateX, slot: effect.promoteXSlot, keys: keys) else {
                            out.error = "missing promote x slot"; Self.populateIdentityPubs(&out, keys: keys); return out
                        }
                        promotedX = x
                    }

                    // Phase 1: persist promoted DEVICE_* keys BEFORE removing candidate slots or committing buckets.
                    let originalKeys = keys
                    var promoteDirty = false
                    for (devKind, promoted) in [(PBSecurityKeyKind.deviceEd, promotedEd), (.deviceX, promotedX)] {
                        if let p = promoted {
                            let k = Int32(devKind.rawValue)
                            keys[SecurityStoreKeyId(kind: k, slot: "")] = SecurityStoreStoredKey(
                                kind: k, slot: "", privateKey: p.key.privateKey, publicKey: p.key.publicKey
                            )
                            promoteDirty = true
                        }
                    }
                    if promoteDirty { try backend.writeKeys(keys) }

                    // Phase 2: commit buckets (record, clearing pendingOp / regIntent); roll back promoted keys if buckets fail to write.
                    var bucketsDirty = false
                    var notifyUpdate: Data?
                    var notifyDeleted = false
                    var recordWritten = false
                    for w in effect.batch {
                        let id = SecurityStoreBucketKey(bucket: Int32(w.bucket.rawValue), key: w.key)
                        if w.remove {
                            if buckets.removeValue(forKey: id) != nil { bucketsDirty = true }
                            if w.bucket == .notifyRecord { notifyDeleted = true; notifyUpdate = nil }
                        } else {
                            buckets[id] = SecurityStoreStoredBucket(
                                bucket: Int32(w.bucket.rawValue), key: w.key, revision: w.nextRevision, data: w.data
                            )
                            bucketsDirty = true
                            if w.bucket == .record { recordWritten = true }
                            if w.bucket == .notifyRecord { notifyUpdate = w.data; notifyDeleted = false }
                        }
                    }
                    var bucketsCommitted = false
                    do {
                        if bucketsDirty {
                            try backend.writeBuckets(buckets)
                            bucketsCommitted = true
                        }
                        if let blob = notifyUpdate {
                            if blob.isEmpty { try backend.deleteNotify() } else { try backend.writeNotify(blob) }
                        } else if notifyDeleted {
                            try backend.deleteNotify()
                        }
                        if effect.clearLoginToken_p { try backend.deleteLoginToken() }
                        if recordWritten, (try backend.hasUpgradeNotice()) { try backend.setUpgradeNotice(false) }
                    } catch {
                        if promoteDirty && !bucketsCommitted {
                            try? backend.writeKeys(originalKeys)
                        }
                        throw error
                    }

                    // Phase 3: best-effort post-commit cleanup of promoted candidates and discarded slots (with one retry).
                    var cleanupDirty = false
                    for p in [promotedEd, promotedX].compactMap({ $0 }) {
                        ephemeralKeys.removeValue(forKey: p.candId)
                        if keys.removeValue(forKey: p.candId) != nil { cleanupDirty = true }
                    }
                    for slot in effect.discardSlots {
                        for kind in [PBSecurityKeyKind.candidateEd, .candidateX, .ephemeralX] {
                            let id = SecurityStoreKeyId(kind: Int32(kind.rawValue), slot: slot)
                            ephemeralKeys.removeValue(forKey: id)
                            if kind != .ephemeralX, keys.removeValue(forKey: id) != nil { cleanupDirty = true }
                        }
                    }
                    if cleanupDirty {
                        do { try backend.writeKeys(keys) } catch { try? backend.writeKeys(keys) }
                    }
                    out.conflict = false
                    Self.populateIdentityPubs(&out, keys: keys)
                    return out

                default:
                    out.error = "unsupported storage op"
                    return out
                }
            } catch {
                out.error = error.localizedDescription
                return out
            }
        }
    }

    private func loadKeyLocked(kind: PBSecurityKeyKind, slot: String) throws -> SecurityStoreStoredKey? {
        let id = SecurityStoreKeyId(kind: Int32(kind.rawValue), slot: slot)
        if kind == .ephemeralX {
            return ephemeralKeys[id]
        }
        if let eph = ephemeralKeys[id] {
            return eph
        }
        return try backend.readKeys()[id]
    }

    private static func isPersistent(kind: PBSecurityKeyKind, persist: Bool) -> Bool {
        switch kind {
        case .ephemeralX: return false
        case .deviceEd, .deviceX: return true
        default: return persist
        }
    }

    private static func generatePair(kind: PBSecurityKeyKind, slot: String) throws -> SecurityStoreStoredKey {
        let k = Int32(kind.rawValue)
        switch kind {
        case .deviceEd, .candidateEd:
            let priv = Curve25519.Signing.PrivateKey()
            return SecurityStoreStoredKey(kind: k, slot: slot, privateKey: priv.rawRepresentation, publicKey: priv.publicKey.rawRepresentation)
        case .deviceX, .candidateX, .ephemeralX:
            let priv = Curve25519.KeyAgreement.PrivateKey()
            return SecurityStoreStoredKey(kind: k, slot: slot, privateKey: priv.rawRepresentation, publicKey: priv.publicKey.rawRepresentation)
        default:
            throw SecurityStoreError.unsupportedKeyKind
        }
    }

    private static func populateIdentityPubs(_ out: inout PBSecurityStorageResultEvent, keys: [SecurityStoreKeyId: SecurityStoreStoredKey]) {
        if let ed = keys[SecurityStoreKeyId(kind: Int32(PBSecurityKeyKind.deviceEd.rawValue), slot: "")] {
            out.hasDeviceEd_p = true
            out.deviceEdPub = ed.publicKey
        } else {
            out.hasDeviceEd_p = false
        }
        if let x = keys[SecurityStoreKeyId(kind: Int32(PBSecurityKeyKind.deviceX.rawValue), slot: "")] {
            out.hasDeviceX_p = true
            out.deviceXPub = x.publicKey
        } else {
            out.hasDeviceX_p = false
        }
    }
#if DEBUG
    /// Test-only helper kept `internal` (accessible only via `@testable import RACore`) so
    /// production stores such as Keychain `Vault` do not expose a public record/key injection API.
    func seedTestRecord(
        edPrivate: Data,
        edPublic: Data,
        xPrivate: Data,
        xPublic: Data,
        recordBytes: Data,
        notifyBytes: Data? = nil
    ) throws {
        try lock.withLock {
            var keys = try backend.readKeys()
            var buckets = try backend.readBuckets()
            let edId = SecurityStoreKeyId(kind: Int32(PBSecurityKeyKind.deviceEd.rawValue), slot: "")
            let xId = SecurityStoreKeyId(kind: Int32(PBSecurityKeyKind.deviceX.rawValue), slot: "")
            keys[edId] = SecurityStoreStoredKey(kind: edId.kind, slot: "", privateKey: edPrivate, publicKey: edPublic)
            keys[xId] = SecurityStoreStoredKey(kind: xId.kind, slot: "", privateKey: xPrivate, publicKey: xPublic)
            let recBucket = SecurityStoreBucketKey(bucket: Int32(PBSecurityStoreBucket.record.rawValue), key: "record")
            buckets[recBucket] = SecurityStoreStoredBucket(bucket: recBucket.bucket, key: "record", revision: "test-1", data: recordBytes)
            if let notifyBytes {
                try backend.writeNotify(notifyBytes)
                let ntfBucket = SecurityStoreBucketKey(bucket: Int32(PBSecurityStoreBucket.notifyRecord.rawValue), key: "notify")
                buckets[ntfBucket] = SecurityStoreStoredBucket(bucket: ntfBucket.bucket, key: "notify", revision: "test-ntf-1", data: notifyBytes)
            }
            try backend.writeKeys(keys)
            try backend.writeBuckets(buckets)
        }
    }
#endif
}

#if DEBUG
extension SecurityStoreV4Proxy {
    func seedTestRecord(
        edPrivate: Data,
        edPublic: Data,
        xPrivate: Data,
        xPublic: Data,
        recordBytes: Data,
        notifyBytes: Data? = nil
    ) throws {
        try securityCore.seedTestRecord(
            edPrivate: edPrivate, edPublic: edPublic,
            xPrivate: xPrivate, xPublic: xPublic,
            recordBytes: recordBytes, notifyBytes: notifyBytes
        )
    }
}
#endif

/// Pure-memory `SecurityStoreV4` for SwiftPM unit tests and headless `Remote` instances.
public final class MemorySecurityStoreV4: SecurityStoreV4Proxy, SecurityStoreV4Backend, @unchecked Sendable {
    private let lock = NSRecursiveLock()
    private var buckets: [SecurityStoreBucketKey: SecurityStoreStoredBucket] = [:]
    private var keys: [SecurityStoreKeyId: SecurityStoreStoredKey] = [:]
    private var notifyData: Data?
    private var loginTokenData: Data?
    private var upgradeNotice = false
    private var legacyMarker = false
    public lazy var securityCore = SecurityStoreV4Core(lock: lock, backend: self)

    public init(seedLegacyMarker: Bool = false) { self.legacyMarker = seedLegacyMarker }

    public func readBuckets() -> [SecurityStoreBucketKey: SecurityStoreStoredBucket] { buckets }
    public func writeBuckets(_ next: [SecurityStoreBucketKey: SecurityStoreStoredBucket]) { buckets = next }
    public func readKeys() -> [SecurityStoreKeyId: SecurityStoreStoredKey] { keys }
    public func writeKeys(_ next: [SecurityStoreKeyId: SecurityStoreStoredKey]) { keys = next }
    public func readNotify() -> Data? { notifyData }
    public func writeNotify(_ data: Data) { notifyData = data }
    public func deleteNotify() { notifyData = nil }
    public func readLoginTokenRaw() -> Data? { loginTokenData }
    public func writeLoginTokenRaw(_ data: Data) { loginTokenData = data }
    public func deleteLoginToken() { loginTokenData = nil }
    public func hasUpgradeNotice() -> Bool { lock.withLock { upgradeNotice } }
    public func setUpgradeNotice(_ on: Bool) { upgradeNotice = on }
    public func detectAndClearLegacy() -> Bool {
        guard legacyMarker else { return upgradeNotice }
        legacyMarker = false; buckets.removeAll(); keys.removeAll()
        notifyData = nil; loginTokenData = nil; upgradeNotice = true
        return true
    }
    public func clearAllBacking() {
        legacyMarker = false; buckets.removeAll(); keys.removeAll()
        notifyData = nil; loginTokenData = nil; upgradeNotice = false
    }

    public func seedLegacyStateForTesting() {
        lock.withLock { legacyMarker = true }
    }
}
