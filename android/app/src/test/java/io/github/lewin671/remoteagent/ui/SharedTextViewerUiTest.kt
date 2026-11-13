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
import io.github.lewin671.remoteagent.data.SharedFileManager
import io.github.lewin671.remoteagent.data.SharedTextMaxBytes
import io.github.lewin671.remoteagent.proto.SharedFile
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
        manager = SharedFileManager(context) { null }
    }

    private fun mountHost() {
        ui.setContent {
            RaTheme(LightPalette) {
                SharedFileHost("text", "Test computer", true, false, manager = manager) {
                    host = LocalSharedFileHost.current!!
                }
            }
        }
        ui.waitForIdle()
    }

    private fun open(bytes: ByteArray) {
        val file = SharedFile.newBuilder().setFileId("text").setName("notes.md").setSizeBytes(bytes.size.toLong()).build()
        val local = manager.store.localFileFor("text", file).apply { parentFile!!.mkdirs(); writeBytes(bytes) }
        ui.runOnIdle { openSharedFile(context, file, local, host) }
        ui.waitUntil(5_000) {
            ui.mainClock.advanceTimeByFrame()
            ui.waitForIdle()
            host.pendingTextPreview == null
        }
        ui.waitForIdle()
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
        val actions = mutableListOf<SharedMenuAction>()
        var closed = false
        ui.setContent {
            RaTheme(DarkPalette) {
                CompositionLocalProvider(LocalMessageLocale provides "en") {
                    SharedFileTextViewerDialog("notes.txt", "Plain text", { closed = true }, actions::add)
                }
            }
        }
        fun copy(message: io.github.lewin671.remoteagent.core.UiMessage) = resolveUiMessage(context, message, "en")
        ui.onNodeWithContentDescription(copy(UiMessages.sharedFileMenuShare())).performClick()
        ui.onNodeWithContentDescription(copy(UiMessages.commonAndroidMore())).performClick()
        ui.onNodeWithText(copy(UiMessages.sharedFileMenuOpenWith())).performClick()
        ui.onNodeWithContentDescription(copy(UiMessages.commonAndroidMore())).performClick()
        ui.onNodeWithText(copy(UiMessages.sharedFileMenuSaveToDownloads())).performClick()
        assertEquals(listOf(SharedMenuAction.SHARE, SharedMenuAction.OPEN_WITH, SharedMenuAction.SAVE_TO_DOWNLOADS), actions)
        ui.onNodeWithContentDescription(copy(UiMessages.commonBack())).performClick()
        assertTrue(closed)
    }

    @Test fun middleEllipsisKeepsExtensionAndDoesNotStallAtSurrogatePairs() {
        lateinit var measurer: androidx.compose.ui.text.TextMeasurer
        ui.setContent { measurer = androidx.compose.ui.text.rememberTextMeasurer() }
        ui.runOnIdle {
            for (width in 100..240) {
                val title = fitMiddleEllipsizedName("🪴".repeat(60) + ".markdown", T.s17b, width, 1, measurer)
                assertTrue(title.endsWith(".markdown"))
                assertTrue(title.contains('…'))
                assertEquals(title, title.toByteArray().toString(Charsets.UTF_8))
            }
        }
    }
}
