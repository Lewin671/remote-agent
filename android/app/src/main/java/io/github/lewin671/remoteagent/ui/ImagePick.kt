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
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
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
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.coreTextToMessageText
import io.github.lewin671.remoteagent.core.proto.AttachmentCandidateInput
import io.github.lewin671.remoteagent.core.proto.AttachmentEvalScreenRequest
import io.github.lewin671.remoteagent.core.proto.ImageProblemView
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

/**
 * The library (照片和视频): delegates capacity, eligibility, and candidate routing to Core
 * (`Screen.attachment_eval`) on user action / pick result (never during row render).
 */
@Composable
internal fun rememberImagePicker(
    draft: ImageDraft,
    remote: Remote,
    machineId: String?,
    caps: AttachmentCaps,
    sessionId: String = "",
): () -> Unit {
    val copy = uiText()
    val context = LocalContext.current
    val contractCap = maxOf(2, caps.maxImages + if (caps.galleryAllowsVideo) caps.maxFiles else 0)
    val take = { uris: List<android.net.Uri> ->
        draft.addPickedCandidates(
            context = context,
            uris = uris,
            remote = remote,
            machineId = machineId,
            sessionId = sessionId,
            caps = caps,
            forceFile = false,
        )
    }
    val many = rememberLauncherForActivityResult(ActivityResultContracts.PickMultipleVisualMedia(contractCap)) { uris ->
        if (uris.isNotEmpty()) take(uris)
    }
    val one = rememberLauncherForActivityResult(ActivityResultContracts.PickVisualMedia()) { uri ->
        if (uri != null) take(listOf(uri))
    }
    return {
        if (!draft.restoring) {
            val eval = remote.evaluateAttachments(
                AttachmentEvalScreenRequest.newBuilder().apply {
                    if (!machineId.isNullOrEmpty()) setMachineId(machineId)
                    if (sessionId.isNotEmpty()) setSessionId(sessionId)
                    setCurrentImages(draft.pictures.size)
                    setCurrentFiles(draft.documents.size)
                }.build(),
            )
            if (!eval.canPickGallery) {
                val reasonText = when {
                    eval.hasGalleryBlockedReason() -> eval.galleryBlockedReason
                    eval.hasImagesPickBlockedReason() -> eval.imagesPickBlockedReason
                    else -> null
                }
                val reason = reasonText?.let { coreTextToMessageText(it).resolve(copy) }.orEmpty()
                if (reason.isNotEmpty()) ImageNotices.show(draft.key, reason)
            } else {
                val type = when {
                    !eval.canPickImages -> ActivityResultContracts.PickVisualMedia.VideoOnly
                    eval.galleryAllowsVideo && eval.canPickFiles -> ActivityResultContracts.PickVisualMedia.ImageAndVideo
                    else -> ActivityResultContracts.PickVisualMedia.ImageOnly
                }
                val req = PickVisualMediaRequest(type)
                if (eval.libraryRoom == 1) one.launch(req) else many.launch(req)
            }
        }
    }
}

/** A one-line reason shown by the strip, e.g. why the plus does nothing. */
object ImageNotices {
    val text = androidx.compose.runtime.mutableStateMapOf<String, String>()
    fun show(key: String, s: String) {
        text[key] = s
    }
}

internal data class EvaluatedDocumentMeta(
    val isVideo: Boolean,
    val formattedSize: String,
)

/**
 * Evaluates all draft documents once when the document list or verified sizes change (never per row),
 * returning Core's `isVideo` and `formattedSize` per document ID.
 */
@Composable
internal fun rememberEvaluatedDocuments(
    remote: Remote,
    machineId: String?,
    documents: List<PendingImage>,
): Map<String, EvaluatedDocumentMeta> {
    val key = documents.map { Triple(it.id, it.fileName.orEmpty(), it.fileBytes) }
    return remember(remote, machineId, key) {
        if (documents.isEmpty()) return@remember emptyMap()
        val req = AttachmentEvalScreenRequest.newBuilder().apply {
            if (!machineId.isNullOrEmpty()) setMachineId(machineId)
            for (doc in documents) {
                addCandidates(
                    AttachmentCandidateInput.newBuilder()
                        .setName(doc.fileName.orEmpty())
                        .setSizeBytes(doc.fileBytes)
                        .setForceFile(true),
                )
            }
        }.build()
        val decisions = remote.evaluateAttachments(req).decisionsList
        buildMap(documents.size) {
            for (i in documents.indices) {
                val doc = documents[i]
                val d = decisions.getOrNull(i)
                put(
                    doc.id,
                    EvaluatedDocumentMeta(
                        isVideo = doc.isVideo || (d?.isVideo == true),
                        formattedSize = d?.formattedSize?.takeIf { it.isNotEmpty() } ?: doc.formattedSize,
                    ),
                )
            }
        }
    }
}

/**
 * Evaluates current draft attachments once when the draft contents or machine capabilities change,
 * returning Core's localized block reason (or null when all current attachments are allowed).
 */
@Composable
internal fun rememberDraftAttachmentsBlocked(
    remote: Remote,
    machineId: String?,
    sessionId: String = "",
    documents: List<PendingImage>,
    pictures: List<PendingImage>,
    caps: AttachmentCaps,
): String? {
    val copy = uiText()
    val docKey = documents.map { Triple(it.id, it.fileName.orEmpty(), it.fileBytes) }
    val picKey = pictures.size
    return remember(remote, machineId, sessionId, docKey, picKey, caps) {
        if (documents.isEmpty() && pictures.isEmpty()) null
        else evaluateDraftAttachmentsBlocked(remote, machineId, sessionId, documents, pictures, copy = copy)
    }
}

/**
 * The picked images and videos above the text, in one row: each removable,
 * a spinner or progress while it goes to the computer, a retry when it
 * failed; a video has its length in a corner. 原图 for the whole message
 * and the count when there are images. Sending waits until all are on the
 * computer.
 */
@Composable
internal fun ImageStrip(
    draft: ImageDraft,
    remote: Remote,
    machineId: String?,
    max: Int,
    caps: AttachmentCaps? = null,
    sessionId: String = "",
) {
    val copy = uiText()
    val p = pal
    val context = LocalContext.current
    var viewing by remember(draft.key) { mutableStateOf<Int?>(null) }
    val notice = ImageNotices.text[draft.key]
    LaunchedEffect(notice) {
        if (notice != null) {
            kotlinx.coroutines.delay(4000)
            ImageNotices.text.remove(draft.key)
        }
    }
    var showRestoring by remember(draft.key) { mutableStateOf(false) }
    LaunchedEffect(draft.key, draft.restoring) {
        if (draft.restoring) {
            kotlinx.coroutines.delay(250)
            showRestoring = true
        } else {
            showRestoring = false
        }
    }
    if (draft.restoring) {
        if (showRestoring) {
            Text(copy(UiMessages.imageAndroidRestoring()), Modifier.padding(horizontal = 12.dp, vertical = 4.dp), style = T.s13, color = p.ink2)
        }
        return
    }
    val effectiveCaps = caps ?: remember(remote, machineId, sessionId) { remote.attachmentCapsFor(machineId, sessionId) }
    val docMetaById = rememberEvaluatedDocuments(remote, machineId, draft.documents)

    val failedPictures = draft.pictures.mapNotNull { img ->
        (img.state as? PendingImage.State.Failed)?.failure?.input?.let { img.id to it }
    }
    val problemEval = remember(remote, failedPictures) {
        evaluateImageProblems(remote, failedPictures.map { it.second })
    }
    val problemsById: Map<String, ImageProblemView> = remember(failedPictures, problemEval) {
        buildMap(failedPictures.size) {
            for (i in failedPictures.indices) {
                problemEval.problemsList.getOrNull(i)?.let { put(failedPictures[i].first, it) }
            }
        }
    }

    val thumbs = draft.images.filter { !it.isFile || docMetaById[it.id]?.isVideo == true }
    if (thumbs.isEmpty()) {
        val line = notice ?: draft.notice.takeIf { it.isNotEmpty() }
        if (line != null) Text(line, Modifier.padding(horizontal = 12.dp, vertical = 4.dp), style = T.s13, color = p.orangeInk)
    } else {
        Column(Modifier.fillMaxWidth().padding(bottom = 6.dp)) {
        Row(Modifier.horizontalScroll(rememberScrollState()).padding(bottom = 6.dp)) {
            for (img in thumbs) {
                val index = draft.pictures.indexOf(img)
                val problem = problemsById[img.id]
                // Reserve the upper/right half of the corner-centred remove target.
                Box(Modifier.width(86.dp).height(86.dp)) {
                    Box(
                        Modifier.align(Alignment.BottomStart).size(64.dp).clip(Radii.R14).background(p.fill)
                            .clickable(role = Role.Button) {
                                if (!img.isFile) viewing = index
                                else if (img.state is PendingImage.State.Failed) machineId?.let { draft.upload(remote, img, it, forceFresh = true, caps = caps) }
                            }
                            .semantics {
                                if (!img.isFile) {
                                    contentDescription = copy(UiMessages.imagePosition(count = draft.pictures.size, index = index + 1))
                                    stateDescription = imageStateWords(img, problem, copy = copy)
                                }
                            },
                    ) {
                        if (img.isFile) VideoFrame(img)
                        else img.thumb?.let { Image(it, null, Modifier.fillMaxSize(), contentScale = ContentScale.Crop) }
                        when (val st = img.state) {
                            PendingImage.State.Processing -> CircularProgressIndicator(Modifier.align(Alignment.Center).size(24.dp), color = Color.White, strokeWidth = 3.dp, trackColor = Color(0x59FFFFFF))
                            is PendingImage.State.Uploading -> {
                                CircularProgressIndicator(Modifier.align(Alignment.Center).size(24.dp), color = Color.White, strokeWidth = 3.dp, trackColor = Color(0x59FFFFFF))
                                if (draft.original || img.isFile) Box(Modifier.align(Alignment.BottomCenter).fillMaxWidth().height(4.dp).background(Color(0x59000000))) {
                                    Box(Modifier.fillMaxWidth(st.progress).height(4.dp).background(Color.White))
                                }
                            }
                            is PendingImage.State.Failed -> Box(
                                Modifier.fillMaxSize().background(Color(0x99000000)),
                                contentAlignment = Alignment.BottomCenter,
                            ) {
                                Text(
                                    if (img.isFile) uiText(UiMessages.commonRetry()) else copy(UiMessages.attachmentFailed()),
                                    color = Color.White,
                                    style = T.s12b,
                                    modifier = Modifier.padding(bottom = 4.dp).clearAndSetSemantics {},
                                )
                            }
                            PendingImage.State.Ready -> {}
                        }
                    }
                    // Compose otherwise expands a 44dp clickable to 48dp on Android.
                    val viewConfiguration = LocalViewConfiguration.current
                    CompositionLocalProvider(
                        LocalViewConfiguration provides remember(viewConfiguration) {
                            object : ViewConfiguration by viewConfiguration {
                                override val minimumTouchTargetSize = DpSize(44.dp, 44.dp)
                            }
                        },
                    ) {
                        Box(
                            Modifier.align(Alignment.TopStart).offset(x = 42.dp).size(44.dp)
                                .clickable(role = Role.Button) { draft.remove(img) }
                                .semantics {
                                    contentDescription = if (img.isFile) {
                                        copy(UiMessages.attachmentRemoveVideo(name = img.fileName.toString()))
                                    } else {
                                        copy(UiMessages.imageRemoveIndex(index = index + 1))
                                    }
                                },
                            contentAlignment = Alignment.Center,
                        ) {
                            // Keep the mark at its existing inset inside the picture.
                            Box(Modifier.offset(x = (-11).dp, y = 9.dp).size(18.dp).clip(CircleShape).background(Color(0x99000000)), contentAlignment = Alignment.Center) {
                                Ic(Lucide.X, 12.dp, Color.White, contentDescription = null)
                            }
                        }
                    }
                }
            }
        }
        if (draft.pictures.isNotEmpty()) Row(Modifier.fillMaxWidth().padding(horizontal = 4.dp), verticalAlignment = Alignment.CenterVertically) {
            Row(
                Modifier.heightIn(min = 44.dp).clickable {
                    draft.setOriginal(context, !draft.original, remote, effectiveCaps.originalImageMaxBytes) { machineId }
                },
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Box(Modifier.size(16.dp).clip(CircleShape).border(if (draft.original) 5.dp else 1.5.dp, if (draft.original) p.blue else p.ink3, CircleShape))
                Text(
                    copy(UiMessages.imageAndroidOriginalMeta()) + if (draft.original) copy(UiMessages.imageAndroidTotalSize(size = "%.1f".format(draft.totalBytes / 1024.0 / 1024.0))) else "",
                    style = T.s13,
                    color = p.ink,
                )
            }
            Spacer(Modifier.weight(1f))
            Text("${draft.pictures.size} / $max", style = T.s13, color = p.ink2)
        }
        val summaryLine = if (problemEval.hasFailureSummary()) uiText(problemEval.failureSummary).ifEmpty { null } else null
        val notes = draft.pictures.mapNotNull { it.processed?.note?.takeIf { n -> n.isNotEmpty() } }.distinct()
        val line = summaryLine ?: notice ?: draft.notice.takeIf { it.isNotEmpty() }
        if (line != null) Text(line, style = T.s13, color = p.orangeInk, modifier = Modifier.padding(horizontal = 8.dp, vertical = 2.dp))
        if (draft.original && notes.isNotEmpty()) Text(notes.joinToString("；"), style = T.s13, color = p.ink2, modifier = Modifier.padding(horizontal = 8.dp, vertical = 2.dp))
        if (draft.original && draft.pictures.isNotEmpty()) {
            val hint = effectiveCaps.originalImageHint?.let { uiText(it) }.orEmpty()
            if (hint.isNotEmpty()) Text(hint, style = T.s13, color = p.ink2, modifier = Modifier.padding(horizontal = 8.dp, vertical = 2.dp))
        }
        }
    }
    FileStrip(draft, remote, machineId, effectiveCaps, docMetaById, sessionId = sessionId)
    viewing?.let {
        DraftImageViewer(
            draft = draft,
            remote = remote,
            machineId = machineId,
            start = it,
            originalMaxBytes = effectiveCaps.originalImageMaxBytes,
        ) { viewing = null }
    }
}

/** A video's first frame with its length in a corner; when neither can be read, the file icon and its name. */
@Composable
private fun BoxScope.VideoFrame(img: PendingImage) {
    val context = LocalContext.current
    val frame by produceState(VideoFrames.cache[img.id], img.id, img.uri) {
        if (value == null && img.uri != null) value = withContext(Dispatchers.IO) { VideoFrames.read(context, img) }
    }
    val f = frame
    if (f?.image != null) Image(f.image, null, Modifier.fillMaxSize(), contentScale = ContentScale.Crop)
    else Column(Modifier.fillMaxSize().padding(6.dp), verticalArrangement = Arrangement.Center, horizontalAlignment = Alignment.CenterHorizontally) {
        Ic(Lucide.File, 18.dp, pal.ink2)
        Text(img.fileName.orEmpty(), style = T.s10, color = pal.ink2, maxLines = 2, overflow = TextOverflow.MiddleEllipsis)
    }
    if (f?.lengthMs != null) Text(
        io.github.lewin671.remoteagent.core.videoLength(f.lengthMs),
        style = T.s10.copy(fontWeight = T.s12b.fontWeight),
        color = Color.White,
        modifier = Modifier.align(Alignment.BottomStart).padding(4.dp).clip(Radii.R6).background(Color(0x8C000000)).padding(horizontal = 4.dp, vertical = 1.dp),
    )
}

/** First frames and lengths of picked videos, read once each for this run. */
private object VideoFrames {
    class Frame(val image: androidx.compose.ui.graphics.ImageBitmap?, val lengthMs: Long?)

    val cache = java.util.concurrent.ConcurrentHashMap<String, Frame>()

    fun read(context: android.content.Context, img: PendingImage): Frame {
        cache[img.id]?.let { return it }
        val r = android.media.MediaMetadataRetriever()
        val frame = try {
            r.setDataSource(context, android.net.Uri.parse(img.uri))
            val length = r.extractMetadata(android.media.MediaMetadataRetriever.METADATA_KEY_DURATION)?.toLongOrNull()
            val bitmap = runCatching { r.getFrameAtTime(0, android.media.MediaMetadataRetriever.OPTION_CLOSEST_SYNC) }.getOrNull()?.let { b ->
                val scale = 192f / maxOf(b.width, b.height)
                if (scale >= 1f) b else android.graphics.Bitmap.createScaledBitmap(b, (b.width * scale).toInt().coerceAtLeast(1), (b.height * scale).toInt().coerceAtLeast(1), true).also { b.recycle() }
            }
            Frame(bitmap?.asImageBitmap(), length)
        } catch (e: Exception) {
            Frame(null, null)
        } finally {
            runCatching { r.release() }
        }
        cache[img.id] = frame
        return frame
    }
}

/** Upload again when an unused image expires, including after the page returns from the background. */
@Composable
internal fun RefreshDraftUploads(
    draft: ImageDraft,
    remote: Remote,
    machineId: String?,
    blocked: String?,
    caps: AttachmentCaps,
    sessionId: String = "",
) {
    draft.bindRemote(remote)
    val draftBlocked = rememberDraftAttachmentsBlocked(remote, machineId, sessionId, draft.documents, draft.pictures, caps)
    val deadlines = draft.images.map { it.expiresMs }
    LaunchedEffect(draft, machineId, blocked, draftBlocked, caps, deadlines, draft.images.size, draft.restoring) {
        if (!draft.restoring && machineId != null) draft.retarget(remote, machineId)
        if (draft.restoring || machineId == null || !caps.machineOnline) return@LaunchedEffect
        if (blocked != null && !caps.supportsFiles) return@LaunchedEffect
        draft.sendAllTo(remote, machineId, caps, sessionId)
        val next = draft.images.filter { it.readyFor(machineId) }.minOfOrNull { it.expiresMs }
        if (next != null) {
            kotlinx.coroutines.delay((next - System.currentTimeMillis()).coerceAtLeast(1))
            draft.sendAllTo(remote, machineId, caps, sessionId)
        }
    }
}
