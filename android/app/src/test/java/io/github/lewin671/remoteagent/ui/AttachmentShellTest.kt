package io.github.lewin671.remoteagent.ui

import android.app.Application
import android.graphics.Bitmap
import android.net.Uri
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.proto.AttachmentDraftItemView
import io.github.lewin671.remoteagent.core.proto.AttachmentDraftView
import io.github.lewin671.remoteagent.core.proto.AttachmentIOErrorKind
import io.github.lewin671.remoteagent.core.proto.AttachmentItemKind
import io.github.lewin671.remoteagent.core.proto.AttachmentItemState
import io.github.lewin671.remoteagent.core.proto.DraftImageViewerView
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
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import java.io.ByteArrayOutputStream
import java.io.File
import java.nio.file.Files

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class AttachmentShellTest {
    @get:Rule(order = 0) val sessionMessages = SessionMessageContextRule()
    @get:Rule(order = 1) val ui = createComposeRule()
    private val remote = Remote(Account("http://127.0.0.1:1", "test-client"), Api.defaultHttp())

    @After fun close() = remote.close()

    @Test fun prepareImageHonorsCoreSuppliedLimitsWithoutHardcodedBudgets() {
        val dir = Files.createTempDirectory("ra-img-prep").toFile()
        try {
            val src = File(dir, "sample.png")
            val bmp = Bitmap.createBitmap(120, 80, Bitmap.Config.ARGB_8888).apply {
                eraseColor(android.graphics.Color.rgb(40, 120, 200))
            }
            val raw = ByteArrayOutputStream().apply { bmp.compress(Bitmap.CompressFormat.PNG, 100, this) }.toByteArray()
            src.writeBytes(raw)
            val ctx = RuntimeEnvironment.getApplication()
            val uri = Uri.fromFile(src).toString()

            // Respects Core's maxDimensionPx=60.
            val compressed = prepareImageFromSource(
                context = ctx,
                sourceRef = uri,
                original = false,
                maxOriginalBytes = 10L * 1024 * 1024,
                maxDimensionPx = 60,
                maxCompressedBytes = 2L * 1024 * 1024,
            )
            assertEquals(60, maxOf(compressed.width, compressed.height))
            assertTrue(compressed.bytes.isNotEmpty())

            // Rejects original when larger than Core's supplied maxOriginalBytes.
            val tooBig = runCatching {
                prepareImageFromSource(
                    context = ctx,
                    sourceRef = uri,
                    original = true,
                    maxOriginalBytes = (raw.size - 1).toLong().coerceAtLeast(1L),
                    maxDimensionPx = 2048,
                    maxCompressedBytes = 2L * 1024 * 1024,
                )
            }.exceptionOrNull() as? PrepareImageException
            assertEquals(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_ORIGINAL_TOO_LARGE, tooBig?.kind)
        } finally {
            dir.deleteRecursively()
        }
    }

    @Test fun draftStripRendersCoreOriginalTotalSizeTextAndDisabledReason() {
        val scope = sessionDraftScope("s1")
        val item = AttachmentDraftItemView.newBuilder()
            .setItemId("img-1")
            .setKind(AttachmentItemKind.ATTACHMENT_ITEM_KIND_IMAGE)
            .setState(AttachmentItemState.ATTACHMENT_ITEM_STATE_READY)
            .setImageIndex(0)
            .setDisplayName("photo.jpg")
            .setCanRemove(true)
            .build()
        val draft = AttachmentDraftView.newBuilder()
            .setScope(scope)
            .addItems(item)
            .addImages(item)
            .setOriginal(true)
            .setCanToggleOriginal(false)
            .setOriginalToggleDisabledReason(CoreText.newBuilder().setLiteral("Original unavailable right now"))
            .setOriginalTotalSizeText(CoreText.newBuilder().setLiteral("（3.4 MB）"))
            .setImageCountText(CoreText.newBuilder().setLiteral("1 / 9"))
            .build()
        ui.setContent { RaTheme(ThemeMode.LIGHT) { AttachmentDraftStrip(draft, scope, remote) } }
        ui.onNodeWithText("（3.4 MB）", substring = true).assertIsDisplayed()
        ui.onNodeWithText("1 / 9").assertIsDisplayed()
        ui.onNodeWithText("（3.4 MB）", substring = true).performClick()
        ui.onNodeWithText("Original unavailable right now").assertIsDisplayed()
    }

    @Test fun draftViewerRendersCorePositionTextAndPauseResumeRetryControls() {
        val scope = sessionDraftScope("s2")
        val item = AttachmentDraftItemView.newBuilder()
            .setItemId("img-1")
            .setKind(AttachmentItemKind.ATTACHMENT_ITEM_KIND_IMAGE)
            .setState(AttachmentItemState.ATTACHMENT_ITEM_STATE_PAUSED)
            .setImageIndex(0)
            .setPositionText(CoreText.newBuilder().setLiteral("1 / 3"))
            .setPositionA11Y(CoreText.newBuilder().setLiteral("Image 1 of 3"))
            .setStatusText(CoreText.newBuilder().setLiteral("Paused at 42%"))
            .setCanResume(true)
            .setCanRetry(true)
            .setCanRemove(true)
            .build()
        val draft = AttachmentDraftView.newBuilder()
            .setScope(scope)
            .addItems(item)
            .addImages(item)
            .setImageViewer(
                DraftImageViewerView.newBuilder()
                    .setOpen(true)
                    .setCurrentIndex(0)
                    .setTotalCount(3)
                    .setPositionText(CoreText.newBuilder().setLiteral("1 / 3")),
            )
            .build()
        ui.setContent { RaTheme(ThemeMode.LIGHT) { DraftImageViewer(draft, scope, remote) } }
        ui.onNodeWithText("1 / 3").assertIsDisplayed()
        ui.onNodeWithText("Paused at 42%").assertIsDisplayed()
        ui.onNodeWithText("恢复").assertIsDisplayed()
        ui.onNodeWithText("重试").assertIsDisplayed()
    }
}
