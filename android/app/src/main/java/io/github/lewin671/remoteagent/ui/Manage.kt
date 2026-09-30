package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.lazy.items
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.unit.sp
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.MoreVert
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
import io.github.lewin671.remoteagent.core.PrefsOp
import io.github.lewin671.remoteagent.core.Project
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.errorText
import io.github.lewin671.remoteagent.core.projectGroups
import io.github.lewin671.remoteagent.core.projectName
import io.github.lewin671.remoteagent.core.shortPath
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

/**
 * Changes to the project preferences in flight, per project path, for this
 * run: the row says 保存中 until the server has it, and a failure leaves
 * the list as it was with a retry.
 */
object PrefsChanges {
    sealed interface State {
        data object Saving : State
        data class Failed(val op: PrefsOp, val error: String) : State
    }

    val states = mutableStateMapOf<String, State>()

    /** The last project removed from the list here, for 撤销 while it shows. */
    var undo by mutableStateOf<String?>(null)

    fun run(remote: Remote, op: PrefsOp, onDone: () -> Unit = {}) {
        if (states[op.path] == State.Saving) return
        states[op.path] = State.Saving
        RaApp.instance.scope.launch {
            try {
                remote.changePrefs(op)
                states.remove(op.path)
                onDone()
            } catch (e: Exception) {
                states[op.path] = State.Failed(op, errorText(e))
            }
        }
    }

    fun clear() {
        states.clear()
        undo = null
    }
}

/** Why the preferences cannot be changed now, or null when they can. */
fun prefsBlocked(state: RemoteState): String? = when {
    !state.prefsSupported -> "服务器还不支持同步项目偏好，更新服务器后才能管理"
    state.conn != ConnState.ONLINE -> "当前显示缓存 · 连上后才能管理"
    state.prefs == null -> "正在读取项目偏好…"
    else -> null
}

/** 项目管理: pinned in their order, the others by activity; only the entries change, never folders on the computer. */
@Composable
fun ManageProjectsScreen() {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val p = pal
    val g = projectGroups(state)
    val blocked = prefsBlocked(state)
    // A change from another client while this page is open is said once.
    var seenRev by remember { mutableStateOf(state.prefs?.revision) }
    var elsewhere by remember { mutableStateOf(false) }
    LaunchedEffect(state.prefs?.revision) {
        val rev = state.prefs?.revision
        if (seenRev != null && rev != null && rev != seenRev && PrefsChanges.states.isEmpty() && PrefsChanges.undo == null) elsewhere = true
        seenRev = rev
    }
    PageScaffold("项目管理") {
        LazyColumn(Modifier.weight(1f)) {
            item { Text("只管理项目入口，不移动或删除电脑上的目录。", style = T.sm, color = p.muted, modifier = Modifier.padding(start = 16.dp, end = 16.dp, top = 8.dp, bottom = 12.dp)) }
            if (blocked != null) item { Banner(blocked, Modifier.padding(horizontal = 12.dp, vertical = 4.dp)) }
            if (elsewhere) item { Banner("项目列表已在另一客户端更新", Modifier.padding(horizontal = 12.dp, vertical = 4.dp), action = "知道了", onAction = { elsewhere = false }) }
            if (g.pinned.isNotEmpty()) {
                item { Label("常用", g.pinned.size) }
                items(g.pinned, key = { "p" + it.key }) { pr -> ManageRow(state, pr, pinned = true, blocked = blocked != null) }
            }
            if (g.others.isNotEmpty()) {
                item { Label("其他项目", g.others.size) }
                items(g.others, key = { "o" + it.key }) { pr -> ManageRow(state, pr, pinned = false, blocked = blocked != null) }
            }
            item {
                if (g.hidden.isEmpty()) Label("没有已移除的项目")
                else Label("已移除 ${g.hidden.size} 个项目", action = "查看", onAction = { nav.hiddenProjects() })
            }
            item { Text("常用和已移除项目在账户内同步。断线时可查看缓存，修改暂停；不会影响会话和提醒。", style = T.sm, color = p.muted, modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) }
        }
        UndoBar(remote, state)
    }
}

@Composable
private fun ManageRow(state: RemoteState, pr: Project, pinned: Boolean, blocked: Boolean) {
    val p = pal
    val remote = LocalRemote.current
    var menu by remember { mutableStateOf(false) }
    val change = PrefsChanges.states[pr.key]
    Row(Modifier.fillMaxWidth().padding(start = 16.dp, end = 8.dp, top = 10.dp, bottom = 10.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
        Avatar(pr.key, 22)
        Column(Modifier.weight(1f)) {
            Text(pr.name, style = T.bodyB, color = p.fg, maxLines = 1, overflow = TextOverflow.Ellipsis)
            Text(shortPath(pr.key), style = T.meta, color = p.muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
            when (change) {
                PrefsChanges.State.Saving -> Text("◌ 正在同步…", style = T.meta, color = p.muted)
                is PrefsChanges.State.Failed -> Row(verticalAlignment = Alignment.CenterVertically) {
                    Text("没能保存，列表保持原样：${change.error}", style = T.meta, color = p.red, modifier = Modifier.weight(1f, fill = false))
                    Text(" · 重试", style = T.metaB, color = p.blue, modifier = Modifier.clickable(enabled = !blocked) { PrefsChanges.run(remote, change.op) })
                }
                null -> {}
            }
        }
        Box(Modifier.size(44.dp).clickable(enabled = !blocked && change != PrefsChanges.State.Saving) { menu = true }.alpha(if (blocked) .4f else 1f), contentAlignment = Alignment.Center) {
            Text("⋯", style = T.body, color = p.blue)
        }
    }
    if (menu) ActionSheet(
        info = {
            Column(horizontalAlignment = Alignment.CenterHorizontally) {
                Text(pr.name, style = T.metaB, color = p.muted)
                Text(shortPath(pr.key), style = T.mono, color = p.fg2)
            }
        },
        actions = listOf(
            SheetAction(if (pinned) "取消常用" else "设为常用") { PrefsChanges.run(remote, if (pinned) PrefsOp.Unpin(pr.key) else PrefsOp.Pin(pr.key)) },
            SheetAction("从项目列表移除", danger = true) { PrefsChanges.run(remote, PrefsOp.Hide(pr.key)) { PrefsChanges.undo = pr.key } },
        ),
        onDismiss = { menu = false },
    )
}

/** 已移除 billing · 撤销, for a while after removing; the removed list keeps it longer. */
@Composable
fun UndoBar(remote: Remote, state: RemoteState) {
    val key = PrefsChanges.undo ?: return
    LaunchedEffect(key) {
        delay(8_000)
        if (PrefsChanges.undo == key) PrefsChanges.undo = null
    }
    Banner(
        "已移除 ${projectName(key)}", Modifier.padding(horizontal = 12.dp, vertical = 8.dp),
        action = if (prefsBlocked(state) == null) "撤销" else null,
        onAction = {
            PrefsChanges.undo = null
            PrefsChanges.run(remote, PrefsOp.Unhide(key))
        },
    )
}

/** 已移除的项目: restore brings a project back among the others; it starts or resumes nothing. */
@Composable
fun HiddenProjectsScreen() {
    val remote = LocalRemote.current
    val state by remote.state.collectAsState()
    val p = pal
    val hidden = projectGroups(state).hidden
    val blocked = prefsBlocked(state)
    PageScaffold("已移除的项目") {
        LazyColumn(Modifier.weight(1f)) {
            item { Text("恢复只让项目重新出现在列表里，不会启动或恢复会话。", style = T.sm, color = p.muted, modifier = Modifier.padding(start = 16.dp, end = 16.dp, top = 8.dp, bottom = 12.dp)) }
            if (blocked != null) item { Banner(blocked, Modifier.padding(horizontal = 12.dp, vertical = 4.dp)) }
            if (hidden.isEmpty()) item { PageNote("没有已移除的项目") }
            items(hidden, key = { it.key }) { pr ->
                val change = PrefsChanges.states[pr.key]
                Row(Modifier.fillMaxWidth().padding(start = 16.dp, end = 8.dp, top = 10.dp, bottom = 10.dp), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                    Avatar(pr.key, 22)
                    Column(Modifier.weight(1f)) {
                        Text(pr.name, style = T.bodyB, color = p.fg)
                        Text(shortPath(pr.key), style = T.meta, color = p.muted)
                        if (change is PrefsChanges.State.Failed) Text("没能保存：${change.error}", style = T.meta, color = p.red)
                    }
                    if (change == PrefsChanges.State.Saving) Text("恢复中…", style = T.body, color = p.muted)
                    else TextAction("恢复", { PrefsChanges.run(remote, PrefsOp.Unhide(pr.key)) }, enabled = blocked == null, style = T.body)
                }
            }
            item { Text("已移除的项目不出现在项目列表和新建选择器中；进行中会话、待处理卡片和通知入口仍然可访问。", style = T.sm, color = p.muted, modifier = Modifier.padding(16.dp)) }
        }
    }
}
