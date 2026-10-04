package io.github.lewin671.remoteagent.ui

import android.graphics.BitmapFactory
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.proto.ImageRef
import io.github.lewin671.remoteagent.proto.ImageSync

/** The preview embedded in an image ref, decoded once; null without one or when it does not decode. */
@Composable
fun rememberPreview(ref: ImageRef): ImageBitmap? = remember(ref.imageId, ref.preview.size(), ref.path) {
    val b = ref.preview.toByteArray()
    if (b.isEmpty()) null else runCatching { BitmapFactory.decodeByteArray(b, 0, b.size)?.asImageBitmap() }.getOrNull()
}

/** Width over height of an image as the event says, so its place is kept before anything loads. */
fun aspect(ref: ImageRef): Float {
    val (w, h) = when {
        ref.previewWidth > 0 && ref.previewHeight > 0 -> ref.previewWidth to ref.previewHeight
        ref.displayWidth > 0 && ref.displayHeight > 0 -> ref.displayWidth to ref.displayHeight
        ref.width > 0 && ref.height > 0 -> ref.width to ref.height
        else -> 4 to 3
    }
    return (w.toFloat() / h).coerceIn(0.4f, 2.5f)
}

fun fileName(path: String): String = path.substringAfterLast('/').ifEmpty { "图片" }

/**
 * A message's or tool's images: previews at their own proportions, four to
 * a row at most, the rest folded into +N. The corner number is the CLI's
 * [Image #N], so it matches the text and the terminal. A tap opens the
 * viewer at that image.
 */
@Composable
fun ImageRow(refs: List<ImageRef>, onOpen: (Int) -> Unit, modifier: Modifier = Modifier, height: Int = 88) {
    if (refs.isEmpty()) return
    val shown = refs.take(4)
    Row(modifier, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
        shown.forEachIndexed { i, ref ->
            val more = if (i == 3 && refs.size > 4) refs.size - 4 else 0
            Thumb(ref, Modifier.height(height.dp).aspectRatio(aspect(ref), matchHeightConstraintsFirst = true).widthIn(max = 180.dp), more) { onOpen(i) }
        }
    }
}

@Composable
fun Thumb(ref: ImageRef, modifier: Modifier, more: Int = 0, onClick: () -> Unit) {
    val p = pal
    val bmp = rememberPreview(ref)
    Box(modifier.clip(RoundedCornerShape(10.dp)).background(p.chip).clickable(onClick = onClick)) {
        if (bmp != null) Image(bmp, fileName(ref.path), Modifier.fillMaxSize(), contentScale = ContentScale.Crop)
        else Column(Modifier.fillMaxSize().padding(6.dp), verticalArrangement = Arrangement.Center, horizontalAlignment = Alignment.CenterHorizontally) {
            // Not even a preview: the file name is all there is.
            Text(if (ref.sync == ImageSync.IMAGE_SYNC_NAME_ONLY) "图片没有同步" else "没有预览", style = MaterialTheme.typography.labelSmall, color = p.muted)
            if (ref.path.isNotEmpty()) Text(fileName(ref.path), style = MaterialTheme.typography.labelSmall, color = p.faint, maxLines = 2, overflow = TextOverflow.Ellipsis)
        }
        if (ref.cliIndex > 0) Text(
            "${ref.cliIndex}",
            Modifier.align(Alignment.TopStart).padding(4.dp).background(Color(0x99000000), RoundedCornerShape(6.dp)).padding(horizontal = 5.dp),
            color = Color.White, style = MaterialTheme.typography.labelSmall,
        )
        if (ref.sync == ImageSync.IMAGE_SYNC_PREVIEW_ONLY && more == 0) Text(
            "只有预览",
            Modifier.align(Alignment.BottomStart).padding(4.dp).background(Color(0x99000000), RoundedCornerShape(6.dp)).padding(horizontal = 5.dp),
            color = Color.White, style = MaterialTheme.typography.labelSmall,
        )
        if (more > 0) Box(Modifier.fillMaxSize().background(Color(0x88000000)), contentAlignment = Alignment.Center) {
            Text("+$more", color = Color.White, style = MaterialTheme.typography.titleMedium)
        }
    }
}

/** A generated image: its own card across the chat, with the prompt the CLI rewrote behind 提示词. */
@Composable
fun GeneratedCard(item: ChatItem.Generated, onOpen: (Int) -> Unit) {
    val p = pal
    var open by rememberSaveable(item.key) { mutableStateOf(false) }
    Card(Modifier.padding(horizontal = 12.dp, vertical = 6.dp)) {
        Column(Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            item.images.forEachIndexed { i, ref ->
                val bmp = rememberPreview(ref)
                Box(Modifier.fillMaxWidth().aspectRatio(aspect(ref)).clip(RoundedCornerShape(10.dp)).background(p.chip).clickable { onOpen(i) }) {
                    if (bmp != null) Image(bmp, "生成的图片", Modifier.fillMaxSize(), contentScale = ContentScale.Fit)
                    else Text("没有预览", Modifier.align(Alignment.Center), color = p.muted)
                }
            }
            Row(verticalAlignment = Alignment.CenterVertically) {
                val r = item.images.firstOrNull()
                val size = r?.let { if (it.width > 0) " · ${it.width} × ${it.height}" else "" }.orEmpty()
                Text("生成的图片$size", Modifier.weight(1f), style = MaterialTheme.typography.bodySmall, color = p.muted)
                if (item.prompt.isNotEmpty()) Box(Modifier.clickable { open = !open }.padding(6.dp)) { ArrowText("提示词", p.blue, MaterialTheme.typography.bodySmall, down = !open, up = open) }
            }
            if (open) Text(item.prompt, style = MaterialTheme.typography.bodyMedium, color = p.fg2)
        }
    }
}
