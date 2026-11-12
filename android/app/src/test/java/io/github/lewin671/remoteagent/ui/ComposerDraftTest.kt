package io.github.lewin671.remoteagent.ui

import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Test

@org.junit.runner.RunWith(org.robolectric.RobolectricTestRunner::class)
@org.robolectric.annotation.Config(sdk = [35], application = android.app.Application::class)
class ComposerDraftTest {
    @org.junit.Before fun sessionMessageContext() = installSessionMessageContext()
    @Test fun removingASessionDropsItsDraftAndOutgoingRecordsOnly() {
        val files = MemoryFiles()
        val records = OutgoingRecords(files)
        records.save("gone", Outgoing("one", "old"), false)
        records.save("keep", Outgoing("two", "keep"), false)
        val draft = ImageDraft("session:gone", files, scope = null)
        draft.text.value = "draft"
        draft.clear()
        records.removeSession("gone")
        assertEquals(emptyList<Outgoing>(), records.load("gone"))
        assertEquals(listOf("two"), records.load("keep").map { it.requestId })
        assertEquals(null, files.get("session:gone", "text"))
    }

    @Test fun unknownTextOnlyMessagesCanBePutBack() {
        val o = Outgoing("text-only", "hello").apply { state = Outgoing.State.UNKNOWN }
        org.junit.Assert.assertTrue(o.canPutBack)
    }

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
