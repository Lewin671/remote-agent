package io.github.lewin671.remoteagent.ui

import android.graphics.Bitmap
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.snap
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.gestures.rememberTransformableState
import androidx.compose.foundation.gestures.transformable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.asPaddingValues
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.navigationBars
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.requiredWidth
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsPropertyKey
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.IntSize
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.data.SharedPdfDocument
import io.github.lewin671.remoteagent.data.pdfRetainedPages
import java.io.File
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.awaitCancellation
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.drop
import kotlin.math.roundToInt

internal val PdfZoomScale = SemanticsPropertyKey<Float>("PdfZoomScale")
internal val PdfCachedPages = SemanticsPropertyKey<List<Int>>("PdfCachedPages")

/** Loading failures never open a dialog; later render failures also return to the same chooser. */
@Composable
internal fun SharedFilePdfViewer(
    fileName: String,
    localFile: File,
    onDismiss: () -> Unit,
    onUnsupported: () -> Unit,
    onAction: (SharedMenuAction) -> Unit,
) {
    var document by remember(localFile) { mutableStateOf<SharedPdfDocument?>(null) }
    val unsupported by rememberUpdatedState(onUnsupported)
    LaunchedEffect(localFile) {
        val owned = SharedPdfDocument.open(localFile)
        if (owned == null) {
            unsupported()
            return@LaunchedEffect
        }
        try {
            document = owned
            awaitCancellation()
        } finally {
            document = null
            owned.close()
        }
    }
    document?.let { pdf ->
        SharedFileViewerDialog(onDismiss) {
            SharedFileViewerPage(fileName, onDismiss, onAction, tag = "pdf-viewer") {
                SharedPdfPages(pdf, onUnsupported, Modifier.weight(1f))
            }
        }
    }
}

internal class PdfZoomState {
    var scale by mutableFloatStateOf(1f)
        private set
    var panX by mutableFloatStateOf(0f)
        private set

    fun transform(factor: Float, pan: Offset, focus: Offset, viewportWidth: Float, pageWidth: Float, list: LazyListState) {
        val next = (scale * factor).coerceIn(1f, 4f)
        val ratio = next / scale
        if (next != scale) {
            val anchor = list.layoutInfo.visibleItemsInfo.lastOrNull { it.offset <= focus.y }
                ?: list.layoutInfo.visibleItemsInfo.firstOrNull()
            if (anchor != null) {
                list.requestScrollToItem(anchor.index, ((focus.y - anchor.offset) * ratio - focus.y).roundToInt())
            }
        }
        val centered = focus.x - viewportWidth / 2f
        val limit = (pageWidth * next - pageWidth) / 2f
        panX = ((panX - centered) * ratio + centered + pan.x).coerceIn(-limit, limit)
        scale = next
        // At 1x the LazyColumn owns drag/fling. Zoomed drags pan through the whole document.
        if (pan.y != 0f) list.dispatchRawDelta(-pan.y)
    }
}

@Composable
private fun SharedPdfPages(document: SharedPdfDocument, onUnsupported: () -> Unit, modifier: Modifier) {
    val list = rememberLazyListState()
    val zoom = remember(document) { PdfZoomState() }
    val unsupported by rememberUpdatedState(onUnsupported)
    val bottom = WindowInsets.navigationBars.asPaddingValues().calculateBottomPadding()
    BoxWithConstraints(modifier.fillMaxWidth().clipToBounds()) {
        val density = LocalDensity.current
        val viewportWidth = constraints.maxWidth
        val viewportHeight = constraints.maxHeight
        val pageWidth = maxWidth - 24.dp
        val pageWidthPx = with(density) { pageWidth.toPx() }
        val bitmaps = remember(document, viewportWidth) { mutableStateMapOf<Int, Bitmap>() }
        val visible by remember(list) {
            derivedStateOf {
                val info = list.layoutInfo
                info.visibleItemsInfo.filter { it.offset + it.size > info.viewportStartOffset && it.offset < info.viewportEndOffset }
                    .map { it.index }
            }
        }
        LaunchedEffect(document, viewportWidth) {
            try {
                snapshotFlow { visible }.distinctUntilChanged().collectLatest { indices ->
                    val retain = pdfRetainedPages(indices, document.pages.size)
                    bitmaps.keys.toList().filter { it !in retain }.forEach { bitmaps.remove(it)?.recycle() }
                    // Visible pages first, then the one-page buffer on either side.
                    for (index in indices + retain.filter { it !in indices }) {
                        if (index !in bitmaps) bitmaps[index] = document.render(index, viewportWidth)
                    }
                }
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (_: Exception) {
                unsupported()
            } catch (_: OutOfMemoryError) {
                unsupported()
            } finally {
                bitmaps.values.forEach { it.recycle() }
                bitmaps.clear()
            }
        }
        val transform = rememberTransformableState { factor, pan, _ ->
            zoom.transform(factor, pan, Offset(viewportWidth / 2f, viewportHeight / 2f), viewportWidth.toFloat(), pageWidthPx, list)
        }
        LazyColumn(
            state = list,
            userScrollEnabled = zoom.scale == 1f,
            modifier = Modifier.fillMaxSize().testTag("pdf-pages")
                .semantics {
                    this[PdfZoomScale] = zoom.scale
                    this[PdfCachedPages] = bitmaps.keys.sorted()
                }
                .pointerInput(document, viewportWidth) {
                    detectTapGestures(onDoubleTap = { focus ->
                        val target = if (zoom.scale == 1f) 2.5f else 1f
                        zoom.transform(target / zoom.scale, Offset.Zero, focus, viewportWidth.toFloat(), pageWidthPx, list)
                    })
                }
                .transformable(transform, canPan = { zoom.scale > 1f }),
            contentPadding = PaddingValues(start = 12.dp, end = 12.dp, bottom = 12.dp + bottom),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            items(document.pages.indices.toList(), key = { it }) { index ->
                val dimensions = document.pages[index]
                val bitmap = bitmaps[index]
                val height = pageWidth * (dimensions.height.toFloat() / dimensions.width) * zoom.scale
                Box(Modifier.fillMaxWidth().height(height), contentAlignment = Alignment.Center) {
                    val image = remember(bitmap) { bitmap?.asImageBitmap() }
                    Canvas(
                        Modifier.requiredWidth(pageWidth * zoom.scale).height(height)
                            .graphicsLayer { translationX = zoom.panX }
                            .clip(RoundedCornerShape(4.dp))
                            .background(if (bitmap == null) pal.bubble else Color.White)
                            .testTag(if (bitmap == null) "pdf-placeholder-$index" else "pdf-page-$index"),
                    ) {
                        // A page can remain composed for LazyColumn prefetch after cache eviction.
                        if (image != null && bitmap?.isRecycled == false) {
                            drawImage(image, dstSize = IntSize(size.width.roundToInt(), size.height.roundToInt()))
                        }
                    }
                }
            }
        }
        var badgeVisible by remember { mutableStateOf(false) }
        LaunchedEffect(list) {
            snapshotFlow { Triple(list.firstVisibleItemIndex, list.firstVisibleItemScrollOffset, list.isScrollInProgress) }
                .drop(1).collectLatest { (_, _, scrolling) ->
                    badgeVisible = true
                    if (scrolling) awaitCancellation()
                    delay(1500)
                    badgeVisible = false
                }
        }
        val currentPage by remember(list) {
            derivedStateOf {
                val info = list.layoutInfo
                (info.visibleItemsInfo.maxByOrNull {
                    minOf(it.offset + it.size, info.viewportEndOffset) - maxOf(it.offset, info.viewportStartOffset)
                }?.index ?: 0) + 1
            }
        }
        val badgeAlpha by animateFloatAsState(if (badgeVisible) 1f else 0f, if (badgeVisible) snap() else tween(200))
        if (badgeAlpha > 0f) Text(
            uiText(UiMessages.sharedFilePdfPagePosition(current = currentPage, total = document.pages.size)),
            color = Color.White, style = T.s12,
            modifier = Modifier.align(Alignment.BottomEnd).padding(end = 16.dp, bottom = 16.dp + bottom)
                .alpha(badgeAlpha).clip(RoundedCornerShape(50)).background(Color.Black.copy(alpha = 0.65f))
                .padding(horizontal = 10.dp, vertical = 6.dp).testTag("pdf-page-position"),
        )
    }
}
