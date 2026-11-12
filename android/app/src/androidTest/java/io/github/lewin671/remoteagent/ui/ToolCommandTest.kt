package io.github.lewin671.remoteagent.ui

import android.content.ClipboardManager
import androidx.compose.foundation.layout.Column
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTouchInput
import androidx.test.platform.app.InstrumentationRegistry
import io.github.lewin671.remoteagent.proto.ToolCall
import io.github.lewin671.remoteagent.proto.ToolKind
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test

class ToolCommandTest {
    @get:Rule val ui = createComposeRule()
    private val raw = "cat <<'EOF'\n/work/full/path\nEOF\n"

    @Test fun expandsSelectsCopiesAndCollapses() {
        ui.setContent { RaTheme(LightPalette) { ExpandableTool("one", raw) { ToolLine(Lucide.Check, pal.ink3, "preview") } } }
        ui.onNodeWithText("preview").performClick()
        ui.onNodeWithText(raw).assertIsDisplayed().performTouchInput { longClick(center) }
        ui.onNodeWithText(raw).assertIsDisplayed()
        // Copy is a separate target and must leave the block open.
        ui.onNodeWithContentDescription("复制代码").performClick()
        ui.onNodeWithText(raw).assertIsDisplayed()
        val clipboard = InstrumentationRegistry.getInstrumentation().targetContext.getSystemService(ClipboardManager::class.java)
        ui.runOnIdle { assertEquals(raw, clipboard.primaryClip!!.getItemAt(0).text.toString()) }
        ui.onNodeWithText("preview").performClick()
        ui.onNodeWithText(raw).assertDoesNotExist()
    }

    @Test fun groupCommandsKeepIndependentExpansionWhenGroupReopens() {
        val calls = listOf("one", "two").map { name -> ToolCall.newBuilder().setCallId(name).setKind(ToolKind.TOOL_KIND_COMMAND).setTitle("echo $name\n/work/$name").build() }
        ui.setContent { RaTheme(LightPalette) { Column { ChatItemView(ChatItem.Tools("group", calls), "codex", "/work") } } }
        ui.onNodeWithText("运行了 2 条命令").performClick()
        ui.onNodeWithText("echo one").performClick()
        ui.onNodeWithText(calls[0].title).assertIsDisplayed()
        ui.onNodeWithText(calls[1].title).assertDoesNotExist()
        ui.onNodeWithText("运行了 2 条命令").performClick()
        ui.onNodeWithText("运行了 2 条命令").performClick()
        ui.onNodeWithText(calls[0].title).assertIsDisplayed()
    }
}
