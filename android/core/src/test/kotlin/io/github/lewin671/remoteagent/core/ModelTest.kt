package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.assertMessageEquals as assertEquals

import org.junit.Test

class ModelTest {
    @Test fun markdownTablePreviewMatchesSharedVectors() {
        val vectors = kotlinx.serialization.json.Json.parseToJsonElement(
            java.io.File(System.getProperty("previewTableVectors")).readText()) as kotlinx.serialization.json.JsonArray
        for (vector in vectors) {
            val fields = vector as kotlinx.serialization.json.JsonObject
            fun value(key: String) = (fields.getValue(key) as kotlinx.serialization.json.JsonPrimitive).content
            org.junit.Assert.assertEquals(value("input"), value("expected"), plain(value("input")))
        }
        assertEquals("修 bug", plain("修 `bug`"))
    }
}
