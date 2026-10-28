package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.ImageUploadState
import io.github.lewin671.remoteagent.proto.SentFile
import kotlinx.coroutines.ensureActive
import java.io.InputStream
import java.security.MessageDigest
import kotlin.coroutines.coroutineContext

const val MAX_FILES = 6
const val MAX_FILE_BYTES = 100L * 1024 * 1024
fun filesSupported(m: MachineView?) = capabilities(m)?.fileUpload == true
fun maxFiles(m: MachineView?) = capabilities(m)?.maxFilesPerMessage?.takeIf { it > 0 }?.coerceAtMost(MAX_FILES) ?: MAX_FILES
fun maxFileBytes(m: MachineView?) = capabilities(m)?.maxFileUploadBytes?.takeIf { it > 0 }?.coerceAtMost(MAX_FILE_BYTES) ?: MAX_FILE_BYTES
fun fileLimitError(size: Long, count: Int, max: Int, maxBytes: Long): String? = when {
    count >= max -> "一条消息最多 $max 个文件"
    size < 0 -> "无法读取文件大小，请重新选择文件"
    size > maxBytes -> "文件过大，单个文件最多 ${fileSize(maxBytes)}"
    else -> null
}
fun fileSize(bytes: Long): String = when {
    bytes < 1024 -> "$bytes B"
    bytes < 1024 * 1024 -> java.lang.String.format(java.util.Locale.ROOT, "%.1f KB", bytes / 1024.0)
    else -> java.lang.String.format(java.util.Locale.ROOT, "%.1f MB", bytes / 1024.0 / 1024.0)
}

/** Code point based middle truncation; the tail always contains the extension. */
fun fileChipName(name: String, max: Int = 32): String {
    val chars = name.codePoints().toArray()
    if (chars.size <= max || max < 3) return name
    val extension = name.substringAfterLast('.', "").codePointCount(0, name.substringAfterLast('.', "").length) + 1
    val tail = maxOf((max - 1) / 2, extension).coerceAtMost(max - 2)
    return String(chars, 0, max - 1 - tail) + "…" + String(chars, chars.size - tail, tail)
}

/** Only attested complete reference lines and their preceding blank separator are hidden. */
fun withoutFileLines(text: String, files: List<SentFile>): String {
    val paths = files.map { it.path }.filter { it.isNotEmpty() }.toSet()
    val lines = text.split('\n')
    val hidden = HashSet<Int>()
    lines.forEachIndexed { i, line ->
        val path = when {
            line.startsWith("File: @") && line.endsWith(" (file)") -> line.substring(6, line.length - 7)
            line.startsWith("File: ") -> line.drop(6)
            else -> line
        }
        if (path in paths || path.startsWith('@') && path.drop(1) in paths) {
            hidden += i
            if (i > 0 && lines[i - 1].isEmpty()) hidden += i - 1
        }
    }
    return lines.filterIndexed { i, _ -> i !in hidden }.joinToString("\n")
}

data class UploadedFile(val fileId: String, val expiresMs: Long)

/** Two streaming passes. Only one chunk is held; reopen at the durable offset after an unknown result. */
suspend fun uploadFile(
    ch: UploadChannel, transferId: String, name: String, total: Long,
    open: () -> InputStream, maxChunk: Int = DEFAULT_CHUNK_BYTES, retries: Int = 3,
    onProgress: (Long, Long) -> Unit = { _, _ -> },
): UploadedFile {
    require(name.isNotBlank() && name.toByteArray().size <= 255) { "文件名过长或为空，请重命名后重新选择" }
    require(total in 0..MAX_FILE_BYTES) { "文件过大，单个文件最多 100 MB" }
    val digest = MessageDigest.getInstance("SHA-256")
    open().use { input ->
        val buffer = ByteArray(64 * 1024)
        var read = 0L
        while (true) {
            coroutineContext.ensureActive()
            val n = input.read(buffer)
            if (n < 0) break
            read += n
            require(read <= total) { "文件已改变，请重新选择文件" }
            digest.update(buffer, 0, n)
        }
        require(read == total) { "文件已改变，请重新选择文件" }
    }
    val hash = digest.digest()
    var tries = 0
    suspend fun <T> step(block: suspend () -> T, resync: suspend () -> Unit): T {
        while (true) try { return block() } catch (e: CommandException) {
            if ((!e.unknown && e.code != "network") || tries++ >= retries) throw e
            resync()
        }
    }
    var st = step({ ch.beginFile(transferId, name, total, hash) }) {}
    val chunk = minOf(maxChunk, DEFAULT_CHUNK_BYTES, st.maxChunkBytes.takeIf { it > 0 } ?: DEFAULT_CHUNK_BYTES)
    require(chunk > 0)
    var source: InputStream? = null
    var cursor = 0L
    try {
        while (st.state == ImageUploadState.IMAGE_UPLOAD_STATE_RECEIVING && st.persistedBytes < total) {
            coroutineContext.ensureActive()
            val offset = st.persistedBytes
            require(offset in 0..total) { "电脑返回的文件进度无效" }
            if (source == null || cursor > offset) {
                source?.close()
                source = open()
                cursor = 0
            }
            val input = checkNotNull(source)
            while (cursor < offset) {
                coroutineContext.ensureActive()
                val n = input.skip(offset - cursor)
                if (n == 0L) { check(input.read() >= 0) { "无法读取源文件，请重新选择文件" }; cursor++ } else cursor += n
            }
            val data = ByteArray(minOf(chunk.toLong(), total - offset).toInt())
            var n = 0
            while (n < data.size) {
                coroutineContext.ensureActive()
                val got = input.read(data, n, data.size - n)
                check(got >= 0) { "无法读取源文件，请重新选择文件" }
                n += got
            }
            cursor += n
            onProgress(offset, total)
            st = step({ ch.put(transferId, offset, data) }) { st = ch.status(transferId) }
        }
    } finally { source?.close() }
    if (st.state == ImageUploadState.IMAGE_UPLOAD_STATE_RECEIVING) st = step({ ch.complete(transferId) }) { st = ch.status(transferId) }
    if (st.fileId.isEmpty()) throw CommandException("internal", "电脑没有返回文件 ID")
    onProgress(total, total)
    return UploadedFile(st.fileId, st.expiresMs)
}
