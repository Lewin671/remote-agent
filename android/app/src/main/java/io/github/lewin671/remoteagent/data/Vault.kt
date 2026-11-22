package io.github.lewin671.remoteagent.data

import android.app.KeyguardManager
import android.content.Context
import android.os.Build
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.system.Os
import android.system.OsConstants
import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.KEY_SIZE
import io.github.lewin671.remoteagent.core.LoginToken
import io.github.lewin671.remoteagent.core.SecurityFailure
import io.github.lewin671.remoteagent.core.SecurityStoreV4
import io.github.lewin671.remoteagent.core.proto.SecurityEncapsulateRecipient
import io.github.lewin671.remoteagent.core.proto.SecurityEncapsulatedPair
import io.github.lewin671.remoteagent.core.proto.SecurityKeyKind
import io.github.lewin671.remoteagent.core.proto.SecurityStorageEffect
import io.github.lewin671.remoteagent.core.proto.SecurityStorageEntry
import io.github.lewin671.remoteagent.core.proto.SecurityStorageOp
import io.github.lewin671.remoteagent.core.proto.SecurityStorageResultEvent
import io.github.lewin671.remoteagent.core.proto.SecurityStoreBucket
import io.github.lewin671.remoteagent.core.randomBytes
import io.github.lewin671.remoteagent.corebind.Corebind
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.DataInputStream
import java.io.DataOutputStream
import java.io.File
import java.io.FileOutputStream
import java.security.KeyStore
import java.util.concurrent.ConcurrentHashMap
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * Keystore-backed S6 v4 security storage on Android:
 * - Persistent Ed25519/X25519 keys, v4 buckets, and login tokens are encrypted under
 *   [ACCOUNT_ALIAS] (unlocked-device required on API 28+).
 * - [SECURITY_STORE_BUCKET_NOTIFY_RECORD] is mirrored to `security-v4-notify.bin` under
 *   [NOTIFY_ALIAS] (usable while locked) so FCM pushes can open in Core without unwrapping
 *   device identity keys.
 * - Legacy v3/v2 files trigger a clean wipe (preserving app [Prefs]) and raise a persistent
 *   repair notice flag until dismissed or re-paired.
 */
class Vault internal constructor(
    context: Context,
    private val onDecrypt: ((String) -> Unit)? = null,
    private val onParse: ((String) -> Unit)? = null,
    private val onLegacyWipe: (() -> Unit)? = null,
    private val onClearAll: (() -> Unit)? = null,
) : SecurityStoreV4 {
    constructor(
        context: Context,
        onLegacyWipe: (() -> Unit)? = null,
        onClearAll: (() -> Unit)? = null,
    ) : this(context, null, null, onLegacyWipe, onClearAll)

    private data class KeyId(val kindNum: Int, val slot: String)
    private data class BucketId(val bucketNum: Int, val key: String)
    private data class StoredKey(val privateKey: ByteArray, val publicKey: ByteArray)
    private data class StoredBucket(
        val bucketNum: Int,
        val key: String,
        val revision: String,
        val data: ByteArray,
    )

    private val keyguard = context.getSystemService(KeyguardManager::class.java)
    private val dir = context.noBackupFilesDir
    private val bucketsFile = File(dir, "security-v4-buckets.bin")
    private val keysFile = File(dir, "security-v4-keys.bin")
    private val tokenFile = File(dir, "security-v4-token.bin")
    private val notifyFile = File(dir, "security-v4-notify.bin")
    private val upgradeNoticeFile = File(dir, "security-v4-upgrade-notice.flag")

    private val ephemeralKeys = ConcurrentHashMap<KeyId, StoredKey>()

    // Access under SECURITY_LOCK. Cache only ciphertexts that were decrypted and parsed cleanly.
    private var bucketsCiphertext: ByteArray? = null
    private var bucketsCache: LinkedHashMap<BucketId, StoredBucket>? = null
    private var keysCiphertext: ByteArray? = null
    private var keysCache: LinkedHashMap<KeyId, StoredKey>? = null
    private var notifyCiphertext: ByteArray? = null
    private var notifyPlain: ByteArray? = null

    fun hasAccount(): Boolean = securityTransaction {
        bucketsFile.exists() || notifyFile.exists()
    }

    fun hasUpgradeNotice(): Boolean = upgradeNoticeFile.exists()

    override fun clearMemoryCache() = synchronized(SECURITY_LOCK) {
        bucketsCiphertext = null
        bucketsCache = null
        keysCiphertext = null
        keysCache = null
        notifyCiphertext = null
        notifyPlain = null
    }

    override fun dismissUpgradeNotice() {
        securityTransaction {
            if (upgradeNoticeFile.exists()) {
                upgradeNoticeFile.delete()
                syncDirectory()
            }
        }
    }

    private fun <T> securityTransaction(block: () -> T): T =
        synchronized(SECURITY_LOCK) {
            dir.mkdirs()
            FileOutputStream(File(dir, "security-v4.lock"), true).channel.use { channel ->
                channel.lock().use { block() }
            }
        }

    private fun isPersistent(kind: SecurityKeyKind, persist: Boolean): Boolean =
        when (kind) {
            SecurityKeyKind.SECURITY_KEY_KIND_EPHEMERAL_X -> false
            SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_ED,
            SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_X -> true
            else -> persist
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

    override fun generateKey(kind: SecurityKeyKind, slot: String, persist: Boolean): ByteArray {
        val pair = generatePairForKind(kind)
        val id = KeyId(kind.number, slot)
        if (!isPersistent(kind, persist)) {
            ephemeralKeys[id] = pair
            return pair.publicKey.copyOf()
        }
        securityTransaction {
            ephemeralKeys.remove(id)
            val map = LinkedHashMap(readKeysUnlocked())
            map[id] = pair
            writeKeysUnlocked(map)
        }
        return pair.publicKey.copyOf()
    }

    private fun loadKeyLocked(kind: SecurityKeyKind, slot: String): StoredKey? {
        val id = KeyId(kind.number, slot)
        if (kind == SecurityKeyKind.SECURITY_KEY_KIND_EPHEMERAL_X) {
            return ephemeralKeys[id]
        }
        ephemeralKeys[id]?.let { return it }
        return readKeysUnlocked()[id]
    }

    override fun signKey(kind: SecurityKeyKind, slot: String, message: ByteArray): ByteArray {
        val row = securityTransaction { loadKeyLocked(kind, slot) }
            ?: throw IllegalStateException("missing signing key")
        return Corebind.ed25519Sign(row.privateKey, message)
    }

    override fun agreeKey(kind: SecurityKeyKind, slot: String, peerPublic: ByteArray): ByteArray {
        require(peerPublic.size == KEY_SIZE) { "invalid peer public key" }
        val row = securityTransaction { loadKeyLocked(kind, slot) }
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
        val id = KeyId(kind.number, slot)
        ephemeralKeys.remove(id)
        if (kind == SecurityKeyKind.SECURITY_KEY_KIND_EPHEMERAL_X) return
        securityTransaction {
            val map = LinkedHashMap(readKeysUnlocked())
            if (map.remove(id) != null) {
                writeKeysUnlocked(map)
            }
        }
    }

    override fun executeStorage(effect: SecurityStorageEffect): SecurityStorageResultEvent =
        securityTransaction {
            val out = SecurityStorageResultEvent.newBuilder().setOpId(effect.opId)
            when (effect.op) {
                SecurityStorageOp.SECURITY_STORAGE_OP_CLEAR_ALL -> {
                    clearAllUnlocked()
                    onClearAll?.invoke()
                    out.build()
                }
                SecurityStorageOp.SECURITY_STORAGE_OP_READ -> {
                    val buckets = readBucketsUnlocked()
                    val keys = readKeysUnlocked()
                    val row = buckets[BucketId(effect.bucket.number, effect.key)]
                    if (row != null) {
                        out.setFound(true)
                            .setRevision(row.revision)
                            .setData(ByteString.copyFrom(row.data))
                    }
                    out.populateIdentityPubs(keys).build()
                }
                SecurityStorageOp.SECURITY_STORAGE_OP_READ_BOOTSTRAP -> {
                    val buckets = readBucketsUnlocked()
                    val keys = readKeysUnlocked()
                    val hasV4State = buckets.values.any { row ->
                        row.data.isNotEmpty() && row.bucketNum in setOf(
                            SecurityStoreBucket.SECURITY_STORE_BUCKET_RECORD.number,
                            SecurityStoreBucket.SECURITY_STORE_BUCKET_OPERATION.number,
                            SecurityStoreBucket.SECURITY_STORE_BUCKET_REGISTRATION_INTENT.number,
                        )
                    }
                    val legacyCleared = if (!hasV4State) {
                        detectAndClearLegacyUnlocked()
                    } else {
                        if (upgradeNoticeFile.exists()) {
                            upgradeNoticeFile.delete()
                            syncDirectory()
                        }
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
                    val buckets = LinkedHashMap(readBucketsUnlocked())
                    val keys = readKeysUnlocked()
                    val id = BucketId(effect.bucket.number, effect.key)
                    val cur = buckets[id]
                    val conflict =
                        (effect.createOnly && cur != null) ||
                            (effect.expectedRevision.isNotEmpty() && (cur == null || cur.revision != effect.expectedRevision))
                    if (conflict) {
                        out.setConflict(true).populateIdentityPubs(keys).build()
                    } else {
                        val dataBytes = effect.data.toByteArray()
                        buckets[id] = StoredBucket(effect.bucket.number, effect.key, effect.nextRevision, dataBytes)
                        writeBucketsUnlocked(buckets)
                        if (effect.bucket == SecurityStoreBucket.SECURITY_STORE_BUCKET_NOTIFY_RECORD) {
                            writeNotifyBlobUnlocked(dataBytes)
                        }
                        if (effect.bucket == SecurityStoreBucket.SECURITY_STORE_BUCKET_RECORD && upgradeNoticeFile.exists()) {
                            upgradeNoticeFile.delete()
                            syncDirectory()
                        }
                        out.setConflict(false)
                            .setRevision(effect.nextRevision)
                            .populateIdentityPubs(keys)
                            .build()
                    }
                }
                SecurityStorageOp.SECURITY_STORAGE_OP_DELETE_CAS -> {
                    val buckets = LinkedHashMap(readBucketsUnlocked())
                    val keys = readKeysUnlocked()
                    val id = BucketId(effect.bucket.number, effect.key)
                    val cur = buckets[id]
                    val conflict =
                        effect.expectedRevision.isNotEmpty() && cur != null && cur.revision != effect.expectedRevision
                    if (!conflict && buckets.remove(id) != null) {
                        writeBucketsUnlocked(buckets)
                        if (effect.bucket == SecurityStoreBucket.SECURITY_STORE_BUCKET_NOTIFY_RECORD) {
                            deleteNotifyBlobUnlocked()
                        }
                    }
                    out.setConflict(conflict).populateIdentityPubs(keys).build()
                }
                SecurityStorageOp.SECURITY_STORAGE_OP_BATCH_COMMIT -> {
                    val buckets = LinkedHashMap(readBucketsUnlocked())
                    val keys = LinkedHashMap(readKeysUnlocked())
                    for (w in effect.batchList) {
                        val cur = buckets[BucketId(w.bucket.number, w.key)]
                        val mismatch = if (w.remove) {
                            w.expectedRevision.isNotEmpty() && cur != null && cur.revision != w.expectedRevision
                        } else {
                            (w.createOnly && cur != null) ||
                                (w.expectedRevision.isNotEmpty() && (cur == null || cur.revision != w.expectedRevision))
                        }
                        if (mismatch) {
                            return@securityTransaction out.setConflict(true).populateIdentityPubs(keys).build()
                        }
                    }
                    var promotedEd: StoredKey? = null
                    var promotedX: StoredKey? = null
                    var keysDirty = false
                    var bucketsDirty = false
                    var notifyUpdate: ByteArray? = null
                    var notifyDeleted = false

                    if (effect.promoteEdSlot.isNotEmpty()) {
                        val candId = KeyId(SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_ED.number, effect.promoteEdSlot)
                        promotedEd = ephemeralKeys[candId] ?: keys[candId]
                        if (promotedEd == null) {
                            return@securityTransaction out.setError("missing promote ed slot")
                                .populateIdentityPubs(keys)
                                .build()
                        }
                    }
                    if (effect.promoteXSlot.isNotEmpty()) {
                        val candId = KeyId(SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_X.number, effect.promoteXSlot)
                        promotedX = ephemeralKeys[candId] ?: keys[candId]
                        if (promotedX == null) {
                            return@securityTransaction out.setError("missing promote x slot")
                                .populateIdentityPubs(keys)
                                .build()
                        }
                    }
                    if (promotedEd != null) {
                        val candId = KeyId(SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_ED.number, effect.promoteEdSlot)
                        keys[KeyId(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_ED.number, "")] = promotedEd
                        keys.remove(candId)
                        ephemeralKeys.remove(candId)
                        keysDirty = true
                    }
                    if (promotedX != null) {
                        val candId = KeyId(SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_X.number, effect.promoteXSlot)
                        keys[KeyId(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_X.number, "")] = promotedX
                        keys.remove(candId)
                        ephemeralKeys.remove(candId)
                        keysDirty = true
                    }
                    for (slot in effect.discardSlotsList) {
                        for (kind in listOf(
                            SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_ED,
                            SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_X,
                            SecurityKeyKind.SECURITY_KEY_KIND_EPHEMERAL_X,
                        )) {
                            val id = KeyId(kind.number, slot)
                            ephemeralKeys.remove(id)
                            if (kind != SecurityKeyKind.SECURITY_KEY_KIND_EPHEMERAL_X && keys.remove(id) != null) {
                                keysDirty = true
                            }
                        }
                    }
                    var recordWritten = false
                    for (w in effect.batchList) {
                        val id = BucketId(w.bucket.number, w.key)
                        if (w.remove) {
                            if (buckets.remove(id) != null) bucketsDirty = true
                            if (w.bucket == SecurityStoreBucket.SECURITY_STORE_BUCKET_NOTIFY_RECORD) {
                                notifyDeleted = true
                                notifyUpdate = null
                            }
                        } else {
                            val dataBytes = w.data.toByteArray()
                            buckets[id] = StoredBucket(w.bucket.number, w.key, w.nextRevision, dataBytes)
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
                    if (keysDirty) writeKeysUnlocked(keys)
                    if (bucketsDirty) writeBucketsUnlocked(buckets)
                    if (notifyUpdate != null) writeNotifyBlobUnlocked(notifyUpdate)
                    else if (notifyDeleted) deleteNotifyBlobUnlocked()
                    if (effect.clearLoginToken) deleteLoginTokenUnlocked()
                    if (recordWritten && upgradeNoticeFile.exists()) {
                        upgradeNoticeFile.delete()
                        syncDirectory()
                    }
                    out.setConflict(false).populateIdentityPubs(keys).build()
                }
                else -> out.setError("unsupported storage op").build()
            }
        }

    override fun loadLoginToken(server: String, deviceId: String): LoginToken? =
        securityTransaction {
            if (Build.VERSION.SDK_INT >= 28 && keyguard.isDeviceLocked) {
                return@securityTransaction null
            }
            val buckets = try {
                readBucketsUnlocked()
            } catch (_: java.security.GeneralSecurityException) {
                return@securityTransaction null
            } catch (_: java.security.ProviderException) {
                return@securityTransaction null
            } catch (_: Exception) {
                null
            }
            if (buckets != null && buckets.isEmpty()) {
                deleteLoginTokenUnlocked()
                return@securityTransaction null
            }
            val raw = try {
                read(tokenFile, ACCOUNT_ALIAS) ?: return@securityTransaction null
            } catch (_: java.security.GeneralSecurityException) {
                return@securityTransaction null
            } catch (_: java.security.ProviderException) {
                return@securityTransaction null
            } catch (_: SecurityFailure) {
                deleteLoginTokenUnlocked()
                return@securityTransaction null
            }
            try {
                DataInputStream(ByteArrayInputStream(raw)).use { s ->
                    if (s.readUTF() != server || s.readUTF() != deviceId) {
                        deleteLoginTokenUnlocked()
                        return@securityTransaction null
                    }
                    LoginToken(s.readUTF(), s.readLong())
                }
            } catch (_: Exception) {
                deleteLoginTokenUnlocked()
                null
            }
        }

    override fun saveLoginToken(server: String, deviceId: String, token: LoginToken) =
        securityTransaction {
            val out = ByteArrayOutputStream()
            DataOutputStream(out).use { s ->
                s.writeUTF(server)
                s.writeUTF(deviceId)
                s.writeUTF(token.value)
                s.writeLong(token.expiresMs)
            }
            write(tokenFile, ACCOUNT_ALIAS, out.toByteArray(), unlockedOnly = true)
        }

    override fun clearLoginToken(server: String, deviceId: String) =
        securityTransaction {
            val existing = runCatching {
                val raw = read(tokenFile, ACCOUNT_ALIAS) ?: return@securityTransaction
                DataInputStream(ByteArrayInputStream(raw)).use { s -> s.readUTF() to s.readUTF() }
            }.getOrNull()
            if (existing == null || (existing.first == server && existing.second == deviceId)) {
                deleteLoginTokenUnlocked()
            }
        }

    override fun readNotifyBlob(): ByteArray? =
        securityTransaction {
            if (!notifyFile.exists()) {
                notifyCiphertext = null
                notifyPlain = null
                return@securityTransaction null
            }
            val data = notifyFile.readBytes()
            if (notifyCiphertext?.contentEquals(data) == true) {
                return@securityTransaction notifyPlain?.copyOf()
            }
            val plain = decrypt(notifyFile, NOTIFY_ALIAS, data) ?: return@securityTransaction null
            notifyCiphertext = data
            notifyPlain = plain
            plain.copyOf()
        }

    fun clear() = securityTransaction {
        clearAllUnlocked()
        onClearAll?.invoke()
    }

    private fun clearAllUnlocked() {
        ephemeralKeys.clear()
        clearMemoryCache()
        deleteLoginTokenUnlocked()
        deleteNotifyBlobUnlocked()
        bucketsFile.delete()
        keysFile.delete()
        upgradeNoticeFile.delete()
        deleteLegacyFilesUnlocked()
        val ks = keyStore()
        for (a in listOf(ACCOUNT_ALIAS, NOTIFY_ALIAS)) {
            if (ks.containsAlias(a)) ks.deleteEntry(a)
        }
        syncDirectory()
    }

    private fun detectAndClearLegacyUnlocked(): Boolean {
        val hasLegacy = LEGACY_FILES.any { File(dir, it).exists() } ||
            (dir.listFiles()?.any { it.name.startsWith("pair-candidate-") } == true)
        if (!hasLegacy) {
            return upgradeNoticeFile.exists()
        }
        clearMemoryCache()
        ephemeralKeys.clear()
        deleteLegacyFilesUnlocked()
        deleteLoginTokenUnlocked()
        deleteNotifyBlobUnlocked()
        bucketsFile.delete()
        keysFile.delete()
        upgradeNoticeFile.writeBytes(byteArrayOf(1))
        syncDirectory()
        onLegacyWipe?.invoke()
        return true
    }

    private fun deleteLegacyFilesUnlocked() {
        for (name in LEGACY_FILES) {
            val f = File(dir, name)
            if (f.isDirectory) f.deleteRecursively() else f.delete()
        }
        dir.listFiles()?.filter { it.name.startsWith("pair-candidate-") }?.forEach { it.delete() }
    }

    private fun deleteLoginTokenUnlocked() {
        if (tokenFile.exists() && !tokenFile.delete()) throw SecurityFailure("storage")
        syncDirectory()
    }

    private fun writeNotifyBlobUnlocked(blob: ByteArray) {
        notifyCiphertext = null
        notifyPlain = null
        if (blob.isEmpty()) {
            deleteNotifyBlobUnlocked()
            return
        }
        write(notifyFile, NOTIFY_ALIAS, blob, unlockedOnly = false)
    }

    private fun deleteNotifyBlobUnlocked() {
        notifyCiphertext = null
        notifyPlain = null
        if (notifyFile.exists() && !notifyFile.delete()) throw SecurityFailure("storage")
        syncDirectory()
    }

    private fun SecurityStorageResultEvent.Builder.populateIdentityPubs(
        keys: Map<KeyId, StoredKey>,
    ): SecurityStorageResultEvent.Builder {
        val ed = keys[KeyId(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_ED.number, "")]
        val x = keys[KeyId(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_X.number, "")]
        setHasDeviceEd(ed != null)
        if (ed != null) setDeviceEdPub(ByteString.copyFrom(ed.publicKey))
        setHasDeviceX(x != null)
        if (x != null) setDeviceXPub(ByteString.copyFrom(x.publicKey))
        return this
    }

    private fun readBucketsUnlocked(): Map<BucketId, StoredBucket> {
        if (!bucketsFile.exists()) {
            bucketsCiphertext = null
            bucketsCache = null
            return emptyMap()
        }
        val data = bucketsFile.readBytes()
        if (Build.VERSION.SDK_INT >= 28 && keyguard.isDeviceLocked) clearMemoryCache()
        if (bucketsCiphertext?.contentEquals(data) == true) return bucketsCache ?: emptyMap()
        bucketsCiphertext = null
        bucketsCache = null
        val plain = decrypt(bucketsFile, ACCOUNT_ALIAS, data) ?: throw SecurityFailure("storage")
        onParse?.invoke(bucketsFile.name)
        val parsed = decodeBuckets(plain)
        bucketsCiphertext = data
        bucketsCache = parsed
        return parsed
    }

    private fun writeBucketsUnlocked(map: Map<BucketId, StoredBucket>) {
        bucketsCiphertext = null
        bucketsCache = null
        write(bucketsFile, ACCOUNT_ALIAS, encodeBuckets(map), unlockedOnly = true)
    }

    private fun readKeysUnlocked(): Map<KeyId, StoredKey> {
        if (!keysFile.exists()) {
            keysCiphertext = null
            keysCache = null
            return emptyMap()
        }
        val data = keysFile.readBytes()
        if (Build.VERSION.SDK_INT >= 28 && keyguard.isDeviceLocked) clearMemoryCache()
        if (keysCiphertext?.contentEquals(data) == true) return keysCache ?: emptyMap()
        keysCiphertext = null
        keysCache = null
        val plain = decrypt(keysFile, ACCOUNT_ALIAS, data) ?: throw SecurityFailure("storage")
        onParse?.invoke(keysFile.name)
        val parsed = decodeKeys(plain)
        keysCiphertext = data
        keysCache = parsed
        return parsed
    }

    private fun writeKeysUnlocked(map: Map<KeyId, StoredKey>) {
        keysCiphertext = null
        keysCache = null
        write(keysFile, ACCOUNT_ALIAS, encodeKeys(map), unlockedOnly = true)
    }

    private fun encodeBuckets(map: Map<BucketId, StoredBucket>): ByteArray {
        val out = ByteArrayOutputStream()
        DataOutputStream(out).use { s ->
            s.writeInt(CODEC_VERSION)
            s.writeInt(map.size)
            for (row in map.values) {
                s.writeInt(row.bucketNum)
                s.writeUTF(row.key)
                s.writeUTF(row.revision)
                s.writeInt(row.data.size)
                s.write(row.data)
            }
        }
        return out.toByteArray()
    }

    private fun decodeBuckets(plain: ByteArray): LinkedHashMap<BucketId, StoredBucket> {
        val map = LinkedHashMap<BucketId, StoredBucket>()
        DataInputStream(ByteArrayInputStream(plain)).use { s ->
            val ver = s.readInt()
            if (ver != CODEC_VERSION) throw SecurityFailure("storage")
            val count = s.readInt()
            repeat(count) {
                val b = s.readInt()
                val k = s.readUTF()
                val rev = s.readUTF()
                val len = s.readInt()
                val bytes = ByteArray(len).also { s.readFully(it) }
                map[BucketId(b, k)] = StoredBucket(b, k, rev, bytes)
            }
        }
        return map
    }

    private fun encodeKeys(map: Map<KeyId, StoredKey>): ByteArray {
        val out = ByteArrayOutputStream()
        DataOutputStream(out).use { s ->
            s.writeInt(CODEC_VERSION)
            s.writeInt(map.size)
            for ((id, row) in map) {
                s.writeInt(id.kindNum)
                s.writeUTF(id.slot)
                s.writeInt(row.privateKey.size)
                s.write(row.privateKey)
                s.writeInt(row.publicKey.size)
                s.write(row.publicKey)
            }
        }
        return out.toByteArray()
    }

    private fun decodeKeys(plain: ByteArray): LinkedHashMap<KeyId, StoredKey> {
        val map = LinkedHashMap<KeyId, StoredKey>()
        DataInputStream(ByteArrayInputStream(plain)).use { s ->
            val ver = s.readInt()
            if (ver != CODEC_VERSION) throw SecurityFailure("storage")
            val count = s.readInt()
            repeat(count) {
                val kind = s.readInt()
                val slot = s.readUTF()
                val privLen = s.readInt()
                val priv = ByteArray(privLen).also { s.readFully(it) }
                val pubLen = s.readInt()
                val pub = ByteArray(pubLen).also { s.readFully(it) }
                map[KeyId(kind, slot)] = StoredKey(priv, pub)
            }
        }
        return map
    }

    private fun syncDirectory(directory: File = dir) {
        val parent = Os.open(directory.absolutePath, OsConstants.O_RDONLY, 0)
        try {
            Os.fsync(parent)
        } finally {
            Os.close(parent)
        }
    }

    private fun read(f: File, alias: String): ByteArray? {
        if (!f.exists()) return null
        return decrypt(f, alias, f.readBytes())
    }

    private fun decrypt(f: File, alias: String, data: ByteArray): ByteArray? {
        if (data.size < 12) throw SecurityFailure("storage")
        val key = keyStore().getKey(alias, null) as? SecretKey ?: return null
        val c = Cipher.getInstance("AES/GCM/NoPadding")
        c.init(Cipher.DECRYPT_MODE, key, GCMParameterSpec(128, data, 0, 12))
        onDecrypt?.invoke(f.name)
        return c.doFinal(data, 12, data.size - 12)
    }

    private fun write(f: File, alias: String, plain: ByteArray, unlockedOnly: Boolean) {
        val key = keyStore().getKey(alias, null) as? SecretKey ?: newKey(alias, unlockedOnly)
        val c = Cipher.getInstance("AES/GCM/NoPadding")
        c.init(Cipher.ENCRYPT_MODE, key)
        val tmp = File(f.parentFile, f.name + ".tmp")
        try {
            FileOutputStream(tmp).use { out ->
                out.write(c.iv + c.doFinal(plain))
                out.fd.sync()
            }
            check(tmp.renameTo(f)) { "Unable to persist security file" }
            syncDirectory(f.parentFile!!)
        } finally {
            tmp.delete()
        }
    }

    private fun newKey(alias: String, unlockedOnly: Boolean): SecretKey {
        val b = KeyGenParameterSpec.Builder(
            alias,
            KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
        )
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
            .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
            .setKeySize(256)
        if (unlockedOnly && Build.VERSION.SDK_INT >= 28) b.setUnlockedDeviceRequired(true)
        val g = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
        g.init(b.build())
        return g.generateKey()
    }

    private fun keyStore() = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }

    companion object {
        private val SECURITY_LOCK = Any()
        private const val CODEC_VERSION = 1
        private const val ACCOUNT_ALIAS = "ra-account-unlocked-v2"
        private const val NOTIFY_ALIAS = "ra-notify"
        private val LEGACY_FILES = listOf(
            "account-v2.bin",
            "notify.bin",
            "login-token.bin",
            "security-v3.bin",
            "security-operation.bin",
            "registration-intent.bin",
            "security-v2.bin",
            "security-removals-v2.bin",
            "security-history-v2",
            "recovery-pending.bin",
            "security-v3.lock",
        )
    }
}
