package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.PendingInput
import io.github.lewin671.remoteagent.core.CommandException
import io.github.lewin671.remoteagent.proto.*
import org.junit.Assert.*
import org.junit.Test

@org.junit.runner.RunWith(org.robolectric.RobolectricTestRunner::class)
@org.robolectric.annotation.Config(sdk = [35], application = android.app.Application::class)
class DeliveryTest {
    @get:org.junit.Rule(order = 0) val sessionMessages = io.github.lewin671.remoteagent.ui.SessionMessageContextRule()
    private val pending = PendingInput("r", "hello", SendMode.SEND_MODE_STEER, false, false, null)

    @Test fun pendingWordsStayShortAndKeepActionProgress() {
        fun label(p: PendingInput = pending, starting: String? = null, cancel: Cancels.State? = null, resuming: Boolean = false, failed: Boolean = false) = pendingInputLabel(p, starting, cancel, resuming, failed)
        assertEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.queueInsert()), label())
        assertEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.queueInsert()), label(pending.copy(held = true)))
        assertEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.queueAfterTurn()), label(pending.copy(mode = SendMode.SEND_MODE_QUEUE)))
        assertEquals("等 Codex 就绪后发出", label(starting = "Codex"))
        assertEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.queuePaused()), label(pending.copy(paused = true)))
        assertEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.queueCanceling()), label(cancel = Cancels.State.Canceling))
        assertEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.queueResuming()), label(resuming = true))
        assertEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.queueResumeFailed()), label(failed = true))
        assertEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.queuePaused()), pendingInputCaption("已暂停", 1, 2, ChatSource("本客户端", true), false))
        assertEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.queuePaused()), pendingInputCaption("已暂停", 1, 1, ChatSource("本客户端", true)))
        assertEquals("插入这一轮 · 第 2 条 · Android · Pixel 9", pendingInputCaption("插入这一轮", 2, 4, ChatSource("Android · Pixel 9")))
    }

    @Test fun inputStatesDistinguishDefiniteAndUncertainDelivery() {
        fun feedback(status: InputStatus, reason: String = "") = inputFeedback(status, reason, Agent.AGENT_CODEX)
        assertEquals(SendFeedback("没发出去：会话已结束", false), feedback(InputStatus.INPUT_STATUS_FAILED, "exited"))
        assertEquals(SendFeedback("没发出去：Codex 没认出图片", false), feedback(InputStatus.INPUT_STATUS_FAILED, "image_not_recognized"))
        assertEquals(SendFeedback("可能没发出去：无法确认终端输入框的内容", true), feedback(InputStatus.INPUT_STATUS_NEEDS_TERMINAL, "input_changed"))
        assertEquals("没发出去：等终端就绪超时", feedback(InputStatus.INPUT_STATUS_FAILED, "timeout").text)
        assertEquals("没发出去：电脑上的 remote-agent 写入时重启了", feedback(InputStatus.INPUT_STATUS_FAILED, "restarted").text)
        assertEquals("没发出去：没有写进终端", feedback(InputStatus.INPUT_STATUS_FAILED, "").text)
        assertEquals(SendFeedback("可能没发出去：已送到终端但没确认", true), feedback(InputStatus.INPUT_STATUS_UNCONFIRMED))
    }

    @Test fun retryingAnUnknownSendDoesNotTurnItIntoADefiniteFailure() = kotlinx.coroutines.runBlocking {
        val outgoing = Outgoing("r", "hello")
        outgoing.deliver { throw CommandException(CommandException.UNKNOWN, "连接超时") }
        assertEquals(SendFeedback("可能没发出去：连接超时", true), deliveryFailure(outgoing.error, outgoing.state == Outgoing.State.UNKNOWN))
        outgoing.deliver { throw CommandException(CommandException.UNAVAILABLE, "电脑不在线，指令没有发出", failureKind = io.github.lewin671.remoteagent.core.CommandFailureKind.COMPUTER_OFFLINE) }
        assertEquals(Outgoing.State.UNKNOWN, outgoing.state)
        val definite = Outgoing("d", "hello")
        definite.deliver { throw CommandException(CommandException.UNAVAILABLE, "电脑不在线，指令没有发出", failureKind = io.github.lewin671.remoteagent.core.CommandFailureKind.COMPUTER_OFFLINE) }
        assertEquals(SendFeedback("没发出去：电脑不在线", false), deliveryFailure(definite.error, definite.state == Outgoing.State.UNKNOWN, kind = definite.failureKind))
        assertEquals("可能没发出去：连接中途断了", deliveryFailure("raw diagnostic in any language", true, kind = io.github.lewin671.remoteagent.core.CommandFailureKind.CONNECTION_DROPPED).text)
        assertEquals("可能没发出去：回复验证失败", deliveryFailure("raw diagnostic in any language", true, kind = io.github.lewin671.remoteagent.core.CommandFailureKind.REPLY_UNVERIFIED).text)
    }
}
