package io.github.lewin671.remoteagent.ui

import androidx.compose.ui.focus.FocusManager
import androidx.compose.ui.platform.SoftwareKeyboardController

/** Page navigation ends editing; composer popovers never call this. */
internal fun dismissPageInput(focus: FocusManager, keyboard: SoftwareKeyboardController?) {
    // Dropping Compose focus alone can leave an already visible Android IME on the next page.
    keyboard?.hide()
    focus.clearFocus(force = true)
}
