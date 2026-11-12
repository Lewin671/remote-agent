package io.github.lewin671.remoteagent.ui

import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Test

class ComposerDraftTest {
    @Test fun textRestoresWithTheImagesAndEmptyTextRemovesItsRecord() = runBlocking {
        val files = MemoryFiles()
        val first = ImageDraft("session:example", files, scope = null)
        first.text.value = "文字和图片的草稿"
        val later = ImageDraft("session:example", files, scope = null)
        later.restore(this) { null }.join()
        assertEquals(first.text.value, later.text.value)
        later.text.value = ""
        val empty = ImageDraft("session:example", files, scope = null)
        empty.restore(this) { null }.join()
        assertEquals("", empty.text.value)
    }
}
