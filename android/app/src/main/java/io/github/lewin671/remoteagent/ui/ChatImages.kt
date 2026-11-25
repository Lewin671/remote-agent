package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages
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
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.unit.Constraints
import androidx.compose.ui.unit.constrainWidth
import androidx.compose.ui.unit.constrainHeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.proto.SessionImageSyncState
import io.github.lewin671.remoteagent.core.proto.SessionImageView

/** The preview embedded in an image view, decoded once; null without one or when it does not decode. */
@Composable
fun rememberPreview(image: SessionImageView): ImageBitmap? =
    rememberPreviewBytes(image.imageId.ifEmpty { image.displayName }, image.preview)

@Composable
fun rememberPreviewBytes(key: String, preview: ByteString): ImageBitmap? = remember(key, preview.size()) {
    val b = preview.toByteArray()
    if (b.isEmpty()) null else runCatching { BitmapFactory.decodeByteArray(b, 0, b.size)?.asImageBitmap() }.getOrNull()
}

private fun clampedAspect(image: SessionImageView): Float =
    (if (image.clampedAspectRatio > 0.0) image.clampedAspectRatio
    else if (image.aspectRatio > 0.0) image.aspectRatio
    else 4.0 / 3.0).toFloat().coerceIn(0.4f, 2.5f)

internal data class ImageRowGeometry(val widths: List<Int>, val height: Int, val gap: Int)

/** Match iOS ThumbStrip: shrink the whole row uniformly to its proposed width. */
internal fun imageRowGeometry(aspects: List<Float>, available: Int, height: Int, maxWidth: Int, spacing: Int): ImageRowGeometry {
    val gap = if (aspects.size > 1) minOf(spacing, available / (aspects.size - 1)) else 0
    val ideal = aspects.map { minOf(maxWidth.toFloat(), height * it) }
    val total = ideal.sum()
    val room = (available - gap * (aspects.size - 1).coerceAtLeast(0)).coerceAtLeast(0)
    val scale = if (total > 0) minOf(1f, room / total) else 1f
    return ImageRowGeometry(ideal.map { (it * scale).toInt().coerceAtLeast(0) }, (height * scale).toInt().coerceAtLeast(0), gap)
}

/**
 * A message's or tool's images: previews at their own proportions, four to
 * a row at most, the rest folded into +N. The corner number is the CLI's
 * [Image #N], so it matches the text and the terminal. A tap opens the
 * viewer at that image.
 */
@Composable
fun ImageRow(images: List<SessionImageView>, onOpen: (Int) -> Unit, modifier: Modifier = Modifier, height: Int = 88) {
    if (images.isEmpty()) return
    val shown = images.take(4)
    Layout(modifier = modifier, content = {
        shown.forEachIndexed { i, img ->
            val more = if (i == 3 && images.size > 4) images.size - 4 else 0
            Thumb(img, Modifier, more) { onOpen(i) }
        }
    }) { children, constraints ->
        val geometry = imageRowGeometry(shown.map(::clampedAspect), constraints.maxWidth, height.dp.roundToPx(), 180.dp.roundToPx(), 6.dp.roundToPx())
        val placed = children.mapIndexed { i, child -> child.measure(Constraints.fixed(geometry.widths[i], geometry.height)) }
        layout(constraints.constrainWidth(geometry.widths.sum() + geometry.gap * (shown.size - 1)), constraints.constrainHeight(geometry.height)) {
            var x = 0
            for (child in placed) { child.placeRelative(x, 0); x += child.width + geometry.gap }
        }
    }
}

@Composable
fun Thumb(image: SessionImageView, modifier: Modifier, more: Int = 0, onClick: () -> Unit) {
    val copy = uiText()
    val p = pal
    val bmp = rememberPreview(image)
    val label = image.displayName.ifEmpty { copy(UiMessages.imageTitle()) }
    Box(modifier.clip(Radii.R14).background(p.fill).clickable(onClick = onClick)) {
        if (bmp != null) Image(bmp, label, Modifier.fillMaxSize(), contentScale = ContentScale.Crop)
        else Column(Modifier.fillMaxSize().padding(6.dp), verticalArrangement = Arrangement.Center, horizontalAlignment = Alignment.CenterHorizontally) {
            // Not even a preview: the file name is all there is.
            Text(if (image.syncState == SessionImageSyncState.SESSION_IMAGE_SYNC_STATE_NAME_ONLY) copy(UiMessages.imageUnsynced()) else copy(UiMessages.imageAndroidNoPreview()), style = T.s12, color = p.ink2)
            if (image.displayName.isNotEmpty()) Text(image.displayName, style = T.s12, color = p.ink3, maxLines = 2, overflow = TextOverflow.Ellipsis)
        }
        if (image.cliIndex > 0) Text(
            image.cliIndex.toString(),
            Modifier.align(Alignment.TopStart).padding(4.dp).background(Color(0x99000000), RoundedCornerShape(6.dp)).padding(horizontal = 5.dp),
            color = Color.White, style = T.s12b,
        )
        if (image.syncState == SessionImageSyncState.SESSION_IMAGE_SYNC_STATE_PREVIEW_ONLY && more == 0) Text(
            copy(UiMessages.imagePreviewOnly()),
            Modifier.align(Alignment.BottomStart).padding(4.dp).background(Color(0x99000000), RoundedCornerShape(6.dp)).padding(horizontal = 5.dp),
            color = Color.White, style = T.s12b,
        )
        if (more > 0) Box(Modifier.fillMaxSize().background(Color(0x88000000)), contentAlignment = Alignment.Center) {
            Text("+$more", color = Color.White, style = T.s17b)
        }
    }
}

/** A generated image: its own card across the chat, with the prompt the CLI rewrote behind 提示词. */
@Composable
fun GeneratedCard(rowKey: String, images: List<SessionImageView>, prompt: String, onOpen: (Int) -> Unit) {
    val copy = uiText()
    val p = pal
    var open by rememberSaveable(rowKey) { mutableStateOf(false) }
    Column(Modifier.padding(horizontal = ChatSide, vertical = 6.dp).fillMaxWidth().clip(Radii.R18).background(p.gbg)) {
        Column(Modifier.padding(12.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            images.forEachIndexed { i, img ->
                val bmp = rememberPreview(img)
                Box(Modifier.fillMaxWidth().aspectRatio(clampedAspect(img)).clip(Radii.R14).background(p.fill).clickable { onOpen(i) }) {
                    if (bmp != null) Image(bmp, copy(UiMessages.imageGenerated()), Modifier.fillMaxSize(), contentScale = ContentScale.Fit)
                    else Text(copy(UiMessages.imageAndroidNoPreview()), Modifier.align(Alignment.Center), color = p.ink2)
                }
            }
            Row(verticalAlignment = Alignment.CenterVertically) {
                val r = images.firstOrNull()
                val size = r?.sizeText?.takeIf { it.isNotEmpty() }?.let { " · $it" }.orEmpty()
                Text(copy(UiMessages.imageAndroidGeneratedSize(size = size)), Modifier.weight(1f), style = T.s13, color = p.ink2)
                if (prompt.isNotEmpty()) Box(Modifier.clickable { open = !open }.padding(6.dp)) { ArrowText(copy(UiMessages.imagePrompt()), p.blue, T.s13, down = !open, up = open) }
            }
            if (open) Text(prompt, style = T.s15, color = p.ink)
        }
    }
}
