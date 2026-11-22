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

/** Pure-JVM in-memory v4 security store for tests and headless Remote instances. */
class MemorySecurityStoreV4(
    private val onClearAll: (() -> Unit)? = null,
) : SecurityStoreV4 {
    data class KeyId(val kind: SecurityKeyKind, val slot: String)
    data class BucketId(val bucket: SecurityStoreBucket, val key: String)
    data class StoredBucket(
        val bucket: SecurityStoreBucket,
        val key: String,
        val revision: String,
        val data: ByteArray,
    )
    data class StoredKey(
        val privateKey: ByteArray,
        val publicKey: ByteArray,
    )

    private val lock = Any()
    private val persistentKeys = LinkedHashMap<KeyId, StoredKey>()
    private val ephemeralKeys = LinkedHashMap<KeyId, StoredKey>()
    private val buckets = LinkedHashMap<BucketId, StoredBucket>()
    private var loginTokenEntry: Triple<String, String, LoginToken>? = null
    private var upgradeNoticePending = false
    private var legacyPendingForTest = false

    private fun isPersistent(kind: SecurityKeyKind, persist: Boolean): Boolean =
        when (kind) {
            SecurityKeyKind.SECURITY_KEY_KIND_EPHEMERAL_X -> false
            SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_ED,
            SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_X -> true
            else -> persist
        }

    fun seedLegacyForTest() = synchronized(lock) {
        legacyPendingForTest = true
    }

    fun hasV4Record(): Boolean = synchronized(lock) {
        val rec = buckets[BucketId(SecurityStoreBucket.SECURITY_STORE_BUCKET_RECORD, "record")]
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
        persistentKeys[KeyId(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_ED, "")] =
            StoredKey(edPrivate.copyOf(), edPublic.copyOf())
        persistentKeys[KeyId(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_X, "")] =
            StoredKey(xPrivate.copyOf(), xPublic.copyOf())
        buckets[BucketId(SecurityStoreBucket.SECURITY_STORE_BUCKET_RECORD, "record")] =
            StoredBucket(SecurityStoreBucket.SECURITY_STORE_BUCKET_RECORD, "record", "rev-1", recordBytes.copyOf())
        if (notifyBytes != null && notifyBytes.isNotEmpty()) {
            buckets[BucketId(SecurityStoreBucket.SECURITY_STORE_BUCKET_NOTIFY_RECORD, "notify")] =
                StoredBucket(SecurityStoreBucket.SECURITY_STORE_BUCKET_NOTIFY_RECORD, "notify", "rev-1", notifyBytes.copyOf())
        }
    }

    private fun generateRawEd25519(): StoredKey {
        while (true) {
            val seed = randomBytes(KEY_SIZE)
            val pub = runCatching { Corebind.ed25519PublicKey(seed) }.getOrNull() ?: continue
            return StoredKey(seed, pub)
        }
    }

    private fun generateRawX25519(): StoredKey {
        while (true) {
            val priv = randomBytes(KEY_SIZE)
            val pub = runCatching { Corebind.x25519PublicKey(priv) }.getOrNull() ?: continue
            return StoredKey(priv, pub)
        }
    }

    private fun generatePairForKind(kind: SecurityKeyKind): StoredKey = when (kind) {
        SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_ED,
        SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_ED -> generateRawEd25519()
        SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_X,
        SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_X,
        SecurityKeyKind.SECURITY_KEY_KIND_EPHEMERAL_X -> generateRawX25519()
        else -> throw IllegalArgumentException("unsupported key kind: $kind")
    }

    override fun generateKey(kind: SecurityKeyKind, slot: String, persist: Boolean): ByteArray =
        synchronized(lock) {
            val id = KeyId(kind, slot)
            val pair = generatePairForKind(kind)
            if (isPersistent(kind, persist)) {
                ephemeralKeys.remove(id)
                persistentKeys[id] = pair
            } else {
                ephemeralKeys[id] = pair
            }
            pair.publicKey.copyOf()
        }

    private fun loadKeyLocked(kind: SecurityKeyKind, slot: String): StoredKey? {
        val id = KeyId(kind, slot)
        if (kind == SecurityKeyKind.SECURITY_KEY_KIND_EPHEMERAL_X) {
            return ephemeralKeys[id]
        }
        return ephemeralKeys[id] ?: persistentKeys[id]
    }

    override fun signKey(kind: SecurityKeyKind, slot: String, message: ByteArray): ByteArray {
        val row = synchronized(lock) { loadKeyLocked(kind, slot) }
            ?: throw IllegalStateException("missing signing key")
        return Corebind.ed25519Sign(row.privateKey, message)
    }

    override fun agreeKey(kind: SecurityKeyKind, slot: String, peerPublic: ByteArray): ByteArray {
        require(peerPublic.size == KEY_SIZE) { "invalid peer public key" }
        val row = synchronized(lock) { loadKeyLocked(kind, slot) }
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
        synchronized(lock) {
            val id = KeyId(kind, slot)
            ephemeralKeys.remove(id)
            if (kind != SecurityKeyKind.SECURITY_KEY_KIND_EPHEMERAL_X) {
                persistentKeys.remove(id)
            }
        }
    }

    private fun SecurityStorageResultEvent.Builder.populateIdentityPubsLocked(): SecurityStorageResultEvent.Builder {
        val ed = persistentKeys[KeyId(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_ED, "")]
        val x = persistentKeys[KeyId(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_X, "")]
        setHasDeviceEd(ed != null)
        if (ed != null) setDeviceEdPub(ByteString.copyFrom(ed.publicKey))
        setHasDeviceX(x != null)
        if (x != null) setDeviceXPub(ByteString.copyFrom(x.publicKey))
        return this
    }

    override fun executeStorage(effect: SecurityStorageEffect): SecurityStorageResultEvent =
        synchronized(lock) {
            val out = SecurityStorageResultEvent.newBuilder().setOpId(effect.opId)
            when (effect.op) {
                SecurityStorageOp.SECURITY_STORAGE_OP_CLEAR_ALL -> {
                    ephemeralKeys.clear()
                    persistentKeys.clear()
                    buckets.clear()
                    loginTokenEntry = null
                    upgradeNoticePending = false
                    legacyPendingForTest = false
                    onClearAll?.invoke()
                    out.build()
                }
                SecurityStorageOp.SECURITY_STORAGE_OP_READ -> {
                    val row = buckets[BucketId(effect.bucket, effect.key)]
                    if (row != null) {
                        out.setFound(true)
                            .setRevision(row.revision)
                            .setData(ByteString.copyFrom(row.data))
                    }
                    out.populateIdentityPubsLocked().build()
                }
                SecurityStorageOp.SECURITY_STORAGE_OP_READ_BOOTSTRAP -> {
                    val hasV4State = buckets.values.any { row ->
                        row.data.isNotEmpty() && row.bucket in setOf(
                            SecurityStoreBucket.SECURITY_STORE_BUCKET_RECORD,
                            SecurityStoreBucket.SECURITY_STORE_BUCKET_OPERATION,
                            SecurityStoreBucket.SECURITY_STORE_BUCKET_REGISTRATION_INTENT,
                        )
                    }
                    val legacyCleared = if (!hasV4State) {
                        if (legacyPendingForTest) {
                            legacyPendingForTest = false
                            upgradeNoticePending = true
                            true
                        } else {
                            upgradeNoticePending
                        }
                    } else {
                        upgradeNoticePending = false
                        false
                    }
                    for (row in buckets.values) {
                        if (effect.bucket != SecurityStoreBucket.SECURITY_STORE_BUCKET_UNSPECIFIED &&
                            row.bucket != effect.bucket
                        ) {
                            continue
                        }
                        out.addEntries(
                            SecurityStorageEntry.newBuilder()
                                .setBucket(row.bucket)
                                .setKey(row.key)
                                .setRevision(row.revision)
                                .setData(ByteString.copyFrom(row.data))
                                .build(),
                        )
                    }
                    out.setLegacyCleared(legacyCleared)
                    out.populateIdentityPubsLocked().build()
                }
                SecurityStorageOp.SECURITY_STORAGE_OP_WRITE_CAS -> {
                    val id = BucketId(effect.bucket, effect.key)
                    val cur = buckets[id]
                    val conflict =
                        (effect.createOnly && cur != null) ||
                            (effect.expectedRevision.isNotEmpty() && (cur == null || cur.revision != effect.expectedRevision))
                    if (conflict) {
                        out.setConflict(true).populateIdentityPubsLocked().build()
                    } else {
                        val nextRev = effect.nextRevision
                        buckets[id] = StoredBucket(effect.bucket, effect.key, nextRev, effect.data.toByteArray())
                        if (effect.bucket == SecurityStoreBucket.SECURITY_STORE_BUCKET_RECORD) {
                            upgradeNoticePending = false
                        }
                        out.setConflict(false).setRevision(nextRev).populateIdentityPubsLocked().build()
                    }
                }
                SecurityStorageOp.SECURITY_STORAGE_OP_DELETE_CAS -> {
                    val id = BucketId(effect.bucket, effect.key)
                    val cur = buckets[id]
                    val conflict =
                        effect.expectedRevision.isNotEmpty() && cur != null && cur.revision != effect.expectedRevision
                    if (!conflict) {
                        buckets.remove(id)
                    }
                    out.setConflict(conflict).populateIdentityPubsLocked().build()
                }
                SecurityStorageOp.SECURITY_STORAGE_OP_BATCH_COMMIT -> {
                    for (w in effect.batchList) {
                        val cur = buckets[BucketId(w.bucket, w.key)]
                        val mismatch = if (w.remove) {
                            w.expectedRevision.isNotEmpty() && cur != null && cur.revision != w.expectedRevision
                        } else {
                            (w.createOnly && cur != null) ||
                                (w.expectedRevision.isNotEmpty() && (cur == null || cur.revision != w.expectedRevision))
                        }
                        if (mismatch) {
                            return@synchronized out.setConflict(true).populateIdentityPubsLocked().build()
                        }
                    }
                    var promotedEd: StoredKey? = null
                    var promotedX: StoredKey? = null
                    if (effect.promoteEdSlot.isNotEmpty()) {
                        promotedEd = loadKeyLocked(SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_ED, effect.promoteEdSlot)
                        if (promotedEd == null) {
                            return@synchronized out.setError("missing promote ed slot").populateIdentityPubsLocked().build()
                        }
                    }
                    if (effect.promoteXSlot.isNotEmpty()) {
                        promotedX = loadKeyLocked(SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_X, effect.promoteXSlot)
                        if (promotedX == null) {
                            return@synchronized out.setError("missing promote x slot").populateIdentityPubsLocked().build()
                        }
                    }
                    if (promotedEd != null) {
                        val candId = KeyId(SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_ED, effect.promoteEdSlot)
                        persistentKeys[KeyId(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_ED, "")] = promotedEd
                        persistentKeys.remove(candId)
                        ephemeralKeys.remove(candId)
                    }
                    if (promotedX != null) {
                        val candId = KeyId(SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_X, effect.promoteXSlot)
                        persistentKeys[KeyId(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_X, "")] = promotedX
                        persistentKeys.remove(candId)
                        ephemeralKeys.remove(candId)
                    }
                    for (w in effect.batchList) {
                        val id = BucketId(w.bucket, w.key)
                        if (w.remove) {
                            buckets.remove(id)
                        } else {
                            buckets[id] = StoredBucket(w.bucket, w.key, w.nextRevision, w.data.toByteArray())
                            if (w.bucket == SecurityStoreBucket.SECURITY_STORE_BUCKET_RECORD) {
                                upgradeNoticePending = false
                            }
                        }
                    }
                    for (slot in effect.discardSlotsList) {
                        for (kind in listOf(
                            SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_ED,
                            SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_X,
                            SecurityKeyKind.SECURITY_KEY_KIND_EPHEMERAL_X,
                        )) {
                            val id = KeyId(kind, slot)
                            ephemeralKeys.remove(id)
                            if (kind != SecurityKeyKind.SECURITY_KEY_KIND_EPHEMERAL_X) {
                                persistentKeys.remove(id)
                            }
                        }
                    }
                    if (effect.clearLoginToken) {
                        loginTokenEntry = null
                    }
                    out.setConflict(false).populateIdentityPubsLocked().build()
                }
                else -> out.setError("unsupported storage op").build()
            }
        }

    override fun loadLoginToken(server: String, deviceId: String): LoginToken? =
        synchronized(lock) {
            val e = loginTokenEntry ?: return@synchronized null
            if (e.first == server && e.second == deviceId) e.third else null
        }

    override fun saveLoginToken(server: String, deviceId: String, token: LoginToken) {
        synchronized(lock) {
            loginTokenEntry = Triple(server, deviceId, token)
        }
    }

    override fun clearLoginToken(server: String, deviceId: String) {
        synchronized(lock) {
            val e = loginTokenEntry ?: return
            if (e.first == server && e.second == deviceId) {
                loginTokenEntry = null
            }
        }
    }

    override fun readNotifyBlob(): ByteArray? =
        synchronized(lock) {
            buckets[BucketId(SecurityStoreBucket.SECURITY_STORE_BUCKET_NOTIFY_RECORD, "notify")]?.data?.copyOf()
        }

    override fun dismissUpgradeNotice() {
        synchronized(lock) {
            upgradeNoticePending = false
        }
    }
}
