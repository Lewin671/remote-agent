package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.AttachmentDraftItemView
import io.github.lewin671.remoteagent.core.proto.AttachmentDraftView
import io.github.lewin671.remoteagent.core.proto.AttachmentItemState
import io.github.lewin671.remoteagent.core.proto.DraftScope

/**
 * Full-screen preview for draft images, driven strictly by Core's [DraftImageViewerView].
 * Position text (`position_text`), status/error messages, and retry/pause/resume/remove eligibility
 * all come directly from Core.
 */
@Composable
internal fun DraftImageViewer(
    draft: AttachmentDraftView,
    scope: DraftScope,
    remote: Remote,
    onClose: () -> Unit = { remote.closeDraftImageViewer(scope) },
) {
    val copy = uiText()
    val viewer = draft.imageViewer
    val images = draft.imagesList
    Dialog(onDismissRequest = onClose, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        PagedImageViewer(
            count = images.size,
            start = viewer.currentIndex.coerceIn(0, (images.size - 1).coerceAtLeast(0)),
            onClose = onClose,
            syncedPage = viewer.currentIndex,
            onPageSettled = { settled ->
                if (settled != viewer.currentIndex) {
                    remote.navigateDraftImageViewer(scope, setIndex = true, index = settled)
                }
            },
            positionLabel = { idx ->
                val item = images.getOrNull(idx)
                when {
                    item != null && item.hasPositionText() -> uiText(item.positionText)
                    viewer.hasPositionText() -> uiText(viewer.positionText)
                    else -> ""
                }
            },
            empty = if (viewer.hasEmptyText()) uiText(viewer.emptyText) else uiText(UiMessages.imageAndroidEmpty()),
            picture = { index -> key(images[index].itemId) { DraftViewerPage(images[index]) } },
            info = { index ->
                val img = images[index]
                val header = if (img.hasPositionA11Y()) uiText(img.positionA11Y)
                else if (img.hasPositionText()) uiText(img.positionText)
                else ""
                if (header.isNotEmpty()) {
                    Text(header, color = Color.White, style = T.s15b)
                }
                val status = when {
                    img.hasProblem() && img.problem.hasMessage() -> uiText(img.problem.message)
                    img.hasStatusText() -> uiText(img.statusText)
                    else -> ""
                }
                if (status.isNotEmpty()) {
                    Text(status, color = Color.White, style = T.s15)
                }
                Row {
                    if (img.canRetry) {
                        TextButton(onClick = { remote.retryDraftItem(scope, img.itemId) }) {
                            Text(copy(UiMessages.commonRetry()), color = Color(0xFF9EC1FF))
                        }
                    }
                    if (img.canPause) {
                        TextButton(onClick = { remote.pauseAttachmentUpload(scope, img.itemId) }) {
                            Text(copy(UiMessages.schedulePause()), color = Color(0xFF9EC1FF))
                        }
                    }
                    if (img.canResume) {
                        TextButton(onClick = { remote.resumeAttachmentUpload(scope, img.itemId) }) {
                            Text(copy(UiMessages.scheduleResume()), color = Color(0xFF9EC1FF))
                        }
                    }
                    if (img.canRemove) {
                        TextButton(onClick = { remote.removeDraftItem(scope, img.itemId) }) {
                            Text(copy(UiMessages.commonRemove()), color = Color(0xFF9EC1FF))
                        }
                    }
                }
            },
        )
    }
}

@Composable
private fun DraftViewerPage(image: AttachmentDraftItemView) {
    val copy = uiText()
    val app = remember { RaApp.instanceOrNull() }
    val bitmap by produceState<ImageBitmap?>(null, image.preparedRef, image.sourceRef, image.thumbRef) {
        if (app == null) return@produceState
        if (image.thumbRef.isNotEmpty()) {
            value = app.attachments.loadThumbnail(image.thumbRef, image.preparedRef)
        }
        val full = app.attachments.loadViewerImage(image.preparedRef, image.sourceRef)
        if (full != null) value = full
    }
    Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
        bitmap?.let { ViewerBitmap(it, copy(UiMessages.imageDraft())) }
        if (image.state == AttachmentItemState.ATTACHMENT_ITEM_STATE_PREPARING && bitmap == null) {
            Text(copy(UiMessages.imagePreparing()), color = Color.White)
        }
    }
}
