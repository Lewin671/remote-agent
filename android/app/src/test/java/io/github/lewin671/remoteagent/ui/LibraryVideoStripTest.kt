package io.github.lewin671.remoteagent.ui

import android.app.Application
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
import io.github.lewin671.remoteagent.core.proto.AttachmentDraftItemView
import io.github.lewin671.remoteagent.core.proto.AttachmentDraftView
import io.github.lewin671.remoteagent.core.proto.AttachmentItemKind
import io.github.lewin671.remoteagent.core.proto.AttachmentItemState
import io.github.lewin671.remoteagent.core.proto.Text as CoreText
import io.github.lewin671.remoteagent.core.sessionDraftScope
import io.github.lewin671.remoteagent.data.ThemeMode
import org.junit.After
import org.junit.Assert.assertEquals
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
    @get:Rule(order = 0) val sessionMessages = SessionMessageContextRule()
    @get:Rule(order = 1) val ui = createComposeRule()
    private val remote = Remote(Account("http://127.0.0.1:1", "2222222222222222"), Api.defaultHttp())

    @After fun close() = remote.close()

    @Test fun removeTargetOnlyCoversTheCornerAndFitsBeforeTheNextThumbnail() {
        val scope = sessionDraftScope("geometry")
        val builder = AttachmentDraftView.newBuilder().setScope(scope)
        for (i in 1..3) {
            val item = AttachmentDraftItemView.newBuilder()
                .setItemId("image-$i")
                .setKind(AttachmentItemKind.ATTACHMENT_ITEM_KIND_IMAGE)
                .setState(AttachmentItemState.ATTACHMENT_ITEM_STATE_READY)
                .setImageIndex(i - 1)
                .setPositionA11Y(CoreText.newBuilder().setLiteral("第 $i 张，共 3 张"))
                .setRemoveA11Y(CoreText.newBuilder().setLiteral("移除图片 $i"))
                .setCanRemove(true)
                .build()
            builder.addItems(item)
            builder.addImages(item)
        }
        val draft = builder.build()
        ui.setContent { RaTheme(ThemeMode.LIGHT) { AttachmentDraftStrip(draft, scope, remote) } }
        val body = ui.onNodeWithContentDescription("第 1 张，共 3 张").getUnclippedBoundsInRoot()
        val remove = ui.onNodeWithContentDescription("移除图片 1").getUnclippedBoundsInRoot()
        val next = ui.onNodeWithContentDescription("第 2 张，共 3 张").getUnclippedBoundsInRoot()
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
        val scope = sessionDraftScope("s")
        val video = AttachmentDraftItemView.newBuilder()
            .setItemId("v")
            .setKind(AttachmentItemKind.ATTACHMENT_ITEM_KIND_FILE)
            .setIsVideo(true)
            .setState(AttachmentItemState.ATTACHMENT_ITEM_STATE_READY)
            .setDisplayName("clip.mp4")
            .setSizeText(CoreText.newBuilder().setLiteral("2.0 KB"))
            .setDurationText("0:42")
            .setRemoveA11Y(CoreText.newBuilder().setLiteral("移除视频 clip.mp4"))
            .setCanRemove(true)
            .build()
        val doc = AttachmentDraftItemView.newBuilder()
            .setItemId("d")
            .setKind(AttachmentItemKind.ATTACHMENT_ITEM_KIND_FILE)
            .setIsVideo(false)
            .setState(AttachmentItemState.ATTACHMENT_ITEM_STATE_READY)
            .setDisplayName("report.pdf")
            .setSizeText(CoreText.newBuilder().setLiteral("1.0 KB"))
            .setRemoveA11Y(CoreText.newBuilder().setLiteral("移除文件 report.pdf"))
            .setCanRemove(true)
            .build()
        val draft = AttachmentDraftView.newBuilder()
            .setScope(scope)
            .addItems(video)
            .addItems(doc)
            .addFiles(video)
            .addFiles(doc)
            .setFileMetadataNotice(CoreText.newBuilder().setKey(UiMessages.attachmentMetadata().key))
            .build()
        ui.setContent { RaTheme(ThemeMode.LIGHT) { AttachmentDraftStrip(draft, scope, remote) } }
        ui.onNodeWithContentDescription("移除视频 clip.mp4").assertIsDisplayed()
        ui.onNodeWithContentDescription("移除文件 report.pdf").assertIsDisplayed()
        ui.onNodeWithContentDescription("移除文件 clip.mp4").assertDoesNotExist()
        // Only a video: no 原图 and no image count.
        ui.onNodeWithText("原图", substring = true).assertDoesNotExist()
        ui.onNodeWithText(messageTextForTest(UiMessages.attachmentMetadata())).assertIsDisplayed()
    }
}
