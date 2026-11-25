package io.github.lewin671.remoteagent.ui

import android.content.ContentValues
import android.content.Context
import android.content.Intent
import android.graphics.BitmapFactory
import android.os.Build
import android.provider.MediaStore
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.Orientation
import androidx.compose.foundation.gestures.draggable
import androidx.compose.foundation.gestures.rememberDraggableState
import androidx.compose.foundation.gestures.rememberTransformableState
import androidx.compose.foundation.gestures.transformable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.pager.HorizontalPager
import androidx.compose.foundation.pager.rememberPagerState
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.core.content.FileProvider
import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.RETIRED_HISTORY_ERROR
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.ConnState as CoreConnState
import io.github.lewin671.remoteagent.core.proto.SessionImageSyncState
import io.github.lewin671.remoteagent.core.proto.SessionImageView
import io.github.lewin671.remoteagent.core.proto.SessionTimelineItem
import io.github.lewin671.remoteagent.core.proto.SessionViewModel
import io.github.lewin671.remoteagent.core.proto.SessionViewerImageView
import io.github.lewin671.remoteagent.proto.ImageFormat
import java.io.File
import java.util.concurrent.atomic.AtomicBoolean
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/** Where a display copy stands while the viewer shows an image. */
sealed interface Big {
    data object Loading : Big

    class Ready(
        val bitmap: ImageBitmap,
        val bytes: ByteArray,
        val format: ImageFormat,
    ) : Big

    /** The event says there is no display copy. */
    data object NotSynced : Big

    data object Expired : Big

    /** Never fetched and no connection to fetch it. */
    data object Offline : Big

    data class Failed(val why: String) : Big

    data class Corrupt(val why: io.github.lewin671.remoteagent.core.MessageText) : Big
}

/**
 * The image viewer: every image of the session projected by Core (`timeline.viewerImages` +
 * `imageViewer`), paged from the tapped anchor. Pinch to zoom, swipe down or back to close;
 * save and share hand the picture to the system.
 */
@Composable
fun ImageViewer(sid: String, seq: Long, index: Int) {
    val copy = uiText()
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val closed = remember(remote, sid, seq, index) { AtomicBoolean(false) }
    val closeCoreOnly = remember(remote, sid, closed) {
        {
            if (closed.compareAndSet(false, true)) {
                remote.closeSessionImageViewer(sid)
            }
        }
    }
    val closeAndBack = remember(closeCoreOnly, nav) {
        {
            closeCoreOnly()
            nav.back()
        }
    }

    LaunchedEffect(remote, sid) { remote.openSession(sid) }
    LaunchedEffect(remote, sid, seq, index) {
        remote.openSessionImageViewer(sid, seq = seq, imageIndex = index)
    }
    DisposableEffect(remote, sid, closeCoreOnly) {
        onDispose { closeCoreOnly() }
    }

    val vm by remember(remote, sid) { remote.watchSession(sid) }.collectAsState(
        initial = remember(sid) { remote.currentSessionView(sid) ?: SessionViewModel.getDefaultInstance() },
    )
    val all = vm.timeline.viewerImagesList
    val viewer = vm.imageViewer
    val previewsById = remember(vm.timeline.itemsList) { indexPreviewsById(vm.timeline.itemsList) }

    val start = remember(all.size, viewer.open, viewer.currentIndex, seq, index) {
        when {
            all.isEmpty() -> 0
            viewer.open -> viewer.currentIndex.coerceIn(0, all.lastIndex)
            else -> all.indexOfFirst { it.seq == seq && it.eventImageIndex == index }
                .takeIf { it >= 0 }
                ?: all.indexOfFirst { it.seq == seq }.takeIf { it >= 0 }?.let { (it + index).coerceIn(0, all.lastIndex) }
                ?: 0
        }
    }

    val emptyNote = when {
        viewer.hasEmptyText() -> uiText(viewer.emptyText)
        !vm.header.found && (vm.header.missing || (vm.header.sessionsListed && vm.header.hasMissingText())) ->
            uiText(vm.header.missingText).ifEmpty { copy(UiMessages.commonAndroidCannotFindThisSession()) }
        else -> copy(UiMessages.projectsReadingDirectory())
    }

    PagedImageViewer(
        count = all.size,
        start = start,
        onClose = closeAndBack,
        empty = emptyNote,
        syncedPage = if (viewer.open && all.isNotEmpty()) viewer.currentIndex.coerceIn(0, all.lastIndex) else null,
        onPageSettled = { page ->
            if (page != viewer.currentIndex) {
                remote.navigateSessionImageViewer(sid, setIndex = true, index = page)
            }
        },
        positionLabel = { page ->
            if (viewer.hasPositionText() && viewer.open && page == viewer.currentIndex) {
                uiText(viewer.positionText)
            } else {
                ""
            }
        },
        picture = { page ->
            val cur = all[page]
            ViewerPage(
                remote = remote,
                sid = sid,
                img = cur,
                previewBytes = previewsById[cur.imageId] ?: cur.preview,
                online = vm.header.connState == CoreConnState.CONN_STATE_CONNECTED,
            )
        },
        info = { page ->
            val cur = all[page]
            val from = if (cur.hasSourceLabel()) uiText(cur.sourceLabel) else ""
            if (from.isNotEmpty()) {
                Text(from, color = Color.White, style = T.s13b, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
            val path = cur.displayPath.ifEmpty { cur.displayName }
            if (path.isNotEmpty()) {
                Text(
                    path,
                    color = Color(0xFFD5DADE),
                    style = T.s13,
                    fontFamily = MonoFamily,
                    maxLines = 1,
                    overflow = TextOverflow.MiddleEllipsis,
                )
            }
            val title = uiText(vm.header.title)
            val meta = listOfNotNull(cur.sizeText.ifEmpty { null }, title.ifEmpty { null }).joinToString(" · ")
            if (meta.isNotEmpty()) {
                Text(meta, color = Color(0xFFB9BFC6), style = T.s13)
            }
            ViewerActions(remote, sid, cur, previewsById[cur.imageId] ?: cur.preview)
        },
    )
}

private fun indexPreviewsById(items: List<SessionTimelineItem>): Map<String, ByteString> = buildMap {
    fun record(images: List<SessionImageView>) {
        for (img in images) {
            if (img.imageId.isNotEmpty() && !img.preview.isEmpty) {
                put(img.imageId, img.preview)
            }
        }
    }
    fun visit(list: List<SessionTimelineItem>) {
        for (item in list) {
            when (item.kindCase) {
                SessionTimelineItem.KindCase.USER_MESSAGE -> record(item.userMessage.imageViewsList)
                SessionTimelineItem.KindCase.TOOL_CALL -> record(item.toolCall.imageViewsList)
                SessionTimelineItem.KindCase.TOOL_GROUP -> item.toolGroup.callsList.forEach { record(it.imageViewsList) }
                SessionTimelineItem.KindCase.GENERATED_IMAGES -> record(item.generatedImages.imageViewsList)
                SessionTimelineItem.KindCase.QUIET_SCHEDULED_GROUP -> visit(item.quietScheduledGroup.itemsList)
                else -> Unit
            }
        }
    }
    visit(items)
}

/** The same full-screen paging/close controls for message and draft images. */
@Composable
internal fun PagedImageViewer(
    count: Int,
    start: Int,
    onClose: () -> Unit,
    empty: String = uiText(UiMessages.imageAndroidEmpty()),
    syncedPage: Int? = null,
    onPageSettled: ((Int) -> Unit)? = null,
    positionLabel: (@Composable (Int) -> String)? = null,
    picture: @Composable (Int) -> Unit,
    info: @Composable (Int) -> Unit,
) {
    BackHandler { onClose() }
    Box(Modifier.fillMaxSize().background(Color.Black)) {
        if (count == 0) {
            Text(empty, Modifier.align(Alignment.Center), color = Color.White)
            return@Box
        }
        val pager = rememberPagerState(initialPage = start.coerceIn(0, count - 1)) { count }
        LaunchedEffect(syncedPage, count) {
            val target = syncedPage?.coerceIn(0, count - 1) ?: return@LaunchedEffect
            if (!pager.isScrollInProgress && pager.currentPage != target) {
                pager.scrollToPage(target)
            }
        }
        if (onPageSettled != null) {
            LaunchedEffect(pager) {
                snapshotFlow { pager.settledPage }
                    .distinctUntilChanged()
                    .collect { settled -> onPageSettled(settled) }
            }
        }
        var drag by remember { mutableFloatStateOf(0f) }
        HorizontalPager(
            pager,
            Modifier.fillMaxSize()
                .graphicsLayer { translationY = drag }
                .draggable(
                    rememberDraggableState { drag = (drag + it).coerceAtLeast(0f) },
                    Orientation.Vertical,
                    onDragStopped = { if (drag > 300f) onClose() else drag = 0f },
                ),
        ) { page ->
            picture(page)
        }
        val current = pager.currentPage.coerceAtMost(count - 1)
        Row(Modifier.fillMaxWidth().padding(8.dp), verticalAlignment = Alignment.CenterVertically) {
            GlassButton(Lucide.X, uiText(UiMessages.commonClose()), onClose, dark = true)
            Spacer(Modifier.width(12.dp))
            val posLabel = positionLabel?.invoke(current).orEmpty()
            if (posLabel.isNotEmpty()) {
                Text(
                    posLabel,
                    color = Color.White,
                    style = T.s15b,
                )
            }
        }
        Column(
            Modifier.align(Alignment.BottomCenter)
                .fillMaxWidth()
                .background(Color(0xAA000000))
                .padding(horizontal = 16.dp, vertical = 10.dp),
        ) {
            info(current)
        }
    }
}

/**
 * Images decrypted while the viewer is open, by session and image, so paging back does not fetch
 * again.
 */
internal val ViewerImages = AccountCache<Big.Ready>(6)

@Composable
private fun ViewerPage(
    remote: Remote,
    sid: String,
    img: SessionViewerImageView,
    previewBytes: ByteString,
    online: Boolean,
) {
    val copy = uiText()
    val preview = rememberPreviewBytes(img.imageId.ifEmpty { img.displayName }, previewBytes)
    var big by remember(img.imageId, img.hasDisplayCopy) { mutableStateOf<Big>(Big.Loading) }
    var attempt by remember { mutableIntStateOf(0) }
    LaunchedEffect(img.imageId, img.hasDisplayCopy, attempt, online) {
        big = load(remote, sid, img, online)
    }
    Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
        val b = big
        val bmp = (b as? Big.Ready)?.bitmap ?: preview
        val label = img.displayName.ifEmpty { img.displayPath.ifEmpty { copy(UiMessages.imageTitle()) } }
        if (bmp != null) ViewerBitmap(bmp, label)
        else Text(label, color = Color.White)
        val note = when (b) {
            Big.Loading -> null
            is Big.Ready -> null
            Big.NotSynced ->
                if (img.syncState == SessionImageSyncState.SESSION_IMAGE_SYNC_STATE_NAME_ONLY) copy(UiMessages.imageAndroidNameOnly())
                else copy(UiMessages.imageOriginalOnComputer())
            Big.Expired -> copy(UiMessages.imageOriginalExpired())
            Big.Offline -> copy(UiMessages.imageLoadConnected())
            is Big.Failed -> copy(UiMessages.imageAndroidDownloadRetry(reason = b.why))
            is Big.Corrupt -> if (b.why === RETIRED_HISTORY_ERROR) uiText(b.why) else copy(UiMessages.imageAndroidVerifyFailed(reason = uiText(b.why)))
        }
        if (b == Big.Loading) {
            Spinner(Color.White, Modifier.align(Alignment.Center), size = 32.dp, stroke = 3.dp)
        }
        if (note != null) {
            Text(
                note,
                Modifier.align(Alignment.TopCenter)
                    .padding(top = 72.dp)
                    .background(Color(0xCC000000), Radii.Pill)
                    .clickable(enabled = b is Big.Failed || b == Big.Offline) { attempt++ }
                    .padding(horizontal = 16.dp, vertical = 10.dp),
                color = Color.White,
                style = T.s15,
            )
        }
    }
}

@Composable
internal fun ViewerBitmap(bitmap: ImageBitmap, label: String) {
    var scale by remember(bitmap) { mutableFloatStateOf(1f) }
    var offset by remember(bitmap) { mutableStateOf(androidx.compose.ui.geometry.Offset.Zero) }
    val zoom = rememberTransformableState { z, pan, _ ->
        scale = (scale * z).coerceIn(1f, 6f)
        offset = if (scale == 1f) androidx.compose.ui.geometry.Offset.Zero else offset + pan
    }
    Image(
        bitmap,
        label,
        Modifier.fillMaxSize().transformable(zoom, canPan = { scale > 1f }).graphicsLayer {
            scaleX = scale
            scaleY = scale
            translationX = offset.x
            translationY = offset.y
        },
        contentScale = ContentScale.Fit,
    )
}

private suspend fun load(remote: Remote, sid: String, img: SessionViewerImageView, online: Boolean): Big {
    if (!img.hasDisplayCopy || img.imageId.isEmpty()) return Big.NotSynced
    ViewerImages.get(remote, "$sid/${img.imageId}")?.let { return it }
    return try {
        when (val r = remote.image(sid, img.imageId)) {
            is Remote.ImageResult.Loaded -> {
                val bytes = r.payload.data.toByteArray()
                val bmp = withContext(Dispatchers.Default) {
                    BitmapFactory.decodeByteArray(bytes, 0, bytes.size)
                } ?: return Big.Corrupt(io.github.lewin671.remoteagent.core.MessageText.Message(UiMessages.imageAndroidCannotDecodeImage()))
                Big.Ready(bmp.asImageBitmap(), bytes, r.payload.format).also {
                    ViewerImages.put(remote, "$sid/${img.imageId}", it)
                }
            }
            Remote.ImageResult.Expired -> Big.Expired
            Remote.ImageResult.Missing -> Big.NotSynced
            is Remote.ImageResult.Corrupt -> Big.Corrupt(r.why)
        }
    } catch (e: kotlin.coroutines.cancellation.CancellationException) {
        throw e
    } catch (e: Exception) {
        if (!online) Big.Offline else Big.Failed(displayError(e))
    }
}

/**
 * 保存 and 分享: the display copy (or the preview when that is all there is) leaves the app here, by
 * the user's choice.
 */
@Composable
private fun ViewerActions(
    remote: Remote,
    sid: String,
    img: SessionViewerImageView,
    previewBytes: ByteString,
) {
    val copy = uiText()
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    var msg by remember(img.imageId) { mutableStateOf("") }
    val mime = img.mimeType.ifEmpty { "image/jpeg" }
    val downloadName = img.downloadName

    fun bytes(): ByteArray? {
        ViewerImages.get(remote, "$sid/${img.imageId}")?.let { return it.bytes }
        val p = previewBytes.toByteArray()
        return if (p.isEmpty()) null else p
    }

    fun doSave() {
        val data = bytes() ?: return
        scope.launch {
            msg = try {
                withContext(Dispatchers.IO) {
                    save(context, data, mime, downloadName)
                }
                copy(UiMessages.imageAndroidSaved())
            } catch (e: Exception) {
                copy(UiMessages.projectsAndroidSaveFailed(reason = displayError(e)))
            }
        }
    }

    val askStorage = androidx.activity.compose.rememberLauncherForActivityResult(
        androidx.activity.result.contract.ActivityResultContracts.RequestPermission(),
    ) { ok ->
        if (ok) doSave() else msg = copy(UiMessages.imageAndroidSavePermission())
    }

    Row(
        horizontalArrangement = Arrangement.spacedBy(8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        TextButton(
            onClick = {
                if (
                    needsStoragePermission(Build.VERSION.SDK_INT) &&
                    androidx.core.content.ContextCompat.checkSelfPermission(
                        context,
                        android.Manifest.permission.WRITE_EXTERNAL_STORAGE,
                    ) != android.content.pm.PackageManager.PERMISSION_GRANTED
                ) {
                    askStorage.launch(android.Manifest.permission.WRITE_EXTERNAL_STORAGE)
                } else {
                    doSave()
                }
            },
        ) {
            Text(copy(UiMessages.commonSave()), style = T.s15b, color = CardColors.link)
        }
        TextButton(
            onClick = {
                val data = bytes() ?: return@TextButton
                scope.launch {
                    try {
                        val uri = withContext(Dispatchers.IO) { shareFile(context, data, downloadName) }
                        context.startActivity(
                            Intent.createChooser(
                                Intent(Intent.ACTION_SEND)
                                    .setType(mime)
                                    .putExtra(Intent.EXTRA_STREAM, uri)
                                    .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION),
                                copy(UiMessages.imageAndroidShareTitle()),
                            ),
                        )
                    } catch (e: Exception) {
                        msg = copy(UiMessages.imageShareFailed(reason = displayError(e)))
                    }
                }
            },
        ) {
            Text(copy(UiMessages.imageShare()), style = T.s15b, color = CardColors.link)
        }
        if (msg.isNotEmpty()) {
            Text(msg, color = Color.White, style = T.s13)
        }
    }
}

private fun save(
    context: Context,
    data: ByteArray,
    mime: String,
    downloadName: String,
    copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText,
) {
    val dot = downloadName.lastIndexOf('.')
    val displayName = if (dot > 0 && dot < downloadName.length - 1) {
        "${downloadName.substring(0, dot)}-${System.currentTimeMillis()}${downloadName.substring(dot)}"
    } else {
        "$downloadName-${System.currentTimeMillis()}"
    }
    val values = ContentValues().apply {
        put(MediaStore.Images.Media.DISPLAY_NAME, displayName)
        put(MediaStore.Images.Media.MIME_TYPE, mime)
        if (Build.VERSION.SDK_INT >= 29) {
            put(MediaStore.Images.Media.RELATIVE_PATH, "Pictures/remote-agent")
            put(MediaStore.Images.Media.IS_PENDING, 1)
        }
    }
    val resolver = context.contentResolver
    val uri = resolver.insert(MediaStore.Images.Media.EXTERNAL_CONTENT_URI, values)
        ?: throw IllegalStateException(copy(UiMessages.imageAndroidPhotoLibraryIsNotWritable()))
    resolver.openOutputStream(uri)?.use { it.write(data) }
        ?: throw IllegalStateException(copy(UiMessages.imageAndroidPhotoLibraryIsNotWritable()))
    if (Build.VERSION.SDK_INT >= 29) {
        values.clear()
        values.put(MediaStore.Images.Media.IS_PENDING, 0)
        resolver.update(uri, values, null, null)
    }
}

/** A file for the share sheet; shared files are deleted when the app starts again. */
private fun shareFile(context: Context, data: ByteArray, downloadName: String): android.net.Uri {
    val dir = File(File(context.cacheDir, SHARED_DIR), System.currentTimeMillis().toString()).apply { mkdirs() }
    val safeName = File(downloadName).name.ifEmpty { "image.bin" }
    val f = File(dir, safeName)
    f.writeBytes(data)
    return FileProvider.getUriForFile(context, context.packageName + ".files", f)
}

const val SHARED_DIR = "shared"

/** Saving to the gallery asks for the storage permission before Android 10 only. */
fun needsStoragePermission(sdk: Int): Boolean = sdk < 29
