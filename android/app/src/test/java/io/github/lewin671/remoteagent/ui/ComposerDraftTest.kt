package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.size
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.data.ThemeMode
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test

@org.junit.runner.RunWith(org.robolectric.RobolectricTestRunner::class)
@org.robolectric.annotation.Config(sdk = [35], qualifiers = "w392dp-h785dp-440dpi", application = android.app.Application::class)
class ComposerDraftTest {
    @get:Rule(order = 0) val sessionMessages = io.github.lewin671.remoteagent.ui.SessionMessageContextRule()
    @get:Rule(order = 1) val ui = createComposeRule()

    @Test fun removingASessionDropsItsDraftAndOutgoingRecordsOnly() {
        val files = MemoryFiles()
        val records = OutgoingRecords(files)
        records.save("gone", Outgoing("one"), false)
        records.save("keep", Outgoing("two"), false)
        val draft = ImageDraft("session:gone", files, scope = null)
        draft.text.value = "draft"
        draft.clear()
        records.removeSession("gone")
        assertEquals(emptyList<Outgoing>(), records.load("gone"))
        assertEquals(listOf("two"), records.load("keep").map { it.requestId })
        assertEquals(null, files.get("session:gone", "text"))
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

    @Test fun fullAttachmentDraftKeepsInputRowVisibleAndUnsquashedInTightViewport() {
        val remote = Remote(Account("http://127.0.0.1:1", "2222222222222222"), Api.defaultHttp())
        try {
            val draft = ImageDraft("session:tight", MemoryFiles(), scope = null)
            assertTrue(draft.attach((1..6).map { PendingImage("img-$it", null) }))
            assertTrue(
                draft.attach(
                    (1..6).map { PendingImage("file-$it", "content://doc/$it", "ra-p3-f$it.txt", 1024L, retainSource = false) },
                ),
            )
            // Pixel 3 keyboard-open chat viewport (~392dp x 240dp).
            ui.setContent {
                RaTheme(ThemeMode.DARK) {
                    Column(Modifier.size(392.dp, 240.dp)) {
                        Box(Modifier.weight(1f).fillMaxWidth())
                        ComposeBox(
                            text = "pixel3 hello",
                            onText = {},
                            placeholder = sessionText(UiMessages.composerMessage()),
                            canSend = true,
                            onSend = {},
                            onPlus = {},
                            onFile = {},
                            hasDraft = true,
                            top = { ImageStrip(draft, remote, null, 6) },
                        )
                    }
                }
            }
            val attach = ui.onNodeWithContentDescription(sessionText(UiMessages.newSessionIosAttach()))
            val send = ui.onNodeWithContentDescription(sessionText(UiMessages.composerSend()))
            val input = ui.onNodeWithText("pixel3 hello")
            attach.assertIsDisplayed()
            send.assertIsDisplayed()
            input.assertIsDisplayed()
            val attachBounds = attach.getUnclippedBoundsInRoot()
            val sendBounds = send.getUnclippedBoundsInRoot()
            val inputBounds = input.getUnclippedBoundsInRoot()
            assertTrue("attach button squashed: $attachBounds", attachBounds.bottom - attachBounds.top >= 40.dp)
            assertTrue("send button squashed: $sendBounds", sendBounds.bottom - sendBounds.top >= 40.dp)
            assertTrue("attach button clipped below viewport: $attachBounds", attachBounds.bottom <= 240.dp)
            assertTrue("send button clipped below viewport: $sendBounds", sendBounds.bottom <= 240.dp)
            assertTrue("input text clipped below viewport: $inputBounds", inputBounds.bottom <= 240.dp)
        } finally {
            remote.close()
        }
    }
}
