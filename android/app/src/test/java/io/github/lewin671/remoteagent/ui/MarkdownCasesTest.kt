package io.github.lewin671.remoteagent.ui

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.boolean
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File

/**
 * The cases both clients read Markdown by. The web client runs the same
 * file (markdown.cases.test.ts), so a change there changes both or fails
 * one. The trees are compared in the shape the file has them.
 */
class MarkdownCasesTest {
    private val cases: JsonObject = run {
        var dir: File? = File("").absoluteFile
        while (dir != null && !File(dir, "testdata/markdown/cases.json").isFile) dir = dir.parentFile
        val file = File(checkNotNull(dir) { "testdata/markdown/cases.json is not above ${File("").absolutePath}" }, "testdata/markdown/cases.json")
        Json.parseToJsonElement(file.readText()).jsonObject
    }

    private fun group(name: String): List<JsonObject> = cases.getValue(name).jsonArray.map { it.jsonObject }.also { assertTrue("no $name cases", it.isNotEmpty()) }
    private fun JsonObject.str(key: String): String = getValue(key).jsonPrimitive.content

    private fun strings(v: List<String>) = JsonArray(v.map { JsonPrimitive(it) })
    private fun obj(vararg fields: Pair<String, JsonElement>) = JsonObject(mapOf(*fields))

    private fun spans(v: List<Span>): JsonElement = JsonArray(v.map { s ->
        when (s) {
            is Span.Text -> obj("t" to JsonPrimitive("text"), "v" to JsonPrimitive(s.v))
            is Span.Code -> obj("t" to JsonPrimitive("code"), "v" to JsonPrimitive(s.v))
            is Span.Bold -> obj("t" to JsonPrimitive("b"), "c" to spans(s.c))
            is Span.Italic -> obj("t" to JsonPrimitive("i"), "c" to spans(s.c))
            is Span.Strike -> obj("t" to JsonPrimitive("s"), "c" to spans(s.c))
            is Span.Link -> obj("t" to JsonPrimitive("link"), "v" to JsonPrimitive(s.v), "href" to JsonPrimitive(s.href))
            is Span.Ref -> obj("t" to JsonPrimitive("ref"), "v" to JsonPrimitive(s.v), "target" to JsonPrimitive(s.target))
        }
    })

    private fun blocks(v: List<Block>): JsonElement = JsonArray(v.map { b ->
        when (b) {
            is Block.Para -> obj("kind" to JsonPrimitive("text"), "text" to JsonPrimitive(b.text))
            is Block.Code -> obj("kind" to JsonPrimitive("code"), "lang" to JsonPrimitive(b.lang), "text" to JsonPrimitive(b.text))
            is Block.Heading -> obj("kind" to JsonPrimitive("heading"), "level" to JsonPrimitive(b.level), "text" to JsonPrimitive(b.text))
            Block.Rule -> obj("kind" to JsonPrimitive("rule"))
            is Block.Quote -> obj("kind" to JsonPrimitive("quote"), "blocks" to blocks(b.blocks))
            is Block.ItemList -> obj(
                "kind" to JsonPrimitive("list"), "ordered" to JsonPrimitive(b.ordered), "start" to JsonPrimitive(b.start),
                "items" to JsonArray(b.items.map { item ->
                    JsonObject(buildMap {
                        put("text", JsonPrimitive(item.text))
                        // The file has no key at all for an item that is not a task.
                        item.task?.let { put("task", JsonPrimitive(if (it == Task.DONE) "done" else "todo")) }
                        put("children", blocks(item.children))
                    })
                }),
            )
            is Block.Table -> obj(
                "kind" to JsonPrimitive("table"), "head" to strings(b.head),
                "align" to JsonArray(b.align.map { a -> if (a == null) JsonNull else JsonPrimitive(a.name.lowercase()) }),
                "rows" to JsonArray(b.rows.map { strings(it) }),
            )
        }
    })

    /** Runs every case and names all that differ, not only the first. */
    private fun check(name: String, case: (JsonObject) -> String?) {
        val failed = group(name).mapNotNull(case)
        assertEquals("$name cases that read differently", emptyList<String>(), failed)
    }

    @Test fun blocks() = check("blocks") { c ->
        val got = blocks(parseMarkdown(c.str("md")))
        if (got == c.getValue("want")) null else "${c.str("name")}\n  want ${c.getValue("want")}\n  got  $got"
    }

    @Test fun inline() = check("inline") { c ->
        val got = spans(inlineSpans(c.str("md")))
        if (got == c.getValue("want")) null else "${c.str("name")}\n  want ${c.getValue("want")}\n  got  $got"
    }

    @Test fun numericColumns() = check("numericColumn") { c ->
        val cells = c.getValue("cells").jsonArray.map { it.jsonPrimitive.content }
        val want = c.getValue("want").jsonPrimitive.boolean
        if (numericColumn(cells) == want) null else "$cells should be $want"
    }

    @Test fun looseLists() = check("looseList") { c ->
        val items = c.getValue("items").jsonArray.map { it.jsonPrimitive.content }
        val want = c.getValue("want").jsonPrimitive.boolean
        if (looseList(items) == want) null else "$items should be $want"
    }

    @Test fun boldHeadings() = check("boldHeading") { c ->
        val want = c.getValue("want").jsonPrimitive.boolean
        if (boldHeading(c.str("md")) == want) null else "${c.getValue("md")} should be $want"
    }
}
