package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.proto.SessionSharedFileItem
import io.github.lewin671.remoteagent.core.proto.SharedFileFailureKind as CoreSharedFileFailureKind
import io.github.lewin671.remoteagent.core.proto.SharedFileKind as CoreSharedFileKind
import io.github.lewin671.remoteagent.core.proto.SharedFileLocalStateEvent
import io.github.lewin671.remoteagent.core.proto.SharedFileLocalStateUpdate
import io.github.lewin671.remoteagent.core.proto.SharedFileTransferStatus
import io.github.lewin671.remoteagent.proto.GetSharedFileChunkResponse
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
import kotlinx.coroutines.CoroutineStart
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

const val SHARED_FILE_BLOCK_BYTES = 786_432
const val SHARED_FILE_RECONNECT_WAIT_MS = 30_000L
const val SHARED_FILE_PROGRESS_THROTTLE_MS = 250L

/** Minimal identity and transfer metadata needed by the shell's chunk downloader and cache store. */
data class SharedFileTarget(
    val fileId: String,
    val displayName: String,
    val sizeBytes: Long,
    val sha256: ByteArray = ByteArray(0),
    val kind: CoreSharedFileKind = CoreSharedFileKind.SHARED_FILE_KIND_OTHER,
    val hasMediaDuration: Boolean = false,
) {
    override fun equals(other: Any?): Boolean =
        this === other || (
            other is SharedFileTarget &&
                fileId == other.fileId &&
                displayName == other.displayName &&
                sizeBytes == other.sizeBytes &&
                sha256.contentEquals(other.sha256) &&
                kind == other.kind &&
                hasMediaDuration == other.hasMediaDuration
            )

    override fun hashCode(): Int {
        var r = fileId.hashCode()
        r = 31 * r + displayName.hashCode()
        r = 31 * r + sizeBytes.hashCode()
        r = 31 * r + sha256.contentHashCode()
        r = 31 * r + kind.hashCode()
        r = 31 * r + hasMediaDuration.hashCode()
        return r
    }
}

fun SessionSharedFileItem.toSharedFileTarget(): SharedFileTarget =
    SharedFileTarget(
        fileId = fileId,
        displayName = displayName,
        sizeBytes = sizeBytes,
        sha256 = sha256.toByteArray(),
        kind = kind,
        hasMediaDuration = hasMediaDuration,
    )

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
        val kind: CoreSharedFileFailureKind,
        val detail: String = "",
    ) : SharedFileTransferState

    data object CleanedUp : SharedFileTransferState
}

data class SharedFileBlockPlan(
    val offsets: List<Long>,
    val lastBlockFileBytes: Int,
)

data class SharedFileCompletion(
    val sessionId: String,
    val fileId: String,
    val displayName: String,
    val sizeBytes: Long,
    val kind: CoreSharedFileKind,
    val localFile: File,
    val userInitiated: Boolean,
)

fun interface SharedFileChunkFetcher {
    suspend fun fetchChunk(sessionId: String, fileId: String, offset: Long): GetSharedFileChunkResponse
}

/** Negotiated chunk size from daemon capabilities, falling back to the protocol default. */
fun sharedFileBlockBytes(machine: MachineView?): Int =
    capabilities(machine)?.sharedFileChunkBytes?.takeIf { it > 0 } ?: SHARED_FILE_BLOCK_BYTES

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

/** Converts a shell transfer state into a Core [SharedFileLocalStateUpdate]. */
fun SharedFileTransferState.toLocalStateUpdate(
    sessionId: String,
    fileId: String,
): SharedFileLocalStateUpdate {
    val b = SharedFileLocalStateUpdate.newBuilder()
        .setSessionId(sessionId)
        .setFileId(fileId)
    when (this) {
        SharedFileTransferState.Idle -> b.setStatus(SharedFileTransferStatus.SHARED_FILE_TRANSFER_STATUS_UNSPECIFIED)
        is SharedFileTransferState.Downloading -> {
            b.setStatus(SharedFileTransferStatus.SHARED_FILE_TRANSFER_STATUS_DOWNLOADING)
                .setDownloadedBytes(downloadedBytes)
                .setWaitingReconnect(waitingReconnect)
            if (etaSeconds != null) {
                b.setHasEta(true).setEtaSeconds(etaSeconds)
            }
        }
        is SharedFileTransferState.Paused -> {
            b.setStatus(SharedFileTransferStatus.SHARED_FILE_TRANSFER_STATUS_PAUSED)
                .setDownloadedBytes(downloadedBytes)
        }
        is SharedFileTransferState.Downloaded -> {
            b.setStatus(SharedFileTransferStatus.SHARED_FILE_TRANSFER_STATUS_DOWNLOADED)
            if (durationMs != null) {
                b.setHasDuration(true).setDurationMs(durationMs)
            }
        }
        is SharedFileTransferState.Failed -> {
            b.setStatus(SharedFileTransferStatus.SHARED_FILE_TRANSFER_STATUS_FAILED)
                .setFailureKind(kind)
                .setFailureDetail(detail)
        }
        SharedFileTransferState.CleanedUp -> b.setStatus(SharedFileTransferStatus.SHARED_FILE_TRANSFER_STATUS_CLEANED_UP)
    }
    return b.build()
}

/**
 * Pure-Kotlin resumable chunk downloader and cache store for shared files sent by an Agent.
 * Deduplicates transfers by `(sessionId, fileId)` across duplicate cards and app navigation,
 * and reports throttled local state transitions to Core via [onLocalStateEvent].
 */
class SharedFileStore(
    private val rootDir: File,
    private val fetcher: SharedFileChunkFetcher,
    private val isMachineOnline: (sessionId: String) -> Boolean,
    private val blockBytesProvider: (sessionId: String) -> Int = { SHARED_FILE_BLOCK_BYTES },
    private val clock: () -> Long = { System.currentTimeMillis() },
    private val sleeper: suspend (Long) -> Unit = { kotlinx.coroutines.delay(it) },
    private val ioDispatcher: CoroutineDispatcher = Dispatchers.IO,
    private val durationExtractor: (File, Boolean) -> Long? = { _, _ -> null },
    private val onLocalStateEvent: (SharedFileLocalStateEvent) -> Unit = {},
) {
    private val scope = CoroutineScope(SupervisorJob() + ioDispatcher)
    private val jobs = ConcurrentHashMap<String, Job>()
    private val controlModes = ConcurrentHashMap<String, ControlMode>()
    private val _states = MutableStateFlow<Map<String, SharedFileTransferState>>(emptyMap())
    val states: StateFlow<Map<String, SharedFileTransferState>> = _states.asStateFlow()
    private val entryKeys = ConcurrentHashMap<String, Pair<String, String>>()
    private val lastProgressReport = ConcurrentHashMap<String, ProgressStamp>()

    private val _completions = MutableSharedFlow<SharedFileCompletion>(extraBufferCapacity = 32)
    val completions: SharedFlow<SharedFileCompletion> = _completions.asSharedFlow()

    private enum class ControlMode {
        PAUSE,
        CANCEL,
    }

    private data class ProgressStamp(
        val timeMs: Long,
        val percent: Int,
        val waitingReconnect: Boolean,
        val etaSeconds: Long?,
    )

    private fun key(sessionId: String, fileId: String) = "$sessionId:$fileId"

    private fun safeLocalFileName(displayName: String): String =
        displayName.substringAfterLast('/').substringAfterLast('\\').trim().takeIf {
            it.isNotEmpty() && it != "." && it != ".."
        } ?: "file"

    private fun fileDir(sessionId: String, fileId: String): File {
        val safeSid = sessionId.replace(Regex("[^A-Za-z0-9_-]"), "_")
        val safeFid = fileId.replace(Regex("[^A-Za-z0-9_-]"), "_")
        return File(File(rootDir, safeSid), safeFid)
    }

    private fun partFile(dir: File) = File(dir, "download.part")

    private fun metaFile(dir: File) = File(dir, "meta.properties")

    fun localFileFor(sessionId: String, file: SharedFileTarget): File =
        File(fileDir(sessionId, file.fileId), safeLocalFileName(file.displayName))

    fun localFileIfDownloaded(sessionId: String, fileId: String): File? =
        (_states.value[key(sessionId, fileId)] as? SharedFileTransferState.Downloaded)?.localFile?.takeIf { it.isFile }

    /** Builds a full-state `replace_all = true` snapshot for startup / account switch reporting. */
    fun buildFullLocalStateEvent(): SharedFileLocalStateEvent {
        val b = SharedFileLocalStateEvent.newBuilder().setReplaceAll(true)
        for ((k, state) in _states.value) {
            if (state == SharedFileTransferState.Idle) continue
            val pair = entryKeys[k] ?: continue
            b.addUpdates(state.toLocalStateUpdate(pair.first, pair.second))
        }
        return b.build()
    }

    /** Loads existing disk state for [file] into [states] and reports non-idle state to Core. */
    fun hydrate(sessionId: String, file: SharedFileTarget): SharedFileTransferState {
        val k = key(sessionId, file.fileId)
        _states.value[k]?.let { existing ->
            if (existing !is SharedFileTransferState.Downloaded || existing.localFile.isFile) {
                return existing
            }
        }
        val state = inspectDisk(sessionId, file)
        entryKeys[k] = sessionId to file.fileId
        _states.update { it + (k to state) }
        if (state != SharedFileTransferState.Idle) {
            emitSingle(sessionId, file.fileId, state, force = true)
        }
        return state
    }

    fun inspectDisk(sessionId: String, file: SharedFileTarget): SharedFileTransferState {
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
        val expectedHash = file.sha256.toHex()
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

    @Synchronized
    fun startOrResume(
        sessionId: String,
        file: SharedFileTarget,
        userInitiated: Boolean = true,
    ) {
        val k = key(sessionId, file.fileId)
        if (jobs[k]?.isActive == true) return
        val existing = inspectDisk(sessionId, file)
        if (existing is SharedFileTransferState.Downloaded) {
            setState(sessionId, file.fileId, existing)
            return
        }
        controlModes.remove(k)
        val previous = jobs[k]
        val job = scope.launch(start = CoroutineStart.LAZY) {
            previous?.join()
            currentCoroutineContext().ensureActive()
            runDownload(sessionId, file, userInitiated)
        }
        jobs[k] = job
        job.invokeOnCompletion {
            jobs.remove(k, job)
        }
        job.start()
    }

    @Synchronized
    fun pause(sessionId: String, file: SharedFileTarget) {
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
        setState(sessionId, file.fileId, SharedFileTransferState.Paused(completed, file.sizeBytes))
    }

    @Synchronized
    fun cancel(sessionId: String, file: SharedFileTarget) {
        val k = key(sessionId, file.fileId)
        controlModes[k] = ControlMode.CANCEL
        jobs[k]?.cancel()
        val dir = fileDir(sessionId, file.fileId)
        partFile(dir).delete()
        metaFile(dir).delete()
        setState(sessionId, file.fileId, SharedFileTransferState.Idle)
    }

    @Synchronized
    fun deleteLocal(sessionId: String, file: SharedFileTarget) {
        val k = key(sessionId, file.fileId)
        controlModes[k] = ControlMode.CANCEL
        jobs[k]?.cancel()
        fileDir(sessionId, file.fileId).deleteRecursively()
        setState(sessionId, file.fileId, SharedFileTransferState.Idle)
    }

    @Synchronized
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
        entryKeys.keys.removeIf { it.startsWith(prefix) }
        lastProgressReport.keys.removeIf { it.startsWith(prefix) }
        _states.update { map -> map.filterKeys { !it.startsWith(prefix) } }
        onLocalStateEvent(SharedFileLocalStateEvent.newBuilder().setClearSessionId(sessionId).build())
    }

    @Synchronized
    fun clearAll() {
        for ((k, job) in jobs.entries) {
            controlModes[k] = ControlMode.CANCEL
            job.cancel()
        }
        rootDir.listFiles()?.forEach { it.deleteRecursively() }
        entryKeys.clear()
        lastProgressReport.clear()
        _states.value = emptyMap()
        onLocalStateEvent(SharedFileLocalStateEvent.newBuilder().setReplaceAll(true).build())
    }

    fun usedBytes(): Long = ImageDiskPolicy.bytes(rootDir)

    fun close() {
        scope.cancel()
    }

    private suspend fun runDownload(
        sessionId: String,
        file: SharedFileTarget,
        userInitiated: Boolean,
    ) {
        val k = key(sessionId, file.fileId)
        val dir = fileDir(sessionId, file.fileId)
        val finalFile = localFileFor(sessionId, file)
        val part = partFile(dir)
        val meta = metaFile(dir)
        val blockBytes = blockBytesProvider(sessionId).coerceAtLeast(1)
        val expectedHash = file.sha256
        val expectedHashHex = expectedHash.toHex()

        try {
            val startContext = currentCoroutineContext()
            val nextOffset = synchronized(this) {
                startContext.ensureActive()
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

                setState(sessionId, file.fileId, SharedFileTransferState.Downloading(nextOffset.coerceAtMost(file.sizeBytes), file.sizeBytes))
                nextOffset
            }

            val startTimeMs = clock()
            var sessionBytes = 0L
            var retriedOnce = false

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
                        currentCoroutineContext().ensureActive()
                        if (e.code == "not_found") {
                            part.delete()
                            meta.delete()
                            setState(sessionId, file.fileId, SharedFileTransferState.CleanedUp)
                            return
                        }
                        if (isTransientDisconnect(e) && !retriedOnce) {
                            retriedOnce = true
                            val currentDownloaded = offset.coerceAtMost(file.sizeBytes)
                            val reconnected = waitForReconnect(sessionId, file.fileId, currentDownloaded, file.sizeBytes)
                            if (reconnected) {
                                continue
                            }
                            setState(sessionId, file.fileId, SharedFileTransferState.Failed(CoreSharedFileFailureKind.SHARED_FILE_FAILURE_KIND_DISCONNECTED))
                            return
                        }
                        if (isTransientDisconnect(e)) {
                            setState(sessionId, file.fileId, SharedFileTransferState.Failed(CoreSharedFileFailureKind.SHARED_FILE_FAILURE_KIND_DISCONNECTED))
                            return
                        }
                        setState(sessionId, file.fileId, SharedFileTransferState.Failed(CoreSharedFileFailureKind.SHARED_FILE_FAILURE_KIND_OTHER, e.message.orEmpty()))
                        return
                    } catch (e: IOException) {
                        val kind =
                            if (isDiskFull(e)) CoreSharedFileFailureKind.SHARED_FILE_FAILURE_KIND_PHONE_STORAGE_FULL
                            else CoreSharedFileFailureKind.SHARED_FILE_FAILURE_KIND_DISCONNECTED
                        setState(sessionId, file.fileId, SharedFileTransferState.Failed(kind, e.message.orEmpty()))
                        return
                    }

                // A response can arrive as pause/cancel is being processed. Commit the
                // block and its visible state atomically with those controls.
                val activeContext = currentCoroutineContext()
                synchronized(this) {
                    activeContext.ensureActive()
                    if (!res.hasTotalBytes() || res.totalBytes != file.sizeBytes) {
                        part.delete()
                        meta.delete()
                        setState(sessionId, file.fileId, SharedFileTransferState.Failed(CoreSharedFileFailureKind.SHARED_FILE_FAILURE_KIND_CHECKSUM_MISMATCH))
                        return
                    }
                    val chunkBytes = res.data.toByteArray()
                    val remaining = (file.sizeBytes - offset).coerceAtLeast(0L)
                    val expectedWrite = minOf(remaining, blockBytes.toLong()).toInt()
                    if (chunkBytes.size != blockBytes && !(file.sizeBytes == 0L && chunkBytes.isEmpty())) {
                        part.delete()
                        meta.delete()
                        setState(sessionId, file.fileId, SharedFileTransferState.Failed(CoreSharedFileFailureKind.SHARED_FILE_FAILURE_KIND_CHECKSUM_MISMATCH))
                        return
                    }
                    if (chunkBytes.size < expectedWrite) {
                        part.delete()
                        meta.delete()
                        setState(sessionId, file.fileId, SharedFileTransferState.Failed(CoreSharedFileFailureKind.SHARED_FILE_FAILURE_KIND_CHECKSUM_MISMATCH))
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
                            if (isDiskFull(e)) CoreSharedFileFailureKind.SHARED_FILE_FAILURE_KIND_PHONE_STORAGE_FULL
                            else CoreSharedFileFailureKind.SHARED_FILE_FAILURE_KIND_OTHER
                        setState(sessionId, file.fileId, SharedFileTransferState.Failed(kind, e.message.orEmpty()))
                        return
                    }

                    val committedOffset = offset + blockBytes
                    val downloadedNow = minOf(file.sizeBytes, offset + expectedWrite)
                    sessionBytes += expectedWrite
                    writeMeta(meta, file.sizeBytes, blockBytes, committedOffset, expectedHashHex, durationMs = null)

                    val elapsedMs = (clock() - startTimeMs).coerceAtLeast(0L)
                    val eta =
                        if (elapsedMs >= 3_000L && sessionBytes > 0L && downloadedNow < file.sizeBytes) {
                            val ratePerSec = sessionBytes / (elapsedMs / 1000.0)
                            ((file.sizeBytes - downloadedNow) / ratePerSec).toLong().coerceAtLeast(1L)
                        } else null
                    setState(sessionId, file.fileId, SharedFileTransferState.Downloading(downloadedNow, file.sizeBytes, eta))
                    blockIndex++
                }
            }

            val finalContext = currentCoroutineContext()
            synchronized(this) {
                finalContext.ensureActive()
                // Full file fetched: verify SHA-256 over the truncated payload.
                if (expectedHash.size == 32) {
                    val actualHash = sha256File(part, file.sizeBytes)
                    if (!MessageDigest.isEqual(actualHash, expectedHash)) {
                        part.delete()
                        meta.delete()
                        setState(sessionId, file.fileId, SharedFileTransferState.Failed(CoreSharedFileFailureKind.SHARED_FILE_FAILURE_KIND_CHECKSUM_MISMATCH))
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

                val durationMs = durationExtractor(finalFile, file.hasMediaDuration)
                writeMeta(meta, file.sizeBytes, blockBytes, file.sizeBytes, expectedHashHex, durationMs)
                setState(sessionId, file.fileId, SharedFileTransferState.Downloaded(finalFile, durationMs))
                _completions.tryEmit(
                    SharedFileCompletion(
                        sessionId = sessionId,
                        fileId = file.fileId,
                        displayName = file.displayName,
                        sizeBytes = file.sizeBytes,
                        kind = file.kind,
                        localFile = finalFile,
                        userInitiated = userInitiated,
                    ),
                )
            }
        } catch (e: CancellationException) {
            if (controlModes[k] == ControlMode.CANCEL) {
                part.delete()
                meta.delete()
            }
            throw e
        } catch (e: IOException) {
            val kind =
                if (isDiskFull(e)) CoreSharedFileFailureKind.SHARED_FILE_FAILURE_KIND_PHONE_STORAGE_FULL
                else CoreSharedFileFailureKind.SHARED_FILE_FAILURE_KIND_OTHER
            setState(sessionId, file.fileId, SharedFileTransferState.Failed(kind, e.message.orEmpty()))
        }
    }

    private suspend fun waitForReconnect(
        sessionId: String,
        fileId: String,
        downloadedBytes: Long,
        totalBytes: Long,
    ): Boolean {
        val deadline = clock() + SHARED_FILE_RECONNECT_WAIT_MS
        setState(
            sessionId,
            fileId,
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

    private fun setState(sessionId: String, fileId: String, state: SharedFileTransferState) {
        val k = key(sessionId, fileId)
        entryKeys[k] = sessionId to fileId
        _states.update { it + (k to state) }
        emitSingle(sessionId, fileId, state, force = false)
    }

    private fun emitSingle(sessionId: String, fileId: String, state: SharedFileTransferState, force: Boolean) {
        val k = key(sessionId, fileId)
        if (state is SharedFileTransferState.Downloading && !force) {
            val now = clock()
            val percent = if (state.totalBytes > 0L) {
                ((state.downloadedBytes * 100L) / state.totalBytes).toInt().coerceIn(0, 100)
            } else 0
            val prev = lastProgressReport[k]
            val shouldSend = prev == null ||
                now - prev.timeMs >= SHARED_FILE_PROGRESS_THROTTLE_MS ||
                percent != prev.percent ||
                state.waitingReconnect != prev.waitingReconnect ||
                state.etaSeconds != prev.etaSeconds ||
                (state.totalBytes > 0L && state.downloadedBytes >= state.totalBytes)
            if (!shouldSend) return
            lastProgressReport[k] = ProgressStamp(now, percent, state.waitingReconnect, state.etaSeconds)
        } else {
            lastProgressReport.remove(k)
        }
        onLocalStateEvent(
            SharedFileLocalStateEvent.newBuilder()
                .addUpdates(state.toLocalStateUpdate(sessionId, fileId))
                .build(),
        )
    }

    /** Seeds a transfer state directly for deterministic UI and screenshot tests. */
    fun setStateForTest(sessionId: String, fileId: String, state: SharedFileTransferState) {
        setState(sessionId, fileId, state)
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
