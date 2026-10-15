package io.github.lewin671.remoteagent.term

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File

/**
 * The cases the three clients read coloured terminal history by. The web and iOS clients
 * run the same file, so a change there changes all of them or fails one.
 */
class TerminalSgrTest {
    private val cases: JsonArray = run {
        var dir: File? = File("").absoluteFile
        while (dir != null && !File(dir, "testdata/terminal/history-sgr.json").isFile) dir = dir.parentFile
        val file = File(checkNotNull(dir) { "testdata/terminal/history-sgr.json is not above ${File("").absolutePath}" }, "testdata/terminal/history-sgr.json")
        Json.parseToJsonElement(file.readText()).jsonObject.getValue("cases").jsonArray
    }

    private fun color(c: SgrColor): JsonElement = when (c) {
        is SgrColor.Palette -> JsonPrimitive(c.index)
        is SgrColor.Rgb -> JsonPrimitive(c.hex)
    }

    /** One line's runs in the shape the file has them: only the styles that are set are written. */
    private fun runs(runs: List<SgrRun>): JsonElement = JsonArray(runs.map { r ->
        JsonObject(buildMap {
            put("text", JsonPrimitive(r.text))
            r.fg?.let { put("fg", color(it)) }
            r.bg?.let { put("bg", color(it)) }
            if (r.bold) put("bold", JsonPrimitive(true))
            if (r.dim) put("dim", JsonPrimitive(true))
            if (r.italic) put("italic", JsonPrimitive(true))
            if (r.underline) put("underline", JsonPrimitive(true))
            if (r.inverse) put("inverse", JsonPrimitive(true))
        })
    })

    /** Runs every case and names all that differ, not only the first. */
    @Test fun everyCaseReadsTheSame() {
        assertTrue("no cases in testdata/terminal/history-sgr.json", cases.isNotEmpty())
        val failed = cases.mapNotNull { case ->
            val c = case.jsonObject
            val got = runs(TerminalSgr.parse(c.getValue("line").jsonPrimitive.content))
            val want = c.getValue("runs")
            if (got == want) null else "${c.getValue("name").jsonPrimitive.content}\n  want $want\n  got  $got"
        }
        assertEquals("cases that read differently", emptyList<String>(), failed)
    }
}
