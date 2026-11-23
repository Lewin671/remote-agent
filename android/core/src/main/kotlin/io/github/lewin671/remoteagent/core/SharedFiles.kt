package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.proto.SharedFileClearAllEffect
import io.github.lewin671.remoteagent.core.proto.SharedFileDeleteLocalEffect
import io.github.lewin671.remoteagent.core.proto.SharedFileDeletePartialEffect
import io.github.lewin671.remoteagent.core.proto.SharedFileDeleteSessionEffect
import io.github.lewin671.remoteagent.core.proto.SharedFileDiskInspectedEvent
import io.github.lewin671.remoteagent.core.proto.SharedFileIOErrorKind
import io.github.lewin671.remoteagent.core.proto.SharedFileInspectDiskEffect
import io.github.lewin671.remoteagent.core.proto.SharedFileLocalEntry
import io.github.lewin671.remoteagent.core.proto.SharedFileLocalScannedEvent
import io.github.lewin671.remoteagent.core.proto.SharedFileScanLocalEffect
import io.github.lewin671.remoteagent.core.proto.SharedFileVerifyAndCommitEffect
import io.github.lewin671.remoteagent.core.proto.SharedFileVerifyAndCommitResultEvent
import io.github.lewin671.remoteagent.core.proto.SharedFileWriteChunkEffect
import io.github.lewin671.remoteagent.core.proto.SharedFileWriteChunkResultEvent
import java.io.File
import java.io.IOException
import java.io.RandomAccessFile
import java.nio.file.Files
import java.nio.file.StandardCopyOption
import java.security.MessageDigest
import java.util.Locale
import java.util.Properties
import java.util.concurrent.atomic.AtomicBoolean
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.launch

private const val META_FORMAT_VERSION = "2"
private const val PART_FILE_NAME = "download.part"
private const val PARTIAL_META_NAME = "partial.meta"
private const val COMMITTED_META_NAME = "committed.meta"
private const val COMMITTED_FILE_DIR = "file"

/**
 * Pure-JVM platform storage executor for Core-driven shared-file downloads.
 *
 * All storage effects (`ScanLocal`, `InspectDisk`, `WriteChunk`, `VerifyAndCommit`,
 * `DeletePartial`, `DeleteLocal`, `DeleteSession`, `ClearAll`) run strictly sequentially
 * through a single global FIFO queue so directory-level and per-file disk operations
 * never reorder or race. `VerifyAndCommit` is never cancelled mid-flight.
 */
@OptIn(ExperimentalCoroutinesApi::class)
class SharedFileIoExecutor(
    val rootDir: File,
    private val availableBytesProvider: (File) -> Long? = { dir ->
        dir.mkdirs()
        dir.usableSpace.takeIf { it >= 0L }
    },
    private val durationExtractor: (File) -> Long? = { null },
    private val isStorageFullError: (Throwable) -> Boolean = { false },
    private val onLocalDeleted: (sessionId: String?, fileId: String?) -> Unit = { _, _ -> },
    ioDispatcher: CoroutineDispatcher = Dispatchers.IO.limitedParallelism(1),
) {
    private val scope = CoroutineScope(SupervisorJob() + ioDispatcher)
    private val queue = Channel<() -> Unit>(Channel.UNLIMITED)
    private val closed = AtomicBoolean(false)

    init {
        scope.launch {
            for (task in queue) {
                if (closed.get()) break
                try {
                    task()
                } catch (e: CancellationException) {
                    if (closed.get()) break
                } catch (_: Throwable) {
                    // Isolate task errors so the global FIFO queue never dies or crashes the app.
                }
            }
        }
    }

    /** Wires all 8 shared-file storage Effect callbacks on [coreHost] onto the single global FIFO queue. */
    fun attach(coreHost: CoreHost) {
        coreHost.onSharedFileScanLocal = { eff ->
            enqueue {
                val res = try {
                    executeScanLocal(eff)
                } catch (e: Throwable) {
                    SharedFileLocalScannedEvent.newBuilder()
                        .setOpId(eff.opId)
                        .setError(e.message?.takeIf { it.isNotBlank() } ?: e.javaClass.simpleName)
                        .build()
                }
                coreHost.deliverSharedFileLocalScanned(res)
            }
        }
        coreHost.onSharedFileInspectDisk = { eff ->
            enqueue {
                val res = try {
                    executeInspectDisk(eff)
                } catch (e: Throwable) {
                    SharedFileDiskInspectedEvent.newBuilder()
                        .setOpId(eff.opId)
                        .setSessionId(eff.sessionId)
                        .setFileId(eff.fileId)
                        .setKnown(false)
                        .setErrorKind(classifyIoError(e))
                        .setErrorDetail(e.message.orEmpty())
                        .build()
                }
                coreHost.deliverSharedFileDiskInspected(res)
            }
        }
        coreHost.onSharedFileWriteChunk = { eff ->
            enqueue {
                val res = try {
                    executeWriteChunk(eff)
                } catch (e: Throwable) {
                    SharedFileWriteChunkResultEvent.newBuilder()
                        .setOpId(eff.opId)
                        .setSessionId(eff.sessionId)
                        .setFileId(eff.fileId)
                        .setOk(false)
                        .setErrorKind(classifyIoError(e))
                        .setErrorDetail(e.message.orEmpty())
                        .build()
                }
                coreHost.deliverSharedFileWriteChunkResult(res)
            }
        }
        coreHost.onSharedFileVerifyAndCommit = { eff ->
            enqueue {
                val res = try {
                    executeVerifyAndCommit(eff)
                } catch (e: Throwable) {
                    SharedFileVerifyAndCommitResultEvent.newBuilder()
                        .setOpId(eff.opId)
                        .setSessionId(eff.sessionId)
                        .setFileId(eff.fileId)
                        .setOk(false)
                        .setErrorKind(classifyIoError(e))
                        .setErrorDetail(e.message.orEmpty())
                        .build()
                }
                coreHost.deliverSharedFileVerifyAndCommitResult(res)
            }
        }
        coreHost.onSharedFileDeletePartial = { eff ->
            enqueue { executeDeletePartial(eff) }
        }
        coreHost.onSharedFileDeleteLocal = { eff ->
            enqueue { executeDeleteLocal(eff) }
        }
        coreHost.onSharedFileDeleteSession = { eff ->
            enqueue { executeDeleteSession(eff) }
        }
        coreHost.onSharedFileClearAll = { eff ->
            enqueue { executeClearAll(eff) }
        }
    }

    fun localFilePath(sessionId: String, fileId: String, displayName: String): File =
        File(File(fileDir(sessionId, fileId), COMMITTED_FILE_DIR), safeLocalFileName(displayName))

    fun localFileIfDownloaded(sessionId: String, fileId: String, displayName: String = ""): File? {
        val dir = fileDir(sessionId, fileId)
        val resolvedName = displayName.takeIf { it.isNotEmpty() }
            ?: readMeta(committedMetaFile(dir))?.getProperty("display_name")?.takeIf { it.isNotEmpty() }
            ?: return null
        return File(File(dir, COMMITTED_FILE_DIR), safeLocalFileName(resolvedName)).takeIf { it.isFile }
    }

    /** Persists a newly probed positive media duration into `committed.meta` via the global FIFO queue. */
    fun enqueuePersistDuration(sessionId: String, fileId: String, durationMs: Long) {
        if (sessionId.isEmpty() || fileId.isEmpty() || durationMs <= 0L) return
        enqueue {
            val dir = fileDir(sessionId, fileId)
            val metaFile = committedMetaFile(dir)
            val props = readMeta(metaFile) ?: return@enqueue
            if (props.getProperty("format") != META_FORMAT_VERSION) return@enqueue
            if (props.getProperty("duration_ms")?.toLongOrNull() == durationMs) return@enqueue
            props.setProperty("duration_ms", durationMs.toString())
            runCatching { writePropertiesAtomic(metaFile, props) }
        }
    }

    /** Enqueues a direct session directory cleanup onto the global FIFO queue (fallback when no Core is attached). */
    fun enqueueDeleteSession(sessionId: String) {
        if (sessionId.isEmpty()) return
        enqueue {
            executeDeleteSession(SharedFileDeleteSessionEffect.newBuilder().setSessionId(sessionId).build())
        }
    }

    /** Enqueues a direct wipe of all cached shared files onto the global FIFO queue. */
    fun enqueueClearAll() {
        enqueue { executeClearAll(SharedFileClearAllEffect.getDefaultInstance()) }
    }

    /** Enqueues a direct wipe of all cached shared files onto the global FIFO queue and suspends until done. */
    suspend fun clearAllAndWait() {
        val done = CompletableDeferred<Unit>()
        if (closed.get() || queue.trySend {
            try {
                executeClearAll(SharedFileClearAllEffect.getDefaultInstance())
            } finally {
                done.complete(Unit)
            }
        }.isFailure) {
            return
        }
        done.await()
    }

    /** Suspends until all previously enqueued FIFO storage operations have completed. */
    suspend fun awaitIdle() {
        val barrier = CompletableDeferred<Unit>()
        if (closed.get() || queue.trySend { barrier.complete(Unit) }.isFailure) return
        barrier.await()
    }

    /** Writes a valid v2 committed file + metadata directly on disk for deterministic tests. */
    fun seedCommittedFileForTest(
        sessionId: String,
        fileId: String,
        displayName: String,
        bytes: ByteArray,
        durationMs: Long? = null,
    ): File {
        val dir = fileDir(sessionId, fileId)
        val fileDir = File(dir, COMMITTED_FILE_DIR).apply { mkdirs() }
        val finalFile = File(fileDir, safeLocalFileName(displayName)).apply { writeBytes(bytes) }
        val props = Properties().apply {
            setProperty("format", META_FORMAT_VERSION)
            setProperty("session_id", sessionId)
            setProperty("file_id", fileId)
            setProperty("display_name", displayName)
            setProperty("total_size_bytes", bytes.size.toLong().toString())
            if (durationMs != null && durationMs > 0L) {
                setProperty("duration_ms", durationMs.toString())
            }
        }
        writePropertiesAtomic(committedMetaFile(dir), props)
        return finalFile
    }

    fun usedBytes(): Long = ImageDiskPolicy.bytes(rootDir)

    fun close() {
        if (closed.compareAndSet(false, true)) {
            queue.close()
            scope.cancel()
        }
    }

    private fun enqueue(task: () -> Unit) {
        if (closed.get()) return
        queue.trySend(task)
    }

    // --- Effect execution (always invoked sequentially on the single FIFO worker) ---

    internal fun executeScanLocal(eff: SharedFileScanLocalEffect): SharedFileLocalScannedEvent {
        val builder = SharedFileLocalScannedEvent.newBuilder().setOpId(eff.opId)
        if (!rootDir.exists()) return builder.build()
        return try {
            val sessionDirs = rootDir.listFiles().orEmpty()
            for (sidDir in sessionDirs) {
                if (!sidDir.isDirectory) continue
                val fileDirs = sidDir.listFiles().orEmpty()
                for (fidDir in fileDirs) {
                    if (!fidDir.isDirectory) continue
                    scanFileDir(sidDir.name, fidDir)?.let { builder.addEntries(it) }
                }
            }
            builder.build()
        } catch (e: Throwable) {
            SharedFileLocalScannedEvent.newBuilder()
                .setOpId(eff.opId)
                .setError(e.message?.takeIf { it.isNotBlank() } ?: e.javaClass.simpleName)
                .build()
        }
    }

    private fun scanFileDir(fallbackSid: String, fidDir: File): SharedFileLocalEntry? {
        val fallbackFid = fidDir.name
        val committedMeta = committedMetaFile(fidDir)
        if (committedMeta.exists()) {
            val props = readMeta(committedMeta)
            val format = props?.getProperty("format")
            val sid = props?.getProperty("session_id").orEmpty()
            val fid = props?.getProperty("file_id").orEmpty()
            val displayName = props?.getProperty("display_name").orEmpty()
            val expectedSize = props?.getProperty("total_size_bytes")?.toLongOrNull()
            val durationMs = props?.getProperty("duration_ms")?.toLongOrNull()
            if (
                format == META_FORMAT_VERSION &&
                sid.isNotEmpty() &&
                fid.isNotEmpty() &&
                displayName.isNotEmpty() &&
                expectedSize != null &&
                expectedSize >= 0L
            ) {
                val finalFile = File(File(fidDir, COMMITTED_FILE_DIR), safeLocalFileName(displayName))
                if (finalFile.isFile) {
                    return SharedFileLocalEntry.newBuilder()
                        .setSessionId(sid)
                        .setFileId(fid)
                        .setCompleted(true)
                        .setBytesOnDisk(finalFile.length())
                        .setExpectedSizeBytes(expectedSize)
                        .apply {
                            if (durationMs != null && durationMs > 0L) {
                                setHasDuration(true).setDurationMs(durationMs)
                            }
                        }
                        .build()
                }
            }
            // Corrupt/legacy committed.meta or evicted committed file: report malformed completed
            // entry truthfully so Core decides and emits SharedFileDeleteLocalEffect.
            return SharedFileLocalEntry.newBuilder()
                .setSessionId(sid.ifEmpty { fallbackSid })
                .setFileId(fid.ifEmpty { fallbackFid })
                .setCompleted(true)
                .setBytesOnDisk(-1L)
                .build()
        }

        // Crash between renameTo(finalFile) and writing committed.meta leaves an orphan file/ directory.
        // Report it truthfully as a malformed completed entry so Core emits SharedFileDeleteLocalEffect.
        val orphanFileDir = File(fidDir, COMMITTED_FILE_DIR)
        if (orphanFileDir.isDirectory && orphanFileDir.listFiles().orEmpty().isNotEmpty()) {
            return SharedFileLocalEntry.newBuilder()
                .setSessionId(fallbackSid)
                .setFileId(fallbackFid)
                .setCompleted(true)
                .setBytesOnDisk(-1L)
                .build()
        }

        val partialMeta = partialMetaFile(fidDir)
        if (partialMeta.exists()) {
            val props = readMeta(partialMeta)
            val format = props?.getProperty("format")
            val sid = props?.getProperty("session_id").orEmpty()
            val fid = props?.getProperty("file_id").orEmpty()
            val part = partFile(fidDir)
            val bytesOnDisk = if (part.isFile) part.length() else 0L
            if (format == META_FORMAT_VERSION && sid.isNotEmpty() && fid.isNotEmpty()) {
                val expectedSize = props?.getProperty("total_size_bytes")?.toLongOrNull() ?: 0L
                val chunkBytes = props?.getProperty("chunk_bytes")?.toIntOrNull() ?: 0
                val committedOffset = props?.getProperty("committed_offset")?.toLongOrNull() ?: 0L
                val sha256Bytes = decodeHex32(props?.getProperty("expected_sha256").orEmpty())
                return SharedFileLocalEntry.newBuilder()
                    .setSessionId(sid)
                    .setFileId(fid)
                    .setCompleted(false)
                    .setBytesOnDisk(bytesOnDisk)
                    .setExpectedSizeBytes(expectedSize)
                    .setChunkBytes(chunkBytes)
                    .setCommittedOffset(committedOffset)
                    .setSha256(ByteString.copyFrom(sha256Bytes))
                    .build()
            }
            return SharedFileLocalEntry.newBuilder()
                .setSessionId(sid.ifEmpty { fallbackSid })
                .setFileId(fid.ifEmpty { fallbackFid })
                .setCompleted(false)
                .setBytesOnDisk(bytesOnDisk)
                .build()
        }

        // Legacy/unrecognized format (e.g. old meta.properties) or orphan download.part:
        // report truthfully as malformed partial so Core emits SharedFileDeletePartialEffect.
        val children = fidDir.listFiles().orEmpty()
        if (children.isNotEmpty()) {
            val part = partFile(fidDir)
            val bytesOnDisk = if (part.isFile) part.length() else 0L
            return SharedFileLocalEntry.newBuilder()
                .setSessionId(fallbackSid)
                .setFileId(fallbackFid)
                .setCompleted(false)
                .setBytesOnDisk(bytesOnDisk)
                .build()
        }
        return null
    }

    internal fun executeInspectDisk(eff: SharedFileInspectDiskEffect): SharedFileDiskInspectedEvent {
        val builder = SharedFileDiskInspectedEvent.newBuilder()
            .setOpId(eff.opId)
            .setSessionId(eff.sessionId)
            .setFileId(eff.fileId)
        return try {
            val avail = availableBytesProvider(rootDir)
            if (avail != null) {
                builder.setKnown(true).setAvailableBytes(avail.coerceAtLeast(0L)).build()
            } else {
                builder.setKnown(false).build()
            }
        } catch (e: Throwable) {
            val kind = classifyIoError(e)
            if (kind == SharedFileIOErrorKind.SHARED_FILE_IO_ERROR_KIND_STORAGE_FULL) {
                builder
                    .setKnown(false)
                    .setErrorKind(kind)
                    .setErrorDetail(e.message.orEmpty())
                    .build()
            } else {
                // Non-ENOSPC probe failures fall back to write-time enforcement.
                builder.setKnown(false).build()
            }
        }
    }

    internal fun executeWriteChunk(eff: SharedFileWriteChunkEffect): SharedFileWriteChunkResultEvent {
        val builder = SharedFileWriteChunkResultEvent.newBuilder()
            .setOpId(eff.opId)
            .setSessionId(eff.sessionId)
            .setFileId(eff.fileId)
        return try {
            val dir = fileDir(eff.sessionId, eff.fileId)
            if (!dir.exists() && !dir.mkdirs() && !dir.isDirectory) {
                throw IOException("failed to create directory ${dir.path}")
            }
            val part = partFile(dir)
            val data = eff.data.toByteArray()
            if (eff.resetPartial && part.exists()) {
                part.delete()
            }
            RandomAccessFile(part, "rw").use { raf ->
                if (eff.resetPartial) raf.setLength(0L)
                raf.seek(eff.offset)
                if (data.isNotEmpty()) raf.write(data)
                raf.setLength(eff.offset + data.size.toLong())
            }
            val props = Properties().apply {
                setProperty("format", META_FORMAT_VERSION)
                setProperty("session_id", eff.sessionId)
                setProperty("file_id", eff.fileId)
                setProperty("display_name", eff.displayName)
                setProperty("total_size_bytes", eff.totalSizeBytes.toString())
                setProperty("chunk_bytes", eff.chunkBytes.toString())
                setProperty("committed_offset", eff.committedOffset.toString())
                setProperty("expected_sha256", eff.expectedSha256.toByteArray().toHex())
            }
            writePropertiesAtomic(partialMetaFile(dir), props)
            builder.setOk(true).build()
        } catch (e: Throwable) {
            builder
                .setOk(false)
                .setErrorKind(classifyIoError(e))
                .setErrorDetail(e.message.orEmpty())
                .build()
        }
    }

    internal fun executeVerifyAndCommit(eff: SharedFileVerifyAndCommitEffect): SharedFileVerifyAndCommitResultEvent {
        val builder = SharedFileVerifyAndCommitResultEvent.newBuilder()
            .setOpId(eff.opId)
            .setSessionId(eff.sessionId)
            .setFileId(eff.fileId)
        val dir = fileDir(eff.sessionId, eff.fileId)
        val part = partFile(dir)
        val expectedHash = eff.expectedSha256.toByteArray()
        val totalBytes = eff.totalSizeBytes

        if (expectedHash.size != 32 || totalBytes < 0L) {
            deletePartialFiles(dir)
            pruneEmptyDirs(dir)
            return builder
                .setOk(false)
                .setErrorKind(SharedFileIOErrorKind.SHARED_FILE_IO_ERROR_KIND_CHECKSUM_MISMATCH)
                .build()
        }
        if (totalBytes > 0L && (!part.isFile || part.length() < totalBytes)) {
            deletePartialFiles(dir)
            pruneEmptyDirs(dir)
            return builder
                .setOk(false)
                .setErrorKind(SharedFileIOErrorKind.SHARED_FILE_IO_ERROR_KIND_CHECKSUM_MISMATCH)
                .build()
        }

        val actualHash = try {
            sha256File(part, totalBytes)
        } catch (e: Throwable) {
            return builder
                .setOk(false)
                .setErrorKind(classifyIoError(e))
                .setErrorDetail(e.message.orEmpty())
                .build()
        }

        if (actualHash == null || !MessageDigest.isEqual(actualHash, expectedHash)) {
            deletePartialFiles(dir)
            pruneEmptyDirs(dir)
            return builder
                .setOk(false)
                .setErrorKind(SharedFileIOErrorKind.SHARED_FILE_IO_ERROR_KIND_CHECKSUM_MISMATCH)
                .build()
        }

        if (eff.deliverOnly) {
            deletePartialFiles(dir)
            pruneEmptyDirs(dir)
            return builder.setOk(true).build()
        }

        return try {
            val fileSubDir = File(dir, COMMITTED_FILE_DIR)
            if (!fileSubDir.exists() && !fileSubDir.mkdirs() && !fileSubDir.isDirectory) {
                throw IOException("failed to create committed directory ${fileSubDir.path}")
            }
            val safeName = safeLocalFileName(eff.displayName)
            val finalFile = File(fileSubDir, safeName)

            if (totalBytes > 0L && part.length() > totalBytes) {
                RandomAccessFile(part, "rw").use { it.setLength(totalBytes) }
            }

            // Remove any prior committed file under a different filename.
            fileSubDir.listFiles()?.forEach { existing ->
                if (existing != finalFile) existing.deleteRecursively()
            }

            var movedFromPart = false
            if (totalBytes == 0L && !part.exists()) {
                finalFile.writeBytes(ByteArray(0))
            } else {
                if (finalFile.exists()) finalFile.delete()
                if (part.renameTo(finalFile)) {
                    movedFromPart = true
                } else {
                    val tmpTarget = File(fileSubDir, "$safeName.tmp")
                    try {
                        part.inputStream().use { input ->
                            tmpTarget.outputStream().use { output -> input.copyTo(output) }
                        }
                        if (!tmpTarget.renameTo(finalFile)) {
                            Files.move(tmpTarget.toPath(), finalFile.toPath(), StandardCopyOption.REPLACE_EXISTING)
                        }
                    } catch (e: Throwable) {
                        tmpTarget.delete()
                        throw e
                    }
                }
            }

            val durationMs = if (eff.probeDuration) {
                runCatching { durationExtractor(finalFile) }.getOrNull()?.takeIf { it > 0L }
            } else {
                null
            }

            val props = Properties().apply {
                setProperty("format", META_FORMAT_VERSION)
                setProperty("session_id", eff.sessionId)
                setProperty("file_id", eff.fileId)
                setProperty("display_name", eff.displayName)
                setProperty("total_size_bytes", totalBytes.toString())
                if (durationMs != null) {
                    setProperty("duration_ms", durationMs.toString())
                }
            }
            try {
                writePropertiesAtomic(committedMetaFile(dir), props)
            } catch (e: Throwable) {
                // Roll back so partial chunks stay resumable if disk filled right on meta write.
                if (movedFromPart && !part.exists() && finalFile.isFile) {
                    finalFile.renameTo(part)
                } else {
                    finalFile.delete()
                }
                throw e
            }

            deletePartialFiles(dir)
            builder.setOk(true).apply {
                if (durationMs != null) {
                    setHasDuration(true).setDurationMs(durationMs)
                }
            }.build()
        } catch (e: Throwable) {
            builder
                .setOk(false)
                .setErrorKind(classifyIoError(e))
                .setErrorDetail(e.message.orEmpty())
                .build()
        }
    }

    internal fun executeDeletePartial(eff: SharedFileDeletePartialEffect) {
        val dir = fileDir(eff.sessionId, eff.fileId)
        deletePartialFiles(dir)
        pruneEmptyDirs(dir)
    }

    internal fun executeDeleteLocal(eff: SharedFileDeleteLocalEffect) {
        val dir = fileDir(eff.sessionId, eff.fileId)
        dir.deleteRecursively()
        pruneEmptySessionDir(dir.parentFile)
        onLocalDeleted(eff.sessionId, eff.fileId)
    }

    internal fun executeDeleteSession(eff: SharedFileDeleteSessionEffect) {
        sessionDir(eff.sessionId).deleteRecursively()
        onLocalDeleted(eff.sessionId, null)
    }

    internal fun executeClearAll(@Suppress("UNUSED_PARAMETER") eff: SharedFileClearAllEffect) {
        rootDir.listFiles()?.forEach { it.deleteRecursively() }
        onLocalDeleted(null, null)
    }

    // --- Helpers ---

    private fun safeSegment(raw: String): String =
        raw.replace(Regex("[^A-Za-z0-9_-]"), "_").ifEmpty { "_" }

    private fun sessionDir(sessionId: String): File = File(rootDir, safeSegment(sessionId))

    private fun fileDir(sessionId: String, fileId: String): File =
        File(sessionDir(sessionId), safeSegment(fileId))

    private fun partFile(dir: File): File = File(dir, PART_FILE_NAME)

    private fun partialMetaFile(dir: File): File = File(dir, PARTIAL_META_NAME)

    private fun committedMetaFile(dir: File): File = File(dir, COMMITTED_META_NAME)

    private fun deletePartialFiles(dir: File) {
        partFile(dir).delete()
        partialMetaFile(dir).delete()
        File(dir, "$PARTIAL_META_NAME.tmp").delete()
    }

    private fun pruneEmptyDirs(fidDir: File) {
        if (fidDir.isDirectory && fidDir.listFiles().isNullOrEmpty()) {
            fidDir.delete()
            pruneEmptySessionDir(fidDir.parentFile)
        }
    }

    private fun pruneEmptySessionDir(sidDir: File?) {
        if (sidDir != null && sidDir != rootDir && sidDir.isDirectory && sidDir.listFiles().isNullOrEmpty()) {
            sidDir.delete()
        }
    }

    private fun classifyIoError(t: Throwable): SharedFileIOErrorKind {
        var cur: Throwable? = t
        while (cur != null) {
            if (isStorageFullError(cur)) return SharedFileIOErrorKind.SHARED_FILE_IO_ERROR_KIND_STORAGE_FULL
            val msg = cur.message?.lowercase(Locale.ROOT).orEmpty()
            if (
                "enospc" in msg ||
                "no space" in msg ||
                "disk full" in msg ||
                "edquot" in msg ||
                "quota exceeded" in msg
            ) {
                return SharedFileIOErrorKind.SHARED_FILE_IO_ERROR_KIND_STORAGE_FULL
            }
            cur = cur.cause
        }
        return SharedFileIOErrorKind.SHARED_FILE_IO_ERROR_KIND_OTHER
    }

    private fun readMeta(file: File): Properties? {
        if (!file.isFile) return null
        return runCatching {
            Properties().apply {
                file.inputStream().use { load(it) }
            }
        }.getOrNull()
    }

    private fun writePropertiesAtomic(target: File, props: Properties) {
        val parent = target.parentFile
        if (parent != null && !parent.exists()) parent.mkdirs()
        val tmp = File(parent, "${target.name}.tmp")
        try {
            tmp.outputStream().use { props.store(it, null) }
            try {
                Files.move(
                    tmp.toPath(),
                    target.toPath(),
                    StandardCopyOption.REPLACE_EXISTING,
                    StandardCopyOption.ATOMIC_MOVE,
                )
            } catch (_: Exception) {
                Files.move(tmp.toPath(), target.toPath(), StandardCopyOption.REPLACE_EXISTING)
            }
        } catch (e: Throwable) {
            tmp.delete()
            throw e
        }
    }

    private fun sha256File(file: File, exactBytes: Long): ByteArray? {
        val md = MessageDigest.getInstance("SHA-256")
        if (exactBytes == 0L) return md.digest()
        val buf = ByteArray(8192)
        var remaining = exactBytes
        file.inputStream().use { input ->
            while (remaining > 0L) {
                val read = input.read(buf, 0, minOf(buf.size.toLong(), remaining).toInt())
                if (read <= 0) return null
                md.update(buf, 0, read)
                remaining -= read
            }
        }
        return md.digest()
    }

    companion object {
        fun safeLocalFileName(displayName: String): String =
            displayName.substringAfterLast('/').substringAfterLast('\\').trim().takeIf {
                it.isNotEmpty() && it != "." && it != ".."
            } ?: "file"

        private fun decodeHex32(hex: String): ByteArray {
            if (hex.length != 64) return ByteArray(0)
            return try {
                ByteArray(32) { i -> hex.substring(i * 2, i * 2 + 2).toInt(16).toByte() }
            } catch (_: NumberFormatException) {
                ByteArray(0)
            }
        }

        private fun ByteArray.toHex(): String = joinToString("") { "%02x".format(it) }
    }
}
