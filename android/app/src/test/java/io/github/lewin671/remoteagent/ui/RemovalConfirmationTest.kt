package io.github.lewin671.remoteagent.ui

import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import io.github.lewin671.remoteagent.core.proto.AccountSecurityViewModel
import io.github.lewin671.remoteagent.core.proto.ClientDetailViewModel
import io.github.lewin671.remoteagent.core.proto.PendingSecurityOpKind
import io.github.lewin671.remoteagent.data.ThemeMode
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test

/** Removing a device is one confirmation that names it and says what is deleted. */
@org.junit.runner.RunWith(org.robolectric.RobolectricTestRunner::class)
@org.robolectric.annotation.Config(sdk = [35], application = android.app.Application::class)
class RemovalConfirmationTest {
    @get:Rule val batchCResources = io.github.lewin671.remoteagent.ui.SessionMessageContextRule()
    @get:Rule val ui = createComposeRule()

    @Test
    fun aClientRemovalNamesTheDeviceAndTheOneConfirmation() {
        val (title, text) = removalDialog("我的 iPad", machine = false)
        assertTrue(title.contains("我的 iPad"))
        assertTrue(text.contains("断开它的登录"))
        assertTrue(text.contains("不结束电脑上的会话"))
    }

    @Test
    fun aComputerRemovalSaysItsArchiveIsRetained() {
        val (title, text) = removalDialog("工作站", machine = true)
        assertTrue(title.contains("工作站"))
        assertTrue(text.contains("保留中继上的会话和图片"))
    }

    @Test
    fun localStateClientRemoveAndRenameDialogsCloseOnConfirm() {
        var removed = 0
        var renamedTo: String? = null
        val vm = ClientDetailViewModel.newBuilder()
            .setFound(true)
            .setDeviceId("cli-1")
            .setDisplayName("我的 iPad")
            .setDefaultName("iPad")
            .setCustomName("我的 iPad")
            .setCanRename(true)
            .setCanRemove(true)
            .build()
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                CompositionLocalProvider(
                    LocalNav provides Nav({}, {}, {}, {}, {}, {}, {}, {}, { _, _ -> }, {}, {}, {}),
                ) {
                    ClientScreenContent(
                        vm = vm,
                        onRename = { renamedTo = it },
                        onRemove = { removed++ },
                    )
                }
            }
        }
        ui.onNodeWithText("移除这个客户端").performClick()
        ui.onNodeWithText("移除 我的 iPad？").assertExists()
        ui.onNodeWithText("移除").performClick()
        assertEquals(1, removed)
        ui.onNodeWithText("移除 我的 iPad？").assertDoesNotExist()

        ui.onNodeWithText("名称").performClick()
        ui.onNodeWithText("保存").assertExists().performClick()
        assertEquals("我的 iPad", renamedTo)
        ui.onNodeWithText("保存").assertDoesNotExist()
    }

    @Test
    fun localStateAbandonSuccessionConfirmDialogClosesOnConfirm() {
        val discarded = mutableListOf<Boolean>()
        val vm = AccountSecurityViewModel.newBuilder()
            .setHasPendingOp(true)
            .setPendingKind(PendingSecurityOpKind.PENDING_SECURITY_OP_KIND_HANDOFF_OFFER)
            .setCanContinuePending(true)
            .setCanDiscardPending(true)
            .setDiscardRequiresConfirm(true)
            .build()
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                PendingSecurityOperationPage(
                    vm = vm,
                    onDiscard = { discarded += it },
                )
            }
        }
        ui.onNodeWithText("放弃此接任准备").performClick()
        ui.onNodeWithText("放弃").assertExists().performClick()
        assertEquals(listOf(true), discarded)
        ui.onNodeWithText("放弃").assertDoesNotExist()
    }
}
