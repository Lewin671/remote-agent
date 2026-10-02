package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.layout.wrapContentWidth
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.imePadding
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
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.Mode
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.SessionView
import io.github.lewin671.remoteagent.core.agent
import io.github.lewin671.remoteagent.core.agentName
import io.github.lewin671.remoteagent.core.contextPercent
import io.github.lewin671.remoteagent.core.errorText
import io.github.lewin671.remoteagent.core.minutesLeft
import io.github.lewin671.remoteagent.core.modeOf
import io.github.lewin671.remoteagent.core.titleOf
import io.github.lewin671.remoteagent.proto.Agent
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

/** A sheet's title names the CLI only when it is not Codex, as the mockup does. */
private fun sheetTitle(s: SessionView, what: String) = if (s.agent() == Agent.AGENT_CODEX) what else "${agentName(s.agent())} · $what"

/** 审批模式: the four modes; full access is confirmed first; only this session changes. */
@Composable
fun ModeSheet(s: SessionView, onClose: () -> Unit) {
    val p = pal
    val remote = LocalRemote.current
    val act = rememberAction()
    var confirmFull by remember { mutableStateOf(false) }
    val cur = modeOf(s)
    fun unavailable(m: Mode) = (m == Mode.FULL && s.info?.fullAccessUnavailable == true) || (m == Mode.AUTO && io.github.lewin671.remoteagent.core.noAutoMode(s))
    fun set(m: Mode) = act.run({
        remote.command(s.id, remote.cmd { setApprovalMode = SetApprovalModeRequest.newBuilder().setSessionId(s.id).setMode(m.proto).build() })
    }) { onClose() }
    // A mode set in the terminal outside the four is shown as it is.
    val outside = s.info?.permissionMode?.takeIf { it.isNotEmpty() && io.github.lewin671.remoteagent.core.permissionMode(it) == null }
    Sheet(onClose) {
        SheetHead(sheetTitle(s, "审批模式"))
        if (outside != null) Text("当前：${if (outside == "acceptEdits") "accept edits" else outside}（在终端设置）", style = T.meta, color = p.muted, modifier = Modifier.padding(bottom = 4.dp))
        Mode.entries.forEachIndexed { i, m ->
            val off = unavailable(m)
            ListItem(
                m.text,
                when {
                    off && m == Mode.FULL -> "本会话启动时未启用；需要时新建会话选择"
                    off -> "当前 Haiku 模型不支持"
                    else -> modeHelp.getValue(m).replace("CLI", agentName(s.agent()))
                },
                checked = m == cur, enabled = !off, danger = m == Mode.FULL && !off, last = i == Mode.entries.size - 1,
                onClick = if (act.running || off) null else ({ if (m == Mode.FULL && cur != Mode.FULL) confirmFull = true else set(m) }),
            )
        }
        Hint(if (outside != null) "照实显示终端中的模式；不提供切到 ${if (outside == "acceptEdits") "accept edits" else outside} 的按钮。" else "只改当前会话")
        if (act.running) Hint("正在切换…")
        if (act.error.isNotEmpty()) Hint("没能切换：${act.error}", color = p.red)
    }
    if (confirmFull) Confirm(
        "切到完全放行？", "之后所有操作都不会再问你，手机上也收不到审批。只对这个会话生效。", "切换", danger = true,
        onDismiss = { confirmFull = false },
    ) { set(Mode.FULL) }
}

/** The context box of 会话设置: the percent, what it is of, and 压缩. */
@Composable
fun ContextBox(percent: Int, detail: String, action: String?, onAction: () -> Unit, enabled: Boolean = true) {
    val p = pal
    Row(Modifier.fillMaxWidth().clip(Radii.MD).background(p.soft).padding(12.dp), verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            Text("$percent%", style = T.h22, color = if (percent > 80) p.orangeInk else p.fg)
            Text(detail, style = T.sm, color = p.muted)
        }
        if (action != null) Box(Modifier.heightIn(min = 44.dp).clickable(enabled = enabled, onClick = onAction).padding(horizontal = 6.dp), contentAlignment = Alignment.Center) {
            Text(action, style = T.bodyB, color = if (enabled) p.blue else p.muted)
        }
    }
}

/** Choices laid out three to a row (推理强度). */
@Composable
fun ChoiceGrid(items: List<String>, selected: String?, enabled: Boolean, onPick: (String) -> Unit) {
    val p = pal
    Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
        for (row in items.chunked(3)) Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
            for (e in row) {
                val on = e == selected
                Box(
                    Modifier.weight(1f).heightIn(min = 44.dp).clip(Radii.SM).background(if (on) p.blueSoft else p.soft)
                        .then(if (on) Modifier.border(1.dp, p.blue, Radii.SM) else Modifier)
                        .clickable(enabled = enabled) { onPick(e) },
                    contentAlignment = Alignment.Center,
                ) { Text(e, style = T.sm, color = if (on) p.blue else if (enabled) p.fg else p.muted) }
            }
            repeat(3 - row.size) { Box(Modifier.weight(1f)) }
        }
    }
}

/**
 * 会话设置: context and 压缩, the model and its reasoning effort. A choice
 * is sent at once while idle; the sheet shows the computer's value after.
 */
@Composable
fun ModelSheet(state: RemoteState, s: SessionView, running: Boolean, onClose: () -> Unit) {
    val p = pal
    val remote = LocalRemote.current
    var models by remember { mutableStateOf<ListModelsResponse?>(null) }
    var loadError by remember { mutableStateOf("") }
    var attempt by remember { mutableStateOf(0) }
    var all by remember { mutableStateOf(false) }
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
    var pickedModel by remember { mutableStateOf("") }
    fun setModel(slug: String, effort: String) = act.run({
        remote.command(s.id, remote.cmd { setModel = SetModelRequest.newBuilder().setSessionId(s.id).setModel(slug).also { if (effort.isNotEmpty()) it.setEffort(effort) }.build() })
    }) { onClose() }
    Sheet(onClose) {
        SheetHead(sheetTitle(s, "会话设置"))
        val pc = contextPercent(s)
        if (pc != null) {
            val i = s.info!!
            ContextBox(
                pc, if (i.contextWindow > 0) "上下文 ${i.contextTokens / 1000}k / ${i.contextWindow / 1000}k" else "上下文占用",
                if (compact.running) "正在压缩…" else "压缩", enabled = !running && !compact.running,
                onAction = { compact.run({ remote.command(s.id, remote.cmd { this.compact = CompactRequest.newBuilder().setSessionId(s.id).build() }) }) { onClose() } },
            )
            if (compact.error.isNotEmpty()) Hint("没能压缩：${compact.error}", color = p.red)
        }
        FieldLabel("模型", Modifier.padding(top = 2.dp))
        val ms = models
        when {
            loadError.isNotEmpty() -> Banner("没能读取模型列表：$loadError", tone = Tone2.ERROR, action = "重试", onAction = { attempt++ })
            ms == null -> Text("读取中…", style = T.sm, color = p.muted, modifier = Modifier.padding(vertical = 12.dp))
            else -> {
                val current = io.github.lewin671.remoteagent.core.findModel(ms.modelsList, s.info?.model.orEmpty()) { it.slug }
                if (pickedModel.isEmpty()) pickedModel = current?.slug.orEmpty()
                val list = if (all || ms.modelsCount <= 5) ms.modelsList else ms.modelsList.take(3)
                list.forEachIndexed { i, m ->
                    ListItem(
                        m.displayName.ifEmpty { m.slug }, small = listOfNotNull(io.github.lewin671.remoteagent.core.contextText(m.contextWindow), if (m.effortsList.isEmpty()) "无推理强度" else m.effortsList.lastOrNull()?.takeIf { it != "high" }?.let { "最高 $it" }).joinToString(" · ").ifEmpty { null },
                        checked = m.slug == pickedModel, enabled = !running && !act.running, last = i == list.size - 1 && (all || ms.modelsCount <= 5),
                        onClick = {
                            pickedModel = m.slug
                            // A model without efforts has nothing more to choose.
                            if (m.effortsList.isEmpty() && m != current) setModel(m.slug, "")
                        },
                    )
                }
                if (!all && ms.modelsCount > 5) Box(Modifier.heightIn(min = 44.dp).clickable { all = true }, contentAlignment = Alignment.CenterStart) {
                    ArrowText("全部模型", p.blue, T.body)
                }
                val model = ms.modelsList.firstOrNull { it.slug == pickedModel }
                if (model != null && model.effortsList.isNotEmpty()) {
                    FieldLabel("推理强度")
                    ChoiceGrid(model.effortsList, if (model == current) s.info?.effort else null, !running && !act.running) { setModel(model.slug, it) }
                }
            }
        }
        Hint(if (running) "只在空闲时能切换；这一轮结束后再选" else "仅显示当前模型支持的档位；空闲时点选提交，等电脑确认后更新实际值。")
        if (act.running) Hint("正在切换…")
        if (act.error.isNotEmpty()) Hint("没能切换：${act.error}", color = p.red)
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
    val d = rememberDerived(events.events, s?.agent() ?: Agent.AGENT_CODEX)
    val a = d.approvals[aid]
    val p = pal
    val t = now()
    val act = rememberAction()
    var reason by Drafts.of("deny:$aid")
    PageScaffold("审批详情", subtitle = s?.let { titleOf(it) + if (a != null && a.hasDeadline()) " · 剩 ${minutesLeft(a.deadline.seconds * 1000, t)} 分钟" else "" }) {
        if (s == null || a == null) {
            val r = d.resolved[aid]
            Text(if (r != null) "这条审批已经处理了。" else "读取中…", style = T.body, color = p.muted, modifier = Modifier.padding(16.dp))
            return@PageScaffold
        }
        Column(Modifier.weight(1f).verticalScroll(rememberScrollState()).padding(horizontal = 14.dp)) {
            if (a.toolName.isNotEmpty() && a.command.isEmpty()) Text("请求权限 · ${approvalDisplay(a.toolName)}", style = T.smB, color = p.fg2, modifier = Modifier.padding(bottom = 4.dp))
            if (a.description.isNotEmpty()) Text(approvalDisplay(a.description), style = T.sm, color = p.fg2, modifier = Modifier.padding(bottom = 6.dp))
            Code(approvalDisplay(a.command.ifEmpty { a.toolInputJson }))
        }
        Column(Modifier.fillMaxWidth().padding(horizontal = 14.dp).topLine(p.line).padding(top = 8.dp, bottom = 16.dp)) {
            ApprovalButtons(s, a, compact = false)
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
    val d = rememberDerived(events.events, s?.agent() ?: Agent.AGENT_CODEX)
    PageScaffold("计划", subtitle = s?.let { titleOf(it) }) {
        val plan = d.plan
        if (s == null || plan == null) {
            Text("读取中…", style = T.body, color = pal.muted, modifier = Modifier.padding(16.dp))
            return@PageScaffold
        }
        Column(Modifier.weight(1f).verticalScroll(rememberScrollState()).padding(horizontal = 16.dp)) { Markdown(plan.text) }
        if (s.info?.planOpen == true) Column(Modifier.padding(horizontal = 14.dp).topLine(pal.line).padding(top = 8.dp, bottom = 16.dp)) { PlanButtons(s, full = true) { nav.back() } }
    }
}

/** A page with ‹, a centered title and an optional second line; [gray] for grouped settings pages. */
@Composable
fun PageScaffold(title: String, subtitle: String? = null, gray: Boolean = false, action: (@Composable () -> Unit)? = null, content: @Composable ColumnScope.() -> Unit) {
    val nav = LocalNav.current
    val p = pal
    androidx.compose.runtime.CompositionLocalProvider(LocalOnSoft provides gray) {
        Column(Modifier.fillMaxSize().background(if (gray) p.soft else p.card).imePadding()) {
            NavBar(title, nav.back, subtitle = subtitle, right = action)
            if (gray) Column(Modifier.weight(1f).fillMaxWidth().readableColumn()) { content() } else content()
        }
    }
}

/**
 * On a wide screen (a tablet, a phone on its side) settings rows keep a
 * centred column of about 640 dp instead of stretching across the window.
 */
fun Modifier.readableColumn(): Modifier = wrapContentWidth(Alignment.CenterHorizontally).widthIn(max = 672.dp)

/** A centered empty or loading note on a page. */
@Composable
fun PageNote(text: String) {
    Text(text, style = T.sm, color = pal.muted, textAlign = TextAlign.Center, modifier = Modifier.fillMaxWidth().padding(24.dp))
}
