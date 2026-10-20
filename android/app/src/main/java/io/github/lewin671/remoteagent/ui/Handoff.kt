package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.clientName
import io.github.lewin671.remoteagent.core.errorText
import io.github.lewin671.remoteagent.proto.DeviceKind
import kotlinx.coroutines.launch

/** The new phone only offers its public recovery key after explicit backup confirmation. */
@Composable
fun HandoffPreparation() {
    val remote = LocalRemote.current
    val state by remote.state.collectAsState()
    val scope = rememberAuthorityScope()
    var prepared by remember { mutableStateOf<Pair<String, ByteArray>?>(null) }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    PageScaffold("接管管理权") {
        Column(Modifier.fillMaxWidth().padding(16.dp)) {
            Text(
                "先在旧手机批准这台手机加入。保存下面的新恢复码后，再请旧手机在“恢复与交接”里批准交接。旧手机会退出账户，不需要等待 48 小时。",
                style = T.sm,
                color = pal.muted,
            )
            val value = prepared
            if (value != null) {
                RecoveryCodeFigure(value.first)
                Btn(
                    "我已离线保存，通知旧手机",
                    {
                        busy = true
                        scope.launch {
                            try {
                                remote.offerHandoff(value.second)
                                prepared = null
                            } catch (e: Exception) {
                                error = errorText(e)
                            } finally {
                                busy = false
                            }
                        }
                    },
                    Modifier.fillMaxWidth().padding(top = 12.dp),
                    BtnKind.PRIMARY,
                    enabled = !busy,
                )
            } else {
                val offered =
                    state.devices[remote.account.deviceId]?.meta?.handoffRecoveryEd?.size() == 32
                if (offered)
                    Hint("恢复码已确认保存。等待旧手机批准；可以关闭后重新打开此页查看。", Modifier.padding(vertical = 12.dp))
                Btn(
                    if (offered) "重新生成恢复码" else "生成新的恢复码",
                    { prepared = remote.prepareHandoffCode() },
                    Modifier.fillMaxWidth().padding(top = 12.dp),
                    BtnKind.SECONDARY,
                    enabled = !busy,
                )
            }
            if (error.isNotEmpty()) Banner(error, Modifier.padding(top = 12.dp), Tone2.ERROR)
        }
    }
}

/** The old phone authenticates the new phone through ordinary pairing first. */
@Composable
fun HandoffControls() {
    val remote = LocalRemote.current
    val state by remote.state.collectAsState()
    val scope = rememberAuthorityScope()
    var target by remember { mutableStateOf<String?>(null) }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf("") }
    val candidates =
        state.devices.values.filter {
            it.id != remote.account.deviceId &&
                it.kind == DeviceKind.DEVICE_KIND_CLIENT &&
                it.meta?.handoffRecoveryEd?.size() == 32
        }
    GLabel("正常换手机")
    Hint("在新手机加入账户并确认保存新恢复码后，在这里批准。交接保留可达历史，同时移除本机。", Modifier)
    for (d in candidates) Btn(
        "交接给 " + clientName(d, d.id, state.devices.values),
        { target = d.id },
        Modifier.fillMaxWidth().padding(top = 8.dp),
        BtnKind.SECONDARY,
        enabled = !busy,
    )
    if (candidates.isEmpty()) Hint("等待新手机生成并保存恢复码", Modifier.padding(vertical = 8.dp))
    if (error.isNotEmpty()) Banner(error, Modifier.padding(vertical = 8.dp), Tone2.ERROR)
    target?.let { id ->
        Alert(
            "交接管理权",
            "确认新手机已经保存新的恢复码。交接后这台手机会被移除，新手机成为唯一管理端。",
            "确认交接",
            danger = true,
            onDismiss = { target = null },
        ) {
            target = null
            busy = true
            scope.launch {
                try {
                    remote.handoffAuthority(id)
                } catch (e: Exception) {
                    error = errorText(e)
                } finally {
                    busy = false
                }
            }
        }
    }
}

@Composable
fun DeleteMachineRecordsAction(id: String, authority: Boolean) {
    if (!authority) return
    val remote = LocalRemote.current
    val state by remote.state.collectAsState()
    if (id in state.devices) return
    val scope = rememberAuthorityScope()
    var confirm by remember { mutableStateOf(false) }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf("") }
    Group(
        rows =
            listOf(
                {
                    GRow("删除这台电脑的记录", onClick = { confirm = true }, danger = true, enabled = !busy)
                },
            ),
    )
    if (error.isNotEmpty()) Banner(error, Modifier.padding(16.dp), Tone2.ERROR)
    if (confirm)
        Alert(
            "删除这台电脑的记录",
            "将删除中继上的会话、事件和图片，无法撤销。不会删除电脑本地文件，也不会代替移除设备。",
            "删除记录",
            danger = true,
            onDismiss = { confirm = false },
        ) {
            confirm = false
            busy = true
            scope.launch {
                try {
                    remote.deleteMachineRecords(id)
                } catch (e: Exception) {
                    error = errorText(e)
                } finally {
                    busy = false
                }
            }
        }
}
