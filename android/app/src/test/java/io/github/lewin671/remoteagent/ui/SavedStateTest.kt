package io.github.lewin671.remoteagent.ui

import java.io.File
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Saved instance state can be written to disk by the system in plaintext,
 * outside the ciphertext cache and the Keystore (architecture §15), so text a
 * user writes (first messages, deny reasons, join codes) stays in memory.
 */
class SavedStateTest {
    // Not written by the user about their work.
    private val allowed = setOf("server")

    @Test
    fun noWrittenTextGoesIntoSavedState() {
        val src = File("src/main/java")
        assertTrue(src.isDirectory)
        val saved = Regex("""var (\w+) by rememberSaveable(\([^)]*\))? \{ mutableStateOf\(""")
        val texts = src.walk().filter { it.extension == "kt" }.flatMap { f ->
            f.readLines().mapNotNull { line ->
                val m = saved.find(line) ?: return@mapNotNull null
                // Strings only; flags and indexes are not content.
                if (!line.substring(m.range.last + 1).startsWith("\"")) return@mapNotNull null
                m.groupValues[1].takeIf { it !in allowed }?.let { "${f.name}: $it" }
            }
        }.toList()
        assertEquals(emptyList<String>(), texts)
    }

    @Test
    fun draftsLastForTheRun() {
        Drafts.of("deny:a1").value = "不要删"
        assertEquals("不要删", Drafts.of("deny:a1").value)
        assertEquals("", Drafts.of("deny:a2").value)
    }
}
