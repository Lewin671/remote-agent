package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.PendingInput
import io.github.lewin671.remoteagent.core.failureText
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.InputStatus
import io.github.lewin671.remoteagent.proto.SendMode

internal data class SendFeedback(val text: String, val uncertain: Boolean)

internal fun deliveryFailure(reason: String, uncertain: Boolean): SendFeedback =
    SendFeedback((if (uncertain) "可能没发出去：" else "没发出去：") + shortSendReason(reason), uncertain)

private fun shortSendReason(reason: String): String = when {
    reason.contains("回复打不开") || reason.contains("可验证的执行结果") -> "回复验证失败"
    reason.contains("连接中途断了") -> "连接中途断了"
    reason.contains("不确定电脑有没有执行") || reason.contains("停止等待回复") -> "结果未知"
    reason.contains("图片不在电脑上") -> "图片不在电脑上"
    reason.contains("电脑不在线") -> "电脑不在线"
    else -> reason.trim().ifEmpty { "原因未知" }
}

internal fun inputFeedback(status: InputStatus, reason: String, agent: Agent): SendFeedback =
    if (status == InputStatus.INPUT_STATUS_UNCONFIRMED) deliveryFailure("已送到终端但没确认", true)
    else deliveryFailure(failureText(reason, agent), status == InputStatus.INPUT_STATUS_NEEDS_TERMINAL)

internal fun pendingInputLabel(
    pending: PendingInput,
    starting: String?,
    cancel: Cancels.State?,
    resuming: Boolean,
    resumeFailed: Boolean,
): String = when {
    cancel == Cancels.State.Canceling -> "正在取消…"
    cancel is Cancels.State.Failed -> "取消没成功，仍在排队"
    resuming -> "正在发送…"
    resumeFailed -> "发送没成功，仍已暂停"
    pending.paused -> "已暂停"
    pending.held -> "插入这一轮"
    pending.mode == SendMode.SEND_MODE_QUEUE -> "这一轮结束后发"
    starting != null -> "等 $starting 就绪后发出"
    else -> "插入这一轮"
}

internal fun pendingInputCaption(label: String, index: Int, total: Int, origin: String?): String =
    label + (if (total > 1) " · 第 $index 条" else "") +
        (if (origin != null && origin != "本客户端") " · $origin" else "")
