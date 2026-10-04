package io.github.lewin671.remoteagent.ui

import android.content.Context
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.MutableState
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.core.ATTENTION_MAX
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.ENDED_PAGE
import io.github.lewin671.remoteagent.core.FILTER_OVER
import io.github.lewin671.remoteagent.core.HomeScope
import io.github.lewin671.remoteagent.core.RUNNING_MAX
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.RowMeta
import io.github.lewin671.remoteagent.core.SessionView
import io.github.lewin671.remoteagent.core.Status
import io.github.lewin671.remoteagent.core.agent
import io.github.lewin671.remoteagent.core.agentName
import io.github.lewin671.remoteagent.core.cardMeta
import io.github.lewin671.remoteagent.core.decodeScope
import io.github.lewin671.remoteagent.core.encodeScope
import io.github.lewin671.remoteagent.core.endedMs
import io.github.lewin671.remoteagent.core.endedTail
import io.github.lewin671.remoteagent.core.endedTime
import io.github.lewin671.remoteagent.core.exited
import io.github.lewin671.remoteagent.core.filterTail
import io.github.lewin671.remoteagent.core.groupEnded
import io.github.lewin671.remoteagent.core.groupTitle
import io.github.lewin671.remoteagent.core.homeTimeline
import io.github.lewin671.remoteagent.core.listedProjects
import io.github.lewin671.remoteagent.core.metaLine
import io.github.lewin671.remoteagent.core.minutesLeft
import io.github.lewin671.remoteagent.core.previewOf
import io.github.lewin671.remoteagent.core.projectKey
import io.github.lewin671.remoteagent.core.projectName
import io.github.lewin671.remoteagent.core.resolveScope
import io.github.lewin671.remoteagent.core.scopeRows
import io.github.lewin671.remoteagent.core.titleOf
import io.github.lewin671.remoteagent.core.whenText

/**
 * The home page's scope as this device remembers it (client-v27,
 * architecture §8): kept with the device's own settings and not in the
 * account's preferences, because different devices often look at different
 * projects. Null until the user chose one; what the page then uses is
 * resolveScope's answer.
 */
object HomeScopes {
    private var state: MutableState<HomeScope?>? = null

    private fun store(context: Context) = context.applicationContext.getSharedPreferences("home-scope", Context.MODE_PRIVATE)

    private fun state(context: Context): MutableState<HomeScope?> =
        state ?: mutableStateOf(decodeScope(store(context).getString("scope", null))).also { state = it }

    fun remembered(context: Context): HomeScope? = state(context).value

    fun choose(context: Context, scope: HomeScope) {
        state(context).value = scope
        store(context).edit().putString("scope", encodeScope(scope)).apply()
    }

    /** Forgotten with the account: the scope names one of its project paths. */
    fun clear(context: Context) {
        state(context).value = null
        store(context).edit().clear().apply()
    }
}

/**
 * 主页 (client-v27): one timeline of sessions for a scope, a project or
 * every project. What needs you comes first and is not scoped, then what is
 * running, then the ended sessions by day, a page at a time.
 */
@Composable
fun HomeScreen() {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val context = LocalContext.current
    val state by remote.state.collectAsState()
    val p = pal
    val t = now()
    val scope = resolveScope(HomeScopes.remembered(context), state)
    val scopeKey = encodeScope(scope)
    // What is typed stays in memory: saved instance state is plaintext on disk (architecture §15).
    var query by remember(scopeKey) { mutableStateOf("") }
    val filtering = query.isNotBlank()
    val tl = remember(state, scope, query) { homeTimeline(state, scope, query) }
    var showAll by remember(scopeKey) { mutableStateOf(false) }
    var showRunning by remember(scopeKey) { mutableStateOf(false) }
    var shownEnded by remember(scopeKey, query) { mutableIntStateOf(ENDED_PAGE) }
    var pick by rememberSaveable { mutableStateOf(false) }
    var menu by rememberSaveable { mutableStateOf(false) }
    // Without any project, removed ones included, there is nothing to scope by.
    val anyProject = state.sessions.isNotEmpty()

    LazyColumn(Modifier.fillMaxSize().background(p.card)) {
        item {
            ScopeTitle(
                scope.takeIf { anyProject }, onOpen = { menu = true },
                // A project as the scope is where the new session goes; every project asks which first.
                onNew = { if (scope is HomeScope.Of) nav.projectNew(scope.key) else pick = true },
            )
            if (state.conn != ConnState.ONLINE) ConnBanner(state)
            if (tl.total > FILTER_OVER || query.isNotEmpty()) FilterBox(query) { query = it }
        }
        if (!state.loaded && state.sessions.isEmpty()) {
            item { Skeleton() }
        } else if (state.sessions.isEmpty()) {
            item { EmptyAccount(state) }
        } else if (scope is HomeScope.Of && tl.total == 0 && !filtering) {
            item { EmptyScope() }
        }
        if (tl.attention.isNotEmpty()) {
            item { Label("需要处理", tl.attention.size) }
            val shown = if (showAll || filtering) tl.attention else tl.attention.take(ATTENTION_MAX)
            items(shown, key = { "a" + it.id }) { s -> AttentionCard(state, s, t, scope) }
            if (shown.size < tl.attention.size) item {
                Box(Modifier.padding(start = 12.dp, end = 12.dp, bottom = 4.dp).heightIn(min = 44.dp).clickable { showAll = true }.padding(horizontal = 4.dp), contentAlignment = Alignment.CenterStart) {
                    ArrowText("还有 ${tl.attention.size - shown.size} 项待处理", p.blue, T.smB)
                }
            }
        }
        if (tl.running.isNotEmpty()) {
            item { Label("进行中", tl.running.size) }
            val shown = if (showRunning || filtering) tl.running else tl.running.take(RUNNING_MAX)
            items(shown, key = { "r" + it.id }) { s -> SessionRow(state, s, t, tl.meta, last = s == shown.last()) }
            if (shown.size < tl.running.size) item { TextAction("还有 ${tl.running.size - shown.size} 个进行中 ›", { showRunning = true }, Modifier.padding(horizontal = 16.dp)) }
        }
        val ended = tl.ended.take(shownEnded)
        for ((group, list) in groupEnded(ended, t)) {
            item(key = "g$group") { Label(groupTitle(group, t)) }
            items(list, key = { "e" + it.id }) { s -> EndedRow(s, t, project = tl.meta.project) }
        }
        val more = ended.size < tl.ended.size
        val tail = if (filtering) filterTail(tl.size, query) else endedTail(tl.ended.size, ended.size)
        if (tail != null || more) item(key = "tail") {
            // Every summary is already on this device: reaching the end only renders the next page.
            if (more) LaunchedEffect(shownEnded) { shownEnded += ENDED_PAGE }
            Text(tail.orEmpty(), style = T.meta, color = p.muted, modifier = Modifier.padding(start = 35.dp, end = 16.dp, top = 12.dp, bottom = 4.dp).testTag("timeline-tail"))
        }
        item { Spacer(Modifier.height(24.dp)) }
    }
    if (pick) PickProjectSheet(state) { pick = false }
    if (menu) ScopeSheet(state, scope, t, onPick = { HomeScopes.choose(context, it) }) { menu = false }
}

/**
 * The home page's title is its scope (client-v27): the project's avatar and
 * name, or 全 on grey and 全部项目, 22 with a › pointing down; a tap opens
 * 看哪个项目. Without any project ([scope] null) it is a plain 会话 that
 * does nothing. The new-session button stays at the right.
 */
@Composable
private fun ScopeTitle(scope: HomeScope?, onOpen: () -> Unit, onNew: () -> Unit) {
    val p = pal
    Row(Modifier.fillMaxWidth().padding(start = 16.dp, end = 12.dp, top = 4.dp, bottom = 2.dp), verticalAlignment = Alignment.CenterVertically) {
        if (scope == null) {
            Text("会话", style = T.title30, color = p.fg, modifier = Modifier.weight(1f).padding(vertical = 3.dp))
        } else {
            Box(Modifier.weight(1f)) {
                Row(
                    Modifier.heightIn(min = 44.dp).clip(Radii.SM).clickable(onClickLabel = "换范围", role = Role.Button, onClick = onOpen).testTag("scope-title"),
                    verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp),
                ) {
                    ScopeAvatar(scope, 28)
                    Text(scopeName(scope), style = T.h22, color = p.fg, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
                    Arrow(p.faint, ts(17.sp, lineHeight = 1f), down = true)
                }
            }
        }
        NewSessionButton(onNew)
    }
}

private fun scopeName(scope: HomeScope): String = when (scope) {
    HomeScope.All -> "全部项目"
    is HomeScope.Of -> projectName(scope.key)
}

/** A scope's avatar: the project's, or 全 on grey for every project. */
@Composable
private fun ScopeAvatar(scope: HomeScope, size: Int) {
    val p = pal
    when (scope) {
        is HomeScope.Of -> Avatar(scope.key, size)
        HomeScope.All -> Box(Modifier.size(size.dp).clip(RoundedCornerShape(if (size >= 28) 8.dp else 6.dp)).background(p.chip), contentAlignment = Alignment.Center) {
            Text("全", style = ts(if (size >= 28) 15.sp else 12.sp, FontWeight.SemiBold, 1.2f), color = p.muted)
        }
    }
}

/**
 * 看哪个项目 (client-v27): 全部项目 when there is more than one project,
 * the pinned ones, then the others, each with its path and what runs there,
 * ✓ on the current scope; a search above more than eight. 新项目… picks a
 * machine and a folder in the same sheet; 管理项目 goes to 项目管理.
 */
@Composable
private fun ScopeSheet(state: RemoteState, scope: HomeScope, t: Long, onPick: (HomeScope) -> Unit, onClose: () -> Unit) {
    val p = pal
    val nav = LocalNav.current
    var browsing by remember { mutableStateOf(false) }
    var search by remember { mutableStateOf("") }
    Sheet(onClose) {
        if (browsing) {
            SheetHead("新项目 · 选目录", "取消", onClose, onBack = { browsing = false })
            DirPicker(state) { machine, path -> onClose(); nav.newProjectAt(machine, path) }
            return@Sheet
        }
        SheetHead("看哪个项目", "取消", onClose)
        if (listedProjects(state).size > FILTER_OVER) OutlinedTextField(search, { search = it }, label = { Text("搜索项目") }, singleLine = true,
            modifier = Modifier.fillMaxWidth().padding(bottom = 8.dp))
        for (row in scopeRows(state, scope, t, search)) {
            Row(
                Modifier.fillMaxWidth().heightIn(min = 52.dp).bottomLine(p.line).clickable(role = Role.Button) { onPick(row.scope); onClose() }.padding(vertical = 6.dp),
                verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp),
            ) {
                ScopeAvatar(row.scope, 22)
                Column(Modifier.weight(1f)) {
                    Text(row.name, style = T.bodyB, color = p.fg, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    Text(row.detail, style = T.meta, color = p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
                Box(Modifier.size(16.dp).semantics { if (row.current) contentDescription = "当前范围" }, contentAlignment = Alignment.Center) {
                    if (row.current) Mark("✓", p.blue, 16.dp, 1.8.dp)
                }
            }
        }
        TextAction("新项目…", { browsing = true }, Modifier.fillMaxWidth(), style = T.body)
        TextAction("管理项目 ›", { onClose(); nav.manageProjects() }, Modifier.fillMaxWidth(), style = T.body)
    }
}

/**
 * The title filter (client-v27, design-system §6 筛选框): a pill on chip
 * grey with ⌕, 按标题筛选 until something is typed, then ✕ to clear.
 */
@Composable
private fun FilterBox(value: String, onChange: (String) -> Unit) {
    val p = pal
    Box(Modifier.fillMaxWidth().heightIn(min = 44.dp).padding(horizontal = 16.dp), contentAlignment = Alignment.CenterStart) {
        BasicTextField(
            value, onChange, Modifier.fillMaxWidth().testTag("title-filter"), singleLine = true,
            textStyle = T.body.copy(color = p.fg), cursorBrush = SolidColor(p.blue),
            decorationBox = { inner ->
                Row(
                    Modifier.fillMaxWidth().heightIn(min = 36.dp).clip(RoundedCornerShape(18.dp)).background(p.chip).padding(start = 14.dp, end = if (value.isEmpty()) 14.dp else 44.dp),
                    verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp),
                ) {
                    Text("⌕", style = T.body, color = p.muted)
                    Box(Modifier.weight(1f)) {
                        if (value.isEmpty()) Text("按标题筛选", style = T.body, color = p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        inner()
                    }
                }
            },
        )
        if (value.isNotEmpty()) Box(Modifier.align(Alignment.CenterEnd).size(44.dp).tap { onChange("") }.semantics { contentDescription = "清空筛选" }, contentAlignment = Alignment.Center) {
            Text("✕", style = T.meta, color = p.faint)
        }
    }
}

/** A project chosen as the scope that has no session. */
@Composable
private fun EmptyScope() {
    val p = pal
    Column(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 24.dp), horizontalAlignment = Alignment.CenterHorizontally) {
        Text("这个项目还没有会话", style = T.h17, color = p.fg)
        Text("点右上角的新建按钮开始一个", style = T.sm, color = p.muted, textAlign = TextAlign.Center, modifier = Modifier.padding(top = 4.dp))
    }
}

@Composable
private fun EmptyAccount(state: RemoteState) {
    val p = pal
    val nav = LocalNav.current
    val online = state.machines.values.filter { it.online }
    Column(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 24.dp), horizontalAlignment = Alignment.CenterHorizontally) {
        Text("还没有会话", style = T.h17, color = p.fg)
        when {
            state.machines.isEmpty() -> {
                Text("这个账户还没有电脑。在电脑上运行 remote-agent account join 加入。", style = T.sm, color = p.muted, textAlign = androidx.compose.ui.text.style.TextAlign.Center, modifier = Modifier.padding(top = 4.dp, bottom = 14.dp))
                Btn("添加设备", { nav.pair() }, Modifier.fillMaxWidth(), BtnKind.PRIMARY)
            }
            online.isEmpty() -> Text("没有在线的电脑。在电脑上运行 remote-agent daemon。", style = T.sm, color = p.muted, textAlign = androidx.compose.ui.text.style.TextAlign.Center, modifier = Modifier.padding(top = 4.dp))
            else -> {
                Text("${machineLabel(state, online.first().id)} 在线，可以直接开始", style = T.sm, color = p.muted, modifier = Modifier.padding(top = 4.dp, bottom = 14.dp))
                Btn("开始第一个会话", { nav.newProject() }, Modifier.fillMaxWidth(), BtnKind.PRIMARY)
            }
        }
    }
}

/**
 * One session in a list (client-v27): a dot, the title and time, then the
 * status word in its color · the preview, then only what differs between
 * the rows of this list ([meta]): 分叉 · project · CLI · machine, and no
 * third line when none does. An ended session is an [EndedRow]. The line
 * under a row starts where its text does.
 */
@Composable
fun SessionRow(state: RemoteState, s: SessionView, t: Long, meta: RowMeta = RowMeta(), last: Boolean = false, preview: Boolean = true, selected: Boolean = false) {
    if (exited(s)) return EndedRow(s, t, project = meta.project, selected = selected)
    val p = pal
    val nav = LocalNav.current
    val st = statusIn(state, s)
    Row(
        Modifier.fillMaxWidth().background(if (selected) p.blueSoft else Color.Transparent).clickableRow { nav.session(s.id) }.padding(start = 16.dp, end = 16.dp, top = 10.dp),
        verticalAlignment = Alignment.Top, horizontalArrangement = Arrangement.spacedBy(11.dp),
    ) {
        Box(Modifier.padding(top = 8.dp)) { Dot(p.tone(st.tone)) }
        Column(Modifier.weight(1f).then(if (!last) Modifier.bottomLine(p.line) else Modifier).padding(bottom = 10.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Text(titleOf(s), style = T.bodyB, color = p.fg, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                Text(whenText(s.updatedMs, t), style = T.meta, color = p.muted, maxLines = 1)
            }
            val pv = if (preview) previewOf(s, st) else ""
            Text(
                buildAnnotatedString {
                    pushStyle(SpanStyle(color = p.toneInk(st.tone), fontWeight = platformWeight(FontWeight.SemiBold)))
                    append(st.text)
                    pop()
                    // The status word already says a plan waits.
                    if (pv.isNotEmpty() && pv != st.text) append(" · $pv")
                },
                style = T.sm, color = p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis,
            )
            val rest = metaLine(state, s, meta)
            if (rest.isNotEmpty()) Text(rest, style = T.meta, color = p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.padding(top = 1.dp))
        }
    }
}

/**
 * An ended session takes one line (client-v27): the title, and at the right
 * when it ended, after the project where the list spans projects. No dot:
 * the group's title above says these have ended. The time is the time of
 * day for today and yesterday, the weekday within a week, else the date.
 */
@Composable
fun EndedRow(s: SessionView, t: Long, project: Boolean = false, selected: Boolean = false) {
    val p = pal
    val nav = LocalNav.current
    val time = endedTime(endedMs(s), t)
    val base = Modifier.fillMaxWidth().background(if (selected) p.blueSoft else Color.Transparent).clickableRow { nav.session(s.id) }.heightIn(min = 44.dp)
        .padding(start = 35.dp, end = 16.dp)
    val right: @Composable () -> Unit = {
        Row(verticalAlignment = Alignment.CenterVertically) {
            if (project) Text(projectName(projectKey(s)) + if (time.isNotEmpty()) " · " else "", style = T.meta, color = p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.widthIn(max = 120.dp))
            Text(time, style = T.meta, color = p.muted, maxLines = 1)
        }
    }
    // At a large font size the two do not fit side by side: the time goes under the title, at the right.
    if (LocalDensity.current.fontScale >= 1.8f) {
        Column(base.padding(vertical = 6.dp)) {
            Text(titleOf(s), style = T.body, color = p.fg2, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.End) { right() }
        }
        return
    }
    Row(base, verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        Text(titleOf(s), style = T.body, color = p.fg2, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
        right()
    }
}

/**
 * A session that waits for the user, on a grey card. Approvals are decided
 * right here; questions, plans and popups open the session. The cards are
 * the whole account's, whatever the scope.
 */
@Composable
private fun AttentionCard(state: RemoteState, s: SessionView, t: Long, scope: HomeScope) {
    val p = pal
    val nav = LocalNav.current
    val remote = LocalRemote.current
    val st = statusIn(state, s)
    val events by remote.events(s.id).collectAsState()
    val d = rememberDerived(events.events, s.agent())
    val a = d.approvals.values.firstOrNull()
    Column(Modifier.padding(start = 12.dp, end = 12.dp, bottom = 8.dp).fillMaxWidth().clip(Radii.MD).background(p.soft).padding(horizontal = 13.dp, vertical = 11.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            Row(Modifier.weight(1f), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(5.dp)) {
            Dot(p.orangeInk, 8)
            Text(
                when (st) {
                    Status.APPROVAL -> if (a?.command.isNullOrEmpty()) "请求权限" else "请求执行"
                    Status.QUESTION -> "${agentName(s.agent())} 在问你"
                    else -> st.text
                },
                style = T.metaB, color = p.orangeInk, 
            )
            }
            Text(
                if (st == Status.APPROVAL && a != null && a.hasDeadline()) "剩 ${minutesLeft(a.deadline.seconds * 1000, t)} 分钟，超时拒绝" else whenText(s.updatedMs, t),
                style = T.meta, color = p.muted,
            )
        }
        Row(Modifier.padding(top = 4.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            Text(titleOf(s), style = T.bodyB, color = p.fg, modifier = Modifier.weight(1f), maxLines = 1, overflow = TextOverflow.Ellipsis)
            Box(Modifier.clickable { nav.session(s.id) }) { ArrowText(if (st == Status.APPROVAL) "查看会话" else if (st == Status.QUESTION) "去回答" else "去处理", p.blue, T.smB) }
        }
        val q = d.questions.values.firstOrNull()?.questionsList?.firstOrNull()?.text
        // Not scoped: a card from another project than the scope's says which (client-v27).
        val line = (cardMeta(state, s, scope) + listOfNotNull(q)).joinToString(" · ")
        if (line.isNotEmpty()) Text(line, style = T.meta, color = p.muted, maxLines = 2, overflow = TextOverflow.Ellipsis)
        if (st == Status.APPROVAL && a != null) {
            if (a.description.isNotEmpty()) Text(approvalDisplay(a.description), style = T.sm, color = p.fg2, modifier = Modifier.padding(top = 6.dp))
            if (a.command.isNotEmpty()) Text(
                approvalDisplay(a.command), style = T.mono, color = p.fg2, maxLines = 3, overflow = TextOverflow.Ellipsis,
                modifier = Modifier.padding(vertical = 8.dp).fillMaxWidth().clip(Radii.XS).background(p.card).border(1.dp, p.line, Radii.XS).padding(horizontal = 10.dp, vertical = 7.dp),
            )
            ApprovalButtons(s, a, compact = true, onCard = true)
        }
    }
}

fun Modifier.clickableRow(onClick: () -> Unit): Modifier = this.clickable(onClick = onClick)
