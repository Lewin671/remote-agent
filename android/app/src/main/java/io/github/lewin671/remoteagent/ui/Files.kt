package io.github.lewin671.remoteagent.ui

import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.*
import androidx.compose.material3.*
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.proto.SentFile

@Composable
internal fun rememberDocumentPicker(draft: ImageDraft, remote: Remote, machineId: String?): (() -> Unit)? {
    val context = LocalContext.current
    val machine = remote.state.value.machines[machineId]
    val picker = rememberLauncherForActivityResult(ActivityResultContracts.OpenMultipleDocuments()) {
        draft.addDocuments(context, it, remote, machineId)
    }
    if (!filesSupported(machine)) return null
    return {
        when {
            draft.restoring || draft.readingFiles -> Unit
            draft.documents.size >= maxFiles(machine) -> ImageNotices.show(draft.key, "一条消息最多 ${maxFiles(machine)} 个文件")
            else -> picker.launch(arrayOf("*/*"))
        }
    }
}

/** The same chip for editing, pending and recorded messages. No file content is loaded to render it. */
@Composable
internal fun FileChip(name: String, size: Long, state: PendingImage.State? = null, onRetry: (() -> Unit)? = null, onRemove: (() -> Unit)? = null) {
    val p = pal
    Row(Modifier.fillMaxWidth().background(p.soft, Radii.SM).border(1.dp, p.line, Radii.SM).padding(start = 8.dp), verticalAlignment = Alignment.CenterVertically) {
        Ic(Lucide.File, 18.dp, p.muted)
        Column(Modifier.weight(1f).padding(horizontal = 8.dp, vertical = 6.dp)) {
            Text(fileChipName(name), maxLines = 1, overflow = androidx.compose.ui.text.style.TextOverflow.MiddleEllipsis, style = T.sm, color = p.fg2, modifier = Modifier.semantics { contentDescription = name })
            val status = when (state) {
                PendingImage.State.Processing -> " · 准备中"
                is PendingImage.State.Uploading -> " · 上传 ${(state.progress * 100).toInt()}%"
                is PendingImage.State.Failed -> " · 上传失败"
                else -> ""
            }
            Text(fileSize(size) + status, style = T.meta, color = if (state is PendingImage.State.Failed) p.red else p.muted)
            if (state is PendingImage.State.Uploading) LinearProgressIndicator(progress = { state.progress }, modifier = Modifier.fillMaxWidth().height(3.dp), color = p.blue)
            if (state is PendingImage.State.Failed) Text(state.error, style = T.meta, color = p.red)
        }
        if (state is PendingImage.State.Failed && onRetry != null) TextButton(onClick = onRetry) { Text("重试") }
        if (onRemove != null) IconButton(onClick = onRemove, modifier = Modifier.size(44.dp).semantics { contentDescription = "移除文件 $name" }) { Ic(Lucide.X, 16.dp, p.muted) }
    }
}

@Composable
internal fun FileChips(files: List<SentFile>) {
    if (files.isNotEmpty()) Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
        files.forEach { FileChip(it.name, it.sizeBytes) }
    }
}

@Composable
internal fun FileStrip(draft: ImageDraft, remote: Remote, machineId: String?) {
    if (draft.readingFiles) Text("正在读取所选文件…", style = T.meta, color = pal.muted)
    if (draft.documents.isEmpty()) return
    Column(Modifier.padding(horizontal = 6.dp, vertical = 4.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
        draft.documents.forEach { file ->
            FileChip(file.fileName!!, file.fileBytes, file.state,
                onRetry = { machineId?.let { draft.upload(remote, file, it) } }, onRemove = { draft.remove(file) })
        }
        fileDraftBlocked(draft, remote.state.value.machines[machineId])?.let { Text(it, style = T.meta, color = pal.red) }
        Text("文件原样发送，包含其中的元数据。", style = T.meta, color = pal.muted)
    }
}
