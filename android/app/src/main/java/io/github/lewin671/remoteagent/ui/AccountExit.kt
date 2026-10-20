package io.github.lewin671.remoteagent.ui

import androidx.compose.runtime.*
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.errorText
import kotlinx.coroutines.launch

internal fun authorityExitAdvice(authority: Boolean, unresolved: Boolean): String =
    when {
        unresolved -> "安全操作结果尚未核实，请先继续原操作；退出会删除唯一完成材料。"
        authority -> "本机是唯一管理端，建议先交接；退出会删除管理密钥，没有恢复码备份将丢失管理权。"
        else -> "退出会删除本机密钥，需要重新配对。"
    }

/** A failed read must never turn an unresolved operation into an ordinary logout. */
@Composable
internal fun AccountExitAction(
    state: RemoteState,
    readPending: suspend () -> Boolean,
    exit: suspend () -> Unit,
) {
    val scope = rememberCoroutineScope()
    var advice by remember { mutableStateOf<String?>(null) }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    GRow(
        "退出并删除本机密钥",
        onClick = {
            busy = true
            error = ""
            scope.launch {
                try {
                    advice = authorityExitAdvice(state.isAuthority, readPending())
                } catch (e: Exception) {
                    error = "退出前没能读取安全操作状态：${errorText(e, state)}"
                } finally {
                    busy = false
                }
            }
        },
        danger = true,
        enabled = !busy,
    )
    if (error.isNotEmpty()) Banner(error, tone = Tone2.ERROR)
    advice?.let { message ->
        Confirm(
            "退出并删除本机密钥？",
            message,
            "退出",
            danger = true,
            onDismiss = { if (!busy) advice = null },
        ) {
            if (!busy) {
                busy = true
                scope.launch {
                    try {
                        // A publication may have started while the confirmation was open.
                        val current = authorityExitAdvice(state.isAuthority, readPending())
                        if (current != message) advice = current
                        else {
                            exit()
                            advice = null
                        }
                    } catch (e: Exception) {
                        advice = null
                        error = "退出未完成：${errorText(e, state)}"
                    } finally {
                        busy = false
                    }
                }
            }
        }
    }
}
