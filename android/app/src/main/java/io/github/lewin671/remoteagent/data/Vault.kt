package io.github.lewin671.remoteagent.data

import android.app.KeyguardManager
import android.content.Context
import android.os.Build
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.system.Os
import android.system.OsConstants
import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.LoginToken
import io.github.lewin671.remoteagent.core.SecurityFailure
import io.github.lewin671.remoteagent.core.SecurityStoreBucketId
import io.github.lewin671.remoteagent.core.SecurityStoreKeyId
import io.github.lewin671.remoteagent.core.SecurityStoreStoredBucket
import io.github.lewin671.remoteagent.core.SecurityStoreStoredKey
import io.github.lewin671.remoteagent.core.SecurityStoreV4
import io.github.lewin671.remoteagent.core.SecurityStoreV4Backend
import io.github.lewin671.remoteagent.core.SecurityStoreV4Core
import io.github.lewin671.remoteagent.core.proto.SecurityEncapsulateRecipient
import io.github.lewin671.remoteagent.core.proto.SecurityEncapsulatedPair
import io.github.lewin671.remoteagent.core.proto.SecurityKeyKind
import io.github.lewin671.remoteagent.core.proto.SecurityStorageEffect
import io.github.lewin671.remoteagent.core.proto.SecurityStorageResultEvent
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.DataInputStream
import java.io.DataOutputStream
import java.io.File
import java.io.FileOutputStream
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * Keystore-backed S6 v4 security storage on Android:
 * - Persistent Ed25519/X25519 keys, v4 buckets, and login tokens are encrypted under
 *   [ACCOUNT_ALIAS] (unlocked-device required on API 28+).
 * - Notify record blob is mirrored to `security-v4-notify.bin` under [NOTIFY_ALIAS]
 *   (usable while locked) so FCM pushes can open in Core without unwrapping device identity keys.
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

    private val keyguard = context.getSystemService(KeyguardManager::class.java)
    private val dir = context.noBackupFilesDir
    private val bucketsFile = File(dir, "security-v4-buckets.bin")
    private val keysFile = File(dir, "security-v4-keys.bin")
    private val tokenFile = File(dir, "security-v4-token.bin")
    private val notifyFile = File(dir, "security-v4-notify.bin")
    private val upgradeNoticeFile = File(dir, "security-v4-upgrade-notice.flag")

    // Access under SECURITY_LOCK. Cache only ciphertexts that were decrypted and parsed cleanly.
    private var bucketsCiphertext: ByteArray? = null
    private var bucketsCache: LinkedHashMap<SecurityStoreBucketId, SecurityStoreStoredBucket>? = null
    private var keysCiphertext: ByteArray? = null
    private var keysCache: LinkedHashMap<SecurityStoreKeyId, SecurityStoreStoredKey>? = null
    private var notifyCiphertext: ByteArray? = null
    private var notifyPlain: ByteArray? = null

    private val core = SecurityStoreV4Core(
        backend = object : SecurityStoreV4Backend {
            override fun <T> transaction(block: () -> T): T = securityTransaction(block)
            override fun readBuckets() = readBucketsUnlocked()
            override fun writeBuckets(buckets: Map<SecurityStoreBucketId, SecurityStoreStoredBucket>) = writeBucketsUnlocked(buckets)
            override fun readKeys() = readKeysUnlocked()
            override fun writeKeys(keys: Map<SecurityStoreKeyId, SecurityStoreStoredKey>) = writeKeysUnlocked(keys)
            override fun readNotifyBlob(): ByteArray? = readNotifyBlobUnlocked()
            override fun writeNotifyBlob(blob: ByteArray) = writeNotifyBlobUnlocked(blob)
            override fun deleteNotifyBlob() = deleteNotifyBlobUnlocked()
            override fun deleteLoginToken() = deleteLoginTokenUnlocked()
            override fun clearUpgradeNotice() = clearUpgradeNoticeUnlocked()
            override fun detectAndClearLegacy(): Boolean = detectAndClearLegacyUnlocked()
            override fun clearAll() {
                clearAllUnlocked()
                onClearAll?.invoke()
            }
        },
        onLoadLoginToken = ::loadLoginTokenImpl,
        onSaveLoginToken = ::saveLoginTokenImpl,
        onClearLoginToken = ::clearLoginTokenImpl,
        onClearCache = { clearMemoryCache() },
    )

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

    override fun dismissUpgradeNotice() = core.dismissUpgradeNotice()

    private fun <T> securityTransaction(block: () -> T): T =
        synchronized(SECURITY_LOCK) {
            dir.mkdirs()
            FileOutputStream(File(dir, "security-v4.lock"), true).channel.use { channel ->
                channel.lock().use { block() }
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

    private fun loadLoginTokenImpl(server: String, deviceId: String): LoginToken? =
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

    private fun saveLoginTokenImpl(server: String, deviceId: String, token: LoginToken) =
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

    private fun clearLoginTokenImpl(server: String, deviceId: String) =
        securityTransaction {
            val existing = runCatching {
                val raw = read(tokenFile, ACCOUNT_ALIAS) ?: return@securityTransaction
                DataInputStream(ByteArrayInputStream(raw)).use { s -> s.readUTF() to s.readUTF() }
            }.getOrNull()
            if (existing == null || (existing.first == server && existing.second == deviceId)) {
                deleteLoginTokenUnlocked()
            }
        }

    private fun readNotifyBlobUnlocked(): ByteArray? =
        if (!notifyFile.exists()) {
            notifyCiphertext = null
            notifyPlain = null
            null
        } else {
            val data = notifyFile.readBytes()
            if (notifyCiphertext?.contentEquals(data) == true) {
                notifyPlain?.copyOf()
            } else {
                val plain = decrypt(notifyFile, NOTIFY_ALIAS, data)
                if (plain == null) {
                    null
                } else {
                    notifyCiphertext = data
                    notifyPlain = plain
                    plain.copyOf()
                }
            }
        }

    fun clear() = securityTransaction {
        core.clearEphemeralKeys()
        clearAllUnlocked()
        onClearAll?.invoke()
    }

    private fun clearAllUnlocked() {
        core.clearEphemeralKeys()
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

    private fun clearUpgradeNoticeUnlocked() {
        if (upgradeNoticeFile.exists()) {
            upgradeNoticeFile.delete()
            syncDirectory()
        }
    }

    private fun detectAndClearLegacyUnlocked(): Boolean {
        val hasLegacy = LEGACY_FILES.any { File(dir, it).exists() } ||
            (dir.listFiles()?.any { it.name.startsWith("pair-candidate-") } == true)
        if (!hasLegacy) {
            return upgradeNoticeFile.exists()
        }
        clearMemoryCache()
        core.clearEphemeralKeys()
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

    private fun readBucketsUnlocked(): Map<SecurityStoreBucketId, SecurityStoreStoredBucket> {
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

    private fun writeBucketsUnlocked(map: Map<SecurityStoreBucketId, SecurityStoreStoredBucket>) {
        bucketsCiphertext = null
        bucketsCache = null
        write(bucketsFile, ACCOUNT_ALIAS, encodeBuckets(map), unlockedOnly = true)
    }

    private fun readKeysUnlocked(): Map<SecurityStoreKeyId, SecurityStoreStoredKey> {
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

    private fun writeKeysUnlocked(map: Map<SecurityStoreKeyId, SecurityStoreStoredKey>) {
        keysCiphertext = null
        keysCache = null
        write(keysFile, ACCOUNT_ALIAS, encodeKeys(map), unlockedOnly = true)
    }

    private fun encodeBuckets(map: Map<SecurityStoreBucketId, SecurityStoreStoredBucket>): ByteArray {
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

    private fun decodeBuckets(plain: ByteArray): LinkedHashMap<SecurityStoreBucketId, SecurityStoreStoredBucket> {
        val map = LinkedHashMap<SecurityStoreBucketId, SecurityStoreStoredBucket>()
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
                map[SecurityStoreBucketId(b, k)] = SecurityStoreStoredBucket(b, k, rev, bytes)
            }
        }
        return map
    }

    private fun encodeKeys(map: Map<SecurityStoreKeyId, SecurityStoreStoredKey>): ByteArray {
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

    private fun decodeKeys(plain: ByteArray): LinkedHashMap<SecurityStoreKeyId, SecurityStoreStoredKey> {
        val map = LinkedHashMap<SecurityStoreKeyId, SecurityStoreStoredKey>()
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
                map[SecurityStoreKeyId(kind, slot)] = SecurityStoreStoredKey(priv, pub)
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
