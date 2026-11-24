package io.github.lewin671.remoteagent.data

import android.content.ContentResolver
import android.content.Context
import android.content.Intent
import android.graphics.BitmapFactory
import android.media.MediaMetadataRetriever
import android.net.Uri
import android.os.ParcelFileDescriptor
import android.provider.DocumentsContract
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.system.ErrnoException
import android.system.Os
import android.system.OsConstants
import android.util.LruCache
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import io.github.lewin671.remoteagent.core.AttachmentIoExecutor
import io.github.lewin671.remoteagent.core.AttachmentKeyUnavailableException
import io.github.lewin671.remoteagent.core.AttachmentPlatformIo
import io.github.lewin671.remoteagent.core.AttachmentStorageFullException
import io.github.lewin671.remoteagent.core.AttachmentStorageKeyProvider
import io.github.lewin671.remoteagent.core.HashedFileOutcome
import io.github.lewin671.remoteagent.core.LOCAL_BLOB_REF_PREFIX
import io.github.lewin671.remoteagent.core.PreparedImageOutcome
import io.github.lewin671.remoteagent.core.ReadChunkOutcome
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.proto.AttachmentHashFileEffect
import io.github.lewin671.remoteagent.core.proto.AttachmentIOErrorKind
import io.github.lewin671.remoteagent.core.proto.AttachmentPrepareImageEffect
import io.github.lewin671.remoteagent.core.proto.AttachmentReadChunkEffect
import io.github.lewin671.remoteagent.core.randomBytes
import io.github.lewin671.remoteagent.core.sha256
import io.github.lewin671.remoteagent.ui.PrepareImageException
import io.github.lewin671.remoteagent.ui.VideoFrames
import io.github.lewin671.remoteagent.ui.encodeThumbnailBytes
import io.github.lewin671.remoteagent.ui.prepareImageFromSource
import java.io.Closeable
import java.io.File
import java.io.FileDescriptor
import java.io.FileInputStream
import java.io.FileNotFoundException
import java.io.FilterInputStream
import java.io.IOException
import java.io.InputStream
import java.nio.ByteBuffer
import java.security.KeyStore
import java.security.MessageDigest
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

/** Injectable Keystore abstraction so Keystore failures can be tested deterministically. */
internal interface AndroidKeyStoreBackend {
    /** Returns the existing wrapping key, `null` ONLY when the alias truly does not exist, or throws on Keystore error. */
    fun getKey(alias: String): SecretKey?
    fun generateKey(alias: String): SecretKey
    fun deleteKey(alias: String)
}

internal object RealAndroidKeyStoreBackend : AndroidKeyStoreBackend {
    override fun getKey(alias: String): SecretKey? {
        val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        if (!ks.containsAlias(alias)) return null
        return ks.getKey(alias, null) as? SecretKey
            ?: throw AttachmentKeyUnavailableException("keystore_key_null")
    }

    override fun generateKey(alias: String): SecretKey =
        KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore").run {
            init(
                KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                    .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                    .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                    .setKeySize(256)
                    .build(),
            )
            generateKey()
        }

    override fun deleteKey(alias: String) {
        val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        if (ks.containsAlias(alias)) ks.deleteEntry(alias)
    }
}

/**
 * Android KeyStore-backed key provider for sealing persisted drafts, outbox records, and prepared
 * image/thumbnail blobs under `noBackupFilesDir/pending-images/`.
 *
 * Strictly distinguishes "no key exists yet" (`!keyFile.exists()`) from transient Keystore or
 * decryption failures: the latter throw [AttachmentKeyUnavailableException] so existing drafts,
 * outbox records, blobs, and `key.bin` are preserved untouched for retry on next launch.
 */
internal class AndroidAttachmentKeyProvider(
    private val dir: File,
    private val keystore: AndroidKeyStoreBackend = RealAndroidKeyStoreBackend,
    private val alias: String = ALIAS,
) : AttachmentStorageKeyProvider {
    private val keyFile = File(dir, "key.bin")
    @Volatile private var cachedKey: ByteArray? = null

    @Synchronized fun setKeyForTest(key: ByteArray) {
        cachedKey = key.copyOf()
    }

    @Synchronized override fun loadExistingKey(): ByteArray? {
        cachedKey?.let { return it.copyOf() }
        val stored = readStoredKey() ?: return null
        cachedKey = stored
        return stored.copyOf()
    }

    @Synchronized override fun ensureKey(): ByteArray {
        cachedKey?.let { return it.copyOf() }
        // If key.bin already exists, readStoredKey() either returns it or throws AttachmentKeyUnavailableException;
        // never silently overwrite an existing key.bin!
        readStoredKey()?.let {
            cachedKey = it
            return it.copyOf()
        }
        dir.mkdirs()
        val wrap = try {
            keystore.getKey(alias) ?: keystore.generateKey(alias)
        } catch (e: AttachmentKeyUnavailableException) {
            throw e
        } catch (t: Throwable) {
            throw AttachmentKeyUnavailableException("keystore_ensure_failed", t)
        }
        val fresh = randomBytes(32)
        val sealed = try {
            val c = Cipher.getInstance("AES/GCM/NoPadding")
            c.init(Cipher.ENCRYPT_MODE, wrap)
            c.iv + c.doFinal(fresh)
        } catch (t: Throwable) {
            throw AttachmentKeyUnavailableException("keystore_seal_failed", t)
        }
        val tmp = File(dir, "key.${java.util.UUID.randomUUID()}.tmp")
        try {
            tmp.writeBytes(sealed)
            if (!tmp.renameTo(keyFile)) keyFile.writeBytes(sealed)
        } finally {
            tmp.delete()
        }
        cachedKey = fresh
        return fresh.copyOf()
    }

    @Synchronized override fun clearKey() {
        cachedKey?.fill(0)
        cachedKey = null
        runCatching { keyFile.delete() }
        runCatching { keystore.deleteKey(alias) }
    }

    private fun readStoredKey(): ByteArray? {
        if (!keyFile.exists()) return null
        if (!keyFile.isFile) throw AttachmentKeyUnavailableException("key_file_not_regular")
        val wrap = try {
            keystore.getKey(alias)
        } catch (e: AttachmentKeyUnavailableException) {
            throw e
        } catch (t: Throwable) {
            throw AttachmentKeyUnavailableException("keystore_read_failed", t)
        } ?: throw AttachmentKeyUnavailableException("wrapping_key_missing")
        return try {
            val d = keyFile.readBytes()
            if (d.size <= 12) throw AttachmentKeyUnavailableException("key_file_truncated")
            val c = Cipher.getInstance("AES/GCM/NoPadding")
            c.init(Cipher.DECRYPT_MODE, wrap, GCMParameterSpec(128, d, 0, 12))
            c.doFinal(d, 12, d.size - 12)
        } catch (e: AttachmentKeyUnavailableException) {
            throw e
        } catch (t: Throwable) {
            throw AttachmentKeyUnavailableException("key_unwrap_failed", t)
        }
    }

    companion object {
        private const val ALIAS = "ra-pending"
    }
}

/**
 * App-wide Android platform coordinator for Core-driven attachment uploads, draft trays,
 * composer drafts, and outbox persistence (`noBackupFilesDir/pending-images/`).
 *
 * Upload state machines, retry/reconnect/poll timers, slot reservations, outbox write barriers,
 * 7-day draft expiry, and orphan local-handle GC all live in Core.
 * This class owns only platform I/O: the single global FIFO storage executor, image preparation,
 * streaming file hashing/chunk reading, persisted SAF URI grants, and thumbnail decoding.
 */
class AttachmentManager(
    private val context: Context,
    private val remoteProvider: () -> Remote?,
) {
    val rootDir = File(context.noBackupFilesDir, "pending-images").apply { mkdirs() }
    private val keyProvider = AndroidAttachmentKeyProvider(rootDir)
    private val thumbCache = LruCache<String, ImageBitmap>(64)
    private val chunkReader = SourceChunkReader(
        access = object : AttachmentSourceAccess {
            override fun isDirectory(ref: String): Boolean = isDirectoryUri(Uri.parse(ref))

            override fun open(ref: String): OpenedAttachmentSource? {
                val uri = Uri.parse(ref)
                val pfd = if (uri.scheme == ContentResolver.SCHEME_FILE && uri.path != null) {
                    val f = File(uri.path!!)
                    if (f.isDirectory) throw IOException("EISDIR")
                    ParcelFileDescriptor.open(f, ParcelFileDescriptor.MODE_READ_ONLY)
                } else {
                    context.contentResolver.openFileDescriptor(uri, "r")
                }
                if (pfd != null) return openParcelFileDescriptorAsSource(pfd)
                val stream = openSourceStream(uri) ?: return null
                return OpenedAttachmentSource.Stream(stream)
            }
        },
        isDirectoryError = { isDirectoryError(it) },
    )

    val executor: AttachmentIoExecutor = AttachmentIoExecutor(
        rootDir = rootDir,
        keyProvider = keyProvider,
        platformIo = object : AttachmentPlatformIo {
            override fun prepareImage(
                eff: AttachmentPrepareImageEffect,
                storeBlob: (suffix: String, bytes: ByteArray) -> String,
            ): PreparedImageOutcome = executePrepareImage(eff, storeBlob)

            override fun hashFile(eff: AttachmentHashFileEffect): HashedFileOutcome =
                executeHashFile(eff)

            override fun readFileChunk(eff: AttachmentReadChunkEffect): ReadChunkOutcome =
                executeReadFileChunk(eff)

            override fun listExternalLocalRefs(): List<String> =
                runCatching {
                    context.contentResolver.persistedUriPermissions.map { it.uri.toString() }
                }.getOrDefault(emptyList())

            override fun releaseExternalRef(ref: String) {
                chunkReader.closeRef(ref)
                VideoFrames.remove(ref)
                if (ref.startsWith("content://")) {
                    runCatching {
                        context.contentResolver.releasePersistableUriPermission(
                            Uri.parse(ref),
                            Intent.FLAG_GRANT_READ_URI_PERMISSION,
                        )
                    }
                }
            }

            override fun releaseAllExternalRefs() {
                chunkReader.closeAll()
                VideoFrames.clear()
                runCatching {
                    context.contentResolver.persistedUriPermissions.forEach { perm ->
                        runCatching {
                            context.contentResolver.releasePersistableUriPermission(
                                perm.uri,
                                Intent.FLAG_GRANT_READ_URI_PERMISSION,
                            )
                        }
                    }
                }
            }

            override fun isStorageFullError(t: Throwable): Boolean =
                t is ErrnoException && (t.errno == OsConstants.ENOSPC || t.errno == OsConstants.EDQUOT)

            override fun onLocalBlobDeleted(ref: String) {
                chunkReader.closeRef(ref)
                VideoFrames.remove(ref)
                thumbCache.remove(ref)
            }

            override fun onAllCleared() {
                chunkReader.closeAll()
                VideoFrames.clear()
                thumbCache.evictAll()
            }
        },
    )

    /**
     * Attaches all 9 attachment/storage Effect handlers to [remote]'s CoreHost and immediately
     * dispatches `AttachmentConfigEvent` before any Draft* action can fire.
     */
    fun attachRemote(remote: Remote) {
        executor.attach(remote.coreHost)
        remote.coreHost.sendAttachmentConfig(
            persistDrafts = true,
            persistOutbox = true,
            scanStorage = true,
            maxChunkBytes = 0,
            hasPersistedPreparedImages = true,
        )
    }

    fun forgetSession(sessionId: String) {
        remoteProvider()?.forgetDraftSession(sessionId)
    }

    fun clearAllOnDiskAsync() {
        executor.enqueueClearAll()
        thumbCache.evictAll()
        VideoFrames.clear()
    }

    suspend fun clearAllOnDisk() {
        executor.clearAllAndWait()
        thumbCache.evictAll()
        VideoFrames.clear()
    }

    fun usedBytes(): Long = executor.usedBytes()

    internal fun setKeyForTest(key: ByteArray) {
        keyProvider.setKeyForTest(key)
    }

    fun close() {
        chunkReader.close()
        executor.close()
        VideoFrames.clear()
    }

    /** Decodes and caches a small thumbnail for a draft or outbox image item. */
    suspend fun loadThumbnail(thumbRef: String, preparedRef: String = ""): ImageBitmap? {
        val key = thumbRef.ifEmpty { preparedRef }
        if (key.isEmpty()) return null
        thumbCache.get(key)?.let { return it }
        val decoded = withContext(Dispatchers.IO) {
            val bytes = when {
                thumbRef.startsWith(LOCAL_BLOB_REF_PREFIX) -> executor.readEncryptedBlobFull(thumbRef)
                preparedRef.startsWith(LOCAL_BLOB_REF_PREFIX) -> executor.readEncryptedBlobFull(preparedRef)
                else -> null
            } ?: return@withContext null
            decodeDownsampled(bytes, maxLongSide = 256)
        } ?: return null
        thumbCache.put(key, decoded)
        return decoded
    }

    /** Decodes a full-resolution (<= 2048px) preview bitmap for `DraftImageViewer`. */
    suspend fun loadViewerImage(preparedRef: String, sourceRef: String): ImageBitmap? =
        withContext(Dispatchers.IO) {
            when {
                preparedRef.startsWith(LOCAL_BLOB_REF_PREFIX) -> {
                    val bytes = executor.readEncryptedBlobFull(preparedRef) ?: return@withContext null
                    decodeDownsampled(bytes, maxLongSide = 2048)
                }
                sourceRef.startsWith(LOCAL_BLOB_REF_PREFIX) -> {
                    val bytes = executor.readEncryptedBlobFull(sourceRef) ?: return@withContext null
                    decodeDownsampled(bytes, maxLongSide = 2048)
                }
                sourceRef.isNotEmpty() -> {
                    val uri = Uri.parse(sourceRef)
                    val knownSize = probeSourceSizeBytes(uri)
                    decodeDownsampledStream(
                        openStream = { runCatching { openSourceStream(uri) }.getOrNull() },
                        maxLongSide = 2048,
                        maxSourceBytes = MAX_VIEWER_SOURCE_BYTES,
                        knownSizeBytes = knownSize,
                    )
                }
                else -> null
            }
        }

    private fun decodeDownsampled(bytes: ByteArray, maxLongSide: Int): ImageBitmap? = runCatching {
        val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
        BitmapFactory.decodeByteArray(bytes, 0, bytes.size, bounds)
        if (bounds.outWidth <= 0 || bounds.outHeight <= 0) return null
        var sample = 1
        while (maxOf(bounds.outWidth, bounds.outHeight) / (sample * 2) >= maxLongSide) {
            sample *= 2
        }
        BitmapFactory.decodeByteArray(
            bytes,
            0,
            bytes.size,
            BitmapFactory.Options().apply { inSampleSize = sample },
        )?.asImageBitmap()
    }.getOrNull()

    // --- Platform Effect Implementations ---

    private fun executePrepareImage(
        eff: AttachmentPrepareImageEffect,
        storeBlob: (suffix: String, bytes: ByteArray) -> String,
    ): PreparedImageOutcome {
        return try {
            val prepared = prepareImageFromSource(
                context = context,
                sourceRef = eff.sourceRef,
                original = eff.original,
                maxOriginalBytes = eff.maxOriginalBytes,
                maxDimensionPx = eff.maxDimensionPx,
                maxCompressedBytes = eff.maxCompressedBytes,
                readLocalBlob = { ref -> executor.readEncryptedBlobFull(ref) },
            )
            val hash = sha256(prepared.bytes)
            val preparedRef = storeBlob(".prep", prepared.bytes)
            val thumbBytes = encodeThumbnailBytes(prepared.bytes)
            val thumbRef = if (thumbBytes != null && thumbBytes.isNotEmpty()) {
                storeBlob(".thumb", thumbBytes)
            } else {
                ""
            }
            PreparedImageOutcome(
                ok = true,
                preparedRef = preparedRef,
                thumbRef = thumbRef,
                format = prepared.format,
                width = prepared.width,
                height = prepared.height,
                sizeBytes = prepared.bytes.size.toLong(),
                sha256 = hash,
            )
        } catch (e: PrepareImageException) {
            PreparedImageOutcome(
                ok = false,
                errorKind = e.kind,
                errorCode = e.errorCode,
                errorDetail = e.detail,
                isReadingSource = e.isReadingSource,
            )
        } catch (e: AttachmentStorageFullException) {
            PreparedImageOutcome(
                ok = false,
                errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_STORAGE_FULL,
                errorDetail = e.message.orEmpty(),
            )
        } catch (e: OutOfMemoryError) {
            PreparedImageOutcome(
                ok = false,
                errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_DIMENSIONS,
                errorDetail = "out_of_memory",
            )
        } catch (e: Throwable) {
            PreparedImageOutcome(
                ok = false,
                errorKind = executor.classifyIoError(e),
                errorDetail = e.message.orEmpty(),
            )
        }
    }

    private fun executeHashFile(eff: AttachmentHashFileEffect): HashedFileOutcome {
        val ref = eff.sourceRef
        if (ref.isEmpty()) {
            return HashedFileOutcome(
                ok = false,
                errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING,
                errorDetail = "empty_source_ref",
            )
        }
        val uri = Uri.parse(ref)
        if (isDirectoryUri(uri)) {
            return HashedFileOutcome(
                ok = false,
                errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_DIRECTORY,
                errorDetail = "directory",
            )
        }
        return try {
            val digest = MessageDigest.getInstance("SHA-256")
            var actual = 0L
            val maxBytes = eff.maxBytes
            val stream = openSourceStream(uri)
                ?: return HashedFileOutcome(
                    ok = false,
                    errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING,
                    errorDetail = "cannot_open_stream",
                )
            stream.use { input ->
                val buffer = ByteArray(64 * 1024)
                while (true) {
                    val n = input.read(buffer)
                    if (n < 0) break
                    actual += n
                    if (maxBytes > 0L && actual > maxBytes) {
                        return HashedFileOutcome(
                            ok = false,
                            errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_FILE_TOO_LARGE,
                            errorDetail = "exceeds_max_bytes",
                        )
                    }
                    digest.update(buffer, 0, n)
                }
            }
            val durationMs = if (eff.isVideo) probeVideoDurationMs(uri) else null
            HashedFileOutcome(
                ok = true,
                sizeBytes = actual,
                sha256 = digest.digest(),
                hasDuration = durationMs != null && durationMs > 0L,
                durationMs = durationMs ?: 0L,
            )
        } catch (e: FileNotFoundException) {
            HashedFileOutcome(
                ok = false,
                errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING,
                errorDetail = e.message.orEmpty(),
            )
        } catch (e: SecurityException) {
            HashedFileOutcome(
                ok = false,
                errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING,
                errorDetail = e.message.orEmpty(),
            )
        } catch (e: Throwable) {
            if (isDirectoryError(e)) {
                HashedFileOutcome(
                    ok = false,
                    errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_DIRECTORY,
                    errorDetail = e.message.orEmpty(),
                )
            } else {
                HashedFileOutcome(
                    ok = false,
                    errorKind = executor.classifyIoError(e),
                    errorDetail = e.message.orEmpty(),
                )
            }
        }
    }

    private fun executeReadFileChunk(eff: AttachmentReadChunkEffect): ReadChunkOutcome =
        chunkReader.readChunk(
            ref = eff.sourceRef,
            offset = eff.offset,
            length = eff.length,
            expectedSize = eff.expectedSizeBytes,
            classifyError = { executor.classifyIoError(it) },
        )

    private fun openSourceStream(uri: Uri) =
        if (uri.scheme == ContentResolver.SCHEME_FILE && uri.path != null) {
            val f = File(uri.path!!)
            if (f.isDirectory) throw IOException("EISDIR")
            FileInputStream(f)
        } else {
            context.contentResolver.openInputStream(uri)
        }

    private fun probeSourceSizeBytes(uri: Uri): Long =
        if (uri.scheme == ContentResolver.SCHEME_FILE && uri.path != null) {
            val f = File(uri.path!!)
            if (f.isFile) f.length() else -1L
        } else {
            runCatching {
                context.contentResolver.openAssetFileDescriptor(uri, "r")?.use { it.length }
            }.getOrNull() ?: -1L
        }

    private fun isDirectoryUri(uri: Uri): Boolean {
        if (uri.scheme == ContentResolver.SCHEME_FILE && uri.path != null) {
            return File(uri.path!!).isDirectory
        }
        val mime = runCatching { context.contentResolver.getType(uri) }.getOrNull()
        return mime == DocumentsContract.Document.MIME_TYPE_DIR
    }

    private fun isDirectoryError(t: Throwable): Boolean {
        var cur: Throwable? = t
        while (cur != null) {
            if (cur is ErrnoException && cur.errno == OsConstants.EISDIR) return true
            if (cur.message?.contains("EISDIR", ignoreCase = true) == true) return true
            cur = cur.cause
        }
        return false
    }

    private fun probeVideoDurationMs(uri: Uri): Long? {
        val mmr = MediaMetadataRetriever()
        return try {
            if (uri.scheme == ContentResolver.SCHEME_FILE && uri.path != null) {
                mmr.setDataSource(uri.path)
            } else {
                mmr.setDataSource(context, uri)
            }
            mmr.extractMetadata(MediaMetadataRetriever.METADATA_KEY_DURATION)?.toLongOrNull()?.takeIf { it > 0L }
        } catch (_: Throwable) {
            null
        } finally {
            runCatching { mmr.release() }
        }
    }
}

internal sealed class OpenedAttachmentSource : Closeable {
    class Regular(
        private val closeable: Closeable,
        private val sizeProvider: () -> Long,
        private val reader: (ByteBuffer, Long) -> Int,
    ) : OpenedAttachmentSource() {
        fun size(): Long = sizeProvider()
        fun readAt(dst: ByteBuffer, position: Long): Int = reader(dst, position)
        override fun close() = closeable.close()
    }

    class Stream(val input: InputStream) : OpenedAttachmentSource() {
        override fun close() = input.close()
    }
}

internal interface AttachmentSourceAccess {
    fun isDirectory(ref: String): Boolean
    fun open(ref: String): OpenedAttachmentSource?
}

internal fun isRegularFileDescriptor(fd: FileDescriptor): Boolean = try {
    OsConstants.S_ISREG(Os.fstat(fd).st_mode)
} catch (_: Throwable) {
    false
}

internal fun openParcelFileDescriptorAsSource(pfd: ParcelFileDescriptor): OpenedAttachmentSource {
    if (isRegularFileDescriptor(pfd.fileDescriptor)) {
        val fis = FileInputStream(pfd.fileDescriptor)
        val ch = fis.channel
        val size = runCatching { ch.size() }.getOrNull()
        if (size != null && size >= 0L) {
            return OpenedAttachmentSource.Regular(
                closeable = Closeable {
                    runCatching { fis.close() }
                    runCatching { pfd.close() }
                },
                sizeProvider = { size },
                reader = { dst, pos -> ch.read(dst, pos) },
            )
        }
        return OpenedAttachmentSource.Stream(
            object : FilterInputStream(fis) {
                override fun close() {
                    try {
                        super.close()
                    } finally {
                        runCatching { pfd.close() }
                    }
                }
            },
        )
    }
    return OpenedAttachmentSource.Stream(ParcelFileDescriptor.AutoCloseInputStream(pfd))
}

/**
 * Reads chunks from regular files in O(1) via positional `FileChannel.read` and from non-regular
 * descriptors (such as SAF pipes/virtual streams where `fstat` is not `S_ISREG`) via a bounded
 * sequential stream cursor cache so consecutive chunk reads run in O(n) total time.
 */
internal class SourceChunkReader(
    private val access: AttachmentSourceAccess,
    private val isDirectoryError: (Throwable) -> Boolean = { false },
    private val maxOpenCursors: Int = 4,
) : Closeable {
    private class StreamCursor(
        val ref: String,
        val input: InputStream,
        var pos: Long = 0L,
    ) : Closeable {
        override fun close() {
            runCatching { input.close() }
        }
    }

    private val lock = Any()
    private val cursors = LinkedHashMap<String, StreamCursor>(maxOpenCursors, 0.75f, true)
    @Volatile private var closed = false

    fun readChunk(
        ref: String,
        offset: Long,
        length: Int,
        expectedSize: Long,
        classifyError: (Throwable) -> AttachmentIOErrorKind = { AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_OTHER },
    ): ReadChunkOutcome {
        if (ref.isEmpty()) {
            return ReadChunkOutcome(
                ok = false,
                errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING,
                errorDetail = "empty_source_ref",
            )
        }
        if (offset < 0L || length < 0) {
            return ReadChunkOutcome(
                ok = false,
                errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_OTHER,
                errorDetail = "invalid_range",
            )
        }
        if (expectedSize >= 0L && offset + length > expectedSize) {
            return ReadChunkOutcome(
                ok = false,
                errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_CHANGED,
                errorDetail = "range_past_eof",
            )
        }
        if (access.isDirectory(ref)) {
            return ReadChunkOutcome(
                ok = false,
                errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_DIRECTORY,
                errorDetail = "directory",
            )
        }
        var activeCursor: StreamCursor? = null
        return try {
            val cached = synchronized(lock) { cursors.remove(ref) }
            if (cached != null) {
                if (cached.pos <= offset) {
                    activeCursor = cached
                } else {
                    cached.close()
                }
            }
            if (activeCursor == null) {
                val opened = access.open(ref)
                    ?: return ReadChunkOutcome(
                        ok = false,
                        errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING,
                        errorDetail = "cannot_open_stream",
                    )
                when (opened) {
                    is OpenedAttachmentSource.Regular -> {
                        return opened.use { reg ->
                            val fileSize = reg.size()
                            if (expectedSize >= 0L && fileSize != expectedSize) {
                                return ReadChunkOutcome(
                                    ok = false,
                                    errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_CHANGED,
                                    errorDetail = "size_changed",
                                )
                            }
                            if (offset + length > fileSize) {
                                return ReadChunkOutcome(
                                    ok = false,
                                    errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_CHANGED,
                                    errorDetail = "range_past_eof",
                                )
                            }
                            if (length == 0) return ReadChunkOutcome(ok = true, data = ByteArray(0))
                            val out = ByteArray(length)
                            val buf = ByteBuffer.wrap(out)
                            var pos = offset
                            while (buf.hasRemaining()) {
                                val n = reg.readAt(buf, pos)
                                if (n < 0) {
                                    return ReadChunkOutcome(
                                        ok = false,
                                        errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_CHANGED,
                                        errorDetail = "unexpected_eof",
                                    )
                                }
                                pos += n
                            }
                            ReadChunkOutcome(ok = true, data = out)
                        }
                    }
                    is OpenedAttachmentSource.Stream -> {
                        activeCursor = StreamCursor(ref = ref, input = opened.input, pos = 0L)
                    }
                }
            }

            val cursor = activeCursor!!
            if (cursor.pos < offset) {
                val scratch = ByteArray(64 * 1024)
                while (cursor.pos < offset) {
                    val toRead = minOf(scratch.size.toLong(), offset - cursor.pos).toInt()
                    val n = cursor.input.read(scratch, 0, toRead)
                    if (n < 0) {
                        cursor.close()
                        activeCursor = null
                        return ReadChunkOutcome(
                            ok = false,
                            errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_CHANGED,
                            errorDetail = "unexpected_eof_before_offset",
                        )
                    }
                    cursor.pos += n
                }
            }
            val out = if (length == 0) {
                ByteArray(0)
            } else {
                val buf = ByteArray(length)
                var got = 0
                while (got < length) {
                    val n = cursor.input.read(buf, got, length - got)
                    if (n < 0) {
                        cursor.close()
                        activeCursor = null
                        return ReadChunkOutcome(
                            ok = false,
                            errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_CHANGED,
                            errorDetail = "unexpected_eof_in_chunk",
                        )
                    }
                    got += n
                    cursor.pos += n
                }
                buf
            }

            if (expectedSize >= 0L && cursor.pos == expectedSize) {
                // Final chunk reached expected EOF: probe 1 extra byte to catch oversized streams.
                val extra = cursor.input.read()
                cursor.close()
                activeCursor = null
                if (extra >= 0) {
                    return ReadChunkOutcome(
                        ok = false,
                        errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_CHANGED,
                        errorDetail = "size_exceeded",
                    )
                }
                return ReadChunkOutcome(ok = true, data = out)
            }

            // More chunks expected: park cursor for O(1) continuation on the next sequential chunk.
            parkCursor(cursor)
            activeCursor = null
            ReadChunkOutcome(ok = true, data = out)
        } catch (e: FileNotFoundException) {
            activeCursor?.close()
            ReadChunkOutcome(
                ok = false,
                errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING,
                errorDetail = e.message.orEmpty(),
            )
        } catch (e: SecurityException) {
            activeCursor?.close()
            ReadChunkOutcome(
                ok = false,
                errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING,
                errorDetail = e.message.orEmpty(),
            )
        } catch (e: Throwable) {
            activeCursor?.close()
            ReadChunkOutcome(
                ok = false,
                errorKind = if (isDirectoryError(e)) AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_DIRECTORY else classifyError(e),
                errorDetail = e.message.orEmpty(),
            )
        }
    }

    private fun parkCursor(cursor: StreamCursor) {
        val toClose = mutableListOf<StreamCursor>()
        synchronized(lock) {
            if (closed) {
                toClose.add(cursor)
            } else {
                cursors.put(cursor.ref, cursor)?.let { toClose.add(it) }
                while (cursors.size > maxOpenCursors) {
                    val eldest = cursors.entries.iterator().next()
                    toClose.add(eldest.value)
                    cursors.remove(eldest.key)
                }
            }
        }
        for (c in toClose) c.close()
    }

    fun closeRef(ref: String) {
        val removed = synchronized(lock) { cursors.remove(ref) }
        removed?.close()
    }

    fun closeAll() {
        val all = synchronized(lock) {
            val copy = cursors.values.toList()
            cursors.clear()
            copy
        }
        for (c in all) c.close()
    }

    override fun close() {
        closed = true
        closeAll()
    }
}

internal const val MAX_VIEWER_SOURCE_BYTES: Long = 20L * 1024L * 1024L

internal class ViewerSourceTooLargeException : IOException("viewer_source_too_large")

/** InputStream wrapper that hard-caps the number of bytes read to prevent OOM on giant viewer sources. */
internal class BoundedInputStream(
    input: InputStream,
    private val maxBytes: Long,
) : FilterInputStream(input) {
    private var totalRead = 0L
    @Volatile var exceeded: Boolean = false
        private set

    override fun read(): Int {
        val b = super.read()
        if (b >= 0) {
            totalRead++
            if (totalRead > maxBytes) {
                exceeded = true
                throw ViewerSourceTooLargeException()
            }
        }
        return b
    }

    override fun read(b: ByteArray, off: Int, len: Int): Int {
        if (len <= 0) return 0
        val remaining = (maxBytes - totalRead) + 1L
        if (remaining <= 0L) {
            exceeded = true
            throw ViewerSourceTooLargeException()
        }
        val cappedLen = minOf(len.toLong(), remaining).toInt()
        val n = super.read(b, off, cappedLen)
        if (n > 0) {
            totalRead += n
            if (totalRead > maxBytes) {
                exceeded = true
                throw ViewerSourceTooLargeException()
            }
        }
        return n
    }
}

/**
 * Two-pass bounded stream image decode: first reads bounds (`inJustDecodeBounds`), then decodes
 * with an appropriate `inSampleSize`, rejecting any stream exceeding [maxSourceBytes] without
 * buffering the whole source file into a heap `ByteArray`.
 */
internal fun decodeDownsampledStream(
    openStream: () -> InputStream?,
    maxLongSide: Int,
    maxSourceBytes: Long = MAX_VIEWER_SOURCE_BYTES,
    knownSizeBytes: Long = -1L,
): ImageBitmap? {
    if (knownSizeBytes > maxSourceBytes) return null
    return runCatching {
        val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
        val first = openStream() ?: return null
        val bounded1 = BoundedInputStream(first, maxSourceBytes)
        bounded1.use { BitmapFactory.decodeStream(it, null, bounds) }
        if (bounded1.exceeded || bounds.outWidth <= 0 || bounds.outHeight <= 0) return null
        var sample = 1
        while (maxOf(bounds.outWidth, bounds.outHeight) / (sample * 2) >= maxLongSide) {
            sample *= 2
        }
        val second = openStream() ?: return null
        val bounded2 = BoundedInputStream(second, maxSourceBytes)
        val bmp = bounded2.use {
            BitmapFactory.decodeStream(
                it,
                null,
                BitmapFactory.Options().apply { inSampleSize = sample },
            )
        }
        if (bounded2.exceeded) {
            bmp?.recycle()
            return null
        }
        bmp?.asImageBitmap()
    }.getOrNull()
}
