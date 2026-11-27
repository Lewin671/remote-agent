package io.github.lewin671.remoteagent.ui

import android.app.Application
import android.content.ComponentName
import android.content.Intent
import android.content.IntentFilter
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.assertHeightIsEqualTo
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.core.app.ApplicationProvider
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.SessionSharedFileItem
import io.github.lewin671.remoteagent.core.proto.SharedFileMenuAction
import io.github.lewin671.remoteagent.core.proto.SharedFileMenuItemView
import io.github.lewin671.remoteagent.core.proto.Text as CoreText
import io.github.lewin671.remoteagent.data.SharedFileManager
import io.github.lewin671.remoteagent.data.SharedTextMaxBytes
import kotlinx.coroutines.Dispatchers
import org.junit.After
import org.junit.Assert.*
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class, qualifiers = "w390dp-h844dp-mdpi")
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class SharedTextViewerUiTest {
    @get:Rule val ui = createComposeRule()
    private val context: Application get() = ApplicationProvider.getApplicationContext()
    private lateinit var manager: SharedFileManager
    private lateinit var host: SharedFileHostState

    @Before fun setup() {
        (androidx.core.content.FileProvider::class.java.getDeclaredField("sCache").apply { isAccessible = true }
            .get(null) as MutableMap<*, *>).clear()
        manager = SharedFileManager(context, ioDispatcher = Dispatchers.Unconfined) { null }
    }

    @After fun teardown() {
        manager.close()
    }

    private fun mountHost() {
        ui.setContent {
            RaTheme(LightPalette) {
                SharedFileHost("text", manager = manager) {
                    host = LocalSharedFileHost.current!!
                }
            }
        }
        ui.waitForIdle()
    }

    private fun open(bytes: ByteArray) {
        val file = SessionSharedFileItem.newBuilder()
            .setFileId("text")
            .setDisplayName("notes.md")
            .setSizeBytes(bytes.size.toLong())
            .build()
        val local = manager.seedCommittedFileForTest("text", file.fileId, file.displayName, bytes)
        ui.runOnIdle { openSharedFile(context, file, local, host) }
        ui.waitForIdle()
        assertNull(host.pendingTextPreview)
    }

    @Test fun validMarkdownOpensOnlyInApp() {
        mountHost()
        open("# Preview\n\nReadable **Markdown**".toByteArray())
        ui.onNodeWithTag("text-viewer").assertIsDisplayed()
        ui.onNodeWithTag("text-viewer-toolbar").assertHeightIsEqualTo(56.dp)
        assertEquals("# Preview\n\nReadable **Markdown**", host.textPreview?.text)
        assertNull(shadowOf(context).nextStartedActivity)
    }

    @Test fun rejectedContentFallsBackToPlainTextChooserWithoutAPreview() {
        val component = ComponentName("com.example.reader", "com.example.reader.Main")
        shadowOf(context.packageManager).apply {
            addActivityIfNotPresent(component)
            addIntentFilterForActivity(component, IntentFilter(Intent.ACTION_VIEW).apply {
                addCategory(Intent.CATEGORY_DEFAULT)
                addDataType("text/plain")
            })
        }
        mountHost()
        for (bytes in listOf(ByteArray(SharedTextMaxBytes + 1) { 65 }, byteArrayOf(65, 0, 66), byteArrayOf(0xFF.toByte()))) {
            open(bytes)
            assertNull(host.textPreview)
            assertNull(host.unopenableTarget)
            val chooser = shadowOf(context).nextStartedActivity
            assertEquals(Intent.ACTION_CHOOSER, chooser.action)
            assertEquals("text/plain", chooser.getParcelableExtra<Intent>(Intent.EXTRA_INTENT)!!.type)
        }
    }

    @Test fun rejectedContentWithoutAReaderUsesTheExistingNotice() {
        mountHost()
        open(byteArrayOf(0))
        assertNull(host.textPreview)
        assertNotNull(host.unopenableTarget)
        assertNull(shadowOf(context).nextStartedActivity)
    }

    @Test fun toolbarAndMenuDispatchTheExistingActions() {
        val actions = mutableListOf<SharedFileMenuAction>()
        var closed = false
        val menuItems = listOf(
            SharedFileMenuItemView.newBuilder()
                .setAction(SharedFileMenuAction.SHARED_FILE_MENU_ACTION_SAVE)
                .setLabel(CoreText.newBuilder().setKey(UiMessages.sharedFileMenuSaveToDevice().key))
                .setEnabled(true)
                .build(),
            SharedFileMenuItemView.newBuilder()
                .setAction(SharedFileMenuAction.SHARED_FILE_MENU_ACTION_OPEN_WITH)
                .setLabel(CoreText.newBuilder().setKey(UiMessages.sharedFileMenuOpenWith().key))
                .setEnabled(true)
                .build(),
        )
        ui.setContent {
            RaTheme(DarkPalette) {
                CompositionLocalProvider(LocalMessageLocale provides "en") {
                    SharedFileTextViewerDialog("notes.txt", "Plain text", { closed = true }, actions::add, menuItems = menuItems)
                }
            }
        }
        fun copy(message: io.github.lewin671.remoteagent.core.UiMessage) = resolveUiMessage(context, message, "en")
        ui.onNodeWithContentDescription(copy(UiMessages.sharedFileMenuShare())).performClick()
        ui.onNodeWithContentDescription(copy(UiMessages.commonAndroidMore())).performClick()
        ui.onNodeWithText(copy(UiMessages.sharedFileMenuOpenWith())).performClick()
        ui.onNodeWithContentDescription(copy(UiMessages.commonAndroidMore())).performClick()
        ui.onNodeWithText(copy(UiMessages.sharedFileMenuSaveToDevice())).performClick()
        assertEquals(
            listOf(
                SharedFileMenuAction.SHARED_FILE_MENU_ACTION_SHARE,
                SharedFileMenuAction.SHARED_FILE_MENU_ACTION_OPEN_WITH,
                SharedFileMenuAction.SHARED_FILE_MENU_ACTION_SAVE,
            ),
            actions,
        )
        ui.onNodeWithContentDescription(copy(UiMessages.commonBack())).performClick()
        assertTrue(closed)
    }

    @Test fun middleEllipsisKeepsExtensionAndDoesNotStallAtSurrogatePairs() {
        lateinit var measurer: androidx.compose.ui.text.TextMeasurer
        ui.setContent { measurer = androidx.compose.ui.text.rememberTextMeasurer() }
        ui.runOnIdle {
            val head = "🪴".repeat(57)
            val tail = "🪴".repeat(3) + ".markdown"
            val full = head + tail
            for (width in 100..240) {
                val title = fitMiddleEllipsizedName(full, head, tail, T.s17b, width, 1, measurer)
                assertTrue(title.endsWith(".markdown"))
                assertTrue(title.contains('…'))
                assertEquals(title, title.toByteArray().toString(Charsets.UTF_8))
            }
        }
    }
}
