package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.widthIn
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.Link
import io.github.lewin671.remoteagent.core.Ping
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.SLOW_MACHINE_MS
import io.github.lewin671.remoteagent.core.SLOW_SERVER_MS
import io.github.lewin671.remoteagent.core.errorText
import io.github.lewin671.remoteagent.core.median
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.coroutineScope

/** The link to the server in a word: what 设置 says before anything is measured. */
fun connWord(state: RemoteState): String = when {
    state.conn == ConnState.ONLINE -> "已连接"
    state.synced -> "重连中"
    state.link == Link.NO_SERVER -> "连不上"
    else -> "连接中"
}

/**
 * 连接检测 (client-v34): how long a round trip takes now on each stretch of the way, the
 * nearest first: this device's network, the server, then each computer through the
 * server. Measured when opened and on 再测一次; nothing runs in the background.
 */
@Composable
fun ConnCheckSheet(state: RemoteState, onClose: () -> Unit) {
    val p = pal
    val remote = LocalRemote.current
    var server by remember { mutableStateOf<Ping>(Ping.Wait) }
    val machines = remember { mutableStateMapOf<String, Ping>() }
    var run by remember { mutableStateOf(0) }
    var busy by remember { mutableStateOf(true) }
    val list = state.machines.values.sortedWith(compareByDescending<io.github.lewin671.remoteagent.core.MachineView> { it.online }.thenBy { machineLabel(state, it.id) })
    LaunchedEffect(run) {
        busy = true
        val online = remote.state.value.machines.values.filter { it.online }.map { it.id }
        server = Ping.Wait
        machines.clear()
        online.forEach { machines[it] = Ping.Wait }
        try {
            server = Ping.Ok(median(List(3) { remote.pingServer() }))
            coroutineScope {
                online.map { id ->
                    async { machines[id] = runCatching { Ping.Ok(remote.pingMachine(id)) }.getOrElse { if (it is kotlinx.coroutines.CancellationException) throw it else Ping.Fail(errorText(it).ifEmpty { "没有回应" }) } }
                }.awaitAll()
            }
        } catch (e: kotlinx.coroutines.CancellationException) {
            throw e
        } catch (e: Exception) {
            server = Ping.Fail(errorText(e).ifEmpty { "没有回应" })
            online.forEach { machines[it] = Ping.Skip }
        }
        busy = false
    }
    @Composable
    fun line(name: String, sub: String? = null, value: @Composable () -> Unit) {
        Row(Modifier.fillMaxWidth().heightIn(min = 44.dp).padding(horizontal = 14.dp, vertical = 6.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
            Column(Modifier.weight(1f)) {
                Text(name, style = T.body, color = p.fg, maxLines = 1, overflow = TextOverflow.Ellipsis)
                if (sub != null) Text(sub, style = T.meta, color = p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
            value()
        }
    }
    @Composable
    fun dotted(color: androidx.compose.ui.graphics.Color, text: String, ink: androidx.compose.ui.graphics.Color = p.fg2) =
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
            Dot(color)
            Text(text, style = T.sm, color = ink)
        }
    @Composable
    fun result(ping: Ping?, slow: Long) = when (ping) {
        null, Ping.Wait -> IconText(Lucide.LoaderCircle, "检测中…", p.muted, T.sm)
        Ping.Skip -> Text("未检测", style = T.sm, color = p.muted)
        is Ping.Fail -> Text(ping.error, style = T.sm, color = p.red, maxLines = 2, overflow = TextOverflow.Ellipsis, modifier = Modifier.widthIn(max = 200.dp))
        is Ping.Ok -> dotted(if (ping.ms >= slow) p.orange else p.green, "${ping.ms} ms" + if (ping.ms >= slow) " · 较慢" else "")
    }
    Sheet(onClose) {
        SheetHead("连接检测", "完成", onClose)
        Column(Modifier.fillMaxWidth().clip(Radii.MD).background(p.soft)) {
            val network = state.link != Link.NO_NETWORK
            line("本机网络") { dotted(if (network) p.green else p.gray, if (network) "正常" else "不通") }
            HorizontalDivider(color = p.line, thickness = 1.dp)
            line("服务器", remote.account.server.substringAfter("://")) { result(server, SLOW_SERVER_MS) }
            for (m in list) {
                HorizontalDivider(color = p.line, thickness = 1.dp)
                line(machineLabel(state, m.id)) { if (m.online) result(machines[m.id], SLOW_MACHINE_MS) else dotted(p.gray, "不在线", p.muted) }
            }
        }
        Hint("延时是一次往返的时间。服务器一行是这台设备到服务器；电脑一行是经服务器到电脑再回来，所以总比服务器慢。")
        Btn("再测一次", { run++ }, Modifier.fillMaxWidth().padding(top = 12.dp), enabled = !busy)
    }
}
