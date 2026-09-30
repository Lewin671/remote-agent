package io.github.lewin671.remoteagent.term

import android.view.inputmethod.InputMethodManager
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.ime
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.content.res.ResourcesCompat
import com.termux.view.TerminalView
import io.github.lewin671.remoteagent.R
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.ui.pal
import kotlinx.coroutines.launch
import kotlin.math.max

/**
 * The key bar's keys (client-v2): the arrows and Enter on the first row with
 * Esc, Tab and Ctrl+C next to the keyboard toggle on the second; none hidden
 * in a scrolling row. An arrow is a letter sent as ESC [ x, or ESC O x when
 * the program asked for application cursor keys, as a keyboard would.
 */
internal sealed interface Key {
    val label: String

    data class Text(override val label: String, val seq: String) : Key
    data class Arrow(override val label: String, val letter: Char) : Key
}

internal val keyRows: List<List<Key>> = listOf(
    listOf(Key.Text("Esc", "\u001b"), Key.Arrow("←", 'D'), Key.Arrow("↑", 'A'), Key.Arrow("↓", 'B'), Key.Arrow("→", 'C'), Key.Text("↵", "\r")),
    listOf(Key.Text("Tab", "\t"), Key.Text("Ctrl+C", "\u0003")),
)

/** What a key sends; [appCursor] is the terminal's application cursor keys mode. */
internal fun keySequence(k: Key, appCursor: Boolean): String = when (k) {
    is Key.Text -> k.seq
    is Key.Arrow -> (if (appCursor) "\u001bO" else "\u001b[") + k.letter
}

/**
 * The live terminal of a session with its toolbar and key bar. The font is
 * scaled so the whole tmux window fits; the keyboard and rotation never
 * resize the remote window. Font changes refit only an already fitted window.
 */
@Composable
fun TerminalPane(remote: Remote, sessionId: String, sessionRunning: Boolean, modifier: Modifier = Modifier) {
    val scope = rememberCoroutineScope()
    val term = remember(remote, sessionId) { RemoteTerminal(remote, sessionId, scope) }
    val owner = androidx.lifecycle.compose.LocalLifecycleOwner.current
    val online = remote.state.collectAsStateWithLifecycle().value.conn == io.github.lewin671.remoteagent.core.ConnState.ONLINE
    DisposableEffect(term, owner, online) {
        val observer = androidx.lifecycle.LifecycleEventObserver { _, event ->
            when (event) {
                androidx.lifecycle.Lifecycle.Event.ON_START -> if (online && remote.state.value.conn == io.github.lewin671.remoteagent.core.ConnState.ONLINE) term.connect()
                androidx.lifecycle.Lifecycle.Event.ON_STOP -> term.close()
                else -> {}
            }
        }
        owner.lifecycle.addObserver(observer)
        onDispose { owner.lifecycle.removeObserver(observer); term.close() }
    }
    val state by term.state.collectAsState()
    val size by term.size.collectAsState()
    val fitted by term.fitted.collectAsState()
    val resized by term.resizedElsewhere.collectAsState()
    LaunchedEffect(resized) {
        if (resized) {
            kotlinx.coroutines.delay(10_000)
            term.dismissResized()
        }
    }
    val context = LocalContext.current
    val typeface = remember(context) { requireNotNull(ResourcesCompat.getFont(context, R.font.sarasa_fixed_sc_regular)) }
    val preferences = remember(context) { context.getSharedPreferences("terminal-display", android.content.Context.MODE_PRIVATE) }
    var fontDp by remember { mutableIntStateOf(preferences.getInt("font-dp", 14).coerceIn(10, 22)) }
    var refitAfterFontChange by remember { mutableStateOf(false) }
    fun changeFont(delta: Int) {
        fontDp = (fontDp + delta).coerceIn(10, 22)
        preferences.edit().putInt("font-dp", fontDp).apply()
        refitAfterFontChange = true
    }
    val hostView = LocalView.current
    val density = LocalDensity.current
    val imeVisible = WindowInsets.ime.getBottom(density) > 0
    val p = pal

    Column(modifier) {
        Row(
            Modifier.fillMaxWidth().background(p.soft).padding(horizontal = 12.dp).height(40.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            val (cols, rows) = size
            Text(
                when {
                    state != RemoteTerminal.State.Live -> "终端"
                    fitted -> "已适配这块屏幕 · ${cols}×$rows"
                    else -> "电脑上 ${cols}×$rows"
                },
                style = MaterialTheme.typography.bodyMedium,
                color = p.muted,
                modifier = Modifier.weight(1f),
            )
            if (state == RemoteTerminal.State.Live) {
                TextButton(onClick = { term.toggleFit() }) { Text(if (fitted) "还原" else "⤢ 适配我的屏幕") }
            }
        }
        Row(
            Modifier.fillMaxWidth().background(p.soft).height(40.dp).padding(horizontal = 12.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text("字号", style = MaterialTheme.typography.bodyMedium, color = p.muted, modifier = Modifier.weight(1f))
            TextButton(onClick = { changeFont(-1) }, enabled = fontDp > 10) { Text("A−") }
            Text("$fontDp", style = MaterialTheme.typography.bodyMedium, color = p.muted)
            TextButton(onClick = { changeFont(1) }, enabled = fontDp < 22) { Text("A+") }
        }
        if (resized) {
            Column(Modifier.fillMaxWidth().background(p.orangeSoft).padding(horizontal = 12.dp, vertical = 8.dp)) {
                Text("远端尺寸已变化", style = MaterialTheme.typography.titleSmall, color = p.orangeInk)
                Text(
                    "窗口尺寸已变化，可能来自电脑或另一客户端。这里按最新尺寸显示；打开键盘不改远端尺寸。",
                    style = MaterialTheme.typography.bodySmall, color = p.orangeInk,
                )
            }
        }
        BoxWithConstraints(Modifier.weight(1f).fillMaxWidth().background(Color.Black)) {
            val widthPx = constraints.maxWidth
            val heightPx = constraints.maxHeight
            val (cols, rows) = size
            // Opening the keyboard must neither shrink the font nor resize the shared window.
            var fullHeightPx by remember { mutableIntStateOf(heightPx) }
            // adjustResize can arrive before Compose's IME insets update. Read the
            // native insets too, so the smaller keyboard viewport never becomes our baseline.
            val keyboardVisible = imeVisible ||
                ViewCompat.getRootWindowInsets(hostView)?.isVisible(WindowInsetsCompat.Type.ime()) == true
            if (!keyboardVisible) fullHeightPx = heightPx
            val sizingHeight = fullHeightPx
            val pad = with(density) { 8.dp.roundToPx() }
            val preferredPx = with(density) { fontDp.dp.roundToPx() }
            val preferredGeometry = remember(typeface, preferredPx) { terminalGeometry(typeface, preferredPx) }
            term.fitSize = preferredGeometry.capacity(widthPx - pad, sizingHeight - pad)
            val fontPx = remember(typeface, preferredPx, widthPx, sizingHeight, cols, rows, pad) {
                terminalFontPixels(typeface, preferredPx, widthPx - pad, sizingHeight - pad, cols, rows)
            }
            LaunchedEffect(fontDp, refitAfterFontChange) {
                if (refitAfterFontChange) {
                    if (fitted) term.fitToScreen()
                    refitAfterFontChange = false
                }
            }
            val geometry = remember(typeface, fontPx) { terminalGeometry(typeface, fontPx) }
            val lineHeightPx = geometry.lineHeight
            val contentPx = max(heightPx, rows.coerceAtLeast(1) * lineHeightPx + geometry.topOffset)
            val scroll = rememberScrollState()
            fun followCursor() {
                val em = term.session.emulator ?: return
                val bottom = (em.cursorRow + 3) * lineHeightPx
                val target = (bottom - heightPx).coerceIn(0, scroll.maxValue)
                if (bottom > scroll.value + heightPx || em.cursorRow * lineHeightPx < scroll.value) scope.launch { scroll.scrollTo(target) }
            }
            LaunchedEffect(imeVisible, heightPx) { followCursor() }
            term.onScreen = { followCursor() }
            Box(Modifier.fillMaxSize().verticalScroll(scroll, enabled = false)) {
                AndroidView(
                    factory = { ctx ->
                        TerminalView(ctx, null).apply {
                            val v = this
                            setTerminalViewClient(RemoteTerminal.ViewClient {
                                v.requestFocus()
                                ctx.getSystemService(InputMethodManager::class.java).showSoftInput(v, 0)
                            })
                            isFocusable = true
                            isFocusableInTouchMode = true
                            setTextSize(fontPx)
                            setTypeface(typeface)
                            tag = fontPx
                            attachSession(term.session)
                            term.view = this
                        }
                    },
                    update = { v ->
                        if (v.tag != fontPx) {
                            v.setTextSize(fontPx)
                            v.tag = fontPx
                        }
                    },
                    modifier = Modifier.fillMaxWidth().height(with(density) { contentPx.toDp() }),
                )
            }
            if (state != RemoteTerminal.State.Live) {
                Surface(
                    Modifier.align(Alignment.Center).padding(24.dp),
                    shape = MaterialTheme.shapes.large,
                    color = p.card,
                ) {
                    Column(Modifier.padding(20.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                        when (val s = state) {
                            RemoteTerminal.State.Connecting -> Text("连接终端…", color = p.muted)
                            is RemoteTerminal.State.Closed -> {
                                Text("终端连接断开了", style = MaterialTheme.typography.titleSmall)
                                Text(
                                    if (!online) "等待服务器连接恢复后，会重新获取终端画面。" else if (sessionRunning) "会话还在运行，聊天照常更新。" else "会话不在运行。",
                                    style = MaterialTheme.typography.bodyMedium, color = p.muted, textAlign = TextAlign.Center,
                                )
                                var details by remember { mutableIntStateOf(0) }
                                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                    TextButton(onClick = { if (remote.state.value.conn == io.github.lewin671.remoteagent.core.ConnState.ONLINE) term.connect() }, enabled = online) { Text("重新连接") }
                                    TextButton(onClick = { details++ }) { Text("查看详情") }
                                }
                                if (details % 2 == 1) Text(s.reason, style = MaterialTheme.typography.bodySmall, color = p.muted, fontFamily = FontFamily.Monospace)
                            }
                            else -> {}
                        }
                    }
                }
            }
        }
        KeyBar(enabled = state == RemoteTerminal.State.Live, onKey = { term.sendText(keySequence(it, term.session.emulator?.isCursorKeysApplicationMode == true)) }, onKeyboard = {
            val v = term.view ?: return@KeyBar
            val imm = v.context.getSystemService(InputMethodManager::class.java)
            if (imeVisible) imm.hideSoftInputFromWindow(v.windowToken, 0) else {
                v.requestFocus()
                imm.showSoftInput(v, 0)
            }
        })
    }
}

@Composable
private fun KeyBar(enabled: Boolean, onKey: (Key) -> Unit, onKeyboard: () -> Unit) {
    val p = pal
    Column(Modifier.fillMaxWidth().background(p.soft)) {
        Row(Modifier.fillMaxWidth().height(44.dp)) {
            for (k in keyRows[0]) KeyButton(k.label, enabled, Modifier.weight(1f)) { onKey(k) }
        }
        Row(Modifier.fillMaxWidth().height(44.dp)) {
            for (k in keyRows[1]) KeyButton(k.label, enabled, Modifier.weight(1f)) { onKey(k) }
            KeyButton("⌨ 键盘", true, Modifier.weight(1f), onKeyboard)
        }
    }
}

@Composable
private fun KeyButton(label: String, enabled: Boolean, modifier: Modifier, onClick: () -> Unit) {
    val p = pal
    TextButton(
        onClick = onClick,
        enabled = enabled,
        modifier = modifier.height(44.dp),
        shape = MaterialTheme.shapes.small,
        contentPadding = androidx.compose.foundation.layout.PaddingValues(0.dp),
    ) {
        Text(label, fontFamily = FontFamily.Monospace, color = if (enabled) p.fg else p.faint, maxLines = 1)
    }
}
