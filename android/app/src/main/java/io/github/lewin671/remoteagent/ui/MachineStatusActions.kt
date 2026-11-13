package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.core.DiskReminder
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.StatusText
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.cleanupDraft
import io.github.lewin671.remoteagent.core.diskReminders
import io.github.lewin671.remoteagent.core.recentMachineProject

/*
 * The other places 电脑状态 and 磁盘保护 show (ui-spec B1, B3, B4): the cards at the top of the home, the card that
 * replaces a start failing for lack of space, and 让 Agent 帮我清理.
 */

/** The home's reminder cards (界面稿「主页提醒」): one per computer under 10 GB, the tightest first. */
@Composable
fun DiskReminderCards(state: RemoteState, modifier: Modifier = Modifier) {
    val copy = uiText()
    val locale = LocalMessageLocale.current
    val nav = LocalNav.current
    val cards = remember(state, copy, locale) { diskReminders(state, StatusText(copy, locale)) { machineLabel(state, it) } }
    if (cards.isEmpty()) return
    Column(modifier.padding(start = 16.dp, end = 16.dp, top = 14.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
        for (c in cards) androidx.compose.runtime.key(c.machineId) { DiskReminderCard(c) { nav.machineStatus(c.machineId) } }
    }
}

@Composable
fun DiskReminderCard(c: DiskReminder, onOpen: () -> Unit) {
    val p = pal
    val copy = uiText()
    val colors = if (c.critical) listOf(Color(0xFFFF6B60), Color(0xFFE5342A)) else listOf(Color(0xFFFFB340), Color(0xFFF08A00))
    Row(
        Modifier.fillMaxWidth().clip(Radii.R22).background(p.gbg).clickable(role = Role.Button, onClick = onOpen)
            .padding(start = 13.dp, end = 14.dp, top = 13.dp, bottom = 13.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(13.dp),
    ) {
        Box(Modifier.size(36.dp).clip(androidx.compose.foundation.shape.RoundedCornerShape(9.dp)).background(Brush.linearGradient(colors)), contentAlignment = Alignment.Center) {
            Ic(Lucide.HardDrive, 19.dp, Color.White)
        }
        Column(Modifier.weight(1f)) {
            Text(copy(c.title), style = ts(16.sp, FontWeight.SemiBold), color = p.ink)
            Text(copy(c.detail), style = ts(14.sp), color = p.ink2)
        }
        Arrow(p.ink3, T.s15)
    }
}

/** 启动失败 for lack of space (界面稿「启动失败」): what it says, and the two ways on. [onRetry] null greys 重试. */
@Composable
fun DiskFullCard(onUsage: () -> Unit, onRetry: (() -> Unit)?, modifier: Modifier = Modifier) {
    val p = pal
    val copy = uiText()
    Column(modifier.fillMaxWidth().clip(Radii.R22).background(p.gbg)) {
        Column(Modifier.padding(start = 16.dp, end = 16.dp, top = 16.dp)) {
            Text(copy(UiMessages.machineStatusDiskFull()), style = ts(17.sp, FontWeight.SemiBold), color = p.ink)
            Text(copy(UiMessages.machineStatusLaunchFailedBody()), style = ts(15.sp, lineHeight = 1.45f), color = p.ink2, modifier = Modifier.padding(top = 3.dp, bottom = 14.dp))
        }
        Box(Modifier.fillMaxWidth().height1().background(p.sep))
        Row(Modifier.fillMaxWidth().heightIn(min = 46.dp)) {
            Box(Modifier.weight(1f).heightIn(min = 46.dp).clickable(role = Role.Button, onClick = onUsage).padding(horizontal = 8.dp), contentAlignment = Alignment.Center) {
                Text(copy(UiMessages.machineStatusViewUsage()), style = ts(17.sp), color = p.blue, textAlign = TextAlign.Center)
            }
            Box(Modifier.width(0.5.dp).heightIn(min = 46.dp).background(p.sep))
            Box(
                Modifier.weight(1f).heightIn(min = 46.dp).alpha(if (onRetry != null) 1f else .45f)
                    .clickable(enabled = onRetry != null, role = Role.Button) { onRetry?.invoke() }.padding(horizontal = 8.dp),
                contentAlignment = Alignment.Center,
            ) { Text(copy(UiMessages.commonRetry()), style = ts(17.sp, FontWeight.SemiBold), color = p.blue, textAlign = TextAlign.Center) }
        }
    }
}

private fun Modifier.height1(): Modifier = this.then(Modifier.heightIn(min = 0.5.dp, max = 0.5.dp))

/** The words of 让 Agent 帮我清理 for a computer with no project to start in yet, until the chooser's next new session. */
object CleanupDraft {
    private var pending: Pair<String, String>? = null

    fun hold(machine: String, prompt: String) {
        pending = machine to prompt
    }

    /** The held prompt if it is for [machine]; it is given out once. */
    fun take(machine: String): String? = pending?.takeIf { it.first == machine }?.also { pending = null }?.second

    fun clear() { pending = null }
}

/**
 * Readies the draft of 让 Agent 帮我清理 (ui-spec B4): the computer chosen, the project the computer used last, the
 * words in the box after what was already there. Nothing is sent. The project's key, or null (the prompt is held for the
 * chooser) when the computer has no project yet.
 */
fun prepareCleanupDraft(state: RemoteState, machine: String, prompt: String): String? {
    val project = state.machines[machine]?.meta?.let { recentMachineProject(it) }
    if (project == null) {
        CleanupDraft.hold(machine, prompt)
        return null
    }
    val seed = projectStart(state, project, machine = machine)
    val entry = Drafts.newSession(project, seed?.config, seed?.settled != false)
    projectConfig(state, project, null, machine)?.let { entry.config.choose(it) }
    entry.text.value = cleanupDraft(entry.text.value, prompt)
    return project
}
