package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.layout.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.data.ThemeMode
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test

class FilesLayoutTest {
    @get:Rule val ui = createComposeRule()

    @Test fun chipsAndMenuAtTwoHundredPercentLight() = chips(false)
    @Test fun chipsAndMenuAtTwoHundredPercentDark() = chips(true)

    private fun chips(dark: Boolean) {
        val name = "季度资料-非常长的文件名称-包含补充说明和修订记录.zip"
        var picturePicks = 0
        var filePicks = 0
        var removals = 0
        ui.setContent {
            val density = LocalDensity.current
            RaTheme(if (dark) ThemeMode.DARK else ThemeMode.LIGHT) {
                CompositionLocalProvider(LocalDensity provides Density(density.density, 2f)) {
                    Column(Modifier.width(320.dp)) {
                        FileChip(name, 1024, PendingImage.State.Uploading(.4f), onRemove = { removals++ })
                        FileChip("data.csv", 0, PendingImage.State.Failed("无法读取源文件，请重新选择文件"), onRetry = {}, onRemove = {})
                        ComposeBox("", {}, "消息", false, {}, { picturePicks++ }, onFile = { filePicks++ })
                    }
                }
            }
        }
        val nameNode = ui.onNodeWithContentDescription(name, useUnmergedTree = true)
        nameNode.assertIsDisplayed()
        val remove = ui.onNodeWithContentDescription("移除文件 $name", useUnmergedTree = true)
        remove.assertIsDisplayed()
        remove.assertWidthIsAtLeast(44.dp).assertHeightIsAtLeast(44.dp)
        remove.performClick()
        ui.runOnIdle { assertEquals(1, removals) }
        ui.onNodeWithContentDescription("添加附件").performClick()
        ui.onNodeWithText("图片", useUnmergedTree = true).assertIsDisplayed()
        ui.onNodeWithText("文件", useUnmergedTree = true).performClick()
        ui.runOnIdle { assertEquals(1, filePicks); assertEquals(0, picturePicks) }
    }

    @Test fun filesOnlyHistoryHasChipsWithoutAnEmptyBubble() {
        val file = io.github.lewin671.remoteagent.proto.SentFile.newBuilder().setName("data.csv").setSizeBytes(3).build()
        ui.setContent { RaTheme(ThemeMode.LIGHT) { ChatItemView(ChatItem.User("u", "", false, 0, files = listOf(file)), "Codex") } }
        ui.onNodeWithText("data.csv").assertIsDisplayed()
        ui.onAllNodes(hasText("", substring = false), useUnmergedTree = true).assertCountEquals(0)
    }

    @Test fun aMachineWithoutFilesKeepsTheDirectPhotoAction() {
        var picks = 0
        ui.setContent { RaTheme(ThemeMode.LIGHT) { ComposeBox("", {}, "消息", false, {}, { picks++ }) } }
        ui.onNodeWithContentDescription("添加图片").performClick()
        ui.runOnIdle { assertEquals(1, picks) }
        ui.onNodeWithText("文件").assertDoesNotExist()
    }
}
