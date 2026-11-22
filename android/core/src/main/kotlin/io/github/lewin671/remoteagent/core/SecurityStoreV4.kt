package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.proto.SecurityEncapsulateRecipient
import io.github.lewin671.remoteagent.core.proto.SecurityEncapsulatedPair
import io.github.lewin671.remoteagent.core.proto.SecurityKeyKind
import io.github.lewin671.remoteagent.core.proto.SecurityStorageEffect
import io.github.lewin671.remoteagent.core.proto.SecurityStorageEntry
import io.github.lewin671.remoteagent.core.proto.SecurityStorageOp
import io.github.lewin671.remoteagent.core.proto.SecurityStorageResultEvent
import io.github.lewin671.remoteagent.core.proto.SecurityStoreBucket
import io.github.lewin671.remoteagent.corebind.Corebind

/**
 * S6 v4 security storage and private-key boundary on Android.
 *
 * Implementations hold all long-lived Ed25519 / X25519 private keys in host storage
 * (Android Keystore-backed encrypted files in :app, or [MemorySecurityStoreV4] in JVM tests)
 * and expose only CAS bucket reads/writes, key-handle ops, and encapsulated key exchange to Core.
 */
interface SecurityStoreV4 {
    fun generateKey(kind: SecurityKeyKind, slot: String, persist: Boolean): ByteArray

    fun signKey(kind: SecurityKeyKind, slot: String, message: ByteArray): ByteArray

    fun agreeKey(kind: SecurityKeyKind, slot: String, peerPublic: ByteArray): ByteArray

    fun encapsulate(recipients: List<SecurityEncapsulateRecipient>): List<SecurityEncapsulatedPair>

    fun discardKey(kind: SecurityKeyKind, slot: String)

    fun executeStorage(effect: SecurityStorageEffect): SecurityStorageResultEvent

    fun loadLoginToken(server: String, deviceId: String): LoginToken?

    fun saveLoginToken(server: String, deviceId: String, token: LoginToken)

    fun clearLoginToken(server: String, deviceId: String)

    fun readNotifyBlob(): ByteArray?

    fun dismissUpgradeNotice()

    fun clearMemoryCache() {}
}

data class SecurityStoreKeyId(val kindNum: Int, val slot: String)

data class SecurityStoreBucketId(val bucketNum: Int, val key: String)

data class SecurityStoreStoredKey(val privateKey: ByteArray, val publicKey: ByteArray)

data class SecurityStoreStoredBucket(
    val bucketNum: Int,
    val key: String,
    val revision: String,
    val data: ByteArray,
)

interface SecurityStoreV4Backend {
    fun <T> transaction(block: () -> T): T
    fun readBuckets(): Map<SecurityStoreBucketId, SecurityStoreStoredBucket>
    fun writeBuckets(buckets: Map<SecurityStoreBucketId, SecurityStoreStoredBucket>)
    fun readKeys(): Map<SecurityStoreKeyId, SecurityStoreStoredKey>
    fun writeKeys(keys: Map<SecurityStoreKeyId, SecurityStoreStoredKey>)
    fun readNotifyBlob(): ByteArray?
    fun writeNotifyBlob(blob: ByteArray)
    fun deleteNotifyBlob()
    fun deleteLoginToken()
    fun clearUpgradeNotice()
    fun detectAndClearLegacy(): Boolean
    fun clearAll()
    fun onBatchCommitResult(hasError: Boolean) {}
}

/**
 * Shared key and CAS bucket storage engine parameterized over [SecurityStoreV4Backend] so
 * Android Keystore [Vault] and JVM [MemorySecurityStoreV4] share one crash-safe implementation.
 */
class SecurityStoreV4Core(
    private val backend: SecurityStoreV4Backend,
    private val onLoadLoginToken: (String, String) -> LoginToken? = { _, _ -> null },
    private val onSaveLoginToken: (String, String, LoginToken) -> Unit = { _, _, _ -> },
    private val onClearLoginToken: (String, String) -> Unit = { _, _ -> },
    private val onClearCache: () -> Unit = {},
) : SecurityStoreV4 {
    private val ephemeralKeys = java.util.concurrent.ConcurrentHashMap<SecurityStoreKeyId, SecurityStoreStoredKey>()

    fun clearEphemeralKeys() {
        ephemeralKeys.clear()
    }

    private fun isPersistent(kind: SecurityKeyKind, persist: Boolean): Boolean =
        when (kind) {
            SecurityKeyKind.SECURITY_KEY_KIND_EPHEMERAL_X -> false
            SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_ED,
            SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_X -> true
            else -> persist
        }

    private fun generateRawEd25519(): SecurityStoreStoredKey {
        while (true) {
            val seed = randomBytes(KEY_SIZE)
            val pub = runCatching { Corebind.ed25519PublicKey(seed) }.getOrNull() ?: continue
            return SecurityStoreStoredKey(seed, pub)
        }
    }

    private fun generateRawX25519(): SecurityStoreStoredKey {
        while (true) {
            val priv = randomBytes(KEY_SIZE)
            val pub = runCatching { Corebind.x25519PublicKey(priv) }.getOrNull() ?: continue
            return SecurityStoreStoredKey(priv, pub)
        }
    }

    private fun generatePairForKind(kind: SecurityKeyKind): SecurityStoreStoredKey = when (kind) {
        SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_ED,
        SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_ED -> generateRawEd25519()
        SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_X,
        SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_X,
        SecurityKeyKind.SECURITY_KEY_KIND_EPHEMERAL_X -> generateRawX25519()
        else -> throw IllegalArgumentException("unsupported key kind: $kind")
    }

    override fun generateKey(kind: SecurityKeyKind, slot: String, persist: Boolean): ByteArray {
        val pair = generatePairForKind(kind)
        val id = SecurityStoreKeyId(kind.number, slot)
        if (!isPersistent(kind, persist)) {
            ephemeralKeys[id] = pair
            return pair.publicKey.copyOf()
        }
        backend.transaction {
            ephemeralKeys.remove(id)
            val map = LinkedHashMap(backend.readKeys())
            map[id] = pair
            backend.writeKeys(map)
        }
        return pair.publicKey.copyOf()
    }

    private fun loadKeyLocked(kind: SecurityKeyKind, slot: String): SecurityStoreStoredKey? {
        val id = SecurityStoreKeyId(kind.number, slot)
        if (kind == SecurityKeyKind.SECURITY_KEY_KIND_EPHEMERAL_X) {
            return ephemeralKeys[id]
        }
        return ephemeralKeys[id] ?: backend.readKeys()[id]
    }

    override fun signKey(kind: SecurityKeyKind, slot: String, message: ByteArray): ByteArray {
        val row = backend.transaction { loadKeyLocked(kind, slot) }
            ?: throw IllegalStateException("missing signing key")
        return Corebind.ed25519Sign(row.privateKey, message)
    }

    override fun agreeKey(kind: SecurityKeyKind, slot: String, peerPublic: ByteArray): ByteArray {
        require(peerPublic.size == KEY_SIZE) { "invalid peer public key" }
        val row = backend.transaction { loadKeyLocked(kind, slot) }
            ?: throw IllegalStateException("missing agreement key")
        return Corebind.x25519Shared(row.privateKey, peerPublic)
    }

    override fun encapsulate(recipients: List<SecurityEncapsulateRecipient>): List<SecurityEncapsulatedPair> =
        recipients.map { r ->
            val peerPub = r.xPublic.toByteArray()
            require(peerPub.size == KEY_SIZE) { "invalid recipient x25519 public key" }
            val eph = generateRawX25519()
            val dh = Corebind.x25519Shared(eph.privateKey, peerPub)
            SecurityEncapsulatedPair.newBuilder()
                .setMemberId(r.memberId)
                .setEnc(ByteString.copyFrom(eph.publicKey))
                .setDh(ByteString.copyFrom(dh))
                .build()
        }

    override fun discardKey(kind: SecurityKeyKind, slot: String) {
        val id = SecurityStoreKeyId(kind.number, slot)
        ephemeralKeys.remove(id)
        if (kind == SecurityKeyKind.SECURITY_KEY_KIND_EPHEMERAL_X) return
        backend.transaction {
            val map = LinkedHashMap(backend.readKeys())
            if (map.remove(id) != null) {
                backend.writeKeys(map)
            }
        }
    }

    private fun SecurityStorageResultEvent.Builder.populateIdentityPubs(
        keys: Map<SecurityStoreKeyId, SecurityStoreStoredKey>,
    ): SecurityStorageResultEvent.Builder {
        val ed = keys[SecurityStoreKeyId(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_ED.number, "")]
        val x = keys[SecurityStoreKeyId(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_X.number, "")]
        setHasDeviceEd(ed != null)
        if (ed != null) setDeviceEdPub(ByteString.copyFrom(ed.publicKey))
        setHasDeviceX(x != null)
        if (x != null) setDeviceXPub(ByteString.copyFrom(x.publicKey))
        return this
    }

    override fun executeStorage(effect: SecurityStorageEffect): SecurityStorageResultEvent =
        try {
            backend.transaction {
                val out = SecurityStorageResultEvent.newBuilder().setOpId(effect.opId)
                when (effect.op) {
                SecurityStorageOp.SECURITY_STORAGE_OP_CLEAR_ALL -> {
                    ephemeralKeys.clear()
                    backend.clearAll()
                    out.build()
                }
                SecurityStorageOp.SECURITY_STORAGE_OP_READ -> {
                    val buckets = backend.readBuckets()
                    val keys = backend.readKeys()
                    val row = buckets[SecurityStoreBucketId(effect.bucket.number, effect.key)]
                    if (row != null) {
                        out.setFound(true)
                            .setRevision(row.revision)
                            .setData(ByteString.copyFrom(row.data))
                    }
                    out.populateIdentityPubs(keys).build()
                }
                SecurityStorageOp.SECURITY_STORAGE_OP_READ_BOOTSTRAP -> {
                    val buckets = backend.readBuckets()
                    val keys = backend.readKeys()
                    val hasV4State = buckets.values.any { row ->
                        row.data.isNotEmpty() && row.bucketNum in setOf(
                            SecurityStoreBucket.SECURITY_STORE_BUCKET_RECORD.number,
                            SecurityStoreBucket.SECURITY_STORE_BUCKET_OPERATION.number,
                            SecurityStoreBucket.SECURITY_STORE_BUCKET_REGISTRATION_INTENT.number,
                        )
                    }
                    val legacyCleared = if (!hasV4State) {
                        backend.detectAndClearLegacy()
                    } else {
                        backend.clearUpgradeNotice()
                        false
                    }
                    for (row in buckets.values) {
                        val bEnum = SecurityStoreBucket.forNumber(row.bucketNum)
                            ?: SecurityStoreBucket.SECURITY_STORE_BUCKET_UNSPECIFIED
                        if (effect.bucket != SecurityStoreBucket.SECURITY_STORE_BUCKET_UNSPECIFIED &&
                            bEnum != effect.bucket
                        ) {
                            continue
                        }
                        out.addEntries(
                            SecurityStorageEntry.newBuilder()
                                .setBucket(bEnum)
                                .setKey(row.key)
                                .setRevision(row.revision)
                                .setData(ByteString.copyFrom(row.data))
                                .build(),
                        )
                    }
                    out.setLegacyCleared(legacyCleared)
                    out.populateIdentityPubs(keys).build()
                }
                SecurityStorageOp.SECURITY_STORAGE_OP_WRITE_CAS -> {
                    val buckets = LinkedHashMap(backend.readBuckets())
                    val keys = backend.readKeys()
                    val id = SecurityStoreBucketId(effect.bucket.number, effect.key)
                    val cur = buckets[id]
                    val conflict =
                        (effect.createOnly && cur != null) ||
                            (effect.expectedRevision.isNotEmpty() && (cur == null || cur.revision != effect.expectedRevision))
                    if (conflict) {
                        out.setConflict(true).populateIdentityPubs(keys).build()
                    } else {
                        val dataBytes = effect.data.toByteArray()
                        buckets[id] = SecurityStoreStoredBucket(effect.bucket.number, effect.key, effect.nextRevision, dataBytes)
                        backend.writeBuckets(buckets)
                        if (effect.bucket == SecurityStoreBucket.SECURITY_STORE_BUCKET_NOTIFY_RECORD) {
                            backend.writeNotifyBlob(dataBytes)
                        }
                        if (effect.bucket == SecurityStoreBucket.SECURITY_STORE_BUCKET_RECORD) {
                            backend.clearUpgradeNotice()
                        }
                        out.setConflict(false)
                            .setRevision(effect.nextRevision)
                            .populateIdentityPubs(keys)
                            .build()
                    }
                }
                SecurityStorageOp.SECURITY_STORAGE_OP_DELETE_CAS -> {
                    val buckets = LinkedHashMap(backend.readBuckets())
                    val keys = backend.readKeys()
                    val id = SecurityStoreBucketId(effect.bucket.number, effect.key)
                    val cur = buckets[id]
                    val conflict =
                        effect.expectedRevision.isNotEmpty() && cur != null && cur.revision != effect.expectedRevision
                    if (!conflict && buckets.remove(id) != null) {
                        backend.writeBuckets(buckets)
                        if (effect.bucket == SecurityStoreBucket.SECURITY_STORE_BUCKET_NOTIFY_RECORD) {
                            backend.deleteNotifyBlob()
                        }
                    }
                    out.setConflict(conflict).populateIdentityPubs(keys).build()
                }
                SecurityStorageOp.SECURITY_STORAGE_OP_BATCH_COMMIT -> {
                    val buckets = LinkedHashMap(backend.readBuckets())
                    val keys = LinkedHashMap(backend.readKeys())
                    for (w in effect.batchList) {
                        val cur = buckets[SecurityStoreBucketId(w.bucket.number, w.key)]
                        val mismatch = if (w.remove) {
                            w.expectedRevision.isNotEmpty() && cur != null && cur.revision != w.expectedRevision
                        } else {
                            (w.createOnly && cur != null) ||
                                (w.expectedRevision.isNotEmpty() && (cur == null || cur.revision != w.expectedRevision))
                        }
                        if (mismatch) {
                            return@transaction out.setConflict(true).populateIdentityPubs(keys).build()
                        }
                    }
                    var promotedEdId: SecurityStoreKeyId? = null
                    var promotedEd: SecurityStoreStoredKey? = null
                    var promotedXId: SecurityStoreKeyId? = null
                    var promotedX: SecurityStoreStoredKey? = null
                    if (effect.promoteEdSlot.isNotEmpty()) {
                        val candId = SecurityStoreKeyId(SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_ED.number, effect.promoteEdSlot)
                        val ed = ephemeralKeys[candId] ?: keys[candId]
                        if (ed == null) {
                            return@transaction out.setError("missing promote ed slot")
                                .populateIdentityPubs(keys)
                                .build()
                        }
                        promotedEdId = candId
                        promotedEd = ed
                    }
                    if (effect.promoteXSlot.isNotEmpty()) {
                        val candId = SecurityStoreKeyId(SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_X.number, effect.promoteXSlot)
                        val x = ephemeralKeys[candId] ?: keys[candId]
                        if (x == null) {
                            return@transaction out.setError("missing promote x slot")
                                .populateIdentityPubs(keys)
                                .build()
                        }
                        promotedXId = candId
                        promotedX = x
                    }

                    // Phase 1: persist promoted DEVICE_* keys BEFORE deleting candidate slots or committing buckets.
                    var promoteDirty = false
                    if (promotedEd != null) {
                        keys[SecurityStoreKeyId(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_ED.number, "")] = promotedEd
                        promoteDirty = true
                    }
                    if (promotedX != null) {
                        keys[SecurityStoreKeyId(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_X.number, "")] = promotedX
                        promoteDirty = true
                    }
                    if (promoteDirty) backend.writeKeys(keys)

                    // Phase 2: commit buckets (record, clearing pendingOp / regIntent) while candidate slots remain intact.
                    var bucketsDirty = false
                    var notifyUpdate: ByteArray? = null
                    var notifyDeleted = false
                    var recordWritten = false
                    for (w in effect.batchList) {
                        val id = SecurityStoreBucketId(w.bucket.number, w.key)
                        if (w.remove) {
                            if (buckets.remove(id) != null) bucketsDirty = true
                            if (w.bucket == SecurityStoreBucket.SECURITY_STORE_BUCKET_NOTIFY_RECORD) {
                                notifyDeleted = true
                                notifyUpdate = null
                            }
                        } else {
                            val dataBytes = w.data.toByteArray()
                            buckets[id] = SecurityStoreStoredBucket(w.bucket.number, w.key, w.nextRevision, dataBytes)
                            bucketsDirty = true
                            if (w.bucket == SecurityStoreBucket.SECURITY_STORE_BUCKET_RECORD) {
                                recordWritten = true
                            }
                            if (w.bucket == SecurityStoreBucket.SECURITY_STORE_BUCKET_NOTIFY_RECORD) {
                                notifyUpdate = dataBytes
                                notifyDeleted = false
                            }
                        }
                    }
                    if (bucketsDirty) backend.writeBuckets(buckets)
                    if (notifyUpdate != null) backend.writeNotifyBlob(notifyUpdate)
                    else if (notifyDeleted) backend.deleteNotifyBlob()
                    if (effect.clearLoginToken) backend.deleteLoginToken()
                    if (recordWritten) backend.clearUpgradeNotice()

                    // Phase 3: best-effort post-commit cleanup of promoted candidates and discarded slots.
                    var cleanupDirty = false
                    if (promotedEdId != null) {
                        ephemeralKeys.remove(promotedEdId)
                        if (keys.remove(promotedEdId) != null) cleanupDirty = true
                    }
                    if (promotedXId != null) {
                        ephemeralKeys.remove(promotedXId)
                        if (keys.remove(promotedXId) != null) cleanupDirty = true
                    }
                    for (slot in effect.discardSlotsList) {
                        for (kind in listOf(
                            SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_ED,
                            SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_X,
                            SecurityKeyKind.SECURITY_KEY_KIND_EPHEMERAL_X,
                        )) {
                            val id = SecurityStoreKeyId(kind.number, slot)
                            ephemeralKeys.remove(id)
                            if (kind != SecurityKeyKind.SECURITY_KEY_KIND_EPHEMERAL_X && keys.remove(id) != null) {
                                cleanupDirty = true
                            }
                        }
                    }
                    if (cleanupDirty) runCatching { backend.writeKeys(keys) }
                    out.setConflict(false).populateIdentityPubs(keys).build()
                }
                else -> out.setError("unsupported storage op").build()
            }
        }
        } catch (e: Exception) {
            SecurityStorageResultEvent.newBuilder()
                .setOpId(effect.opId)
                .setError(e.message ?: e.javaClass.simpleName)
                .build()
        }

    override fun loadLoginToken(server: String, deviceId: String): LoginToken? = onLoadLoginToken(server, deviceId)

    override fun saveLoginToken(server: String, deviceId: String, token: LoginToken) = onSaveLoginToken(server, deviceId, token)

    override fun clearLoginToken(server: String, deviceId: String) = onClearLoginToken(server, deviceId)

    override fun readNotifyBlob(): ByteArray? = backend.readNotifyBlob()

    override fun dismissUpgradeNotice() = backend.transaction { backend.clearUpgradeNotice() }

    override fun clearMemoryCache() = onClearCache()
}

/** Pure-JVM in-memory v4 security store for tests and headless Remote instances. */
class MemorySecurityStoreV4(
    private val onClearAll: (() -> Unit)? = null,
) : SecurityStoreV4 {
    private val lock = Any()
    private val persistentKeys = LinkedHashMap<SecurityStoreKeyId, SecurityStoreStoredKey>()
    private val buckets = LinkedHashMap<SecurityStoreBucketId, SecurityStoreStoredBucket>()
    private var loginTokenEntry: Triple<String, String, LoginToken>? = null
    private var upgradeNoticePending = false
    private var legacyPendingForTest = false

    private val core = SecurityStoreV4Core(
        backend = object : SecurityStoreV4Backend {
            override fun <T> transaction(block: () -> T): T = synchronized(lock) { block() }
            override fun readBuckets() = buckets
            override fun writeBuckets(next: Map<SecurityStoreBucketId, SecurityStoreStoredBucket>) {
                if (next !== buckets) {
                    buckets.clear()
                    buckets.putAll(next)
                }
            }
            override fun readKeys() = persistentKeys
            override fun writeKeys(next: Map<SecurityStoreKeyId, SecurityStoreStoredKey>) {
                if (next !== persistentKeys) {
                    persistentKeys.clear()
                    persistentKeys.putAll(next)
                }
            }
            override fun readNotifyBlob(): ByteArray? =
                buckets[SecurityStoreBucketId(SecurityStoreBucket.SECURITY_STORE_BUCKET_NOTIFY_RECORD.number, "notify")]?.data?.copyOf()
            override fun writeNotifyBlob(blob: ByteArray) {}
            override fun deleteNotifyBlob() {}
            override fun deleteLoginToken() { loginTokenEntry = null }
            override fun clearUpgradeNotice() { upgradeNoticePending = false }
            override fun detectAndClearLegacy(): Boolean {
                if (!legacyPendingForTest) return upgradeNoticePending
                legacyPendingForTest = false
                upgradeNoticePending = true
                return true
            }
            override fun clearAll() {
                persistentKeys.clear()
                buckets.clear()
                loginTokenEntry = null
                upgradeNoticePending = false
                legacyPendingForTest = false
                onClearAll?.invoke()
            }
        },
        onLoadLoginToken = { server, deviceId ->
            synchronized(lock) {
                val e = loginTokenEntry ?: return@synchronized null
                if (e.first == server && e.second == deviceId) e.third else null
            }
        },
        onSaveLoginToken = { server, deviceId, token ->
            synchronized(lock) { loginTokenEntry = Triple(server, deviceId, token) }
        },
        onClearLoginToken = { server, deviceId ->
            synchronized(lock) {
                val e = loginTokenEntry ?: return@synchronized
                if (e.first == server && e.second == deviceId) loginTokenEntry = null
            }
        },
    )

    fun seedLegacyForTest() = synchronized(lock) {
        legacyPendingForTest = true
    }

    fun hasV4Record(): Boolean = synchronized(lock) {
        val rec = buckets[SecurityStoreBucketId(SecurityStoreBucket.SECURITY_STORE_BUCKET_RECORD.number, "record")]
        rec != null && rec.data.isNotEmpty()
    }

    fun upgradeNoticeActive(): Boolean = synchronized(lock) { upgradeNoticePending }

    fun seedTestRecord(
        edPrivate: ByteArray,
        edPublic: ByteArray,
        xPrivate: ByteArray,
        xPublic: ByteArray,
        recordBytes: ByteArray,
        notifyBytes: ByteArray? = null,
    ) = synchronized(lock) {
        persistentKeys[SecurityStoreKeyId(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_ED.number, "")] =
            SecurityStoreStoredKey(edPrivate.copyOf(), edPublic.copyOf())
        persistentKeys[SecurityStoreKeyId(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_X.number, "")] =
            SecurityStoreStoredKey(xPrivate.copyOf(), xPublic.copyOf())
        buckets[SecurityStoreBucketId(SecurityStoreBucket.SECURITY_STORE_BUCKET_RECORD.number, "record")] =
            SecurityStoreStoredBucket(SecurityStoreBucket.SECURITY_STORE_BUCKET_RECORD.number, "record", "rev-1", recordBytes.copyOf())
        if (notifyBytes != null && notifyBytes.isNotEmpty()) {
            buckets[SecurityStoreBucketId(SecurityStoreBucket.SECURITY_STORE_BUCKET_NOTIFY_RECORD.number, "notify")] =
                SecurityStoreStoredBucket(SecurityStoreBucket.SECURITY_STORE_BUCKET_NOTIFY_RECORD.number, "notify", "rev-1", notifyBytes.copyOf())
        }
    }

    override fun generateKey(kind: SecurityKeyKind, slot: String, persist: Boolean): ByteArray = core.generateKey(kind, slot, persist)
    override fun signKey(kind: SecurityKeyKind, slot: String, message: ByteArray): ByteArray = core.signKey(kind, slot, message)
    override fun agreeKey(kind: SecurityKeyKind, slot: String, peerPublic: ByteArray): ByteArray = core.agreeKey(kind, slot, peerPublic)
    override fun encapsulate(recipients: List<SecurityEncapsulateRecipient>): List<SecurityEncapsulatedPair> = core.encapsulate(recipients)
    override fun discardKey(kind: SecurityKeyKind, slot: String) = core.discardKey(kind, slot)
    override fun executeStorage(effect: SecurityStorageEffect): SecurityStorageResultEvent = core.executeStorage(effect)
    override fun loadLoginToken(server: String, deviceId: String): LoginToken? = core.loadLoginToken(server, deviceId)
    override fun saveLoginToken(server: String, deviceId: String, token: LoginToken) = core.saveLoginToken(server, deviceId, token)
    override fun clearLoginToken(server: String, deviceId: String) = core.clearLoginToken(server, deviceId)
    override fun readNotifyBlob(): ByteArray? = core.readNotifyBlob()
    override fun dismissUpgradeNotice() = core.dismissUpgradeNotice()
}
