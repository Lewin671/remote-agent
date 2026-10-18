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
    private val securityFile = File(dir, "security-v2.bin")

    fun hasAccount(): Boolean = accountFile.exists()

    fun load(): Account? {
        val blob = read(accountFile, ACCOUNT_ALIAS) ?: return null
        return DataInputStream(ByteArrayInputStream(blob)).use { s ->
            if (s.readInt() != 1) return null
            val server = s.readUTF()
            val deviceId = s.readUTF()
            val root = ByteArray(s.readInt()).also { s.readFully(it) }
            val seed = ByteArray(s.readInt()).also { s.readFully(it) }
            Account(server, root, seed, deviceId)
        }
    }

    fun save(a: Account, notifyKey: ByteArray) {
        val out = ByteArrayOutputStream()
        DataOutputStream(out).use { s ->
            s.writeInt(1)
            s.writeUTF(a.server)
            s.writeUTF(a.deviceId)
            s.writeInt(a.root.size)
            s.write(a.root)
            s.writeInt(a.deviceSeed.size)
            s.write(a.deviceSeed)
        }
        write(accountFile, ACCOUNT_ALIAS, out.toByteArray(), unlockedOnly = true)
        write(notifyFile, NOTIFY_ALIAS, notifyKey, unlockedOnly = false)
    }

    private fun <T> securityTransaction(block: () -> T): T =
        synchronized(SECURITY_LOCK) {
            FileOutputStream(File(dir, "security-v2.lock"), true).channel.use { channel ->
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
            refreshNotifyUnlocked(record)
        }
    }

    override fun reserveSecurity(expectedState: ByteArray): DeviceSecurity.Record =
        securityTransaction {
            val old = readSecurityUnlocked()
            if (!DeviceSecurity.stateHash(old.state).contentEquals(expectedState))
                throw DeviceSecurity.Failure("stale")
            val next = old.incremented()
            write(securityFile, ACCOUNT_ALIAS, next.bytes(), unlockedOnly = true)
            next
        }

    override fun advanceSecurity(
        expectedState: ByteArray,
        next: DeviceSecurity.State,
    ): DeviceSecurity.Record = securityTransaction {
        val old = readSecurityUnlocked()
        if (!DeviceSecurity.stateHash(old.state).contentEquals(expectedState))
            throw DeviceSecurity.Failure("stale")
        write(
            historyFile(old.state.core.epoch),
            ACCOUNT_ALIAS,
            DeviceSecurity.Historical(old.state, old.secret()).bytes(),
            unlockedOnly = true,
        )
        val record = old.advanced(next)
        write(securityFile, ACCOUNT_ALIAS, record.bytes(), unlockedOnly = true)
        refreshNotifyUnlocked(record)
        record
    }

    private fun refreshNotifyUnlocked(record: DeviceSecurity.Record) {
        val old =
            read(notifyFile, NOTIFY_ALIAS)?.let {
                runCatching { DeviceSecurity.NotifyRecord.parse(it) }.getOrNull()
            }
        if (
            old != null &&
                DeviceSecurity.stateHash(old.state)
                    .contentEquals(DeviceSecurity.stateHash(record.state))
        )
            return
        write(
            notifyFile,
            NOTIFY_ALIAS,
            DeviceSecurity.NotifyRecord.fromRecord(record).bytes(),
            unlockedOnly = false,
        )
    }

    private fun historyFile(epoch: ULong): File {
        if (epoch !in 1uL..32768uL) throw DeviceSecurity.Failure()
        return File(File(dir, "security-history-v2").also { it.mkdirs() }, "epoch-$epoch.bin")
    }

    override fun readSecurityHistory(epoch: ULong): DeviceSecurity.Historical? =
        securityTransaction {
            val current = readSecurityUnlocked()
            if (epoch > current.state.core.epoch) throw DeviceSecurity.Failure("untrusted")
            val data = read(historyFile(epoch), ACCOUNT_ALIAS) ?: return@securityTransaction null
            val history = DeviceSecurity.Historical.parse(data)
            if (history.state.core.epoch != epoch) throw DeviceSecurity.Failure("storage")
            DeviceSecurity.verifyState(current.pin, history.state)
            history
        }

    override fun cacheSecurityHistory(history: DeviceSecurity.Historical) {
        securityTransaction {
            val current = readSecurityUnlocked()
            if (history.state.core.epoch > current.state.core.epoch)
                throw DeviceSecurity.Failure("untrusted")
            DeviceSecurity.verifyState(current.pin, history.state)
            write(
                historyFile(history.state.core.epoch),
                ACCOUNT_ALIAS,
                history.bytes(),
                unlockedOnly = true,
            )
        }
    }

    override fun readRemovalOperations(): ByteArray? = securityTransaction {
        readSecurityUnlocked()
        read(File(dir, "security-removals-v2.bin"), ACCOUNT_ALIAS)
    }

    override fun writeRemovalOperations(data: ByteArray) {
        securityTransaction {
            readSecurityUnlocked()
            if (data.size > 16 * 1024 * 1024) throw DeviceSecurity.Failure("capacity")
            write(File(dir, "security-removals-v2.bin"), ACCOUNT_ALIAS, data, unlockedOnly = true)
        }
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
        File(dir, "security-removals-v2.bin").delete()
        File(dir, "security-history-v2").deleteRecursively()
        accountFile.delete()
        notifyFile.delete()
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
