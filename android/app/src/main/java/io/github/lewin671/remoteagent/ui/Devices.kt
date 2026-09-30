package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.clickable
import androidx.compose.ui.Alignment
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.KeyboardArrowRight
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.clientName
import io.github.lewin671.remoteagent.core.shortId
import io.github.lewin671.remoteagent.core.whenText
import io.github.lewin671.remoteagent.proto.DeviceKind

/**
 * 设备: computers, where the CLIs run, apart from clients, which operate
 * them. A computer's online state says nothing about the clients, and a
 * paired client is not shown as watching or online.
 */
@Composable
fun DevicesScreen() {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val p = pal
    val t = now()
    val me = remote.account.deviceId
    PageScaffold("设备") {
        Column(Modifier.weight(1f).verticalScroll(rememberScrollState())) {
            GLabel("电脑 · 运行 CLI")
            val ms = state.machines.values.sortedBy { machineLabel(state, it.id) }
            if (ms.isEmpty()) Hint("还没有电脑", Modifier.padding(horizontal = 30.dp).padding(bottom = 12.dp))
            else Group(rows = ms.map { m -> @Composable {
                GRow(machineLabel(state, m.id), onClick = { nav.device(m.id) }, trailing = {
                    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(5.dp)) {
                        if (m.online) Dot(p.green)
                        Text((if (m.online) "在线" else whenText(m.lastSeenMs, t).ifEmpty { "不在线" }) + " ›", style = T.sm, color = p.muted)
                    }
                })
            } })
            GLabel("客户端 · 操作会话")
            if (!state.devicesSupported) Hint("服务器还不支持设备目录，更新服务器后才能看到其他客户端", Modifier.padding(horizontal = 30.dp))
            else {
                val clients = state.devices.values.filter { it.kind == DeviceKind.DEVICE_KIND_CLIENT }.sortedWith(compareBy({ it.id != me }, { it.createdMs }))
                Group(rows = clients.map { c -> @Composable { GRow(clientName(c, c.id), if (c.id == me) "当前客户端" else "已配对 ›", onClick = { nav.client(c.id) }) } })
            }
            Text("客户端名称在设备详情中设置；未命名时显示类型和 ID 短码。已配对不表示现在正在查看。", style = T.sm, color = p.muted, modifier = Modifier.padding(horizontal = 16.dp))
            Box(Modifier.padding(horizontal = 16.dp, vertical = 8.dp).heightIn(min = 44.dp).clickable { nav.pair() }, contentAlignment = Alignment.CenterStart) {
                Text("＋ 添加电脑或客户端", style = T.body, color = p.blue)
            }
            Spacer(Modifier.height(24.dp))
        }
    }
}

/** A client's details: its name for the whole account, its ID to compare, and removing it. */
@Composable
fun ClientScreen(id: String) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val p = pal
    val me = remote.account.deviceId
    val d = state.devices[id]
    var editing by remember { mutableStateOf(false) }
    var confirm by remember { mutableStateOf(false) }
    val save = rememberAction()
    val remove = rememberAction()
    PageScaffold(clientName(d, id), subtitle = if (id == me) "当前客户端" else "已配对的客户端") {
        Column(Modifier.weight(1f).verticalScroll(rememberScrollState())) {
            GLabel("客户端")
            Group(rows = listOfNotNull<@Composable () -> Unit>(
                { GRow("名称", (d?.meta?.name?.ifEmpty { null } ?: "未命名") + " ›", onClick = if (state.conn == ConnState.ONLINE) ({ editing = true }) else null) },
                { GRow("设备 ID", trailing = { Text("…" + shortId(id), style = T.mono13, color = p.muted) }) },
                d?.meta?.description?.takeIf { it.isNotEmpty() }?.let { desc -> { GRow("系统", desc) } },
            ))
            if (save.error.isNotEmpty()) Banner("没能保存名称：${save.error}", Modifier.padding(horizontal = 16.dp).padding(bottom = 8.dp), Tone2.ERROR)
            Text("修改名称同步到其他客户端。通知开关要在这台手机上设置，不在这里远程修改。", style = T.sm, color = p.muted, modifier = Modifier.padding(horizontal = 16.dp))
            if (id == me) {
                Box(Modifier.padding(horizontal = 16.dp, vertical = 12.dp).heightIn(min = 44.dp).clickable { nav.settings() }, contentAlignment = Alignment.CenterStart) {
                    ArrowText("退出并删除本机密钥在设置页", p.muted, T.body)
                }
            } else {
                Box(Modifier.padding(horizontal = 16.dp, vertical = 12.dp).heightIn(min = 44.dp).clickable(enabled = state.conn == ConnState.ONLINE) { confirm = true }, contentAlignment = Alignment.CenterStart) {
                    Text("移除这个客户端…", style = T.body, color = p.red)
                }
                Text("移除会断开它的登录并清除推送订阅，不结束电脑上的会话，不删除会话历史。持有账户密钥的设备仍可重新加入。", style = T.sm, color = p.muted, modifier = Modifier.padding(horizontal = 16.dp))
                if (remove.error.isNotEmpty()) Banner(remove.error, Modifier.padding(16.dp), Tone2.ERROR)
            }
        }
    }
    if (editing) InputAlert("客户端名称", d?.meta?.name.orEmpty(), "如：我的手机", "保存", onDismiss = { editing = false }) { name ->
        save.run({ remote.renameDevice(id, name.trim()); null })
    }
    if (confirm) Confirm(
        "移除 ${clientName(d, id)}？",
        "它的登录和推送订阅会被清除，电脑上的会话和历史不受影响。它持有账户密钥时仍可重新加入。",
        "移除", danger = true, onDismiss = { confirm = false },
    ) { remove.run({ remote.removeDevice(id); null }) { nav.back() } }
}
