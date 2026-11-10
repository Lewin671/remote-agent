package io.github.lewin671.remoteagent.ui

import android.content.Context
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.MutableState
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.core.AccountEmpty
import io.github.lewin671.remoteagent.core.ENDED_PAGE
import io.github.lewin671.remoteagent.core.HomeEmpty
import io.github.lewin671.remoteagent.core.HomeHeadline
import io.github.lewin671.remoteagent.core.HomeItem
import io.github.lewin671.remoteagent.core.HomeScope
import io.github.lewin671.remoteagent.core.HomeShown
import io.github.lewin671.remoteagent.core.HomeTail
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.RowMeta
import io.github.lewin671.remoteagent.core.SessionView
import io.github.lewin671.remoteagent.core.agent
import io.github.lewin671.remoteagent.core.approvalCardText
import io.github.lewin671.remoteagent.core.decodeScope
import io.github.lewin671.remoteagent.core.encodeScope
import io.github.lewin671.remoteagent.core.homeHeadline
import io.github.lewin671.remoteagent.core.homeList
import io.github.lewin671.remoteagent.core.listedProjects
import io.github.lewin671.remoteagent.core.pendingRowText
import io.github.lewin671.remoteagent.core.projectName
import io.github.lewin671.remoteagent.core.projects
import io.github.lewin671.remoteagent.core.resolveScope
import io.github.lewin671.remoteagent.core.scopeKey
import io.github.lewin671.remoteagent.core.scopeRows
import kotlinx.coroutines.launch

/**
 * The home page's scope as this device remembers it (client-v27, architecture §8): kept with the
 * device's own settings and not in the account's preferences, because different devices often look
 * at different projects. Those settings are plaintext on disk, so a project is kept as a mark made
 * with the account's key ([encodeScope]) and never as its path. Nothing is remembered until the
 * user chose a scope; what the page then uses is resolveScope's answer. `root` is the account's
 * root key.
 */
object HomeScopes {
    private var stored: MutableState<String?>? = null

    private fun store(context: Context) =
        context.applicationContext.getSharedPreferences("home-scope", Context.MODE_PRIVATE)

    private fun stored(context: Context): MutableState<String?> =
        stored ?: mutableStateOf(store(context).getString("scope", null)).also { stored = it }

    /** The scope chosen, while the project it names is one of [state]'s lists. */
    fun remembered(context: Context, root: ByteArray, state: RemoteState): HomeScope? =
        decodeScope(stored(context).value, scopeKey(root), listedProjects(state))

    fun choose(context: Context, root: ByteArray, scope: HomeScope) {
        val mark = encodeScope(scope, scopeKey(root))
        stored(context).value = mark
        store(context).edit().putString("scope", mark).apply()
    }

    /** Forgotten with the account: the mark is one of its projects'. */
    fun clear(context: Context) {
        stored(context).value = null
        store(context).edit().clear().apply()
    }
}

/**
 * 主页 (client-v58 #home): one sentence of how things stand, then what waits for the whole
 * account (the most urgent approval as the dark card, the rest one row each), then what runs in
 * the scope and the ended sessions by day, a page at a time. The scope capsule at the top left and
 * the gear at the top right float over it; at the bottom the tabs and 新任务. The list's shape is
 * core's [homeList], the sentence [homeHeadline].
 */
@Composable
fun HomeScreen() {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val context = LocalContext.current
    val state by remote.state.collectAsState()
    val t = now()
    val scope = resolveScope(HomeScopes.remembered(context, remote.account.localKey, state), state)
    // What is typed stays in memory: saved instance state is plaintext on disk (architecture §15).
    var query by remember(scope) { mutableStateOf("") }
    val wide = LocalConfiguration.current.screenWidthDp >= 600
    var shown by remember(scope) { mutableStateOf(HomeShown()) }
    // A wide window lists all of what waits and runs (design-system §6 「长内容」).
    val list = remember(state, scope, query, shown, t, wide) {
        homeList(state, scope, query, if (wide) shown.copy(allAttention = true, allRunning = true) else shown, t)
    }
    val headline = remember(state, scope) { homeHeadline(state, scope) }
    var pick by rememberSaveable { mutableStateOf(false) }
    var menu by rememberSaveable { mutableStateOf(false) }
    var details by remember { mutableStateOf(false) }
    var updating by remember { mutableStateOf(false) }
    // Without any project, removed ones included, there is nothing to scope by.
    val anyProject = remember(state) { projects(state).isNotEmpty() }
    FetchEarlier(state, page = list.wantPage, all = list.wantAll)
    // A new session needs the server and a computer to run on.
    val canStart = !state.unlinked && state.machines.isNotEmpty()
    ShellPage(
        Modifier.fillMaxSize(),
        top = {
            Box(Modifier.weight(1f)) { if (anyProject) ScopeCapsule(scope) { menu = true } }
            GlassButton(Lucide.Settings, "设置", nav.settings)
        },
        bottom = {
            ShellBottom(
                ShellTab.SESSIONS, "新任务", Lucide.SquarePen, enabled = canStart,
                // A project as the scope is where the new session goes; every project asks which first.
                onAction = { if (scope is HomeScope.Of) nav.projectNew(scope.key) else pick = true },
            )
        },
    ) {
        LazyColumn(
            Modifier.fillMaxSize().align(Alignment.TopCenter).widthIn(max = 760.dp).testTag("home-list"),
            contentPadding = shellPadding(),
        ) {
            item(key = "headline") {
                Column {
                    if (headline != null) Headline(headline) { details = true }
                    UpdateNotice(Modifier.padding(start = 16.dp, end = 16.dp, top = 10.dp)) { updating = true }
                    if (list.showFilter) FilterField(query, { query = it }, Modifier.padding(start = 16.dp, end = 16.dp, top = 18.dp))
                }
            }
            when (val e = list.empty) {
                // Out of reach of the server the sentence says why nothing is here.
                HomeEmpty.Loading -> if (headline == null) item(key = "loading") { Skeleton() }
                is HomeEmpty.Account -> item(key = "empty") { EmptyAccount(state, e.why) }
                HomeEmpty.Scope -> item(key = "empty") { EmptyNote("点右下角的「新任务」在这里开始一个。") }
                null -> {}
            }
            items(list.items, key = { it.id }) { item ->
                HomeLine(state, item, list.meta, scope, t, list.grows) { shown = it(shown) }
            }
        }
    }
    if (updating) UpdateSheet { updating = false }
    if (details) ServerDetails(state) { details = false }
    if (pick) PickProjectSheet(state) { pick = false }
    if (menu) ScopeSheet(state, scope, onPick = { HomeScopes.choose(context, remote.account.localKey, it) }) { menu = false }
}

/** One line of the list; [change] opens more of a part ([HomeShown]). */
@Composable
private fun HomeLine(
    state: RemoteState,
    item: HomeItem,
    meta: RowMeta,
    scope: HomeScope,
    t: Long,
    grows: Boolean,
    change: ((HomeShown) -> HomeShown) -> Unit,
) {
    val nav = LocalNav.current
    val remote = LocalRemote.current
    val coroutines = rememberCoroutineScope()
    when (item) {
        is HomeItem.AttentionCard -> {
            val a = rememberApproval(item.session)
            // Until the approval itself is read it is a row like the rest.
            if (a == null) PendingItem(state, item.session, scope, first = true, last = true)
            else androidx.compose.runtime.key(a.approvalId) {
                // Keyed by approval: the next one must not inherit this one's state.
                ApprovalCard(
                    state, item.session, a, approvalCardText(state, item.session, a, timeoutNote = true, now = t), t,
                    onOpen = { nav.session(item.session.id) },
                    modifier = Modifier.padding(start = 16.dp, end = 16.dp, top = 20.dp),
                )
            }
        }
        is HomeItem.Attention -> PendingItem(state, item.session, scope, item.first, item.last)
        is HomeItem.MoreAttention -> MoreRow("还有 ${item.count} 件") { change { it.copy(allAttention = true) } }
        is HomeItem.Label -> GroupTitle(item.title, date = item.key != "running")
        is HomeItem.Running -> SessionRow(state, item.session, t, meta, last = item.last)
        is HomeItem.MoreRunning -> ListMore("还有 ${item.count} 个", { change { it.copy(allRunning = true) } })
        is HomeItem.Ended -> EndedRow(item.session, t, project = meta.project)
        is HomeItem.Tail -> {
            // Reaching the end shows the next page, once the ones before it are all rendered.
            if (grows) LaunchedEffect(Unit) { change { it.copy(ended = it.ended + ENDED_PAGE) } }
            when (val tail = item.tail) {
                is HomeTail.Text -> ListMore(tail.text, null, Modifier.testTag("timeline-tail"))
                is HomeTail.Earlier -> ListMore(
                    tail.text,
                    if (tail.retry) ({ coroutines.launch { runCatching { remote.loadMoreSessions() } } }) else null,
                    Modifier.testTag("timeline-tail"),
                )
            }
        }
    }
}

/** A row of what waits, from the session's own events: its approval or question. */
@Composable
private fun PendingItem(state: RemoteState, s: SessionView, scope: HomeScope, first: Boolean, last: Boolean) {
    val nav = LocalNav.current
    val remote = LocalRemote.current
    val events by remote.events(s.id).collectAsState()
    val d = rememberDerived(events, s.agent())
    val row = pendingRowText(
        state, s, scope, d.approvals.values.firstOrNull(),
        d.questions.values.firstOrNull()?.questionsList?.firstOrNull()?.text,
    )
    PendingRow(row, first, last) { nav.session(s.id) }
}

/**
 * The sentence at the top (design-system §6 大标题): 34 / 700, the count of what waits in orange,
 * and the line under it, 17 ink2: red when it is why the server cannot be reached with nothing to
 * show, a path in mono, and 详情 in blue when there is more to say about the connection.
 */
@Composable
private fun Headline(h: HomeHeadline, onDetails: () -> Unit) {
    val p = pal
    val count = (h.kind as? HomeHeadline.Kind.Waiting)?.count
    Text(
        buildAnnotatedString {
            if (count == null) append(h.title)
            else {
                withStyle(SpanStyle(color = p.orange)) { append("$count") }
                append(h.title.removePrefix("$count"))
            }
        },
        style = T.title, color = p.ink,
        modifier = Modifier.fillMaxWidth().padding(start = 24.dp, end = 24.dp, top = 6.dp).semantics { heading() }.testTag("headline"),
    )
    if (h.sub.isNotEmpty() || h.details) Row(
        Modifier.fillMaxWidth().padding(start = 24.dp, end = 24.dp, top = 7.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        if (h.sub.isNotEmpty()) Text(
            h.sub,
            style = if (h.subPath) T.mono13.copy(fontSize = 15.sp, lineHeight = 21.sp) else T.s17,
            color = if (h.subError) p.red else p.ink2,
            modifier = Modifier.weight(1f, fill = false),
        )
        if (h.details) Box(
            Modifier.heightIn(min = 44.dp).clickable(role = Role.Button, onClick = onDetails).padding(start = 12.dp, end = 4.dp),
            contentAlignment = Alignment.Center,
        ) { Text("详情", style = T.s17, color = p.blue) }
    }
}

/** A sentence under the title of a page with nothing to list, 17 ink2. */
@Composable
private fun EmptyNote(text: String) {
    Text(text, style = T.s17.copy(lineHeight = 24.sp), color = pal.ink2, modifier = Modifier.padding(start = 24.dp, end = 24.dp, top = 14.dp))
}

/**
 * Under 还没有会话: what to do first, by what computers the account has. The bottom right's
 * 新任务 is the screen's one ink button; only with no computer yet is 添加设备 the thing to do, on
 * the authority (the others name it). Out of reach of the server the title says why.
 */
@Composable
internal fun EmptyAccount(state: RemoteState, why: AccountEmpty) {
    val nav = LocalNav.current
    when (why) {
        AccountEmpty.NoMachines -> if (state.isAuthority) Column {
            EmptyNote("这个账户还没有电脑。点「添加设备」生成配对码，再在电脑上用它运行 remote-agent account join。")
            PillButton("添加设备", { nav.pair() }, Modifier.padding(start = 24.dp, top = 20.dp), ButtonKind.INK, icon = Lucide.Plus)
        } else EmptyNote(noMachinesHint(state))
        AccountEmpty.NoneOnline -> EmptyNote("没有在线的电脑。在电脑上运行 remote-agent daemon。")
        is AccountEmpty.Ready -> EmptyNote("${why.machine} 在线，点右下角的「新任务」开始第一个会话。")
        AccountEmpty.Unlinked -> {}
    }
}

/**
 * The scope capsule (design-system §6 范围胶囊): glass, 44 tall, the project's avatar and name or
 * 全 and 全部项目, then ⌄; a tap opens the scope menu.
 */
@Composable
private fun ScopeCapsule(scope: HomeScope, modifier: Modifier = Modifier, onOpen: () -> Unit) {
    val p = pal
    GlassCapsule(onOpen, modifier.testTag("scope-title"), onClickLabel = "换范围") {
        ProjectAvatar((scope as? HomeScope.Of)?.key.orEmpty(), 28, all = scope == HomeScope.All)
        Spacer(Modifier.size(7.dp))
        Text(scopeName(scope), style = T.s15b, color = p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
        Spacer(Modifier.size(4.dp))
        Ic(Lucide.ChevronDown, 15.dp, p.ink3)
    }
}

private fun scopeName(scope: HomeScope): String =
    when (scope) {
        HomeScope.All -> "全部项目"
        is HomeScope.Of -> projectName(scope.key)
    }

/**
 * 看哪个项目 (client-v58 #home): 全部项目 when there is more than one project, then the projects as
 * every list of them draws them, a blue ✓ on the current scope; a search above more than eight.
 * 新项目… picks a machine and a folder in the same sheet; 管理项目 goes to 项目管理.
 */
@Composable
private fun ScopeSheet(state: RemoteState, scope: HomeScope, onPick: (HomeScope) -> Unit, onClose: () -> Unit) {
    val p = pal
    val nav = LocalNav.current
    var browsing by remember { mutableStateOf(false) }
    Sheet(onClose, color = p.gbg) {
        if (browsing) {
            SheetHead("新项目 · 选目录", "取消", onClose, onBack = { browsing = false })
            DirPicker(state) { machine, path ->
                onClose()
                nav.newProjectAt(machine, path)
            }
            return@Sheet
        }
        Box(Modifier.fillMaxWidth().padding(bottom = 10.dp)) {
            Text("看哪个项目", style = T.s17b, color = p.ink, textAlign = TextAlign.Center, modifier = Modifier.align(Alignment.Center))
            Box(Modifier.align(Alignment.CenterEnd).heightIn(min = 44.dp).clickable(role = Role.Button, onClick = onClose), contentAlignment = Alignment.Center) {
                Text("取消", style = T.s17, color = p.blue)
            }
        }
        @Composable
        fun check(on: Boolean) = Box(Modifier.size(44.dp).semantics { if (on) contentDescription = "当前范围" }, contentAlignment = Alignment.Center) {
            if (on) Ic(Lucide.Check, 18.dp, p.blue)
        }
        Column(Modifier.fillMaxWidth().clip(Radii.R20).background(p.raised).padding(start = 18.dp, end = 4.dp)) {
            ProjectList(
                state,
                onNew = { browsing = true },
                first = {
                    // 全部项目, when there is more than one project to choose between.
                    scopeRows(state, scope).firstOrNull { it.scope == HomeScope.All }?.let { all ->
                        Row(
                            Modifier.fillMaxWidth().heightIn(min = 52.dp).bottomLine(p.sep).clickable(role = Role.Button) {
                                onPick(all.scope)
                                onClose()
                            }.padding(vertical = 6.dp),
                            verticalAlignment = Alignment.CenterVertically,
                            horizontalArrangement = Arrangement.spacedBy(12.dp),
                        ) {
                            ProjectAvatar("", 28, all = true)
                            Column(Modifier.weight(1f)) {
                                Text(all.name, style = T.s17b, color = p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                                Text(all.detail, style = T.s13, color = p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
                            }
                            check(all.current)
                        }
                    }
                },
            ) { pr ->
                ProjectRow(state, pr, onClick = {
                    onPick(HomeScope.Of(pr.key))
                    onClose()
                }) { check(scope == HomeScope.Of(pr.key)) }
            }
        }
        Row(
            Modifier.padding(top = 12.dp).fillMaxWidth().clip(Radii.R20).background(p.raised).heightIn(min = 50.dp)
                .clickable(role = Role.Button) {
                    onClose()
                    nav.manageProjects()
                }.padding(start = 18.dp, end = 16.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text("管理项目", style = T.s17, color = p.ink, modifier = Modifier.weight(1f))
            Ic(Lucide.ChevronRight, 18.dp, p.ink3)
        }
    }
}
