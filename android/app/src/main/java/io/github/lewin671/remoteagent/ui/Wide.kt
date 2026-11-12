package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.compositionLocalOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.unit.dp

/** The session shown beside the home list in a wide window, which its row marks. */
val LocalSelectedSession = compositionLocalOf<String?> { null }

/** Whether the window is wide enough for the sidebar and a main area (design-system §6 宽屏): 840. */
@Composable
fun wideWindow(): Boolean = LocalConfiguration.current.screenWidthDp.dp >= WIDE_WINDOW_MIN_WIDTH

/**
 * The home page, or in a wide window (a tablet, a phone on its side) the
 * sidebar with the home list and, on a sheet of paper beside it, the main
 * area: the chosen session, or a new task (design-system §6: in a wide window
 * 新任务 is the main area itself, not a sheet). Going back from either
 * empties the main area.
 */
@Composable
fun HomeOrWide() {
    if (!wideWindow()) return HomeScreen()
    val nav = LocalNav.current
    val p = pal
    // "s" + a session ID, "n" + a project key, or "p" + machine, newline, folder.
    var main by rememberSaveable { mutableStateOf<String?>(null) }
    val here = nav.copy(
        session = { main = "s$it" },
        startedSession = { main = "s$it" },
        sessionTerminal = { OpenTerminal.requested[it] = true; main = "s$it" },
        project = { main = "n$it" },
        projectNew = { main = "n$it" },
        newProjectAt = { machine, path -> main = "p$machine\n$path" },
    )
    Row(Modifier.fillMaxSize().background(p.gbg)) {
        CompositionLocalProvider(LocalNav provides here, LocalSelectedSession provides main?.takeIf { it.startsWith("s") }?.drop(1)) {
            Box(Modifier.width(380.dp).fillMaxHeight()) { HomeScreen() }
        }
        Box(Modifier.weight(1f).fillMaxHeight().padding(top = 8.dp, end = 8.dp, bottom = 8.dp).clip(Radii.R24).background(p.paper)) {
            val m = main
            CompositionLocalProvider(LocalNav provides here.copy(back = { main = null })) {
                key(m) {
                    val arg = m?.drop(1).orEmpty()
                    when (m?.firstOrNull()) {
                        's' -> SessionScreen(arg)
                        'n' -> AppSafeArea { ProjectScreen(arg, focus = true) }
                        'p' -> arg.split('\n', limit = 2).let { AppSafeArea { NewProjectAtScreen(it[0], it.getOrElse(1) { "" }) } }
                        else -> Text(uiText(UiMessages.homeAndroidSelectSession()), style = T.s15, color = p.ink2, modifier = Modifier.align(Alignment.Center))
                    }
                }
            }
        }
    }
}
