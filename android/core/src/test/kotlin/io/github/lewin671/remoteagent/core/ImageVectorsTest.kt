package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.corebind.Corebind
import io.github.lewin671.remoteagent.proto.ImageFormat
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.int
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.fail
import org.junit.Test
import java.io.ByteArrayOutputStream
import java.io.File
import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.util.Base64

class ImageVectorsTest {
    private fun findVectorsFile(): File {
        var dir: File? = File("").absoluteFile
        while (dir != null && !File(dir, "testdata/images/vectors.json").isFile) {
            dir = dir.parentFile
        }
        return File(checkNotNull(dir) { "testdata/images/vectors.json not found above ${File("").absolutePath}" }, "testdata/images/vectors.json")
    }

    private fun coreFormatName(code: Int): String = when (code) {
        1 -> "jpeg"
        2 -> "png"
        3 -> "webp"
        4 -> "gif"
        5 -> "heic"
        6 -> "avif"
        else -> "unknown"
    }

    @Test
    fun sharedImageParseAndStripVectors() {
        val root = Json.parseToJsonElement(findVectorsFile().readText()).jsonObject
        val cases = root.getValue("cases").jsonArray
        for (elem in cases) {
            val obj = elem.jsonObject
            val name = obj.getValue("name").jsonPrimitive.content
            val input = Base64.getDecoder().decode(obj.getValue("inputBase64").jsonPrimitive.content)
            val wantFmt = obj.getValue("format").jsonPrimitive.content
            val wantOrient = obj.getValue("orientation").jsonPrimitive.int
            val stripError = obj["stripError"]?.jsonPrimitive?.content
            val stripErrorCode = obj["stripErrorCode"]?.jsonPrimitive?.int ?: 0
            val wantStrippedB64 = obj["expectedStrippedBase64"]?.jsonPrimitive?.content
            val forbidden = obj["forbiddenSubstrings"]?.jsonArray?.map { it.jsonPrimitive.content }.orEmpty()

            // Verify Core format sniffing (including heic/avif) and Android detectFormat passthrough mapping.
            val head = if (input.size > 256) input.copyOfRange(0, 256) else input
            assertEquals("$name: sniff", wantFmt, coreFormatName(Corebind.sniffImageFormat(head)))
            val wantProtoFmt = when (wantFmt) {
                "jpeg" -> ImageFormat.IMAGE_FORMAT_JPEG
                "png" -> ImageFormat.IMAGE_FORMAT_PNG
                "webp" -> ImageFormat.IMAGE_FORMAT_WEBP
                "gif" -> ImageFormat.IMAGE_FORMAT_GIF
                else -> null
            }
            assertEquals("$name: detectFormat", wantProtoFmt, detectFormat(input))
            assertEquals("$name: orientationOf", wantOrient, orientationOf(input))

            if (stripError != null) {
                when (wantFmt) {
                    "jpeg" -> try { stripJpeg(input); fail("$name: expected error") } catch (_: MessageException) {}
                    "png" -> try { stripPng(input); fail("$name: expected error") } catch (_: MessageException) {}
                    "webp" -> try { stripWebp(input); fail("$name: expected error") } catch (_: MessageException) {}
                    "gif" -> try {
                        stripGif(input)
                        fail("$name: expected error")
                    } catch (e: MessageException) {
                        val wantMsg = when (stripErrorCode) {
                            5 -> messageTextForTest(UiMessages.imageAndroidGifContainsAnUnrecognizedBlock())
                            4 -> messageTextForTest(UiMessages.imageAndroidGifIsTruncated())
                            3 -> messageTextForTest(UiMessages.imageAndroidUnreadableGif())
                            else -> null
                        }
                        if (wantMsg != null) assertEquals("$name: gif error message", wantMsg, e.display.testText())
                    }
                    else -> assertNull("$name: stripOriginal unsupported", stripOriginal(input))
                }
            } else if (wantStrippedB64 != null) {
                val wantStripped = Base64.getDecoder().decode(wantStrippedB64)
                val stripped = when (wantFmt) {
                    "jpeg" -> stripJpeg(input)
                    "png" -> stripPng(input)
                    "webp" -> stripWebp(input)
                    "gif" -> stripGif(input)
                    else -> fail("$name: unexpected passthrough format $wantFmt") as ByteArray
                }
                assertArrayEquals("$name: stripped bytes", wantStripped, stripped)
                assertEquals("$name: stripped orientation", 1, orientationOf(stripped))
                val latin = String(stripped, Charsets.ISO_8859_1)
                for (bad in forbidden) {
                    assertFalse("$name: leaked $bad", latin.contains(bad))
                }
                if (wantOrient == 1) {
                    assertArrayEquals("$name: stripOriginal", wantStripped, stripOriginal(input))
                } else {
                    assertNull("$name: stripOriginal turned", stripOriginal(input))
                }
            }
        }
    }

    private fun makeTiffOrient(orient: Int, totalSize: Int): ByteArray {
        val b = ByteArray(maxOf(32, totalSize))
        val hdr = byteArrayOf('M'.code.toByte(), 'M'.code.toByte(), 0, 42, 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, orient.toByte(), 0, 0)
        System.arraycopy(hdr, 0, b, 0, hdr.size)
        return b
    }

    private fun makePngChunk(type: String, payload: ByteArray): ByteArray {
        val out = ByteArray(12 + payload.size)
        ByteBuffer.wrap(out).order(ByteOrder.BIG_ENDIAN).putInt(0, payload.size)
        System.arraycopy(type.toByteArray(Charsets.ISO_8859_1), 0, out, 4, 4)
        System.arraycopy(payload, 0, out, 8, payload.size)
        return out
    }

    private fun makeRiffChunk(fourcc: String, payload: ByteArray): ByteArray {
        val pad = payload.size and 1
        val out = ByteArray(8 + payload.size + pad)
        System.arraycopy(fourcc.toByteArray(Charsets.ISO_8859_1), 0, out, 0, 4)
        ByteBuffer.wrap(out).order(ByteOrder.LITTLE_ENDIAN).putInt(4, payload.size)
        System.arraycopy(payload, 0, out, 8, payload.size)
        return out
    }

    @Test(timeout = 10_000)
    fun largeFilesCoverNeedReadAndWindowContinuationBranches() {
        // 1. JPEG: 70 KB 0xFF fill + full-length APP1 (65535) with fill + 128 KB scan + post-scan COM
        val body = byteArrayOf(
            0xff.toByte(), 0xc0.toByte(), 0, 11, 8, 0, 12, 0, 16, 1, 1, 0x11, 0,
            0xff.toByte(), 0xda.toByte(), 0, 8, 1, 1, 0, 0, 63, 0,
        )
        val scan = ByteArray(128 * 1024) { 0x22 }
        val postCom = ByteArray(4 + 60_000).also {
            it[0] = 0xff.toByte(); it[1] = 0xfe.toByte()
            it[2] = ((60_002 shr 8) and 0xff).toByte(); it[3] = (60_002 and 0xff).toByte()
        }
        val eoi = byteArrayOf(0xff.toByte(), 0xd9.toByte())
        val expectedJpeg = ByteArrayOutputStream().apply {
            write(byteArrayOf(0xff.toByte(), 0xd8.toByte())); write(body); write(scan); write(eoi)
        }.toByteArray()

        val fillJpeg = ByteArrayOutputStream().apply {
            write(byteArrayOf(0xff.toByte(), 0xd8.toByte()))
            write(ByteArray(70_000) { 0xff.toByte() })
            write(body); write(scan); write(postCom); write(eoi)
        }.toByteArray()
        assertEquals(1, orientationOf(fillJpeg))
        assertArrayEquals(expectedJpeg, stripJpeg(fillJpeg))

        val app1Jpeg = ByteArrayOutputStream().apply {
            write(byteArrayOf(0xff.toByte(), 0xd8.toByte(), 0xff.toByte(), 0xff.toByte(), 0xff.toByte(), 0xff.toByte(), 0xff.toByte()))
            write(byteArrayOf(0xff.toByte(), 0xe1.toByte(), 0xff.toByte(), 0xff.toByte()))
            write("Exif\u0000\u0000".toByteArray(Charsets.ISO_8859_1))
            write(makeTiffOrient(3, 65535 - 2 - 6))
            write(body); write(scan); write(postCom); write(eoi)
        }.toByteArray()
        assertEquals(3, orientationOf(app1Jpeg))
        assertArrayEquals(expectedJpeg, stripJpeg(app1Jpeg))

        // 2. PNG: >64 KB eXIf + 24 x 8 KB IDAT chunks + post-IDAT tEXt
        val pngSig = byteArrayOf(0x89.toByte(), 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a)
        val ihdr = makePngChunk("IHDR", byteArrayOf(0, 0, 0, 10, 0, 0, 0, 20, 8, 6, 0, 0, 0))
        val idat = makePngChunk("IDAT", ByteArray(8 * 1024))
        val iend = makePngChunk("IEND", ByteArray(0))
        val pngOut = ByteArrayOutputStream().apply {
            write(pngSig); write(ihdr); write(makePngChunk("eXIf", makeTiffOrient(6, 70_000)))
            repeat(24) { write(idat) }
            write(makePngChunk("tEXt", "Comment\u0000SECRET".toByteArray(Charsets.ISO_8859_1)))
            write(iend)
        }.toByteArray()
        val pngClean = ByteArrayOutputStream().apply {
            write(pngSig); write(ihdr); repeat(24) { write(idat) }; write(iend)
        }.toByteArray()
        assertEquals(6, orientationOf(pngOut))
        assertArrayEquals(pngClean, stripPng(pngOut))

        // 3. WebP: 128 KB VP8L + trailing 70 KB EXIF + XMP
        val vp8x = ByteArray(10).also { it[0] = 0x1c; it[4] = 9; it[7] = 19 }
        val vp8l = ByteArray(128 * 1024).also { it[0] = 0x2f }
        val chunks = ByteArrayOutputStream().apply {
            write(makeRiffChunk("VP8X", vp8x))
            write(makeRiffChunk("VP8L", vp8l))
            write(makeRiffChunk("EXIF", makeTiffOrient(8, 70_000)))
            write(makeRiffChunk("XMP ", "<x:xmpmeta>SECRET</x:xmpmeta>".toByteArray(Charsets.ISO_8859_1)))
        }.toByteArray()
        val webp = ByteArray(12 + chunks.size)
        System.arraycopy("RIFF".toByteArray(Charsets.ISO_8859_1), 0, webp, 0, 4)
        ByteBuffer.wrap(webp).order(ByteOrder.LITTLE_ENDIAN).putInt(4, 4 + chunks.size)
        System.arraycopy("WEBP".toByteArray(Charsets.ISO_8859_1), 0, webp, 8, 4)
        System.arraycopy(chunks, 0, webp, 12, chunks.size)
        assertEquals(8, orientationOf(webp))
        val webpStripped = stripWebp(webp)
        assertEquals(1, orientationOf(webpStripped))
        assertEquals(0x10, webpStripped[20].toInt() and 0xff)
        assertFalse(String(webpStripped, Charsets.ISO_8859_1).contains("SECRET"))
    }
}
