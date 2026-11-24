package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.assertMessageEquals as assertEquals

import io.github.lewin671.remoteagent.proto.ImageFormat
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assert.assertSame
import org.junit.Test
import java.awt.image.BufferedImage
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.util.zip.CRC32
import javax.imageio.ImageIO

class ImagesTest {
    private fun picture(format: String, w: Int = 40, h: Int = 20): ByteArray {
        val img = BufferedImage(w, h, BufferedImage.TYPE_INT_RGB)
        for (x in 0 until w) for (y in 0 until h) img.setRGB(x, y, if (x < w / 2) 0xff0000 else 0x0000ff)
        return ByteArrayOutputStream().also { ImageIO.write(img, format, it) }.toByteArray()
    }

    /** A big-endian TIFF block with the orientation and a GPS-looking tag, as a camera writes. */
    private fun tiff(orientation: Int): ByteArray {
        val o = ByteArrayOutputStream()
        o.write(byteArrayOf('M'.code.toByte(), 'M'.code.toByte(), 0, 42, 0, 0, 0, 8))
        o.write(byteArrayOf(0, 2))
        o.write(byteArrayOf(0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, orientation.toByte(), 0, 0))
        // GPSInfo IFD pointer, pointing nowhere useful: only its presence matters here.
        o.write(byteArrayOf(0x88.toByte(), 0x25, 0, 4, 0, 0, 0, 1, 0, 0, 0, 0))
        o.write(byteArrayOf(0, 0, 0, 0))
        o.write("LOCATION 31.2304N 121.4737E".toByteArray())
        return o.toByteArray()
    }

    private fun withSegment(jpeg: ByteArray, marker: Int, payload: ByteArray): ByteArray {
        val o = ByteArrayOutputStream()
        o.write(jpeg, 0, 2)
        val len = payload.size + 2
        o.write(byteArrayOf(0xFF.toByte(), marker.toByte(), (len shr 8).toByte(), len.toByte()))
        o.write(payload)
        o.write(jpeg, 2, jpeg.size - 2)
        return o.toByteArray()
    }

    private fun exifJpeg(orientation: Int): ByteArray {
        val base = picture("jpg")
        val withExif = withSegment(base, 0xE1, "Exif\u0000\u0000".toByteArray(Charsets.ISO_8859_1) + tiff(orientation))
        return withSegment(withExif, 0xFE, "shot on a phone".toByteArray())
    }

    private fun contains(hay: ByteArray, needle: String) = String(hay, Charsets.ISO_8859_1).contains(needle)

    @Test
    fun readsTheOrientationOfAJpeg() {
        assertEquals(6, jpegOrientation(exifJpeg(6)))
        assertEquals(1, jpegOrientation(picture("jpg")))
        assertEquals(6, orientationOf(exifJpeg(6)))
    }

    @Test
    fun aTurnedPhotoIsNotSentAsIs() {
        // Stripping Exif from it would send it sideways: pixels must turn first.
        assertNull(stripOriginal(exifJpeg(6)))
    }

    @Test
    fun strippingAJpegKeepsItsPixelsAndDropsLocationAndComments() {
        val src = exifJpeg(1)
        assertTrue(contains(src, "LOCATION"))
        val out = stripOriginal(src)!!
        assertFalse(contains(out, "LOCATION"))
        assertFalse(contains(out, "shot on a phone"))
        assertFalse(contains(out, "Exif"))
        // The same image data, byte for byte, from the start of scan on.
        val sos = { b: ByteArray -> String(b, Charsets.ISO_8859_1).indexOf("ÿÚ") }
        assertArrayEquals(src.copyOfRange(sos(src), src.size), out.copyOfRange(sos(out), out.size))
        val img = ImageIO.read(ByteArrayInputStream(out))
        assertEquals(40, img.width)
        assertTrue(contains(out, "JFIF"))
    }

    private fun pngChunk(type: String, data: ByteArray): ByteArray {
        val o = ByteArrayOutputStream()
        o.write(byteArrayOf((data.size shr 24).toByte(), (data.size shr 16).toByte(), (data.size shr 8).toByte(), data.size.toByte()))
        val td = type.toByteArray(Charsets.ISO_8859_1) + data
        o.write(td)
        val crc = CRC32().also { it.update(td) }.value
        o.write(byteArrayOf((crc shr 24).toByte(), (crc shr 16).toByte(), (crc shr 8).toByte(), crc.toByte()))
        return o.toByteArray()
    }

    private fun pngWith(vararg chunks: ByteArray): ByteArray {
        val base = picture("png")
        // After the signature and IHDR (8 + 25 bytes).
        val o = ByteArrayOutputStream()
        o.write(base, 0, 33)
        for (c in chunks) o.write(c)
        o.write(base, 33, base.size - 33)
        return o.toByteArray()
    }

    @Test
    fun strippingAPngDropsTextAndExif() {
        val src = pngWith(pngChunk("tEXt", "Comment\u0000taken at home".toByteArray(Charsets.ISO_8859_1)), pngChunk("eXIf", tiff(1)))
        val out = stripOriginal(src)!!
        assertFalse(contains(out, "taken at home"))
        assertFalse(contains(out, "LOCATION"))
        assertEquals(40, ImageIO.read(ByteArrayInputStream(out)).width)
        assertEquals(6, pngOrientation(pngWith(pngChunk("eXIf", tiff(6)))))
        assertNull(stripOriginal(pngWith(pngChunk("eXIf", tiff(6)))))
    }

    private fun riffChunk(type: String, data: ByteArray): ByteArray {
        val o = ByteArrayOutputStream()
        o.write(type.toByteArray(Charsets.ISO_8859_1))
        o.write(byteArrayOf(data.size.toByte(), (data.size shr 8).toByte(), (data.size shr 16).toByte(), (data.size shr 24).toByte()))
        o.write(data)
        if (data.size % 2 == 1) o.write(0)
        return o.toByteArray()
    }

    private fun webp(vararg chunks: ByteArray): ByteArray {
        val body = ByteArrayOutputStream().also { o -> chunks.forEach { o.write(it) } }.toByteArray()
        val size = body.size + 4
        return "RIFF".toByteArray() + byteArrayOf(size.toByte(), (size shr 8).toByte(), (size shr 16).toByte(), (size shr 24).toByte()) + "WEBP".toByteArray() + body
    }

    @Test
    fun strippingAWebpFixesItsFlagsAndSize() {
        val vp8x = ByteArray(10).also { it[0] = 0x0C } // Exif and XMP flags
        val image = ByteArray(15) { 7 }
        val src = webp(riffChunk("VP8X", vp8x), riffChunk("VP8L", image), riffChunk("EXIF", tiff(1)), riffChunk("XMP ", "<x:xmpmeta/>".toByteArray()))
        assertEquals(ImageFormat.IMAGE_FORMAT_WEBP, detectFormat(src))
        val out = stripWebp(src)
        assertFalse(contains(out, "LOCATION"))
        assertFalse(contains(out, "xmpmeta"))
        assertEquals(out.size - 8, (out[4].toInt() and 0xff) or ((out[5].toInt() and 0xff) shl 8))
        assertEquals(0, out[20].toInt() and 0x0C)
        assertTrue(contains(out, "VP8L"))
        assertEquals(6, webpOrientation(webp(riffChunk("VP8X", vp8x), riffChunk("EXIF", tiff(6)))))
    }

    @Test
    fun strippingAGifDropsCommentsAndKeepsFrames() {
        val base = picture("gif")
        // Insert a comment block right after the header and palette.
        val flags = base[10].toInt() and 0xff
        val headerEnd = 13 + if (flags and 0x80 != 0) 3 * (1 shl ((flags and 7) + 1)) else 0
        val comment = byteArrayOf(0x21, 0xFE.toByte(), 9) + "home, 5pm".toByteArray() + byteArrayOf(0)
        val src = base.copyOfRange(0, headerEnd) + comment + base.copyOfRange(headerEnd, base.size)
        val out = stripOriginal(src)!!
        assertFalse(contains(out, "home, 5pm"))
        assertNotNull(ImageIO.read(ByteArrayInputStream(out)))
        assertEquals(base.size, out.size)
    }

    @Test
    fun formatsAreToldBySignatureOnly() {
        assertEquals(ImageFormat.IMAGE_FORMAT_JPEG, detectFormat(picture("jpg")))
        assertEquals(ImageFormat.IMAGE_FORMAT_PNG, detectFormat(picture("png")))
        assertEquals(ImageFormat.IMAGE_FORMAT_GIF, detectFormat(picture("gif")))
        // HEIC is not one the CLIs read: it gets new pixels.
        val heic = ByteArray(16).also { System.arraycopy("ftypheic".toByteArray(), 0, it, 4, 8) }
        assertNull(detectFormat(heic))
        assertNull(stripOriginal(heic))
    }

    @Test
    fun compressionShrinksTheLongSideOnly() {
        assertEquals(2048 to 1536, fitLongSide(4032, 3024, 2048))
        assertEquals(1536 to 2048, fitLongSide(3024, 4032, 2048))
        assertEquals(800 to 600, fitLongSide(800, 600, 2048))
    }
}

class JpegAfterScanTest {
    private fun jpeg(progressive: Boolean): ByteArray {
        val img = BufferedImage(64, 48, BufferedImage.TYPE_INT_RGB)
        for (x in 0 until 64) for (y in 0 until 48) img.setRGB(x, y, (x * 4 shl 16) or (y * 5 shl 8) or 90)
        val w = ImageIO.getImageWritersByFormatName("jpeg").next()
        val out = ByteArrayOutputStream()
        w.output = ImageIO.createImageOutputStream(out)
        val p = w.defaultWriteParam
        if (progressive) p.progressiveMode = javax.imageio.ImageWriteParam.MODE_DEFAULT
        w.write(null, javax.imageio.IIOImage(img, null, null), p)
        w.dispose()
        return out.toByteArray()
    }

    private fun seg(marker: Int, payload: ByteArray): ByteArray {
        val l = payload.size + 2
        return byteArrayOf(0xFF.toByte(), marker.toByte(), (l shr 8).toByte(), l.toByte()) + payload
    }

    /** The bytes with [extra] put right before EOI. */
    private fun beforeEoi(j: ByteArray, extra: ByteArray): ByteArray = j.copyOfRange(0, j.size - 2) + extra + j.copyOfRange(j.size - 2, j.size)

    private fun pixels(b: ByteArray): IntArray = ImageIO.read(ByteArrayInputStream(b)).let { it.getRGB(0, 0, it.width, it.height, null, 0, it.width) }

    private fun leaks(b: ByteArray, s: String) = String(b, Charsets.ISO_8859_1).contains(s)

    @Test
    fun aCommentBeforeTheEndIsDropped() {
        val src = beforeEoi(jpeg(false), seg(0xFE, "GPS 31.2304N 121.4737E".toByteArray()))
        val out = stripJpeg(src)
        assertFalse(leaks(out, "GPS 31.23"))
        assertArrayEquals(pixels(src), pixels(out))
    }

    @Test
    fun exifAfterTheScanIsDropped() {
        val src = beforeEoi(jpeg(false), seg(0xE1, "Exif\u0000\u0000".toByteArray(Charsets.ISO_8859_1) + "LOCATION-AFTER-SCAN".toByteArray()))
        val out = stripJpeg(src)
        assertFalse(leaks(out, "LOCATION-AFTER-SCAN"))
        assertArrayEquals(pixels(src), pixels(out))
    }

    @Test
    fun aProgressiveJpegKeepsEveryScanAndLosesItsComments() {
        val base = jpeg(true)
        // Put a comment between the first two scans.
        val s = String(base, Charsets.ISO_8859_1)
        val second = s.indexOf("ÿÚ", s.indexOf("ÿÚ") + 2)
        assertTrue(second > 0)
        val src = base.copyOfRange(0, second) + seg(0xFE, "BETWEEN-SCANS".toByteArray()) + base.copyOfRange(second, base.size)
        val out = stripJpeg(src)
        assertFalse(leaks(out, "BETWEEN-SCANS"))
        assertArrayEquals(pixels(base), pixels(out))
        assertEquals(s.split("ÿÚ").size, String(out, Charsets.ISO_8859_1).split("ÿÚ").size)
    }

    @Test
    fun fillBytesBeforeARestartMarkerAreScanData() {
        // The web client's fixture: 4x3 MCUs with a restart marker between each two.
        val fixture = "web/src/testdata/images/restart.jpg"
        var dir: java.io.File? = java.io.File("").absoluteFile
        while (dir != null && !java.io.File(dir, fixture).isFile) dir = dir.parentFile
        val src = java.io.File(checkNotNull(dir) { "$fixture is not above ${java.io.File("").absolutePath}" }, fixture).readBytes()
        val s = String(src, Charsets.ISO_8859_1)
        val scan = s.indexOf("ÿÚ")
        // In the scan's data 0xFF is followed by 0x00 or a restart marker.
        val out = java.io.ByteArrayOutputStream()
        out.write(src, 0, scan)
        var restarts = 0
        for (i in scan until src.size) {
            if (src[i] == 0xFF.toByte() && i + 1 < src.size && (src[i + 1].toInt() and 0xff) in 0xD0..0xD7) {
                out.write(0xFF)
                out.write(0xFF)
                restarts++
            }
            out.write(src[i].toInt())
        }
        assertEquals(11, restarts)
        val filled = out.toByteArray()
        val stripped = stripJpeg(filled)
        assertArrayEquals(filled, stripped)
        assertArrayEquals(pixels(src), pixels(stripped))
    }

    @Test
    fun whatFollowsTheEndIsDropped() {
        val src = jpeg(false) + "TRAILER-GPS".toByteArray()
        assertFalse(leaks(stripJpeg(src), "TRAILER-GPS"))
    }
}

class DecodeBudgetTest {
    @Test
    fun aHugePictureIsDecodedWithinTheBudget() {
        // A 200 MP panorama as an original: never decoded whole.
        val s = decodeSample(20_000, 10_000, 0, MAX_DECODE_PIXELS)
        assertTrue((20_000L / s) * (10_000L / s) <= MAX_DECODE_PIXELS)
        assertEquals(4, s)
        // A 12 MP photo fits as it is.
        assertEquals(1, decodeSample(4032, 3024, 0, MAX_DECODE_PIXELS))
        // Compressed: sampled down toward the 2048 long side, then the budget.
        assertEquals(1, decodeSample(4032, 3024, 2048, MAX_DECODE_PIXELS))
        assertEquals(2, decodeSample(8064, 6048, 2048, MAX_DECODE_PIXELS))
        assertEquals(8, decodeSample(4032, 3024, 0, 300_000))
    }
}
