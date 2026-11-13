package io.github.lewin671.remoteagent.ui

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.dp
import androidx.test.platform.app.InstrumentationRegistry
import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.data.ThemeMode
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import java.io.File

/** Real touches with synthetic images only; no account or relay connection. */
class DraftImageStripTest {
    @get:Rule val ui = createComposeRule()

    @Test fun cornerTargetLeavesThePreviewAndNeighbourAvailable() {
        val files = object : SealedFiles {
            private val data = mutableMapOf<String, ByteArray>()
            override fun put(folder: String, name: String, data: ByteArray) { this.data["$folder/$name"] = data }
            override fun get(folder: String, name: String) = data["$folder/$name"]
            override fun remove(folder: String, name: String) { data.remove("$folder/$name") }
            override fun clearFolder(folder: String) { data.keys.removeAll { it.startsWith("$folder/") } }
        }
        val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "2222222222222222"), Api.defaultHttp())
        val draft = ImageDraft("session:geometry", files, scope = null)
        val colors = listOf(0xff79abc4.toInt(), 0xffb9ca85.toInt(), 0xffd9ac84.toInt())
        draft.attach(colors.mapIndexed { index, color ->
            PendingImage("image-$index", null).apply {
                val bitmap = Bitmap.createBitmap(120, 90, Bitmap.Config.ARGB_8888).apply {
                    eraseColor(color)
                    android.graphics.Canvas(this).drawCircle(85f, 26f, 13f, android.graphics.Paint().apply { this.color = android.graphics.Color.WHITE })
                }
                thumb = bitmap.asImageBitmap()
                val bytes = java.io.ByteArrayOutputStream().apply { bitmap.compress(Bitmap.CompressFormat.PNG, 100, this) }.toByteArray()
                processed = Processed(bytes, io.github.lewin671.remoteagent.proto.ImageFormat.IMAGE_FORMAT_PNG, 120, 90)
                state = PendingImage.State.Ready
            }
        }, 9)
        try {
            ui.setContent {
                RaTheme(ThemeMode.LIGHT) {
                    Column(Modifier.fillMaxSize().background(Color(0xfff6f4ef)).padding(16.dp)) {
                        Text("草稿图片 · 点击区域验收")
                        Spacer(Modifier.height(24.dp))
                        ImageStrip(draft, remote, null, 9)
                        ComposeBox("查看这些图片", {}, "消息", false, {}, {})
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
            ui.runOnIdle { assertEquals(3, draft.pictures.size) }
            // Just beyond the target's lower-left corner: preview, not removal.
            first.performTouchInput { click(androidx.compose.ui.geometry.Offset(41.dp.toPx(), 21.dp.toPx())) }
            ui.onNodeWithContentDescription("关闭").performClick()
            ui.runOnIdle { assertEquals(3, draft.pictures.size) }
            remove.performTouchInput { click(androidx.compose.ui.geometry.Offset(33.dp.toPx(), 11.dp.toPx())) }
            ui.runOnIdle { assertEquals(2, draft.pictures.size) }
        } finally { remote.close() }
    }
}
