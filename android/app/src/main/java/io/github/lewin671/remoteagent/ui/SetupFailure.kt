package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.layout.Column
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.*
import io.github.lewin671.remoteagent.RaApp
import kotlinx.coroutines.launch
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.fillMaxWidth

@Composable
fun AccountLoadFailure(message: String, retry: () -> Unit, discard: () -> Unit) {
    Column(Modifier.padding(24.dp)) {
        Text(message, style = T.s17, color = pal.ink)
        PillButton("重试", retry, Modifier.fillMaxWidth().padding(top = 16.dp), ButtonKind.INK)
        DiscardSetupAction(discard)
    }
}

@Composable
fun DiscardSetupAction(discard: () -> Unit) {
    var asking by remember { mutableStateOf(false) }
    TextLink("退出并重新开始", { asking = true }, Modifier.fillMaxWidth(), danger = true)
    if (asking)
        Confirm(
            "退出并删除本机密钥？",
            "本机账户、未完成的注册和安全操作会被清除，需要重新配对或创建。服务器上的会话和档案不会被删除；结果未明的操作可能已经成功，请在管理端核实。",
            "确认退出",
            danger = true,
            onDismiss = { asking = false },
        ) {
            asking = false
            discard()
        }
}

@Composable
fun DiscardSetup(app: RaApp, onDiscarded: () -> Unit = {}) {
    val scope = rememberCoroutineScope()
    DiscardSetupAction {
        scope.launch {
            app.logout()
            onDiscarded()
        }
    }
}
