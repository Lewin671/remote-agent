package io.github.lewin671.remoteagent.ui

import android.app.Application
import android.graphics.Bitmap
import android.net.Uri
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
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
import io.github.lewin671.remoteagent.data.decodeDownsampledStream
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import java.io.ByteArrayInputStream
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

    @Test fun draftStripRendersCoreBadgeTextVerbatimForPausedAndFailedItems() {
        val scope = sessionDraftScope("s3")
        val paused = AttachmentDraftItemView.newBuilder()
            .setItemId("vid-1")
            .setKind(AttachmentItemKind.ATTACHMENT_ITEM_KIND_FILE)
            .setIsVideo(true)
            .setState(AttachmentItemState.ATTACHMENT_ITEM_STATE_PAUSED)
            .setDisplayName("clip.mp4")
            .setPositionA11Y(CoreText.newBuilder().setLiteral("Video 1"))
            .setBadgeText(CoreText.newBuilder().setLiteral("CORE-PAUSED-BADGE"))
            .setCanResume(true)
            .setCanRemove(true)
            .build()
        val failed = AttachmentDraftItemView.newBuilder()
            .setItemId("img-2")
            .setKind(AttachmentItemKind.ATTACHMENT_ITEM_KIND_IMAGE)
            .setState(AttachmentItemState.ATTACHMENT_ITEM_STATE_FAILED)
            .setImageIndex(0)
            .setDisplayName("bad.jpg")
            .setPositionA11Y(CoreText.newBuilder().setLiteral("Image 1"))
            .setBadgeText(CoreText.newBuilder().setLiteral("CORE-FAILED-BADGE"))
            .setCanRemove(true)
            .build()
        val noBadge = AttachmentDraftItemView.newBuilder()
            .setItemId("img-3")
            .setKind(AttachmentItemKind.ATTACHMENT_ITEM_KIND_IMAGE)
            .setState(AttachmentItemState.ATTACHMENT_ITEM_STATE_FAILED)
            .build()

        // Shell uses Core's badge_text verbatim without state-based text fallbacks.
        assertEquals("CORE-PAUSED-BADGE", draftThumbBadgeText(paused) { it.key })
        assertEquals("CORE-FAILED-BADGE", draftThumbBadgeText(failed) { it.key })
        assertEquals("", draftThumbBadgeText(noBadge) { it.key })

        val draft = AttachmentDraftView.newBuilder()
            .setScope(scope)
            .addItems(paused)
            .addItems(failed)
            .addImages(failed)
            .build()
        ui.setContent { RaTheme(ThemeMode.LIGHT) { AttachmentDraftStrip(draft, scope, remote) } }
        ui.onNodeWithContentDescription("Video 1").assertIsDisplayed()
        ui.onNodeWithContentDescription("Image 1").assertIsDisplayed()
    }

    @Test fun decodeDownsampledStreamDecodesUnderCapAndRejectsOversizedSource() {
        val bmp = Bitmap.createBitmap(240, 120, Bitmap.Config.ARGB_8888).apply {
            eraseColor(android.graphics.Color.rgb(10, 180, 90))
        }
        val raw = ByteArrayOutputStream().apply { bmp.compress(Bitmap.CompressFormat.PNG, 100, this) }.toByteArray()

        val ok = decodeDownsampledStream(
            openStream = { ByteArrayInputStream(raw) },
            maxLongSide = 120,
            maxSourceBytes = raw.size.toLong() + 1024L,
        )
        assertNotNull(ok)
        assertTrue(maxOf(ok!!.width, ok.height) <= 120)

        // Rejected upfront when knownSizeBytes exceeds cap.
        val rejectedByKnownSize = decodeDownsampledStream(
            openStream = { ByteArrayInputStream(raw) },
            maxLongSide = 120,
            maxSourceBytes = 1024L,
            knownSizeBytes = 1025L,
        )
        assertNull("Oversized known size must be rejected upfront", rejectedByKnownSize)

        // Rejected mid-stream when BoundedInputStream cap is exceeded even without knownSizeBytes.
        val rejectedByStreamCap = decodeDownsampledStream(
            openStream = { ByteArrayInputStream(raw) },
            maxLongSide = 120,
            maxSourceBytes = 16L,
        )
        assertNull("Oversized stream must be rejected cleanly via BoundedInputStream", rejectedByStreamCap)
    }

    @Test fun draftStripRemovesFirstItemWithoutLeakingThumbnailToPreparingSuccessor() {
        val scope = sessionDraftScope("s-thumb-reuse")
        val bmp = Bitmap.createBitmap(16, 16, Bitmap.Config.ARGB_8888).apply {
            eraseColor(android.graphics.Color.RED)
        }.asImageBitmap()
        val readyItem = AttachmentDraftItemView.newBuilder()
            .setItemId("img-a")
            .setKind(AttachmentItemKind.ATTACHMENT_ITEM_KIND_IMAGE)
            .setState(AttachmentItemState.ATTACHMENT_ITEM_STATE_READY)
            .setImageIndex(0)
            .setThumbRef("blob:thumb-a")
            .setPreparedRef("blob:prep-a")
            .setPositionA11Y(CoreText.newBuilder().setLiteral("Image 1"))
            .setCanRemove(true)
            .build()
        val preparingItem = AttachmentDraftItemView.newBuilder()
            .setItemId("img-b")
            .setKind(AttachmentItemKind.ATTACHMENT_ITEM_KIND_IMAGE)
            .setState(AttachmentItemState.ATTACHMENT_ITEM_STATE_PREPARING)
            .setImageIndex(1)
            .setPositionA11Y(CoreText.newBuilder().setLiteral("Image 2"))
            .setCanRemove(true)
            .build()

        val draftState = mutableStateOf(
            AttachmentDraftView.newBuilder()
                .setScope(scope)
                .addItems(readyItem)
                .addItems(preparingItem)
                .addImages(readyItem)
                .addImages(preparingItem)
                .build(),
        )
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                AttachmentDraftStrip(
                    draft = draftState.value,
                    scope = scope,
                    remote = remote,
                    thumbLoader = { t, _ -> if (t == "blob:thumb-a") bmp else null },
                )
            }
        }
        ui.waitForIdle()
        assertEquals(1, ui.onAllNodesWithTag("draft-thumb-bitmap", useUnmergedTree = true).fetchSemanticsNodes().size)

        // Remove the first (ready) item so the still-preparing second item shifts to index 0.
        val shiftedPreparing = preparingItem.toBuilder()
            .setImageIndex(0)
            .setPositionA11Y(CoreText.newBuilder().setLiteral("Image 1"))
            .build()
        draftState.value = AttachmentDraftView.newBuilder()
            .setScope(scope)
            .addItems(shiftedPreparing)
            .addImages(shiftedPreparing)
            .build()
        ui.waitForIdle()
        assertEquals(
            "Removing an earlier item must not leak its decoded thumbnail into a preparing successor",
            0,
            ui.onAllNodesWithTag("draft-thumb-bitmap", useUnmergedTree = true).fetchSemanticsNodes().size,
        )
    }

    @Test fun draftImageThumbClearsBitmapWhenRefsBecomeEmpty() {
        val bmp = Bitmap.createBitmap(16, 16, Bitmap.Config.ARGB_8888).apply {
            eraseColor(android.graphics.Color.BLUE)
        }.asImageBitmap()
        val refs = mutableStateOf("blob:thumb-1" to "blob:prep-1")
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                DraftImageThumb(
                    thumbRef = refs.value.first,
                    preparedRef = refs.value.second,
                    loader = { t, _ -> if (t == "blob:thumb-1") bmp else null },
                )
            }
        }
        ui.waitForIdle()
        ui.onNodeWithTag("draft-thumb-bitmap").assertExists()

        // Clearing refs (e.g. toggling original mode) must clear the previous bitmap.
        refs.value = "" to ""
        ui.waitForIdle()
        ui.onNodeWithTag("draft-thumb-bitmap").assertDoesNotExist()
    }
}
