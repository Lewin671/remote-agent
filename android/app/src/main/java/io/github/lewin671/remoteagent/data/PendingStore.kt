package io.github.lewin671.remoteagent.data

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import io.github.lewin671.remoteagent.core.hex
import io.github.lewin671.remoteagent.core.randomBytes
import io.github.lewin671.remoteagent.core.sha256
import java.io.File
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.SecretKeySpec

/**
 * Images waiting to be sent, kept encrypted on disk so that the system
 * reclaiming the process, or the picker's grant running out, does not lose
 * them. A random file key encrypts them in process (images are too large
 * for the Keystore to handle quickly); the Keystore wraps that key. Nothing
 * here goes into backups, and logging out deletes it all.
 */
class PendingStore(context: Context) {
    private val dir = File(context.noBackupFilesDir, "pending-images").apply { mkdirs() }
    private val keyFile = File(dir, "key.bin")
    private val key: ByteArray by lazy { loadKey() }

    private fun folder(draft: String) = File(dir, sha256(draft.toByteArray()).hex().take(32)).apply { mkdirs() }

    fun put(draft: String, name: String, data: ByteArray) {
        val f = File(folder(draft), name)
        val tmp = File(f.parentFile, "$name.tmp")
        tmp.writeBytes(seal(data))
        tmp.renameTo(f)
    }

    fun get(draft: String, name: String): ByteArray? = File(folder(draft), name).takeIf { it.exists() }?.let { runCatching { open(it.readBytes()) }.getOrNull() }

    fun remove(draft: String, name: String) {
        File(folder(draft), name).delete()
    }

    fun names(draft: String): List<String> = folder(draft).list()?.filter { !it.endsWith(".tmp") }.orEmpty()

    fun clearDraft(draft: String) {
        folder(draft).deleteRecursively()
    }

    fun clearAll() {
        dir.listFiles()?.forEach { if (it != keyFile) it.deleteRecursively() }
        keyFile.delete()
        val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        if (ks.containsAlias(ALIAS)) ks.deleteEntry(ALIAS)
    }

    private fun seal(plain: ByteArray): ByteArray {
        val iv = randomBytes(12)
        val c = Cipher.getInstance("AES/GCM/NoPadding")
        c.init(Cipher.ENCRYPT_MODE, SecretKeySpec(key, "AES"), GCMParameterSpec(128, iv))
        return iv + c.doFinal(plain)
    }

    private fun open(data: ByteArray): ByteArray {
        val c = Cipher.getInstance("AES/GCM/NoPadding")
        c.init(Cipher.DECRYPT_MODE, SecretKeySpec(key, "AES"), GCMParameterSpec(128, data, 0, 12))
        return c.doFinal(data, 12, data.size - 12)
    }

    private fun loadKey(): ByteArray {
        val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        val wrap = ks.getKey(ALIAS, null) as? SecretKey
        if (wrap != null && keyFile.exists()) {
            runCatching {
                val d = keyFile.readBytes()
                val c = Cipher.getInstance("AES/GCM/NoPadding")
                c.init(Cipher.DECRYPT_MODE, wrap, GCMParameterSpec(128, d, 0, 12))
                return c.doFinal(d, 12, d.size - 12)
            }
        }
        // No key, or it does not open: what was stored under it is lost.
        dir.listFiles()?.forEach { if (it != keyFile) it.deleteRecursively() }
        val w = wrap ?: KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore").run {
            init(
                KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                    .setBlockModes(KeyProperties.BLOCK_MODE_GCM).setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE).setKeySize(256).build(),
            )
            generateKey()
        }
        val k = randomBytes(32)
        val c = Cipher.getInstance("AES/GCM/NoPadding")
        c.init(Cipher.ENCRYPT_MODE, w)
        keyFile.writeBytes(c.iv + c.doFinal(k))
        return k
    }

    companion object {
        private const val ALIAS = "ra-pending"
    }
}
