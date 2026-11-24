package io.github.lewin671.remoteagent.data

import android.content.Context
import android.content.Intent
import android.graphics.BitmapFactory
import android.media.MediaMetadataRetriever
import android.net.Uri
import android.provider.DocumentsContract
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.system.ErrnoException
import android.system.OsConstants
import android.util.LruCache
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import io.github.lewin671.remoteagent.core.AttachmentIoExecutor
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
import io.github.lewin671.remoteagent.ui.encodeThumbnailBytes
import io.github.lewin671.remoteagent.ui.prepareImageFromSource
import java.io.File
import java.io.FileInputStream
import java.io.FileNotFoundException
import java.io.IOException
import java.nio.ByteBuffer
import java.security.KeyStore
import java.security.MessageDigest
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

/**
 * Android KeyStore-backed key provider for sealing persisted drafts, outbox records, and prepared
 * image/thumbnail blobs under `noBackupFilesDir/pending-images/`.
 */
internal class AndroidAttachmentKeyProvider(
    private val dir: File,
) : AttachmentStorageKeyProvider {
    private val keyFile = File(dir, "key.bin")
    @Volatile private var cachedKey: ByteArray? = null

    @Synchronized override fun loadExistingKey(): ByteArray? {
        cachedKey?.let { return it.copyOf() }
        val stored = readStoredKey() ?: return null
        cachedKey = stored
        return stored.copyOf()
    }

    @Synchronized override fun ensureKey(): ByteArray {
        cachedKey?.let { return it.copyOf() }
        readStoredKey()?.let {
            cachedKey = it
            return it.copyOf()
        }
        dir.mkdirs()
        val wrap = wrappingKey() ?: KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore").run {
            init(
                KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                    .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                    .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                    .setKeySize(256)
                    .build(),
            )
            generateKey()
        }
        val fresh = randomBytes(32)
        val c = Cipher.getInstance("AES/GCM/NoPadding")
        c.init(Cipher.ENCRYPT_MODE, wrap)
        val sealed = c.iv + c.doFinal(fresh)
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
        runCatching {
            val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
            if (ks.containsAlias(ALIAS)) ks.deleteEntry(ALIAS)
        }
    }

    private fun wrappingKey(): SecretKey? =
        runCatching {
            KeyStore.getInstance("AndroidKeyStore").apply { load(null) }.getKey(ALIAS, null) as? SecretKey
        }.getOrNull()

    private fun readStoredKey(wrap: SecretKey? = wrappingKey()): ByteArray? {
        if (wrap == null || !keyFile.isFile) return null
        return runCatching {
            val d = keyFile.readBytes()
            if (d.size <= 12) return null
            val c = Cipher.getInstance("AES/GCM/NoPadding")
            c.init(Cipher.DECRYPT_MODE, wrap, GCMParameterSpec(128, d, 0, 12))
            c.doFinal(d, 12, d.size - 12)
        }.getOrNull()
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
                thumbCache.remove(ref)
            }

            override fun onAllCleared() {
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
    }

    suspend fun clearAllOnDisk() {
        executor.clearAllAndWait()
        thumbCache.evictAll()
    }

    fun usedBytes(): Long = executor.usedBytes()

    fun close() {
        executor.close()
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
            val bytes = when {
                preparedRef.startsWith(LOCAL_BLOB_REF_PREFIX) -> executor.readEncryptedBlobFull(preparedRef)
                sourceRef.startsWith(LOCAL_BLOB_REF_PREFIX) -> executor.readEncryptedBlobFull(sourceRef)
                sourceRef.isNotEmpty() -> runCatching {
                    context.contentResolver.openInputStream(Uri.parse(sourceRef))?.use { it.readBytes() }
                }.getOrNull()
                else -> null
            } ?: return@withContext null
            decodeDownsampled(bytes, maxLongSide = 2048)
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

    private fun executeReadFileChunk(eff: AttachmentReadChunkEffect): ReadChunkOutcome {
        val ref = eff.sourceRef
        if (ref.isEmpty()) {
            return ReadChunkOutcome(
                ok = false,
                errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING,
                errorDetail = "empty_source_ref",
            )
        }
        val uri = Uri.parse(ref)
        if (isDirectoryUri(uri)) {
            return ReadChunkOutcome(
                ok = false,
                errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_DIRECTORY,
                errorDetail = "directory",
            )
        }
        val offset = eff.offset
        val length = eff.length
        val expectedSize = eff.expectedSizeBytes
        if (offset < 0L || length < 0) {
            return ReadChunkOutcome(
                ok = false,
                errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_OTHER,
                errorDetail = "invalid_range",
            )
        }
        return try {
            // Fast O(1) seekable FileChannel path for local files & SAF providers.
            val pfd = try {
                if (uri.scheme == "file" && uri.path != null) {
                    val f = File(uri.path!!)
                    if (f.isDirectory) {
                        return ReadChunkOutcome(
                            ok = false,
                            errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_DIRECTORY,
                            errorDetail = "directory",
                        )
                    }
                    android.os.ParcelFileDescriptor.open(f, android.os.ParcelFileDescriptor.MODE_READ_ONLY)
                } else {
                    context.contentResolver.openFileDescriptor(uri, "r")
                }
            } catch (e: FileNotFoundException) {
                return ReadChunkOutcome(
                    ok = false,
                    errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING,
                    errorDetail = e.message.orEmpty(),
                )
            } catch (e: SecurityException) {
                return ReadChunkOutcome(
                    ok = false,
                    errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING,
                    errorDetail = e.message.orEmpty(),
                )
            }
            if (pfd != null) {
                pfd.use { fd ->
                    FileInputStream(fd.fileDescriptor).use { fis ->
                        val ch = fis.channel
                        val channelSize = runCatching { ch.size() }.getOrNull()
                        if (channelSize != null && channelSize >= 0L) {
                            if (expectedSize >= 0L && channelSize != expectedSize) {
                                return ReadChunkOutcome(
                                    ok = false,
                                    errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_CHANGED,
                                    errorDetail = "size_changed",
                                )
                            }
                            if (offset + length > channelSize) {
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
                                val n = ch.read(buf, pos)
                                if (n < 0) {
                                    return ReadChunkOutcome(
                                        ok = false,
                                        errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_CHANGED,
                                        errorDetail = "unexpected_eof",
                                    )
                                }
                                pos += n
                            }
                            return ReadChunkOutcome(ok = true, data = out)
                        }
                    }
                }
            }
            // Fallback for non-seekable virtual content streams.
            val stream = openSourceStream(uri)
                ?: return ReadChunkOutcome(
                    ok = false,
                    errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING,
                    errorDetail = "cannot_open_stream",
                )
            stream.use { input ->
                val scratch = ByteArray(64 * 1024)
                var skipped = 0L
                while (skipped < offset) {
                    val toRead = minOf(scratch.size.toLong(), offset - skipped).toInt()
                    val n = input.read(scratch, 0, toRead)
                    if (n < 0) {
                        return ReadChunkOutcome(
                            ok = false,
                            errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_CHANGED,
                            errorDetail = "unexpected_eof_before_offset",
                        )
                    }
                    skipped += n
                }
                if (length == 0) return ReadChunkOutcome(ok = true, data = ByteArray(0))
                val out = ByteArray(length)
                var got = 0
                while (got < length) {
                    val n = input.read(out, got, length - got)
                    if (n < 0) {
                        return ReadChunkOutcome(
                            ok = false,
                            errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_CHANGED,
                            errorDetail = "unexpected_eof_in_chunk",
                        )
                    }
                    got += n
                }
                ReadChunkOutcome(ok = true, data = out)
            }
        } catch (e: FileNotFoundException) {
            ReadChunkOutcome(
                ok = false,
                errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING,
                errorDetail = e.message.orEmpty(),
            )
        } catch (e: SecurityException) {
            ReadChunkOutcome(
                ok = false,
                errorKind = AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING,
                errorDetail = e.message.orEmpty(),
            )
        } catch (e: Throwable) {
            ReadChunkOutcome(
                ok = false,
                errorKind = if (isDirectoryError(e)) AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_DIRECTORY else executor.classifyIoError(e),
                errorDetail = e.message.orEmpty(),
            )
        }
    }

    private fun openSourceStream(uri: Uri) =
        if (uri.scheme == "file" && uri.path != null) {
            val f = File(uri.path!!)
            if (f.isDirectory) throw IOException("EISDIR")
            FileInputStream(f)
        } else {
            context.contentResolver.openInputStream(uri)
        }

    private fun isDirectoryUri(uri: Uri): Boolean {
        if (uri.scheme == "file" && uri.path != null) {
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
            if (uri.scheme == "file" && uri.path != null) {
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
