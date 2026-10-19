package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.safeDrawing
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier

/** The activity owns system insets for both account setup and enrolled navigation. */
@Composable
internal fun AppSafeArea(
    insets: WindowInsets = WindowInsets.safeDrawing,
    content: @Composable () -> Unit,
) {
    Box(Modifier.fillMaxSize().windowInsetsPadding(insets)) { content() }
}
