package io.github.lewin671.remoteagent.term

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.ui.TermColors

@Composable
fun TerminalHistoryPanel(history: TerminalHistory.State, online: Boolean, font: Int, load: () -> Unit) {
    val list = rememberLazyListState()
    val nearTop by remember(history.pages.size) { derivedStateOf { list.layoutInfo.visibleItemsInfo.any { it.index >= history.pages.size - 1 } } }
    LaunchedEffect(nearTop, history.pages.size, history.pending) {
        if (nearTop && online && history.pending.isEmpty() && history.error.isEmpty() && history.cursor.isNotEmpty()) load()
    }
    LazyColumn(Modifier.fillMaxSize().background(TermColors.bg).padding(horizontal = 12.dp), state = list, reverseLayout = true) {
        items(history.pages, key = { it.id }) { page ->
            SelectionContainer {
                Text(page.text + "\n", color = TermColors.fg, fontFamily = FontFamily.Monospace, fontSize = font.sp,
                    softWrap = false, modifier = Modifier.horizontalScroll(rememberScrollState()))
            }
        }
        item(key = "status") {
            val status = when {
                history.pending.isNotEmpty() -> "读取历史…"
                history.error.isNotEmpty() -> history.error + " · 重试"
                history.cursor.isNotEmpty() -> "读取更早历史"
                history.pages.isNotEmpty() -> "已到保留历史的开头"
                else -> "暂无历史"
            }
            Text(status, color = TermColors.link, fontSize = 12.sp,
                modifier = Modifier.fillMaxWidth().clickable(enabled = online && history.pending.isEmpty() && (history.error.isNotEmpty() || history.cursor.isNotEmpty()), onClick = load).padding(vertical = 12.dp))
        }
    }
}
