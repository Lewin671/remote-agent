package io.github.lewin671.remoteagent.data

import android.content.Context
import io.github.lewin671.remoteagent.core.hex
import io.github.lewin671.remoteagent.core.sha256
import java.io.File
import io.github.lewin671.remoteagent.core.ImageDiskPolicy

/**
 * Display copies as fetched: sealed envelopes only, in the cache directory,
 * so a viewed image opens offline and nothing decrypted touches the disk.
 * The oldest go once they take more than [limit] bytes.
 */
class ImageEnvelopes(context: Context) {
    private val limit = ImageDiskPolicy.DEFAULT_LIMIT
    val used: Long get() = synchronized(this) { ImageDiskPolicy.bytes(dir) }

    private val dir = File(context.cacheDir, "image-envelopes").apply { mkdirs() }
    init { trim() }

    private fun file(key: String) = File(dir, sha256(key.toByteArray()).hex())

    @Synchronized fun get(key: String): ByteArray? = file(key).takeIf { it.exists() }?.let { f ->
        f.setLastModified(System.currentTimeMillis())
        f.readBytes()
    }

    @Synchronized fun put(key: String, envelope: ByteArray) {
        val f = file(key)
        val tmp = File(dir, f.name + ".tmp")
        try {
            tmp.writeBytes(envelope)
            check(tmp.renameTo(f)) { "没能保存图片缓存" }
        } finally { tmp.delete() }
        trim()
    }

    private fun trim() { ImageDiskPolicy.trimCache(dir, limit) }

    @Synchronized fun clear() {
        dir.listFiles()?.forEach { it.delete() }
    }
}
