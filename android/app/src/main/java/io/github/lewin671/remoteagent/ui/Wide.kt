package io.github.lewin671.remoteagent.ui

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
 * sidebar with the home list and the chosen session in the main area on a
 * sheet of paper. A session chosen from the list opens there instead of on
 * a page of its own; going back from it empties the main area.
 */
@Composable
fun HomeOrWide() {
    if (!wideWindow()) return HomeScreen()
    val nav = LocalNav.current
    val p = pal
    var selected by rememberSaveable { mutableStateOf<String?>(null) }
    Row(Modifier.fillMaxSize().background(p.gbg)) {
        CompositionLocalProvider(
            LocalNav provides nav.copy(session = { selected = it }, sessionTerminal = { OpenTerminal.requested[it] = true; selected = it }),
            LocalSelectedSession provides selected,
        ) {
            Box(Modifier.width(380.dp).fillMaxHeight()) { HomeScreen() }
        }
        Box(Modifier.weight(1f).fillMaxHeight().padding(top = 8.dp, end = 8.dp, bottom = 8.dp).clip(Radii.R24).background(p.paper)) {
            val sid = selected
            if (sid == null) Text("选一个会话，在这里看", style = T.s15, color = p.ink2, modifier = Modifier.align(Alignment.Center))
            else CompositionLocalProvider(LocalNav provides nav.copy(back = { selected = null })) { key(sid) { SessionScreen(sid) } }
        }
    }
}
