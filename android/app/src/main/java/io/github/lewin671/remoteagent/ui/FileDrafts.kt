package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.provider.OpenableColumns
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.*
import kotlinx.coroutines.*
import java.io.InputStream

/** Provider size is a display hint; the hashing pass establishes the actual byte count. */
internal fun documentEntry(context: Context, uri: Uri): PendingImage {
    var name: String? = null
    var size = 0L
    context.contentResolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE), null, null, null)?.use {
        if (it.moveToFirst()) {
            val n = it.getColumnIndex(OpenableColumns.DISPLAY_NAME)
            val b = it.getColumnIndex(OpenableColumns.SIZE)
            if (n >= 0 && !it.isNull(n)) name = it.getString(n)
            if (b >= 0 && !it.isNull(b)) size = it.getLong(b).coerceAtLeast(0)
        }
    }
    if (name.isNullOrBlank()) throw io.github.lewin671.remoteagent.core.MessageArgumentException(io.github.lewin671.remoteagent.core.MessageText.Message(UiMessages.attachmentAndroidNameUnreadable()), imageKind = ImageProblemKind.SOURCE)
    if (name.toByteArray().size > 255) throw io.github.lewin671.remoteagent.core.MessageArgumentException(io.github.lewin671.remoteagent.core.MessageText.Message(UiMessages.attachmentAndroidNameTooLong()), imageKind = ImageProblemKind.SOURCE)
    return PendingImage(newRequestId(), uri.toString(), name, size, retainSource = false)
}

/** Files from 文件, or ([videos]) videos from the library, which are turned away as they are picked when over the size limit. */
internal fun ImageDraft.addDocuments(context: Context, uris: List<Uri>, remote: Remote, machine: String?, videos: Boolean = false) {
    if (restoring || readingFiles || uris.isEmpty()) return
    bindRemote(remote)
    readingFiles = true
    notice = ""
    RaApp.instance.scope.launch {
        try {
            for (uri in uris) {
                try {
                    val target = remote.state.value.machines[machine]
                    if (!filesSupported(target)) throw io.github.lewin671.remoteagent.core.MessageException(io.github.lewin671.remoteagent.core.MessageText.Message(UiMessages.attachmentUpdateRequired()), imageKind = ImageProblemKind.UPDATE_REQUIRED)
                    if (documents.size >= maxFiles(target)) { notice = sessionText(UiMessages.attachmentAndroidFilesLimit(count = (maxFiles(target)).toInt())); break }
                    val entry = withContext(Dispatchers.IO) { documentEntry(context, uri) }
                    if (videos) pickedVideoError(entry.fileName!!, entry.fileBytes, maxFileBytes(target))?.let { notice = sessionText(it); continue }
                    if (AccountMemory.token !== accountToken) { PendingUris.release(entry); return@launch }
                    PendingUris.retain(entry)
                    val kept = runCatching { context.contentResolver.takePersistableUriPermission(uri, Intent.FLAG_GRANT_READ_URI_PERMISSION) }.isSuccess
                    if (!kept) notice = sessionText(UiMessages.attachmentAndroidPermissionTemporary())
                    images += entry
                    prepared(entry)
                    machine?.let { upload(remote, entry, it) }
                } catch (e: CancellationException) { throw e
                } catch (e: Exception) {
                    notice = if (e is java.io.IOException || e is SecurityException) sessionText(UiMessages.attachmentAndroidSourceUnreadable()) else displayError(e)
                }
            }
        } finally { readingFiles = false }
    }
}

internal fun fileDraftBlocked(draft: ImageDraft, machine: MachineView?, copy: (io.github.lewin671.remoteagent.core.UiMessage) -> String = ::sessionText): String? {
    if (draft.documents.isEmpty()) return null
    if (!filesSupported(machine)) return copy(UiMessages.attachmentUpdateRequired())
    if (draft.documents.size > maxFiles(machine)) return copy(UiMessages.attachmentMaxFiles(count = (maxFiles(machine)).toInt()))
    return draft.documents.filter { it.sizeVerified }.firstNotNullOfOrNull { fileLimitError(it.fileBytes, 0, maxFiles(machine), maxFileBytes(machine))?.resolve(copy) }
}

internal fun ImageDraft.uploadDocument(remote: Remote, entry: PendingImage, target: String, forceFresh: Boolean = false) {
    bindRemote(remote)
    val machine = remote.state.value.machines[target]
    if (!filesSupported(machine)) {
        val turn = entry.stopUpload()
        if (entry.valid(turn)) entry.state = PendingImage.State.Failed(AppImageFailure.UPDATE_REQUIRED)
        return
    }
    startDocumentUpload(entry, target, RaApp.instance.scope, remote.uploadChannel(target), maxFileBytes(machine), forceFresh = forceFresh) {
        RaApp.instance.contentResolver.openInputStream(Uri.parse(entry.uri)) ?: throw java.io.FileNotFoundException()
    }
}

/** Injectable channel/source/scope exercise cancellation races without an Android runtime. */
internal fun ImageDraft.startDocumentUpload(
    entry: PendingImage, target: String, owner: CoroutineScope, channel: UploadChannel, limit: Long,
    io: CoroutineDispatcher = Dispatchers.IO, forceFresh: Boolean = false, open: () -> InputStream) {
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
                            uploadFile(channel, transfer, entry.fileName!!, entry.fileBytes, open, maxBytes = limit,
                                onPrepared = { actual ->
                                    entry.change(attemptTurn, target) {
                                        entry.fileBytes = actual
                                        entry.sizeVerified = true
                                        entry.state = PendingImage.State.Uploading(0f)
                                        prepared(entry)
                                    }
                                },
                                onProgress = { done, total -> entry.change(attemptTurn, target) {
                                    entry.state = PendingImage.State.Uploading(if (total > 0) done.toFloat() / total else 1f)
                                } },
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
        } catch (e: CancellationException) { throw e
        } catch (e: Exception) {
            entry.change(turn, target) { entry.state = PendingImage.State.Failed(attachmentFailure(e, upload = true)) }
        }
    }
}

/** History references without local sources remain visible and block partial/text-only resends. */
internal fun ImageDraft.restoreReferences(files: List<io.github.lewin671.remoteagent.proto.SentFile>, imageIds: List<String>, maxImages: Int, maxFiles: Int): Boolean {
    if (files.isEmpty() && imageIds.isEmpty()) return true
    val missing = files.map { PendingImage(newRequestId(), null, it.name, it.sizeBytes, retainSource = false) } +
        imageIds.map { PendingImage(newRequestId(), null, retainSource = false) }
    return attach(missing, maxImages, fileMax = maxFiles)
}
