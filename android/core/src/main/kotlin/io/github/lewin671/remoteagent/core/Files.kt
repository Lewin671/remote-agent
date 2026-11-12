package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.ImageUploadState
import io.github.lewin671.remoteagent.proto.SentFile
import kotlinx.coroutines.ensureActive
import java.io.InputStream
import java.security.MessageDigest
import kotlin.coroutines.coroutineContext

const val MAX_FILES = 6
const val MAX_FILE_BYTES = 100L * 1024 * 1024
val FILE_UPLOAD_UPDATE_REQUIRED = UiMessages.attachmentUpdateRequired().messageText()
fun filesSupported(m: MachineView?) = capabilities(m)?.fileUpload == true
fun maxFiles(m: MachineView?) = capabilities(m)?.maxFilesPerMessage?.takeIf { it > 0 }?.coerceAtMost(MAX_FILES) ?: MAX_FILES
fun maxFileBytes(m: MachineView?) = capabilities(m)?.maxFileUploadBytes?.takeIf { it > 0 }?.coerceAtMost(MAX_FILE_BYTES) ?: MAX_FILE_BYTES
fun fileLimitError(size: Long, count: Int, max: Int, maxBytes: Long): MessageText? = when {
    count >= max -> UiMessages.attachmentMaxFiles((max).toInt()).messageText()
    size < 0 -> UiMessages.errorsAndroidCannotReadFileSizeSelectTheFileAgain().messageText()
    size > maxBytes -> UiMessages.errorsAndroidFileTooLargeMaximumSizePerFileValue1((fileSize(maxBytes)).toString()).messageText()
    else -> null
}
/**
 * Videos picked from the library go as files, beside the images
 * (session-files 「从相册选视频」). A picked item is one by its type, or,
 * when that is not known, by its name.
 */
fun isVideo(mime: String?, name: String): Boolean =
    if (mime != null) mime.startsWith("video/") else name.substringAfterLast('.', "").lowercase() in VIDEO_EXTENSIONS

private val VIDEO_EXTENSIONS = setOf("mp4", "m4v", "mov", "3gp", "3g2", "webm", "mkv", "avi", "mpg", "mpeg", "ts", "wmv", "flv")

/** Why a video picked from the library is turned away as it is picked: it is over the size limit. Null when it is taken. */
fun pickedVideoError(name: String, size: Long, maxBytes: Long): MessageText? =
    if (size > maxBytes) UiMessages.attachmentNamedSizeLimit((maxBytes / 1024 / 1024).toInt(), (name).toString()).messageText() else null

/**
 * How many more items the library may give: images while they fit, and,
 * when the computer takes files ([videos]), videos while files fit.
 */
fun libraryRoom(images: Int, maxImages: Int, files: Int, maxFiles: Int, videos: Boolean): Int =
    (maxImages - images).coerceAtLeast(0) + if (videos) (maxFiles - files).coerceAtLeast(0) else 0

/** A video's length in the corner of its thumbnail: 0:42, 12:05, 1:02:03. */
fun videoLength(ms: Long): String {
    val total = (ms.coerceAtLeast(0) + 500) / 1000
    val h = total / 3600
    val m = total / 60 % 60
    val sec = total % 60
    return if (h > 0) String.format(java.util.Locale.ROOT, "%d:%02d:%02d", h, m, sec) else String.format(java.util.Locale.ROOT, "%d:%02d", m, sec)
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
            line.startsWith("File: @\"") && line.endsWith("\" (file)") -> "@" + line.substring(8, line.length - 8)
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

/** IO from the provider is distinguished from network IO by where it happened. */
class FileSourceException(cause: Exception) : MessageException(UiMessages.errorsAndroidCannotReadTheSourceFileRemoveItAndSelectThe().messageText(), cause)
class FileContentChanged : MessageException(UiMessages.errorsAndroidTheFileChangedDuringUploadRetryOrSelectTheFile().messageText())
fun isUploadHashMismatch(e: Throwable) = e is CommandException && e.code == "invalid_argument" && e.message.orEmpty().contains("hash", ignoreCase = true)
fun restartUpload(e: Throwable) = e is CommandException && (e.code == "already_exists" || e.code == "not_found" || isUploadHashMismatch(e))
fun uploadErrorText(e: Throwable): MessageText = when {
    e is FileSourceException || e is FileContentChanged -> errorText(e)
    isUploadHashMismatch(e) -> UiMessages.errorsAndroidFileVerificationFailedRetryOrSelectTheFileAgain().messageText()
    e is CommandException && e.code == "already_exists" -> UiMessages.errorsAndroidAttachmentTransferContentChangedRetry().messageText()
    e is CommandException && e.code == "aborted" -> UiMessages.errorsAndroidAttachmentTransferStoppedOrChangedRetry().messageText()
    e is java.io.IOException -> UiMessages.errorsAndroidUploadConnectionInterruptedRetry().messageText()
    else -> errorText(e)
}

/** One file's preparation and network transfer across every draft. Canceling a waiter releases its place. */
object FileUploads {
    private val lock = kotlinx.coroutines.sync.Mutex()
    suspend fun <T> serial(work: suspend () -> T): T {
        lock.lock()
        try { return work() } finally { lock.unlock() }
    }
}

/** Metadata size is only a hint. Hash the actual bytes, then verify the upload pass against them. */
suspend fun uploadFile(
    ch: UploadChannel, transferId: String, name: String, total: Long,
    open: () -> InputStream, maxChunk: Int = DEFAULT_CHUNK_BYTES, retries: Int = 3,
    maxBytes: Long = MAX_FILE_BYTES,
    onPrepared: (Long) -> Unit = {},
    onProgress: (Long, Long) -> Unit = { _, _ -> },
): UploadedFile {
    requireMessage(name.isNotBlank() && name.toByteArray().size <= 255) { UiMessages.errorsAndroidFileNameIsTooLongOrEmptyRenameTheFile().messageText() }
    fun <T> source(work: () -> T): T = try { work() } catch (e: java.io.IOException) { throw FileSourceException(e) } catch (e: SecurityException) { throw FileSourceException(e) }
    fun openSource(): InputStream = object : java.io.FilterInputStream(source { open() }) {
        override fun close() { try { super.close() } catch (e: java.io.IOException) { throw FileSourceException(e) } }
    }
    val digest = MessageDigest.getInstance("SHA-256")
    var actual = 0L
    openSource().use { input ->
        val buffer = ByteArray(64 * 1024)
        while (true) {
            coroutineContext.ensureActive()
            val n = source { input.read(buffer) }
            if (n < 0) break
            actual += n
            requireMessage(actual <= minOf(maxBytes, MAX_FILE_BYTES)) { UiMessages.errorsAndroidFileTooLargeMaximumSizePerFileValue1((fileSize(minOf(maxBytes, MAX_FILE_BYTES))).toString()).messageText() }
            digest.update(buffer, 0, n)
        }
    }
    onPrepared(actual)
    val hash = digest.digest()
    var tries = 0
    suspend fun <T> step(block: suspend () -> T, resync: suspend () -> Unit): T {
        while (true) try { return block().also { tries = 0 } } catch (e: CommandException) {
            if (e.code == "aborted" && e.message.orEmpty().contains("transfer changed")) throw e
            if ((!e.unknown && e.code != "network") || tries++ >= retries) throw e
            resync()
        }
    }
    var st = step({ ch.beginFile(transferId, name, actual, hash) }) {}
    val chunk = minOf(maxChunk, DEFAULT_CHUNK_BYTES, st.maxChunkBytes.takeIf { it > 0 } ?: DEFAULT_CHUNK_BYTES)
    require(chunk > 0)
    if (st.state == ImageUploadState.IMAGE_UPLOAD_STATE_RECEIVING) {
        requireMessage(st.persistedBytes in 0..actual) { UiMessages.errorsAndroidTheComputerReturnedInvalidFileProgress().messageText() }
        // Read even the resumed prefix: changes there must not silently pass verification.
        val check = MessageDigest.getInstance("SHA-256")
        openSource().use { input ->
            var cursor = 0L
            val prefix = ByteArray(64 * 1024)
            while (cursor < st.persistedBytes) {
                coroutineContext.ensureActive()
                val n = source { input.read(prefix, 0, minOf(prefix.size.toLong(), st.persistedBytes - cursor).toInt()) }
                if (n < 0) throw FileContentChanged()
                check.update(prefix, 0, n); cursor += n
            }
            while (cursor < actual) {
                coroutineContext.ensureActive()
                val offset = cursor
                val data = ByteArray(minOf(chunk.toLong(), actual - cursor).toInt())
                var n = 0
                while (n < data.size) {
                    val got = source { input.read(data, n, data.size - n) }
                    if (got < 0) throw FileContentChanged()
                    n += got
                }
                check.update(data); cursor += data.size
                onProgress(offset, actual)
                st = step({ ch.put(transferId, offset, data) }) { st = ch.status(transferId) }
            }
            if (source { input.read() } != -1 || !check.digest().contentEquals(hash)) {
                runCatching { ch.abort(transferId) }
                throw FileContentChanged()
            }
        }
    }
    var completing = 0
    while (st.state == ImageUploadState.IMAGE_UPLOAD_STATE_RECEIVING) {
        if (completing++ > retries) throw CommandException("unavailable", UiMessages.imageAndroidTheComputerHasNotConfirmedTheAttachmentUploadRetry().messageText())
        st = step({ ch.complete(transferId) }) { st = ch.status(transferId) }
    }
    if (st.fileId.isEmpty()) throw CommandException("internal", UiMessages.errorsAndroidTheComputerDidNotReturnAFileId().messageText())
    onProgress(actual, actual)
    return UploadedFile(st.fileId, st.expiresMs)
}
