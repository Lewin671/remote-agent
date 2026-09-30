package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
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
            SectionHeader("电脑 · 运行 CLI")
            Card(Modifier.padding(horizontal = 12.dp)) {
                Column {
                    val ms = state.machines.values.sortedBy { machineLabel(state, it.id) }
                    if (ms.isEmpty()) Text("还没有电脑", Modifier.padding(16.dp), color = p.muted)
                    ms.forEachIndexed { i, m ->
                        if (i > 0) HorizontalDivider(Modifier.padding(start = 16.dp), color = p.line)
                        Row44(onClick = { nav.device(m.id) }) {
                            Text(machineLabel(state, m.id), Modifier.weight(1f), style = MaterialTheme.typography.titleSmall)
                            Text(if (m.online) "在线" else whenText(m.lastSeenMs, t).ifEmpty { "不在线" }, color = if (m.online) p.greenInk else p.muted, style = MaterialTheme.typography.bodySmall)
                            Icon(Icons.AutoMirrored.Filled.KeyboardArrowRight, null, tint = p.faint)
                        }
                    }
                }
            }
            SectionHeader("客户端 · 操作会话")
            Card(Modifier.padding(horizontal = 12.dp)) {
                Column {
                    if (!state.devicesSupported) {
                        Text("服务器还不支持设备目录，更新服务器后才能看到其他客户端", Modifier.padding(16.dp), color = p.muted, style = MaterialTheme.typography.bodySmall)
                    } else {
                        val clients = state.devices.values.filter { it.kind == DeviceKind.DEVICE_KIND_CLIENT }.sortedWith(compareBy({ it.id != me }, { it.createdMs }))
                        clients.forEachIndexed { i, c ->
                            if (i > 0) HorizontalDivider(Modifier.padding(start = 16.dp), color = p.line)
                            Row44(onClick = { nav.client(c.id) }) {
                                Text(clientName(c, c.id), Modifier.weight(1f), style = MaterialTheme.typography.titleSmall)
                                Text(if (c.id == me) "当前客户端" else "已配对", color = p.muted, style = MaterialTheme.typography.bodySmall)
                                Icon(Icons.AutoMirrored.Filled.KeyboardArrowRight, null, tint = p.faint)
                            }
                        }
                    }
                }
            }
            Text(
                "客户端名称在设备详情中设置；未命名时显示类型和 ID 短码。已配对不表示现在正在查看。",
                Modifier.padding(16.dp), style = MaterialTheme.typography.bodySmall, color = p.muted,
            )
            Card(Modifier.padding(horizontal = 12.dp), onClick = { nav.pair() }) { Row44 { Text("＋ 添加电脑或客户端", color = p.blue) } }
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
    PageScaffold(clientName(d, id)) {
        Column(Modifier.weight(1f).verticalScroll(rememberScrollState())) {
            Text(if (id == me) "当前客户端" else "已配对的客户端", Modifier.padding(horizontal = 16.dp), color = p.muted)
            SectionHeader("客户端")
            Card(Modifier.padding(horizontal = 12.dp)) {
                Column {
                    Row44(onClick = if (state.conn == ConnState.ONLINE) ({ editing = true }) else null) {
                        Text("名称", Modifier.weight(1f))
                        Text(d?.meta?.name?.ifEmpty { null } ?: "未命名", color = p.muted)
                        Icon(Icons.AutoMirrored.Filled.KeyboardArrowRight, null, tint = p.faint)
                    }
                    HorizontalDivider(Modifier.padding(start = 16.dp), color = p.line)
                    Row44 {
                        Text("设备 ID", Modifier.weight(1f))
                        Text("…" + shortId(id), color = p.muted, fontFamily = FontFamily.Monospace)
                    }
                    d?.meta?.description?.takeIf { it.isNotEmpty() }?.let {
                        HorizontalDivider(Modifier.padding(start = 16.dp), color = p.line)
                        Row44 { Text("系统", Modifier.weight(1f)); Text(it, color = p.muted) }
                    }
                }
            }
            if (save.error.isNotEmpty()) Text("没能保存名称：${save.error}", Modifier.padding(16.dp), color = p.red)
            Text(
                "修改名称同步到其他客户端。通知开关要在这台手机上设置，不在这里远程修改。",
                Modifier.padding(16.dp), style = MaterialTheme.typography.bodySmall, color = p.muted,
            )
            if (id == me) {
                TextButton(onClick = { nav.settings() }, Modifier.padding(horizontal = 8.dp)) { Text("退出并删除本机密钥在设置页", color = p.muted) }
            } else {
                TextButton(onClick = { confirm = true }, Modifier.padding(horizontal = 8.dp), enabled = state.conn == ConnState.ONLINE) { Text("移除这个客户端…", color = p.red) }
                Text(
                    "移除会断开它的登录并清除推送订阅，不结束电脑上的会话，不删除会话历史。持有账户密钥的设备仍可重新加入。",
                    Modifier.padding(horizontal = 16.dp), style = MaterialTheme.typography.bodySmall, color = p.muted,
                )
                if (remove.error.isNotEmpty()) Text(remove.error, Modifier.padding(16.dp), color = p.red)
            }
        }
    }
    if (editing) {
        var name by remember { mutableStateOf(d?.meta?.name.orEmpty()) }
        AlertDialog(
            onDismissRequest = { editing = false },
            title = { Text("客户端名称") },
            text = { OutlinedTextField(name, { name = it.take(40) }, singleLine = true, placeholder = { Text("如：我的手机") }) },
            confirmButton = {
                TextButton(onClick = {
                    editing = false
                    save.run({ remote.renameDevice(id, name.trim()); null })
                }) { Text("保存") }
            },
            dismissButton = { TextButton(onClick = { editing = false }) { Text("取消") } },
        )
    }
    if (confirm) Confirm(
        "移除 ${clientName(d, id)}？",
        "它的登录和推送订阅会被清除，电脑上的会话和历史不受影响。它持有账户密钥时仍可重新加入。",
        "移除", danger = true, onDismiss = { confirm = false },
    ) { remove.run({ remote.removeDevice(id); null }) { nav.back() } }
}
