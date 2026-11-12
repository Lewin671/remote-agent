package io.github.lewin671.remoteagent.ui

import android.os.SystemClock

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.input.key.Key
import androidx.compose.ui.input.key.KeyEventType
import androidx.compose.ui.input.key.key
import androidx.compose.ui.input.key.onPreInterceptKeyBeforeSoftKeyboard
import androidx.compose.ui.input.key.type
import androidx.compose.ui.input.pointer.PointerEventPass
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.layout.positionInWindow
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.paneTitle
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.IntRect
import androidx.compose.ui.unit.IntSize
import androidx.compose.ui.unit.LayoutDirection
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Popup
import androidx.compose.ui.window.PopupPositionProvider
import androidx.compose.ui.window.PopupProperties
import kotlin.math.roundToInt

/** Geometry is in window coordinates, shared by placement and outside-tap handling. */
internal class ComposerPicker(private val onClose: () -> Unit = {}) {
    var open by mutableStateOf<String?>(null)
    var top by mutableStateOf(0f)
    var composer by mutableStateOf(Rect.Zero)
    var agent by mutableStateOf(Rect.Zero)
    var model by mutableStateOf(Rect.Zero)
    var origin = Offset.Zero
    private var outsideChip: String? = null
    private var outsideTime = Long.MIN_VALUE
    fun dismissOutside() {
        outsideChip = open
        outsideTime = SystemClock.uptimeMillis()
        close()
    }
    // ACTION_OUTSIDE precedes the same DOWN in the underlying window. Match that event's
    // timestamp, not a debounce interval: a later, separate tap must still open the picker.
    fun openedAtDown(uptime: Long): String? {
        val result = open ?: outsideChip.takeIf { uptime <= outsideTime }
        outsideChip = null
        return result
    }
    fun close() {
        if (open == null) return
        // Clear before saving: native dismissal and the page's DOWN can both close one popup.
        open = null
        onClose()
    }
    /** A different project/machine/CLI invalidates the pending choice; it is not a user dismissal. */
    fun cancel() { open = null; outsideChip = null }
    fun toggle(which: String) {
        val previous = open
        close()
        open = which.takeUnless { previous == it }
    }
}

@Composable
internal fun ComposerPickerBack(picker: ComposerPicker) {
    BackHandler(picker.open != null) { picker.close() }
}

/**
 * A non-focusable popup leaves touches outside its window with the page. Consume
 * a touch on the opening chip so the same gesture cannot dismiss on DOWN and reopen on UP.
 * Native outside dismissal also covers taps in another window, including the keyboard.
 * Pre-IME Back is needed on older Android: an ordinary BackHandler runs after the IME hides.
 */
internal fun Modifier.composerPickerEvents(picker: ComposerPicker): Modifier =
    onGloballyPositioned { picker.origin = it.positionInWindow() }
        .onPreInterceptKeyBeforeSoftKeyboard {
            if (picker.open != null && it.key == Key.Back) {
                if (it.type == KeyEventType.KeyUp) picker.close()
                true
            } else false
        }
        .pointerInput(picker) {
            awaitEachGesture {
                val down = awaitFirstDown(requireUnconsumed = false, pass = PointerEventPass.Initial)
                val opened = picker.openedAtDown(down.uptimeMillis)
                if (opened != null) {
                    val chip = if (opened == "agent") picker.agent else picker.model
                    if (chip.contains(down.position + picker.origin)) down.consume()
                    picker.close()
                }
            }
        }

/** No window focus, scrim, IME calls or content-size animation. Only the panel's body scrolls. */
@Composable
internal fun ComposerPopover(picker: ComposerPicker, title: String, panel: Boolean, content: @Composable ColumnScope.() -> Unit) {
    val p = pal
    val density = LocalDensity.current
    val gap = with(density) { 8.dp.toPx() }
    val anchor = if (panel) picker.composer else picker.agent
    val available = with(density) { (anchor.top - picker.top - gap * 2).coerceAtLeast(0f).toDp() }
    if (available <= 0.dp) return
    val width = with(density) { (picker.composer.width - gap * 3).coerceAtLeast(0f).toDp() }.coerceAtMost(if (panel) 420.dp else 240.dp)
    val shape = RoundedCornerShape(14.dp)
    val position = object : PopupPositionProvider {
        override fun calculatePosition(anchorBounds: IntRect, windowSize: IntSize, layoutDirection: LayoutDirection, popupContentSize: IntSize): IntOffset {
            val left = if (panel) picker.composer.left + gap * 1.5f else anchor.left
            return IntOffset(left.roundToInt().coerceIn(gap.roundToInt(), (windowSize.width - popupContentSize.width - gap).roundToInt().coerceAtLeast(gap.roundToInt())),
                (anchor.top - gap - popupContentSize.height).roundToInt())
        }
    }
    Popup(position, onDismissRequest = picker::dismissOutside,
        properties = PopupProperties(focusable = false, dismissOnClickOutside = true)) {
        CompositionLocalProvider(LocalHintInset provides 12.dp, LocalOnSoft provides false) {
            Column(Modifier.width(width)
                .heightIn(max = available.coerceAtMost(if (panel) 600.dp else available))
                .shadow(16.dp, shape, ambientColor = p.float, spotColor = p.float)
                .clip(shape).background(if (p.dark) Color(0xFF2B2B30) else Color.White)
                .border(.5.dp, if (p.dark) Color.White.copy(alpha = .10f) else Color.Black.copy(alpha = .08f), shape)
                .semantics { paneTitle = title }.testTag(if (panel) "new-model-popover" else "new-agent-menu")
                .then(if (panel) Modifier else Modifier.verticalScroll(rememberScrollState()))
                .padding(if (panel) 12.dp else 4.dp), content = content)
        }
    }
}
