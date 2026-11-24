// Images sent from the phone (architecture §17): container format sniffing,
// orientation inspection, and lossless metadata stripping delegate to
// clientcore/domain/imageparse via Corebind using header/seek windows and
// local range splicing (no full-file copy across JNI). Decoding and
// re-encoding pixels remain in :app.
package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.proto.ImageProblemKind
import io.github.lewin671.remoteagent.corebind.Corebind
import io.github.lewin671.remoteagent.proto.ImageFormat
import java.nio.ByteBuffer
import java.nio.ByteOrder

private const val IMG_FMT_UNKNOWN = 0
private const val IMG_FMT_JPEG = 1
private const val IMG_FMT_PNG = 2
private const val IMG_FMT_WEBP = 3
private const val IMG_FMT_GIF = 4

private const val IMG_MODE_INSPECT = 1
private const val IMG_MODE_STRIP = 2

private const val IMG_STATUS_DONE = 0
private const val IMG_STATUS_NEED_READ = 1
private const val IMG_STATUS_NEED_SCAN_JPEG_ENTROPY = 2
private const val IMG_STATUS_NEED_SKIP_GIF_SUB_BLOCKS = 3
private const val IMG_STATUS_ERROR = 4

private const val IMG_SNIFF_HEADER_BYTES = 256
private const val IMG_INITIAL_HEADER_BYTES = 64 * 1024

private fun formatFromCore(code: Int): ImageFormat? = when (code) {
    IMG_FMT_JPEG -> ImageFormat.IMAGE_FORMAT_JPEG
    IMG_FMT_PNG -> ImageFormat.IMAGE_FORMAT_PNG
    IMG_FMT_WEBP -> ImageFormat.IMAGE_FORMAT_WEBP
    IMG_FMT_GIF -> ImageFormat.IMAGE_FORMAT_GIF
    else -> null
}

/** The format of encoded image bytes, by their signature; null for anything else (HEIC and the like). */
fun detectFormat(b: ByteArray): ImageFormat? {
    val head = if (b.size > IMG_SNIFF_HEADER_BYTES) b.copyOfRange(0, IMG_SNIFF_HEADER_BYTES) else b
    return formatFromCore(Corebind.sniffImageFormat(head))
}

/** The orientation (1 to 8) a TIFF/Exif block records; 1 when it says none or cannot be read. */
fun tiffOrientation(t: ByteArray, off: Int = 0, end: Int = t.size): Int {
    if (off < 0 || end < off || end > t.size || end - off < 8) return 1
    val slice = if (off == 0 && end == t.size) t else t.copyOfRange(off, end)
    return Corebind.tiffOrientation(slice)
}

/** In-place 8-byte SWAR scan for the first real JPEG marker after entropy-coded data; -1 on EOF. */
private fun scanJpegEntropy(b: ByteArray, pos: Int): Int {
    val len = b.size
    val bb = ByteBuffer.wrap(b).order(ByteOrder.LITTLE_ENDIAN)
    var i = pos
    while (i + 1 < len) {
        while (i + 8 <= len) {
            val w = bb.getLong(i).inv()
            if (((w - 0x0101010101010101L) and w.inv() and -0x7f7f7f7f7f7f7f80L) != 0L) break
            i += 8
        }
        while (i + 1 < len && b[i] != 0xFF.toByte()) i++
        if (i + 1 >= len) return -1
        var k = i
        while (k + 1 < len && b[k + 1] == 0xFF.toByte()) k++
        if (k + 1 >= len) return -1
        val next = b[k + 1].toInt() and 0xff
        if (next == 0x00 || next in 0xD0..0xD7) {
            i = k + 2
            continue
        }
        return i
    }
    return -1
}

/** In-place skip over a GIF sub-block chain; returns offset after 0x00 terminator or -1 on EOF. */
private fun skipGifSubBlocks(b: ByteArray, pos: Int): Int {
    var p = pos
    val len = b.size
    while (true) {
        if (p >= len) return -1
        val n = b[p].toInt() and 0xff
        p += 1 + n
        if (p > len) return -1
        if (n == 0) return p
    }
}

private fun runImagePlan(
    b: ByteArray,
    mode: Int,
    expectedFormat: Int,
    onError: (String) -> Nothing,
): ByteArray {
    val plan = Corebind.newImagePlan(mode, expectedFormat, b.size.toLong())
    var offset = 0L
    var slice = if (b.size > IMG_INITIAL_HEADER_BYTES) b.copyOfRange(0, IMG_INITIAL_HEADER_BYTES) else b
    while (true) {
        val res = plan.step(offset, slice)
        val status = res[0].toInt() and 0xff
        if (status == IMG_STATUS_DONE) return res
        if (status == IMG_STATUS_ERROR) {
            val detail = String(res, 2, res.size - 2, Charsets.UTF_8)
            onError(detail)
        }
        val bb = ByteBuffer.wrap(res).order(ByteOrder.LITTLE_ENDIAN)
        when (status) {
            IMG_STATUS_NEED_READ -> {
                offset = bb.getLong(1)
                val need = bb.getInt(9)
                val start = offset.toInt().coerceIn(0, b.size)
                val end = (offset + need.toLong()).coerceAtMost(b.size.toLong()).toInt()
                slice = b.copyOfRange(start, end)
            }
            IMG_STATUS_NEED_SCAN_JPEG_ENTROPY -> {
                val from = bb.getLong(1).toInt()
                val found = scanJpegEntropy(b, from)
                offset = found.toLong()
                slice = if (found >= 0) {
                    b.copyOfRange(found, minOf(found + IMG_INITIAL_HEADER_BYTES, b.size))
                } else {
                    ByteArray(0)
                }
            }
            IMG_STATUS_NEED_SKIP_GIF_SUB_BLOCKS -> {
                val from = bb.getLong(1).toInt()
                val end = skipGifSubBlocks(b, from)
                offset = end.toLong()
                slice = if (end >= 0) {
                    b.copyOfRange(end, minOf(end + IMG_INITIAL_HEADER_BYTES, b.size))
                } else {
                    ByteArray(0)
                }
            }
            else -> onError("unexpected_plan_status")
        }
    }
}

private fun spliceImagePlan(src: ByteArray, done: ByteArray): ByteArray {
    val bb = ByteBuffer.wrap(done).order(ByteOrder.LITTLE_ENDIAN)
    val count = bb.getInt(12)
    var at = 16
    var total = 0
    for (i in 0 until count) {
        val kind = done[at++].toInt() and 0xff
        if (kind == 0) {
            val s = bb.getLong(at).toInt()
            val e = bb.getLong(at + 8).toInt()
            total += e - s
            at += 16
        } else {
            val n = bb.getInt(at)
            total += n
            at += 4 + n
        }
    }
    val out = ByteArray(total)
    at = 16
    var dst = 0
    for (i in 0 until count) {
        val kind = done[at++].toInt() and 0xff
        if (kind == 0) {
            val s = bb.getLong(at).toInt()
            val e = bb.getLong(at + 8).toInt()
            val n = e - s
            System.arraycopy(src, s, out, dst, n)
            dst += n
            at += 16
        } else {
            val n = bb.getInt(at)
            System.arraycopy(done, at + 4, out, dst, n)
            dst += n
            at += 4 + n
        }
    }
    return out
}

private fun inspectOrientation(b: ByteArray, expectedFormat: Int): Int {
    return try {
        val done = runImagePlan(b, IMG_MODE_INSPECT, expectedFormat) { throw IllegalStateException(it) }
        val o = done[2].toInt() and 0xff
        if (o in 1..8) o else 1
    } catch (_: Throwable) {
        1
    }
}

/** A JPEG's Exif orientation, 1 when none. */
fun jpegOrientation(b: ByteArray): Int = inspectOrientation(b, IMG_FMT_JPEG)

/** A PNG's orientation from its eXIf chunk, 1 when none. */
fun pngOrientation(b: ByteArray): Int = inspectOrientation(b, IMG_FMT_PNG)

/** A WebP's orientation from its EXIF chunk, 1 when none. */
fun webpOrientation(b: ByteArray): Int = inspectOrientation(b, IMG_FMT_WEBP)

/** An image's orientation, by format; 1 for GIF and anything without one. */
fun orientationOf(b: ByteArray): Int = inspectOrientation(b, IMG_FMT_UNKNOWN)

/** A JPEG without its metadata (EXIF, XMP, IPTC, comments, trailing data); JFIF/ICC/Adobe stay. */
fun stripJpeg(b: ByteArray): ByteArray {
    val done = runImagePlan(b, IMG_MODE_STRIP, IMG_FMT_JPEG) {
        throw MessageException(UiMessages.imageAndroidUnreadableJpeg().messageText(), imageKind = ImageProblemKind.IMAGE_PROBLEM_KIND_DECODE)
    }
    return spliceImagePlan(b, done)
}

/** A PNG without text, time and Exif chunks; the image data is not touched. */
fun stripPng(b: ByteArray): ByteArray {
    val done = runImagePlan(b, IMG_MODE_STRIP, IMG_FMT_PNG) {
        throw MessageException(UiMessages.imageAndroidUnreadablePng().messageText(), imageKind = ImageProblemKind.IMAGE_PROBLEM_KIND_DECODE)
    }
    return spliceImagePlan(b, done)
}

/** A WebP without its EXIF and XMP chunks, with the extended header's flags and the file size to match. */
fun stripWebp(b: ByteArray): ByteArray {
    val done = runImagePlan(b, IMG_MODE_STRIP, IMG_FMT_WEBP) {
        throw MessageException(UiMessages.imageAndroidUnreadableWebp().messageText(), imageKind = ImageProblemKind.IMAGE_PROBLEM_KIND_DECODE)
    }
    return spliceImagePlan(b, done)
}

/** A GIF without comments and non-loop application blocks; frames are not touched. */
fun stripGif(b: ByteArray): ByteArray {
    val done = runImagePlan(b, IMG_MODE_STRIP, IMG_FMT_GIF) { detail ->
        val msg = when {
            detail.contains("unknown block") -> UiMessages.imageAndroidGifContainsAnUnrecognizedBlock()
            detail.contains("not a GIF") || detail.contains("empty") || detail.contains("does not match") -> UiMessages.imageAndroidUnreadableGif()
            else -> UiMessages.imageAndroidGifIsTruncated()
        }
        throw MessageException(msg.messageText(), imageKind = ImageProblemKind.IMAGE_PROBLEM_KIND_DECODE)
    }
    return spliceImagePlan(b, done)
}

/**
 * An original with its metadata taken out, or null when that needs new
 * pixels: it is turned by an orientation tag (lossless rotation is not
 * done here; the caller re-encodes at the highest quality instead) or its
 * format is not one the CLIs read (HEIC).
 */
fun stripOriginal(b: ByteArray): ByteArray? {
    val f = detectFormat(b) ?: return null
    if (orientationOf(b) != 1) return null
    return when (f) {
        ImageFormat.IMAGE_FORMAT_JPEG -> stripJpeg(b)
        ImageFormat.IMAGE_FORMAT_PNG -> stripPng(b)
        ImageFormat.IMAGE_FORMAT_WEBP -> stripWebp(b)
        ImageFormat.IMAGE_FORMAT_GIF -> stripGif(b)
        else -> null
    }
}

/** The size of a picture shrunk so its long side is at most [max]; never enlarged. */
fun fitLongSide(w: Int, h: Int, max: Int): Pair<Int, Int> {
    val long = maxOf(w, h)
    if (long <= max || long == 0) return w to h
    val scale = max.toDouble() / long
    return maxOf(1, Math.round(w * scale).toInt()) to maxOf(1, Math.round(h * scale).toInt())
}

// --- image decoding ---

/** Most pixels an image is decoded at on the phone, whatever its size: 40 MP, ARGB, is 160 MB. */
const val MAX_DECODE_PIXELS = 40_000_000L

/**
 * The power-of-two sample size to decode a [w]×[h] image at: small enough
 * that the decoded picture fits [pixelBudget] (so memory has a bound,
 * rotation's second copy included in the caller's budget), and no smaller
 * than needed for a long side of [minLong] (0: as large as the budget
 * allows).
 */
fun decodeSample(w: Int, h: Int, minLong: Int, pixelBudget: Long): Int {
    if (w <= 0 || h <= 0) throw MessageArgumentException(UiMessages.imageAndroidImageHasNoDimensions().messageText(), imageKind = ImageProblemKind.IMAGE_PROBLEM_KIND_DIMENSIONS)
    var s = 1
    if (minLong > 0) while (maxOf(w, h) / (s * 2) >= minLong) s *= 2
    while ((w.toLong() / s) * (h.toLong() / s) > pixelBudget) s *= 2
    return s
}
