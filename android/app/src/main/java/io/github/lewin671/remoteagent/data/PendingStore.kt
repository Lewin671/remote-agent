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
    private val scopes = FileScopes(this)
    fun scoped() = scopes.scoped()
    fun invalidate() = scopes.invalidate()

    private val prefs = context.getSharedPreferences("image-storage", Context.MODE_PRIVATE)
    private val limit = ImageDiskPolicy.DRAFT_LIMIT
    val used: Long get() = synchronized(this) { ImageDiskPolicy.bytes(dir) }
    val expired: Int get() = prefs.getInt("expired-drafts", 0)
    @Synchronized fun expire(): Int {
        // An index that is there and does not open says nothing about what to keep: nothing goes.
        val keep = outboxFolders() ?: return 0
        val n = ImageDiskPolicy.expireDrafts(dir, keep)
        if (n > 0) prefs.edit().putInt("expired-drafts", expired + n).apply()
        return n
    }

    /**
     * The folders that do not expire: the outbox's own, and those of the
     * messages its index names, which wait to be confirmed or removed however
     * long that takes. Read from the index every time, which is what says a
     * message is waiting: a mark left beside each folder could be missing
     * after a full disk or a process that ended in between. Null when the
     * index is there and does not open.
     */
    private fun outboxFolders(): Set<String>? {
        val index = File(File(dir, folderName("outbox")), "index")
        if (!index.exists()) return emptySet()
        val k = cachedKey ?: storedKey()?.also { cachedKey = it } ?: return null
        val text = runCatching { open(index.readBytes(), k).toString(Charsets.UTF_8) }.getOrNull() ?: return null
        return ImageDiskPolicy.outboxKeys(text).mapTo(mutableSetOf(folderName("outbox"))) { folderName(it) }
    }
    private val dir = File(context.noBackupFilesDir, "pending-images").apply { mkdirs() }
    private val keyFile = File(dir, "key.bin")
    // Read or made at first use; forgotten with everything else on logout,
    // so the next account's images are sealed under a key that is kept.
    @Volatile private var cachedKey: ByteArray? = null
    private val key: ByteArray
        get() = cachedKey ?: synchronized(this) { cachedKey ?: loadKey().also { cachedKey = it } }

    /** A draft's folder is named by a hash of its key, which may hold a path or a session ID. */
    private fun folderName(draft: String) = sha256(draft.toByteArray()).hex().take(32)

    private fun folder(draft: String) = File(dir, folderName(draft)).apply { mkdirs() }

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

    @Synchronized override fun clearFolder(folder: String) {
        folder(folder).deleteRecursively()
    }

    @Synchronized fun clearAll() {
        invalidate()
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

    private fun open(data: ByteArray, k: ByteArray = key): ByteArray {
        val c = Cipher.getInstance("AES/GCM/NoPadding")
        c.init(Cipher.DECRYPT_MODE, SecretKeySpec(k, "AES"), GCMParameterSpec(128, data, 0, 12))
        return c.doFinal(data, 12, data.size - 12)
    }

    private fun wrappingKey(): SecretKey? = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }.getKey(ALIAS, null) as? SecretKey

    /** The key there is, without making one; null when there is none or it does not open. */
    private fun storedKey(wrap: SecretKey? = wrappingKey()): ByteArray? {
        if (wrap == null || !keyFile.exists()) return null
        return runCatching {
            val d = keyFile.readBytes()
            val c = Cipher.getInstance("AES/GCM/NoPadding")
            c.init(Cipher.DECRYPT_MODE, wrap, GCMParameterSpec(128, d, 0, 12))
            c.doFinal(d, 12, d.size - 12)
        }.getOrNull()
    }

    private fun loadKey(): ByteArray {
        val wrap = wrappingKey()
        storedKey(wrap)?.let { return it }
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
