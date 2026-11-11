package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.approvalDisplay
import io.github.lewin671.remoteagent.core.approvalBody
import io.github.lewin671.remoteagent.core.approvalHead

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
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
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.layout.wrapContentWidth
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.platform.LocalWindowInfo
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.TextFieldValue
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import io.github.lewin671.remoteagent.core.DEFAULT_AGENT
import io.github.lewin671.remoteagent.core.Mode
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.SessionView
import io.github.lewin671.remoteagent.core.agent
import io.github.lewin671.remoteagent.core.agentInfo
import io.github.lewin671.remoteagent.core.agentName
import io.github.lewin671.remoteagent.core.contextPercent
import io.github.lewin671.remoteagent.core.errorText
import io.github.lewin671.remoteagent.core.minutesLeft
import io.github.lewin671.remoteagent.core.modeOf
import io.github.lewin671.remoteagent.core.renameInitial
import io.github.lewin671.remoteagent.core.renameTitle
import io.github.lewin671.remoteagent.core.titleInput
import io.github.lewin671.remoteagent.core.titleOf
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.CompactRequest
import io.github.lewin671.remoteagent.proto.ListModelsResponse
import io.github.lewin671.remoteagent.proto.RenameSessionRequest
import io.github.lewin671.remoteagent.proto.SetApprovalModeRequest
import io.github.lewin671.remoteagent.proto.SetModelRequest

val modeHelp =
    mapOf(
        Mode.ASK to "需要审批时由你确认，超时拒绝",
        Mode.AUTO to "由 CLI 自己判断，拿不准的在对话里问你",
        Mode.PLAN to "只读和出计划，你批准后才开始改",
        Mode.FULL to "所有操作都不审批",
    )

/** 审批模式: the four modes; full access is confirmed first; only this session changes. */
@Composable
fun ModeSheet(s: SessionView, onClose: () -> Unit) {
    val p = pal
    val remote = LocalRemote.current
    val act = rememberAction()
    var confirmFull by remember { mutableStateOf(false) }
    val cur = modeOf(s)
    // The auto mode goes with the model, which the CLI's own list says.
    var modelList by remember {
        mutableStateOf<List<io.github.lewin671.remoteagent.proto.ModelInfo>?>(null)
    }
    LaunchedEffect(s.machineId, s.info?.model) {
        modelList =
            runCatching { remote.listModels(s.machineId, s.agent()) }.getOrNull()?.modelsList
    }
    fun unavailable(m: Mode): String? {
        if (s.info?.modeLocked == true) return "本会话以完全放行启动，不能切换审批模式"
        if (m == Mode.FULL && s.info?.fullAccessUnavailable == true) return "本会话启动时未启用；需要时新建会话选择"
        if (m == Mode.AUTO) {
            if (io.github.lewin671.remoteagent.core.allAutoUnavailable(modelList)) return "当前账户不支持自动模式"
            if (io.github.lewin671.remoteagent.core.autoUnavailable(modelList, s.info?.model.orEmpty())) return "当前模型不支持"
        }
        return null
    }
    fun set(m: Mode) =
        act.run(
            {
                remote.command(
                    s.id,
                    remote.cmd {
                        setApprovalMode =
                            SetApprovalModeRequest.newBuilder()
                                .setSessionId(s.id)
                                .setMode(m.proto)
                                .build()
                    },
                )
            },
        ) {
            onClose()
        }
    // A mode set in the terminal outside the four is shown as it is.
    val outside =
        s.info?.permissionMode?.takeIf {
            it.isNotEmpty() && io.github.lewin671.remoteagent.core.permissionMode(it) == null
        }
    Sheet(onClose) {
        SheetHead("审批模式")
        if (outside != null)
            Text(
                "当前：${if (outside == "acceptEdits") "accept edits" else outside}（在终端设置）",
                style = T.s12,
                color = p.ink2,
                modifier = Modifier.padding(bottom = 4.dp),
            )
        SheetList { Mode.entries.forEachIndexed { i, m ->
            val reason = unavailable(m)
            ListItem(
                m.text,
                reason ?: modeHelp.getValue(m).replace("CLI", agentName(s.agent())),
                checked = m == cur,
                enabled = reason == null && !act.running,
                danger = m == Mode.FULL && reason == null,
                last = i == Mode.entries.size - 1,
                onClick = {
                    if (m == Mode.FULL && cur != Mode.FULL) confirmFull = true else set(m)
                },
            )
        } }
        Hint(
            when {
                s.info?.modeLocked == true ->
                    "以完全放行启动后本次会话内无法退回需审批的模式；要恢复审批请新建会话。"
                outside != null ->
                    "照实显示终端中的模式；不提供切到 ${if (outside == "acceptEdits") "accept edits" else outside} 的按钮。"
                else -> "只改当前会话"
            },
        )
        if (act.running) Hint("正在切换…")
        if (act.error.isNotEmpty()) Hint("没能切换：${act.error}", color = p.red)
    }
    if (confirmFull)
        Confirm(
            "切到完全放行？",
            "之后所有操作都不会再问你，手机上也收不到审批。只对这个会话生效。",
            "切换",
            danger = true,
            onDismiss = { confirmFull = false },
        ) {
            set(Mode.FULL)
        }
}

/**
 * 重命名会话: the title on one line, selected so that typing replaces it, with the keyboard up. Saving
 * it empty goes back to the automatic title. The new title comes back in the session's metadata; a
 * failure keeps the dialog and what was typed.
 */
@Composable
fun RenameDialog(s: SessionView, onClose: () -> Unit) {
    val p = pal
    val remote = LocalRemote.current
    val act = rememberAction()
    val initial = remember { renameInitial(s) }
    var value by remember { mutableStateOf(TextFieldValue(initial, TextRange(0, initial.length))) }
    val focus = remember { FocusRequester() }
    fun save() {
        if (act.running) return
        // The same title again is a cancel.
        val title = renameTitle(value.text, initial) ?: return onClose()
        act.run(
            {
                remote.command(
                    s.id,
                    remote.cmd {
                        renameSession =
                            RenameSessionRequest.newBuilder()
                                .setSessionId(s.id)
                                .setTitle(title)
                                .build()
                    },
                )
            },
        ) {
            onClose()
        }
    }
    // Leaving while it is being saved would drop the answer.
    Dialog(
        onDismissRequest = { if (!act.running) onClose() },
        properties = DialogProperties(usePlatformDefaultWidth = false),
    ) {
        // The keyboard only comes up for a window that has the focus.
        val window = LocalWindowInfo.current
        LaunchedEffect(window) {
            snapshotFlow { window.isWindowFocused }.collect { if (it) focus.requestFocus() }
        }
        Box(
            Modifier.fillMaxWidth().padding(horizontal = 30.dp).imePadding(),
            contentAlignment = Alignment.Center,
        ) {
            Column(
                Modifier.widthIn(max = 320.dp)
                    .fillMaxWidth()
                    .clip(Radii.R20)
                    .background(p.paper)
                    .padding(start = 16.dp, end = 16.dp, top = 18.dp, bottom = 12.dp),
                horizontalAlignment = Alignment.CenterHorizontally,
            ) {
                Text(
                    "重命名会话",
                    style = T.s17b,
                    color = p.ink,
                    textAlign = TextAlign.Center,
                    modifier = Modifier.padding(bottom = 12.dp),
                )
                BasicTextField(
                    value,
                    { value = it.copy(text = titleInput(it.text)) },
                    Modifier.fillMaxWidth().padding(bottom = 8.dp).focusRequester(focus),
                    textStyle = T.s15.copy(color = p.ink),
                    cursorBrush = SolidColor(p.blue),
                    singleLine = true,
                    keyboardOptions = KeyboardOptions(imeAction = ImeAction.Done),
                    keyboardActions = KeyboardActions(onDone = { save() }),
                    decorationBox = { inner -> FieldBox { inner() } },
                )
                Text(
                    if (act.error.isNotEmpty()) "没能保存：${act.error}" else "留空保存会恢复自动标题",
                    style = T.s13,
                    color = if (act.error.isNotEmpty()) p.red else p.ink2,
                    textAlign = TextAlign.Center,
                    modifier = Modifier.padding(bottom = 14.dp),
                )
                Btns {
                    PillButton("取消", onClose, Modifier.weight(1f), enabled = !act.running)
                    PillButton(
                        "保存",
                        { save() },
                        Modifier.weight(1f),
                        ButtonKind.INK,
                        enabled = !act.running,
                    )
                }
            }
        }
    }
}

/** The context box of 会话设置: the percent, what it is of, and 压缩. */
@Composable
fun ContextBox(
    percent: Int,
    detail: String,
    action: String?,
    onAction: () -> Unit,
    enabled: Boolean = true,
) {
    val p = pal
    Row(
        Modifier.fillMaxWidth().clip(Radii.R14).background(if (LocalOnSoft.current) p.raised else p.gbg).padding(12.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Column(Modifier.weight(1f)) {
            Text("$percent%", style = T.h22, color = if (percent > 80) p.orangeInk else p.ink)
            Text(detail, style = T.s13, color = p.ink2)
        }
        if (action != null)
            Box(
                Modifier.heightIn(min = 44.dp)
                    .clickable(enabled = enabled, onClick = onAction)
                    .padding(horizontal = 6.dp),
                contentAlignment = Alignment.Center,
            ) {
                Text(action, style = T.s15b, color = if (enabled) p.blue else p.ink2)
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
                    Modifier.weight(1f)
                        .heightIn(min = 44.dp)
                        .clip(Radii.R14)
                        .background(if (on) p.fill else p.gbg)
                        .then(if (on) Modifier.border(1.dp, p.blue, Radii.R14) else Modifier)
                        .clickable(enabled = enabled) { onPick(e) },
                    contentAlignment = Alignment.Center,
                ) {
                    Text(
                        e,
                        style = T.s13,
                        color = if (on) p.blue else if (enabled) p.ink else p.ink2,
                    )
                }
            }
            repeat(3 - row.size) { Box(Modifier.weight(1f)) }
        }
    }
}

/**
 * 会话设置: context and 压缩, the model and its reasoning effort. A choice is sent at once while idle;
 * the sheet shows the computer's value after.
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
            models = remote.listModels(s.machineId, s.agent(), fresh = true)
            loadError = ""
        } catch (e: Exception) {
            loadError = errorText(e)
        }
    }
    val act = rememberAction()
    val compact = rememberAction()
    var pickedModel by remember { mutableStateOf("") }
    fun setModel(slug: String, effort: String) =
        act.run(
            {
                remote.command(
                    s.id,
                    remote.cmd {
                        setModel =
                            SetModelRequest.newBuilder()
                                .setSessionId(s.id)
                                .setModel(slug)
                                .also { if (effort.isNotEmpty()) it.setEffort(effort) }
                                .build()
                    },
                )
            },
        ) {
            onClose()
        }
    Sheet(onClose) {
        SheetHead("会话设置")
        val pc = contextPercent(s)
        if (pc != null) {
            val i = s.info!!
            ContextBox(
                pc,
                if (i.contextWindow > 0)
                    "上下文 ${i.contextTokens / 1000}k / ${i.contextWindow / 1000}k"
                else "上下文占用",
                if (compact.running) "正在压缩…" else "压缩",
                enabled = !running && !compact.running,
                onAction = {
                    compact.run(
                        {
                            remote.command(
                                s.id,
                                remote.cmd {
                                    this.compact =
                                        CompactRequest.newBuilder().setSessionId(s.id).build()
                                },
                            )
                        },
                    ) {
                        onClose()
                    }
                },
            )
            if (compact.error.isNotEmpty()) Hint("没能压缩：${compact.error}", color = p.red)
        }
        val canSwitch = agentInfo(s.agent()).switchesModelInSession
        FieldLabel("模型", Modifier.padding(top = 2.dp))
        val ms = models
        when {
            !canSwitch -> {
                val current =
                    ms?.let {
                        io.github.lewin671.remoteagent.core.findModel(
                            it.modelsList,
                            s.info?.model.orEmpty(),
                        ) { m -> m.slug }
                    }
                val modelDisplay = current?.displayName?.ifEmpty { current.slug }
                    ?: io.github.lewin671.remoteagent.core.modelLabel(ms?.modelsList, s.info?.model.orEmpty(), s.agent())
                    ?: s.info?.model?.ifEmpty { null }
                    ?: "默认模型"
                SheetList {
                    ListItem(
                        modelDisplay,
                        small = "运行中不能换模型，新建会话时可选",
                        enabled = false,
                        last = true,
                        onClick = null,
                    )
                }
                FieldLabel("推理强度")
                val effortDisplay = s.info?.effort?.ifEmpty { null }
                    ?: current?.defaultEffort?.ifEmpty { null }
                    ?: "默认"
                SheetList {
                    ListItem(
                        effortDisplay,
                        small = "运行中不能换推理强度",
                        enabled = false,
                        last = true,
                        onClick = null,
                    )
                }
            }
            loadError.isNotEmpty() ->
                Notice(
                    "没能读取模型列表：$loadError",
                    tone = NoticeTone.ERROR,
                    action = "重试",
                    onAction = { attempt++ },
                )
            ms == null ->
                Text(
                    "读取中…",
                    style = T.s13,
                    color = p.ink2,
                    modifier = Modifier.padding(vertical = 12.dp),
                )
            else -> {
                val offered = ms.modelsList
                val current =
                    io.github.lewin671.remoteagent.core.findModel(
                        offered,
                        s.info?.model.orEmpty(),
                    ) {
                        it.slug
                    }
                if (pickedModel.isEmpty()) pickedModel = current?.slug.orEmpty()
                val list =
                    io.github.lewin671.remoteagent.core.shownModels(
                        offered,
                        offered.firstOrNull { it.slug == pickedModel },
                        all,
                    )
                val few = list.size == offered.size
                SheetList { list.forEachIndexed { i, m ->
                    ListItem(
                        m.displayName.ifEmpty { m.slug },
                        small =
                            if (m.effortsList.isEmpty()) "无推理强度"
                            else
                                m.effortsList
                                    .lastOrNull()
                                    ?.takeIf { it != "high" }
                                    ?.let { "最高 $it" },
                        checked = m.slug == pickedModel,
                        enabled = !running && !act.running,
                        last = i == list.size - 1,
                        onClick = {
                            pickedModel = m.slug
                            // A model without efforts has nothing more to choose.
                            if (m.effortsList.isEmpty() && m != current) setModel(m.slug, "")
                        },
                    )
                } }
                if (!few) AllModels { all = true }
                val model = offered.firstOrNull { it.slug == pickedModel }
                if (model != null && model.effortsList.isNotEmpty()) {
                    FieldLabel("推理强度")
                    ChoiceGrid(
                        model.effortsList,
                        if (model == current) s.info?.effort else null,
                        !running && !act.running,
                    ) {
                        setModel(model.slug, it)
                    }
                }
            }
        }
        if (canSwitch) {
            Hint(if (running) "只在空闲时能切换；这一轮结束后再选" else "仅显示当前模型支持的档位；空闲时点选提交，等电脑确认后更新实际值。")
        }
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
    val lookup = sessionLookup(sid) // Fetches it by its ID when it is not held here.
    val events by remote.events(sid).collectAsState()
    val d = rememberDerived(events, s?.agent() ?: DEFAULT_AGENT)
    val a = d.approvals[aid]
    val p = pal
    val t = now()
    val act = rememberAction()
    var reason by Drafts.of("deny:$aid")
    PageScaffold(
        "审批详情",
        subtitle =
            s?.let {
                titleOf(it) +
                    if (a != null && a.hasDeadline())
                        " · 剩 ${minutesLeft(a.deadline.seconds * 1000, t)} 分钟"
                    else ""
            },
    ) {
        if (s == null || a == null) {
            val r = d.resolved[aid]
            Text(
                if (r != null) "这条审批已经处理了。"
                else io.github.lewin671.remoteagent.core.lookupNote(lookup) ?: "读取中…",
                style = T.s15,
                color = p.ink2,
                modifier = Modifier.padding(16.dp),
            )
            if (r != null && reason.isNotEmpty()) {
                Column(
                    Modifier.fillMaxWidth()
                        .verticalScroll(rememberScrollState())
                        .padding(horizontal = 16.dp),
                ) {
                    Code(reason)
                    val clipboard = androidx.compose.ui.platform.LocalClipboard.current
                    TextAction(
                        "复制",
                        {
                            act.run(
                                {
                                    clipboard.setClipEntry(
                                        androidx.compose.ui.platform.ClipEntry(
                                            android.content.ClipData.newPlainText("", reason),
                                        ),
                                    )
                                    null
                                },
                            )
                        },
                        alignment = Alignment.Center,
                    )
                }
            }
            return@PageScaffold
        }
        val decision = rememberApprovalReply(s, a)
        val card = Modifier.padding(start = 16.dp, end = 16.dp, top = 8.dp, bottom = 16.dp)
        if (decision.submitted) {
            ApprovalSubmitted(approvalHead(a), decision, card)
            return@PageScaffold
        }
        var denying by androidx.compose.runtime.saveable.rememberSaveable(aid) { mutableStateOf(false) }
        // The command first, then why, scrolling in the card; the buttons and a reason stay above the keyboard.
        DarkCard(Modifier.weight(1f, fill = false).then(card)) {
            DarkCardHead(approvalHead(a))
            if (io.github.lewin671.remoteagent.core.isSkeleton(a)) {
                Text(io.github.lewin671.remoteagent.core.skeletonMessage, style = T.s15, color = CardColors.text2, modifier = Modifier.padding(top = 12.dp))
                return@DarkCard
            }
            Column(Modifier.weight(1f, fill = false).verticalScroll(rememberScrollState())) { ApprovalContent(a) }
            ApprovalButtons(state, s, a, decision, denying, { denying = it }, approvalLocked(state, a, t))
        }
    }
}

@Composable
fun PlanPage(sid: String) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val s = state.sessions[sid]
    val lookup = sessionLookup(sid) // Fetches it by its ID when it is not held here.
    val events by remote.events(sid).collectAsState()
    val d = rememberDerived(events, s?.agent() ?: DEFAULT_AGENT)
    PageScaffold("计划", subtitle = s?.let { titleOf(it) }) {
        val plan = d.plan
        if (s == null || plan == null) {
            Text(
                io.github.lewin671.remoteagent.core.lookupNote(lookup) ?: "读取中…",
                style = T.s15,
                color = pal.ink2,
                modifier = Modifier.padding(16.dp),
            )
            return@PageScaffold
        }
        if (io.github.lewin671.remoteagent.core.isSkeleton(plan.text)) {
            Text(
                io.github.lewin671.remoteagent.core.skeletonMessage,
                style = T.s13,
                color = pal.ink2,
            )
            return@PageScaffold
        }
        Column(
            Modifier.weight(1f).verticalScrollFaded().padding(horizontal = 16.dp),
        ) {
            Markdown(plan.text, bleed = 16.dp)
        }
        if (s.info?.planOpen == true)
            Column(
                Modifier.padding(horizontal = 14.dp)
                    .topLine(pal.sep)
                    .padding(top = 8.dp, bottom = 16.dp),
            ) {
                PlanButtons(s, full = true, onCard = false) { nav.back() }
            }
    }
}

/**
 * A page with back, a centered title and an optional second line, on `paper` with `gbg` cards like
 * 设置 itself (design-system §2, §6): no bar colour. [readable] keeps a centred column of about 640
 * dp on a wide screen, for settings pages.
 */
@Composable
fun PageScaffold(
    title: String,
    subtitle: String? = null,
    readable: Boolean = false,
    action: (@Composable () -> Unit)? = null,
    onBack: (() -> Unit)? = null,
    content: @Composable ColumnScope.() -> Unit,
) {
    val back = onBack ?: LocalNav.current.back
    if (onBack != null) androidx.activity.compose.BackHandler(onBack = onBack)
    val p = pal
    Column(Modifier.fillMaxSize().background(p.paper).imePadding()) {
        PageBar(title, back, subtitle = subtitle, right = action)
        if (readable) Column(Modifier.weight(1f).fillMaxWidth().readableColumn()) { content() }
        else content()
    }
}

/**
 * On a wide screen (a tablet, a phone on its side) settings rows keep a centred column of about 640
 * dp instead of stretching across the window.
 */
fun Modifier.readableColumn(): Modifier =
    wrapContentWidth(Alignment.CenterHorizontally).widthIn(max = 672.dp)

/** A centered empty or loading note on a page. */
@Composable
fun PageNote(text: String) {
    Text(
        text,
        style = T.s13,
        color = pal.ink2,
        textAlign = TextAlign.Center,
        modifier = Modifier.fillMaxWidth().padding(24.dp),
    )
}

/** The link under a model list cut short by [io.github.lewin671.remoteagent.core.shownModels]. */
@Composable
fun AllModels(onClick: () -> Unit) {
    Box(Modifier.heightIn(min = 44.dp).clickable(onClick = onClick), contentAlignment = Alignment.CenterStart) {
        ArrowText("全部模型", pal.blue, T.s15)
    }
}
