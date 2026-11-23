package io.github.lewin671.remoteagent.data

import android.content.ActivityNotFoundException
import android.content.ComponentName
import android.content.ContentValues
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.ImageDecoder
import android.media.MediaMetadataRetriever
import android.net.Uri
import android.os.Build
import android.os.Environment
import android.os.StatFs
import android.provider.MediaStore
import android.system.ErrnoException
import android.system.OsConstants
import android.util.LruCache
import android.webkit.MimeTypeMap
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.core.content.FileProvider
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.SharedFileIoExecutor
import io.github.lewin671.remoteagent.core.proto.SharedFileCompletedEffect
import io.github.lewin671.remoteagent.core.proto.SharedFileFailureKind
import io.github.lewin671.remoteagent.core.proto.SharedFileKind
import java.io.File
import java.io.IOException
import java.util.Locale
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.asSharedFlow
import kotlinx.coroutines.withContext

/**
 * App-wide platform coordinator for Agent-shared files (`cache/shared-files/<sessionId>/<fileId>/`).
 *
 * Download orchestration, chunking, reconnect, ETA, and auto-open eligibility live in Core.
 * This class owns the single global FIFO storage executor, thumbnail/duration extraction,
 * and Android open/preview/export/share APIs.
 */
class SharedFileManager(
    private val context: Context,
    private val remoteProvider: () -> Remote?,
) {
    val rootDir = File(context.cacheDir, "shared-files")

    private val thumbCache = LruCache<String, ImageBitmap>(64)

    private val _completions = MutableSharedFlow<SharedFileCompletedEffect>(extraBufferCapacity = 32)
    val completions: SharedFlow<SharedFileCompletedEffect> = _completions.asSharedFlow()

    private val visibleLock = Any()
    private var visibleToken: Any? = null
    private var visibleSessionId: String = ""

    val executor = SharedFileIoExecutor(
        rootDir = rootDir,
        availableBytesProvider = { dir ->
            dir.mkdirs()
            StatFs(dir.path).availableBytes
        },
        durationExtractor = { file -> extractMediaDurationMs(file) },
        isStorageFullError = { t ->
            t is ErrnoException && (t.errno == OsConstants.ENOSPC || t.errno == OsConstants.EDQUOT)
        },
        onLocalDeleted = { _, _ -> thumbCache.evictAll() },
    )

    /** Wires the FIFO I/O executor to [remote]'s CoreHost, configures Android cache policy, and scans disk. */
    fun attachRemote(remote: Remote) {
        executor.attach(remote.coreHost)
        remote.coreHost.onSharedFileCompleted = { eff -> _completions.tryEmit(eff) }
        remote.coreHost.sendSharedFileConfig(
            localCacheMaxBytes = 0L,
            storageFullKind = SharedFileFailureKind.SHARED_FILE_FAILURE_KIND_PHONE_STORAGE_FULL,
            scanLocal = true,
        )
        val activeSid = synchronized(visibleLock) { visibleSessionId }
        if (activeSid.isNotEmpty()) {
            remote.setSharedFileVisibleSession(activeSid)
        }
    }

    fun enterVisibleSession(token: Any, sessionId: String) {
        if (sessionId.isEmpty()) return
        synchronized(visibleLock) {
            visibleToken = token
            visibleSessionId = sessionId
        }
        remoteProvider()?.setSharedFileVisibleSession(sessionId)
    }

    fun leaveVisibleSession(token: Any) {
        val cleared = synchronized(visibleLock) {
            if (visibleToken === token) {
                visibleToken = null
                visibleSessionId = ""
                true
            } else {
                false
            }
        }
        if (cleared) {
            remoteProvider()?.setSharedFileVisibleSession("")
        }
    }

    fun startDownload(sessionId: String, fileId: String, autoOpenIfStaying: Boolean = true) {
        remoteProvider()?.startSharedFileDownload(sessionId, fileId, autoOpenIfStaying)
    }

    fun pause(sessionId: String, fileId: String) {
        remoteProvider()?.pauseSharedFileDownload(sessionId, fileId)
    }

    fun cancel(sessionId: String, fileId: String) {
        remoteProvider()?.cancelSharedFileDownload(sessionId, fileId)
    }

    fun deleteLocal(sessionId: String, fileId: String) {
        remoteProvider()?.deleteSharedFileLocal(sessionId, fileId)
    }

    fun forgetSession(sessionId: String) {
        val r = remoteProvider()
        if (r != null) {
            r.forgetSharedFileSession(sessionId)
        } else {
            executor.enqueueDeleteSession(sessionId)
        }
    }

    suspend fun clearAll() {
        val r = remoteProvider()
        if (r != null) {
            r.clearAllSharedFilesAndWait()
            executor.awaitIdle()
        } else {
            executor.clearAllAndWait()
        }
        thumbCache.evictAll()
    }

    fun clearAllOnDiskAsync() {
        executor.enqueueClearAll()
        thumbCache.evictAll()
    }

    suspend fun clearAllOnDisk() {
        executor.clearAllAndWait()
        thumbCache.evictAll()
    }

    fun reportDurationProbed(sessionId: String, fileId: String, durationMs: Long) {
        if (durationMs <= 0L) return
        executor.enqueuePersistDuration(sessionId, fileId, durationMs)
        remoteProvider()?.reportSharedFileDurationProbed(sessionId, fileId, durationMs)
    }

    fun reportLocalMissing(sessionId: String, fileId: String) {
        remoteProvider()?.reportSharedFileLocalMissing(sessionId, fileId)
    }

    fun localFileIfDownloaded(sessionId: String, fileId: String, displayName: String = ""): File? =
        executor.localFileIfDownloaded(sessionId, fileId, displayName)

    fun localFilePath(sessionId: String, fileId: String, displayName: String): File =
        executor.localFilePath(sessionId, fileId, displayName)

    fun usedBytes(): Long = executor.usedBytes()

    fun close() {
        executor.close()
    }

    fun seedCommittedFileForTest(
        sessionId: String,
        fileId: String,
        displayName: String,
        bytes: ByteArray,
        durationMs: Long? = null,
    ): File = executor.seedCommittedFileForTest(sessionId, fileId, displayName, bytes, durationMs)

    fun emitCompletedForTest(effect: SharedFileCompletedEffect) {
        _completions.tryEmit(effect)
    }

    fun putThumbnailForTest(file: File, targetPx: Int, bitmap: ImageBitmap) {
        thumbCache.put("${file.absolutePath}:${file.lastModified()}:$targetPx", bitmap)
    }

    suspend fun loadThumbnail(file: File, kind: SharedFileKind, targetPx: Int): ImageBitmap? {
        if (!file.isFile) return null
        if (kind != SharedFileKind.SHARED_FILE_KIND_IMAGE && kind != SharedFileKind.SHARED_FILE_KIND_VIDEO) return null
        val key = "${file.absolutePath}:${file.lastModified()}:$targetPx"
        thumbCache.get(key)?.let { return it }
        val decoded = withContext(Dispatchers.IO) {
            decodeThumbnail(file, kind, targetPx)
        } ?: return null
        thumbCache.put(key, decoded)
        return decoded
    }

    private fun decodeThumbnail(file: File, kind: SharedFileKind, targetPx: Int): ImageBitmap? =
        runCatching {
            val bmp = when (kind) {
                SharedFileKind.SHARED_FILE_KIND_IMAGE -> decodeImageThumb(file, targetPx)
                SharedFileKind.SHARED_FILE_KIND_VIDEO -> decodeVideoThumb(file, targetPx)
                else -> null
            }
            bmp?.asImageBitmap()
        }.getOrNull()

    private fun decodeImageThumb(file: File, targetPx: Int): Bitmap? {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) {
            val source = ImageDecoder.createSource(file)
            return ImageDecoder.decodeBitmap(source) { decoder, info, _ ->
                val w = info.size.width
                val h = info.size.height
                val minSide = minOf(w, h).coerceAtLeast(1)
                if (minSide > targetPx) {
                    val scale = targetPx.toFloat() / minSide
                    decoder.setTargetSize((w * scale).toInt().coerceAtLeast(1), (h * scale).toInt().coerceAtLeast(1))
                }
            }
        }
        val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
        BitmapFactory.decodeFile(file.absolutePath, bounds)
        var sample = 1
        while (minOf(bounds.outWidth, bounds.outHeight) / (sample * 2) >= targetPx) {
            sample *= 2
        }
        return BitmapFactory.decodeFile(
            file.absolutePath,
            BitmapFactory.Options().apply { inSampleSize = sample },
        )
    }

    private fun decodeVideoThumb(file: File, targetPx: Int): Bitmap? {
        val mmr = MediaMetadataRetriever()
        return try {
            mmr.setDataSource(file.absolutePath)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O_MR1) {
                mmr.getScaledFrameAtTime(-1L, MediaMetadataRetriever.OPTION_CLOSEST_SYNC, targetPx * 2, targetPx * 2)
                    ?: mmr.getFrameAtTime(-1L, MediaMetadataRetriever.OPTION_CLOSEST_SYNC)
            } else {
                mmr.getFrameAtTime(-1L, MediaMetadataRetriever.OPTION_CLOSEST_SYNC)
            }
        } finally {
            runCatching { mmr.release() }
        }
    }

    private fun extractMediaDurationMs(file: File): Long? {
        val mmr = MediaMetadataRetriever()
        return try {
            mmr.setDataSource(file.absolutePath)
            mmr.extractMetadata(MediaMetadataRetriever.METADATA_KEY_DURATION)?.toLongOrNull()?.takeIf { it > 0L }
        } catch (_: Exception) {
            null
        } finally {
            runCatching { mmr.release() }
        }
    }

    private fun safeName(displayName: String): String = SharedFileIoExecutor.safeLocalFileName(displayName)

    fun contentUri(file: File): Uri =
        FileProvider.getUriForFile(context, "${context.packageName}.files", file)

    fun mimeTypeFor(displayName: String): String {
        val clean = safeName(displayName)
        val ext = clean.substringAfterLast('.', "").lowercase(Locale.ROOT)
        if (ext.isEmpty()) return "application/octet-stream"
        return MimeTypeMap.getSingleton().getMimeTypeFromExtension(ext)
            ?: when (ext) {
                "md", "txt", "log", "json", "yaml", "yml", "toml" -> "text/plain"
                "pdf" -> "application/pdf"
                "zip" -> "application/zip"
                "mp4", "m4v" -> "video/mp4"
                "mov" -> "video/quicktime"
                "png" -> "image/png"
                "jpg", "jpeg" -> "image/jpeg"
                "webp" -> "image/webp"
                "mp3" -> "audio/mpeg"
                "m4a" -> "audio/mp4"
                else -> "application/octet-stream"
            }
    }

    /**
     * Always shows the system chooser (titled with the file name) so no app silently takes the file
     * over; returns false, showing nothing, when no app can open it.
     */
    fun openExternal(activityContext: Context, file: File, displayName: String): Boolean {
        val safe = safeName(displayName)
        val uri = contentUri(file)
        val mime = if (isSharedTextFile(safe)) "text/plain" else mimeTypeFor(safe)
        val view = Intent(Intent.ACTION_VIEW)
            .setDataAndType(uri, mime)
            .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        @Suppress("DEPRECATION")
        val handlers = activityContext.packageManager.queryIntentActivities(view, PackageManager.MATCH_DEFAULT_ONLY)
        // Markdown-only readers are additional choices; wildcard/plain-text readers appear once.
        val markdown = Intent(view).setDataAndType(uri, "text/markdown")
        @Suppress("DEPRECATION")
        val initial = if (isSharedMarkdownFile(safe)) {
            val plainPackages = handlers.map { it.activityInfo.packageName }.toSet()
            activityContext.packageManager.queryIntentActivities(markdown, PackageManager.MATCH_DEFAULT_ONLY)
                .filter { it.activityInfo.packageName !in plainPackages }
                .distinctBy { it.activityInfo.packageName }
                .map { Intent(markdown).setComponent(ComponentName(it.activityInfo.packageName, it.activityInfo.name)) }
        } else emptyList()
        if (handlers.isEmpty() && initial.isEmpty()) return false
        val chooser = Intent.createChooser(view, safe)
            .putExtra(Intent.EXTRA_AUTO_LAUNCH_SINGLE_CHOICE, false)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        if (initial.isNotEmpty()) chooser.putExtra(Intent.EXTRA_INITIAL_INTENTS, initial.toTypedArray())
        return try {
            activityContext.startActivity(chooser)
            true
        } catch (_: ActivityNotFoundException) {
            false
        }
    }

    fun shareFile(activityContext: Context, file: File, displayName: String) {
        val safe = safeName(displayName)
        val uri = contentUri(file)
        val mime = mimeTypeFor(safe)
        val intent = Intent(Intent.ACTION_SEND)
            .setType(mime)
            .putExtra(Intent.EXTRA_STREAM, uri)
            .putExtra(Intent.EXTRA_TITLE, safe)
            .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        val chooser = Intent.createChooser(intent, safe)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        activityContext.startActivity(chooser)
    }

    suspend fun saveToAlbum(file: File, kind: SharedFileKind, displayName: String): Result<Unit> =
        withContext(Dispatchers.IO) {
            runCatching {
                val safe = safeName(displayName)
                val mime = mimeTypeFor(safe)
                val collection = when (kind) {
                    SharedFileKind.SHARED_FILE_KIND_VIDEO -> MediaStore.Video.Media.EXTERNAL_CONTENT_URI
                    else -> MediaStore.Images.Media.EXTERNAL_CONTENT_URI
                }
                val relDir = when (kind) {
                    SharedFileKind.SHARED_FILE_KIND_VIDEO -> Environment.DIRECTORY_MOVIES + "/remote-agent"
                    else -> Environment.DIRECTORY_PICTURES + "/remote-agent"
                }
                insertMediaStoreFile(collection, relDir, safe, mime, file)
            }
        }

    suspend fun saveToDownloads(file: File, displayName: String): Result<Unit> =
        withContext(Dispatchers.IO) {
            runCatching {
                val safe = safeName(displayName)
                val mime = mimeTypeFor(safe)
                if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                    insertMediaStoreFile(
                        MediaStore.Downloads.EXTERNAL_CONTENT_URI,
                        Environment.DIRECTORY_DOWNLOADS + "/RemoteAgent",
                        safe,
                        mime,
                        file,
                    )
                } else {
                    @Suppress("DEPRECATION")
                    val downloadsDir = File(Environment.getExternalStoragePublicDirectory(Environment.DIRECTORY_DOWNLOADS), "RemoteAgent")
                    downloadsDir.mkdirs()
                    val dest = uniqueDest(downloadsDir, safe)
                    file.inputStream().use { input ->
                        dest.outputStream().use { output -> input.copyTo(output) }
                    }
                    Unit
                }
            }
        }

    private fun insertMediaStoreFile(
        collection: Uri,
        relativePath: String,
        displayName: String,
        mimeType: String,
        source: File,
    ) {
        val values = ContentValues().apply {
            put(MediaStore.MediaColumns.DISPLAY_NAME, displayName)
            put(MediaStore.MediaColumns.MIME_TYPE, mimeType)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                put(MediaStore.MediaColumns.RELATIVE_PATH, relativePath)
                put(MediaStore.MediaColumns.IS_PENDING, 1)
            }
        }
        val resolver = context.contentResolver
        val uri = resolver.insert(collection, values) ?: throw IOException("MediaStore insert failed")
        try {
            resolver.openOutputStream(uri)?.use { out ->
                source.inputStream().use { input -> input.copyTo(out) }
            } ?: throw IOException("Cannot open MediaStore stream")
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                values.clear()
                values.put(MediaStore.MediaColumns.IS_PENDING, 0)
                resolver.update(uri, values, null, null)
            }
        } catch (e: Exception) {
            resolver.delete(uri, null, null)
            throw e
        }
    }

    private fun uniqueDest(dir: File, name: String): File {
        val candidate = File(dir, name)
        if (!candidate.exists()) return candidate
        val dot = name.lastIndexOf('.')
        val stem = if (dot > 0) name.substring(0, dot) else name
        val ext = if (dot > 0) name.substring(dot) else ""
        var n = 1
        while (true) {
            val next = File(dir, "$stem ($n)$ext")
            if (!next.exists()) return next
            n++
        }
    }
}
