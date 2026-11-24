package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.proto.AttachmentHashFileEffect
import io.github.lewin671.remoteagent.core.proto.AttachmentHashFileResultEvent
import io.github.lewin671.remoteagent.core.proto.AttachmentIOErrorKind
import io.github.lewin671.remoteagent.core.proto.AttachmentItemKind
import io.github.lewin671.remoteagent.core.proto.AttachmentPrepareImageEffect
import io.github.lewin671.remoteagent.core.proto.AttachmentPrepareImageResultEvent
import io.github.lewin671.remoteagent.core.proto.AttachmentReadChunkEffect
import io.github.lewin671.remoteagent.core.proto.AttachmentReadChunkResultEvent
import io.github.lewin671.remoteagent.core.proto.AttachmentReleaseLocalEffect
import io.github.lewin671.remoteagent.core.proto.DraftScope
import io.github.lewin671.remoteagent.core.proto.DraftScopeKind
import io.github.lewin671.remoteagent.core.proto.DraftStorageBucket
import io.github.lewin671.remoteagent.core.proto.DraftStorageClearAllEffect
import io.github.lewin671.remoteagent.core.proto.DraftStorageDeleteEffect
import io.github.lewin671.remoteagent.core.proto.DraftStorageEntry
import io.github.lewin671.remoteagent.core.proto.DraftStorageListEffect
import io.github.lewin671.remoteagent.core.proto.DraftStorageListedEvent
import io.github.lewin671.remoteagent.core.proto.DraftStorageReadEffect
import io.github.lewin671.remoteagent.core.proto.DraftStorageReadResultEvent
import io.github.lewin671.remoteagent.core.proto.DraftStorageWriteEffect
import io.github.lewin671.remoteagent.core.proto.DraftStorageWriteResultEvent
import io.github.lewin671.remoteagent.proto.ImageFormat
import java.io.ByteArrayOutputStream
import java.io.EOFException
import java.io.File
import java.io.FileNotFoundException
import java.io.IOException
import java.io.RandomAccessFile
import java.nio.ByteBuffer
import java.nio.file.Files
import java.nio.file.StandardCopyOption
import java.security.GeneralSecurityException
import java.util.UUID
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicLong
import javax.crypto.Cipher
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.SecretKeySpec
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

private const val DRAFTS_DIR_NAME = "drafts"
private const val OUTBOX_DIR_NAME = "outbox"
private const val BLOBS_DIR_NAME = "blobs"
private const val KEY_FILE_NAME = "key.bin"
private const val RECORD_SUFFIX = ".rec"
private const val CORRUPT_KEY_PREFIX = "__corrupt__:"
const val LOCAL_BLOB_REF_PREFIX = "blob:"

private val BLOB_MAGIC = byteArrayOf('R'.code.toByte(), 'A'.code.toByte(), 'B'.code.toByte(), '1'.code.toByte())
private const val BLOB_HEADER_BYTES = 16
private const val BLOB_CHUNK_PLAIN_BYTES = 256 * 1024
private const val GCM_IV_BYTES = 12
private const val GCM_TAG_BYTES = 16
private const val GCM_TAG_BITS = 128

fun sessionDraftScope(sessionId: String): DraftScope =
    DraftScope.newBuilder()
        .setKind(DraftScopeKind.DRAFT_SCOPE_KIND_SESSION)
        .setId(sessionId)
        .build()

fun newSessionDraftScope(projectKey: String): DraftScope =
    DraftScope.newBuilder()
        .setKind(DraftScopeKind.DRAFT_SCOPE_KIND_NEW_SESSION)
        .setId(projectKey)
        .build()

fun draftScopeStorageKey(scope: DraftScope?): String {
    if (scope == null || scope.id.isEmpty()) return ""
    return when (scope.kind) {
        DraftScopeKind.DRAFT_SCOPE_KIND_SESSION -> "session:${scope.id}"
        DraftScopeKind.DRAFT_SCOPE_KIND_NEW_SESSION -> "new:${scope.id}"
        else -> ""
    }
}

class AttachmentStorageFullException : IOException()

/**
 * Thrown when persisted attachment key material exists (or Keystore itself failed transiently)
 * and cannot currently be read/unwrapped. Callers must preserve on-disk records/blobs and never
 * overwrite `key.bin` so the next startup can recover cleanly.
 */
class AttachmentKeyUnavailableException(message: String, cause: Throwable? = null) : IOException(message, cause)

/**
 * Provides the 32-byte AES-256 key used to seal persisted drafts, outbox records, and prepared
 * image/thumbnail blobs. On Android this is backed by Android KeyStore wrapping `key.bin`; in JVM
 * unit tests an in-memory key provider can be injected.
 */
interface AttachmentStorageKeyProvider {
    /**
     * Returns the existing key if present and openable, or `null` ONLY when no key exists yet.
     * Throws [AttachmentKeyUnavailableException] when Keystore/decryption fails transiently.
     */
    fun loadExistingKey(): ByteArray?

    /**
     * Returns an existing key or generates and persists a fresh 32-byte AES key when none exists.
     * Throws [AttachmentKeyUnavailableException] if existing key material cannot be read (never overwrites).
     */
    fun ensureKey(): ByteArray

    /** Deletes any persisted key material and zeroes in-memory copies. */
    fun clearKey()
}

/** Simple in-memory key provider for deterministic pure-JVM unit tests. */
class InMemoryAttachmentKeyProvider(initialKey: ByteArray? = null) : AttachmentStorageKeyProvider {
    @Volatile private var key: ByteArray? = initialKey?.copyOf()

    @Synchronized override fun loadExistingKey(): ByteArray? = key?.copyOf()

    @Synchronized override fun ensureKey(): ByteArray {
        key?.let { return it.copyOf() }
        val fresh = randomBytes(32)
        key = fresh
        return fresh.copyOf()
    }

    @Synchronized override fun clearKey() {
        key?.fill(0)
        key = null
    }
}

data class PreparedImageOutcome(
    val ok: Boolean,
    val preparedRef: String = "",
    val thumbRef: String = "",
    val format: ImageFormat = ImageFormat.IMAGE_FORMAT_UNSPECIFIED,
    val width: Int = 0,
    val height: Int = 0,
    val sizeBytes: Long = 0L,
    val sha256: ByteArray = ByteArray(0),
    val errorKind: AttachmentIOErrorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_UNSPECIFIED,
    val errorCode: String = "",
    val errorDetail: String = "",
    val isReadingSource: Boolean = false,
)

data class HashedFileOutcome(
    val ok: Boolean,
    val sizeBytes: Long = 0L,
    val sha256: ByteArray = ByteArray(0),
    val updatedSourceRef: String = "",
    val hasDuration: Boolean = false,
    val durationMs: Long = 0L,
    val errorKind: AttachmentIOErrorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_UNSPECIFIED,
    val errorDetail: String = "",
)

data class ReadChunkOutcome(
    val ok: Boolean,
    val data: ByteArray = ByteArray(0),
    val errorKind: AttachmentIOErrorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_UNSPECIFIED,
    val errorDetail: String = "",
)

/** Platform media & content-URI operations injected by `:app` (or test doubles in `:core:test`). */
interface AttachmentPlatformIo {
    fun prepareImage(
        eff: AttachmentPrepareImageEffect,
        storeBlob: (suffix: String, bytes: ByteArray) -> String,
    ): PreparedImageOutcome

    fun hashFile(eff: AttachmentHashFileEffect): HashedFileOutcome

    fun readFileChunk(eff: AttachmentReadChunkEffect): ReadChunkOutcome

    /** External local handles (e.g., Android persisted SAF URI permissions) included in orphan GC. */
    fun listExternalLocalRefs(): List<String> = emptyList()

    /** Releases one unreferenced external handle (e.g., revokes a persisted SAF URI grant). */
    fun releaseExternalRef(ref: String) {}

    /** Releases all external handles on full clear / logout. */
    fun releaseAllExternalRefs() {}

    /** Platform check for OS-level out-of-space errors (e.g., `ENOSPC`/`EDQUOT`). */
    fun isStorageFullError(t: Throwable): Boolean = false

    fun onLocalBlobDeleted(ref: String) {}

    fun onAllCleared() {}
}

/**
 * Pure-JVM platform storage & attachment I/O executor for Core-driven attachment uploads,
 * composer drafts, and outbox persistence (Step 3C-1).
 *
 * Strictly executes all 6 storage/release Effects (`DraftStorageList`, `DraftStorageRead`,
 * `DraftStorageWrite`, `DraftStorageDelete`, `DraftStorageClearAll`, `AttachmentReleaseLocal`)
 * through a single global FIFO queue so write barriers and local handle releases never reorder.
 * Media/chunk work (`PrepareImage`, `HashFile`, `ReadChunk`) runs off the storage FIFO queue and
 * always delivers exactly one result event with the original `op_id`.
 */
@OptIn(ExperimentalCoroutinesApi::class)
class AttachmentIoExecutor(
    val rootDir: File,
    private val keyProvider: AttachmentStorageKeyProvider,
    private val platformIo: AttachmentPlatformIo,
    private val draftLimitBytes: Long = ImageDiskPolicy.DRAFT_LIMIT,
    storageDispatcher: CoroutineDispatcher = Dispatchers.IO.limitedParallelism(1),
    workDispatcher: CoroutineDispatcher = Dispatchers.IO,
) {
    private val storageScope = CoroutineScope(SupervisorJob() + storageDispatcher)
    private val workScope = CoroutineScope(SupervisorJob() + workDispatcher)
    private val fifo = Channel<() -> Unit>(Channel.UNLIMITED)
    private val closed = AtomicBoolean(false)
    private val clearEpoch = AtomicLong(0L)

    init {
        storageScope.launch {
            for (task in fifo) {
                if (closed.get()) break
                try {
                    task()
                } catch (e: CancellationException) {
                    if (closed.get()) break
                } catch (_: Throwable) {
                    // Isolate unexpected exceptions so the global FIFO queue never dies.
                }
            }
        }
    }

    /** Wires all 9 Core attachment & draft storage Effect callbacks on [coreHost]. */
    fun attach(coreHost: CoreHost) {
        coreHost.onDraftStorageList = { eff ->
            enqueueStorage {
                val res = try {
                    executeList(eff)
                } catch (t: Throwable) {
                    DraftStorageListedEvent.newBuilder()
                        .setOpId(eff.opId)
                        .setErrorKind(classifyIoError(t))
                        .setErrorDetail(t.message.orEmpty())
                        .build()
                }
                coreHost.deliverDraftStorageListed(res)
            }
        }
        coreHost.onDraftStorageRead = { eff ->
            enqueueStorage {
                val res = try {
                    executeRead(eff)
                } catch (t: Throwable) {
                    DraftStorageReadResultEvent.newBuilder()
                        .setOpId(eff.opId)
                        .setBucket(eff.bucket)
                        .setKey(eff.key)
                        .setFound(false)
                        .setErrorKind(classifyIoError(t))
                        .setErrorDetail(t.message.orEmpty())
                        .build()
                }
                coreHost.deliverDraftStorageReadResult(res)
            }
        }
        coreHost.onDraftStorageWrite = { eff ->
            enqueueStorage {
                val res = try {
                    executeWrite(eff)
                } catch (t: Throwable) {
                    DraftStorageWriteResultEvent.newBuilder()
                        .setOpId(eff.opId)
                        .setBucket(eff.bucket)
                        .setKey(eff.key)
                        .setOk(false)
                        .setErrorKind(classifyIoError(t))
                        .setErrorDetail(t.message.orEmpty())
                        .build()
                }
                coreHost.deliverDraftStorageWriteResult(res)
            }
        }
        coreHost.onDraftStorageDelete = { eff ->
            enqueueStorage { executeDelete(eff) }
        }
        coreHost.onDraftStorageClearAll = { eff ->
            enqueueStorage { executeClearAll(eff) }
        }
        coreHost.onAttachmentReleaseLocal = { eff ->
            enqueueStorage { executeReleaseLocal(eff) }
        }
        coreHost.onAttachmentPrepareImage = { eff ->
            workScope.launch {
                val res = try {
                    executePrepareImage(eff)
                } catch (t: Throwable) {
                    AttachmentPrepareImageResultEvent.newBuilder()
                        .setOpId(eff.opId)
                        .setScope(eff.scope)
                        .setOutboxRequestId(eff.outboxRequestId)
                        .setItemId(eff.itemId)
                        .setOk(false)
                        .setErrorKind(classifyIoError(t))
                        .setErrorDetail(t.message.orEmpty())
                        .build()
                }
                if (!closed.get()) coreHost.deliverAttachmentPrepareImageResult(res)
            }
        }
        coreHost.onAttachmentHashFile = { eff ->
            workScope.launch {
                val res = try {
                    executeHashFile(eff)
                } catch (t: Throwable) {
                    AttachmentHashFileResultEvent.newBuilder()
                        .setOpId(eff.opId)
                        .setScope(eff.scope)
                        .setOutboxRequestId(eff.outboxRequestId)
                        .setItemId(eff.itemId)
                        .setOk(false)
                        .setErrorKind(classifyIoError(t))
                        .setErrorDetail(t.message.orEmpty())
                        .build()
                }
                if (!closed.get()) coreHost.deliverAttachmentHashFileResult(res)
            }
        }
        coreHost.onAttachmentReadChunk = { eff ->
            workScope.launch {
                val res = try {
                    executeReadChunk(eff)
                } catch (t: Throwable) {
                    AttachmentReadChunkResultEvent.newBuilder()
                        .setOpId(eff.opId)
                        .setScope(eff.scope)
                        .setOutboxRequestId(eff.outboxRequestId)
                        .setItemId(eff.itemId)
                        .setOk(false)
                        .setOffset(eff.offset)
                        .setErrorKind(classifyIoError(t))
                        .setErrorDetail(t.message.orEmpty())
                        .build()
                }
                if (!closed.get()) coreHost.deliverAttachmentReadChunkResult(res)
            }
        }
    }

    /** Enqueues a full wipe of all persisted drafts, outbox records, blobs, key, and URI grants. */
    fun enqueueClearAll() {
        enqueueStorage { executeClearAll(DraftStorageClearAllEffect.getDefaultInstance(), wipeKey = true) }
    }

    /** Wipes all persisted drafts, outbox records, blobs, key, and URI grants on the FIFO queue and suspends until done. */
    suspend fun clearAllAndWait() {
        val done = CompletableDeferred<Unit>()
        if (closed.get() || fifo.trySend {
            try {
                executeClearAll(DraftStorageClearAllEffect.getDefaultInstance(), wipeKey = true)
            } finally {
                done.complete(Unit)
            }
        }.isFailure) {
            return
        }
        done.await()
    }

    /** Suspends until all previously enqueued FIFO storage operations have completed. */
    suspend fun awaitStorageIdle() {
        val barrier = CompletableDeferred<Unit>()
        if (closed.get() || fifo.trySend { barrier.complete(Unit) }.isFailure) return
        barrier.await()
    }

    fun usedBytes(): Long = if (rootDir.exists()) ImageDiskPolicy.bytes(rootDir) else 0L

    fun close() {
        if (closed.compareAndSet(false, true)) {
            fifo.close()
            storageScope.cancel()
            workScope.cancel()
        }
    }

    private fun enqueueStorage(task: () -> Unit) {
        if (closed.get()) return
        fifo.trySend(task)
    }

    // --- FIFO Storage Effect Execution ---

    fun executeList(eff: DraftStorageListEffect): DraftStorageListedEvent {
        val builder = DraftStorageListedEvent.newBuilder().setOpId(eff.opId)
        if (rootDir.exists()) {
            // Drop legacy pre-3C-1 directories/files outside our known v2 layout without migrating.
            for (child in rootDir.listFiles().orEmpty()) {
                if (child.name !in KNOWN_ROOT_CHILDREN) {
                    runCatching { child.deleteRecursively() }
                }
            }
        }
        val key = keyProvider.loadExistingKey()
        try {
            scanBucket(DraftStorageBucket.DRAFT_STORAGE_BUCKET_DRAFT, key, builder)
            scanBucket(DraftStorageBucket.DRAFT_STORAGE_BUCKET_OUTBOX, key, builder)
            val blobDir = blobsDir()
            if (blobDir.isDirectory) {
                for (f in blobDir.listFiles().orEmpty()) {
                    if (!f.isFile) continue
                    if (f.name.endsWith(".tmp")) {
                        runCatching { f.delete() }
                        continue
                    }
                    builder.addLocalRefs(LOCAL_BLOB_REF_PREFIX + f.name)
                }
            }
            for (ext in platformIo.listExternalLocalRefs()) {
                if (ext.isNotEmpty()) builder.addLocalRefs(ext)
            }
            return builder.build()
        } finally {
            key?.fill(0)
        }
    }

    private fun scanBucket(
        bucket: DraftStorageBucket,
        key: ByteArray?,
        out: DraftStorageListedEvent.Builder,
    ) {
        val dir = bucketDir(bucket)
        if (!dir.isDirectory) return
        for (f in dir.listFiles().orEmpty()) {
            if (!f.isFile) continue
            if (f.name.endsWith(".tmp")) {
                runCatching { f.delete() }
                continue
            }
            if (!f.name.endsWith(RECORD_SUFFIX) || key == null) {
                out.addEntries(
                    DraftStorageEntry.newBuilder()
                        .setBucket(bucket)
                        .setKey(CORRUPT_KEY_PREFIX + f.name)
                        .setCorrupt(true),
                )
                continue
            }
            val parsed = runCatching { openRecord(f.readBytes(), key, bucket) }.getOrNull()
            if (parsed == null) {
                out.addEntries(
                    DraftStorageEntry.newBuilder()
                        .setBucket(bucket)
                        .setKey(CORRUPT_KEY_PREFIX + f.name)
                        .setCorrupt(true),
                )
            } else {
                out.addEntries(
                    DraftStorageEntry.newBuilder()
                        .setBucket(bucket)
                        .setKey(parsed.first)
                        .setData(ByteString.copyFrom(parsed.second))
                        .setCorrupt(false),
                )
            }
        }
    }

    internal fun executeRead(eff: DraftStorageReadEffect): DraftStorageReadResultEvent {
        val builder = DraftStorageReadResultEvent.newBuilder()
            .setOpId(eff.opId)
            .setBucket(eff.bucket)
            .setKey(eff.key)
        val f = recordFile(eff.bucket, eff.key)
        if (!f.isFile) {
            return builder.setFound(false).build()
        }
        val key = keyProvider.loadExistingKey()
            ?: return builder
                .setFound(false)
                .setErrorKind(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_CORRUPT_STORE)
                .setErrorDetail("missing_storage_key")
                .build()
        return try {
            val raw = f.readBytes()
            val parsed = openRecord(raw, key, eff.bucket)
            if (parsed.first != eff.key) {
                builder
                    .setFound(false)
                    .setErrorKind(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_CORRUPT_STORE)
                    .setErrorDetail("record_key_mismatch")
                    .build()
            } else {
                builder
                    .setFound(true)
                    .setData(ByteString.copyFrom(parsed.second))
                    .build()
            }
        } catch (e: GeneralSecurityException) {
            builder
                .setFound(false)
                .setErrorKind(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_CORRUPT_STORE)
                .setErrorDetail(e.message.orEmpty())
                .build()
        } catch (e: IllegalArgumentException) {
            builder
                .setFound(false)
                .setErrorKind(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_CORRUPT_STORE)
                .setErrorDetail(e.message.orEmpty())
                .build()
        } finally {
            key.fill(0)
        }
    }

    fun executeWrite(eff: DraftStorageWriteEffect): DraftStorageWriteResultEvent {
        val builder = DraftStorageWriteResultEvent.newBuilder()
            .setOpId(eff.opId)
            .setBucket(eff.bucket)
            .setKey(eff.key)
        val key = keyProvider.ensureKey()
        return try {
            val sealed = sealRecord(eff.key, eff.data.toByteArray(), key, eff.bucket)
            val target = recordFile(eff.bucket, eff.key)
            writeFileAtomicWithQuota(target, sealed)
            builder.setOk(true).build()
        } catch (t: Throwable) {
            builder
                .setOk(false)
                .setErrorKind(classifyIoError(t))
                .setErrorDetail(t.message.orEmpty())
                .build()
        } finally {
            key.fill(0)
        }
    }

    internal fun executeDelete(eff: DraftStorageDeleteEffect) {
        val f = recordFile(eff.bucket, eff.key)
        runCatching { f.delete() }
    }

    internal fun executeClearAll(@Suppress("UNUSED_PARAMETER") eff: DraftStorageClearAllEffect, wipeKey: Boolean = false) {
        clearEpoch.incrementAndGet()
        runCatching {
            if (rootDir.exists()) {
                for (child in rootDir.listFiles().orEmpty()) {
                    if (wipeKey || child.name != KEY_FILE_NAME) {
                        child.deleteRecursively()
                    }
                }
            }
        }
        if (wipeKey) {
            runCatching { keyProvider.clearKey() }
        }
        runCatching { platformIo.releaseAllExternalRefs() }
        runCatching { platformIo.onAllCleared() }
    }

    internal fun executeReleaseLocal(eff: AttachmentReleaseLocalEffect) {
        for (ref in eff.refsList) {
            if (ref.isEmpty()) continue
            if (ref.startsWith(LOCAL_BLOB_REF_PREFIX)) {
                val f = blobFile(ref)
                if (f != null) {
                    runCatching { f.delete() }
                }
                runCatching { platformIo.onLocalBlobDeleted(ref) }
            } else {
                runCatching { platformIo.releaseExternalRef(ref) }
            }
        }
    }

    // --- Media / Chunk Effect Execution (off FIFO queue) ---

    internal fun executePrepareImage(eff: AttachmentPrepareImageEffect): AttachmentPrepareImageResultEvent {
        val epoch = clearEpoch.get()
        val createdBlobs = ArrayList<String>(2)
        val outcome = try {
            platformIo.prepareImage(eff) { suffix, bytes ->
                val ref = writeEncryptedBlob(suffix, bytes)
                createdBlobs += ref
                ref
            }
        } catch (t: Throwable) {
            for (ref in createdBlobs) blobFile(ref)?.delete()
            return AttachmentPrepareImageResultEvent.newBuilder()
                .setOpId(eff.opId)
                .setScope(eff.scope)
                .setOutboxRequestId(eff.outboxRequestId)
                .setItemId(eff.itemId)
                .setOk(false)
                .setErrorKind(classifyIoError(t))
                .setErrorDetail(t.message.orEmpty())
                .build()
        }
        if (!outcome.ok || clearEpoch.get() != epoch) {
            for (ref in createdBlobs) blobFile(ref)?.delete()
            return AttachmentPrepareImageResultEvent.newBuilder()
                .setOpId(eff.opId)
                .setScope(eff.scope)
                .setOutboxRequestId(eff.outboxRequestId)
                .setItemId(eff.itemId)
                .setOk(false)
                .setErrorKind(
                    if (clearEpoch.get() != epoch) AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_OTHER
                    else outcome.errorKind.takeIf { it != AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_UNSPECIFIED }
                        ?: AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_OTHER,
                )
                .setErrorCode(outcome.errorCode)
                .setErrorDetail(outcome.errorDetail)
                .setIsReadingSource(outcome.isReadingSource)
                .build()
        }
        return AttachmentPrepareImageResultEvent.newBuilder()
            .setOpId(eff.opId)
            .setScope(eff.scope)
            .setOutboxRequestId(eff.outboxRequestId)
            .setItemId(eff.itemId)
            .setOk(true)
            .setPreparedRef(outcome.preparedRef)
            .setThumbRef(outcome.thumbRef)
            .setFormat(outcome.format)
            .setWidth(outcome.width)
            .setHeight(outcome.height)
            .setSizeBytes(outcome.sizeBytes)
            .setSha256(ByteString.copyFrom(outcome.sha256))
            .build()
    }

    internal fun executeHashFile(eff: AttachmentHashFileEffect): AttachmentHashFileResultEvent {
        val outcome = platformIo.hashFile(eff)
        return AttachmentHashFileResultEvent.newBuilder()
            .setOpId(eff.opId)
            .setScope(eff.scope)
            .setOutboxRequestId(eff.outboxRequestId)
            .setItemId(eff.itemId)
            .setOk(outcome.ok)
            .setSizeBytes(outcome.sizeBytes)
            .setSha256(ByteString.copyFrom(outcome.sha256))
            .setUpdatedSourceRef(outcome.updatedSourceRef)
            .setHasDuration(outcome.hasDuration)
            .setDurationMs(outcome.durationMs)
            .setErrorKind(outcome.errorKind)
            .setErrorDetail(outcome.errorDetail)
            .build()
    }

    internal fun executeReadChunk(eff: AttachmentReadChunkEffect): AttachmentReadChunkResultEvent {
        val base = AttachmentReadChunkResultEvent.newBuilder()
            .setOpId(eff.opId)
            .setScope(eff.scope)
            .setOutboxRequestId(eff.outboxRequestId)
            .setItemId(eff.itemId)
            .setOffset(eff.offset)
        val outcome = when (eff.kind) {
            AttachmentItemKind.ATTACHMENT_ITEM_KIND_IMAGE -> {
                readBlobRange(
                    blobRef = eff.preparedRef,
                    offset = eff.offset,
                    length = eff.length,
                    expectedSizeBytes = eff.expectedSizeBytes,
                )
            }
            AttachmentItemKind.ATTACHMENT_ITEM_KIND_FILE -> {
                if (eff.sourceRef.startsWith(LOCAL_BLOB_REF_PREFIX)) {
                    readBlobRange(
                        blobRef = eff.sourceRef,
                        offset = eff.offset,
                        length = eff.length,
                        expectedSizeBytes = eff.expectedSizeBytes,
                    )
                } else {
                    platformIo.readFileChunk(eff)
                }
            }
            else -> ReadChunkOutcome(
                ok = false,
                errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_OTHER,
                errorDetail = "unspecified_item_kind",
            )
        }
        return if (outcome.ok) {
            base.setOk(true).setData(ByteString.copyFrom(outcome.data)).build()
        } else {
            base.setOk(false)
                .setErrorKind(
                    outcome.errorKind.takeIf { it != AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_UNSPECIFIED }
                        ?: AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_OTHER,
                )
                .setErrorDetail(outcome.errorDetail)
                .build()
        }
    }

    // --- Chunked AES-256-GCM Blob Store (O(1) range reads, zero plaintext on disk) ---

    /** Encrypts [plain] in 256 KiB AES-GCM chunks, writes atomically under `blobs/`, and returns its opaque ref. */
    fun writeEncryptedBlob(suffix: String, plain: ByteArray): String {
        val safeSuffix = suffix.filter { it.isLetterOrDigit() || it == '.' || it == '-' || it == '_' }
        val fileName = UUID.randomUUID().toString().replace("-", "") + safeSuffix
        val target = File(blobsDir().apply { mkdirs() }, fileName)
        val key = keyProvider.ensureKey()
        try {
            val totalSize = plain.size.toLong()
            val chunkCount = if (plain.isEmpty()) 0 else (plain.size + BLOB_CHUNK_PLAIN_BYTES - 1) / BLOB_CHUNK_PLAIN_BYTES
            val estimatedSize = BLOB_HEADER_BYTES.toLong() + totalSize + chunkCount.toLong() * (GCM_IV_BYTES + GCM_TAG_BYTES)
            requireRoomForWrite(target, estimatedSize)
            val tmp = File(target.parentFile, "$fileName.${UUID.randomUUID()}.tmp")
            try {
                tmp.outputStream().buffered().use { out ->
                    val header = ByteBuffer.allocate(BLOB_HEADER_BYTES)
                        .put(BLOB_MAGIC)
                        .putLong(totalSize)
                        .putInt(BLOB_CHUNK_PLAIN_BYTES)
                        .array()
                    out.write(header)
                    val keySpec = SecretKeySpec(key, "AES")
                    var chunkIdx = 0
                    var pos = 0
                    while (pos < plain.size) {
                        val len = minOf(BLOB_CHUNK_PLAIN_BYTES, plain.size - pos)
                        val iv = randomBytes(GCM_IV_BYTES)
                        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
                        cipher.init(Cipher.ENCRYPT_MODE, keySpec, GCMParameterSpec(GCM_TAG_BITS, iv))
                        cipher.updateAAD(blobChunkAad(totalSize, chunkIdx))
                        val sealed = cipher.doFinal(plain, pos, len)
                        out.write(iv)
                        out.write(sealed)
                        pos += len
                        chunkIdx++
                    }
                    out.flush()
                }
                moveAtomic(tmp, target)
            } finally {
                tmp.delete()
            }
            return LOCAL_BLOB_REF_PREFIX + fileName
        } finally {
            key.fill(0)
        }
    }

    /** Reads the entire decrypted bytes of a local blob (for thumbnail or image viewer display). */
    fun readEncryptedBlobFull(blobRef: String): ByteArray? {
        val f = blobFile(blobRef) ?: return null
        if (!f.isFile) return null
        val key = runCatching { keyProvider.loadExistingKey() }.getOrNull() ?: return null
        return try {
            RandomAccessFile(f, "r").use { raf ->
                val (totalSize, chunkSize) = readBlobHeader(raf) ?: return null
                if (totalSize == 0L) return ByteArray(0)
                if (totalSize > Int.MAX_VALUE) return null
                readBlobRangeFromRaf(raf, key, totalSize, chunkSize, 0L, totalSize.toInt())
            }
        } catch (_: Throwable) {
            null
        } finally {
            key.fill(0)
        }
    }

    /** Streams only the encrypted chunks overlapping `[offset, offset + length)` without reading the whole blob. */
    internal fun readBlobRange(
        blobRef: String,
        offset: Long,
        length: Int,
        expectedSizeBytes: Long,
    ): ReadChunkOutcome {
        val f = blobFile(blobRef)
            ?: return ReadChunkOutcome(false, errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING, errorDetail = "invalid_blob_ref")
        if (!f.isFile) {
            return ReadChunkOutcome(false, errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING, errorDetail = "blob_missing")
        }
        val key = try {
            keyProvider.loadExistingKey()
                ?: return ReadChunkOutcome(false, errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_CORRUPT_STORE, errorDetail = "missing_storage_key")
        } catch (t: Throwable) {
            return ReadChunkOutcome(false, errorKind = classifyIoError(t), errorDetail = t.message.orEmpty())
        }
        return try {
            RandomAccessFile(f, "r").use { raf ->
                val (totalSize, chunkSize) = readBlobHeader(raf)
                    ?: return ReadChunkOutcome(false, errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_CORRUPT_STORE, errorDetail = "corrupt_blob_header")
                if (expectedSizeBytes >= 0L && totalSize != expectedSizeBytes) {
                    return ReadChunkOutcome(false, errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_CHANGED, errorDetail = "blob_size_mismatch")
                }
                if (offset < 0L || length < 0 || offset + length > totalSize) {
                    return ReadChunkOutcome(false, errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_CHANGED, errorDetail = "range_out_of_bounds")
                }
                if (length == 0) {
                    return ReadChunkOutcome(true, ByteArray(0))
                }
                val slice = readBlobRangeFromRaf(raf, key, totalSize, chunkSize, offset, length)
                ReadChunkOutcome(true, slice)
            }
        } catch (e: FileNotFoundException) {
            ReadChunkOutcome(false, errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING, errorDetail = e.message.orEmpty())
        } catch (e: GeneralSecurityException) {
            ReadChunkOutcome(false, errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_CORRUPT_STORE, errorDetail = e.message.orEmpty())
        } catch (e: EOFException) {
            ReadChunkOutcome(false, errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_CORRUPT_STORE, errorDetail = e.message.orEmpty())
        } catch (t: Throwable) {
            ReadChunkOutcome(false, errorKind = classifyIoError(t), errorDetail = t.message.orEmpty())
        } finally {
            key.fill(0)
        }
    }

    private fun readBlobHeader(raf: RandomAccessFile): Pair<Long, Int>? {
        if (raf.length() < BLOB_HEADER_BYTES) return null
        val hdr = ByteArray(BLOB_HEADER_BYTES)
        raf.seek(0L)
        raf.readFully(hdr)
        if (!hdr.copyOfRange(0, 4).contentEquals(BLOB_MAGIC)) return null
        val buf = ByteBuffer.wrap(hdr, 4, 12)
        val totalSize = buf.long
        val chunkSize = buf.int
        if (totalSize < 0L || chunkSize <= 0) return null
        return totalSize to chunkSize
    }

    private fun readBlobRangeFromRaf(
        raf: RandomAccessFile,
        key: ByteArray,
        totalSize: Long,
        chunkSize: Int,
        offset: Long,
        length: Int,
    ): ByteArray {
        val out = ByteArray(length)
        val keySpec = SecretKeySpec(key, "AES")
        val fullFrameBytes = (GCM_IV_BYTES + chunkSize + GCM_TAG_BYTES).toLong()
        val firstChunk = (offset / chunkSize).toInt()
        val lastChunk = ((offset + length - 1L) / chunkSize).toInt()
        val totalChunks = ((totalSize + chunkSize - 1L) / chunkSize).toInt()
        var outPos = 0
        for (chunkIdx in firstChunk..lastChunk) {
            val chunkPlainStart = chunkIdx.toLong() * chunkSize
            val chunkPlainLen = if (chunkIdx == totalChunks - 1) {
                (totalSize - chunkPlainStart).toInt()
            } else {
                chunkSize
            }
            val frameOffset = BLOB_HEADER_BYTES.toLong() + chunkIdx.toLong() * fullFrameBytes
            val iv = ByteArray(GCM_IV_BYTES)
            val cipherBuf = ByteArray(chunkPlainLen + GCM_TAG_BYTES)
            raf.seek(frameOffset)
            raf.readFully(iv)
            raf.readFully(cipherBuf)
            val cipher = Cipher.getInstance("AES/GCM/NoPadding")
            cipher.init(Cipher.DECRYPT_MODE, keySpec, GCMParameterSpec(GCM_TAG_BITS, iv))
            cipher.updateAAD(blobChunkAad(totalSize, chunkIdx))
            val plain = cipher.doFinal(cipherBuf)
            val copyStart = (maxOf(offset, chunkPlainStart) - chunkPlainStart).toInt()
            val copyEnd = (minOf(offset + length, chunkPlainStart + plain.size) - chunkPlainStart).toInt()
            val copyLen = copyEnd - copyStart
            System.arraycopy(plain, copyStart, out, outPos, copyLen)
            outPos += copyLen
        }
        return out
    }

    private fun blobChunkAad(totalSize: Long, chunkIndex: Int): ByteArray =
        ByteBuffer.allocate(16)
            .put(BLOB_MAGIC)
            .putLong(totalSize)
            .putInt(chunkIndex)
            .array()

    // --- Encrypted Record Framing ---

    private fun sealRecord(recordKey: String, data: ByteArray, key: ByteArray, bucket: DraftStorageBucket): ByteArray {
        val keyBytes = recordKey.toByteArray(Charsets.UTF_8)
        val plain = ByteBuffer.allocate(4 + keyBytes.size + data.size)
            .putInt(keyBytes.size)
            .put(keyBytes)
            .put(data)
            .array()
        val iv = randomBytes(GCM_IV_BYTES)
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, SecretKeySpec(key, "AES"), GCMParameterSpec(GCM_TAG_BITS, iv))
        cipher.updateAAD(recordAad(bucket))
        val sealed = cipher.doFinal(plain)
        return ByteArray(GCM_IV_BYTES + sealed.size).also {
            System.arraycopy(iv, 0, it, 0, GCM_IV_BYTES)
            System.arraycopy(sealed, 0, it, GCM_IV_BYTES, sealed.size)
        }
    }

    private fun openRecord(raw: ByteArray, key: ByteArray, bucket: DraftStorageBucket): Pair<String, ByteArray> {
        require(raw.size >= GCM_IV_BYTES + GCM_TAG_BYTES + 4) { "record_too_short" }
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.DECRYPT_MODE, SecretKeySpec(key, "AES"), GCMParameterSpec(GCM_TAG_BITS, raw, 0, GCM_IV_BYTES))
        cipher.updateAAD(recordAad(bucket))
        val plain = cipher.doFinal(raw, GCM_IV_BYTES, raw.size - GCM_IV_BYTES)
        require(plain.size >= 4) { "record_missing_key_len" }
        val keyLen = ByteBuffer.wrap(plain, 0, 4).int
        require(keyLen in 1..(plain.size - 4)) { "record_invalid_key_len" }
        val recordKey = String(plain, 4, keyLen, Charsets.UTF_8)
        val payload = plain.copyOfRange(4 + keyLen, plain.size)
        return recordKey to payload
    }

    private fun recordAad(bucket: DraftStorageBucket): ByteArray =
        "RAR1:${bucket.number}".toByteArray(Charsets.UTF_8)

    // --- Path, Atomic Write & Error Classification ---

    private fun bucketDir(bucket: DraftStorageBucket): File = when (bucket) {
        DraftStorageBucket.DRAFT_STORAGE_BUCKET_DRAFT -> File(rootDir, DRAFTS_DIR_NAME)
        DraftStorageBucket.DRAFT_STORAGE_BUCKET_OUTBOX -> File(rootDir, OUTBOX_DIR_NAME)
        else -> File(rootDir, "unknown")
    }

    private fun blobsDir(): File = File(rootDir, BLOBS_DIR_NAME)

    private fun recordFile(bucket: DraftStorageBucket, key: String): File {
        val dir = bucketDir(bucket)
        if (key.startsWith(CORRUPT_KEY_PREFIX)) {
            val rawName = key.removePrefix(CORRUPT_KEY_PREFIX)
            // Guard against path traversal in corrupt filename tokens.
            val safe = File(rawName).name
            return File(dir, safe)
        }
        val hash = sha256(key.toByteArray(Charsets.UTF_8)).hex().take(32)
        return File(dir, hash + RECORD_SUFFIX)
    }

    internal fun blobFile(blobRef: String): File? {
        if (!blobRef.startsWith(LOCAL_BLOB_REF_PREFIX)) return null
        val name = blobRef.removePrefix(LOCAL_BLOB_REF_PREFIX)
        if (name.isEmpty() || name.contains('/') || name.contains('\\') || name == "." || name == "..") return null
        return File(blobsDir(), name)
    }

    private fun requireRoomForWrite(replacing: File, incomingBytes: Long) {
        if (draftLimitBytes <= 0L) return
        val existingTotal = if (rootDir.exists()) ImageDiskPolicy.bytes(rootDir) else 0L
        val replacingLen = if (replacing.isFile) replacing.length() else 0L
        if (existingTotal - replacingLen + incomingBytes > draftLimitBytes) {
            throw AttachmentStorageFullException()
        }
    }

    private fun writeFileAtomicWithQuota(target: File, bytes: ByteArray) {
        target.parentFile?.mkdirs()
        requireRoomForWrite(target, bytes.size.toLong())
        val tmp = File(target.parentFile, "${target.name}.${UUID.randomUUID()}.tmp")
        try {
            tmp.writeBytes(bytes)
            moveAtomic(tmp, target)
        } finally {
            tmp.delete()
        }
    }

    private fun moveAtomic(src: File, dst: File) {
        try {
            Files.move(src.toPath(), dst.toPath(), StandardCopyOption.REPLACE_EXISTING, StandardCopyOption.ATOMIC_MOVE)
        } catch (_: Throwable) {
            try {
                Files.move(src.toPath(), dst.toPath(), StandardCopyOption.REPLACE_EXISTING)
            } catch (_: Throwable) {
                if (!src.renameTo(dst)) throw IOException("atomic_move_failed")
            }
        }
    }

    fun classifyIoError(t: Throwable): AttachmentIOErrorKind {
        var cur: Throwable? = t
        while (cur != null) {
            if (cur is AttachmentStorageFullException || platformIo.isStorageFullError(cur)) {
                return AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_STORAGE_FULL
            }
            if (cur is FileNotFoundException || cur is SecurityException) {
                return AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING
            }
            val msg = cur.message.orEmpty()
            if (msg.contains("ENOSPC", ignoreCase = true) ||
                msg.contains("No space left", ignoreCase = true) ||
                msg.contains("EDQUOT", ignoreCase = true) ||
                msg.contains("disk quota", ignoreCase = true)
            ) {
                return AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_STORAGE_FULL
            }
            cur = cur.cause
        }
        return AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_OTHER
    }

    companion object {
        private val KNOWN_ROOT_CHILDREN = setOf(DRAFTS_DIR_NAME, OUTBOX_DIR_NAME, BLOBS_DIR_NAME, KEY_FILE_NAME)
    }
}
