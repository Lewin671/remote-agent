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
import org.junit.Assert.assertEquals
import androidx.compose.ui.unit.dp
import androidx.compose.ui.test.getUnclippedBoundsInRoot
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

    @Test fun removeTargetOnlyCoversTheCornerAndFitsBeforeTheNextThumbnail() {
        val draft = ImageDraft("session:geometry", MemoryFiles())
        assertTrue(draft.attach((1..3).map { PendingImage("image-$it", null) }, 9))
        ui.setContent { RaTheme(ThemeMode.LIGHT) { ImageStrip(draft, remote, null, 9) } }
        val body = ui.onNodeWithContentDescription("图片 1，共 3 张").getUnclippedBoundsInRoot()
        val remove = ui.onNodeWithContentDescription("移除图片 1").getUnclippedBoundsInRoot()
        val next = ui.onNodeWithContentDescription("图片 2，共 3 张").getUnclippedBoundsInRoot()
        assertEquals(64.dp, (body.right - body.left))
        assertEquals(64.dp, (body.bottom - body.top))
        assertEquals(44.dp, (remove.right - remove.left))
        assertEquals(44.dp, (remove.bottom - remove.top))
        assertEquals(body.right, ((remove.left + remove.right) / 2))
        assertEquals(body.top, ((remove.top + remove.bottom) / 2))
        val overlapWidth = minOf(body.right, remove.right) - maxOf(body.left, remove.left)
        val overlapHeight = minOf(body.bottom, remove.bottom) - maxOf(body.top, remove.top)
        assertTrue(overlapWidth.value * overlapHeight.value <= 22f * 22f)
        assertTrue(((body.left + body.right) / 2) < remove.left && ((body.top + body.bottom) / 2) > remove.bottom)
        assertTrue(remove.right <= next.left)
        assertTrue(remove.top >= 0.dp)
    }

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
