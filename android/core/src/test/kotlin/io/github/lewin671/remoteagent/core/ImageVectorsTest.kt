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
import java.io.File
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
                    "gif" -> try { stripGif(input); fail("$name: expected error") } catch (_: MessageException) {}
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
}
