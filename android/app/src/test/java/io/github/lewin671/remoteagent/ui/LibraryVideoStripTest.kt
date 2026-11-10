package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.data.ThemeMode
import org.junit.After
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

/** session-files 「从相册选视频」: a video sits in the image row; other files stay chips. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class LibraryVideoStripTest {
    @get:Rule val ui = createComposeRule()
    private val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "2222222222222222"), Api.defaultHttp())

    @After fun close() = remote.close()

    @Test fun aVideoIsAThumbnailBesideTheImagesAndADocumentAChip() {
        val draft = ImageDraft("session:s", MemoryFiles())
        assertTrue(draft.attach(listOf(PendingImage("v", null, "clip.mp4", 2048, retainSource = false), PendingImage("d", null, "report.pdf", 1024, retainSource = false)), 9, fileMax = 6))
        ui.setContent { RaTheme(ThemeMode.LIGHT) { ImageStrip(draft, remote, null, 9) } }
        ui.onNodeWithContentDescription("移除视频 clip.mp4").assertIsDisplayed()
        ui.onNodeWithContentDescription("移除文件 report.pdf").assertIsDisplayed()
        ui.onNodeWithContentDescription("移除文件 clip.mp4").assertDoesNotExist()
        // Only a video: no 原图 and no image count.
        ui.onNodeWithText("原图", substring = true).assertDoesNotExist()
        ui.onNodeWithText(FILES_AS_THEY_ARE).assertIsDisplayed()
    }
}
