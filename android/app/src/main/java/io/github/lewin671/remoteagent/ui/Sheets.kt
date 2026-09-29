package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilterChip
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.rememberModalBottomSheetState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.Mode
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.SessionView
import io.github.lewin671.remoteagent.core.agent
import io.github.lewin671.remoteagent.core.agentName
import io.github.lewin671.remoteagent.core.contextPercent
import io.github.lewin671.remoteagent.core.derive
import io.github.lewin671.remoteagent.core.errorText
import io.github.lewin671.remoteagent.core.minutesLeft
import io.github.lewin671.remoteagent.core.modeOf
import io.github.lewin671.remoteagent.core.titleOf
import io.github.lewin671.remoteagent.proto.CompactRequest
import io.github.lewin671.remoteagent.proto.ListModelsResponse
import io.github.lewin671.remoteagent.proto.SetApprovalModeRequest
import io.github.lewin671.remoteagent.proto.SetModelRequest

val modeHelp = mapOf(
    Mode.ASK to "需要审批时由你确认，超时拒绝",
    Mode.AUTO to "由 CLI 自己判断，拿不准的在对话里问你",
    Mode.PLAN to "只读和出计划，你批准后才开始改",
    Mode.FULL to "所有操作都不审批",
)

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ModeSheet(s: SessionView, onClose: () -> Unit) {
    val p = pal
    val remote = LocalRemote.current
    val act = rememberAction()
    var confirmFull by remember { mutableStateOf(false) }
    val cur = modeOf(s)
    fun set(m: Mode) = act.run({
        remote.command(s.id, remote.cmd { setApprovalMode = SetApprovalModeRequest.newBuilder().setSessionId(s.id).setMode(m.proto).build() })
    }) { onClose() }
    ModalBottomSheet(onDismissRequest = onClose, sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true)) {
        Column(Modifier.padding(horizontal = 16.dp).padding(bottom = 24.dp)) {
            Text("审批模式", style = MaterialTheme.typography.titleMedium)
            Spacer(Modifier.height(8.dp))
            for (m in Mode.entries) {
                Row44(onClick = if (act.running) null else ({ if (m == Mode.FULL && cur != Mode.FULL) confirmFull = true else set(m) })) {
                    Column(Modifier.weight(1f)) {
                        Text(m.text, style = MaterialTheme.typography.titleSmall, color = if (m == Mode.FULL) p.red else p.fg)
                        Text(modeHelp.getValue(m), style = MaterialTheme.typography.bodyMedium, color = p.muted)
                    }
                    if (m == cur) Text("✓", color = p.blue)
                }
            }
            Text("只改当前会话", style = MaterialTheme.typography.bodySmall, color = p.muted, modifier = Modifier.padding(top = 8.dp))
            if (act.running) Text("正在切换…", color = p.muted)
            if (act.error.isNotEmpty()) Text("没能切换：${act.error}", color = p.red)
        }
    }
    if (confirmFull) Confirm(
        "切到完全放行？", "之后所有操作都不会再问你，手机上也收不到审批。只对这个会话生效。", "切换", danger = true,
        onDismiss = { confirmFull = false },
    ) { set(Mode.FULL) }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ModelSheet(state: RemoteState, s: SessionView, running: Boolean, onClose: () -> Unit) {
    val p = pal
    val remote = LocalRemote.current
    var models by remember { mutableStateOf<ListModelsResponse?>(null) }
    var loadError by remember { mutableStateOf("") }
    var attempt by remember { mutableStateOf(0) }
    LaunchedEffect(attempt) {
        try {
            models = remote.listModels(s.machineId, s.agent())
            loadError = ""
        } catch (e: Exception) {
            loadError = errorText(e)
        }
    }
    val act = rememberAction()
    val compact = rememberAction()
    var pickedModel by remember { mutableStateOf(s.info?.model.orEmpty()) }
    ModalBottomSheet(onDismissRequest = onClose, sheetState = rememberModalBottomSheetState(skipPartiallyExpanded = true)) {
        Column(Modifier.padding(horizontal = 16.dp).padding(bottom = 24.dp).verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text("会话设置", style = MaterialTheme.typography.titleMedium)
            val pc = contextPercent(s)
            if (pc != null) {
                val i = s.info!!
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Column(Modifier.weight(1f)) {
                        Text("$pc%  上下文 ${i.contextTokens / 1000}k / ${i.contextWindow / 1000}k", style = MaterialTheme.typography.bodyMedium, color = if (pc > 80) p.orangeInk else p.fg2)
                        LinearProgressIndicator(progress = { pc / 100f }, Modifier.fillMaxWidth().padding(top = 4.dp), color = if (pc > 80) p.orange else p.blue, trackColor = p.track)
                    }
                    Spacer(Modifier.padding(6.dp))
                    OutlinedButton(onClick = {
                        compact.run({ remote.command(s.id, remote.cmd { this.compact = CompactRequest.newBuilder().setSessionId(s.id).build() }) }) { onClose() }
                    }, enabled = !running && !compact.running) { Text(if (compact.running) "正在压缩…" else "压缩") }
                }
                if (compact.error.isNotEmpty()) Text("没能压缩：${compact.error}", color = p.red, style = MaterialTheme.typography.bodySmall)
            }
            if (running) Text("这一轮在运行，结束后才能切换模型和推理强度", style = MaterialTheme.typography.bodySmall, color = p.orangeInk)
            Text("模型", style = MaterialTheme.typography.titleSmall)
            val ms = models
            when {
                loadError.isNotEmpty() -> Row(verticalAlignment = Alignment.CenterVertically) {
                    Text("没能读取模型列表：$loadError", color = p.red, modifier = Modifier.weight(1f))
                    TextButton(onClick = { attempt++ }) { Text("重试") }
                }
                ms == null -> Text("读取中…", color = p.muted)
                else -> {
                    for (m in ms.modelsList) {
                        Row44(onClick = { pickedModel = m.slug }) {
                            Column(Modifier.weight(1f)) {
                                Text(m.displayName.ifEmpty { m.slug }, style = MaterialTheme.typography.titleSmall)
                                if (m.description.isNotEmpty()) Text(m.description, style = MaterialTheme.typography.bodySmall, color = p.muted, maxLines = 2)
                            }
                            if (m.slug == pickedModel || m.displayName == pickedModel) Text("✓", color = p.blue)
                        }
                    }
                    val model = ms.modelsList.firstOrNull { it.slug == pickedModel || it.displayName == pickedModel }
                    if (model != null) {
                        Text("推理强度", style = MaterialTheme.typography.titleSmall)
                        Row(horizontalArrangement = Arrangement.spacedBy(6.dp), modifier = Modifier.fillMaxWidth()) {
                            for (e in model.effortsList) {
                                FilterChip(
                                    selected = e == s.info?.effort && model.slug == s.info?.model,
                                    onClick = {
                                        act.run({
                                            remote.command(s.id, remote.cmd { setModel = SetModelRequest.newBuilder().setSessionId(s.id).setModel(model.slug).setEffort(e).build() })
                                        }) { onClose() }
                                    },
                                    enabled = !running && !act.running,
                                    label = { Text(e) },
                                )
                            }
                        }
                    }
                }
            }
            if (act.running) Text("正在切换…", color = p.muted)
            if (act.error.isNotEmpty()) Text("没能切换：${act.error}", color = p.red)
        }
    }
}

/** 审批详情: the whole request, with a reason box that stays above the keyboard. */
@Composable
fun ApprovalPage(sid: String, aid: String) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val s = state.sessions[sid]
    val events by remote.events(sid).collectAsState()
    val d = remember(events.events) { derive(events.events) }
    val a = d.approvals[aid]
    val p = pal
    val t = now()
    val act = rememberAction()
    var reason by rememberSaveable { mutableStateOf("") }
    PageScaffold(if (s != null) "审批详情" else "审批") {
        if (s == null || a == null) {
            val r = d.resolved[aid]
            Text(if (r != null) "这条审批已经处理了。" else "读取中…", color = p.muted, modifier = Modifier.padding(16.dp))
            TextButton(onClick = nav.back) { Text("返回") }
            return@PageScaffold
        }
        Column(Modifier.weight(1f).verticalScroll(rememberScrollState()).padding(16.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
            Text(titleOf(s) + if (a.hasDeadline()) " · 剩 ${minutesLeft(a.deadline.seconds * 1000, t)} 分钟" else "", color = p.muted)
            if (a.toolName.isNotEmpty()) Text("工具：${a.toolName}", style = MaterialTheme.typography.bodyMedium)
            if (a.description.isNotEmpty()) Text(a.description, style = MaterialTheme.typography.bodyLarge)
            if (a.command.isNotEmpty()) CodeBlock(a.command)
            if (a.toolInputJson.isNotEmpty() && a.command.isEmpty()) CodeBlock(a.toolInputJson)
        }
        Column(Modifier.fillMaxWidth().imePadding().background(p.card).padding(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            OutlinedTextField(reason, { reason = it }, Modifier.fillMaxWidth(), placeholder = { Text("拒绝理由（可选），会告诉 ${agentName(s.agent())}") }, maxLines = 4)
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                fun resolve(allow: Boolean) = act.run({
                    remote.command(sid, remote.cmd {
                        resolveApproval = io.github.lewin671.remoteagent.proto.ResolveApprovalRequest.newBuilder()
                            .setSessionId(sid).setApprovalId(aid).setAllow(allow).setMessage(if (allow) "" else reason.trim()).build()
                    })
                }) { nav.back() }
                OutlinedButton(onClick = { resolve(false) }, enabled = !act.running, modifier = Modifier.weight(1f).heightIn(min = 48.dp),
                    colors = ButtonDefaults.outlinedButtonColors(contentColor = p.red)) { Text(if (reason.isBlank()) "拒绝" else "发送理由并拒绝") }
                Button(onClick = { resolve(true) }, enabled = !act.running && reason.isBlank(), modifier = Modifier.weight(1f).heightIn(min = 48.dp)) { Text("允许") }
            }
            if (act.error.isNotEmpty()) Text("没能提交：${act.error}", color = p.red, style = MaterialTheme.typography.bodySmall)
        }
    }
}

@Composable
fun PlanPage(sid: String) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val s = state.sessions[sid]
    val events by remote.events(sid).collectAsState()
    val d = remember(events.events) { derive(events.events) }
    PageScaffold("计划") {
        val plan = d.plan
        if (s == null || plan == null) {
            Text("读取中…", Modifier.padding(16.dp))
            return@PageScaffold
        }
        Column(Modifier.weight(1f).verticalScroll(rememberScrollState()).padding(16.dp)) { Markdown(plan.text) }
        if (s.info?.planOpen == true) Column(Modifier.padding(12.dp)) { PlanButtons(s, full = true) { nav.back() } }
    }
}

/** A page with a back arrow and a title. */
@Composable
fun PageScaffold(title: String, action: @Composable () -> Unit = {}, content: @Composable androidx.compose.foundation.layout.ColumnScope.() -> Unit) {
    val nav = LocalNav.current
    val p = pal
    Column(Modifier.fillMaxSize().background(p.bg)) {
        Row(Modifier.fillMaxWidth().padding(4.dp), verticalAlignment = Alignment.CenterVertically) {
            IconButton(onClick = nav.back) { Icon(Icons.AutoMirrored.Filled.ArrowBack, "返回") }
            Text(title, style = MaterialTheme.typography.titleMedium, modifier = Modifier.weight(1f))
            action()
        }
        content()
    }
}
