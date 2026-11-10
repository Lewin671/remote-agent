package io.github.lewin671.remoteagent.ui

import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.ui.draw.clip
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

/**
 * A file, the same one-line capsule in the box, in a sent message and in a
 * queued one: the file icon, its name (the middle left out, the extension
 * kept), its size and its upload, and × to remove it. No file content is
 * loaded to draw it.
 */
@Composable
internal fun FileChip(name: String, size: Long, state: PendingImage.State? = null, onRetry: (() -> Unit)? = null, onRemove: (() -> Unit)? = null) {
    val p = pal
    Row(
        Modifier.fillMaxWidth().heightIn(min = 52.dp).clip(Radii.Pill).background(p.fill)
            .padding(start = 14.dp, end = if (onRemove != null || onRetry != null) 6.dp else 14.dp, top = 6.dp, bottom = 6.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        Ic(Lucide.File, 20.dp, p.ink2)
        Column(Modifier.weight(1f)) {
            Text(fileChipName(name), maxLines = 1, overflow = androidx.compose.ui.text.style.TextOverflow.MiddleEllipsis, style = T.s15.copy(fontWeight = platformWeight(androidx.compose.ui.text.font.FontWeight.Medium)), color = p.ink, modifier = Modifier.semantics { contentDescription = name })
            val status = when (state) {
                PendingImage.State.Processing -> " · 准备中"
                is PendingImage.State.Uploading -> " · 上传 ${(state.progress * 100).toInt()}%"
                is PendingImage.State.Failed -> " · 上传失败"
                else -> ""
            }
            Text(fileSize(size) + status, style = T.s13, color = if (state is PendingImage.State.Failed) p.red else p.ink2)
            if (state is PendingImage.State.Uploading) Box(Modifier.padding(top = 5.dp).fillMaxWidth().height(3.dp).clip(Radii.Pill).background(p.fill2)) {
                Box(Modifier.fillMaxWidth(state.progress).height(3.dp).clip(Radii.Pill).background(p.ink))
            }
            if (state is PendingImage.State.Failed) Text(state.error, style = T.s13, color = p.red)
        }
        if (state is PendingImage.State.Failed && onRetry != null) Box(Modifier.heightIn(min = 44.dp).clickable(role = androidx.compose.ui.semantics.Role.Button, onClick = onRetry).padding(horizontal = 4.dp), contentAlignment = Alignment.Center) {
            Text("重试", style = T.s15b, color = p.ink)
        }
        if (onRemove != null) Box(Modifier.size(44.dp).clip(CircleShape).clickable(role = androidx.compose.ui.semantics.Role.Button, onClick = onRemove).semantics { contentDescription = "移除文件 $name" }, contentAlignment = Alignment.Center) {
            Ic(Lucide.X, 18.dp, p.ink2)
        }
    }
}

@Composable
internal fun FileChips(files: List<SentFile>) {
    if (files.isNotEmpty()) Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
        files.forEach { FileChip(it.name, it.sizeBytes) }
    }
}

/** The files in the box, apart from videos from the library, which sit among the images ([ImageStrip]). */
@Composable
internal fun FileStrip(draft: ImageDraft, remote: Remote, machineId: String?) {
    if (draft.readingFiles) Text("正在读取所选文件…", style = T.s13, color = pal.ink2, modifier = Modifier.padding(horizontal = 8.dp, vertical = 4.dp))
    val files = draft.documents.filterNot { isVideoDraft(it) }
    val blocked = fileDraftBlocked(draft, remote.state.value.machines[machineId])
    if (files.isEmpty() && blocked == null) return
    Column(Modifier.padding(bottom = 6.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
        files.forEach { file ->
            FileChip(file.fileName!!, file.fileBytes, file.state,
                onRetry = { machineId?.let { draft.upload(remote, file, it, forceFresh = true) } }, onRemove = { draft.remove(file) })
        }
        blocked?.let { Text(it, style = T.s13, color = pal.red, modifier = Modifier.padding(horizontal = 8.dp)) }
        if (files.isNotEmpty()) Text(FILES_AS_THEY_ARE, style = T.s13, color = pal.ink2, modifier = Modifier.padding(horizontal = 8.dp))
    }
}
