package io.github.lewin671.remoteagent.ui

import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.PickVisualMediaRequest
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
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
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
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
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.core.Remote

/**
 * 添加图片 in the ＋ menu: the system's photo picker, asked for as many as
 * still fit. The picker's fallback may not honour the count, so the draft
 * takes only what fits and says how many were left out. When the machine
 * cannot take images it says why instead.
 */
@Composable
fun rememberImagePicker(draft: ImageDraft, remote: Remote, machineId: String?, blocked: String?, max: Int): () -> Unit {
    val context = LocalContext.current
    val room = (max - draft.images.size).coerceAtLeast(0)
    val many = rememberLauncherForActivityResult(ActivityResultContracts.PickMultipleVisualMedia(maxOf(2, room))) { uris ->
        if (uris.isNotEmpty()) draft.add(context, uris, max, remote) { machineId }
    }
    val one = rememberLauncherForActivityResult(ActivityResultContracts.PickVisualMedia()) { uri ->
        if (uri != null) draft.add(context, listOf(uri), max, remote) { machineId }
    }
    return {
        when {
            blocked != null -> ImageNotices.show(draft.key, blocked)
            room == 0 -> ImageNotices.show(draft.key, "一条消息最多 $max 张")
            else -> {
                val req = PickVisualMediaRequest(ActivityResultContracts.PickVisualMedia.ImageOnly)
                if (room == 1) one.launch(req) else many.launch(req)
            }
        }
    }
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
        if (notice != null) Text(notice, Modifier.padding(horizontal = 12.dp, vertical = 4.dp), style = T.meta, color = p.orangeInk)
        return
    }
    Column(Modifier.fillMaxWidth().padding(start = 2.dp, end = 2.dp, top = 2.dp)) {
        Row(Modifier.horizontalScroll(rememberScrollState()).padding(bottom = 8.dp), horizontalArrangement = Arrangement.spacedBy(6.dp)) {
            for (img in draft.images) {
                Box(Modifier.size(56.dp).clip(Radii.SM).background(p.soft).border(1.dp, Color(0x1A000000), Radii.SM)) {
                    img.thumb?.let { Image(it, null, Modifier.fillMaxSize(), contentScale = ContentScale.Crop) }
                    when (val st = img.state) {
                        PendingImage.State.Processing -> CircularProgressIndicator(Modifier.align(Alignment.Center).size(24.dp), color = Color.White, strokeWidth = 3.dp, trackColor = Color(0x59FFFFFF))
                        is PendingImage.State.Uploading -> {
                            CircularProgressIndicator(Modifier.align(Alignment.Center).size(24.dp), color = Color.White, strokeWidth = 3.dp, trackColor = Color(0x59FFFFFF))
                            if (draft.original) Box(Modifier.align(Alignment.BottomCenter).fillMaxWidth().height(4.dp).background(Color(0x59000000))) {
                                Box(Modifier.fillMaxWidth(st.progress).height(4.dp).background(p.blueBg))
                            }
                        }
                        is PendingImage.State.Failed -> Box(
                            Modifier.fillMaxSize().background(Color(0x99000000)).clickable { draft.retry(context, img, remote) { machineId } },
                            contentAlignment = Alignment.BottomCenter,
                        ) { Text("重试", color = Color.White, style = T.tab, modifier = Modifier.padding(bottom = 4.dp)) }
                        PendingImage.State.Ready -> {}
                    }
                    // 18 dp to see, 44 dp to tap.
                    Box(Modifier.align(Alignment.TopEnd).size(30.dp).clickable { draft.remove(img) }, contentAlignment = Alignment.TopEnd) {
                        Box(Modifier.padding(3.dp).size(18.dp).clip(CircleShape).background(Color(0x99000000)), contentAlignment = Alignment.Center) {
                            Text("✕", color = Color.White, style = ts(11.sp, lineHeight = 1f))
                        }
                    }
                }
            }
        }
        Row(Modifier.fillMaxWidth().padding(horizontal = 2.dp).padding(bottom = 6.dp), verticalAlignment = Alignment.CenterVertically) {
            Row(Modifier.heightIn(min = 44.dp).clickable { draft.setOriginal(context, !draft.original, remote) { machineId } }, verticalAlignment = Alignment.CenterVertically) {
                Box(Modifier.size(14.dp).clip(CircleShape).border(if (draft.original) 4.dp else 1.5.dp, if (draft.original) p.blueBg else p.muted, CircleShape))
                Text(" 原图" + if (draft.original) " · 共 ${"%.1f".format(draft.totalBytes / 1024.0 / 1024.0)} MB" else "", style = T.meta, color = p.fg2)
            }
            Spacer(Modifier.weight(1f))
            Text("${draft.images.size} / $max", style = T.meta, color = p.muted)
        }
        val failed = draft.images.mapNotNull { (it.state as? PendingImage.State.Failed)?.error }.firstOrNull()
        val notes = draft.images.mapNotNull { it.processed?.note?.takeIf { n -> n.isNotEmpty() } }.distinct()
        val line = notice ?: draft.notice.takeIf { it.isNotEmpty() } ?: failed?.let { "有图没能传到电脑：$it，点按重试或移除" }
        if (line != null) Text(line, style = T.meta, color = p.orangeInk, modifier = Modifier.padding(horizontal = 10.dp, vertical = 2.dp))
        if (draft.original && notes.isNotEmpty()) Text(notes.joinToString("；"), style = T.meta, color = p.muted, modifier = Modifier.padding(horizontal = 10.dp, vertical = 2.dp))
    }
}
