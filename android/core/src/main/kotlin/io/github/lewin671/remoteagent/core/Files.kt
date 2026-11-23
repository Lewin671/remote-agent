package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.proto.ImageProblemKind
import io.github.lewin671.remoteagent.proto.ImageUploadState
import kotlinx.coroutines.ensureActive
import java.io.InputStream
import java.security.MessageDigest
import kotlin.coroutines.coroutineContext

/** A video's length in the corner of its thumbnail: 0:42, 12:05, 1:02:03. */
fun videoLength(ms: Long): String {
    val total = (ms.coerceAtLeast(0) + 500) / 1000
    val h = total / 3600
    val m = total / 60 % 60
    val sec = total % 60
    return if (h > 0) String.format(java.util.Locale.ROOT, "%d:%02d:%02d", h, m, sec) else String.format(java.util.Locale.ROOT, "%d:%02d", m, sec)
}

data class UploadedFile(val fileId: String, val expiresMs: Long)

/** IO from the provider is distinguished from network IO by where it happened. */
class FileSourceException(cause: Exception) : MessageException(
    UiMessages.errorsAndroidCannotReadTheSourceFileRemoveItAndSelectThe().messageText(),
    cause,
    imageKind = ImageProblemKind.IMAGE_PROBLEM_KIND_SOURCE_MISSING,
)

class FileContentChanged : MessageException(
    UiMessages.errorsAndroidTheFileChangedDuringUploadRetryOrSelectTheFile().messageText(),
    imageKind = ImageProblemKind.IMAGE_PROBLEM_KIND_SOURCE_MISSING,
)

fun isUploadHashMismatch(e: Throwable) = e is CommandException && e.code == "invalid_argument" && e.message.orEmpty().contains("hash", ignoreCase = true)
fun restartUpload(e: Throwable) = e is CommandException && (e.code == "already_exists" || e.code == "not_found" || isUploadHashMismatch(e))
fun uploadErrorText(e: Throwable): MessageText = when {
    e is FileSourceException || e is FileContentChanged -> errorText(e)
    isUploadHashMismatch(e) -> UiMessages.errorsAndroidFileVerificationFailedRetryOrSelectTheFileAgain().messageText()
    e is CommandException && e.code == "already_exists" -> UiMessages.errorsAndroidAttachmentTransferContentChangedRetry().messageText()
    e is CommandException && e.outcome == Outcome.CONFLICT && e.conflictKind == "business" -> UiMessages.errorsAndroidAttachmentTransferStoppedOrChangedRetry().messageText()
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
    ch: UploadChannel,
    transferId: String,
    name: String,
    total: Long,
    open: () -> InputStream,
    maxChunk: Int = DEFAULT_CHUNK_BYTES,
    retries: Int = 3,
    maxBytes: Long = 100L * 1024 * 1024,
    maxNameBytes: Int = 255,
    maxSizeText: String = "100.0 MB",
    onPrepared: (Long) -> Unit = {},
    onProgress: (Long, Long) -> Unit = { _, _ -> },
): UploadedFile {
    requireMessage(name.isNotBlank() && name.toByteArray().size <= maxNameBytes) { UiMessages.errorsAndroidFileNameIsTooLongOrEmptyRenameTheFile().messageText() }
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
            requireMessage(actual <= maxBytes) { UiMessages.errorsAndroidFileTooLargeMaximumSizePerFileValue1(maxSizeText).messageText() }
            digest.update(buffer, 0, n)
        }
    }
    onPrepared(actual)
    val hash = digest.digest()
    var tries = 0
    suspend fun <T> step(block: suspend () -> T, resync: suspend () -> Unit): T {
        while (true) try { return block().also { tries = 0 } } catch (e: CommandException) {
            if (e.outcome == Outcome.CONFLICT && e.conflictKind == "business") throw e
            if (!e.canRetryUpload || tries++ >= retries) throw e
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
