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

interface SecurityStoreV4Proxy : SecurityStoreV4 {
    val securityCore: SecurityStoreV4Core
    override fun generateKey(kind: SecurityKeyKind, slot: String, persist: Boolean): ByteArray = securityCore.generateKey(kind, slot, persist)
    override fun signKey(kind: SecurityKeyKind, slot: String, message: ByteArray): ByteArray = securityCore.signKey(kind, slot, message)
    override fun agreeKey(kind: SecurityKeyKind, slot: String, peerPublic: ByteArray): ByteArray = securityCore.agreeKey(kind, slot, peerPublic)
    override fun encapsulate(recipients: List<SecurityEncapsulateRecipient>): List<SecurityEncapsulatedPair> = securityCore.encapsulate(recipients)
    override fun discardKey(kind: SecurityKeyKind, slot: String) = securityCore.discardKey(kind, slot)
    override fun executeStorage(effect: SecurityStorageEffect): SecurityStorageResultEvent = securityCore.executeStorage(effect)
    override fun loadLoginToken(server: String, deviceId: String): LoginToken? = securityCore.loadLoginToken(server, deviceId)
    override fun saveLoginToken(server: String, deviceId: String, token: LoginToken) = securityCore.saveLoginToken(server, deviceId, token)
    override fun clearLoginToken(server: String, deviceId: String) = securityCore.clearLoginToken(server, deviceId)
    override fun readNotifyBlob(): ByteArray? = securityCore.readNotifyBlob()
    override fun dismissUpgradeNotice() = securityCore.dismissUpgradeNotice()
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

    private inline fun generateRawKey(derivePub: (ByteArray) -> ByteArray): SecurityStoreStoredKey {
        while (true) {
            val priv = randomBytes(KEY_SIZE)
            val pub = runCatching { derivePub(priv) }.getOrNull() ?: continue
            return SecurityStoreStoredKey(priv, pub)
        }
    }

    private fun generatePairForKind(kind: SecurityKeyKind): SecurityStoreStoredKey = when (kind) {
        SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_ED,
        SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_ED -> generateRawKey(Corebind::ed25519PublicKey)
        SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_X,
        SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_X,
        SecurityKeyKind.SECURITY_KEY_KIND_EPHEMERAL_X -> generateRawKey(Corebind::x25519PublicKey)
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
            val eph = generateRawKey(Corebind::x25519PublicKey)
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

    private fun isCasConflict(
        cur: SecurityStoreStoredBucket?,
        createOnly: Boolean,
        expectedRevision: String,
        remove: Boolean = false,
    ): Boolean = if (remove) {
        expectedRevision.isNotEmpty() && cur != null && cur.revision != expectedRevision
    } else {
        (createOnly && cur != null) ||
            (expectedRevision.isNotEmpty() && (cur == null || cur.revision != expectedRevision))
    }

    private fun resolvePromoteKey(
        kind: SecurityKeyKind,
        slot: String,
        keys: Map<SecurityStoreKeyId, SecurityStoreStoredKey>,
    ): Pair<SecurityStoreKeyId, SecurityStoreStoredKey>? {
        val id = SecurityStoreKeyId(kind.number, slot)
        return (ephemeralKeys[id] ?: keys[id])?.let { id to it }
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
                        buckets[SecurityStoreBucketId(effect.bucket.number, effect.key)]?.let { row ->
                            out.setFound(true).setRevision(row.revision).setData(ByteString.copyFrom(row.data))
                        }
                        out.populateIdentityPubs(keys).build()
                    }
                    SecurityStorageOp.SECURITY_STORAGE_OP_READ_BOOTSTRAP -> {
                        val buckets = backend.readBuckets()
                        val keys = backend.readKeys()
                        val hasV4State = buckets.values.any { row ->
                            row.data.isNotEmpty() && row.bucketNum in V4_STATE_BUCKET_NUMS
                        }
                        val legacyCleared = if (!hasV4State) backend.detectAndClearLegacy() else {
                            backend.clearUpgradeNotice()
                            false
                        }
                        for (row in buckets.values) {
                            val bEnum = SecurityStoreBucket.forNumber(row.bucketNum)
                                ?: SecurityStoreBucket.SECURITY_STORE_BUCKET_UNSPECIFIED
                            if (effect.bucket != SecurityStoreBucket.SECURITY_STORE_BUCKET_UNSPECIFIED && bEnum != effect.bucket) continue
                            out.addEntries(
                                SecurityStorageEntry.newBuilder()
                                    .setBucket(bEnum).setKey(row.key).setRevision(row.revision)
                                    .setData(ByteString.copyFrom(row.data)).build(),
                            )
                        }
                        out.setLegacyCleared(legacyCleared).populateIdentityPubs(keys).build()
                    }
                    SecurityStorageOp.SECURITY_STORAGE_OP_WRITE_CAS -> {
                        val buckets = LinkedHashMap(backend.readBuckets())
                        val keys = backend.readKeys()
                        val id = SecurityStoreBucketId(effect.bucket.number, effect.key)
                        if (isCasConflict(buckets[id], effect.createOnly, effect.expectedRevision)) {
                            out.setConflict(true).populateIdentityPubs(keys).build()
                        } else {
                            val dataBytes = effect.data.toByteArray()
                            buckets[id] = SecurityStoreStoredBucket(effect.bucket.number, effect.key, effect.nextRevision, dataBytes)
                            backend.writeBuckets(buckets)
                            if (effect.bucket == SecurityStoreBucket.SECURITY_STORE_BUCKET_NOTIFY_RECORD) backend.writeNotifyBlob(dataBytes)
                            if (effect.bucket == SecurityStoreBucket.SECURITY_STORE_BUCKET_RECORD) backend.clearUpgradeNotice()
                            out.setConflict(false).setRevision(effect.nextRevision).populateIdentityPubs(keys).build()
                        }
                    }
                    SecurityStorageOp.SECURITY_STORAGE_OP_DELETE_CAS -> {
                        val buckets = LinkedHashMap(backend.readBuckets())
                        val keys = backend.readKeys()
                        val id = SecurityStoreBucketId(effect.bucket.number, effect.key)
                        val conflict = isCasConflict(buckets[id], false, effect.expectedRevision, remove = true)
                        if (!conflict && buckets.remove(id) != null) {
                            backend.writeBuckets(buckets)
                            if (effect.bucket == SecurityStoreBucket.SECURITY_STORE_BUCKET_NOTIFY_RECORD) backend.deleteNotifyBlob()
                        }
                        out.setConflict(conflict).populateIdentityPubs(keys).build()
                    }
                    SecurityStorageOp.SECURITY_STORAGE_OP_BATCH_COMMIT -> {
                        val buckets = LinkedHashMap(backend.readBuckets())
                        val keys = LinkedHashMap(backend.readKeys())
                        if (effect.batchList.any { w ->
                                isCasConflict(buckets[SecurityStoreBucketId(w.bucket.number, w.key)], w.createOnly, w.expectedRevision, w.remove)
                            }
                        ) {
                            return@transaction out.setConflict(true).populateIdentityPubs(keys).build()
                        }
                        val promotedEd = if (effect.promoteEdSlot.isNotEmpty()) {
                            resolvePromoteKey(SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_ED, effect.promoteEdSlot, keys)
                                ?: return@transaction out.setError("missing promote ed slot").populateIdentityPubs(keys).build()
                        } else null
                        val promotedX = if (effect.promoteXSlot.isNotEmpty()) {
                            resolvePromoteKey(SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_X, effect.promoteXSlot, keys)
                                ?: return@transaction out.setError("missing promote x slot").populateIdentityPubs(keys).build()
                        } else null

                        // Phase 1: persist promoted DEVICE_* keys BEFORE deleting candidate slots or committing buckets.
                        var promoteDirty = false
                        if (promotedEd != null) {
                            keys[SecurityStoreKeyId(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_ED.number, "")] = promotedEd.second
                            promoteDirty = true
                        }
                        if (promotedX != null) {
                            keys[SecurityStoreKeyId(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_X.number, "")] = promotedX.second
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
                                if (w.bucket == SecurityStoreBucket.SECURITY_STORE_BUCKET_RECORD) recordWritten = true
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
                        listOfNotNull(promotedEd?.first, promotedX?.first).forEach { candId ->
                            ephemeralKeys.remove(candId)
                            if (keys.remove(candId) != null) cleanupDirty = true
                        }
                        for (slot in effect.discardSlotsList) {
                            for (kind in CLEANUP_CANDIDATE_KINDS) {
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

    companion object {
        private val V4_STATE_BUCKET_NUMS = setOf(
            SecurityStoreBucket.SECURITY_STORE_BUCKET_RECORD.number,
            SecurityStoreBucket.SECURITY_STORE_BUCKET_OPERATION.number,
            SecurityStoreBucket.SECURITY_STORE_BUCKET_REGISTRATION_INTENT.number,
        )
        private val CLEANUP_CANDIDATE_KINDS = listOf(
            SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_ED,
            SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_X,
            SecurityKeyKind.SECURITY_KEY_KIND_EPHEMERAL_X,
        )
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
) : SecurityStoreV4Proxy {
    private val lock = Any()
    private val persistentKeys = LinkedHashMap<SecurityStoreKeyId, SecurityStoreStoredKey>()
    private val buckets = LinkedHashMap<SecurityStoreBucketId, SecurityStoreStoredBucket>()
    private var loginTokenEntry: Triple<String, String, LoginToken>? = null
    private var upgradeNoticePending = false
    private var legacyPendingForTest = false

    override val securityCore = SecurityStoreV4Core(
        backend = object : SecurityStoreV4Backend {
            override fun <T> transaction(block: () -> T): T = synchronized(lock) { block() }
            override fun readBuckets() = buckets
            override fun writeBuckets(buckets: Map<SecurityStoreBucketId, SecurityStoreStoredBucket>) {
                if (buckets !== this@MemorySecurityStoreV4.buckets) {
                    this@MemorySecurityStoreV4.buckets.clear()
                    this@MemorySecurityStoreV4.buckets.putAll(buckets)
                }
            }
            override fun readKeys() = persistentKeys
            override fun writeKeys(keys: Map<SecurityStoreKeyId, SecurityStoreStoredKey>) {
                if (keys !== persistentKeys) {
                    persistentKeys.clear()
                    persistentKeys.putAll(keys)
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
}
