package io.github.lewin671.remoteagent.ui

import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.*
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
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.core.proto.AttachmentEvalScreenRequest
import io.github.lewin671.remoteagent.core.proto.SessionFileRefView

@Composable
internal fun rememberDocumentPicker(
    draft: ImageDraft,
    remote: Remote,
    machineId: String?,
    caps: AttachmentCaps,
    sessionId: String = "",
): (() -> Unit)? {
    val copy = uiText()
    val context = LocalContext.current
    val picker = rememberLauncherForActivityResult(ActivityResultContracts.OpenMultipleDocuments()) { uris ->
        if (uris.isNotEmpty()) {
            draft.addPickedCandidates(
                context = context,
                uris = uris,
                remote = remote,
                machineId = machineId,
                sessionId = sessionId,
                caps = caps,
                forceFile = true,
            )
        }
    }
    if (!caps.supportsFiles) return null
    return {
        if (!draft.restoring && !draft.readingFiles) {
            val eval = remote.evaluateAttachments(
                AttachmentEvalScreenRequest.newBuilder().apply {
                    if (!machineId.isNullOrEmpty()) setMachineId(machineId)
                    if (sessionId.isNotEmpty()) setSessionId(sessionId)
                    setCurrentImages(draft.pictures.size)
                    setCurrentFiles(draft.documents.size)
                }.build(),
            )
            if (!eval.canPickFiles) {
                val reason = if (eval.hasFilesPickBlockedReason()) {
                    coreTextToMessageText(eval.filesPickBlockedReason).resolve(copy)
                } else {
                    ""
                }
                if (reason.isNotEmpty()) ImageNotices.show(draft.key, reason)
            } else {
                picker.launch(arrayOf("*/*"))
            }
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
internal fun FileChip(
    name: String,
    sizeText: String,
    state: PendingImage.State? = null,
    onRetry: (() -> Unit)? = null,
    onRemove: (() -> Unit)? = null,
    nameHead: String = "",
    nameTail: String = "",
) {
    val copy = uiText()
    val p = pal
    // One line is a capsule; once a reason or a large font makes it taller, a card's corners (design-system §5).
    val density = androidx.compose.ui.platform.LocalDensity.current
    var tall by remember { mutableStateOf(false) }
    val nameStyle = T.s15.copy(fontWeight = platformWeight(androidx.compose.ui.text.font.FontWeight.Medium))
    Row(
        Modifier.fillMaxWidth().heightIn(min = 52.dp).onSizeChanged { tall = with(density) { it.height.toDp() } > 60.dp }
            .clip(if (tall) Radii.R20 else Radii.Pill).background(p.fill)
            .padding(start = 14.dp, end = if (onRemove != null || onRetry != null) 6.dp else 14.dp, top = 6.dp, bottom = 6.dp),
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
                    Text(shown, maxLines = 1, overflow = TextOverflow.Ellipsis, style = nameStyle, color = p.ink, modifier = Modifier.semantics { contentDescription = name })
                }
            } else {
                Text(name, maxLines = 1, overflow = TextOverflow.MiddleEllipsis, style = nameStyle, color = p.ink, modifier = Modifier.semantics { contentDescription = name })
            }
            val status = when (state) {
                PendingImage.State.Processing -> copy(UiMessages.attachmentAndroidPreparingMeta())
                is PendingImage.State.Uploading -> copy(UiMessages.attachmentAndroidUploadMeta(percent = ((state.progress * 100).toInt()).toString()))
                is PendingImage.State.Failed -> copy(UiMessages.attachmentAndroidFailedMeta())
                else -> ""
            }
            Text(sizeText + status, style = T.s13, color = if (state is PendingImage.State.Failed) p.red else p.ink2)
            if (state is PendingImage.State.Uploading) Box(Modifier.padding(top = 5.dp).fillMaxWidth().height(3.dp).clip(Radii.Pill).background(p.fill2)) {
                Box(Modifier.fillMaxWidth(state.progress).height(3.dp).clip(Radii.Pill).background(p.ink))
            }
            if (state is PendingImage.State.Failed) Text(uiText(state.error), style = T.s13, color = p.red)
        }
        if (state is PendingImage.State.Failed && onRetry != null) Box(Modifier.heightIn(min = 44.dp).clickable(role = androidx.compose.ui.semantics.Role.Button, onClick = onRetry).padding(horizontal = 4.dp), contentAlignment = Alignment.Center) {
            Text(copy(UiMessages.commonRetry()), style = T.s15b, color = p.ink)
        }
        if (onRemove != null) Box(Modifier.size(44.dp).clip(CircleShape).clickable(role = androidx.compose.ui.semantics.Role.Button, onClick = onRemove).semantics { contentDescription = copy(UiMessages.attachmentRemoveFile(name = name)) }, contentAlignment = Alignment.Center) {
            Ic(Lucide.X, 18.dp, p.ink2)
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

/** The files in the box, apart from videos from the library, which sit among the images ([ImageStrip]). */
@Composable
internal fun FileStrip(
    draft: ImageDraft,
    remote: Remote,
    machineId: String?,
    caps: AttachmentCaps? = null,
    docMetaById: Map<String, EvaluatedDocumentMeta>? = null,
    sessionId: String = "",
) {
    val copy = uiText()
    if (draft.readingFiles) Text(copy(UiMessages.attachmentAndroidReadingFiles()), style = T.s13, color = pal.ink2, modifier = Modifier.padding(horizontal = 8.dp, vertical = 4.dp))
    val metaById = docMetaById ?: rememberEvaluatedDocuments(remote, machineId, draft.documents)
    val files = draft.documents.filterNot { metaById[it.id]?.isVideo == true }
    val effectiveCaps = caps ?: remember(remote, machineId, sessionId) { remote.attachmentCapsFor(machineId, sessionId) }
    val checkedPictures = if (machineId.isNullOrEmpty()) emptyList() else draft.pictures
    val blocked = rememberDraftAttachmentsBlocked(remote, machineId, sessionId, draft.documents, checkedPictures, effectiveCaps)
    if (files.isEmpty() && blocked == null) return
    val metadataNotice = effectiveCaps.fileMetadataNotice?.let { uiText(it) }.orEmpty()
    Column(Modifier.padding(bottom = 6.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
        files.forEach { file ->
            FileChip(
                name = file.fileName!!,
                sizeText = metaById[file.id]?.formattedSize ?: file.formattedSize,
                state = file.state,
                onRetry = { machineId?.let { draft.upload(remote, file, it, forceFresh = true, caps = effectiveCaps) } },
                onRemove = { draft.remove(file) },
            )
        }
        blocked?.let { Text(it, style = T.s13, color = pal.red, modifier = Modifier.padding(horizontal = 8.dp)) }
        if (files.isNotEmpty() && metadataNotice.isNotEmpty()) Text(metadataNotice, style = T.s13, color = pal.ink2, modifier = Modifier.padding(horizontal = 8.dp))
    }
}
