package io.github.lewin671.remoteagent.ui

import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.coreTextToMessageText
import io.github.lewin671.remoteagent.core.draftScopeStorageKey
import io.github.lewin671.remoteagent.core.proto.AttachmentDraftView
import io.github.lewin671.remoteagent.core.proto.AttachmentItemState
import io.github.lewin671.remoteagent.core.proto.DraftScope
import io.github.lewin671.remoteagent.core.proto.SessionFileRefView

@Composable
internal fun rememberDocumentPicker(
    draft: AttachmentDraftView,
    scope: DraftScope,
    remote: Remote,
    supportsFiles: Boolean,
): (() -> Unit)? {
    val copy = uiText()
    val context = LocalContext.current
    val scopeKey = draftScopeStorageKey(scope)
    val picker = rememberLauncherForActivityResult(ActivityResultContracts.OpenMultipleDocuments()) { uris ->
        if (uris.isNotEmpty()) {
            dispatchPickedUris(context, remote, scope, uris, forceFile = true)
        }
    }
    if (!supportsFiles) return null
    return {
        if (!draft.restoring && !draft.hasPendingPicks) {
            if (!draft.canPickFiles) {
                val reason = if (draft.hasFilesPickBlockedReason()) {
                    coreTextToMessageText(draft.filesPickBlockedReason).resolve(copy)
                } else {
                    ""
                }
                if (reason.isNotEmpty()) DraftUiNotices.show(scopeKey, reason)
            } else {
                picker.launch(arrayOf("*/*"))
            }
        }
    }
}

/**
 * A file capsule in the composer draft or in a sent message: the file icon, its name (the middle
 * elided with extension preserved), its size and Core-provided status/progress, and optional
 * pause/resume/retry/remove actions. No file content is loaded to draw it.
 */
@Composable
internal fun FileChip(
    name: String,
    sizeText: String,
    state: AttachmentItemState? = null,
    progress: Float = 0f,
    statusText: String = "",
    onPause: (() -> Unit)? = null,
    onResume: (() -> Unit)? = null,
    onRetry: (() -> Unit)? = null,
    onRemove: (() -> Unit)? = null,
    removeContentDescription: String = "",
    nameHead: String = "",
    nameTail: String = "",
) {
    val copy = uiText()
    val p = pal
    val density = androidx.compose.ui.platform.LocalDensity.current
    var tall by remember { mutableStateOf(false) }
    val nameStyle = T.s15.copy(fontWeight = platformWeight(androidx.compose.ui.text.font.FontWeight.Medium))
    val isFailed = state == AttachmentItemState.ATTACHMENT_ITEM_STATE_FAILED
    val isUploading = state == AttachmentItemState.ATTACHMENT_ITEM_STATE_UPLOADING ||
        state == AttachmentItemState.ATTACHMENT_ITEM_STATE_WAITING_RECONNECT
    val hasTrailing = onRemove != null || onRetry != null || onPause != null || onResume != null
    Row(
        Modifier
            .fillMaxWidth()
            .heightIn(min = 52.dp)
            .onSizeChanged { tall = with(density) { it.height.toDp() } > 60.dp }
            .clip(if (tall) Radii.R20 else Radii.Pill)
            .background(p.fill)
            .padding(start = 14.dp, end = if (hasTrailing) 6.dp else 14.dp, top = 6.dp, bottom = 6.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        Ic(Lucide.File, 20.dp, p.ink2)
        Column(Modifier.weight(1f)) {
            if (nameHead.isNotEmpty() && nameTail.isNotEmpty()) {
                val measurer = rememberTextMeasurer()
                BoxWithConstraints(Modifier.fillMaxWidth()) {
                    val maxWidthPx = constraints.maxWidth
                    val shown = remember(name, nameHead, nameTail, nameStyle, maxWidthPx) {
                        fitMiddleEllipsizedName(name, nameHead, nameTail, nameStyle, maxWidthPx, 1, measurer)
                    }
                    Text(
                        shown,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                        style = nameStyle,
                        color = p.ink,
                        modifier = Modifier.semantics { contentDescription = name },
                    )
                }
            } else {
                Text(
                    name,
                    maxLines = 1,
                    overflow = TextOverflow.MiddleEllipsis,
                    style = nameStyle,
                    color = p.ink,
                    modifier = Modifier.semantics { contentDescription = name },
                )
            }
            val meta = when {
                sizeText.isNotEmpty() && statusText.isNotEmpty() && state != AttachmentItemState.ATTACHMENT_ITEM_STATE_READY ->
                    "$sizeText · $statusText"
                sizeText.isNotEmpty() -> sizeText
                statusText.isNotEmpty() && state != AttachmentItemState.ATTACHMENT_ITEM_STATE_READY -> statusText
                else -> ""
            }
            if (meta.isNotEmpty()) {
                Text(meta, style = T.s13, color = if (isFailed) p.red else p.ink2)
            }
            if (isUploading) {
                Box(
                    Modifier
                        .padding(top = 5.dp)
                        .fillMaxWidth()
                        .height(3.dp)
                        .clip(Radii.Pill)
                        .background(p.fill2),
                ) {
                    Box(
                        Modifier
                            .fillMaxWidth(progress.coerceIn(0f, 1f))
                            .height(3.dp)
                            .clip(Radii.Pill)
                            .background(p.ink),
                    )
                }
            }
        }
        if (onPause != null) {
            Box(
                Modifier
                    .heightIn(min = 44.dp)
                    .clickable(role = Role.Button, onClick = onPause)
                    .padding(horizontal = 4.dp),
                contentAlignment = Alignment.Center,
            ) {
                Text(copy(UiMessages.schedulePause()), style = T.s15b, color = p.ink2)
            }
        }
        if (onResume != null) {
            Box(
                Modifier
                    .heightIn(min = 44.dp)
                    .clickable(role = Role.Button, onClick = onResume)
                    .padding(horizontal = 4.dp),
                contentAlignment = Alignment.Center,
            ) {
                Text(copy(UiMessages.scheduleResume()), style = T.s15b, color = p.ink)
            }
        }
        if (isFailed && onRetry != null) {
            Box(
                Modifier
                    .heightIn(min = 44.dp)
                    .clickable(role = Role.Button, onClick = onRetry)
                    .padding(horizontal = 4.dp),
                contentAlignment = Alignment.Center,
            ) {
                Text(copy(UiMessages.commonRetry()), style = T.s15b, color = p.ink)
            }
        }
        if (onRemove != null) {
            val a11y = removeContentDescription.ifEmpty { copy(UiMessages.attachmentRemoveFile(name = name)) }
            Box(
                Modifier
                    .size(44.dp)
                    .clip(CircleShape)
                    .clickable(role = Role.Button, onClick = onRemove)
                    .semantics { contentDescription = a11y },
                contentAlignment = Alignment.Center,
            ) {
                Ic(Lucide.X, 18.dp, p.ink2)
            }
        }
    }
}

@Composable
internal fun FileChips(files: List<SessionFileRefView>) {
    if (files.isNotEmpty()) Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
        files.forEach { f ->
            FileChip(
                name = f.displayName.ifEmpty { f.name },
                sizeText = if (f.hasSizeText()) uiText(f.sizeText) else "",
                nameHead = f.nameHead,
                nameTail = f.nameTail,
            )
        }
    }
}

/** Non-video draft files in the composer box, rendered purely from Core's [AttachmentDraftView]. */
@Composable
internal fun FileStrip(
    draft: AttachmentDraftView,
    scope: DraftScope,
    remote: Remote,
) {
    val copy = uiText()
    if (draft.hasPendingPicks) {
        Text(
            copy(UiMessages.attachmentAndroidReadingFiles()),
            style = T.s13,
            color = pal.ink2,
            modifier = Modifier.padding(horizontal = 8.dp, vertical = 4.dp),
        )
    }
    val files = draft.filesList.filterNot { it.isVideo }
    val blocked = if (draft.hasSendBlockedReason() && draft.filesCount > 0) {
        uiText(draft.sendBlockedReason).ifEmpty { null }
    } else {
        null
    }
    if (files.isEmpty() && blocked == null) return
    val metadataNotice = if (draft.hasFileMetadataNotice()) uiText(draft.fileMetadataNotice) else ""
    Column(Modifier.padding(bottom = 6.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
        blocked?.let {
            Text(it, style = T.s13, color = pal.red, modifier = Modifier.padding(horizontal = 8.dp))
        }
        files.forEach { item ->
            FileChip(
                name = item.displayName,
                sizeText = if (item.hasSizeText()) uiText(item.sizeText) else "",
                state = item.state,
                progress = item.progress.toFloat(),
                statusText = if (item.hasStatusText()) uiText(item.statusText) else "",
                onPause = if (item.canPause) ({ remote.pauseAttachmentUpload(scope, item.itemId) }) else null,
                onResume = if (item.canResume) ({ remote.resumeAttachmentUpload(scope, item.itemId) }) else null,
                onRetry = if (item.canRetry) ({ remote.retryDraftItem(scope, item.itemId) }) else null,
                onRemove = if (item.canRemove) ({ remote.removeDraftItem(scope, item.itemId) }) else null,
                removeContentDescription = if (item.hasRemoveA11Y()) uiText(item.removeA11Y) else "",
                nameHead = item.nameHead,
                nameTail = item.nameTail,
            )
        }
        if (files.isNotEmpty() && metadataNotice.isNotEmpty()) {
            Text(metadataNotice, style = T.s13, color = pal.ink2, modifier = Modifier.padding(horizontal = 8.dp))
        }
    }
}
