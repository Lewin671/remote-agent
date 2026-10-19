package io.github.lewin671.remoteagent.data

import android.content.Context
import android.os.Build
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.system.Os
import android.system.OsConstants
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.DeviceSecurity
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
 * Keeps the account's secrets on this device (architecture §15): a non-exportable AES key in the
 * Android Keystore encrypts the root key and the device seed, stored in the app's no-backup
 * directory. The notify key is wrapped on its own, under a key usable while the phone is locked, so
 * a push can be decrypted in the background without unwrapping the root key. Android 8 (API 26/27)
 * lacks the unlocked-device Keystore policy; the foreground-only account loading rule is the
 * protection there.
 */
class Vault(context: Context) : DeviceSecurity.Storage {
    private val dir = context.noBackupFilesDir
    private val accountFile = File(dir, "account-v2.bin")
    private val notifyFile = File(dir, "notify.bin")
    private val securityFile = File(dir, "security-v3.bin")

    fun hasAccount(): Boolean = accountFile.exists()

    fun load(): Account? {
        val blob = read(accountFile, ACCOUNT_ALIAS) ?: return null
        return DataInputStream(ByteArrayInputStream(blob)).use { s ->
            val version = s.readInt()
            if (version !in 1..2) return null
            val server = s.readUTF()
            val deviceId = s.readUTF()
            val root = ByteArray(s.readInt()).also { s.readFully(it) }
            val seed = ByteArray(s.readInt()).also { s.readFully(it) }
            if (version == 1) Account(server, root, seed, deviceId)
            else {
                val accountId = s.readUTF()
                val publicKey = ByteArray(s.readInt()).also { s.readFully(it) }
                Account(server, root, seed, deviceId, accountId, publicKey)
            }
        }
    }

    fun save(a: Account, notifyKey: ByteArray) {
        val out = ByteArrayOutputStream()
        DataOutputStream(out).use { s ->
            s.writeInt(2)
            s.writeUTF(a.server)
            s.writeUTF(a.deviceId)
            s.writeInt(a.root.size)
            s.write(a.root)
            s.writeInt(a.deviceSeed.size)
            s.write(a.deviceSeed)
            s.writeUTF(a.accountId)
            s.writeInt(a.accountPublicKey.size)
            s.write(a.accountPublicKey)
        }
        write(accountFile, ACCOUNT_ALIAS, out.toByteArray(), unlockedOnly = true)
        write(notifyFile, NOTIFY_ALIAS, notifyKey, unlockedOnly = false)
    }

    private fun <T> securityTransaction(block: () -> T): T =
        synchronized(SECURITY_LOCK) {
            FileOutputStream(File(dir, "security-v3.lock"), true).channel.use { channel ->
                channel.lock().use { block() }
            }
        }

    fun hasSecurityState(): Boolean = securityTransaction { securityFile.exists() }

    private fun readSecurityUnlocked(): DeviceSecurity.Record =
        DeviceSecurity.Record.parse(
            read(securityFile, ACCOUNT_ALIAS) ?: throw DeviceSecurity.Failure("storage"),
        )

    override fun readSecurity(): DeviceSecurity.Record = securityTransaction {
        readSecurityUnlocked().also { refreshNotifyUnlocked(it) }
    }

    override fun initializeSecurity(record: DeviceSecurity.Record) {
        securityTransaction {
            if (securityFile.exists()) throw DeviceSecurity.Failure("conflict")
            write(securityFile, ACCOUNT_ALIAS, record.bytes(), unlockedOnly = true)
            write(notifyFile, NOTIFY_ALIAS, DeviceSecurity.NotifyRecord.fromRecord(record).bytes(), unlockedOnly = false)
        }
    }

    override fun reserveSecurity(expectedState: ByteArray): DeviceSecurity.Record =
        securityTransaction {
            val old = readSecurityUnlocked()
            if (!DeviceSecurity.stateHash(old.head).contentEquals(expectedState))
                throw DeviceSecurity.Failure("stale")
            val next = old.incremented()
            write(securityFile, ACCOUNT_ALIAS, next.bytes(), unlockedOnly = true)
            next
        }

    override fun installSecurity(
        expectedState: ByteArray,
        states: List<DeviceSecurity.State>,
        proofs: List<ByteArray?>,
    ): DeviceSecurity.Record = securityTransaction {
        val old = readSecurityUnlocked()
        if (!DeviceSecurity.stateHash(old.head).contentEquals(expectedState))
            throw DeviceSecurity.Failure("stale")
        val record = old.installed(states, proofs)
        write(securityFile, ACCOUNT_ALIAS, record.bytes(), unlockedOnly = true)
        refreshNotifyUnlocked(record)
        record
    }

    override fun touchSecurity(expectedState: ByteArray, floor: ULong): DeviceSecurity.Record =
        securityTransaction {
            val old = readSecurityUnlocked()
            if (!DeviceSecurity.stateHash(old.head).contentEquals(expectedState))
                throw DeviceSecurity.Failure("stale")
            val next = old.touched(floor)
            if (next === old) return@securityTransaction old
            write(securityFile, ACCOUNT_ALIAS, next.bytes(), unlockedOnly = true)
            next
        }

    override fun cacheSecurityKey(expectedState: ByteArray, epoch: ULong, secret: ByteArray) {
        securityTransaction {
            val old = readSecurityUnlocked()
            if (!DeviceSecurity.stateHash(old.head).contentEquals(expectedState))
                throw DeviceSecurity.Failure("stale")
            if (epoch > old.head.core.contentEpoch) throw DeviceSecurity.Failure("untrusted")
            if (old.cachedKeys().containsKey(epoch)) return@securityTransaction
            val next = old.withKey(epoch, secret)
            write(securityFile, ACCOUNT_ALIAS, next.bytes(), unlockedOnly = true)
        }
    }

    private fun refreshNotifyUnlocked(record: DeviceSecurity.Record) {
        if (!record.member) {
            // A removed device must not keep a key for the new content epoch.
            notifyFile.delete()
            return
        }
        val old =
            read(notifyFile, NOTIFY_ALIAS)?.let {
                runCatching { DeviceSecurity.NotifyRecord.parse(it) }.getOrNull()
            }
        if (
            old != null &&
                DeviceSecurity.stateHash(old.state)
                    .contentEquals(DeviceSecurity.stateHash(record.head))
        )
            return
        write(
            notifyFile,
            NOTIFY_ALIAS,
            DeviceSecurity.NotifyRecord.fromRecord(record).bytes(),
            unlockedOnly = false,
        )
    }

    /** The identity that submitted a pending recovery, so an app restart can finish it. */
    fun savePendingRecovery(
        recoveryId: String,
        deviceSeed: ByteArray,
        xPrivate: ByteArray,
        stateHash: ByteArray,
    ) {
        securityTransaction {
            if (deviceSeed.size != 32 || xPrivate.size != 32 || stateHash.size != 32)
                throw DeviceSecurity.Failure()
            val out = ByteArrayOutputStream()
            DataOutputStream(out).use { s ->
                s.writeInt(2)
                s.writeUTF(recoveryId)
                s.writeInt(deviceSeed.size)
                s.write(deviceSeed)
                s.writeInt(xPrivate.size)
                s.write(xPrivate)
                s.write(stateHash)
            }
            write(File(dir, "recovery-pending.bin"), ACCOUNT_ALIAS, out.toByteArray(), unlockedOnly = true)
        }
    }

    data class PendingRecovery(
        val deviceSeed: ByteArray,
        val xPrivate: ByteArray,
        val stateHash: ByteArray,
    )

    fun loadPendingRecovery(recoveryId: String): PendingRecovery? = securityTransaction {
        val blob = read(File(dir, "recovery-pending.bin"), ACCOUNT_ALIAS) ?: return@securityTransaction null
        DataInputStream(ByteArrayInputStream(blob)).use { s ->
            if (s.readInt() != 2 || s.readUTF() != recoveryId) return@securityTransaction null
            val seed = ByteArray(s.readInt()).also { s.readFully(it) }
            val x = ByteArray(s.readInt()).also { s.readFully(it) }
            val hash = ByteArray(32).also { s.readFully(it) }
            if (seed.size != 32 || x.size != 32) null
            else PendingRecovery(seed, x, hash)
        }
    }

    fun clearPendingRecovery() = securityTransaction {
        File(dir, "recovery-pending.bin").delete()
    }

    private fun syncDirectory(directory: File = dir) {
        val parent = Os.open(directory.absolutePath, OsConstants.O_RDONLY, 0)
        try {
            Os.fsync(parent)
        } finally {
            Os.close(parent)
        }
    }

    /** The notify key, for decrypting pushes in the background. */
    fun notifyKey(): ByteArray? = read(notifyFile, NOTIFY_ALIAS)

    fun clear() = securityTransaction {
        securityFile.delete()
        File(dir, "security-v2.bin").delete()
        File(dir, "security-removals-v2.bin").delete()
        File(dir, "security-history-v2").deleteRecursively()
        accountFile.delete()
        notifyFile.delete()
        File(dir, "recovery-pending.bin").delete()
        val ks = keyStore()
        for (a in listOf(ACCOUNT_ALIAS, NOTIFY_ALIAS)) if (ks.containsAlias(a)) ks.deleteEntry(a)
    }

    private fun read(f: File, alias: String): ByteArray? {
        if (!f.exists()) return null
        val key = keyStore().getKey(alias, null) as? SecretKey ?: return null
        val data = f.readBytes()
        val c = Cipher.getInstance("AES/GCM/NoPadding")
        c.init(Cipher.DECRYPT_MODE, key, GCMParameterSpec(128, data, 0, 12))
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
            check(tmp.renameTo(f)) { "Unable to persist account key" }
            syncDirectory(f.parentFile!!)
        } finally {
            tmp.delete()
        }
    }

    private fun newKey(alias: String, unlockedOnly: Boolean): SecretKey {
        fun gen(unlocked: Boolean): SecretKey {
            val b =
                KeyGenParameterSpec.Builder(
                        alias,
                        KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
                    )
                    .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                    .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                    .setKeySize(256)
            if (unlocked && Build.VERSION.SDK_INT >= 28) b.setUnlockedDeviceRequired(true)
            val g = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
            g.init(b.build())
            return g.generateKey()
        }
        return gen(unlockedOnly)
    }

    private fun keyStore() = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }

    companion object {
        private val SECURITY_LOCK = Any()
        private const val ACCOUNT_ALIAS = "ra-account-unlocked-v2"
        private const val NOTIFY_ALIAS = "ra-notify"
    }
}
