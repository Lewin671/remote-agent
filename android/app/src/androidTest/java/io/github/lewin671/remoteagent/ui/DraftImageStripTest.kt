package io.github.lewin671.remoteagent.ui

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.dp
import androidx.test.platform.app.InstrumentationRegistry
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.proto.AttachmentDraftItemView
import io.github.lewin671.remoteagent.core.proto.AttachmentDraftView
import io.github.lewin671.remoteagent.core.proto.AttachmentItemKind
import io.github.lewin671.remoteagent.core.proto.AttachmentItemState
import io.github.lewin671.remoteagent.core.proto.DraftImageViewerView
import io.github.lewin671.remoteagent.core.proto.Text as CoreText
import io.github.lewin671.remoteagent.core.sessionDraftScope
import io.github.lewin671.remoteagent.data.ThemeMode
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import java.io.File

/** Real touches with synthetic Core ViewModels only; no account or relay connection. */
class DraftImageStripTest {
    @get:Rule val ui = createComposeRule()

    @Test fun cornerTargetLeavesThePreviewAndNeighbourAvailable() {
        val remote = Remote(Account("http://127.0.0.1:1", "2222222222222222"), Api.defaultHttp())
        val scope = sessionDraftScope("geometry")
        fun buildDraft(ids: List<String>, viewerOpenIndex: Int = -1): AttachmentDraftView {
            val builder = AttachmentDraftView.newBuilder()
                .setScope(scope)
                .setCanSend(true)
            ids.forEachIndexed { idx, id ->
                val item = AttachmentDraftItemView.newBuilder()
                    .setItemId(id)
                    .setKind(AttachmentItemKind.ATTACHMENT_ITEM_KIND_IMAGE)
                    .setState(AttachmentItemState.ATTACHMENT_ITEM_STATE_READY)
                    .setImageIndex(idx)
                    .setDisplayName("image-$idx.png")
                    .setSizeBytes(1024)
                    .setPositionText(CoreText.newBuilder().setLiteral("${idx + 1} / ${ids.size}"))
                    .setPositionA11Y(CoreText.newBuilder().setLiteral("第 ${idx + 1} 张，共 ${ids.size} 张"))
                    .setRemoveA11Y(CoreText.newBuilder().setLiteral("移除图片 ${idx + 1}"))
                    .setCanRemove(true)
                    .build()
                builder.addItems(item)
                builder.addImages(item)
            }
            if (viewerOpenIndex in ids.indices) {
                builder.setImageViewer(
                    DraftImageViewerView.newBuilder()
                        .setOpen(true)
                        .setCurrentIndex(viewerOpenIndex)
                        .setTotalCount(ids.size)
                        .setPositionText(CoreText.newBuilder().setLiteral("${viewerOpenIndex + 1} / ${ids.size}")),
                )
            }
            return builder.build()
        }
        var items by mutableStateOf(listOf("image-0", "image-1", "image-2"))
        var viewerIndex by mutableStateOf(-1)
        try {
            ui.setContent {
                val draft = buildDraft(items, viewerIndex)
                RaTheme(ThemeMode.LIGHT) {
                    Column(Modifier.fillMaxSize().background(Color(0xfff6f4ef)).padding(16.dp)) {
                        Text("草稿图片 · 点击区域验收")
                        Spacer(Modifier.height(24.dp))
                        AttachmentDraftStrip(
                            draft = draft,
                            scope = scope,
                            remote = remote,
                            onOpenViewer = { idx -> viewerIndex = idx },
                            onRemoveItem = { id -> items = items.filterNot { it == id } },
                        )
                        ComposeBox("查看这些图片", {}, "消息", false, {}, {})
                    }
                    if (draft.hasImageViewer() && draft.imageViewer.open) {
                        DraftImageViewer(draft, scope, remote, onClose = { viewerIndex = -1 })
                    }
                }
            }
            val first = ui.onNodeWithContentDescription("第 1 张，共 3 张")
            val remove = ui.onNodeWithContentDescription("移除图片 1")
            val body = first.getUnclippedBoundsInRoot()
            val hit = remove.getUnclippedBoundsInRoot()
            val next = ui.onNodeWithContentDescription("第 2 张，共 3 张").getUnclippedBoundsInRoot()
            assertEquals(44f, (hit.right - hit.left).value, 0.5f)
            assertEquals(44f, (hit.bottom - hit.top).value, 0.5f)
            assertEquals(body.right.value, ((hit.left + hit.right) / 2).value, 0.5f)
            assertEquals(body.top.value, ((hit.top + hit.bottom) / 2).value, 0.5f)
            assertTrue(hit.right <= next.left + 0.5.dp)
            // Layout rounds dp to physical pixels (the Pixel 3 has density 2.75).
            val overlapWidth = minOf(body.right, hit.right) - maxOf(body.left, hit.left)
            val overlapHeight = minOf(body.bottom, hit.bottom) - maxOf(body.top, hit.top)
            assertTrue(overlapWidth.value * overlapHeight.value <= 22f * 22f + 0.1f)
            val evidence = "body=$body\nremove=$hit\nnext=$next\noverlap=$overlapWidth x $overlapHeight; cell=86x86 dp\n"
            val instrumentation = InstrumentationRegistry.getInstrumentation()
            val dir = File(instrumentation.targetContext.getExternalFilesDir(null), "thumb-remove").apply { mkdirs() }
            File(dir, "android-hit-rects.txt").writeText(evidence)
            ui.waitForIdle()
            File(dir, "android-draft-images.png").outputStream().use {
                instrumentation.uiAutomation.takeScreenshot().compress(Bitmap.CompressFormat.PNG, 100, it)
            }
            first.performTouchInput { click(center) }
            ui.onNodeWithContentDescription("关闭").performClick()
            ui.runOnIdle { assertEquals(3, items.size) }
            // Just beyond the target's lower-left corner: preview, not removal.
            first.performTouchInput { click(androidx.compose.ui.geometry.Offset(41.dp.toPx(), 21.dp.toPx())) }
            ui.onNodeWithContentDescription("关闭").performClick()
            ui.runOnIdle { assertEquals(3, items.size) }
            remove.performTouchInput { click(androidx.compose.ui.geometry.Offset(33.dp.toPx(), 11.dp.toPx())) }
            ui.runOnIdle { assertEquals(2, items.size) }
        } finally { remote.close() }
    }
}
