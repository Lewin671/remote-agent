package io.github.lewin671.remoteagent.data

import android.content.Context
import android.os.Build
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import io.github.lewin671.remoteagent.core.Account
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.DataInputStream
import java.io.DataOutputStream
import java.io.File
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * Keeps the account's secrets on this device (architecture §15): a
 * non-exportable AES key in the Android Keystore encrypts the root key and
 * the device seed, stored in the app's no-backup directory. The notify key
 * is wrapped on its own, under a key usable while the phone is locked, so a
 * push can be decrypted in the background without unwrapping the root key.
 */
class Vault(context: Context) {
    private val dir = context.noBackupFilesDir
    private val accountFile = File(dir, "account.bin")
    private val notifyFile = File(dir, "notify.bin")

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

    /** The notify key, for decrypting pushes in the background. */
    fun notifyKey(): ByteArray? = read(notifyFile, NOTIFY_ALIAS)

    fun clear() {
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
        tmp.writeBytes(c.iv + c.doFinal(plain))
        tmp.renameTo(f)
    }

    private fun newKey(alias: String, unlockedOnly: Boolean): SecretKey {
        fun gen(unlocked: Boolean): SecretKey {
            val b = KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
            if (unlocked && Build.VERSION.SDK_INT >= 28) b.setUnlockedDeviceRequired(true)
            val g = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
            g.init(b.build())
            return g.generateKey()
        }
        return try {
            gen(unlockedOnly)
        } catch (e: Exception) {
            gen(false)
        }
    }

    private fun keyStore() = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }

    companion object {
        private const val ACCOUNT_ALIAS = "ra-account"
        private const val NOTIFY_ALIAS = "ra-notify"
    }
}
