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
    public static func encodeBuckets(_ buckets: [SecurityStoreBucketKey: SecurityStoreStoredBucket]) throws -> Data {
        let rows = buckets.values.sorted {
            if $0.bucket != $1.bucket { return $0.bucket < $1.bucket }
            return $0.key < $1.key
        }
        return try PropertyListEncoder.binary.encode(rows)
    }

    public static func decodeBuckets(_ data: Data) throws -> [SecurityStoreBucketKey: SecurityStoreStoredBucket] {
        guard !data.isEmpty else { return [:] }
        let rows = try PropertyListDecoder().decode([SecurityStoreStoredBucket].self, from: data)
        var out: [SecurityStoreBucketKey: SecurityStoreStoredBucket] = [:]
        out.reserveCapacity(rows.count)
        for row in rows {
            out[SecurityStoreBucketKey(bucket: row.bucket, key: row.key)] = row
        }
        return out
    }

    public static func encodeKeys(_ keys: [SecurityStoreKeyId: SecurityStoreStoredKey]) throws -> Data {
        let rows = keys.map { keyId, entry in
            SecurityStoreStoredKey(kind: keyId.kind, slot: keyId.slot, privateKey: entry.privateKey, publicKey: entry.publicKey)
        }.sorted {
            if $0.kind != $1.kind { return $0.kind < $1.kind }
            return $0.slot < $1.slot
        }
        return try PropertyListEncoder.binary.encode(rows)
    }

    public static func decodeKeys(_ data: Data) throws -> [SecurityStoreKeyId: SecurityStoreStoredKey] {
        guard !data.isEmpty else { return [:] }
        let rows = try PropertyListDecoder().decode([SecurityStoreStoredKey].self, from: data)
        var out: [SecurityStoreKeyId: SecurityStoreStoredKey] = [:]
        out.reserveCapacity(rows.count)
        for row in rows {
            out[SecurityStoreKeyId(kind: row.kind, slot: row.slot)] = row
        }
        return out
    }

    public static func encodeLoginToken(_ token: SavedLogin, server: String, deviceId: String) throws -> Data {
        let record = StoredLoginRecord(server: server, deviceId: deviceId, token: token.token, expiresMs: token.expiresMs)
        return try PropertyListEncoder.binary.encode(record)
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

/// Shared key/storage execution core parameterized over any locked backend adapter so Keychain
/// (`Vault`) and `MemorySecurityStoreV4` never duplicate CAS, key-promotion, or bootstrap logic.
public final class SecurityStoreV4Core: @unchecked Sendable {
    public struct Backend {
        public var readBuckets: () throws -> [SecurityStoreBucketKey: SecurityStoreStoredBucket]
        public var writeBuckets: ([SecurityStoreBucketKey: SecurityStoreStoredBucket]) throws -> Void
        public var readKeys: () throws -> [SecurityStoreKeyId: SecurityStoreStoredKey]
        public var writeKeys: ([SecurityStoreKeyId: SecurityStoreStoredKey]) throws -> Void
        public var readNotify: () throws -> Data?
        public var writeNotify: (Data) throws -> Void
        public var deleteNotify: () throws -> Void
        public var readLoginTokenRaw: () throws -> Data?
        public var writeLoginTokenRaw: (Data) throws -> Void
        public var deleteLoginToken: () throws -> Void
        public var hasUpgradeNotice: () throws -> Bool
        public var setUpgradeNotice: (Bool) throws -> Void
        public var detectAndClearLegacy: () throws -> Bool
        public var clearAllBacking: () throws -> Void
        public var onAfterClearAll: () -> Void

        public init(
            readBuckets: @escaping () throws -> [SecurityStoreBucketKey: SecurityStoreStoredBucket],
            writeBuckets: @escaping ([SecurityStoreBucketKey: SecurityStoreStoredBucket]) throws -> Void,
            readKeys: @escaping () throws -> [SecurityStoreKeyId: SecurityStoreStoredKey],
            writeKeys: @escaping ([SecurityStoreKeyId: SecurityStoreStoredKey]) throws -> Void,
            readNotify: @escaping () throws -> Data?,
            writeNotify: @escaping (Data) throws -> Void,
            deleteNotify: @escaping () throws -> Void,
            readLoginTokenRaw: @escaping () throws -> Data?,
            writeLoginTokenRaw: @escaping (Data) throws -> Void,
            deleteLoginToken: @escaping () throws -> Void,
            hasUpgradeNotice: @escaping () throws -> Bool,
            setUpgradeNotice: @escaping (Bool) throws -> Void,
            detectAndClearLegacy: @escaping () throws -> Bool,
            clearAllBacking: @escaping () throws -> Void,
            onAfterClearAll: @escaping () -> Void = {}
        ) {
            self.readBuckets = readBuckets
            self.writeBuckets = writeBuckets
            self.readKeys = readKeys
            self.writeKeys = writeKeys
            self.readNotify = readNotify
            self.writeNotify = writeNotify
            self.deleteNotify = deleteNotify
            self.readLoginTokenRaw = readLoginTokenRaw
            self.writeLoginTokenRaw = writeLoginTokenRaw
            self.deleteLoginToken = deleteLoginToken
            self.hasUpgradeNotice = hasUpgradeNotice
            self.setUpgradeNotice = setUpgradeNotice
            self.detectAndClearLegacy = detectAndClearLegacy
            self.clearAllBacking = clearAllBacking
            self.onAfterClearAll = onAfterClearAll
        }
    }

    private let lock: NSRecursiveLock
    private let backend: Backend
    private var ephemeralKeys: [SecurityStoreKeyId: SecurityStoreStoredKey] = [:]

    public init(lock: NSRecursiveLock = NSRecursiveLock(), backend: Backend) {
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
                        out.found = true
                        out.revision = row.revision
                        out.data = row.data
                    }
                    Self.populateIdentityPubs(&out, keys: keys)
                    return out

                case .readBootstrap:
                    var buckets = try backend.readBuckets()
                    var keys = try backend.readKeys()
                    let hasV4State = buckets.values.contains { row in
                        !row.data.isEmpty && (
                            row.bucket == Int32(PBSecurityStoreBucket.record.rawValue)
                            || row.bucket == Int32(PBSecurityStoreBucket.operation.rawValue)
                            || row.bucket == Int32(PBSecurityStoreBucket.registrationIntent.rawValue)
                        )
                    }
                    let legacyCleared: Bool
                    if !hasV4State {
                        legacyCleared = try backend.detectAndClearLegacy()
                        if legacyCleared {
                            buckets = try backend.readBuckets()
                            keys = try backend.readKeys()
                        }
                    } else {
                        if try backend.hasUpgradeNotice() {
                            try backend.setUpgradeNotice(false)
                        }
                        legacyCleared = false
                    }
                    var entries: [PBSecurityStorageEntry] = []
                    for row in buckets.values {
                        let bEnum = PBSecurityStoreBucket(rawValue: Int(row.bucket)) ?? .unspecified
                        if effect.bucket != .unspecified && bEnum != effect.bucket {
                            continue
                        }
                        entries.append(PBSecurityStorageEntry.with {
                            $0.bucket = bEnum
                            $0.key = row.key
                            $0.revision = row.revision
                            $0.data = row.data
                        })
                    }
                    entries.sort {
                        if $0.bucket.rawValue != $1.bucket.rawValue { return $0.bucket.rawValue < $1.bucket.rawValue }
                        return $0.key < $1.key
                    }
                    out.entries = entries
                    out.legacyCleared = legacyCleared
                    Self.populateIdentityPubs(&out, keys: keys)
                    return out

                case .writeCas:
                    var buckets = try backend.readBuckets()
                    let keys = try backend.readKeys()
                    let id = SecurityStoreBucketKey(bucket: Int32(effect.bucket.rawValue), key: effect.key)
                    let cur = buckets[id]
                    let conflict = (effect.createOnly && cur != nil)
                        || (!effect.expectedRevision.isEmpty && (cur == nil || cur?.revision != effect.expectedRevision))
                    if conflict {
                        out.conflict = true
                        Self.populateIdentityPubs(&out, keys: keys)
                        return out
                    }
                    buckets[id] = SecurityStoreStoredBucket(
                        bucket: Int32(effect.bucket.rawValue),
                        key: effect.key,
                        revision: effect.nextRevision,
                        data: effect.data
                    )
                    try backend.writeBuckets(buckets)
                    if effect.bucket == .notifyRecord {
                        if effect.data.isEmpty {
                            try backend.deleteNotify()
                        } else {
                            try backend.writeNotify(effect.data)
                        }
                    }
                    if effect.bucket == .record, (try backend.hasUpgradeNotice()) {
                        try backend.setUpgradeNotice(false)
                    }
                    out.conflict = false
                    out.revision = effect.nextRevision
                    Self.populateIdentityPubs(&out, keys: keys)
                    return out

                case .deleteCas:
                    var buckets = try backend.readBuckets()
                    let keys = try backend.readKeys()
                    let id = SecurityStoreBucketKey(bucket: Int32(effect.bucket.rawValue), key: effect.key)
                    let cur = buckets[id]
                    let conflict = !effect.expectedRevision.isEmpty && cur != nil && cur?.revision != effect.expectedRevision
                    if !conflict, buckets.removeValue(forKey: id) != nil {
                        try backend.writeBuckets(buckets)
                        if effect.bucket == .notifyRecord {
                            try backend.deleteNotify()
                        }
                    }
                    out.conflict = conflict
                    Self.populateIdentityPubs(&out, keys: keys)
                    return out

                case .batchCommit:
                    var buckets = try backend.readBuckets()
                    var keys = try backend.readKeys()
                    for w in effect.batch {
                        let cur = buckets[SecurityStoreBucketKey(bucket: Int32(w.bucket.rawValue), key: w.key)]
                        let mismatch: Bool
                        if w.remove {
                            mismatch = !w.expectedRevision.isEmpty && cur != nil && cur?.revision != w.expectedRevision
                        } else {
                            mismatch = (w.createOnly && cur != nil)
                                || (!w.expectedRevision.isEmpty && (cur == nil || cur?.revision != w.expectedRevision))
                        }
                        if mismatch {
                            out.conflict = true
                            Self.populateIdentityPubs(&out, keys: keys)
                            return out
                        }
                    }
                    var promotedEd: SecurityStoreStoredKey?
                    var promotedX: SecurityStoreStoredKey?
                    if !effect.promoteEdSlot.isEmpty {
                        let candId = SecurityStoreKeyId(kind: Int32(PBSecurityKeyKind.candidateEd.rawValue), slot: effect.promoteEdSlot)
                        promotedEd = ephemeralKeys[candId] ?? keys[candId]
                        if promotedEd == nil {
                            out.error = "missing promote ed slot"
                            Self.populateIdentityPubs(&out, keys: keys)
                            return out
                        }
                    }
                    if !effect.promoteXSlot.isEmpty {
                        let candId = SecurityStoreKeyId(kind: Int32(PBSecurityKeyKind.candidateX.rawValue), slot: effect.promoteXSlot)
                        promotedX = ephemeralKeys[candId] ?? keys[candId]
                        if promotedX == nil {
                            out.error = "missing promote x slot"
                            Self.populateIdentityPubs(&out, keys: keys)
                            return out
                        }
                    }
                    var keysDirty = false
                    var bucketsDirty = false
                    var notifyUpdate: Data?
                    var notifyDeleted = false
                    if let ed = promotedEd {
                        let candId = SecurityStoreKeyId(kind: Int32(PBSecurityKeyKind.candidateEd.rawValue), slot: effect.promoteEdSlot)
                        keys[SecurityStoreKeyId(kind: Int32(PBSecurityKeyKind.deviceEd.rawValue), slot: "")] = SecurityStoreStoredKey(
                            kind: Int32(PBSecurityKeyKind.deviceEd.rawValue),
                            slot: "",
                            privateKey: ed.privateKey,
                            publicKey: ed.publicKey
                        )
                        keys.removeValue(forKey: candId)
                        ephemeralKeys.removeValue(forKey: candId)
                        keysDirty = true
                    }
                    if let x = promotedX {
                        let candId = SecurityStoreKeyId(kind: Int32(PBSecurityKeyKind.candidateX.rawValue), slot: effect.promoteXSlot)
                        keys[SecurityStoreKeyId(kind: Int32(PBSecurityKeyKind.deviceX.rawValue), slot: "")] = SecurityStoreStoredKey(
                            kind: Int32(PBSecurityKeyKind.deviceX.rawValue),
                            slot: "",
                            privateKey: x.privateKey,
                            publicKey: x.publicKey
                        )
                        keys.removeValue(forKey: candId)
                        ephemeralKeys.removeValue(forKey: candId)
                        keysDirty = true
                    }
                    for slot in effect.discardSlots {
                        for kind in [PBSecurityKeyKind.candidateEd, .candidateX, .ephemeralX] {
                            let id = SecurityStoreKeyId(kind: Int32(kind.rawValue), slot: slot)
                            ephemeralKeys.removeValue(forKey: id)
                            if kind != .ephemeralX, keys.removeValue(forKey: id) != nil {
                                keysDirty = true
                            }
                        }
                    }
                    var recordWritten = false
                    for w in effect.batch {
                        let id = SecurityStoreBucketKey(bucket: Int32(w.bucket.rawValue), key: w.key)
                        if w.remove {
                            if buckets.removeValue(forKey: id) != nil { bucketsDirty = true }
                            if w.bucket == .notifyRecord {
                                notifyDeleted = true
                                notifyUpdate = nil
                            }
                        } else {
                            buckets[id] = SecurityStoreStoredBucket(
                                bucket: Int32(w.bucket.rawValue),
                                key: w.key,
                                revision: w.nextRevision,
                                data: w.data
                            )
                            bucketsDirty = true
                            if w.bucket == .record { recordWritten = true }
                            if w.bucket == .notifyRecord {
                                notifyUpdate = w.data
                                notifyDeleted = false
                            }
                        }
                    }
                    if keysDirty { try backend.writeKeys(keys) }
                    if bucketsDirty { try backend.writeBuckets(buckets) }
                    if let blob = notifyUpdate {
                        if blob.isEmpty { try backend.deleteNotify() } else { try backend.writeNotify(blob) }
                    } else if notifyDeleted {
                        try backend.deleteNotify()
                    }
                    if effect.clearLoginToken_p {
                        try backend.deleteLoginToken()
                    }
                    if recordWritten, (try backend.hasUpgradeNotice()) {
                        try backend.setUpgradeNotice(false)
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
}

/// Pure-memory `SecurityStoreV4` for SwiftPM unit tests and headless `Remote` instances.
public final class MemorySecurityStoreV4: SecurityStoreV4, @unchecked Sendable {
    private let lock = NSRecursiveLock()
    private var buckets: [SecurityStoreBucketKey: SecurityStoreStoredBucket] = [:]
    private var keys: [SecurityStoreKeyId: SecurityStoreStoredKey] = [:]
    private var notifyData: Data?
    private var loginTokenData: Data?
    private var upgradeNotice = false
    private var legacyMarker = false
    private lazy var core: SecurityStoreV4Core = SecurityStoreV4Core(
        lock: lock,
        backend: SecurityStoreV4Core.Backend(
            readBuckets: { self.buckets },
            writeBuckets: { self.buckets = $0 },
            readKeys: { self.keys },
            writeKeys: { self.keys = $0 },
            readNotify: { self.notifyData },
            writeNotify: { self.notifyData = $0 },
            deleteNotify: { self.notifyData = nil },
            readLoginTokenRaw: { self.loginTokenData },
            writeLoginTokenRaw: { self.loginTokenData = $0 },
            deleteLoginToken: { self.loginTokenData = nil },
            hasUpgradeNotice: { self.upgradeNotice },
            setUpgradeNotice: { self.upgradeNotice = $0 },
            detectAndClearLegacy: {
                guard self.legacyMarker else { return self.upgradeNotice }
                self.legacyMarker = false
                self.buckets.removeAll()
                self.keys.removeAll()
                self.notifyData = nil
                self.loginTokenData = nil
                self.upgradeNotice = true
                return true
            },
            clearAllBacking: {
                self.legacyMarker = false
                self.buckets.removeAll()
                self.keys.removeAll()
                self.notifyData = nil
                self.loginTokenData = nil
                self.upgradeNotice = false
            }
        )
    )

    public init(seedLegacyMarker: Bool = false) {
        self.legacyMarker = seedLegacyMarker
    }

    public func seedLegacyStateForTesting() {
        lock.withLock { legacyMarker = true }
    }

    public func seedTestRecord(
        edPrivate: Data,
        edPublic: Data,
        xPrivate: Data,
        xPublic: Data,
        recordBytes: Data,
        notifyBytes: Data? = nil
    ) {
        lock.withLock {
            let edId = SecurityStoreKeyId(kind: Int32(PBSecurityKeyKind.deviceEd.rawValue), slot: "")
            let xId = SecurityStoreKeyId(kind: Int32(PBSecurityKeyKind.deviceX.rawValue), slot: "")
            keys[edId] = SecurityStoreStoredKey(kind: edId.kind, slot: "", privateKey: edPrivate, publicKey: edPublic)
            keys[xId] = SecurityStoreStoredKey(kind: xId.kind, slot: "", privateKey: xPrivate, publicKey: xPublic)
            let recBucket = SecurityStoreBucketKey(bucket: Int32(PBSecurityStoreBucket.record.rawValue), key: "record")
            buckets[recBucket] = SecurityStoreStoredBucket(bucket: recBucket.bucket, key: "record", revision: "test-1", data: recordBytes)
            if let notifyBytes {
                notifyData = notifyBytes
                let ntfBucket = SecurityStoreBucketKey(bucket: Int32(PBSecurityStoreBucket.notifyRecord.rawValue), key: "notify")
                buckets[ntfBucket] = SecurityStoreStoredBucket(bucket: ntfBucket.bucket, key: "notify", revision: "test-ntf-1", data: notifyBytes)
            }
        }
    }

    public func readNotifyBlob() -> Data? { core.readNotifyBlob() }
    public func hasUpgradeNotice() -> Bool { lock.withLock { upgradeNotice } }

    public func hasPersistedState() -> Bool { core.hasPersistedState() }
    public func generateKey(kind: PBSecurityKeyKind, slot: String, persist: Bool) throws -> Data { try core.generateKey(kind: kind, slot: slot, persist: persist) }
    public func signKey(kind: PBSecurityKeyKind, slot: String, message: Data) throws -> Data { try core.signKey(kind: kind, slot: slot, message: message) }
    public func agreeKey(kind: PBSecurityKeyKind, slot: String, peerPublic: Data) throws -> Data { try core.agreeKey(kind: kind, slot: slot, peerPublic: peerPublic) }
    public func encapsulate(recipients: [PBSecurityEncapsulateRecipient]) throws -> [PBSecurityEncapsulatedPair] { try core.encapsulate(recipients: recipients) }
    public func discardKey(kind: PBSecurityKeyKind, slot: String) { core.discardKey(kind: kind, slot: slot) }
    public func executeStorage(_ effect: PBSecurityStorageEffect) -> PBSecurityStorageResultEvent { core.executeStorage(effect) }
    public func dismissLegacyRepairNotice() { core.dismissLegacyRepairNotice() }
    public func clearMemoryCache() { core.clearMemoryCache() }
    public func loadLoginToken(server: String, deviceId: String) -> SavedLogin? { core.loadLoginToken(server: server, deviceId: deviceId) }
    public func saveLoginToken(_ token: SavedLogin, server: String, deviceId: String) { core.saveLoginToken(token, server: server, deviceId: deviceId) }
    public func clearLoginToken(server: String, deviceId: String) { core.clearLoginToken(server: server, deviceId: deviceId) }
}
