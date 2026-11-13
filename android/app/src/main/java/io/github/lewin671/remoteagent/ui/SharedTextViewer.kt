package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.asPaddingValues
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.navigationBars
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.data.isSharedMarkdownFile

@Composable
internal fun SharedFileTextViewerDialog(
    fileName: String,
    text: String,
    onDismiss: () -> Unit,
    onAction: (SharedMenuAction) -> Unit,
) {
    SharedFileViewerDialog(onDismiss) {
        SharedFileTextViewerPage(fileName, text, onDismiss, onAction)
    }
}

@Composable
internal fun SharedFileTextViewerPage(
    fileName: String,
    text: String,
    onClose: () -> Unit,
    onAction: (SharedMenuAction) -> Unit,
) {
    val p = pal
    SharedFileViewerPage(fileName, onClose, onAction, tag = "text-viewer") {
        val bottom = WindowInsets.navigationBars.asPaddingValues().calculateBottomPadding()
        Column(
            Modifier.weight(1f).fillMaxWidth().testTag("text-viewer-body").verticalScroll(rememberScrollState())
                .padding(start = 16.dp, end = 16.dp, top = 12.dp, bottom = 24.dp + bottom),
        ) {
            if (isSharedMarkdownFile(fileName)) {
                Markdown(text, Modifier.fillMaxWidth(), bleed = 16.dp)
            } else {
                SelectionContainer {
                    Text(text, color = p.ink, style = TextStyle(fontFamily = MonoFamily, fontSize = 13.sp, lineHeight = 19.sp),
                        modifier = Modifier.fillMaxWidth())
                }
            }
        }
    }
}
