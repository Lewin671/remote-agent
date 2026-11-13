package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.GetSharedFileChunkResponse
import io.github.lewin671.remoteagent.proto.SharedFile
import java.io.File
import java.io.IOException
import java.io.RandomAccessFile
import java.security.MessageDigest
import java.util.Locale
import java.util.Properties
import java.util.concurrent.ConcurrentHashMap
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asSharedFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

const val SHARED_FILE_BLOCK_BYTES = 786_432
const val SHARED_FILE_RECONNECT_WAIT_MS = 30_000L

enum class SharedFileKind {
    VIDEO,
    IMAGE,
    AUDIO,
    ARCHIVE,
    PDF,
    DOC,
    OTHER,
}

enum class SharedFileTileStyle {
    VIDEO,
    IMAGE,
    AUDIO,
    ARCHIVE_OR_OTHER,
    PDF_OR_DOC,
}

enum class SharedFileFailureKind {
    DISCONNECTED,
    CHECKSUM_MISMATCH,
    DISK_FULL,
    OTHER,
}

enum class CardVisualState {
    AVAILABLE,
    DOWNLOADING,
    PAUSED,
    DOWNLOADED,
    FAILED,
    MACHINE_OFFLINE,
    CLEANED_UP,
}

sealed interface SharedFileTransferState {
    data object Idle : SharedFileTransferState

    data class Downloading(
        val downloadedBytes: Long,
        val totalBytes: Long,
        val etaSeconds: Long? = null,
        val waitingReconnect: Boolean = false,
    ) : SharedFileTransferState

    data class Paused(
        val downloadedBytes: Long,
        val totalBytes: Long,
    ) : SharedFileTransferState

    data class Downloaded(
        val localFile: File,
        val durationMs: Long? = null,
    ) : SharedFileTransferState

    data class Failed(
        val kind: SharedFileFailureKind,
        val detail: String = "",
    ) : SharedFileTransferState

    data object CleanedUp : SharedFileTransferState
}

data class SharedFileBlockPlan(
    val offsets: List<Long>,
    val lastBlockFileBytes: Int,
)

data class SharedFileUiState(
    val visual: CardVisualState,
    val downloadedBytes: Long = 0L,
    val totalBytes: Long = 0L,
    val etaSeconds: Long? = null,
    val waitingReconnect: Boolean = false,
    val localFile: File? = null,
    val durationMs: Long? = null,
    val failureKind: SharedFileFailureKind? = null,
    val failureDetail: String = "",
)

data class SharedFileCompletion(
    val sessionId: String,
    val fileId: String,
    val fileName: String,
    val localFile: File,
    val userInitiated: Boolean,
)

fun interface SharedFileChunkFetcher {
    suspend fun fetchChunk(sessionId: String, fileId: String, offset: Long): GetSharedFileChunkResponse
}

/** Whether the connected daemon advertises chunked shared-file downloads. */
fun sharedFilesSupported(machine: MachineView?): Boolean = capabilities(machine)?.sharedFiles == true

/** Negotiated chunk size from daemon capabilities, falling back to the protocol default. */
fun sharedFileBlockBytes(machine: MachineView?): Int =
    capabilities(machine)?.sharedFileChunkBytes?.takeIf { it > 0 } ?: SHARED_FILE_BLOCK_BYTES

private val VIDEO_EXTS = setOf("mp4", "mov", "m4v", "mkv", "webm", "avi", "3gp")
private val IMAGE_EXTS = setOf("png", "jpg", "jpeg", "webp", "gif", "heic", "heif", "bmp", "svg")
private val AUDIO_EXTS = setOf("mp3", "m4a", "aac", "wav", "ogg", "opus", "flac")
private val ARCHIVE_EXTS = setOf("zip", "tar", "gz", "tgz", "bz2", "xz", "7z", "rar", "zst")
private val DOC_EXTS =
    setOf("doc", "docx", "xls", "xlsx", "ppt", "pptx", "txt", "md", "csv", "rtf", "pages", "numbers", "key")

fun classifySharedFile(name: String): SharedFileKind {
    val clean = sanitizeSharedFileName(name)
    val ext = clean.substringAfterLast('.', "").lowercase(Locale.ROOT)
    return when {
        ext in VIDEO_EXTS -> SharedFileKind.VIDEO
        ext in IMAGE_EXTS -> SharedFileKind.IMAGE
        ext in AUDIO_EXTS -> SharedFileKind.AUDIO
        ext in ARCHIVE_EXTS -> SharedFileKind.ARCHIVE
        ext == "pdf" -> SharedFileKind.PDF
        ext in DOC_EXTS -> SharedFileKind.DOC
        else -> SharedFileKind.OTHER
    }
}

fun SharedFileKind.tileStyle(): SharedFileTileStyle =
    when (this) {
        SharedFileKind.VIDEO -> SharedFileTileStyle.VIDEO
        SharedFileKind.IMAGE -> SharedFileTileStyle.IMAGE
        SharedFileKind.AUDIO -> SharedFileTileStyle.AUDIO
        SharedFileKind.PDF, SharedFileKind.DOC -> SharedFileTileStyle.PDF_OR_DOC
        SharedFileKind.ARCHIVE, SharedFileKind.OTHER -> SharedFileTileStyle.ARCHIVE_OR_OTHER
    }

fun SharedFileKind.typeMessage(): UiMessage =
    when (this) {
        SharedFileKind.VIDEO -> UiMessages.sharedFileTypeVideo()
        SharedFileKind.IMAGE -> UiMessages.sharedFileTypeImage()
        SharedFileKind.AUDIO -> UiMessages.sharedFileTypeAudio()
        SharedFileKind.ARCHIVE -> UiMessages.sharedFileTypeArchive()
        SharedFileKind.PDF -> UiMessages.sharedFileTypePdf()
        SharedFileKind.DOC, SharedFileKind.OTHER -> UiMessages.sharedFileTypeOther()
    }

fun SharedFileKind.supportsAlbumSave(): Boolean =
    this == SharedFileKind.IMAGE || this == SharedFileKind.VIDEO

/** Sanitizes a remote filename for local cache storage while keeping its extension. */
fun sanitizeSharedFileName(raw: String): String {
    val tail = raw.substringAfterLast('/').substringAfterLast('\\')
    val filtered = buildString(tail.length) {
        for (ch in tail) {
            if (ch < ' ' || ch == '\u007f') continue
            if (ch in '\u202a'..'\u202e' || ch in '\u2066'..'\u2069' || ch == '\u200e' || ch == '\u200f') {
                continue
            }
            append(ch)
        }
    }.trim()
    if (filtered.isEmpty() || filtered == "." || filtered == "..") return "file"
    if (filtered.length <= 200) return filtered
    val dot = filtered.lastIndexOf('.')
    return if (dot in 1 until filtered.length - 1 && filtered.length - dot <= 16) {
        val ext = filtered.substring(dot)
        filtered.take(200 - ext.length) + ext
    } else {
        filtered.take(200)
    }
}

/** Splits a filename into base + suffix while keeping extension intact for middle truncation. */
fun splitMiddleTruncateName(name: String): Pair<String, String> {
    val dot = name.lastIndexOf('.')
    if (dot <= 0 || dot >= name.length - 1 || name.length - dot > 12) return name to ""
    val keepTail = (name.length - dot + 3).coerceAtMost(name.length - 1)
    return name.dropLast(keepTail) to name.takeLast(keepTail)
}

/** Shortens POSIX home directories (`/Users/x/...`, `/home/x/...`) to `~/...` for display. */
fun displaySharedPath(path: String, fallbackName: String): String {
    val trimmed = path.trim()
    if (trimmed.isEmpty()) return sanitizeSharedFileName(fallbackName)
    for (prefix in listOf("/Users/", "/home/")) {
        if (trimmed.startsWith(prefix)) {
            val rest = trimmed.removePrefix(prefix)
            val slash = rest.indexOf('/')
            if (slash > 0) return "~" + rest.substring(slash)
        }
    }
    return trimmed
}

fun formatSharedFileSize(bytes: Long): String {
    val safe = bytes.coerceAtLeast(0L)
    if (safe < 1_000L) return "$safe B"
    if (safe < 1_000_000L) return String.format(Locale.US, "%.1f KB", safe / 1_000.0)
    if (safe < 1_000_000_000L) return String.format(Locale.US, "%.1f MB", safe / 1_000_000.0)
    return String.format(Locale.US, "%.2f GB", safe / 1_000_000_000.0)
}

/** Formats `"downloaded / total"`, omitting redundant unit on [downloaded] when units match. */
fun formatSharedProgress(downloaded: Long, total: Long): Pair<String, String> {
    val totalStr = formatSharedFileSize(total)
    val dlStr = formatSharedFileSize(downloaded.coerceAtMost(total))
    val unit = totalStr.substringAfter(' ', "")
    val dlUnit = dlStr.substringAfter(' ', "")
    return if (unit.isNotEmpty() && unit == dlUnit) {
        dlStr.substringBefore(' ') to totalStr
    } else {
        dlStr to totalStr
    }
}

fun formatMediaDuration(durationMs: Long): String {
    val totalSecs = (durationMs.coerceAtLeast(0L) + 500L) / 1000L
    val hours = totalSecs / 3600L
    val mins = (totalSecs % 3600L) / 60L
    val secs = totalSecs % 60L
    return if (hours > 0) {
        String.format(Locale.US, "%d:%02d:%02d", hours, mins, secs)
    } else {
        String.format(Locale.US, "%d:%02d", mins, secs)
    }
}

fun planSharedFileBlocks(
    sizeBytes: Long,
    blockBytes: Int = SHARED_FILE_BLOCK_BYTES,
): SharedFileBlockPlan {
    require(sizeBytes >= 0L) { "sizeBytes must be non-negative" }
    require(blockBytes > 0) { "blockBytes must be positive" }
    if (sizeBytes == 0L) return SharedFileBlockPlan(offsets = listOf(0L), lastBlockFileBytes = 0)
    val offsets = ArrayList<Long>()
    var offset = 0L
    while (offset < sizeBytes) {
        offsets += offset
        offset += blockBytes
    }
    val rem = (sizeBytes % blockBytes).toInt()
    return SharedFileBlockPlan(
        offsets = offsets,
        lastBlockFileBytes = if (rem == 0) blockBytes else rem,
    )
}

fun isValidSharedFileOffset(
    offset: Long,
    sizeBytes: Long,
    blockBytes: Int = SHARED_FILE_BLOCK_BYTES,
): Boolean {
    if (blockBytes <= 0 || offset < 0L || offset % blockBytes != 0L) return false
    return if (sizeBytes == 0L) offset == 0L else offset < sizeBytes
}

/** Combines persisted/active transfer state with live machine and session liveness. */
fun resolveSharedFileUiState(
    file: SharedFile,
    transfer: SharedFileTransferState,
    machineOnline: Boolean,
    sessionExited: Boolean,
): SharedFileUiState {
    if (transfer is SharedFileTransferState.Downloaded && transfer.localFile.isFile) {
        return SharedFileUiState(
            visual = CardVisualState.DOWNLOADED,
            downloadedBytes = file.sizeBytes,
            totalBytes = file.sizeBytes,
            localFile = transfer.localFile,
            durationMs = transfer.durationMs,
        )
    }
    if (transfer is SharedFileTransferState.Downloading) {
        return SharedFileUiState(
            visual = CardVisualState.DOWNLOADING,
            downloadedBytes = transfer.downloadedBytes,
            totalBytes = transfer.totalBytes,
            etaSeconds = transfer.etaSeconds,
            waitingReconnect = transfer.waitingReconnect,
        )
    }
    if (sessionExited || transfer is SharedFileTransferState.CleanedUp) {
        return SharedFileUiState(
            visual = CardVisualState.CLEANED_UP,
            totalBytes = file.sizeBytes,
        )
    }
    if (transfer is SharedFileTransferState.Paused) {
        return SharedFileUiState(
            visual = CardVisualState.PAUSED,
            downloadedBytes = transfer.downloadedBytes,
            totalBytes = transfer.totalBytes,
        )
    }
    if (!machineOnline) {
        return SharedFileUiState(
            visual = CardVisualState.MACHINE_OFFLINE,
            totalBytes = file.sizeBytes,
        )
    }
    if (transfer is SharedFileTransferState.Failed) {
        return SharedFileUiState(
            visual = CardVisualState.FAILED,
            totalBytes = file.sizeBytes,
            failureKind = transfer.kind,
            failureDetail = transfer.detail,
        )
    }
    return SharedFileUiState(
        visual = CardVisualState.AVAILABLE,
        totalBytes = file.sizeBytes,
    )
}

/**
 * Pure-Kotlin resumable chunk downloader and cache store for shared files sent by an Agent.
 * Deduplicates transfers by `(sessionId, fileId)` across duplicate cards and app navigation.
 */
class SharedFileStore(
    private val rootDir: File,
    private val fetcher: SharedFileChunkFetcher,
    private val isMachineOnline: (sessionId: String) -> Boolean,
    private val blockBytesProvider: (sessionId: String) -> Int = { SHARED_FILE_BLOCK_BYTES },
    private val clock: () -> Long = { System.currentTimeMillis() },
    private val sleeper: suspend (Long) -> Unit = { kotlinx.coroutines.delay(it) },
    private val ioDispatcher: CoroutineDispatcher = Dispatchers.IO,
    private val durationExtractor: (File) -> Long? = { null },
) {
    private val scope = CoroutineScope(SupervisorJob() + ioDispatcher)
    private val jobs = ConcurrentHashMap<String, Job>()
    private val controlModes = ConcurrentHashMap<String, ControlMode>()
    private val _states = MutableStateFlow<Map<String, SharedFileTransferState>>(emptyMap())
    val states: StateFlow<Map<String, SharedFileTransferState>> = _states.asStateFlow()

    private val _completions = MutableSharedFlow<SharedFileCompletion>(extraBufferCapacity = 32)
    val completions: SharedFlow<SharedFileCompletion> = _completions.asSharedFlow()

    private enum class ControlMode {
        PAUSE,
        CANCEL,
    }

    private fun key(sessionId: String, fileId: String) = "$sessionId:$fileId"

    private fun fileDir(sessionId: String, fileId: String): File {
        val safeSid = sessionId.replace(Regex("[^A-Za-z0-9_-]"), "_")
        val safeFid = fileId.replace(Regex("[^A-Za-z0-9_-]"), "_")
        return File(File(rootDir, safeSid), safeFid)
    }

    private fun partFile(dir: File) = File(dir, "download.part")

    private fun metaFile(dir: File) = File(dir, "meta.properties")

    fun localFileFor(sessionId: String, file: SharedFile): File =
        File(fileDir(sessionId, file.fileId), sanitizeSharedFileName(file.name))

    /** Loads existing disk state for [file] into [states] if not already tracked in memory. */
    fun hydrate(sessionId: String, file: SharedFile): SharedFileTransferState {
        val k = key(sessionId, file.fileId)
        _states.value[k]?.let { existing ->
            if (existing !is SharedFileTransferState.Downloaded || existing.localFile.isFile) {
                return existing
            }
        }
        val state = inspectDisk(sessionId, file)
        _states.update { it + (k to state) }
        return state
    }

    fun inspectDisk(sessionId: String, file: SharedFile): SharedFileTransferState {
        val dir = fileDir(sessionId, file.fileId)
        val finalFile = localFileFor(sessionId, file)
        val meta = readMeta(metaFile(dir))
        if (finalFile.isFile && finalFile.length() == file.sizeBytes) {
            val dur = meta?.getProperty("durationMs")?.toLongOrNull()
            return SharedFileTransferState.Downloaded(finalFile, dur)
        }
        val part = partFile(dir)
        val blockBytes = meta?.getProperty("blockBytes")?.toIntOrNull() ?: 0
        val completed = meta?.getProperty("completedOffset")?.toLongOrNull() ?: -1L
        val expectedHash = file.sha256.toByteArray().toHex()
        if (
            meta != null &&
            part.isFile &&
            blockBytes > 0 &&
            meta.getProperty("sizeBytes")?.toLongOrNull() == file.sizeBytes &&
            meta.getProperty("sha256") == expectedHash &&
            completed > 0L &&
            completed % blockBytes == 0L &&
            part.length() >= completed
        ) {
            return SharedFileTransferState.Paused(
                downloadedBytes = completed.coerceAtMost(file.sizeBytes),
                totalBytes = file.sizeBytes,
            )
        }
        return SharedFileTransferState.Idle
    }

    fun startOrResume(
        sessionId: String,
        file: SharedFile,
        userInitiated: Boolean = true,
    ) {
        val k = key(sessionId, file.fileId)
        if (jobs[k]?.isActive == true) return
        val existing = inspectDisk(sessionId, file)
        if (existing is SharedFileTransferState.Downloaded) {
            setState(k, existing)
            return
        }
        controlModes.remove(k)
        val job = scope.launch {
            runDownload(sessionId, file, userInitiated)
        }
        jobs[k] = job
        job.invokeOnCompletion {
            jobs.remove(k, job)
        }
    }

    fun pause(sessionId: String, file: SharedFile) {
        val k = key(sessionId, file.fileId)
        controlModes[k] = ControlMode.PAUSE
        val active = jobs[k]
        if (active != null) {
            active.cancel()
        }
        val dir = fileDir(sessionId, file.fileId)
        val meta = readMeta(metaFile(dir))
        val completed = (meta?.getProperty("completedOffset")?.toLongOrNull() ?: 0L)
            .coerceIn(0L, file.sizeBytes)
        setState(k, SharedFileTransferState.Paused(completed, file.sizeBytes))
    }

    fun cancel(sessionId: String, file: SharedFile) {
        val k = key(sessionId, file.fileId)
        controlModes[k] = ControlMode.CANCEL
        jobs[k]?.cancel()
        val dir = fileDir(sessionId, file.fileId)
        partFile(dir).delete()
        metaFile(dir).delete()
        setState(k, SharedFileTransferState.Idle)
    }

    fun deleteLocal(sessionId: String, file: SharedFile) {
        val k = key(sessionId, file.fileId)
        controlModes[k] = ControlMode.CANCEL
        jobs[k]?.cancel()
        fileDir(sessionId, file.fileId).deleteRecursively()
        setState(k, SharedFileTransferState.Idle)
    }

    fun removeSession(sessionId: String) {
        val prefix = "$sessionId:"
        for ((k, job) in jobs.entries) {
            if (k.startsWith(prefix)) {
                controlModes[k] = ControlMode.CANCEL
                job.cancel()
            }
        }
        val safeSid = sessionId.replace(Regex("[^A-Za-z0-9_-]"), "_")
        File(rootDir, safeSid).deleteRecursively()
        _states.update { map -> map.filterKeys { !it.startsWith(prefix) } }
    }

    fun clearAll() {
        for ((k, job) in jobs.entries) {
            controlModes[k] = ControlMode.CANCEL
            job.cancel()
        }
        rootDir.listFiles()?.forEach { it.deleteRecursively() }
        _states.value = emptyMap()
    }

    fun usedBytes(): Long = ImageDiskPolicy.bytes(rootDir)

    fun close() {
        scope.cancel()
    }

    private suspend fun runDownload(
        sessionId: String,
        file: SharedFile,
        userInitiated: Boolean,
    ) {
        val k = key(sessionId, file.fileId)
        val dir = fileDir(sessionId, file.fileId)
        val finalFile = localFileFor(sessionId, file)
        val part = partFile(dir)
        val meta = metaFile(dir)
        val blockBytes = blockBytesProvider(sessionId).coerceAtLeast(1)
        val expectedHash = file.sha256.toByteArray()
        val expectedHashHex = expectedHash.toHex()

        try {
            dir.mkdirs()
            val savedMeta = readMeta(meta)
            var nextOffset = 0L
            if (
                savedMeta != null &&
                part.isFile &&
                savedMeta.getProperty("sizeBytes")?.toLongOrNull() == file.sizeBytes &&
                savedMeta.getProperty("blockBytes")?.toIntOrNull() == blockBytes &&
                savedMeta.getProperty("sha256") == expectedHashHex
            ) {
                val savedCompleted = savedMeta.getProperty("completedOffset")?.toLongOrNull() ?: 0L
                if (savedCompleted > 0L && savedCompleted % blockBytes == 0L && savedCompleted < file.sizeBytes && part.length() >= savedCompleted) {
                    RandomAccessFile(part, "rw").use { it.setLength(savedCompleted) }
                    nextOffset = savedCompleted
                } else {
                    part.delete()
                }
            } else {
                part.delete()
            }

            if (!part.exists()) {
                writeMeta(meta, file.sizeBytes, blockBytes, 0L, expectedHashHex, durationMs = null)
            }

            val startDownloaded = nextOffset.coerceAtMost(file.sizeBytes)
            val startTimeMs = clock()
            var sessionBytes = 0L
            var retriedOnce = false

            setState(k, SharedFileTransferState.Downloading(startDownloaded, file.sizeBytes))

            val plan = planSharedFileBlocks(file.sizeBytes, blockBytes)
            var blockIndex = plan.offsets.indexOfFirst { it >= nextOffset }
            if (blockIndex < 0) blockIndex = 0

            while (blockIndex < plan.offsets.size) {
                currentCoroutineContext().ensureActive()
                val offset = plan.offsets[blockIndex]

                val res =
                    try {
                        fetcher.fetchChunk(sessionId, file.fileId, offset)
                    } catch (e: CancellationException) {
                        throw e
                    } catch (e: CommandException) {
                        if (e.code == "not_found") {
                            part.delete()
                            meta.delete()
                            setState(k, SharedFileTransferState.CleanedUp)
                            return
                        }
                        if (isTransientDisconnect(e) && !retriedOnce) {
                            retriedOnce = true
                            val currentDownloaded = offset.coerceAtMost(file.sizeBytes)
                            val reconnected = waitForReconnect(k, sessionId, currentDownloaded, file.sizeBytes)
                            if (reconnected) {
                                continue
                            }
                            if (!isMachineOnline(sessionId) && currentDownloaded > 0L) {
                                setState(k, SharedFileTransferState.Paused(currentDownloaded, file.sizeBytes))
                            } else {
                                setState(k, SharedFileTransferState.Failed(SharedFileFailureKind.DISCONNECTED))
                            }
                            return
                        }
                        if (isTransientDisconnect(e)) {
                            val currentDownloaded = offset.coerceAtMost(file.sizeBytes)
                            if (!isMachineOnline(sessionId) && currentDownloaded > 0L) {
                                setState(k, SharedFileTransferState.Paused(currentDownloaded, file.sizeBytes))
                            } else {
                                setState(k, SharedFileTransferState.Failed(SharedFileFailureKind.DISCONNECTED))
                            }
                            return
                        }
                        setState(k, SharedFileTransferState.Failed(SharedFileFailureKind.OTHER, e.message.orEmpty()))
                        return
                    } catch (e: IOException) {
                        val kind =
                            if (isDiskFull(e)) SharedFileFailureKind.DISK_FULL
                            else SharedFileFailureKind.DISCONNECTED
                        setState(k, SharedFileTransferState.Failed(kind, e.message.orEmpty()))
                        return
                    }

                if (!res.hasTotalBytes() || res.totalBytes != file.sizeBytes) {
                    part.delete()
                    meta.delete()
                    setState(k, SharedFileTransferState.Failed(SharedFileFailureKind.CHECKSUM_MISMATCH))
                    return
                }
                val chunkBytes = res.data.toByteArray()
                val remaining = (file.sizeBytes - offset).coerceAtLeast(0L)
                val expectedWrite = minOf(remaining, blockBytes.toLong()).toInt()
                if (chunkBytes.size != blockBytes && !(file.sizeBytes == 0L && chunkBytes.isEmpty())) {
                    part.delete()
                    meta.delete()
                    setState(k, SharedFileTransferState.Failed(SharedFileFailureKind.CHECKSUM_MISMATCH))
                    return
                }
                if (chunkBytes.size < expectedWrite) {
                    part.delete()
                    meta.delete()
                    setState(k, SharedFileTransferState.Failed(SharedFileFailureKind.CHECKSUM_MISMATCH))
                    return
                }

                try {
                    RandomAccessFile(part, "rw").use { raf ->
                        raf.seek(offset)
                        if (expectedWrite > 0) raf.write(chunkBytes, 0, expectedWrite)
                        raf.setLength(offset + expectedWrite)
                    }
                } catch (e: IOException) {
                    val kind =
                        if (isDiskFull(e)) SharedFileFailureKind.DISK_FULL else SharedFileFailureKind.OTHER
                    setState(k, SharedFileTransferState.Failed(kind, e.message.orEmpty()))
                    return
                }

                val committedOffset = offset + blockBytes
                val downloadedNow = minOf(file.sizeBytes, offset + expectedWrite)
                sessionBytes += expectedWrite
                writeMeta(meta, file.sizeBytes, blockBytes, committedOffset, expectedHashHex, durationMs = null)

                val elapsedMs = (clock() - startTimeMs).coerceAtLeast(0L)
                val eta =
                    if (elapsedMs >= 1_000L && sessionBytes > 0L && downloadedNow < file.sizeBytes) {
                        val ratePerSec = sessionBytes / (elapsedMs / 1000.0)
                        ((file.sizeBytes - downloadedNow) / ratePerSec).toLong().coerceAtLeast(1L)
                    } else null
                setState(k, SharedFileTransferState.Downloading(downloadedNow, file.sizeBytes, eta))
                blockIndex++
            }

            // Full file fetched: verify SHA-256 over the truncated payload.
            if (expectedHash.size == 32) {
                val actualHash = sha256File(part, file.sizeBytes)
                if (!MessageDigest.isEqual(actualHash, expectedHash)) {
                    part.delete()
                    meta.delete()
                    setState(k, SharedFileTransferState.Failed(SharedFileFailureKind.CHECKSUM_MISMATCH))
                    return
                }
            }

            if (finalFile.exists()) finalFile.delete()
            if (file.sizeBytes == 0L && !part.exists()) {
                finalFile.createNewFile()
            } else if (!part.renameTo(finalFile)) {
                part.copyTo(finalFile, overwrite = true)
                part.delete()
            }

            val durationMs = durationExtractor(finalFile)
            writeMeta(meta, file.sizeBytes, blockBytes, file.sizeBytes, expectedHashHex, durationMs)
            setState(k, SharedFileTransferState.Downloaded(finalFile, durationMs))
            _completions.tryEmit(
                SharedFileCompletion(
                    sessionId = sessionId,
                    fileId = file.fileId,
                    fileName = file.name,
                    localFile = finalFile,
                    userInitiated = userInitiated,
                ),
            )
        } catch (e: CancellationException) {
            if (controlModes[k] == ControlMode.CANCEL) {
                part.delete()
                meta.delete()
            }
            throw e
        } catch (e: IOException) {
            val kind = if (isDiskFull(e)) SharedFileFailureKind.DISK_FULL else SharedFileFailureKind.OTHER
            setState(k, SharedFileTransferState.Failed(kind, e.message.orEmpty()))
        }
    }

    private suspend fun waitForReconnect(
        key: String,
        sessionId: String,
        downloadedBytes: Long,
        totalBytes: Long,
    ): Boolean {
        val deadline = clock() + SHARED_FILE_RECONNECT_WAIT_MS
        setState(
            key,
            SharedFileTransferState.Downloading(
                downloadedBytes = downloadedBytes,
                totalBytes = totalBytes,
                etaSeconds = null,
                waitingReconnect = true,
            ),
        )
        // Brief pause on transient drop even if the machine flag appears online.
        sleeper(1_000L)
        while (clock() < deadline) {
            currentCoroutineContext().ensureActive()
            if (isMachineOnline(sessionId)) return true
            val step = minOf(1_000L, (deadline - clock()).coerceAtLeast(1L))
            sleeper(step)
        }
        return isMachineOnline(sessionId)
    }

    private fun setState(key: String, state: SharedFileTransferState) {
        _states.update { it + (key to state) }
    }

    /** Seeds a transfer state directly for deterministic UI and screenshot tests. */
    fun setStateForTest(sessionId: String, fileId: String, state: SharedFileTransferState) {
        setState(key(sessionId, fileId), state)
    }

    private fun isTransientDisconnect(e: CommandException): Boolean =
        e.code in setOf("network", CommandException.UNAVAILABLE, CommandException.UNKNOWN) ||
            e.failureKind == CommandFailureKind.CONNECTION_DROPPED ||
            e.failureKind == CommandFailureKind.COMPUTER_OFFLINE

    private fun isDiskFull(e: IOException): Boolean {
        val msg = e.message?.lowercase(Locale.ROOT).orEmpty()
        return "enospc" in msg || "no space" in msg || "disk full" in msg
    }

    private fun readMeta(file: File): Properties? {
        if (!file.isFile) return null
        return runCatching {
            Properties().apply {
                file.inputStream().use { load(it) }
            }
        }.getOrNull()
    }

    private fun writeMeta(
        file: File,
        sizeBytes: Long,
        blockBytes: Int,
        completedOffset: Long,
        sha256Hex: String,
        durationMs: Long?,
    ) {
        val props = Properties()
        props.setProperty("sizeBytes", sizeBytes.toString())
        props.setProperty("blockBytes", blockBytes.toString())
        props.setProperty("completedOffset", completedOffset.toString())
        props.setProperty("sha256", sha256Hex)
        if (durationMs != null) props.setProperty("durationMs", durationMs.toString())
        file.outputStream().use { props.store(it, null) }
    }

    private fun sha256File(file: File, exactBytes: Long): ByteArray {
        val md = MessageDigest.getInstance("SHA-256")
        if (exactBytes == 0L || !file.isFile) return md.digest()
        val buf = ByteArray(8192)
        var remaining = exactBytes
        file.inputStream().use { input ->
            while (remaining > 0L) {
                val read = input.read(buf, 0, minOf(buf.size.toLong(), remaining).toInt())
                if (read <= 0) break
                md.update(buf, 0, read)
                remaining -= read
            }
        }
        return md.digest()
    }
}

private fun ByteArray.toHex(): String = joinToString("") { "%02x".format(it) }
