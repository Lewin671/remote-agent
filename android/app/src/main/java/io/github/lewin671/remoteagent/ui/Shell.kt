package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxScope
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.asPaddingValues
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.navigationBars
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawing
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBars
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.layout.wrapContentSize
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.foundation.layout.only
import androidx.compose.foundation.layout.WindowInsetsSides
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.selection.selectableGroup
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.core.scheduleBadgeText
import io.github.lewin671.remoteagent.core.waitingCount

/*
 * The app's shell on client-v58 (design-system §4 材质, §6 底部): no top bar
 * and no tab bar. A first-level page (会话, 定时) runs under the system bars
 * and fades out at both edges; over it float the glass at the top (the scope
 * capsule and the gear on the home) and at the bottom the two tabs in a
 * glass capsule with the ink new button beside them. 设置 is not a tab: the
 * home's gear opens it as a page with a way back.
 */

/** The two tabs at the bottom. */
enum class ShellTab(val route: String, val label: String) {
    SESSIONS("home", "会话"),
    SCHEDULES("schedules", "定时"),
}

/** How far a first-level page's content stays clear of the bottom, past the navigation bar: the tabs and their margin. */
val BottomBarSpace = 110.dp

/** How far below the status bar a first-level page's content starts: the glass at the top and its margin. */
val TopBarSpace = 58.dp

/** The content padding of a first-level page that runs under the bars and the glass on them. */
@Composable
fun shellPadding(top: Boolean = true): PaddingValues {
    val status = WindowInsets.statusBars.asPaddingValues().calculateTopPadding()
    val nav = WindowInsets.navigationBars.asPaddingValues().calculateBottomPadding()
    return PaddingValues(top = status + if (top) TopBarSpace else 0.dp, bottom = nav + BottomBarSpace)
}

/**
 * A first-level page: [content] draws the page from the very top to the very
 * bottom, and gets the fades over both edges, [top] (the glass row at the
 * top, if any) and the [bottom] bar over them.
 */
@Composable
fun ShellPage(
    modifier: Modifier = Modifier,
    top: (@Composable RowScope.() -> Unit)? = null,
    bottom: @Composable () -> Unit,
    content: @Composable BoxScope.() -> Unit,
) {
    val p = pal
    Box(modifier.background(p.paper).windowInsetsPadding(WindowInsets.safeDrawing.only(WindowInsetsSides.Horizontal))) {
        content()
        EdgeFade(top = true, extra = if (top != null) 66.dp else 12.dp)
        EdgeFade(top = false, extra = 100.dp)
        if (top != null) Row(
            Modifier.align(Alignment.TopCenter).fillMaxWidth().windowInsetsPadding(WindowInsets.statusBars)
                .padding(start = 16.dp, end = 16.dp, top = 4.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(10.dp),
            content = top,
        )
        Box(Modifier.align(Alignment.BottomCenter).fillMaxWidth().windowInsetsPadding(WindowInsets.navigationBars)) { bottom() }
    }
}

/**
 * The bottom of 会话 and 定时: the two tabs in a glass capsule (an icon and a
 * 10 label each, the chosen one on fill2) and at the right the ink button
 * that says what it makes. [waiting] is the count on 会话: what waits for the
 * user, which stays while the server is out of reach; 定时 has the count of
 * stopped tasks, or a blue dot for new results.
 */
@Composable
fun BottomBar(
    active: ShellTab,
    waiting: Int,
    scheduled: Int,
    newResults: Boolean,
    onSelect: (ShellTab) -> Unit,
    action: String,
    actionIcon: ImageVector,
    onAction: () -> Unit,
    actionEnabled: Boolean,
) {
    val p = pal
    Row(
        Modifier.fillMaxWidth().padding(start = 20.dp, end = 20.dp, bottom = 12.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Row(Modifier.glass(p, Radii.Pill).padding(4.dp).selectableGroup()) {
            for (tab in ShellTab.entries) {
                val on = tab == active
                val count = if (tab == ShellTab.SESSIONS) waiting else scheduled
                val spoken = when {
                    count > 0 && tab == ShellTab.SESSIONS -> "，$count 件等你处理"
                    count > 0 -> "，$count 件需要处理"
                    tab == ShellTab.SCHEDULES && newResults -> "，有新结果"
                    else -> ""
                }
                Box(
                    Modifier.widthIn(min = 76.dp).heightIn(min = 52.dp).clip(Radii.Pill).background(if (on) p.fill2 else Color.Transparent)
                        .selectable(selected = on, role = Role.Tab) { if (!on) onSelect(tab) }
                        .semantics(mergeDescendants = true) { contentDescription = tab.label + spoken },
                    contentAlignment = Alignment.Center,
                ) {
                    Column(horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(2.dp)) {
                        // The badge hangs off the icon's top right, wherever the label puts it.
                        Box {
                            Ic(if (tab == ShellTab.SESSIONS) Lucide.MessageCircle else Lucide.Clock, 22.dp, if (on) p.ink else p.ink2)
                            Box(Modifier.align(Alignment.TopStart).offset(x = 15.dp, y = (-6).dp).wrapContentSize(unbounded = true, align = Alignment.TopStart)) {
                                if (count > 0) TabBadge(count)
                                else if (tab == ShellTab.SCHEDULES && newResults) Box(Modifier.offset(x = 3.dp, y = 3.dp).size(9.dp).clip(CircleShape).background(p.blue))
                            }
                        }
                        Text(tab.label, style = T.s10, color = if (on) p.ink else p.ink2)
                    }
                }
            }
        }
        Spacer(Modifier.weight(1f).widthIn(min = 12.dp))
        InkFab(action, actionIcon, onAction, enabled = actionEnabled)
    }
}

/**
 * The orange count on a tab (design-system §6 底部): orange-ink with white in
 * the light theme, orange with dark digits in the dark one, so it reads in
 * both; 99+ past 99.
 */
@Composable
fun TabBadge(count: Int) {
    val p = pal
    Box(
        Modifier.heightIn(min = 18.dp).widthIn(min = 18.dp).clip(RoundedCornerShape(9.dp)).background(if (p.dark) p.orange else p.orangeInk)
            .padding(horizontal = 5.dp),
        contentAlignment = Alignment.Center,
    ) {
        Text(
            scheduleBadgeText(count),
            style = ts(12.sp, FontWeight.Bold, 1.5f),
            color = if (p.dark) Color(0xFF1D1D1F) else Color.White,
            maxLines = 1,
        )
    }
}

/** The top of a page that is not first-level: the glass back button over the content. */
@Composable
fun BackRow(onBack: () -> Unit, modifier: Modifier = Modifier, trailing: (@Composable RowScope.() -> Unit)? = null) {
    Row(modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, top = 4.dp, bottom = 4.dp), verticalAlignment = Alignment.CenterVertically) {
        GlassButton(Lucide.ChevronLeft, "返回", onBack)
        Spacer(Modifier.weight(1f))
        trailing?.invoke(this)
    }
}

/**
 * [BottomBar] with its counts read from the account: what waits (kept while the server is out of
 * reach) on 会话, the stopped tasks or new results on 定时.
 */
@Composable
fun ShellBottom(active: ShellTab, action: String, icon: ImageVector, enabled: Boolean, onAction: () -> Unit) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val scheduled by remote.schedules.badge.collectAsState()
    val newResults by remote.schedules.hasNewResults.collectAsState()
    BottomBar(
        active, waitingCount(state), scheduled, newResults,
        onSelect = { if (it == ShellTab.SESSIONS) nav.home() else nav.schedules() },
        action = action, actionIcon = icon, onAction = onAction, actionEnabled = enabled,
    )
}
