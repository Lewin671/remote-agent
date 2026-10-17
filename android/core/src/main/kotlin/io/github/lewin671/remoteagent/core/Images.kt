// Images sent from the phone (architecture §17): reading formats and
// orientation, taking metadata out of files sent as originals, the limits
// of one message, and uploading in chunks. Decoding and re-encoding pixels
// needs the platform and is in :app.
package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.ImageFormat
import java.io.ByteArrayOutputStream

/** At most this many images go with one message, whatever the daemon says or the picker allowed. */
const val MAX_IMAGES = 6

/** An original may be this large at most (architecture §17). */
const val MAX_ORIGINAL_BYTES = 20L * 1024 * 1024

/** The long side of a compressed image. */
const val COMPRESSED_LONG_SIDE = 2048

/** The format of encoded image bytes, by their signature; null for anything else (HEIC and the like). */
fun detectFormat(b: ByteArray): ImageFormat? = when {
    b.size >= 8 && b[0] == 0x89.toByte() && b[1] == 'P'.code.toByte() && b[2] == 'N'.code.toByte() && b[3] == 'G'.code.toByte() -> ImageFormat.IMAGE_FORMAT_PNG
    b.size >= 3 && b[0] == 0xFF.toByte() && b[1] == 0xD8.toByte() && b[2] == 0xFF.toByte() -> ImageFormat.IMAGE_FORMAT_JPEG
    b.size >= 12 && String(b, 0, 4, Charsets.ISO_8859_1) == "RIFF" && String(b, 8, 4, Charsets.ISO_8859_1) == "WEBP" -> ImageFormat.IMAGE_FORMAT_WEBP
    b.size >= 6 && String(b, 0, 6, Charsets.ISO_8859_1).let { it == "GIF87a" || it == "GIF89a" } -> ImageFormat.IMAGE_FORMAT_GIF
    else -> null
}

/**
 * Which images of a pick are taken: the first ones up to [max] in all, in
 * the order picked, and how many were left out. The system picker may not
 * enforce a count (its fallback does not), so the app does.
 */
fun <T> takePicked(have: Int, picked: List<T>, max: Int = MAX_IMAGES): Pair<List<T>, Int> {
    val room = (max - have).coerceAtLeast(0)
    return picked.take(room) to (picked.size - room).coerceAtLeast(0)
}

private fun u16(b: ByteArray, i: Int, le: Boolean): Int =
    if (le) (b[i].toInt() and 0xff) or ((b[i + 1].toInt() and 0xff) shl 8) else ((b[i].toInt() and 0xff) shl 8) or (b[i + 1].toInt() and 0xff)

private fun u32(b: ByteArray, i: Int, le: Boolean): Long =
    if (le) (u16(b, i, true).toLong()) or (u16(b, i + 2, true).toLong() shl 16) else (u16(b, i, false).toLong() shl 16) or u16(b, i + 2, false).toLong()

/** The orientation (1 to 8) a TIFF/Exif block records; 1 when it says none or cannot be read. */
fun tiffOrientation(t: ByteArray, off: Int = 0, end: Int = t.size): Int {
    try {
        if (end - off < 8) return 1
        val le = when (String(t, off, 2, Charsets.ISO_8859_1)) {
            "II" -> true
            "MM" -> false
            else -> return 1
        }
        if (u16(t, off + 2, le) != 42) return 1
        val ifd = off + u32(t, off + 4, le).toInt()
        if (ifd < off || ifd + 2 > end) return 1
        val n = u16(t, ifd, le)
        for (i in 0 until n) {
            val e = ifd + 2 + i * 12
            if (e + 12 > end) return 1
            if (u16(t, e, le) == 0x0112) {
                val v = u16(t, e + 8, le)
                return if (v in 1..8) v else 1
            }
        }
    } catch (e: IndexOutOfBoundsException) {
        return 1
    }
    return 1
}

/** A JPEG's segments before the image data: marker, and payload offset and length. */
private class Segment(val marker: Int, val start: Int, val end: Int)

private fun jpegSegments(b: ByteArray): Pair<List<Segment>, Int>? {
    if (detectFormat(b) != ImageFormat.IMAGE_FORMAT_JPEG) return null
    val out = mutableListOf<Segment>()
    var i = 2
    while (i + 4 <= b.size) {
        if (b[i] != 0xFF.toByte()) return null
        val marker = b[i + 1].toInt() and 0xff
        if (marker == 0xFF) { i++; continue }
        // Start of scan: everything from here on is image data.
        if (marker == 0xDA) return out to i
        if (marker == 0xD8 || marker in 0xD0..0xD7 || marker == 0x01) { i += 2; continue }
        val len = u16(b, i + 2, false)
        if (len < 2 || i + 2 + len > b.size) return null
        out += Segment(marker, i, i + 2 + len)
        i += 2 + len
    }
    return null
}

/** A JPEG's Exif orientation, 1 when none. */
fun jpegOrientation(b: ByteArray): Int {
    val (segs, _) = jpegSegments(b) ?: return 1
    for (s in segs) {
        if (s.marker == 0xE1 && s.end - s.start > 10 && String(b, s.start + 4, 6, Charsets.ISO_8859_1) == "Exif\u0000\u0000") {
            return tiffOrientation(b, s.start + 10, s.end)
        }
    }
    return 1
}

/** Whether an application or comment segment is kept: JFIF, the color profile and Adobe's color transform. */
private fun keepJpegSegment(b: ByteArray, marker: Int, start: Int, end: Int): Boolean = when (marker) {
    0xE0 -> String(b, start + 4, minOf(5, end - start - 4), Charsets.ISO_8859_1).startsWith("JFIF")
    0xE2 -> String(b, start + 4, minOf(12, end - start - 4), Charsets.ISO_8859_1).startsWith("ICC_PROFILE")
    0xEE -> true
    0xFE -> false
    in 0xE1..0xEF -> false
    else -> true
}

/**
 * A JPEG without its metadata: Exif and XMP (APP1), IPTC (APP13), comments
 * and the other application segments go wherever they are, before the
 * first scan, between the scans of a progressive JPEG or right before the
 * end; JFIF (APP0), the color profile (APP2 ICC_PROFILE) and Adobe's color
 * transform (APP14) stay, since the picture depends on them. Only the
 * entropy-coded data of each scan is copied without being parsed, up to the
 * next marker; anything after the end marker goes. Take the orientation
 * into account first: without Exif the picture shows as stored.
 */
fun stripJpeg(b: ByteArray): ByteArray {
    fun bad(): Nothing = throw IllegalArgumentException("不是能读的 JPEG")
    if (detectFormat(b) != ImageFormat.IMAGE_FORMAT_JPEG) bad()
    val out = ByteArrayOutputStream(b.size)
    out.write(b, 0, 2)
    var i = 2
    while (true) {
        if (i + 2 > b.size || b[i] != 0xFF.toByte()) bad()
        val marker = b[i + 1].toInt() and 0xff
        when {
            // Fill bytes before a marker.
            marker == 0xFF -> i++
            marker == 0xD9 -> {
                out.write(b, i, 2)
                return out.toByteArray()
            }
            marker in 0xD0..0xD7 || marker == 0x01 -> {
                out.write(b, i, 2)
                i += 2
            }
            else -> {
                if (i + 4 > b.size) bad()
                val len = u16(b, i + 2, false)
                if (len < 2 || i + 2 + len > b.size) bad()
                val end = i + 2 + len
                if (keepJpegSegment(b, marker, i, end)) out.write(b, i, end - i)
                i = end
                if (marker == 0xDA) {
                    // The scan's coded data runs to the next marker other than
                    // a stuffed zero or a restart marker. Fill bytes (more
                    // 0xFF) may come before a restart marker too.
                    var j = i
                    while (j + 1 < b.size) {
                        if (b[j] == 0xFF.toByte()) {
                            var k = j
                            while (k + 1 < b.size && b[k + 1] == 0xFF.toByte()) k++
                            if (k + 1 >= b.size) bad()
                            val n = b[k + 1].toInt() and 0xff
                            if (n != 0x00 && n !in 0xD0..0xD7) break
                            j = k + 2
                        } else j++
                    }
                    if (j + 1 >= b.size) bad()
                    out.write(b, i, j - i)
                    i = j
                }
            }
        }
    }
}

private fun pngChunks(b: ByteArray): List<Triple<String, Int, Int>>? {
    if (detectFormat(b) != ImageFormat.IMAGE_FORMAT_PNG) return null
    val out = mutableListOf<Triple<String, Int, Int>>()
    var i = 8
    while (i + 12 <= b.size) {
        val len = u32(b, i, false)
        if (len > b.size) return null
        val type = String(b, i + 4, 4, Charsets.ISO_8859_1)
        val end = i + 12 + len.toInt()
        if (end > b.size) return null
        out += Triple(type, i, end)
        i = end
        if (type == "IEND") break
    }
    return out
}

/** A PNG's orientation from its eXIf chunk, 1 when none. */
fun pngOrientation(b: ByteArray): Int {
    val chunks = pngChunks(b) ?: return 1
    val c = chunks.firstOrNull { it.first == "eXIf" } ?: return 1
    return tiffOrientation(b, c.second + 8, c.third - 4)
}

/** A PNG without text, time and Exif chunks; the image data is not touched. */
fun stripPng(b: ByteArray): ByteArray {
    val chunks = pngChunks(b) ?: throw IllegalArgumentException("不是能读的 PNG")
    val drop = setOf("tEXt", "zTXt", "iTXt", "eXIf", "tIME")
    val out = ByteArrayOutputStream(b.size)
    out.write(b, 0, 8)
    for ((type, start, end) in chunks) if (type !in drop) out.write(b, start, end - start)
    return out.toByteArray()
}

private fun webpChunks(b: ByteArray): List<Triple<String, Int, Int>>? {
    if (detectFormat(b) != ImageFormat.IMAGE_FORMAT_WEBP) return null
    val out = mutableListOf<Triple<String, Int, Int>>()
    var i = 12
    while (i + 8 <= b.size) {
        val type = String(b, i, 4, Charsets.ISO_8859_1)
        val len = u32(b, i + 4, true)
        val end = i + 8 + len.toInt() + (len.toInt() and 1)
        if (len > b.size || end > b.size + 1) return null
        out += Triple(type, i, minOf(end, b.size))
        i = end
    }
    return out
}

/** A WebP's orientation from its EXIF chunk, 1 when none. */
fun webpOrientation(b: ByteArray): Int {
    val c = (webpChunks(b) ?: return 1).firstOrNull { it.first == "EXIF" } ?: return 1
    val start = c.second + 8
    // Some writers put the Exif header in front of the TIFF block.
    val tiff = if (c.third - start > 6 && String(b, start, 6, Charsets.ISO_8859_1) == "Exif\u0000\u0000") start + 6 else start
    return tiffOrientation(b, tiff, c.third)
}

/** A WebP without its EXIF and XMP chunks, with the extended header's flags and the file size to match. */
fun stripWebp(b: ByteArray): ByteArray {
    val chunks = webpChunks(b) ?: throw IllegalArgumentException("不是能读的 WebP")
    val out = ByteArrayOutputStream(b.size)
    out.write(b, 0, 12)
    for ((type, start, end) in chunks) {
        if (type == "EXIF" || type == "XMP ") continue
        if (type == "VP8X" && end - start >= 9) {
            val chunk = b.copyOfRange(start, end)
            // Flags: bit 3 Exif, bit 2 XMP.
            chunk[8] = (chunk[8].toInt() and 0x08.inv() and 0x04.inv()).toByte()
            out.write(chunk)
        } else {
            out.write(b, start, end - start)
        }
    }
    val r = out.toByteArray()
    val size = r.size - 8
    r[4] = size.toByte(); r[5] = (size ushr 8).toByte(); r[6] = (size ushr 16).toByte(); r[7] = (size ushr 24).toByte()
    return r
}

/**
 * A GIF without comments and without application blocks other than the
 * animation loop (NETSCAPE2.0), where XMP and the like live; frames are not
 * touched.
 */
fun stripGif(b: ByteArray): ByteArray {
    if (detectFormat(b) != ImageFormat.IMAGE_FORMAT_GIF || b.size < 13) throw IllegalArgumentException("不是能读的 GIF")
    val out = ByteArrayOutputStream(b.size)
    var i = 13
    val flags = b[10].toInt() and 0xff
    if (flags and 0x80 != 0) i += 3 * (1 shl ((flags and 7) + 1))
    out.write(b, 0, i)
    fun subBlocks(from: Int): Int {
        var j = from
        while (true) {
            if (j >= b.size) throw IllegalArgumentException("GIF 被截断")
            val n = b[j].toInt() and 0xff
            j += 1 + n
            if (n == 0) return j
        }
    }
    while (i < b.size) {
        when (b[i].toInt() and 0xff) {
            0x3B -> {
                out.write(0x3B)
                return out.toByteArray()
            }
            0x2C -> {
                if (i + 10 > b.size) throw IllegalArgumentException("GIF 被截断")
                val f = b[i + 9].toInt() and 0xff
                var j = i + 10
                if (f and 0x80 != 0) j += 3 * (1 shl ((f and 7) + 1))
                j = subBlocks(j + 1)
                out.write(b, i, j - i)
                i = j
            }
            0x21 -> {
                val label = b[i + 1].toInt() and 0xff
                val j = subBlocks(i + 2)
                val app = label == 0xFF && i + 14 <= b.size && String(b, i + 3, 11, Charsets.ISO_8859_1).let { it == "NETSCAPE2.0" || it == "ANIMEXTS1.0" }
                val keep = when (label) {
                    0xFE -> false
                    0xFF -> app
                    else -> true
                }
                if (keep) out.write(b, i, j - i)
                i = j
            }
            else -> throw IllegalArgumentException("GIF 里有认不出的块")
        }
    }
    throw IllegalArgumentException("GIF 被截断")
}

/** An image's orientation, by format; 1 for GIF and anything without one. */
fun orientationOf(b: ByteArray): Int = when (detectFormat(b)) {
    ImageFormat.IMAGE_FORMAT_JPEG -> jpegOrientation(b)
    ImageFormat.IMAGE_FORMAT_PNG -> pngOrientation(b)
    ImageFormat.IMAGE_FORMAT_WEBP -> webpOrientation(b)
    else -> 1
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
fun fitLongSide(w: Int, h: Int, max: Int = COMPRESSED_LONG_SIDE): Pair<Int, Int> {
    val long = maxOf(w, h)
    if (long <= max || long == 0) return w to h
    val scale = max.toDouble() / long
    return maxOf(1, Math.round(w * scale).toInt()) to maxOf(1, Math.round(h * scale).toInt())
}

/** Orientations 5 to 8 swap width and height. */
fun swapsSides(orientation: Int): Boolean = orientation in 5..8

// --- uploading ---

/** Chunks start at this size when the daemon names none: an encrypted command stays well under 1 MiB. */
const val DEFAULT_CHUNK_BYTES = 768 * 1024

/** The daemon's upload commands for one machine; each call is one machine command. */
interface UploadChannel {
    suspend fun begin(transferId: String, format: ImageFormat, total: Long, sha256: ByteArray): io.github.lewin671.remoteagent.proto.ImageUploadStatus
    suspend fun put(transferId: String, offset: Long, data: ByteArray): io.github.lewin671.remoteagent.proto.ImageUploadStatus
    suspend fun status(transferId: String): io.github.lewin671.remoteagent.proto.ImageUploadStatus
    suspend fun complete(transferId: String): io.github.lewin671.remoteagent.proto.ImageUploadStatus
}

/** A completed upload is usable only until the daemon-provided deadline. */
data class UploadedImage(val imageId: String, val expiresMs: Long)

/**
 * Sends one image to a machine in chunks and returns its ID and expiry
 * for sending with a message. Every step can be repeated: beginning again returns the
 * transfer's state, a chunk is resent from where the daemon has durably
 * stored bytes, and completing again returns the same ID. When a step's
 * outcome is unknown, the daemon's status says where to go on; a definite
 * refusal ends the upload with an error, never a partial success.
 */
suspend fun uploadImage(
    ch: UploadChannel,
    transferId: String,
    data: ByteArray,
    maxChunk: Int = DEFAULT_CHUNK_BYTES,
    retries: Int = 3,
    onProgress: (Long, Long) -> Unit = { _, _ -> },
): UploadedImage {
    val format = detectFormat(data) ?: throw IllegalArgumentException("不支持的图片格式")
    val total = data.size.toLong()
    var tries = 0
    suspend fun <T> step(block: suspend () -> T, resync: suspend () -> Unit): T {
        while (true) {
            try {
                return block()
            } catch (e: CommandException) {
                if (!e.unknown && e.code != "network" || tries >= retries) throw e
                tries++
                resync()
            }
        }
    }
    var st = step({ ch.begin(transferId, format, total, sha256(data)) }) {}
    val chunk = listOf(maxChunk, st.maxChunkBytes.takeIf { it > 0 } ?: Int.MAX_VALUE).min()
    while (st.state == io.github.lewin671.remoteagent.proto.ImageUploadState.IMAGE_UPLOAD_STATE_RECEIVING && st.persistedBytes < total) {
        val off = st.persistedBytes
        onProgress(off, total)
        val end = minOf(total, off + chunk).toInt()
        st = step({ ch.put(transferId, off, data.copyOfRange(off.toInt(), end)) }) { st = ch.status(transferId) }
    }
    onProgress(total, total)
    if (st.state == io.github.lewin671.remoteagent.proto.ImageUploadState.IMAGE_UPLOAD_STATE_RECEIVING) {
        st = step({ ch.complete(transferId) }) { st = ch.status(transferId) }
    }
    if (st.imageId.isEmpty()) throw CommandException("internal", "电脑没有给出图片 ID")
    return UploadedImage(st.imageId, st.expiresMs)
}

// --- images in a session ---

/** One image as it appears in a session: the ref, the event it is in, and what shows it. */
data class SessionImage(val ref: io.github.lewin671.remoteagent.proto.ImageRef, val seq: Long, val kind: io.github.lewin671.remoteagent.proto.Event.KindCase, val toolTitle: String = "")

/** Every image of a session in the order they appeared, for the viewer to page through. */
fun sessionImages(events: List<io.github.lewin671.remoteagent.proto.Event>): List<SessionImage> = buildList {
    for (e in events) when (e.kindCase) {
        io.github.lewin671.remoteagent.proto.Event.KindCase.USER_MESSAGE -> e.userMessage.imagesList.forEach { add(SessionImage(it, e.seq, e.kindCase)) }
        io.github.lewin671.remoteagent.proto.Event.KindCase.TOOL_CALL -> e.toolCall.imagesList.forEach { add(SessionImage(it, e.seq, e.kindCase, e.toolCall.title)) }
        io.github.lewin671.remoteagent.proto.Event.KindCase.IMAGE_GENERATED -> e.imageGenerated.imagesList.forEach { add(SessionImage(it, e.seq, e.kindCase)) }
        else -> {}
    }
}

private val placeholder = Regex("\\[Image #(\\d+)]")

/**
 * A user message's text as shown next to its images. When the message is
 * its images followed by text, the leading "[Image #N]" placeholders are
 * dropped: the numbered previews stand for them. When the text refers to
 * images further in, every placeholder reads "图 N" so the references keep
 * their meaning. Placeholders the message has no image for, e.g. typed as
 * text, stay as written.
 */
fun imageText(text: String, refs: List<io.github.lewin671.remoteagent.proto.ImageRef>): String {
    val known = refs.map { it.cliIndex }.filter { it > 0 }.toSet()
    if (known.isEmpty()) return text
    var t = text
    while (true) {
        val m = Regex("^\\s*\\[Image #(\\d+)]").find(t) ?: break
        if (m.groupValues[1].toIntOrNull() !in known) break
        t = t.substring(m.range.last + 1)
    }
    val laterRefs = placeholder.findAll(t).any { it.groupValues[1].toIntOrNull() in known }
    val base = if (laterRefs) text else t.trimStart()
    return placeholder.replace(base) { m -> if (m.groupValues[1].toIntOrNull() in known) "图 ${m.groupValues[1]}" else m.value }
}

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
    require(w > 0 && h > 0) { "图片没有尺寸" }
    var s = 1
    if (minLong > 0) while (maxOf(w, h) / (s * 2) >= minLong) s *= 2
    while ((w.toLong() / s) * (h.toLong() / s) > pixelBudget) s *= 2
    return s
}
