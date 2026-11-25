package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.foundation.layout.Column
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import io.github.lewin671.remoteagent.core.proto.HandoffCandidateItemView
import io.github.lewin671.remoteagent.core.proto.HandoffRole
import io.github.lewin671.remoteagent.core.proto.HandoffViewModel
import io.github.lewin671.remoteagent.core.proto.Text as CoreText
import io.github.lewin671.remoteagent.data.ThemeMode
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class HandoffTest {
    @get:Rule val ui = createComposeRule()

    @Test
    fun rendersEmptyCandidateHintWhenNoneReady() {
        val vm = HandoffViewModel.newBuilder()
            .setRole(HandoffRole.HANDOFF_ROLE_OUTGOING_MANAGER)
            .setCanTransfer(true)
            .build()
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                Column { HandoffControlsContent(vm = vm) }
            }
        }
        ui.onNodeWithText("等待接任设备生成并保存恢复码").assertExists()
    }

    @Test
    fun rendersCoreHandoffCandidatesDirectlyAndDispatchesRequestTransfer() {
        var requested = ""
        val vm = HandoffViewModel.newBuilder()
            .setRole(HandoffRole.HANDOFF_ROLE_OUTGOING_MANAGER)
            .setCanTransfer(true)
            .addCandidates(
                HandoffCandidateItemView.newBuilder()
                    .setDeviceId("d-1")
                    .setDisplayName(CoreText.newBuilder().setLiteral("Chrome · macOS · a1b2")),
            )
            .addCandidates(
                HandoffCandidateItemView.newBuilder()
                    .setDeviceId("d-2")
                    .setDisplayName(
                        CoreText.newBuilder()
                            .setSeparatorLiteral(" · ")
                            .addJoin(CoreText.newBuilder().setKey("security.browser"))
                            .addJoin(CoreText.newBuilder().setLiteral("c3d4")),
                    ),
            )
            .build()
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                Column {
                    HandoffControlsContent(
                        vm = vm,
                        onRequestTransfer = { requested = it },
                    )
                }
            }
        }
        ui.onNodeWithText("交接给 Chrome · macOS · a1b2").assertExists().performClick()
        assertEquals("d-1", requested)
        ui.onNodeWithText("交接给 浏览器 · c3d4").assertExists()
    }

    @Test
    fun confirmingHandoffDispatchesConfirmWithoutCancel() {
        val events = mutableListOf<String>()
        val vm = HandoffViewModel.newBuilder()
            .setRole(HandoffRole.HANDOFF_ROLE_OUTGOING_MANAGER)
            .setCanTransfer(true)
            .setConfirmingTargetId("d-1")
            .addCandidates(
                HandoffCandidateItemView.newBuilder()
                    .setDeviceId("d-1")
                    .setDisplayName(CoreText.newBuilder().setLiteral("Chrome · macOS · a1b2")),
            )
            .build()
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                Column {
                    HandoffControlsContent(
                        vm = vm,
                        onCancelTransfer = { events += "cancel" },
                        onConfirmTransfer = { events += "confirm:$it" },
                    )
                }
            }
        }
        ui.onNodeWithText("确认交接").assertExists().performClick()
        assertEquals(listOf("confirm:d-1"), events)
    }

    @Test
    fun cancellingHandoffDispatchesOnlyCancel() {
        val events = mutableListOf<String>()
        val vm = HandoffViewModel.newBuilder()
            .setRole(HandoffRole.HANDOFF_ROLE_OUTGOING_MANAGER)
            .setCanTransfer(true)
            .setConfirmingTargetId("d-1")
            .build()
        ui.setContent {
            RaTheme(ThemeMode.LIGHT) {
                Column {
                    HandoffControlsContent(
                        vm = vm,
                        onCancelTransfer = { events += "cancel" },
                        onConfirmTransfer = { events += "confirm:$it" },
                    )
                }
            }
        }
        ui.onNodeWithText("取消").assertExists().performClick()
        assertEquals(listOf("cancel"), events)
    }
}
