package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.WindowInsetsSides
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.only
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawing
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.SideEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.compose.ui.window.DialogWindowProvider
import androidx.compose.ui.window.Popup
import androidx.compose.ui.window.PopupProperties
import androidx.core.view.WindowCompat
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.sanitizeSharedFileName

/** Full-screen file viewers share the image viewer's platform transition and the chat palette. */
@Composable
internal fun SharedFileViewerDialog(onDismiss: () -> Unit, content: @Composable () -> Unit) {
    Dialog(
        onDismissRequest = onDismiss,
        properties = DialogProperties(usePlatformDefaultWidth = false, decorFitsSystemWindows = false),
    ) {
        val p = pal
        val view = LocalView.current
        SideEffect {
            (view.parent as? DialogWindowProvider)?.window?.let { window ->
                window.statusBarColor = android.graphics.Color.TRANSPARENT
                window.navigationBarColor = android.graphics.Color.TRANSPARENT
                WindowCompat.getInsetsController(window, view).apply {
                    isAppearanceLightStatusBars = !p.dark
                    isAppearanceLightNavigationBars = !p.dark
                }
            }
        }
        content()
    }
}

@Composable
internal fun SharedFileViewerPage(
    fileName: String,
    onClose: () -> Unit,
    onAction: (SharedMenuAction) -> Unit,
    tag: String,
    content: @Composable ColumnScope.() -> Unit,
) {
    val p = pal
    val copy = uiText()
    var menu by remember { mutableStateOf(false) }
    Column(
        Modifier.fillMaxSize().testTag(tag).background(p.paper)
            .windowInsetsPadding(WindowInsets.safeDrawing.only(WindowInsetsSides.Top + WindowInsetsSides.Horizontal)),
    ) {
        Row(
            Modifier.fillMaxWidth().height(56.dp).padding(horizontal = 16.dp).testTag("$tag-toolbar"),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(10.dp),
        ) {
            GlassButton(Lucide.ChevronLeft, copy(UiMessages.commonBack()), onClose)
            val name = sanitizeSharedFileName(fileName)
            val measurer = rememberTextMeasurer()
            BoxWithConstraints(Modifier.weight(1f), contentAlignment = Alignment.Center) {
                val width = constraints.maxWidth
                val title = remember(name, width, measurer) { fitMiddleEllipsizedName(name, T.s17b, width, 1, measurer) }
                Text(title, style = T.s17b, color = p.ink, maxLines = 1, softWrap = false,
                    modifier = Modifier.semantics { contentDescription = name })
            }
            Row(horizontalArrangement = Arrangement.spacedBy(4.dp)) {
                GlassButton(SharedFileIcons.Share, copy(UiMessages.sharedFileMenuShare()), { onAction(SharedMenuAction.SHARE) })
                Box {
                    GlassButton(Lucide.Ellipsis, copy(UiMessages.commonAndroidMore()), { menu = true })
                    if (menu) Popup(
                        alignment = Alignment.TopEnd,
                        offset = IntOffset(0, with(androidx.compose.ui.platform.LocalDensity.current) { 50.dp.roundToPx() }),
                        onDismissRequest = { menu = false },
                        properties = PopupProperties(focusable = true),
                    ) {
                        SharedFileContextMenuCard(
                            path = "", machineName = "",
                            entries = listOf(
                                MenuEntry(SharedMenuAction.OPEN_WITH, { uiText(UiMessages.sharedFileMenuOpenWith()) }, SharedFileIcons.OpenWith),
                                MenuEntry(SharedMenuAction.SAVE_TO_DOWNLOADS, { uiText(UiMessages.sharedFileMenuSaveToDownloads()) }, SharedFileIcons.Save),
                            ),
                            onAction = { menu = false; onAction(it) },
                            copy = copy,
                            modifier = Modifier.testTag("$tag-menu"),
                        )
                    }
                }
            }
        }
        content()
    }
}
