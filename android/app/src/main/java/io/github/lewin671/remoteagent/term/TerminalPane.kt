package io.github.lewin671.remoteagent.term

import android.view.inputmethod.InputMethodManager
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.ui.draw.clip
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.ui.Ic
import io.github.lewin671.remoteagent.ui.IconText
import io.github.lewin671.remoteagent.ui.Lucide
import io.github.lewin671.remoteagent.ui.Radii
import io.github.lewin671.remoteagent.ui.T
import io.github.lewin671.remoteagent.ui.TermColors
import io.github.lewin671.remoteagent.ui.ts
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
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
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
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import com.termux.terminal.TerminalColors
import com.termux.view.TerminalView
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.ui.pal
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextOverflow
import io.github.lewin671.remoteagent.ui.CardButton
import io.github.lewin671.remoteagent.ui.CardColors
import io.github.lewin671.remoteagent.ui.Spinner
import io.github.lewin671.remoteagent.ui.disabledAlpha
import io.github.lewin671.remoteagent.ui.glass
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

    /** Drawn in place of [label], which then only names the key to a screen reader. */
    val icon: ImageVector?

    data class Text(override val label: String, val seq: String, override val icon: ImageVector? = null) : Key
    data class Arrow(override val label: String, val letter: Char, override val icon: ImageVector) : Key
}

internal val keyRows: List<List<Key>> = listOf(
    listOf(Key.Text("Esc", "\u001b"), Key.Arrow("左", 'D', Lucide.ArrowLeft), Key.Arrow("上", 'A', Lucide.ArrowUp), Key.Arrow("下", 'B', Lucide.ArrowDown), Key.Arrow("右", 'C', Lucide.ArrowRight), Key.Text("回车", "\r", Lucide.CornerDownLeft)),
    listOf(Key.Text("Tab", "\t"), Key.Text("Ctrl+C", "\u0003")),
)

/** What a key sends; [appCursor] is the terminal's application cursor keys mode. */
internal fun keySequence(k: Key, appCursor: Boolean): String = when (k) {
    is Key.Text -> k.seq
    is Key.Arrow -> (if (appCursor) "\u001bO" else "\u001b[") + k.letter
}

/** How far a pull on the live screen goes before it asks for a capture; the reader opens that far up. */
private const val ENTER_ROWS = 3

/** The 字号 (A−/A+) range in dp: the smallest doubles as the floor a wide terminal shrinks to (client-v41 §比屏幕宽的终端). */
internal const val FONT_MIN_DP = 6
internal const val FONT_MAX_DP = 22
internal const val FONT_DEFAULT_DP = 14

/** Clamps a stored or chosen font size into the A−/A+ range. */
internal fun clampFontDp(dp: Int): Int = dp.coerceIn(FONT_MIN_DP, FONT_MAX_DP)

/** Where the terminal view hands over the scroll gestures it does not apply itself. */
private class ScrollInbox {
    var onRows: (Int) -> Unit = {}
}

/**
 * The pull on the live screen that opens the reader (client-v40): how far it has gathered, and
 * whether it now asks for a fresh capture. It counts whole terminal rows, negative upward, as
 * the terminal view reports them, so a small drag that never reaches [ENTER_ROWS] leaves the
 * screen alone.
 */
internal class ReadingPull {
    /** Where the reader opens: that many rows above the newest line. */
    var rows = 0
        private set

    /** Forgets the pull: on leaving reading, and once a capture has failed. */
    fun reset() {
        rows = 0
    }

    /** Reads a scroll of [rowsDown]; true when a fresh capture should be asked for now. */
    fun onScroll(reading: TerminalHistory.State, rowsDown: Int): Boolean {
        // The reader is on screen: it scrolls itself, and nothing here opens one or asks for a capture.
        if (reading.visible && reading.entered) return false
        rows = (rows - rowsDown).coerceAtLeast(0)
        if (rows < ENTER_ROWS) return false
        // The pull opened the reader: it keeps its distance, which is where the reader opens.
        if (!reading.visible) return true
        // The first page is on its way, and pulling further only sizes the reader. A capture that
        // failed is the one case that is asked for again, by a pull of its own.
        if (!reading.failed || reading.pending.isNotEmpty()) return false
        reset()
        return true
    }
}

/** The terminal toolbar's left text, and whether it offers the fit button. */
internal data class TerminalToolbar(val text: String, val fit: Boolean)

/**
 * The toolbar of the live terminal. It only says 回看中 once the reader is on screen, and only
 * then does the fit button go away: while the first page is on its way, or after it failed, the
 * screen behind the grey line is still the live one.
 */
internal fun terminalToolbar(state: RemoteTerminal.State, reading: TerminalHistory.State, fitted: Boolean, resized: Boolean, cols: Int, rows: Int) =
    TerminalToolbar(
        text = when {
            reading.entered -> "回看中 · 只读"
            state != RemoteTerminal.State.Live -> "终端"
            resized -> "远端尺寸已变化 · ${cols}×$rows"
            fitted -> "已适配这块屏幕 · ${cols}×$rows"
            else -> "电脑上 ${cols}×$rows"
        },
        fit = state == RemoteTerminal.State.Live && !reading.entered,
    )

/**
 * The live terminal of a session with its toolbar and key bar. The font is
 * scaled so the whole tmux window fits. Opening it fits the remote window to
 * this screen; the keyboard and rotation never resize it. Font changes refit
 * only an already fitted window.
 *
 * The screen keeps no scrollback of its own: pulling up on it opens the reader
 * over it (client-v40 §terminal-history), which is where history is read.
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
    val history by term.history.state.collectAsState()
    val fitted by term.fitted.collectAsState()
    val resized by term.resizedElsewhere.collectAsState()
    LaunchedEffect(resized) {
        if (resized) {
            kotlinx.coroutines.delay(10_000)
            term.dismissResized()
        }
    }
    val context = LocalContext.current
    val typeface = remember(context) { terminalTypeface(context) }
    val preferences = remember(context) { context.getSharedPreferences("terminal-display", android.content.Context.MODE_PRIVATE) }
    var fontDp by remember { mutableIntStateOf(clampFontDp(preferences.getInt("font-dp", FONT_DEFAULT_DP))) }
    var refitAfterFontChange by remember { mutableStateOf(false) }
    fun changeFont(delta: Int) {
        fontDp = clampFontDp(fontDp + delta)
        preferences.edit().putInt("font-dp", fontDp).apply()
        refitAfterFontChange = true
    }
    val hostView = LocalView.current
    val density = LocalDensity.current
    val imeVisible = WindowInsets.ime.getBottom(density) > 0
    val p = pal

    // Reading: the pull that opens the reader, and the output that has arrived since it opened.
    // The live screen keeps running behind the reader, which never follows it.
    val pull = remember { ReadingPull() }
    var newOutput by remember { mutableStateOf(false) }
    val gestures = remember { ScrollInbox() }
    gestures.onRows = { rowsDown ->
        if (pull.onScroll(term.history.state.value, rowsDown)) {
            newOutput = false
            term.loadHistory(fresh = true)
        }
    }
    fun leaveReading() {
        pull.reset()
        newOutput = false
        term.history.close()
    }
    // A capture that came back empty, or failed, starts the pull over: asking again takes a fresh
    // pull, not the distance this one had already gathered.
    LaunchedEffect(history.visible, history.failed, history.entered) {
        if (!history.visible || (history.failed && !history.entered)) pull.reset()
    }
    // Reading is read-only, and the keyboard belongs to the live screen.
    LaunchedEffect(history.entered) {
        if (!history.entered) return@LaunchedEffect
        val v = term.view ?: return@LaunchedEffect
        v.clearFocus()
        (v.context.getSystemService(InputMethodManager::class.java)).hideSoftInputFromWindow(v.windowToken, 0)
    }

    Column(modifier) {
        // The chips say the size, and that it changed; nothing pops up over the screen. Dark glass, as the whole terminal is dark.
        Row(
            Modifier.fillMaxWidth().padding(start = 16.dp, end = 16.dp, top = 8.dp, bottom = 8.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            val (cols, rows) = size
            val toolbar = terminalToolbar(state, history, fitted, resized, cols, rows)
            Row(
                Modifier.weight(1f).heightIn(min = 38.dp).glass(p, Radii.Pill, dark = true).padding(start = 14.dp, end = 4.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text(toolbar.text, style = T.s13, color = CardColors.text.copy(alpha = .75f), maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                if (toolbar.fit) Box(Modifier.heightIn(min = 44.dp).clickable(role = Role.Button) { term.toggleFit() }.padding(horizontal = 10.dp), contentAlignment = Alignment.Center) {
                    if (fitted || resized) Text(if (fitted) "还原" else "适配", style = T.s13, color = CardColors.link)
                    else IconText(Lucide.Maximize2, "适配我的屏幕", CardColors.link, T.s13)
                }
            }
            Row(Modifier.heightIn(min = 38.dp).glass(p, Radii.Pill, dark = true).padding(horizontal = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                FontButton("A−", "字号减小", fontDp > FONT_MIN_DP) { changeFont(-1) }
                Text("$fontDp", style = T.s13, color = CardColors.text.copy(alpha = .75f), modifier = Modifier.semantics { contentDescription = "字号 $fontDp" })
                FontButton("A+", "字号增大", fontDp < FONT_MAX_DP) { changeFont(1) }
            }
        }
        BoxWithConstraints(Modifier.weight(1f).fillMaxWidth().background(TermColors.bg)) {
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
            // The smallest 字号, in pixels: a wide terminal shrinks to this and then pans.
            val minFontPx = with(density) { FONT_MIN_DP.dp.roundToPx() }
            val preferredGeometry = remember(typeface, preferredPx) { terminalGeometry(typeface, preferredPx) }
            term.fitSize = preferredGeometry.capacity(widthPx - pad, sizingHeight - pad)
            val fontPx = remember(typeface, preferredPx, widthPx, sizingHeight, cols, rows, pad, minFontPx) {
                terminalFontPixels(typeface, preferredPx, widthPx - pad, sizingHeight - pad, cols, rows, minFontPx)
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
            term.onScreen = {
                followCursor()
                if (term.history.state.value.entered) newOutput = true
            }
            Box(Modifier.fillMaxSize().verticalScroll(scroll, enabled = false)) {
                AndroidView(
                    factory = { ctx ->
                        TerminalView(ctx, null).apply {
                            val v = this
                            setTerminalViewClient(RemoteTerminal.ViewClient {
                                v.requestFocus()
                                ctx.getSystemService(InputMethodManager::class.java).showSoftInput(v, 0)
                            })
                            setScrollListener { rowsDown -> gestures.onRows(rowsDown) }
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
            if (history.visible && !history.entered) {
                // The first page is on its way, or failed: the live screen stays, with one grey
                // line over it, and its retry captures the terminal again from scratch.
                HistoryStatusLine(history, waiting = true, onRetry = { term.loadHistory(fresh = true) }, modifier = Modifier.align(Alignment.TopStart))
            }
            if (history.entered) {
                val palette = term.session.emulator?.mColors?.mCurrentColors ?: TerminalColors.COLOR_SCHEME.mDefaultColors
                // The reader pans sideways like the live screen: its cell width is the one the live view
                // draws at this font (measured the same way), so both stop at the same right edge, and it
                // opens at the column the live screen is panned to. Taken from the font being applied, not
                // the view, which only takes a new size after this composition.
                val cellWidth = geometry.cellWidth
                val liveGridWidthPx = gridWidthPx(cols.coerceAtLeast(1), cellWidth)
                TerminalHistoryReader(
                    history = history, typeface = typeface, palette = palette, fontPx = fontPx,
                    lineHeightPx = lineHeightPx, cellWidth = cellWidth, viewportPx = heightPx,
                    viewportWidthPx = widthPx, liveGridWidthPx = liveGridWidthPx, entryX = term.view?.panOffset ?: 0,
                    entryRows = pull.rows, onLoadOlder = { term.loadHistory() }, onRetry = { term.loadHistory() },
                    onLeave = { leaveReading() }, modifier = Modifier.fillMaxSize(),
                )
                HistoryPill(newOutput, { leaveReading() }, Modifier.align(Alignment.BottomEnd).padding(12.dp))
            }
            if (state != RemoteTerminal.State.Live && !history.visible) {
                Column(
                    Modifier.align(Alignment.BottomCenter).padding(16.dp).fillMaxWidth().glass(p, Radii.R28, dark = true).padding(18.dp),
                ) {
                    when (val s = state) {
                        RemoteTerminal.State.Connecting -> Row(verticalAlignment = Alignment.CenterVertically) {
                            Spinner(CardColors.text2, size = 15.dp)
                            Text("连接终端…", style = T.s15, color = CardColors.text2, modifier = Modifier.padding(start = 8.dp))
                        }
                        is RemoteTerminal.State.Closed -> {
                            Text("终端连接断开了", style = T.s17b, color = CardColors.text)
                            Text(
                                (if (!online) "等待服务器连接恢复后，会重新获取终端画面。" else if (sessionRunning) "下面是断开前的画面。会话还在运行，聊天照常更新。" else "会话不在运行。"),
                                style = T.s15, color = CardColors.text.copy(alpha = .7f), modifier = Modifier.padding(top = 6.dp, bottom = 14.dp),
                            )
                            var details by remember { mutableIntStateOf(0) }
                            CardButton("重新连接", { if (remote.state.value.conn == io.github.lewin671.remoteagent.core.ConnState.ONLINE) term.connect() }, Modifier.fillMaxWidth(), primary = true, enabled = online)
                            Box(Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable { details++ }, contentAlignment = Alignment.Center) {
                                Text(if (details % 2 == 1) "收起详情" else "查看详情", style = T.s13, color = CardColors.link)
                            }
                            if (details % 2 == 1) Text(s.reason, style = T.mono12, color = CardColors.text2)
                        }
                        else -> {}
                    }
                }
            }
        }
        KeyBar(
            enabled = state == RemoteTerminal.State.Live,
            onKey = { key ->
                if (term.history.state.value.visible) leaveReading()
                term.sendText(keySequence(key, term.session.emulator?.isCursorKeysApplicationMode == true))
            },
            onKeyboard = {
                val v = term.view ?: return@KeyBar
                val reading = term.history.state.value.visible
                if (reading) leaveReading()
                val imm = v.context.getSystemService(InputMethodManager::class.java)
                // Leaving reading opens the keyboard; otherwise the key goes on toggling it.
                if (imeVisible && !reading) imm.hideSoftInputFromWindow(v.windowToken, 0)
                else {
                    v.requestFocus()
                    imm.showSoftInput(v, 0)
                }
            },
        )
    }
}

@Composable
private fun FontButton(label: String, said: String, enabled: Boolean, onClick: () -> Unit) {
    Box(Modifier.size(44.dp).disabledAlpha(enabled).clickable(enabled = enabled, role = Role.Button, onClick = onClick).semantics { contentDescription = said }, contentAlignment = Alignment.Center) {
        Text(label, style = T.s15b, color = CardColors.text)
    }
}

/**
 * The keys a phone keyboard lacks, on a dark glass bar (client-v58): Esc,
 * the arrows and Enter on the first row; Tab, Ctrl+C and the white 键盘 on
 * the second. Every key is at least 44 × 44.
 */
@Composable
private fun KeyBar(enabled: Boolean, onKey: (Key) -> Unit, onKeyboard: () -> Unit) {
    val p = pal
    Column(
        Modifier.fillMaxWidth().padding(start = 12.dp, end = 12.dp, top = 4.dp, bottom = 12.dp)
            .glass(p, RoundedCornerShape(27.dp), dark = true).padding(7.dp),
        verticalArrangement = Arrangement.spacedBy(4.dp),
    ) {
        Row(Modifier.fillMaxWidth().disabledAlpha(enabled), horizontalArrangement = Arrangement.spacedBy(4.dp)) {
            for (k in keyRows[0]) KeyButton(k.label, enabled, Modifier.weight(1f), k.icon) { onKey(k) }
        }
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(4.dp)) {
            for (k in keyRows[1]) KeyButton(k.label, enabled, Modifier.weight(1f).disabledAlpha(enabled), k.icon) { onKey(k) }
            KeyButton("键盘", true, Modifier.weight(1.5f), Lucide.Keyboard, named = true, white = true, onClick = onKeyboard)
        }
    }
}

@Composable
private fun KeyButton(label: String, enabled: Boolean, modifier: Modifier, icon: ImageVector? = null, named: Boolean = false, white: Boolean = false, onClick: () -> Unit) {
    Box(
        modifier.height(44.dp).clip(Radii.Pill).then(if (white) Modifier.background(CardColors.primary) else Modifier)
            .clickable(enabled = enabled, role = Role.Button, onClick = onClick),
        contentAlignment = Alignment.Center,
    ) {
        val color = if (white) CardColors.onPrimary else CardColors.text
        // An icon stands for the key by itself, or with [named] goes before its label.
        if (icon == null) Text(label, style = T.mono13, color = color, maxLines = 1)
        else if (named) IconText(icon, label, color, T.s15b)
        else Ic(icon, 20.dp, color, contentDescription = label)
    }
}
