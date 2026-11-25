package io.github.lewin671.remoteagent.ui

import android.graphics.Bitmap
import android.view.KeyEvent
import androidx.compose.foundation.ScrollState
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.verticalScroll
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performSemanticsAction
import androidx.compose.ui.unit.dp
import androidx.test.platform.app.InstrumentationRegistry
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.SessionSharedFileItem
import io.github.lewin671.remoteagent.core.proto.SharedFileCardStatus
import io.github.lewin671.remoteagent.core.proto.SharedFileKind
import io.github.lewin671.remoteagent.core.proto.SharedFileMenuAction
import io.github.lewin671.remoteagent.core.proto.SharedFileMenuItemView
import io.github.lewin671.remoteagent.core.proto.SharedFilePrimaryAction
import io.github.lewin671.remoteagent.core.proto.Text as CoreText
import io.github.lewin671.remoteagent.data.SharedFileManager
import java.io.File
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test

/** Offline debug fixtures: real downloaded cards, host routing, dialogs and gestures, no account. */
class SharedTextViewerOnDeviceTest {
    @get:Rule val ui = createComposeRule()
    private val instrumentation = InstrumentationRegistry.getInstrumentation()
    private val manager = SharedFileManager(instrumentation.targetContext) { null }
    private var dark by mutableStateOf(false)
    private val chatScroll = ScrollState(240)

    @After fun cleanup() {
        kotlinx.coroutines.runBlocking { manager.clearAll() }
        manager.close()
    }

    private fun show(name: String, content: String) {
        val file = SessionSharedFileItem.newBuilder()
            .setFileId("text-preview")
            .setDisplayName(name)
            .setSizeBytes(content.toByteArray().size.toLong())
            .setKind(SharedFileKind.SHARED_FILE_KIND_DOC)
            .setStatus(SharedFileCardStatus.SHARED_FILE_CARD_STATUS_DOWNLOADED)
            .setPrimaryAction(SharedFilePrimaryAction.SHARED_FILE_PRIMARY_ACTION_OPEN)
            .setPrimaryEnabled(true)
            .addMenuItems(
                SharedFileMenuItemView.newBuilder()
                    .setAction(SharedFileMenuAction.SHARED_FILE_MENU_ACTION_SAVE)
                    .setLabel(CoreText.newBuilder().setKey(UiMessages.sharedFileMenuSaveToDevice().key))
                    .setEnabled(true),
            )
            .addMenuItems(
                SharedFileMenuItemView.newBuilder()
                    .setAction(SharedFileMenuAction.SHARED_FILE_MENU_ACTION_OPEN_WITH)
                    .setLabel(CoreText.newBuilder().setKey(UiMessages.sharedFileMenuOpenWith().key))
                    .setEnabled(true),
            )
            .build()
        manager.seedCommittedFileForTest("text-preview", file.fileId, file.displayName, content.toByteArray())
        ui.setContent {
            RaTheme(if (dark) DarkPalette else LightPalette) {
                SharedFileHost("text-preview", mapOf(file.fileId to file), Modifier.fillMaxSize(), manager) {
                    Column(Modifier.fillMaxSize().background(pal.paper).verticalScroll(chatScroll)) {
                        Spacer(Modifier.height(320.dp))
                        SharedFilesBlock(listOf(file), "text-preview")
                        Spacer(Modifier.height(900.dp))
                    }
                }
            }
        }
        open()
    }

    private fun open() {
        ui.onNodeWithTag(SharedFileCardTag).performClick()
        ui.waitUntil(5_000) { ui.onAllNodesWithTag("text-viewer").fetchSemanticsNodes().isNotEmpty() }
        ui.onNodeWithTag("text-viewer").assertIsDisplayed()
    }

    private fun shot(name: String) {
        ui.waitForIdle()
        // Let the platform dialog's enter transition finish before capturing the device window.
        Thread.sleep(500)
        val bitmap = checkNotNull(instrumentation.uiAutomation.takeScreenshot())
        val dir = File(instrumentation.targetContext.getExternalFilesDir(null), "text-preview-shots").apply { mkdirs() }
        File(dir, "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        bitmap.recycle()
    }

    @Test fun markdownThemesMenuAndBackPreserveChatPosition() {
        show("release-notes-for-the-text-file-preview.markdown", """
            # 文件预览
            与聊天消息相同的 **Markdown** 排版。

            - 在应用内阅读
            - 选中文字后复制

            ```go
            func greet() string {
                return "Hello, 世界"
            }
            ```

            | 文件 | 打开方式 |
            | --- | --- |
            | Markdown | 排版预览 |
            | Go / HTML | 源码文本 |

            [安全链接](https://example.com) · [不可执行](javascript:alert(1))
        """.trimIndent())
        shot("01-markdown-light")
        dark = true
        shot("02-markdown-dark")
        ui.onNodeWithContentDescription("更多").performClick()
        ui.onNodeWithTag("text-viewer-menu").assertIsDisplayed()
        shot("03-more-menu")
        instrumentation.sendKeyDownUpSync(KeyEvent.KEYCODE_BACK)
        ui.waitForIdle()
        ui.onNodeWithTag("text-viewer-menu").assertDoesNotExist()
        ui.onNodeWithContentDescription("返回").performClick()
        ui.onNodeWithTag("text-viewer").assertDoesNotExist()
        assertEquals(240, chatScroll.value)
        open()
        instrumentation.sendKeyDownUpSync(KeyEvent.KEYCODE_BACK)
        ui.waitForIdle()
        ui.onNodeWithTag("text-viewer").assertDoesNotExist()
        assertEquals(240, chatScroll.value)
    }

    @Test fun codeIsWrappedSourceText() {
        show("preview_example.py", """
            from pathlib import Path

            def preview(path: Path) -> str:
                # Read source without executing it.
                data = path.read_bytes()
                if len(data) > 1024 * 1024:
                    raise ValueError("too large")
                return data.decode("utf-8")

            message = "A deliberately long source line wraps automatically inside the preview, including 中文文字, without horizontal scrolling."
            print(message)
        """.trimIndent())
        shot("04-code")
    }

    @Test fun longFileReachesItsLastLine() {
        show("long-log-file.txt", (1..250).joinToString("\n") { "%03d  Text preview — scrolling and selection.".format(it) } + "\nEND OF FILE · 已到末尾")
        ui.onNodeWithTag("text-viewer-body").performSemanticsAction(SemanticsActions.ScrollBy) { it(0f, 1_000_000f) }
        ui.waitForIdle()
        val range = ui.onNodeWithTag("text-viewer-body").fetchSemanticsNode().config[SemanticsProperties.VerticalScrollAxisRange]
        assertTrue(range.maxValue() > 0)
        assertEquals(range.maxValue(), range.value(), 1f)
        shot("05-long-file-bottom")
    }

    @Test fun htmlRemainsLiteralSource() {
        val source = "<h1>Source only</h1>\n<script>throw new Error('must never execute')</script>"
        show("source.html", source)
        ui.onNodeWithText(source).assertIsDisplayed()
    }
}
