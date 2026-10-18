package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.clickable
import androidx.compose.ui.Alignment
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.clientDetail
import io.github.lewin671.remoteagent.core.clientName
import io.github.lewin671.remoteagent.core.clientUsed
import io.github.lewin671.remoteagent.core.dayText
import io.github.lewin671.remoteagent.core.lastUsed
import io.github.lewin671.remoteagent.core.sortClients
import io.github.lewin671.remoteagent.core.defaultClientName
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
    // The times on this page are as of opening it.
    LaunchedEffect(Unit) { if (remote.state.value.conn == ConnState.ONLINE) remote.refreshDevices() }
    PageScaffold("设备") {
        Column(Modifier.weight(1f).verticalScroll(rememberScrollState())) {
            GLabel("电脑 · 运行 CLI")
            val ms = state.machines.values.sortedBy { machineLabel(state, it.id) }
            if (ms.isEmpty()) Hint("还没有电脑", Modifier.padding(horizontal = 30.dp).padding(bottom = 12.dp))
            else Group(rows = ms.map { m -> @Composable {
                GRow(machineLabel(state, m.id), onClick = { nav.device(m.id) }, trailing = {
                    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(5.dp)) {
                        if (m.online) Dot(p.green)
                        ArrowText(if (m.online) "在线" else whenText(m.lastSeenMs, t).ifEmpty { "不在线" }, p.muted, T.sm)
                    }
                })
            } })
            GLabel("客户端 · 操作会话")
            val clients = sortClients(state.devices.values.filter { it.kind == DeviceKind.DEVICE_KIND_CLIENT }, me)
            Group(rows = clients.map { c -> @Composable {
                Row(
                    Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable { nav.client(c.id) }.padding(horizontal = 14.dp, vertical = 8.dp),
                    verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp),
                ) {
                    Column(Modifier.weight(1f)) {
                        Text(clientName(c, c.id), style = T.body, color = p.fg)
                        Text(clientDetail(c), style = T.sm, color = p.muted, modifier = Modifier.padding(top = 2.dp))
                    }
                    if (c.id == me) Text("当前客户端", style = T.sm, color = p.muted) else ArrowText(clientUsed(c, t), p.muted, T.sm)
                }
            } })
            Text("右边是上次使用的时间。很久没用的客户端可以点进去移除，名称也在里面改。", style = T.sm, color = p.muted, modifier = Modifier.padding(horizontal = 16.dp))
            Box(Modifier.padding(horizontal = 16.dp, vertical = 8.dp).heightIn(min = 44.dp).clickable { nav.pair() }, contentAlignment = Alignment.CenterStart) {
                IconText(Lucide.Plus, "添加电脑或客户端", p.blue, T.body)
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
    val t = now()
    var editing by remember { mutableStateOf(false) }
    var confirm by remember { mutableStateOf(false) }
    val save = rememberAction()
    val remove = rememberAction()
    PageScaffold(clientName(d, id), subtitle = if (id == me) "当前客户端" else "已配对的客户端") {
        Column(Modifier.weight(1f).verticalScroll(rememberScrollState())) {
            GLabel("客户端")
            Group(rows = listOfNotNull<@Composable () -> Unit>(
                { GRow("名称", (d?.meta?.name?.ifEmpty { null } ?: "未命名"), onClick = if (state.conn == ConnState.ONLINE) ({ editing = true }) else null, chevron = true) },
                d?.meta?.name?.takeIf { it.isNotEmpty() }?.let { { GRow("恢复默认名称", defaultClientName(d, id), onClick = { save.run({ remote.renameDevice(id, ""); null }) }, action = true, enabled = state.conn == ConnState.ONLINE) } },
                d?.meta?.description?.takeIf { it.isNotEmpty() }?.let { desc -> { GRow("运行在", desc) } },
                { GRow("上次使用", if (id == me) "现在" else lastUsed(d, t).ifEmpty { "没有记录" }) },
                d?.createdMs?.takeIf { it > 0 }?.let { created -> { GRow("添加于", dayText(created, t)) } },
                { GRow("设备 ID", trailing = { Text("…" + shortId(id), style = T.mono13, color = p.muted) }) },
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
                Text("移除会断开它的登录并清除推送订阅，不结束电脑上的会话，不删除会话历史。再次加入需要另一台已登记设备批准。", style = T.sm, color = p.muted, modifier = Modifier.padding(horizontal = 16.dp))
                if (remove.error.isNotEmpty()) Banner(remove.error, Modifier.padding(16.dp), Tone2.ERROR)
            }
        }
    }
    if (editing) InputAlert("客户端名称", d?.meta?.name.orEmpty(), defaultClientName(d, id), "保存", onDismiss = { editing = false }) { name ->
        save.run({ remote.renameDevice(id, name.trim()); null })
    }
    if (confirm) Confirm(
        "移除 ${clientName(d, id)}？",
        "它的登录和推送订阅会被清除，电脑上的会话和历史不受影响。它持有账户密钥时仍可重新加入。",
        "移除", danger = true, onDismiss = { confirm = false },
    ) { remove.run({ remote.removeDevice(id); null }) { nav.back() } }
}
