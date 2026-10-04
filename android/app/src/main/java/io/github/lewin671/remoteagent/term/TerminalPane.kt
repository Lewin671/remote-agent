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
import io.github.lewin671.remoteagent.ui.Btn
import io.github.lewin671.remoteagent.ui.BtnKind
import io.github.lewin671.remoteagent.ui.Ic
import io.github.lewin671.remoteagent.ui.IconText
import io.github.lewin671.remoteagent.ui.Lucide
import io.github.lewin671.remoteagent.ui.Radii
import io.github.lewin671.remoteagent.ui.T
import io.github.lewin671.remoteagent.ui.TermColors
import io.github.lewin671.remoteagent.ui.bottomLine
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
import com.termux.view.TerminalView
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
    val typeface = remember(context) { terminalTypeface(context) }
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
        // The toolbar says the size, and that it changed; nothing pops up over the screen.
        Row(
            Modifier.fillMaxWidth().background(TermColors.bar).bottomLine(TermColors.rule).padding(start = 12.dp, end = 4.dp).heightIn(min = 44.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            val (cols, rows) = size
            Text(
                when {
                    state != RemoteTerminal.State.Live -> "终端"
                    resized -> "远端尺寸已变化 · ${cols}×$rows"
                    fitted -> "已适配这块屏幕 · ${cols}×$rows"
                    else -> "电脑上 ${cols}×$rows"
                },
                style = T.meta, color = TermColors.tool, modifier = Modifier.weight(1f),
            )
            if (state == RemoteTerminal.State.Live) Box(Modifier.heightIn(min = 44.dp).clickable { term.toggleFit() }.padding(horizontal = 8.dp), contentAlignment = Alignment.Center) {
                if (fitted || resized) Text(if (fitted) "还原" else "适配", style = T.metaB, color = TermColors.link)
                else IconText(Lucide.Maximize2, "适配我的屏幕", TermColors.link, T.metaB)
            }
        }
        Row(
            Modifier.fillMaxWidth().background(TermColors.bar).heightIn(min = 40.dp).padding(horizontal = 12.dp),
            verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(20.dp),
        ) {
            Text("字号", style = ts(14.sp), color = TermColors.dim, modifier = Modifier.weight(1f))
            FontButton("A−", fontDp > 10) { changeFont(-1) }
            Text("$fontDp", style = ts(14.sp), color = TermColors.dim)
            FontButton("A+", fontDp < 22) { changeFont(1) }
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
                Column(
                    Modifier.align(Alignment.TopCenter).padding(12.dp).fillMaxWidth().clip(Radii.SM).background(TermColors.bar)
                        .border(1.dp, TermColors.line, Radii.SM).padding(12.dp),
                ) {
                    when (val s = state) {
                        RemoteTerminal.State.Connecting -> IconText(Lucide.LoaderCircle, "连接终端…", TermColors.dim, T.sm)
                        is RemoteTerminal.State.Closed -> {
                            Text("终端连接断开了", style = T.bodyB, color = TermColors.fg)
                            Text(
                                (if (!online) "等待服务器连接恢复后，会重新获取终端画面。" else if (sessionRunning) "下面是断开前的画面。会话还在运行，聊天照常更新。" else "会话不在运行。"),
                                style = T.sm, color = TermColors.dim, modifier = Modifier.padding(top = 6.dp, bottom = 14.dp),
                            )
                            var details by remember { mutableIntStateOf(0) }
                            Btn("重新连接", { if (remote.state.value.conn == io.github.lewin671.remoteagent.core.ConnState.ONLINE) term.connect() }, Modifier.fillMaxWidth(), BtnKind.PRIMARY, enabled = online)
                            Box(Modifier.heightIn(min = 44.dp).clickable { details++ }, contentAlignment = Alignment.CenterStart) {
                                Text(if (details % 2 == 1) "收起详情" else "查看详情", style = T.meta, color = TermColors.link)
                            }
                            if (details % 2 == 1) Text(s.reason, style = T.mono, color = TermColors.dim)
                        }
                        else -> {}
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
private fun FontButton(label: String, enabled: Boolean, onClick: () -> Unit) {
    Box(Modifier.size(44.dp).clickable(enabled = enabled, onClick = onClick), contentAlignment = Alignment.Center) {
        Text(label, style = ts(14.sp, androidx.compose.ui.text.font.FontWeight.SemiBold), color = if (enabled) TermColors.link else TermColors.tool)
    }
}

/** The keys a phone keyboard lacks, two rows of 44 dp keys (client-v7). */
@Composable
private fun KeyBar(enabled: Boolean, onKey: (Key) -> Unit, onKeyboard: () -> Unit) {
    Column(Modifier.fillMaxWidth().background(TermColors.bar).padding(start = 8.dp, end = 8.dp, top = 6.dp, bottom = 16.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(4.dp)) {
            for (k in keyRows[0]) KeyButton(k.label, enabled, Modifier.weight(1f), k.icon) { onKey(k) }
        }
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(4.dp)) {
            for (k in keyRows[1]) KeyButton(k.label, enabled, Modifier.weight(1f), k.icon) { onKey(k) }
            KeyButton("键盘", true, Modifier.weight(1f), Lucide.Keyboard, named = true, onClick = onKeyboard)
        }
    }
}

@Composable
private fun KeyButton(label: String, enabled: Boolean, modifier: Modifier, icon: ImageVector? = null, named: Boolean = false, onClick: () -> Unit) {
    Box(
        modifier.height(44.dp).clip(RoundedCornerShape(7.dp)).background(TermColors.key).clickable(enabled = enabled, onClick = onClick),
        contentAlignment = Alignment.Center,
    ) {
        val color = if (enabled) TermColors.fg else TermColors.tool
        // An icon stands for the key by itself, or with [named] goes before its label.
        if (icon == null) Text(label, style = T.sm, color = color, maxLines = 1)
        else if (named) IconText(icon, label, color, T.sm)
        else Ic(icon, 20.dp, color, contentDescription = label)
    }
}
