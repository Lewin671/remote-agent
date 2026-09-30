package io.github.lewin671.remoteagent.ui

import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.PickVisualMediaRequest
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Checkbox
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.Remote

/**
 * 「＋」: the system's photo picker, asked for as many as still fit. The
 * picker's fallback may not honour the count, so the draft takes only what
 * fits and says how many were left out. When the machine cannot take images
 * the button says why instead.
 */
@Composable
fun ImageAddButton(draft: ImageDraft, remote: Remote, machineId: String?, blocked: String?, max: Int) {
    val p = pal
    val context = LocalContext.current
    val room = (max - draft.images.size).coerceAtLeast(0)
    val many = rememberLauncherForActivityResult(ActivityResultContracts.PickMultipleVisualMedia(maxOf(2, room))) { uris ->
        if (uris.isNotEmpty()) draft.add(context, uris, max, remote) { machineId }
    }
    val one = rememberLauncherForActivityResult(ActivityResultContracts.PickVisualMedia()) { uri ->
        if (uri != null) draft.add(context, listOf(uri), max, remote) { machineId }
    }
    val full = room == 0
    IconButton(
        onClick = {
            when {
                blocked != null -> ImageNotices.show(draft.key, blocked)
                full -> ImageNotices.show(draft.key, "一条消息最多 $max 张")
                else -> {
                    val req = PickVisualMediaRequest(ActivityResultContracts.PickVisualMedia.ImageOnly)
                    if (room == 1) one.launch(req) else many.launch(req)
                }
            }
        },
        modifier = Modifier.size(40.dp),
    ) { Text("＋", color = if (blocked != null || full) p.faint else p.fg2, style = MaterialTheme.typography.titleMedium) }
}

/** A one-line reason shown by the strip, e.g. why 「＋」 does nothing. */
object ImageNotices {
    val text = androidx.compose.runtime.mutableStateMapOf<String, String>()
    fun show(key: String, s: String) {
        text[key] = s
    }
}

/**
 * The picked images above the text: each removable, a spinner or progress
 * while it goes to the computer, a retry when it failed; 原图 for the whole
 * message and the count. Sending waits until all are on the computer.
 */
@Composable
fun ImageStrip(draft: ImageDraft, remote: Remote, machineId: String?, max: Int) {
    val p = pal
    val context = LocalContext.current
    val notice = ImageNotices.text[draft.key]
    LaunchedEffect(notice) {
        if (notice != null) {
            kotlinx.coroutines.delay(4000)
            ImageNotices.text.remove(draft.key)
        }
    }
    if (draft.images.isEmpty()) {
        if (notice != null) Text(notice, Modifier.padding(horizontal = 12.dp, vertical = 4.dp), style = MaterialTheme.typography.bodySmall, color = p.orangeInk)
        return
    }
    Column(Modifier.fillMaxWidth().padding(horizontal = 6.dp, vertical = 4.dp)) {
        Row(Modifier.horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(6.dp)) {
            for (img in draft.images) {
                Box(Modifier.size(64.dp).clip(RoundedCornerShape(10.dp)).background(p.chip)) {
                    img.thumb?.let { Image(it, null, Modifier.fillMaxSize(), contentScale = ContentScale.Crop) }
                    when (val st = img.state) {
                        PendingImage.State.Processing -> CircularProgressIndicator(Modifier.align(Alignment.Center).size(20.dp), strokeWidth = 2.dp)
                        is PendingImage.State.Uploading -> {
                            CircularProgressIndicator(Modifier.align(Alignment.Center).size(20.dp), strokeWidth = 2.dp)
                            if (draft.original) LinearProgressIndicator(progress = { st.progress }, Modifier.align(Alignment.BottomCenter).fillMaxWidth().height(3.dp))
                        }
                        is PendingImage.State.Failed -> Box(
                            Modifier.fillMaxSize().background(Color(0x99C62F2F)).clickable { draft.retry(context, img, remote) { machineId } },
                            contentAlignment = Alignment.Center,
                        ) { Text("重试", color = Color.White, style = MaterialTheme.typography.labelSmall) }
                        PendingImage.State.Ready -> {}
                    }
                    Text(
                        "✕",
                        Modifier.align(Alignment.TopEnd).clickable { draft.remove(img) }.background(Color(0x99000000), RoundedCornerShape(bottomStart = 8.dp)).padding(horizontal = 6.dp, vertical = 2.dp),
                        color = Color.White, style = MaterialTheme.typography.labelSmall,
                    )
                }
            }
        }
        Row(verticalAlignment = Alignment.CenterVertically) {
            Checkbox(draft.original, { draft.setOriginal(context, it, remote) { machineId } })
            Text("原图" + if (draft.original) " · 共 ${"%.1f".format(draft.totalBytes / 1024.0 / 1024.0)} MB" else "", style = MaterialTheme.typography.bodySmall, color = p.fg2)
            Spacer(Modifier.width(8.dp))
            Text("${draft.images.size} / $max", style = MaterialTheme.typography.bodySmall, color = p.muted)
        }
        val failed = draft.images.mapNotNull { (it.state as? PendingImage.State.Failed)?.error }.firstOrNull()
        val notes = draft.images.mapNotNull { it.processed?.note?.takeIf { n -> n.isNotEmpty() } }.distinct()
        val line = notice ?: draft.notice.takeIf { it.isNotEmpty() } ?: failed?.let { "有图没能传到电脑：$it，点按重试或移除" }
        if (line != null) Text(line, style = MaterialTheme.typography.bodySmall, color = p.orangeInk)
        if (draft.original) Text(
            (notes.joinToString("；").let { if (it.isNotEmpty()) "$it。" else "" }) + "两个 CLI 仍会把过大的图缩到 2000px 左右，原图保住的是画质。",
            style = MaterialTheme.typography.bodySmall, color = p.muted,
        )
    }
}
