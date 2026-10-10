package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.unit.sp
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.OutlinedTextField
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.HomeScope
import io.github.lewin671.remoteagent.core.PrefsOp
import io.github.lewin671.remoteagent.core.Project
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.errorText
import io.github.lewin671.remoteagent.core.projectGroups
import io.github.lewin671.remoteagent.core.projectName
import io.github.lewin671.remoteagent.core.isHidden
import io.github.lewin671.remoteagent.core.isPinned
import io.github.lewin671.remoteagent.core.project
import io.github.lewin671.remoteagent.core.projectDetail
import io.github.lewin671.remoteagent.core.shortPath
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

/**
 * Changes to the project preferences in flight, per project path, for this
 * run: the row says 正在同步 until the server has it, and a failure leaves
 * the list as it was with a retry.
 *
 * A project's changes are written one at a time, in the order they were
 * made: one made while another is on its way waits for it, and is never
 * dropped (pinning a project and then saving its settings are two changes).
 * One that fails stops those behind it. They wait with it and go once it is
 * tried again, by its 重试 or when another change is made.
 */
object PrefsChanges {
    sealed interface State {
        data object Saving : State
        data class Failed(val op: PrefsOp, val error: String) : State
    }

    /** How a project's changes stand: one is being written, or the one that failed. */
    val states = mutableStateMapOf<String, State>()

    private class Change(val op: PrefsOp, val write: suspend (PrefsOp) -> Unit, val onDone: () -> Unit)

    /** Each project's changes not yet on the server, oldest first; the first is the one [states] is about. */
    private val queues = HashMap<String, ArrayDeque<Change>>()

    /** Writes [op], after the changes to the same project that are still on their way. */
    fun run(remote: Remote, op: PrefsOp, onDone: () -> Unit = {}) = run(RaApp.instance.scope, op, onDone) { remote.changePrefs(it) }

    /** The same with the write given, which tests answer in the server's place. */
    fun run(scope: kotlinx.coroutines.CoroutineScope, op: PrefsOp, onDone: () -> Unit = {}, write: suspend (PrefsOp) -> Unit) {
        val path = op.path
        val failed = states[path] is State.Failed
        val queue = queues.getOrPut(path) { ArrayDeque() }
        if (failed && queue.firstOrNull()?.op == op) {
            // 重试: the change that failed, again, and then those that waited behind it.
        } else if (queue.lastOrNull()?.op != op) {
            // Twice the same in a row is one change.
            queue.addLast(Change(op, write, onDone))
        }
        if (states[path] != State.Saving) next(scope, path, queue)
    }

    /** Writes the oldest change of a project, and then the one after it. */
    private fun next(scope: kotlinx.coroutines.CoroutineScope, path: String, queue: ArrayDeque<Change>) {
        if (queues[path] !== queue) return
        val change = queue.firstOrNull()
        if (change == null) {
            queues.remove(path)
            states.remove(path)
            return
        }
        states[path] = State.Saving
        scope.launch {
            try {
                change.write(change.op)
            } catch (e: Exception) {
                if (queues[path] !== queue) return@launch
                states[path] = State.Failed(change.op, errorText(e))
                return@launch
            }
            if (queues[path] !== queue) return@launch
            queue.removeFirst()
            change.onDone()
            next(scope, path, queue)
        }
    }

    fun clear() {
        states.clear()
        queues.clear()
    }
}

/** Why the preferences cannot be changed now, or null when they can. */
fun prefsBlocked(state: RemoteState): String? = when {
    state.conn != ConnState.ONLINE -> "连上服务器后才能修改"
    state.prefs == null -> "正在读取项目列表…"
    else -> null
}

/**
 * Lists the folder at [path] on [machine] as a project before its first session, here and on
 * the account's other clients (client-v33). A folder removed from the list earlier is listed again.
 */
fun addProject(remote: Remote, state: RemoteState, machine: String, path: String) {
    if (isHidden(state, path)) PrefsChanges.run(remote, PrefsOp.Unhide(path))
    else if (project(state, path) == null && savedSettings(state, path) == null) PrefsChanges.run(remote, PrefsOp.SetSettings(path, machine))
}

/** One project in a list (client-v34): its mark, its name, and under it [projectDetail]; [end] at the right. */
@Composable
fun ProjectRow(state: RemoteState, pr: Project, onClick: () -> Unit, clickLabel: String? = null, end: @Composable () -> Unit) {
    val p = pal
    val remote = LocalRemote.current
    val change = PrefsChanges.states[pr.key]
    Row(Modifier.fillMaxWidth().heightIn(min = 52.dp).bottomLine(p.line), verticalAlignment = Alignment.CenterVertically) {
        Row(
            Modifier.weight(1f).heightIn(min = 52.dp).clickable(onClickLabel = clickLabel, onClick = onClick).padding(vertical = 6.dp),
            verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            Avatar(pr.key, 22)
            Column(Modifier.weight(1f)) {
                Text(pr.name, style = T.bodyB, color = p.fg, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Text(projectDetail(state, pr), style = T.meta, color = p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
                when (change) {
                    PrefsChanges.State.Saving -> IconText(Lucide.LoaderCircle, "正在同步…", p.muted, T.meta)
                    is PrefsChanges.State.Failed -> Row(verticalAlignment = Alignment.CenterVertically) {
                        Text("没能保存，列表保持原样：${change.error}", style = T.meta, color = p.red, modifier = Modifier.weight(1f, fill = false))
                        Text(" · 重试", style = T.metaB, color = p.blue, modifier = Modifier.clickable(enabled = prefsBlocked(state) == null) { PrefsChanges.run(remote, change.op) })
                    }
                    null -> {}
                }
            }
        }
        end()
    }
}

/**
 * The projects on the list, the same in every place that shows them (client-v34): 常用 then
 * 其他, titled only when there are both, a search above more than eight, and 新项目… at the
 * end. [first] goes above the projects while nothing is searched for.
 */
@Composable
fun ProjectList(state: RemoteState, onNew: () -> Unit, first: (@Composable () -> Unit)? = null, row: @Composable (Project) -> Unit) {
    val p = pal
    val g = projectGroups(state)
    var search by remember { mutableStateOf("") }
    val q = search.trim()
    val match: (Project) -> Boolean = { q.isEmpty() || it.name.contains(q, true) || it.key.contains(q, true) }
    val pinned = g.pinned.filter(match)
    val others = g.others.filter(match)
    val titled = pinned.isNotEmpty() && others.isNotEmpty()
    if (g.pinned.size + g.others.size > 8) OutlinedTextField(search, { search = it }, label = { Text("搜索项目") }, singleLine = true,
        modifier = Modifier.fillMaxWidth().padding(bottom = 8.dp))
    if (q.isEmpty()) first?.invoke()
    if (titled) FieldLabel("常用")
    pinned.forEach { row(it) }
    if (titled) FieldLabel("其他")
    others.forEach { row(it) }
    if (q.isNotEmpty() && pinned.isEmpty() && others.isEmpty()) Hint("没有名字或路径里有「$q」的项目", Modifier.padding(bottom = 8.dp))
    Row(Modifier.fillMaxWidth().heightIn(min = 52.dp).clickable(role = androidx.compose.ui.semantics.Role.Button, onClick = onNew), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(12.dp)) {
        Box(Modifier.width(22.dp), contentAlignment = Alignment.Center) { Ic(Lucide.Plus, 18.dp, p.blue) }
        Text("新项目…", style = T.bodyB, color = p.blue)
    }
}

/** Asks before a project leaves the list: there is no undo, only adding it again. */
@Composable
fun RemoveProjectConfirm(key: String, onDismiss: () -> Unit, onRemoved: () -> Unit = {}) {
    val remote = LocalRemote.current
    Confirm(
        "移除 ${projectName(key)}？", "只从项目列表里移除，会话、历史和电脑上的目录都不动。以后要用，再用「新项目」添加这个目录。", "移除", danger = true,
        onDismiss = onDismiss,
    ) {
        onDismiss()
        PrefsChanges.run(remote, PrefsOp.Hide(key), onRemoved)
    }
}

/** 项目管理: the list, with ⋯ on each project for 常用 and 移除; only the entries change, never folders on the computer. */
@Composable
fun ManageProjectsScreen() {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val context = androidx.compose.ui.platform.LocalContext.current
    val state by remote.state.collectAsState()
    val p = pal
    val blocked = prefsBlocked(state)
    var menu by remember { mutableStateOf<Project?>(null) }
    var removing by remember { mutableStateOf<String?>(null) }
    var adding by remember { mutableStateOf(false) }
    // A change from another client while this page is open is said once.
    var seenRev by remember { mutableStateOf(state.prefs?.revision) }
    var elsewhere by remember { mutableStateOf(false) }
    LaunchedEffect(state.prefs?.revision) {
        val rev = state.prefs?.revision
        if (seenRev != null && rev != null && rev != seenRev && PrefsChanges.states.isEmpty()) elsewhere = true
        seenRev = rev
    }
    PageScaffold("项目管理") {
        Column(Modifier.weight(1f).verticalScroll(rememberScrollState()).padding(horizontal = 16.dp)) {
            if (elsewhere) Banner("项目列表已在另一客户端更新", Modifier.padding(vertical = 4.dp), action = "知道了", onAction = { elsewhere = false })
            ProjectList(state, onNew = { adding = true }) { pr ->
                // The name makes the project the home page's scope and goes there (client-v27); it only changes this device.
                ProjectRow(state, pr, onClick = { HomeScopes.choose(context, remote.account.root, HomeScope.Of(pr.key)); nav.home() }, clickLabel = "在主页看这个项目") {
                    Box(Modifier.size(44.dp).clickable(enabled = PrefsChanges.states[pr.key] != PrefsChanges.State.Saving) { menu = pr }, contentAlignment = Alignment.Center) {
                        Ic(Lucide.Ellipsis, 20.dp, p.blue, contentDescription = "管理 ${pr.name}")
                    }
                }
            }
            Hint("移除只是不再列出这个项目，会话和电脑上的目录都不动。", Modifier.padding(top = 4.dp, bottom = 16.dp))
        }
    }
    menu?.let { pr ->
        val pinned = isPinned(state, pr.key)
        ActionSheet(
            info = {
                Column(horizontalAlignment = Alignment.CenterHorizontally) {
                    Text(pr.name, style = T.metaB, color = p.muted)
                    Text(shortPath(pr.key), style = T.mono, color = p.fg2)
                }
            },
            actions = listOf(
                SheetAction(if (pinned) "取消常用" else "设为常用", enabled = blocked == null, note = blocked.orEmpty()) { PrefsChanges.run(remote, if (pinned) PrefsOp.Unpin(pr.key) else PrefsOp.Pin(pr.key)) },
                SheetAction("移除项目…", danger = true, enabled = blocked == null) { removing = pr.key },
            ),
            onDismiss = { menu = null },
        )
    }
    removing?.let { key -> RemoveProjectConfirm(key, onDismiss = { removing = null }) }
    if (adding) Sheet({ adding = false }) {
        SheetHead("新项目 · 选目录", "取消", { adding = false })
        DirPicker(state) { machine, path -> addProject(remote, state, machine, path); adding = false }
    }
}
