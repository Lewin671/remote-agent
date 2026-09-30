package io.github.lewin671.remoteagent.core

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Test
import java.io.File

/** internal/agent/testdata/digest-vectors.json, made by Go's InputDigest. */
class InputDigestTest {
    @Test
    fun matchesGo() {
        val cases = Json.parseToJsonElement(File(System.getProperty("inputDigests")).readText()).jsonArray
        for (c in cases) {
            val o = c.jsonObject
            val input = o["input"]!!.jsonPrimitive.content
            assertEquals(input, o["digest"]!!.jsonPrimitive.content, inputDigest(o["tool"]!!.jsonPrimitive.content, input))
        }
    }

    @Test
    fun writesNumbersAsGo() {
        assertEquals("1e+21", goNumber(1e21))
        assertEquals("1e-7", goNumber(1e-7))
        assertEquals("0.000001", goNumber(1e-6))
        assertEquals("123456789012", goNumber(123456789012.0))
        assertEquals("-0", goNumber(-0.0))
        assertEquals("2.5", goNumber(2.5))
    }
}
