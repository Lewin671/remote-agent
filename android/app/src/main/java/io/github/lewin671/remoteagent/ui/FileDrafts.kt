package io.github.lewin671.remoteagent.ui

import android.content.Context
import android.content.Intent
import android.net.Uri
import android.provider.OpenableColumns
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.core.proto.AttachmentCandidateInput
import io.github.lewin671.remoteagent.core.proto.AttachmentCandidateRoute
import io.github.lewin671.remoteagent.core.proto.AttachmentEvalScreenRequest
import io.github.lewin671.remoteagent.core.proto.AttachmentEvalViewModel
import io.github.lewin671.remoteagent.core.proto.ImageProblemInput
import io.github.lewin671.remoteagent.core.proto.ImageProblemKind
import io.github.lewin671.remoteagent.core.proto.NewSessionViewModel
import io.github.lewin671.remoteagent.core.proto.SessionHeaderView
import io.github.lewin671.remoteagent.core.proto.Text as CoreText
import java.io.InputStream
import kotlinx.coroutines.*

/** Attachment capabilities projected by Core for the current session or new-session target. */
data class AttachmentCaps(
    val supportsImages: Boolean,
    val supportsFiles: Boolean,
    val maxImages: Int,
    val maxFiles: Int,
    val maxFileBytes: Long,
    val maxFileNameBytes: Int,
    val maxFileSizeText: String,
    val originalImageMaxBytes: Long,
    val galleryAllowsVideo: Boolean,
    val galleryAcceptMime: String,
    val capabilitiesLoaded: Boolean,
    val imageUploadBlockedReason: CoreText?,
    val fileUploadBlockedReason: CoreText?,
    val originalImageHint: CoreText?,
    val fileMetadataNotice: CoreText?,
) {
    val canUploadImages: Boolean get() = supportsImages && imageUploadBlockedReason == null
    val canUploadFiles: Boolean get() = supportsFiles && fileUploadBlockedReason == null
    val canUploadAnything: Boolean get() = canUploadImages || canUploadFiles
}

/** Core `AttachmentEval` verdict for whether the composer `+` button is available and why it is blocked. */
internal data class AttachmentPickState(
    val canPickGallery: Boolean = false,
    val canPickFiles: Boolean = false,
    val blockedReason: CoreText? = null,
) {
    val canPickAny: Boolean get() = canPickGallery || canPickFiles
}

internal fun AttachmentEvalViewModel.toPickState(): AttachmentPickState {
    val reason = when {
        canPickGallery || canPickFiles -> null
        hasGalleryBlockedReason() -> galleryBlockedReason
        hasFilesPickBlockedReason() -> filesPickBlockedReason
        hasImagesPickBlockedReason() -> imagesPickBlockedReason
        hasImageUploadBlockedReason() -> imageUploadBlockedReason
        hasFileUploadBlockedReason() -> fileUploadBlockedReason
        else -> null
    }
    return AttachmentPickState(
        canPickGallery = canPickGallery,
        canPickFiles = canPickFiles,
        blockedReason = reason,
    )
}

internal fun SessionHeaderView.attachmentCaps(): AttachmentCaps {
    val caps = capabilities
    return AttachmentCaps(
        supportsImages = caps.supportsImages,
        supportsFiles = caps.supportsFiles,
        maxImages = caps.maxImages,
        maxFiles = caps.maxFiles,
        maxFileBytes = caps.maxFileBytes,
        maxFileNameBytes = caps.maxFileNameBytes,
        maxFileSizeText = caps.maxFileSizeText,
        originalImageMaxBytes = caps.originalImageMaxBytes,
        galleryAllowsVideo = caps.galleryAllowsVideo,
        galleryAcceptMime = caps.galleryAcceptMime,
        capabilitiesLoaded = caps.capabilitiesLoaded,
        imageUploadBlockedReason = if (caps.hasImageUploadBlockedReason()) caps.imageUploadBlockedReason else null,
        fileUploadBlockedReason = if (caps.hasFileUploadBlockedReason()) caps.fileUploadBlockedReason else null,
        originalImageHint = if (caps.hasOriginalImageHint()) caps.originalImageHint else null,
        fileMetadataNotice = if (caps.hasFileMetadataNotice()) caps.fileMetadataNotice else null,
    )
}

internal fun NewSessionViewModel.attachmentCaps(): AttachmentCaps = AttachmentCaps(
    supportsImages = supportsImages,
    supportsFiles = supportsFiles,
    maxImages = maxImages,
    maxFiles = maxFiles,
    maxFileBytes = maxFileBytes,
    maxFileNameBytes = maxFileNameBytes,
    maxFileSizeText = maxFileSizeText,
    originalImageMaxBytes = originalImageMaxBytes,
    galleryAllowsVideo = galleryAllowsVideo,
    galleryAcceptMime = galleryAcceptMime,
    capabilitiesLoaded = capabilitiesLoaded,
    imageUploadBlockedReason = if (hasImageUploadBlockedReason()) imageUploadBlockedReason else null,
    fileUploadBlockedReason = if (hasFileUploadBlockedReason()) fileUploadBlockedReason else null,
    originalImageHint = if (hasOriginalImageHint()) originalImageHint else null,
    fileMetadataNotice = if (hasFileMetadataNotice()) fileMetadataNotice else null,
)

internal fun AttachmentEvalViewModel.toAttachmentCaps(): AttachmentCaps = AttachmentCaps(
    supportsImages = supportsImages,
    supportsFiles = supportsFiles,
    maxImages = maxImages,
    maxFiles = maxFiles,
    maxFileBytes = maxFileBytes,
    maxFileNameBytes = maxFileNameBytes,
    maxFileSizeText = maxFileSizeText,
    originalImageMaxBytes = originalImageMaxBytes,
    galleryAllowsVideo = galleryAllowsVideo,
    galleryAcceptMime = galleryAcceptMime,
    capabilitiesLoaded = capabilitiesLoaded,
    imageUploadBlockedReason = if (hasImageUploadBlockedReason()) imageUploadBlockedReason else null,
    fileUploadBlockedReason = if (hasFileUploadBlockedReason()) fileUploadBlockedReason else null,
    originalImageHint = if (hasOriginalImageHint()) originalImageHint else null,
    fileMetadataNotice = if (hasFileMetadataNotice()) fileMetadataNotice else null,
)

internal fun Remote.attachmentCapsFor(machineId: String?, sessionId: String = ""): AttachmentCaps {
    val req = AttachmentEvalScreenRequest.newBuilder().apply {
        if (!machineId.isNullOrEmpty()) setMachineId(machineId)
        if (sessionId.isNotEmpty()) setSessionId(sessionId)
    }.build()
    return evaluateAttachments(req).toAttachmentCaps()
}

internal data class PickedUriMeta(
    val uri: Uri,
    val name: String,
    val mimeType: String,
    val sizeBytes: Long,
    val sizeUnknown: Boolean,
)

/** Reads display name, MIME type, and provider size hint for one picked URI on IO. */
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
    )
}

/**
 * Evaluates a batch of picked URIs via Core (`Screen.attachment_eval`) once and routes accepted
 * candidates into the draft's pictures or documents.
 *
 * - `forceFile = false`: photo/video gallery, drag-and-drop, paste (images -> IMAGE, non-images -> FILE/VIDEO_FILE).
 * - `forceFile = true`: explicit "文件" picker (every item routes as FILE/VIDEO_FILE unchanged).
 */
internal fun ImageDraft.addPickedCandidates(
    context: Context,
    uris: List<Uri>,
    remote: Remote,
    machineId: String?,
    sessionId: String = "",
    caps: AttachmentCaps? = null,
    forceFile: Boolean = false,
) {
    if (restoring || readingFiles || uris.isEmpty()) return
    bindRemote(remote)
    readingFiles = true
    notice = ""
    RaApp.instance.scope.launch {
        try {
            val metas = withContext(Dispatchers.IO) { uris.map { queryPickedUriMeta(context, it) } }
            val evalReq = AttachmentEvalScreenRequest.newBuilder().apply {
                if (!machineId.isNullOrEmpty()) setMachineId(machineId)
                if (sessionId.isNotEmpty()) setSessionId(sessionId)
                setCurrentImages(pictures.size)
                setCurrentFiles(documents.size)
                for (m in metas) {
                    addCandidates(
                        AttachmentCandidateInput.newBuilder()
                            .setName(m.name)
                            .setMimeType(m.mimeType)
                            .setSizeBytes(m.sizeBytes)
                            .setSizeUnknown(m.sizeUnknown)
                            .setForceFile(forceFile),
                    )
                }
            }.build()
            val eval = remote.evaluateAttachments(evalReq)
            val effective = caps ?: eval.toAttachmentCaps()
            if (AccountMemory.token !== accountToken) return@launch

            val notices = mutableListOf<String>()
            if (eval.hasNotice()) {
                val resolved = resolveCoreText(context, eval.notice)
                if (resolved.isNotEmpty()) notices += resolved
            }

            var permissionTemporary = false
            val acceptedPictures = mutableListOf<Uri>()
            for ((meta, decision) in metas.zip(eval.decisionsList)) {
                if (!decision.accepted) continue
                when (decision.route) {
                    AttachmentCandidateRoute.ATTACHMENT_CANDIDATE_ROUTE_IMAGE -> {
                        acceptedPictures += meta.uri
                    }
                    AttachmentCandidateRoute.ATTACHMENT_CANDIDATE_ROUTE_VIDEO_FILE,
                    AttachmentCandidateRoute.ATTACHMENT_CANDIDATE_ROUTE_FILE -> {
                        val fileName = decision.displayName.ifEmpty { meta.name }
                        val entry = PendingImage(
                            id = newRequestId(),
                            uri = meta.uri.toString(),
                            fileName = fileName,
                            fileBytes = meta.sizeBytes,
                            retainSource = false,
                            isVideo = decision.isVideo,
                            formattedSize = decision.formattedSize,
                        )
                        PendingUris.retain(entry)
                        val kept = runCatching {
                            context.contentResolver.takePersistableUriPermission(meta.uri, Intent.FLAG_GRANT_READ_URI_PERMISSION)
                        }.isSuccess
                        if (!kept) permissionTemporary = true
                        images += entry
                        prepared(entry)
                        machineId?.let { upload(remote, entry, it, caps = effective) }
                    }
                    else -> Unit
                }
            }
            if (acceptedPictures.isNotEmpty()) {
                addAcceptedPictures(context, acceptedPictures, remote, effective.originalImageMaxBytes) { machineId }
            }
            if (permissionTemporary) {
                notices += sessionText(UiMessages.attachmentAndroidPermissionTemporary())
            }
            if (notices.isNotEmpty()) {
                notice = notices.joinToString(" · ")
            }
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            notice = if (e is java.io.IOException || e is SecurityException) sessionText(UiMessages.attachmentAndroidSourceUnreadable()) else displayError(e)
        } finally {
            readingFiles = false
        }
    }
}

/**
 * Evaluates the draft's current attachments against Core (`Screen.attachment_eval`) to detect
 * machine-capability or limit violations (e.g., after capabilities drop or switching computers).
 * Returns Core's localized block reason, or null when all current attachments are accepted.
 */
internal fun evaluateDraftAttachmentsBlocked(
    remote: Remote,
    machineId: String?,
    sessionId: String = "",
    documents: List<PendingImage>,
    pictures: List<PendingImage> = emptyList(),
    copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText,
): String? {
    if (documents.isEmpty() && pictures.isEmpty()) return null
    val req = AttachmentEvalScreenRequest.newBuilder().apply {
        if (!machineId.isNullOrEmpty()) setMachineId(machineId)
        if (sessionId.isNotEmpty()) setSessionId(sessionId)
        setCurrentImages(0)
        setCurrentFiles(0)
        for (doc in documents) {
            addCandidates(
                AttachmentCandidateInput.newBuilder()
                    .setName(doc.fileName.orEmpty())
                    .setSizeBytes(doc.fileBytes)
                    .setForceFile(true),
            )
        }
        for (pic in pictures) {
            addCandidates(
                AttachmentCandidateInput.newBuilder()
                    .setName(pic.fileName.orEmpty())
                    .setMimeType("image/jpeg")
                    .setForceFile(false),
            )
        }
    }.build()
    val eval = remote.evaluateAttachments(req)
    val rejected = eval.decisionsList.firstOrNull { !it.accepted } ?: return null
    val reason = when {
        eval.hasNotice() -> coreTextToMessageText(eval.notice).resolve(copy)
        rejected.hasRejectReason() -> coreTextToMessageText(rejected.rejectReason).resolve(copy)
        else -> ""
    }
    return reason.ifEmpty { null }
}

internal fun ImageDraft.uploadDocument(
    remote: Remote,
    entry: PendingImage,
    target: String,
    forceFresh: Boolean = false,
    caps: AttachmentCaps? = null,
) {
    bindRemote(remote)
    val effective = caps ?: remote.attachmentCapsFor(target)
    if (!effective.supportsFiles) {
        val turn = entry.stopUpload()
        if (entry.valid(turn)) {
            val detail = effective.fileUploadBlockedReason?.let { coreTextToMessageText(it) }
                ?: MessageText.Raw("")
            entry.state = PendingImage.State.Failed(
                AttachmentFailure(
                    input = ImageProblemInput.newBuilder()
                        .setExplicitKind(ImageProblemKind.IMAGE_PROBLEM_KIND_PROCESSING_FAILED)
                        .build(),
                    detail = detail,
                ),
            )
        }
        return
    }
    startDocumentUpload(
        entry = entry,
        target = target,
        owner = RaApp.instance.scope,
        channel = remote.uploadChannel(target),
        limit = effective.maxFileBytes,
        maxNameBytes = effective.maxFileNameBytes,
        maxSizeText = effective.maxFileSizeText,
        forceFresh = forceFresh,
    ) {
        RaApp.instance.contentResolver.openInputStream(Uri.parse(entry.uri)) ?: throw java.io.FileNotFoundException()
    }
}

/** Injectable channel/source/scope exercise cancellation races without an Android runtime. */
internal fun ImageDraft.startDocumentUpload(
    entry: PendingImage,
    target: String,
    owner: CoroutineScope,
    channel: UploadChannel,
    limit: Long,
    io: CoroutineDispatcher = Dispatchers.IO,
    forceFresh: Boolean = false,
    maxNameBytes: Int,
    maxSizeText: String,
    open: () -> InputStream,
) {
    if (!forceFresh && !needsUpload(entry, target)) return
    val changed = forceFresh || entry.state is PendingImage.State.Failed || entry.transportMachine() != target || entry.isProtected() || entry.fileId != null && entry.expiresMs <= System.currentTimeMillis()
    var turn = entry.stopUpload(abort = changed)
    if (!entry.activate(turn, target, fresh = changed)) return
    if (!entry.hasSource()) {
        entry.change(turn, target) { entry.state = PendingImage.State.Failed(AppImageFailure.FILE_MISSING) }
        persistOrder()
        return
    }
    fun bindAbort() {
        val transfer = entry.transferId
        entry.abortTransfer = { owner.launch { runCatching { channel.abort(transfer) } } }
    }
    bindAbort()
    persistOrder()
    entry.job = owner.launch {
        try {
            FileUploads.serial {
                if (!entry.current(turn, target)) return@serial
                var result: UploadedFile? = null
                for (attempt in 0..1) {
                    try {
                        val attemptTurn = turn
                        val transfer = entry.transferId
                        result = withContext(io) {
                            uploadFile(
                                ch = channel,
                                transferId = transfer,
                                name = entry.fileName!!,
                                total = entry.fileBytes,
                                open = open,
                                maxBytes = limit,
                                maxNameBytes = maxNameBytes,
                                maxSizeText = maxSizeText,
                                onPrepared = { actual ->
                                    entry.change(attemptTurn, target) {
                                        entry.fileBytes = actual
                                        entry.sizeVerified = true
                                        entry.state = PendingImage.State.Uploading(0f)
                                        prepared(entry)
                                    }
                                },
                                onProgress = { done, total ->
                                    entry.change(attemptTurn, target) {
                                        entry.state = PendingImage.State.Uploading(if (total > 0) done.toFloat() / total else 1f)
                                    }
                                },
                            )
                        }
                        break
                    } catch (e: CommandException) {
                        if (attempt > 0 || !restartUpload(e)) throw e
                        if (!entry.current(turn, target)) return@serial
                        turn = entry.stopUpload(cancelWork = false)
                        if (!entry.activate(turn, target, fresh = true)) return@serial
                        bindAbort(); persistOrder()
                    }
                }
                entry.change(turn, target) {
                    entry.machineId = target
                    entry.fileId = checkNotNull(result).fileId
                    entry.expiresMs = result.expiresMs
                    entry.state = PendingImage.State.Ready
                }
                if (entry.current(turn, target)) persistOrder()
            }
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            entry.change(turn, target) { entry.state = PendingImage.State.Failed(attachmentFailure(e, upload = true)) }
        }
    }
}
