package io.github.lewin671.remoteagent.ui

import android.content.Intent
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import androidx.core.content.FileProvider
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.DeviceSecurity
import io.github.lewin671.remoteagent.core.clientDetail
import io.github.lewin671.remoteagent.core.clientName
import io.github.lewin671.remoteagent.core.clientUsed
import io.github.lewin671.remoteagent.core.dayText
import io.github.lewin671.remoteagent.core.defaultClientName
import io.github.lewin671.remoteagent.core.lastUsed
import io.github.lewin671.remoteagent.core.shortId
import io.github.lewin671.remoteagent.core.sortClients
import io.github.lewin671.remoteagent.core.whenText
import io.github.lewin671.remoteagent.proto.DeviceKind
import java.io.File
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/**
 * 设备: computers, where the CLIs run, apart from clients, which operate them. A computer's online
 * state says nothing about the clients, and a paired client is not shown as watching or online.
 */
@Composable
fun DevicesScreen() {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val p = pal
    val t = now()
    val me = remote.account.deviceId
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    // The times on this page are as of opening it.
    LaunchedEffect(Unit) {
        if (remote.state.value.conn == ConnState.ONLINE) remote.refreshDevices()
    }
    PageScaffold("设备") {
        Column(Modifier.weight(1f).verticalScroll(rememberScrollState())) {
            if (state.clockWarning)
                Banner(
                    "手机时钟比服务器快，远程控制暂停。请在系统设置里校准时间。",
                    Modifier.padding(horizontal = 16.dp, vertical = 8.dp),
                    Tone2.WARN,
                )
            if (state.isAuthority && state.recoveryPending)
                Banner(
                    "有人正在用恢复码申请接管管理权。不是你本人就点了否决。",
                    Modifier.padding(horizontal = 16.dp, vertical = 8.dp),
                    Tone2.ERROR,
                    action = "查看",
                    onAction = { nav.recovery() },
                )
            GLabel("电脑 · 运行 CLI")
            val ms = state.machines.values.sortedBy { machineLabel(state, it.id) }
            if (ms.isEmpty())
                Hint("还没有电脑", Modifier.padding(horizontal = 30.dp).padding(bottom = 12.dp))
            else
                Group(
                    rows =
                        ms.map { m ->
                            @Composable {
                                GRow(
                                    machineLabel(state, m.id),
                                    onClick = { nav.device(m.id) },
                                    trailing = {
                                        Row(
                                            verticalAlignment = Alignment.CenterVertically,
                                            horizontalArrangement = Arrangement.spacedBy(5.dp),
                                        ) {
                                            if (m.online) Dot(p.green)
                                            ArrowText(
                                                (if (m.online) "在线"
                                                else
                                                    whenText(m.lastSeenMs, t).ifEmpty {
                                                        "不在线"
                                                    }),
                                                p.muted,
                                                T.sm,
                                            )
                                        }
                                    },
                                )
                            }
                        },
                )
            GLabel("客户端 · 操作会话")
            val clients =
                sortClients(
                    state.devices.values.filter { it.kind == DeviceKind.DEVICE_KIND_CLIENT },
                    me,
                )
            Group(
                rows =
                    clients.map { c ->
                        @Composable {
                            Row(
                                Modifier.fillMaxWidth()
                                    .heightIn(min = 44.dp)
                                    .clickable { nav.client(c.id) }
                                    .padding(horizontal = 14.dp, vertical = 8.dp),
                                verticalAlignment = Alignment.CenterVertically,
                                horizontalArrangement = Arrangement.spacedBy(10.dp),
                            ) {
                                Column(Modifier.weight(1f)) {
                                    Text(clientName(c, c.id, state.devices.values), style = T.body, color = p.fg)
                                    Text(
                                        clientDetail(c),
                                        style = T.sm,
                                        color = p.muted,
                                        modifier = Modifier.padding(top = 2.dp),
                                    )
                                }
                                if (c.id == state.authorityDeviceId)
                                    Text("管理端手机", style = T.sm, color = p.muted)
                                else if (c.id == me) Text("本机", style = T.sm, color = p.muted)
                                else ArrowText(clientUsed(c, t), p.muted, T.sm)
                            }
                        }
                    },
            )
            if (state.isAuthority) {
                GLabel("远程控制")
                Hint(
                    if (state.leaseEndMs > 0)
                        "有效期至 " +
                            dayText(state.leaseEndMs, t) +
                            "。回到 App 时自动续期；过期后远程控制暂停，聊天和历史仍可查看。"
                    else "正在读取成员状态…",
                    Modifier.padding(horizontal = 16.dp),
                )
            }
            Text(
                "右边是上次使用的时间。名称在每台设备上自己改，管理端手机可以移除其他设备。",
                style = T.sm,
                color = p.muted,
                modifier = Modifier.padding(horizontal = 16.dp),
            )
            if (state.isAuthority)
                Box(
                    Modifier.padding(horizontal = 16.dp, vertical = 8.dp)
                        .heightIn(min = 44.dp)
                        .clickable { nav.pair() },
                    contentAlignment = Alignment.CenterStart,
                ) {
                    IconText(Lucide.Plus, "添加设备", p.blue, T.body)
                }
            else
                Hint(
                    ADD_DEVICE_HINT,
                    Modifier.padding(horizontal = 16.dp, vertical = 8.dp),
                )
            if (state.isAuthority) {
                Box(Modifier.padding(horizontal = 16.dp)) {
                    TextAction(
                        "导出创世状态（中继离线固定用）",
                        {
                            scope.launch {
                                val genesis =
                                    withContext(Dispatchers.IO) {
                                        DeviceSecurity.stateBytes(remote.chain().first())
                                    }
                                withContext(Dispatchers.Main) {
                                    shareGenesis(context, genesis)
                                }
                            }
                        },
                        arrow = true,
                    )
                }
                Hint(
                    "这是公开的创世状态，不含密钥；只在升级指南要求中继离线固定时使用。",
                    Modifier.padding(horizontal = 16.dp, vertical = 4.dp),
                )
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
    val canRename = id in state.devices && clientCanRename(id, me, state.conn)
    val d = state.devices[id]
    val t = now()
    var editing by remember { mutableStateOf(false) }
    var confirmRemove by remember { mutableStateOf(false) }
    val save = rememberAction()
    val remove = rememberAction(authority = true)
    PageScaffold(
        clientName(d, id, state.devices.values),
        subtitle = if (d == null) "已移除" else if (id == me) "当前客户端" else "已配对的客户端",
    ) {
        if (d == null) {
            PageNote("这个客户端已不在当前成员列表中")
            return@PageScaffold
        }
        Column(Modifier.weight(1f).verticalScroll(rememberScrollState())) {
            GLabel(if (id == state.authorityDeviceId) "管理端手机" else "客户端")
            Group(
                rows =
                    listOfNotNull<@Composable () -> Unit>(
                        {
                            GRow(
                                "名称",
                                (d?.meta?.name?.ifEmpty { null } ?: "未命名"),
                                onClick = if (canRename) ({ editing = true }) else null,
                                chevron = canRename,
                            )
                        },
                        d?.meta
                            ?.name
                            ?.takeIf { it.isNotEmpty() }
                            ?.let {
                                {
                                    GRow(
                                        "恢复默认名称",
                                        defaultClientName(d, id, state.devices.values),
                                        onClick = {
                                            save.run(
                                                {
                                                    remote.renameDevice(id, "")
                                                    null
                                                },
                                            )
                                        },
                                        action = true,
                                        enabled = canRename,
                                    )
                                }
                            },
                        d?.meta
                            ?.description
                            ?.takeIf { it.isNotEmpty() }
                            ?.let { desc -> { GRow("运行在", desc) } },
                        { GRow("上次使用", if (id == me) "现在" else lastUsed(d, t).ifEmpty { "没有记录" }) },
                        d?.createdMs
                            ?.takeIf { it > 0 }
                            ?.let { created -> { GRow("添加于", dayText(created, t)) } },
                        {
                            GRow(
                                "设备 ID",
                                trailing = {
                                    Text("…" + shortId(id), style = T.mono13, color = p.muted)
                                },
                            )
                        },
                    ),
            )
            if (save.error.isNotEmpty())
                Banner(
                    "没能保存名称：${save.error}",
                    Modifier.padding(horizontal = 16.dp).padding(bottom = 8.dp),
                    Tone2.ERROR,
                )
            Text(
                if (id == me) "修改名称同步到其他客户端。通知开关要在这台手机上设置，不在这里远程修改。"
                else "名称只能由该客户端自己修改。通知开关也要在该设备上设置。",
                style = T.sm,
                color = p.muted,
                modifier = Modifier.padding(horizontal = 16.dp),
            )
            if (id == me) {
                Box(
                    Modifier.padding(horizontal = 16.dp, vertical = 12.dp)
                        .heightIn(min = 44.dp)
                        .clickable { nav.settings() },
                    contentAlignment = Alignment.CenterStart,
                ) {
                    ArrowText("退出并删除本机密钥在设置页", p.muted, T.body)
                }
            } else if (state.isAuthority) {
                if (id == state.authorityDeviceId) {
                    Text(
                        "管理端手机不能普通移除；旧手机在线时可在“恢复与交接”里交接。",
                        style = T.sm,
                        color = p.muted,
                        modifier = Modifier.padding(horizontal = 16.dp),
                    )
                } else {
                    Box(
                        Modifier.padding(horizontal = 16.dp, vertical = 12.dp)
                            .heightIn(min = 44.dp)
                            .clickable(
                                enabled = state.conn == ConnState.ONLINE && !remove.running,
                            ) {
                                confirmRemove = true
                            },
                        contentAlignment = Alignment.CenterStart,
                    ) {
                        Text("移除这个客户端", style = T.body, color = p.red)
                    }
                    Text(
                        "移除会断开它的登录并清除推送订阅，不结束电脑上的会话，不删除会话历史。再次加入要在管理端手机上批准。",
                        style = T.sm,
                        color = p.muted,
                        modifier = Modifier.padding(horizontal = 16.dp),
                    )
                }
                if (remove.error.isNotEmpty())
                    Banner(remove.error, Modifier.padding(16.dp), Tone2.ERROR)
            } else {
                Hint(
                    "删除设备要在管理端手机上操作。",
                    Modifier.padding(horizontal = 16.dp, vertical = 8.dp),
                )
            }
        }
    }
    if (confirmRemove && d != null) {
        val (title, text) = removalDialog(clientName(d, id, state.devices.values), machine = false)
        Alert(
            title,
            text,
            "移除",
            danger = true,
            onDismiss = { confirmRemove = false },
        ) {
            remove.run(
                {
                    remote.removeDevice(id)
                    null
                },
            )
        }
    }
    if (editing && canRename)
        InputAlert(
            "客户端名称",
            d?.meta?.name.orEmpty(),
            defaultClientName(d, id, state.devices.values),
            "保存",
            onDismiss = { editing = false },
        ) { name ->
            save.run(
                {
                    remote.renameDevice(id, name.trim())
                    null
                },
            )
        }
}

/** The one confirmation before removal: names the device and says what happens to it. */
internal fun removalDialog(name: String, machine: Boolean): Pair<String, String> =
    "移除 $name？" to
        if (machine) "这会立即断开连接并撤销权限，保留中继上的会话和图片；它需要重新加入账户才能连接。"
        else "这会断开它的登录并清除推送订阅，不结束电脑上的会话，也不删除会话历史。再次加入要在管理端手机上重新批准。"

/** Shares the public genesis state for the relay's offline pin step. */
internal fun shareGenesis(context: android.content.Context, state: ByteArray) {
    val dir = File(context.cacheDir, SHARED_DIR).apply { mkdirs() }
    val file = File(dir, "public-genesis.bin")
    file.writeBytes(state)
    val uri = FileProvider.getUriForFile(context, context.packageName + ".files", file)
    context.startActivity(
        Intent.createChooser(
            Intent(Intent.ACTION_SEND)
                .setType("application/octet-stream")
                .putExtra(Intent.EXTRA_STREAM, uri)
                .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION),
            "导出创世状态",
        ),
    )
}

/** Both name actions follow the same client ownership/connection policy. */
internal fun clientCanRename(id: String, me: String, connection: ConnState): Boolean =
    id == me && connection == ConnState.ONLINE
