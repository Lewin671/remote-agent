package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.platform.LocalWindowInfo
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.TextFieldValue
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import io.github.lewin671.remoteagent.core.*
import kotlinx.coroutines.launch
import kotlinx.coroutines.Dispatchers

/** Same selected-input interaction as session rename; only a complete title-only spec is submitted. */
@Composable
fun ScheduleRenameDialog(task: ScheduledTask, onClose: () -> Unit, enabled: Boolean = true, onSave: suspend (String) -> Unit) {
    val scope = rememberCoroutineScope()
    var value by remember { mutableStateOf(TextFieldValue(task.title, TextRange(0, task.title.length))) }
    var saving by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf("") }
    var unknown by remember { mutableStateOf(false) }
    val focus = remember { FocusRequester() }
    fun save() {
        if (saving || unknown || !enabled) return
        if (renamedScheduleSpec(task, value.text) == null) return onClose()
        saving = true; error = ""
        scope.launch(Dispatchers.Main.immediate) {
            try { onSave(value.text); onClose() }
            catch (e: kotlinx.coroutines.CancellationException) { throw e }
            catch (e: Exception) { error = errorText(e); unknown = (e as? CommandException)?.unknown == true }
            finally { saving = false }
        }
    }
    Dialog({ if (!saving) onClose() }, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        val window = LocalWindowInfo.current
        LaunchedEffect(window) { snapshotFlow { window.isWindowFocused }.collect { if (it) focus.requestFocus() } }
        Box(Modifier.fillMaxWidth().padding(horizontal = 30.dp).imePadding(), contentAlignment = Alignment.Center) {
            Column(Modifier.widthIn(max = 320.dp).fillMaxWidth().clip(Radii.LG).background(pal.card).padding(start = 16.dp, end = 16.dp, top = 18.dp, bottom = 12.dp)) {
                Text("重命名任务", style = T.h17, color = pal.fg, textAlign = TextAlign.Center, modifier = Modifier.fillMaxWidth().padding(bottom = 12.dp))
                BasicTextField(value, { value = it.copy(text = scheduleNameInput(it.text)) }, Modifier.fillMaxWidth().padding(bottom = 8.dp).focusRequester(focus),
                    textStyle = T.body.copy(color = pal.fg), cursorBrush = SolidColor(pal.blue), singleLine = true, readOnly = !enabled,
                    keyboardOptions = KeyboardOptions(imeAction = ImeAction.Done), keyboardActions = KeyboardActions(onDone = { save() }),
                    decorationBox = { inner -> FieldBox { inner() } })
                Text(if (error.isEmpty()) "留空保存会用消息的第一句" else error, style = T.sm, color = if (error.isEmpty()) pal.muted else pal.red, textAlign = TextAlign.Center, modifier = Modifier.fillMaxWidth().padding(bottom = 14.dp))
                Btns {
                    Btn("取消", onClose, Modifier.weight(1f), enabled = !saving)
                    Btn(if (saving) "保存中…" else "保存", { save() }, Modifier.weight(1f), BtnKind.PRIMARY, enabled = enabled && !saving && !unknown)
                }
            }
        }
    }
}
