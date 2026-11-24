package io.github.lewin671.remoteagent.ui

import android.content.Context
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.ImageDecoder
import android.graphics.Matrix
import android.net.Uri
import android.os.Build
import io.github.lewin671.remoteagent.core.LOCAL_BLOB_REF_PREFIX
import io.github.lewin671.remoteagent.core.MAX_DECODE_PIXELS
import io.github.lewin671.remoteagent.core.decodeSample
import io.github.lewin671.remoteagent.core.detectFormat
import io.github.lewin671.remoteagent.core.fitLongSide
import io.github.lewin671.remoteagent.core.orientationOf
import io.github.lewin671.remoteagent.core.proto.AttachmentIOErrorKind
import io.github.lewin671.remoteagent.core.stripOriginal
import io.github.lewin671.remoteagent.proto.ImageFormat
import java.io.ByteArrayOutputStream
import java.io.File
import java.io.FileInputStream
import java.io.FileNotFoundException
import java.io.IOException

/** An image prepared on the phone: no metadata, upright, encoded per Core's effect parameters. */
class PreparedImageBytes(
    val bytes: ByteArray,
    val format: ImageFormat,
    val width: Int,
    val height: Int,
)

/** Structured preparation failure mapped directly to Core's [AttachmentIOErrorKind]. */
class PrepareImageException(
    val kind: AttachmentIOErrorKind,
    val isReadingSource: Boolean = false,
    val errorCode: String = "",
    val detail: String = "",
    cause: Throwable? = null,
) : Exception(detail.ifEmpty { kind.name }, cause)

/** Reads the raw source bytes up to [readLimitBytes], reporting structured I/O errors. */
private fun readSourceBytes(
    context: Context,
    sourceRef: String,
    readLimitBytes: Long,
    original: Boolean,
    readLocalBlob: (String) -> ByteArray?,
): ByteArray {
    if (sourceRef.isEmpty()) {
        throw PrepareImageException(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING, isReadingSource = true, detail = "empty_source_ref")
    }
    if (sourceRef.startsWith(LOCAL_BLOB_REF_PREFIX)) {
        val blob = readLocalBlob(sourceRef)
            ?: throw PrepareImageException(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING, isReadingSource = true, detail = "missing_blob")
        if (blob.size > readLimitBytes) {
            throw PrepareImageException(
                if (original) AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_ORIGINAL_TOO_LARGE
                else AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_DIMENSIONS,
                detail = "source_exceeds_read_limit",
            )
        }
        return blob
    }
    val uri = Uri.parse(sourceRef)
    val stream = try {
        if (uri.scheme == "file" && uri.path != null) {
            FileInputStream(File(uri.path!!))
        } else {
            context.contentResolver.openInputStream(uri)
        }
    } catch (e: FileNotFoundException) {
        throw PrepareImageException(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING, isReadingSource = true, detail = e.message.orEmpty(), cause = e)
    } catch (e: SecurityException) {
        throw PrepareImageException(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING, isReadingSource = true, detail = e.message.orEmpty(), cause = e)
    } catch (e: IOException) {
        throw PrepareImageException(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_DECODE, isReadingSource = true, detail = e.message.orEmpty(), cause = e)
    } ?: throw PrepareImageException(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING, isReadingSource = true, detail = "null_input_stream")

    val out = ByteArrayOutputStream()
    try {
        stream.use { input ->
            val buf = ByteArray(64 * 1024)
            var total = 0L
            while (true) {
                val n = input.read(buf)
                if (n < 0) break
                total += n
                if (total > readLimitBytes) {
                    throw PrepareImageException(
                        if (original) AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_ORIGINAL_TOO_LARGE
                        else AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_DIMENSIONS,
                        detail = "source_exceeds_read_limit",
                    )
                }
                out.write(buf, 0, n)
            }
        }
    } catch (e: PrepareImageException) {
        throw e
    } catch (e: SecurityException) {
        throw PrepareImageException(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_MISSING, isReadingSource = true, detail = e.message.orEmpty(), cause = e)
    } catch (e: IOException) {
        throw PrepareImageException(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_DECODE, isReadingSource = true, detail = e.message.orEmpty(), cause = e)
    }
    return out.toByteArray()
}

/** An orientation (1 to 8) as the matrix that turns a stored picture upright. */
private fun orientationMatrix(o: Int): Matrix = Matrix().apply {
    when (o) {
        2 -> postScale(-1f, 1f)
        3 -> postRotate(180f)
        4 -> postScale(1f, -1f)
        5 -> { postRotate(90f); postScale(-1f, 1f) }
        6 -> postRotate(90f)
        7 -> { postRotate(270f); postScale(-1f, 1f) }
        8 -> postRotate(270f)
    }
}

/**
 * Pixels this process may decode at once: the fixed cap, and a share of
 * the heap that leaves room for the rotated copy and the encoded bytes.
 */
private fun pixelBudget(): Long = minOf(MAX_DECODE_PIXELS, Runtime.getRuntime().maxMemory() / 4 / 4)

private fun decodeUpright(data: ByteArray, maxLong: Int): Bitmap {
    val budget = pixelBudget()
    val fmt = detectFormat(data)
    if (fmt == null) {
        if (Build.VERSION.SDK_INT < 28) {
            throw PrepareImageException(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_DECODE, detail = "unsupported_image_format")
        }
        return try {
            ImageDecoder.decodeBitmap(ImageDecoder.createSource(java.nio.ByteBuffer.wrap(data))) { d, info, _ ->
                d.allocator = ImageDecoder.ALLOCATOR_SOFTWARE
                if (info.size.width <= 0 || info.size.height <= 0) {
                    throw PrepareImageException(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_DIMENSIONS, detail = "zero_dimensions")
                }
                val sample = decodeSample(info.size.width, info.size.height, maxLong, budget)
                val (w, h) = if (maxLong > 0) fitLongSide(info.size.width, info.size.height, maxLong) else info.size.width to info.size.height
                val (bw, bh) = if (w.toLong() * h > budget) (info.size.width / sample) to (info.size.height / sample) else w to h
                if (bw != info.size.width || bh != info.size.height) d.setTargetSize(bw, bh)
            }
        } catch (e: PrepareImageException) {
            throw e
        } catch (e: Throwable) {
            throw PrepareImageException(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_DECODE, detail = e.message.orEmpty(), cause = e)
        }
    }
    val o = orientationOf(data)
    val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
    BitmapFactory.decodeByteArray(data, 0, data.size, bounds)
    if (bounds.outWidth <= 0 || bounds.outHeight <= 0) {
        throw PrepareImageException(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_DECODE, detail = "cannot_decode_bounds")
    }
    val sample = decodeSample(bounds.outWidth, bounds.outHeight, maxLong, budget)
    var bmp = BitmapFactory.decodeByteArray(data, 0, data.size, BitmapFactory.Options().apply { inSampleSize = sample })
        ?: throw PrepareImageException(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_DECODE, detail = "cannot_decode_bitmap")
    if (maxLong > 0) {
        val (w, h) = fitLongSide(bmp.width, bmp.height, maxLong)
        if (w != bmp.width) bmp = Bitmap.createScaledBitmap(bmp, w, h, true).also { if (it !== bmp) bmp.recycle() }
    }
    if (o != 1) {
        bmp = Bitmap.createBitmap(bmp, 0, 0, bmp.width, bmp.height, orientationMatrix(o), true).also { if (it !== bmp) bmp.recycle() }
    }
    return bmp
}

/** One image is prepared at a time: several full-size decodes at once could run out of memory. */
private val preparingLock = Any()

private fun encodeBitmap(b: Bitmap, png: Boolean, quality: Int): ByteArray =
    ByteArrayOutputStream().also { b.compress(if (png) Bitmap.CompressFormat.PNG else Bitmap.CompressFormat.JPEG, quality, it) }.toByteArray()

/**
 * Prepares an image strictly according to Core's `AttachmentPrepareImageEffect` parameters
 * ([maxOriginalBytes], [maxDimensionPx], [maxCompressedBytes]). Never hardcodes budgets.
 */
fun prepareImageFromSource(
    context: Context,
    sourceRef: String,
    original: Boolean,
    maxOriginalBytes: Long,
    maxDimensionPx: Int,
    maxCompressedBytes: Long,
    readLocalBlob: (String) -> ByteArray? = { null },
): PreparedImageBytes = synchronized(preparingLock) {
    try {
        val readLimit = if (original) maxOriginalBytes * 3L else maxOf(80L * 1024 * 1024, maxOriginalBytes * 3L)
        val data = readSourceBytes(context, sourceRef, readLimit, original, readLocalBlob)
        if (original) {
            val stripped = try {
                stripOriginal(data)
            } catch (e: Throwable) {
                throw PrepareImageException(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_DECODE, detail = e.message.orEmpty(), cause = e)
            }
            if (stripped != null) {
                if (stripped.size > maxOriginalBytes) {
                    throw PrepareImageException(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_ORIGINAL_TOO_LARGE, detail = "original_exceeds_limit")
                }
                val b = BitmapFactory.Options().apply { inJustDecodeBounds = true }
                BitmapFactory.decodeByteArray(stripped, 0, stripped.size, b)
                val fmt = detectFormat(stripped)
                    ?: throw PrepareImageException(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_DECODE, detail = "unknown_stripped_format")
                return@synchronized PreparedImageBytes(stripped, fmt, b.outWidth, b.outHeight)
            }
            val turned = detectFormat(data) != null
            val bmp = decodeUpright(data, maxLong = 0)
            val png = bmp.hasAlpha()
            val out = encodeBitmap(bmp, png, if (turned) 100 else 95)
            val (w, h) = bmp.width to bmp.height
            bmp.recycle()
            if (out.size > maxOriginalBytes) {
                throw PrepareImageException(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_ORIGINAL_TOO_LARGE, detail = "original_exceeds_limit")
            }
            return@synchronized PreparedImageBytes(
                out,
                if (png) ImageFormat.IMAGE_FORMAT_PNG else ImageFormat.IMAGE_FORMAT_JPEG,
                w,
                h,
            )
        }

        var bmp = decodeUpright(data, maxLong = maxDimensionPx)
        val png = bmp.hasAlpha()
        var q = 85
        var out = encodeBitmap(bmp, png, q)
        while (out.size > maxCompressedBytes && (q > 60 || maxOf(bmp.width, bmp.height) > 800)) {
            if (!png && q > 60) {
                q -= 10
            } else {
                bmp = Bitmap.createScaledBitmap(
                    bmp,
                    (bmp.width * 0.8).toInt().coerceAtLeast(1),
                    (bmp.height * 0.8).toInt().coerceAtLeast(1),
                    true,
                ).also { if (it !== bmp) bmp.recycle() }
            }
            out = encodeBitmap(bmp, png, q)
        }
        val (w, h) = bmp.width to bmp.height
        bmp.recycle()
        PreparedImageBytes(
            out,
            if (png) ImageFormat.IMAGE_FORMAT_PNG else ImageFormat.IMAGE_FORMAT_JPEG,
            w,
            h,
        )
    } catch (e: PrepareImageException) {
        throw e
    } catch (e: OutOfMemoryError) {
        throw PrepareImageException(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_DIMENSIONS, detail = "out_of_memory", cause = e)
    }
}

/** Encodes a compact JPEG preview thumbnail (<= 256px long side) from already-prepared image bytes. */
fun encodeThumbnailBytes(preparedBytes: ByteArray): ByteArray? = runCatching {
    val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
    BitmapFactory.decodeByteArray(preparedBytes, 0, preparedBytes.size, bounds)
    if (bounds.outWidth <= 0 || bounds.outHeight <= 0) return null
    var sample = 1
    while (maxOf(bounds.outWidth, bounds.outHeight) / (sample * 2) >= 256) sample *= 2
    val bmp = BitmapFactory.decodeByteArray(
        preparedBytes,
        0,
        preparedBytes.size,
        BitmapFactory.Options().apply { inSampleSize = sample },
    ) ?: return null
    try {
        ByteArrayOutputStream().also { bmp.compress(Bitmap.CompressFormat.JPEG, 80, it) }.toByteArray()
    } finally {
        bmp.recycle()
    }
}.getOrNull()
