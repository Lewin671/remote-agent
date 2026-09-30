package io.github.lewin671.remoteagent.ui

import android.content.Context
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.ImageDecoder
import android.graphics.Matrix
import android.net.Uri
import android.os.Build
import io.github.lewin671.remoteagent.core.COMPRESSED_LONG_SIDE
import io.github.lewin671.remoteagent.core.MAX_ORIGINAL_BYTES
import io.github.lewin671.remoteagent.core.MAX_DECODE_PIXELS
import io.github.lewin671.remoteagent.core.decodeSample
import io.github.lewin671.remoteagent.core.detectFormat
import io.github.lewin671.remoteagent.core.fitLongSide
import io.github.lewin671.remoteagent.core.orientationOf
import io.github.lewin671.remoteagent.core.stripOriginal
import io.github.lewin671.remoteagent.proto.ImageFormat
import java.io.ByteArrayOutputStream

/** An image ready to upload: no metadata, the right way up. */
class Processed(val bytes: ByteArray, val format: ImageFormat, val width: Int, val height: Int, val note: String = "")

class ImageTooLarge(message: String) : Exception(message)

/** A compressed image aims at this size, so it goes in one command. */
private const val COMPRESSED_TARGET = 900 * 1024

/** Reads what the picker gave, up to [limit] bytes. */
fun readUri(context: Context, uri: Uri, limit: Long): ByteArray {
    val out = ByteArrayOutputStream()
    context.contentResolver.openInputStream(uri)?.use { input ->
        val buf = ByteArray(64 * 1024)
        var total = 0L
        while (true) {
            val n = input.read(buf)
            if (n < 0) break
            total += n
            if (total > limit) throw ImageTooLarge("图片超过 ${limit / 1024 / 1024} MB")
            out.write(buf, 0, n)
        }
    } ?: throw IllegalStateException("读不到这张图片")
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

/** A decoded picture and the sample size it was decoded at (1: full size). */
private class Decoded(val bitmap: Bitmap, val sample: Int)

/**
 * Decodes and turns the picture upright, at most [maxLong] on its long
 * side (0: full size), never more than [pixelBudget] pixels: the size is
 * read first and the image sampled down to fit. Formats the CLIs read are
 * decoded here with the orientation they record applied; others (HEIC) go
 * through ImageDecoder, which turns them itself. Intermediate bitmaps are
 * recycled as soon as the next one exists.
 */
private fun decodeUpright(data: ByteArray, maxLong: Int): Decoded {
    val budget = pixelBudget()
    val fmt = detectFormat(data)
    if (fmt == null) {
        if (Build.VERSION.SDK_INT < 28) throw IllegalArgumentException("这台手机读不了这种图片格式")
        var sample = 1
        val bmp = ImageDecoder.decodeBitmap(ImageDecoder.createSource(java.nio.ByteBuffer.wrap(data))) { d, info, _ ->
            d.allocator = ImageDecoder.ALLOCATOR_SOFTWARE
            sample = decodeSample(info.size.width, info.size.height, maxLong, budget)
            val (w, h) = if (maxLong > 0) fitLongSide(info.size.width, info.size.height, maxLong) else info.size.width to info.size.height
            val (bw, bh) = if (w.toLong() * h > budget) (info.size.width / sample) to (info.size.height / sample) else w to h
            if (bw != info.size.width || bh != info.size.height) d.setTargetSize(bw, bh)
        }
        return Decoded(bmp, sample)
    }
    val o = orientationOf(data)
    val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
    BitmapFactory.decodeByteArray(data, 0, data.size, bounds)
    if (bounds.outWidth <= 0) throw IllegalArgumentException("解不出这张图片")
    val sample = decodeSample(bounds.outWidth, bounds.outHeight, maxLong, budget)
    var bmp = BitmapFactory.decodeByteArray(data, 0, data.size, BitmapFactory.Options().apply { inSampleSize = sample })
        ?: throw IllegalArgumentException("解不出这张图片")
    if (maxLong > 0) {
        val (w, h) = fitLongSide(bmp.width, bmp.height, maxLong)
        if (w != bmp.width) bmp = Bitmap.createScaledBitmap(bmp, w, h, true).also { if (it !== bmp) bmp.recycle() }
    }
    if (o != 1) bmp = Bitmap.createBitmap(bmp, 0, 0, bmp.width, bmp.height, orientationMatrix(o), true).also { if (it !== bmp) bmp.recycle() }
    return Decoded(bmp, sample)
}

/** One image is prepared at a time: several full-size decodes at once could run out of memory. */
private val preparing = Any()

private fun encode(b: Bitmap, png: Boolean, quality: Int): ByteArray =
    ByteArrayOutputStream().also { b.compress(if (png) Bitmap.CompressFormat.PNG else Bitmap.CompressFormat.JPEG, quality, it) }.toByteArray()

/**
 * Prepares one picked image (architecture §17). Compressed: upright, long
 * side at most 2048, JPEG, or PNG when it has transparency; re-encoding
 * leaves every bit of metadata behind. Original: the file as it is, its
 * metadata taken out; a photo turned by its orientation tag, or a format
 * the CLIs do not read, is re-encoded at the highest quality instead, and
 * [Processed.note] says so.
 */
fun processImage(context: Context, uri: Uri, original: Boolean): Processed = synchronized(preparing) {
    try {
        prepare(context, uri, original)
    } catch (e: OutOfMemoryError) {
        // An error, not an exception: without this it would end the app.
        throw ImageTooLarge("这张图太大，手机内存不够处理")
    }
}

private fun prepare(context: Context, uri: Uri, original: Boolean): Processed {
    val data = readUri(context, uri, if (original) MAX_ORIGINAL_BYTES * 3 else 80L * 1024 * 1024)
    if (original) {
        stripOriginal(data)?.let { clean ->
            if (clean.size > MAX_ORIGINAL_BYTES) throw ImageTooLarge("原图超过 20 MB")
            val b = BitmapFactory.Options().apply { inJustDecodeBounds = true }
            BitmapFactory.decodeByteArray(clean, 0, clean.size, b)
            return Processed(clean, detectFormat(clean)!!, b.outWidth, b.outHeight)
        }
        val turned = detectFormat(data) != null
        val d = decodeUpright(data, 0)
        val bmp = d.bitmap
        val png = bmp.hasAlpha()
        val out = encode(bmp, png, if (turned) 100 else 95)
        val (w, h) = bmp.width to bmp.height
        bmp.recycle()
        if (out.size > MAX_ORIGINAL_BYTES) throw ImageTooLarge("原图超过 20 MB")
        val shrunk = if (d.sample > 1) "；像素超过手机能处理的上限，缩小到 1/${d.sample}" else ""
        return Processed(
            out, if (png) ImageFormat.IMAGE_FORMAT_PNG else ImageFormat.IMAGE_FORMAT_JPEG, w, h,
            (if (turned) "按拍摄方向转正后以最高画质重新编码" else "转成了 ${if (png) "PNG" else "JPEG"}，保留原始尺寸") + shrunk,
        )
    }
    var bmp = decodeUpright(data, COMPRESSED_LONG_SIDE).bitmap
    val png = bmp.hasAlpha()
    var q = 85
    var out = encode(bmp, png, q)
    while (out.size > COMPRESSED_TARGET && (q > 60 || maxOf(bmp.width, bmp.height) > 800)) {
        if (!png && q > 60) q -= 10
        else bmp = Bitmap.createScaledBitmap(bmp, (bmp.width * 0.8).toInt().coerceAtLeast(1), (bmp.height * 0.8).toInt().coerceAtLeast(1), true).also { if (it !== bmp) bmp.recycle() }
        out = encode(bmp, png, q)
    }
    val (w, h) = bmp.width to bmp.height
    bmp.recycle()
    return Processed(out, if (png) ImageFormat.IMAGE_FORMAT_PNG else ImageFormat.IMAGE_FORMAT_JPEG, w, h)
}
