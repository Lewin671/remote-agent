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
 * Decodes and turns the picture upright, at most [maxLong] on its long
 * side (0: full size). Formats the CLIs read are decoded here with the
 * orientation they record applied; others (HEIC) go through ImageDecoder,
 * which turns them itself.
 */
private fun decodeUpright(data: ByteArray, maxLong: Int): Bitmap {
    val fmt = detectFormat(data)
    if (fmt == null) {
        if (Build.VERSION.SDK_INT < 28) throw IllegalArgumentException("这台手机读不了这种图片格式")
        return ImageDecoder.decodeBitmap(ImageDecoder.createSource(java.nio.ByteBuffer.wrap(data))) { d, info, _ ->
            d.allocator = ImageDecoder.ALLOCATOR_SOFTWARE
            if (maxLong > 0) {
                val (w, h) = fitLongSide(info.size.width, info.size.height, maxLong)
                d.setTargetSize(w, h)
            }
        }
    }
    val o = orientationOf(data)
    val bounds = BitmapFactory.Options().apply { inJustDecodeBounds = true }
    BitmapFactory.decodeByteArray(data, 0, data.size, bounds)
    if (bounds.outWidth <= 0) throw IllegalArgumentException("解不出这张图片")
    var sample = 1
    if (maxLong > 0) while (maxOf(bounds.outWidth, bounds.outHeight) / (sample * 2) >= maxLong) sample *= 2
    val raw = BitmapFactory.decodeByteArray(data, 0, data.size, BitmapFactory.Options().apply { inSampleSize = sample })
        ?: throw IllegalArgumentException("解不出这张图片")
    var bmp = raw
    if (maxLong > 0) {
        val (w, h) = fitLongSide(raw.width, raw.height, maxLong)
        if (w != raw.width) bmp = Bitmap.createScaledBitmap(raw, w, h, true)
    }
    if (o != 1) bmp = Bitmap.createBitmap(bmp, 0, 0, bmp.width, bmp.height, orientationMatrix(o), true)
    return bmp
}

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
fun processImage(context: Context, uri: Uri, original: Boolean): Processed {
    val data = readUri(context, uri, if (original) MAX_ORIGINAL_BYTES * 3 else 80L * 1024 * 1024)
    if (original) {
        stripOriginal(data)?.let { clean ->
            if (clean.size > MAX_ORIGINAL_BYTES) throw ImageTooLarge("原图超过 20 MB")
            val b = BitmapFactory.Options().apply { inJustDecodeBounds = true }
            BitmapFactory.decodeByteArray(clean, 0, clean.size, b)
            return Processed(clean, detectFormat(clean)!!, b.outWidth, b.outHeight)
        }
        val turned = detectFormat(data) != null
        val bmp = decodeUpright(data, 0)
        val png = bmp.hasAlpha()
        val out = encode(bmp, png, if (turned) 100 else 95)
        if (out.size > MAX_ORIGINAL_BYTES) throw ImageTooLarge("原图超过 20 MB")
        return Processed(
            out, if (png) ImageFormat.IMAGE_FORMAT_PNG else ImageFormat.IMAGE_FORMAT_JPEG, bmp.width, bmp.height,
            if (turned) "按拍摄方向转正后以最高画质重新编码" else "转成了 ${if (png) "PNG" else "JPEG"}，保留原始尺寸",
        )
    }
    var bmp = decodeUpright(data, COMPRESSED_LONG_SIDE)
    val png = bmp.hasAlpha()
    var q = 85
    var out = encode(bmp, png, q)
    while (out.size > COMPRESSED_TARGET && (q > 60 || maxOf(bmp.width, bmp.height) > 800)) {
        if (!png && q > 60) q -= 10
        else bmp = Bitmap.createScaledBitmap(bmp, (bmp.width * 0.8).toInt().coerceAtLeast(1), (bmp.height * 0.8).toInt().coerceAtLeast(1), true)
        out = encode(bmp, png, q)
    }
    return Processed(out, if (png) ImageFormat.IMAGE_FORMAT_PNG else ImageFormat.IMAGE_FORMAT_JPEG, bmp.width, bmp.height)
}
