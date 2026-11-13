package io.github.lewin671.remoteagent.data

import io.github.lewin671.remoteagent.core.sanitizeSharedFileName
import java.io.File
import java.io.IOException
import java.nio.ByteBuffer
import java.nio.charset.CharacterCodingException
import java.nio.charset.CodingErrorAction
import java.util.Locale

internal const val SharedTextMaxBytes = 1024 * 1024
private const val BinaryProbeBytes = 8 * 1024

private val TextExtensions = setOf(
    "md", "markdown", "txt", "log", "json", "yaml", "yml", "toml", "csv", "tsv", "xml", "ini", "conf", "env",
    "sh", "py", "js", "ts", "tsx", "jsx", "go", "kt", "swift", "java", "c", "h", "cpp", "rs", "rb", "sql", "diff", "patch", "html",
)

private fun extension(name: String) = sanitizeSharedFileName(name).substringAfterLast('.', "").lowercase(Locale.ROOT)

internal fun isSharedTextFile(name: String): Boolean = extension(name) in TextExtensions
internal fun isSharedMarkdownFile(name: String): Boolean = extension(name) in setOf("md", "markdown")

/** Called on IO; a growing file still cannot make this read more than the preview limit + 1. */
internal fun readSharedTextFile(file: File): String? {
    if (!file.isFile || file.length() > SharedTextMaxBytes) return null
    return try {
        val bytes = ByteArray(SharedTextMaxBytes + 1)
        val size = file.inputStream().use { input ->
            var count = 0
            while (count < bytes.size) {
                val read = input.read(bytes, count, bytes.size - count)
                if (read < 0) break
                count += read
            }
            count
        }
        if (size > SharedTextMaxBytes || (0 until minOf(size, BinaryProbeBytes)).any { bytes[it] == 0.toByte() }) return null
        Charsets.UTF_8.newDecoder()
            .onMalformedInput(CodingErrorAction.REPORT)
            .onUnmappableCharacter(CodingErrorAction.REPORT)
            .decode(ByteBuffer.wrap(bytes, 0, size)).toString().removePrefix("\uFEFF")
    } catch (_: CharacterCodingException) {
        null
    } catch (_: IOException) {
        null
    }
}
