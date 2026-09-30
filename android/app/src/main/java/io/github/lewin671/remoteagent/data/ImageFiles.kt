package io.github.lewin671.remoteagent.data

import android.content.Context
import io.github.lewin671.remoteagent.core.hex
import io.github.lewin671.remoteagent.core.sha256
import java.io.File

/**
 * Display copies as fetched: sealed envelopes only, in the cache directory,
 * so a viewed image opens offline and nothing decrypted touches the disk.
 * The oldest go once they take more than [limit] bytes.
 */
class ImageEnvelopes(context: Context, private val limit: Long = 150L * 1024 * 1024) {
    private val dir = File(context.cacheDir, "image-envelopes").apply { mkdirs() }

    private fun file(key: String) = File(dir, sha256(key.toByteArray()).hex())

    fun get(key: String): ByteArray? = file(key).takeIf { it.exists() }?.let { f ->
        f.setLastModified(System.currentTimeMillis())
        f.readBytes()
    }

    fun put(key: String, envelope: ByteArray) {
        val f = file(key)
        val tmp = File(dir, f.name + ".tmp")
        tmp.writeBytes(envelope)
        tmp.renameTo(f)
        trim()
    }

    private fun trim() {
        val files = dir.listFiles()?.sortedBy { it.lastModified() } ?: return
        var total = files.sumOf { it.length() }
        for (f in files) {
            if (total <= limit) break
            total -= f.length()
            f.delete()
        }
    }

    fun clear() {
        dir.listFiles()?.forEach { it.delete() }
    }
}
