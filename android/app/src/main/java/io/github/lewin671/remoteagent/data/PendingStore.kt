package io.github.lewin671.remoteagent.data

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import io.github.lewin671.remoteagent.core.hex
import io.github.lewin671.remoteagent.core.randomBytes
import io.github.lewin671.remoteagent.core.sha256
import java.io.File
import io.github.lewin671.remoteagent.core.ImageDiskPolicy
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
class PendingStore(context: Context) : io.github.lewin671.remoteagent.ui.SealedFiles {
    private val prefs = context.getSharedPreferences("image-storage", Context.MODE_PRIVATE)
    private val limit = ImageDiskPolicy.DRAFT_LIMIT
    val used: Long get() = synchronized(this) { ImageDiskPolicy.bytes(dir) }
    val expired: Int get() = prefs.getInt("expired-drafts", 0)
    @Synchronized fun expire(): Int {
        val n = ImageDiskPolicy.expireDrafts(dir)
        if (n > 0) prefs.edit().putInt("expired-drafts", expired + n).apply()
        return n
    }
    private val dir = File(context.noBackupFilesDir, "pending-images").apply { mkdirs() }
    private val keyFile = File(dir, "key.bin")
    // Read or made at first use; forgotten with everything else on logout,
    // so the next account's images are sealed under a key that is kept.
    @Volatile private var cachedKey: ByteArray? = null
    private val key: ByteArray
        get() = cachedKey ?: synchronized(this) { cachedKey ?: loadKey().also { cachedKey = it } }

    private fun folder(draft: String) = File(dir, sha256(draft.toByteArray()).hex().take(32)).apply { mkdirs() }

    @Synchronized override fun put(folder: String, name: String, data: ByteArray) {
        val draft = folder
        // Sealed first: the first use of the key may clear what it cannot open.
        expire()
        val sealed = seal(data)
        val f = File(folder(draft), name)
        // Keep each save's temporary file independent.
        val tmp = File(f.parentFile, "$name.${java.util.UUID.randomUUID()}.tmp")
        ImageDiskPolicy.requireRoom(dir, f, sealed.size.toLong(), limit)
        try {
            tmp.writeBytes(sealed)
            check(tmp.renameTo(f)) { "没能保存待发图片" }
        } finally { tmp.delete() }
        if (draft == "outbox" && name == "index") {
            // Protect only records named by the durable outbox index. A
            // failed save before that leaves ordinary expiring draft files.
            val protected = data.toString(Charsets.UTF_8).lineSequence().mapNotNull { line ->
                val parts = line.split("\t")
                if (parts.size != 2) null else sha256("out:${parts[0]}:${parts[1]}".toByteArray()).hex().take(32)
            }.toSet() + f.parentFile!!.name
            for (d in dir.listFiles()?.filter { it.isDirectory }.orEmpty()) {
                val marker = File(d, ".keep")
                if (d.name in protected) marker.createNewFile() else marker.delete()
            }
        }
        f.parentFile?.setLastModified(System.currentTimeMillis())
    }

    @Synchronized override fun get(folder: String, name: String): ByteArray? {
        expire()
        val f = File(folder(folder), name)
        if (!f.exists()) return null
        f.parentFile?.setLastModified(System.currentTimeMillis())
        return runCatching { open(f.readBytes()) }.getOrNull()
    }

    override @Synchronized fun remove(folder: String, name: String) {
        File(folder(folder), name).delete()
    }

    @Synchronized override fun names(folder: String): List<String> {
        expire()
        return folder(folder).list()?.filter { !it.endsWith(".tmp") }.orEmpty()
    }

    @Synchronized override fun clearFolder(folder: String) {
        folder(folder).deleteRecursively()
    }

    @Synchronized fun clearAll() {
        cachedKey?.fill(0)
        cachedKey = null
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
