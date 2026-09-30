package io.github.lewin671.remoteagent.term

import android.graphics.Paint
import android.graphics.Typeface
import android.view.inputmethod.InputMethodManager
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
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import com.termux.view.TerminalView
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.ui.pal
import kotlinx.coroutines.launch
import kotlin.math.floor
import kotlin.math.max
import kotlin.math.min

/** Width of a monospace character as a fraction of the font size. */
private val charRatio: Float by lazy {
    val p = Paint().apply { typeface = Typeface.MONOSPACE; textSize = 100f }
    p.measureText("M") / 100f
}

private val lineRatio: Float by lazy {
    val p = Paint().apply { typeface = Typeface.MONOSPACE; textSize = 100f }
    p.fontSpacing / 100f
}

/** The largest font size in pixels at which [cols] columns fit in [width], measured as the renderer does. */
private fun fitFont(width: Float, cols: Int): Int {
    val p = Paint().apply { typeface = Typeface.MONOSPACE }
    var size = floor(width / (cols * charRatio)).toInt() + 1
    while (size > 4) {
        p.textSize = size.toFloat()
        if (p.measureText("X") * cols <= width) return size
        size--
    }
    return size
}

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
 * resize the remote window, only "适配我的屏幕" does.
 */
@Composable
fun TerminalPane(remote: Remote, sessionId: String, sessionRunning: Boolean, modifier: Modifier = Modifier) {
    val scope = rememberCoroutineScope()
    val term = remember(sessionId) { RemoteTerminal(remote, sessionId, scope) }
    DisposableEffect(term) {
        term.connect()
        onDispose { term.close() }
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
            var fontPx by remember { mutableIntStateOf(0) }
            val (cols, rows) = size
            // Keep the size chosen without the keyboard while it is up.
            if (!imeVisible || fontPx == 0) {
                val pad = with(density) { 8.dp.toPx() }
                val w = if (cols > 0) fitFont(widthPx - pad, cols) else 0
                val h = if (rows > 0) floor((heightPx - pad) / (rows * lineRatio)).toInt() else 0
                val maxPx = with(density) { 16.dp.toPx() }.toInt()
                val chosen = if (w > 0 && h > 0) min(w, h) else with(density) { 12.dp.toPx() }.toInt()
                fontPx = chosen.coerceIn(5, maxPx)
                val readable = with(density) { 12.dp.toPx() }
                term.fitSize = floor((widthPx - pad) / (readable * charRatio)).toInt() to floor((heightPx - pad) / (readable * lineRatio)).toInt()
            }
            val lineHeightPx = remember(fontPx) { max(1, kotlin.math.ceil(Paint().apply { typeface = Typeface.MONOSPACE; textSize = fontPx.toFloat() }.fontSpacing).toInt()) }
            val contentPx = max(heightPx, (rows.coerceAtLeast(1) + 1) * lineHeightPx)
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
                                    if (sessionRunning) "会话还在运行，聊天照常更新。" else "会话不在运行。",
                                    style = MaterialTheme.typography.bodyMedium, color = p.muted, textAlign = TextAlign.Center,
                                )
                                var details by remember { mutableIntStateOf(0) }
                                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                                    TextButton(onClick = { term.connect() }) { Text("重新连接") }
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
