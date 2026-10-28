package io.github.lewin671.remoteagent.ui

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
    require(!name.isNullOrBlank()) { "无法读取文件名，请重新选择文件" }
    require(name!!.toByteArray().size <= 255) { "文件名过长，请重命名后重新选择" }
    return PendingImage(newRequestId(), uri.toString(), name, size, retainSource = false)
}

internal fun ImageDraft.addDocuments(context: Context, uris: List<Uri>, remote: Remote, machine: String?) {
    if (restoring || readingFiles || uris.isEmpty()) return
    bindRemote(remote)
    readingFiles = true
    notice = ""
    RaApp.instance.scope.launch {
        try {
            for (uri in uris) {
                try {
                    val target = remote.state.value.machines[machine]
                    check(filesSupported(target)) { "这台电脑不支持发送文件，请更新电脑上的 remote-agent" }
                    if (documents.size >= maxFiles(target)) { notice = "一条消息最多 ${maxFiles(target)} 个文件，多选的文件没有加入"; break }
                    val entry = withContext(Dispatchers.IO) { documentEntry(context, uri) }
                    if (AccountMemory.token !== accountToken) { PendingUris.release(entry); return@launch }
                    PendingUris.retain(entry)
                    val kept = runCatching { context.contentResolver.takePersistableUriPermission(uri, Intent.FLAG_GRANT_READ_URI_PERMISSION) }.isSuccess
                    if (!kept) notice = "这个文件的读取权限无法长期保留，App 重启后可能需要重新选择文件"
                    images += entry
                    prepared(entry)
                    machine?.let { upload(remote, entry, it) }
                } catch (e: CancellationException) { throw e
                } catch (e: Exception) {
                    notice = if (e is java.io.IOException || e is SecurityException) "无法读取源文件，请重新选择文件" else errorText(e)
                }
            }
        } finally { readingFiles = false }
    }
}

internal fun fileDraftBlocked(draft: ImageDraft, machine: MachineView?): String? {
    if (draft.documents.isEmpty()) return null
    if (!filesSupported(machine)) return "这台电脑不支持发送文件，请更新电脑上的 remote-agent"
    if (draft.documents.size > maxFiles(machine)) return "一条消息最多 ${maxFiles(machine)} 个文件"
    return draft.documents.filter { it.sizeVerified }.firstNotNullOfOrNull { fileLimitError(it.fileBytes, 0, maxFiles(machine), maxFileBytes(machine)) }
}

internal fun ImageDraft.uploadDocument(remote: Remote, entry: PendingImage, target: String) {
    bindRemote(remote)
    val machine = remote.state.value.machines[target]
    if (!filesSupported(machine)) {
        entry.stopUpload()
        entry.state = PendingImage.State.Failed("这台电脑不支持发送文件，请更新电脑上的 remote-agent")
        return
    }
    startDocumentUpload(entry, target, RaApp.instance.scope, remote.uploadChannel(target), maxFileBytes(machine)) {
        RaApp.instance.contentResolver.openInputStream(Uri.parse(entry.uri)) ?: throw java.io.FileNotFoundException()
    }
}

/** Injectable channel/source/scope exercise cancellation races without an Android runtime. */
internal fun ImageDraft.startDocumentUpload(
    entry: PendingImage, target: String, owner: CoroutineScope, channel: UploadChannel, limit: Long,
    io: CoroutineDispatcher = Dispatchers.IO, open: () -> InputStream,
) {
    if (!needsUpload(entry, target)) return
    val changed = entry.machineId != target || entry.fileId != null && entry.expiresMs <= System.currentTimeMillis()
    entry.stopUpload(abort = changed)
    synchronized(entry) {
        if (changed) entry.transferId = newRequestId()
        entry.fileId = null
        entry.machineId = target
        entry.expiresMs = 0
        entry.state = PendingImage.State.Processing
    }
    val turn = entry.generation
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
                        result = withContext(io) {
                            uploadFile(channel, entry.transferId, entry.fileName!!, entry.fileBytes, open, maxBytes = limit,
                                onPrepared = { actual ->
                                    entry.change(turn, target) {
                                        entry.fileBytes = actual
                                        entry.sizeVerified = true
                                        entry.state = PendingImage.State.Uploading(0f)
                                        prepared(entry)
                                    }
                                },
                                onProgress = { done, total -> entry.change(turn, target) {
                                    entry.state = PendingImage.State.Uploading(if (total > 0) done.toFloat() / total else 1f)
                                } },
                            )
                        }
                        break
                    } catch (e: CommandException) {
                        if (attempt > 0 || !restartUpload(e)) throw e
                        val previous = entry.transferId
                        runCatching { channel.abort(previous) }
                        if (!entry.change(turn, target) {
                            entry.transferId = newRequestId(); entry.state = PendingImage.State.Processing
                            bindAbort(); persistOrder()
                        }) return@serial
                    }
                }
                entry.change(turn, target) {
                    entry.fileId = checkNotNull(result).fileId
                    entry.expiresMs = result.expiresMs
                    entry.state = PendingImage.State.Ready
                }
                if (entry.current(turn, target)) persistOrder()
            }
        } catch (e: CancellationException) { throw e
        } catch (e: Exception) {
            entry.change(turn, target) { entry.state = PendingImage.State.Failed(uploadErrorText(e)) }
        }
    }
}
