package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.PendingInput
import io.github.lewin671.remoteagent.core.CommandException
import io.github.lewin671.remoteagent.proto.*
import org.junit.Assert.*
import org.junit.Test

class DeliveryTest {
    private val pending = PendingInput("r", "hello", SendMode.SEND_MODE_STEER, false, false, null)

    @Test fun pendingWordsStayShortAndKeepActionProgress() {
        fun label(p: PendingInput = pending, starting: String? = null, cancel: Cancels.State? = null, resuming: Boolean = false, failed: Boolean = false) = pendingInputLabel(p, starting, cancel, resuming, failed)
        assertEquals("插入这一轮", label())
        assertEquals("插入这一轮", label(pending.copy(held = true)))
        assertEquals("这一轮结束后发", label(pending.copy(mode = SendMode.SEND_MODE_QUEUE)))
        assertEquals("等 Codex 就绪后发出", label(starting = "Codex"))
        assertEquals("已暂停", label(pending.copy(paused = true)))
        assertEquals("正在取消…", label(cancel = Cancels.State.Canceling))
        assertEquals("正在发送…", label(resuming = true))
        assertEquals("发送没成功，仍已暂停", label(failed = true))
        assertEquals("已暂停", pendingInputCaption("已暂停", 1, 2, "本客户端", false))
        assertEquals("已暂停", pendingInputCaption("已暂停", 1, 1, "本客户端"))
        assertEquals("插入这一轮 · 第 2 条 · Android · Pixel 9", pendingInputCaption("插入这一轮", 2, 4, "Android · Pixel 9"))
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
        outgoing.deliver { throw CommandException(CommandException.UNAVAILABLE, "电脑不在线，指令没有发出") }
        assertEquals(Outgoing.State.UNKNOWN, outgoing.state)
        val definite = Outgoing("d", "hello")
        definite.deliver { throw CommandException(CommandException.UNAVAILABLE, "电脑不在线，指令没有发出") }
        assertEquals(SendFeedback("没发出去：电脑不在线", false), deliveryFailure(definite.error, definite.state == Outgoing.State.UNKNOWN))
        assertEquals("可能没发出去：连接中途断了", deliveryFailure("不确定电脑有没有执行：连接中途断了", true).text)
        assertEquals("可能没发出去：回复验证失败", deliveryFailure("电脑的回复打不开，不确定指令有没有执行", true).text)
    }
}
