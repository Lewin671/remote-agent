package io.github.lewin671.remoteagent.ui

import android.content.Context
import android.content.Intent
import android.net.Uri
import android.provider.DocumentsContract
import android.provider.OpenableColumns
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
import androidx.compose.foundation.layout.BoxScope
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalViewConfiguration
import androidx.compose.ui.platform.ViewConfiguration
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.DpSize
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.coreTextToMessageText
import io.github.lewin671.remoteagent.core.draftScopeStorageKey
import io.github.lewin671.remoteagent.core.newRequestId
import io.github.lewin671.remoteagent.core.proto.AttachmentCandidateInput
import io.github.lewin671.remoteagent.core.proto.AttachmentDraftItemView
import io.github.lewin671.remoteagent.core.proto.AttachmentDraftView
import io.github.lewin671.remoteagent.core.proto.AttachmentItemKind
import io.github.lewin671.remoteagent.core.proto.AttachmentItemState
import io.github.lewin671.remoteagent.core.proto.DraftPickedItem
import io.github.lewin671.remoteagent.core.proto.DraftScope
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/** Ephemeral UI-only notice for taps on disabled `+` / `原图` controls, keyed by scope storage key. */
internal object DraftUiNotices {
    val text = androidx.compose.runtime.mutableStateMapOf<String, String>()
    fun show(scopeKey: String, message: String) {
        if (scopeKey.isNotEmpty() && message.isNotEmpty()) text[scopeKey] = message
    }
    fun clear() { text.clear() }
}

internal data class PickedUriMeta(
    val uri: Uri,
    val name: String,
    val mimeType: String,
    val sizeBytes: Long,
    val sizeUnknown: Boolean,
    val isDirectory: Boolean,
)

/** Reads display name, MIME type, directory flag, and provider size hint for one picked URI on IO. */
internal fun queryPickedUriMeta(context: Context, uri: Uri): PickedUriMeta {
    var name: String? = null
    var size = -1L
    val mime = runCatching { context.contentResolver.getType(uri) }.getOrNull().orEmpty()
    runCatching {
        context.contentResolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE), null, null, null)?.use {
            if (it.moveToFirst()) {
                val n = it.getColumnIndex(OpenableColumns.DISPLAY_NAME)
                val b = it.getColumnIndex(OpenableColumns.SIZE)
                if (n >= 0 && !it.isNull(n)) name = it.getString(n)
                if (b >= 0 && !it.isNull(b)) size = it.getLong(b)
            }
        }
    }
    val resolvedName = name?.takeIf { it.isNotBlank() } ?: uri.lastPathSegment.orEmpty()
    return PickedUriMeta(
        uri = uri,
        name = resolvedName,
        mimeType = mime,
        sizeBytes = if (size >= 0L) size else 0L,
        sizeUnknown = size < 0L,
        isDirectory = mime == DocumentsContract.Document.MIME_TYPE_DIR,
    )
}

/**
 * Reserves slots in Core, inspects picked URIs on IO, takes persistable read grants where
 * supported, and atomically hands the batch to Core via `DraftAddItemsAction`.
 */
internal fun dispatchPickedUris(
    context: Context,
    remote: Remote,
    scope: DraftScope,
    uris: List<Uri>,
    forceFile: Boolean,
) {
    if (uris.isEmpty()) return
    val reservationId = newRequestId()
    remote.reserveDraftSlots(
        scope = scope,
        reservationId = reservationId,
        imageSlots = if (forceFile) 0 else uris.size,
        fileSlots = if (forceFile) uris.size else 0,
    )
    val appScope = RaApp.scopeOrNull()
    val work: suspend () -> Unit = {
        try {
            val pickedItems = withContext(Dispatchers.IO) {
                uris.map { uri ->
                    val meta = queryPickedUriMeta(context, uri)
                    runCatching {
                        context.contentResolver.takePersistableUriPermission(
                            uri,
                            Intent.FLAG_GRANT_READ_URI_PERMISSION,
                        )
                    }
                    DraftPickedItem.newBuilder()
                        .setCandidate(
                            AttachmentCandidateInput.newBuilder()
                                .setName(meta.name)
                                .setMimeType(meta.mimeType)
                                .setSizeBytes(meta.sizeBytes)
                                .setSizeUnknown(meta.sizeUnknown)
                                .setIsDirectory(meta.isDirectory)
                                .setForceFile(forceFile),
                        )
                        .setSourceRef(uri.toString())
                        .setHasOriginalSource(true)
                        .build()
                }
            }
            remote.addDraftItems(scope, pickedItems, reservationId = reservationId)
        } catch (e: CancellationException) {
            remote.releaseDraftSlots(scope, reservationId)
            throw e
        } catch (_: Throwable) {
            remote.releaseDraftSlots(scope, reservationId)
        }
    }
    if (appScope != null) {
        appScope.launch { work() }
    } else {
        remote.releaseDraftSlots(scope, reservationId)
    }
}

/**
 * Photo/video gallery picker driven purely by Core's [AttachmentDraftView].
 */
@Composable
internal fun rememberImagePicker(
    draft: AttachmentDraftView,
    scope: DraftScope,
    remote: Remote,
): () -> Unit {
    val copy = uiText()
    val context = LocalContext.current
    val scopeKey = draftScopeStorageKey(scope)
    val contractCap = remember {
        maxOf(2, (draft.maxImages.takeIf { it > 0 } ?: 10) + (draft.maxFiles.takeIf { it > 0 } ?: 10))
    }
    val many = rememberLauncherForActivityResult(ActivityResultContracts.PickMultipleVisualMedia(contractCap)) { uris ->
        if (uris.isNotEmpty()) dispatchPickedUris(context, remote, scope, uris, forceFile = false)
    }
    val one = rememberLauncherForActivityResult(ActivityResultContracts.PickVisualMedia()) { uri ->
        if (uri != null) dispatchPickedUris(context, remote, scope, listOf(uri), forceFile = false)
    }
    return {
        if (!draft.restoring) {
            if (!draft.canPickGallery) {
                val reason = blockedPickReason(draft, copy)
                if (reason.isNotEmpty()) DraftUiNotices.show(scopeKey, reason)
            } else {
                val type = when {
                    !draft.canPickImages -> ActivityResultContracts.PickVisualMedia.VideoOnly
                    draft.galleryAllowsVideo && draft.canPickFiles -> ActivityResultContracts.PickVisualMedia.ImageAndVideo
                    else -> ActivityResultContracts.PickVisualMedia.ImageOnly
                }
                val req = PickVisualMediaRequest(type)
                if (draft.libraryRoom == 1) one.launch(req) else many.launch(req)
            }
        }
    }
}

internal fun blockedPickReason(
    draft: AttachmentDraftView,
    copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String,
): String {
    val reasonText = when {
        draft.hasGalleryBlockedReason() -> draft.galleryBlockedReason
        draft.hasFilesPickBlockedReason() -> draft.filesPickBlockedReason
        draft.hasImagesPickBlockedReason() -> draft.imagesPickBlockedReason
        else -> null
    }
    return reasonText?.let { coreTextToMessageText(it).resolve(copy) }.orEmpty()
}

internal fun showAttachmentBlockedNotice(
    draft: AttachmentDraftView,
    scope: DraftScope,
    copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String,
) {
    val reason = blockedPickReason(draft, copy)
    if (reason.isNotEmpty()) DraftUiNotices.show(draftScopeStorageKey(scope), reason)
}

/**
 * Pure renderer for Core's [AttachmentDraftView]: image/video thumbnail strip, original toggle
 * with Core's formatted total size (`original_total_size_text`), notices, file chips, and
 * Core-driven [DraftImageViewer].
 */
@Composable
internal fun AttachmentDraftStrip(
    draft: AttachmentDraftView,
    scope: DraftScope,
    remote: Remote,
) {
    val copy = uiText()
    val p = pal
    val scopeKey = draftScopeStorageKey(scope)
    val localNotice = DraftUiNotices.text[scopeKey]
    LaunchedEffect(localNotice) {
        if (localNotice != null) {
            kotlinx.coroutines.delay(4000)
            DraftUiNotices.text.remove(scopeKey)
        }
    }
    var showRestoring by remember(scopeKey) { mutableStateOf(false) }
    LaunchedEffect(scopeKey, draft.restoring) {
        if (draft.restoring) {
            kotlinx.coroutines.delay(250)
            showRestoring = true
        } else {
            showRestoring = false
        }
    }
    if (draft.restoring) {
        if (showRestoring) {
            Text(
                copy(UiMessages.imageAndroidRestoring()),
                Modifier.padding(horizontal = 12.dp, vertical = 4.dp),
                style = T.s13,
                color = p.ink2,
            )
        }
        return
    }

    val coreNotice = if (draft.hasNotice()) uiText(draft.notice).ifEmpty { null } else null
    val storageErr = if (draft.hasStorageError()) uiText(draft.storageError).ifEmpty { null } else null
    val summaryLine = if (draft.hasFailureSummary()) uiText(draft.failureSummary).ifEmpty { null } else null

    val thumbs = draft.itemsList.filter { it.kind == AttachmentItemKind.ATTACHMENT_ITEM_KIND_IMAGE || it.isVideo }
    if (thumbs.isEmpty()) {
        val line = storageErr ?: localNotice ?: coreNotice
        if (line != null) {
            Text(
                line,
                Modifier.padding(horizontal = 12.dp, vertical = 4.dp),
                style = T.s13,
                color = if (storageErr != null) p.red else p.orangeInk,
            )
        }
    } else {
        Column(Modifier.fillMaxWidth().padding(bottom = 6.dp)) {
            Row(Modifier.horizontalScroll(rememberScrollState()).padding(bottom = 6.dp)) {
                for (item in thumbs) {
                    DraftThumbCell(
                        item = item,
                        original = draft.original,
                        onTap = {
                            if (item.kind == AttachmentItemKind.ATTACHMENT_ITEM_KIND_IMAGE) {
                                remote.openDraftImageViewer(scope, item.imageIndex.coerceAtLeast(0))
                            } else {
                                when {
                                    item.canRetry -> remote.retryDraftItem(scope, item.itemId)
                                    item.canResume -> remote.resumeAttachmentUpload(scope, item.itemId)
                                    item.canPause -> remote.pauseAttachmentUpload(scope, item.itemId)
                                }
                            }
                        },
                        onRemove = if (item.canRemove) ({ remote.removeDraftItem(scope, item.itemId) }) else null,
                    )
                }
            }
            if (draft.imagesCount > 0) {
                Row(
                    Modifier.fillMaxWidth().padding(horizontal = 4.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    val toggleEnabled = draft.canToggleOriginal
                    val disabledReason = if (draft.hasOriginalToggleDisabledReason()) uiText(draft.originalToggleDisabledReason) else ""
                    Row(
                        Modifier
                            .heightIn(min = 44.dp)
                            .alpha(if (toggleEnabled) 1f else 0.45f)
                            .clickable(role = Role.Checkbox) {
                                if (toggleEnabled) {
                                    remote.setDraftOriginal(scope, !draft.original)
                                } else if (disabledReason.isNotEmpty()) {
                                    DraftUiNotices.show(scopeKey, disabledReason)
                                }
                            },
                        verticalAlignment = Alignment.CenterVertically,
                    ) {
                        Box(
                            Modifier
                                .size(16.dp)
                                .clip(CircleShape)
                                .border(
                                    if (draft.original) 5.dp else 1.5.dp,
                                    if (draft.original) p.blue else p.ink3,
                                    CircleShape,
                                ),
                        )
                        val origTotal = if (draft.original && draft.hasOriginalTotalSizeText()) {
                            uiText(draft.originalTotalSizeText)
                        } else {
                            ""
                        }
                        Text(
                            copy(UiMessages.imageAndroidOriginalMeta()) + origTotal,
                            style = T.s13,
                            color = p.ink,
                        )
                    }
                    Spacer(Modifier.weight(1f))
                    val countText = if (draft.hasImageCountText()) uiText(draft.imageCountText) else ""
                    if (countText.isNotEmpty()) {
                        Text(countText, style = T.s13, color = p.ink2)
                    }
                }
            }
            if (storageErr != null) {
                Text(storageErr, style = T.s13, color = p.red, modifier = Modifier.padding(horizontal = 8.dp, vertical = 2.dp))
            }
            val line = summaryLine ?: localNotice ?: coreNotice
            if (line != null) {
                Text(line, style = T.s13, color = p.orangeInk, modifier = Modifier.padding(horizontal = 8.dp, vertical = 2.dp))
            }
            if (draft.original && draft.imagesCount > 0 && draft.hasOriginalHint()) {
                val hint = uiText(draft.originalHint)
                if (hint.isNotEmpty()) {
                    Text(hint, style = T.s13, color = p.ink2, modifier = Modifier.padding(horizontal = 8.dp, vertical = 2.dp))
                }
            }
        }
    }

    FileStrip(draft = draft, scope = scope, remote = remote)

    if (draft.hasImageViewer() && draft.imageViewer.open) {
        DraftImageViewer(draft = draft, scope = scope, remote = remote)
    }
}

@Composable
private fun DraftThumbCell(
    item: AttachmentDraftItemView,
    original: Boolean,
    onTap: () -> Unit,
    onRemove: (() -> Unit)?,
) {
    val copy = uiText()
    val p = pal
    val isImage = item.kind == AttachmentItemKind.ATTACHMENT_ITEM_KIND_IMAGE
    val posA11y = if (item.hasPositionA11Y()) uiText(item.positionA11Y) else item.displayName
    val statusWords = if (item.hasStatusText()) uiText(item.statusText) else ""
    val removeA11y = if (item.hasRemoveA11Y()) uiText(item.removeA11Y) else copy(UiMessages.commonRemove())

    // Reserve the upper/right half of the corner-centred remove target.
    Box(Modifier.width(86.dp).height(86.dp)) {
        Box(
            Modifier
                .align(Alignment.BottomStart)
                .size(64.dp)
                .clip(Radii.R14)
                .background(p.fill)
                .clickable(role = Role.Button, onClick = onTap)
                .semantics {
                    if (posA11y.isNotEmpty()) contentDescription = posA11y
                    if (statusWords.isNotEmpty()) stateDescription = statusWords
                },
        ) {
            if (isImage) {
                DraftImageThumb(item.thumbRef, item.preparedRef)
            } else {
                VideoFrame(item)
            }
            when (item.state) {
                AttachmentItemState.ATTACHMENT_ITEM_STATE_PREPARING -> {
                    CircularProgressIndicator(
                        Modifier.align(Alignment.Center).size(24.dp),
                        color = Color.White,
                        strokeWidth = 3.dp,
                        trackColor = Color(0x59FFFFFF),
                    )
                }
                AttachmentItemState.ATTACHMENT_ITEM_STATE_UPLOADING,
                AttachmentItemState.ATTACHMENT_ITEM_STATE_WAITING_RECONNECT -> {
                    CircularProgressIndicator(
                        Modifier.align(Alignment.Center).size(24.dp),
                        color = Color.White,
                        strokeWidth = 3.dp,
                        trackColor = Color(0x59FFFFFF),
                    )
                    if (original || !isImage) {
                        Box(
                            Modifier
                                .align(Alignment.BottomCenter)
                                .fillMaxWidth()
                                .height(4.dp)
                                .background(Color(0x59000000)),
                        ) {
                            Box(
                                Modifier
                                    .fillMaxWidth(item.progress.toFloat().coerceIn(0f, 1f))
                                    .height(4.dp)
                                    .background(Color.White),
                            )
                        }
                    }
                }
                AttachmentItemState.ATTACHMENT_ITEM_STATE_PAUSED -> {
                    Box(
                        Modifier.fillMaxSize().background(Color(0x99000000)),
                        contentAlignment = Alignment.BottomCenter,
                    ) {
                        Text(
                            if (item.canResume) copy(UiMessages.scheduleResume()) else copy(UiMessages.attachmentUploadPaused()),
                            color = Color.White,
                            style = T.s12b,
                            modifier = Modifier.padding(bottom = 4.dp).clearAndSetSemantics {},
                        )
                    }
                }
                AttachmentItemState.ATTACHMENT_ITEM_STATE_FAILED -> {
                    Box(
                        Modifier.fillMaxSize().background(Color(0x99000000)),
                        contentAlignment = Alignment.BottomCenter,
                    ) {
                        Text(
                            if (!isImage && item.canRetry) copy(UiMessages.commonRetry()) else copy(UiMessages.attachmentFailed()),
                            color = Color.White,
                            style = T.s12b,
                            modifier = Modifier.padding(bottom = 4.dp).clearAndSetSemantics {},
                        )
                    }
                }
                else -> {}
            }
        }
        if (onRemove != null) {
            val viewConfiguration = LocalViewConfiguration.current
            CompositionLocalProvider(
                LocalViewConfiguration provides remember(viewConfiguration) {
                    object : ViewConfiguration by viewConfiguration {
                        override val minimumTouchTargetSize = DpSize(44.dp, 44.dp)
                    }
                },
            ) {
                Box(
                    Modifier
                        .align(Alignment.TopStart)
                        .offset(x = 42.dp)
                        .size(44.dp)
                        .clickable(role = Role.Button, onClick = onRemove)
                        .semantics { contentDescription = removeA11y },
                    contentAlignment = Alignment.Center,
                ) {
                    Box(
                        Modifier
                            .offset(x = (-11).dp, y = 9.dp)
                            .size(18.dp)
                            .clip(CircleShape)
                            .background(Color(0x99000000)),
                        contentAlignment = Alignment.Center,
                    ) {
                        Ic(Lucide.X, 12.dp, Color.White, contentDescription = null)
                    }
                }
            }
        }
    }
}

@Composable
private fun DraftImageThumb(thumbRef: String, preparedRef: String) {
    val app = remember { RaApp.instanceOrNull() }
    val bmp by produceState<ImageBitmap?>(null, thumbRef, preparedRef) {
        if (app != null && (thumbRef.isNotEmpty() || preparedRef.isNotEmpty())) {
            value = app.attachments.loadThumbnail(thumbRef, preparedRef)
        }
    }
    bmp?.let { Image(it, null, Modifier.fillMaxSize(), contentScale = ContentScale.Crop) }
}

/** A video's first frame with Core's formatted duration in a corner; fallback to icon and name. */
@Composable
private fun BoxScope.VideoFrame(item: AttachmentDraftItemView) {
    val context = LocalContext.current
    val frame by produceState(VideoFrames.cache[item.sourceRef], item.sourceRef) {
        if (value == null && item.sourceRef.isNotEmpty()) {
            value = withContext(Dispatchers.IO) { VideoFrames.read(context, item.sourceRef) }
        }
    }
    val f = frame
    if (f != null) {
        Image(f, null, Modifier.fillMaxSize(), contentScale = ContentScale.Crop)
    } else {
        Column(
            Modifier.fillMaxSize().padding(6.dp),
            verticalArrangement = Arrangement.Center,
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            Ic(Lucide.File, 18.dp, pal.ink2)
            Text(item.displayName, style = T.s10, color = pal.ink2, maxLines = 2, overflow = TextOverflow.MiddleEllipsis)
        }
    }
    if (item.durationText.isNotEmpty()) {
        Text(
            item.durationText,
            style = T.s10.copy(fontWeight = T.s12b.fontWeight),
            color = Color.White,
            modifier = Modifier
                .align(Alignment.BottomStart)
                .padding(4.dp)
                .clip(Radii.R6)
                .background(Color(0x8C000000))
                .padding(horizontal = 4.dp, vertical = 1.dp),
        )
    }
}

/** First frames of picked videos, cached in memory for this run. */
private object VideoFrames {
    val cache = java.util.concurrent.ConcurrentHashMap<String, ImageBitmap>()

    fun read(context: Context, sourceRef: String): ImageBitmap? {
        cache[sourceRef]?.let { return it }
        val r = android.media.MediaMetadataRetriever()
        val bitmap = try {
            r.setDataSource(context, Uri.parse(sourceRef))
            runCatching { r.getFrameAtTime(0, android.media.MediaMetadataRetriever.OPTION_CLOSEST_SYNC) }.getOrNull()?.let { b ->
                val scale = 192f / maxOf(b.width, b.height)
                if (scale >= 1f) b
                else android.graphics.Bitmap.createScaledBitmap(
                    b,
                    (b.width * scale).toInt().coerceAtLeast(1),
                    (b.height * scale).toInt().coerceAtLeast(1),
                    true,
                ).also { b.recycle() }
            }
        } catch (_: Exception) {
            null
        } finally {
            runCatching { r.release() }
        }
        return bitmap?.asImageBitmap()?.also { cache[sourceRef] = it }
    }
}
