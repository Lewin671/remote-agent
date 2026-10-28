package io.github.lewin671.remoteagent.ui

import android.content.Context
import android.content.Intent
import android.net.Uri
import android.provider.OpenableColumns
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.*
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.ensureActive
import kotlin.coroutines.coroutineContext
import kotlinx.coroutines.withContext

/** SAF metadata only; the document stays with its provider, outside the encrypted image byte store. */
internal suspend fun documentEntry(context: Context, uri: Uri): PendingImage {
    var name: String? = null
    var size: Long? = null
    context.contentResolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE), null, null, null)?.use {
        if (it.moveToFirst()) {
            val n = it.getColumnIndex(OpenableColumns.DISPLAY_NAME)
            val b = it.getColumnIndex(OpenableColumns.SIZE)
            if (n >= 0 && !it.isNull(n)) name = it.getString(n)
            if (b >= 0 && !it.isNull(b)) size = it.getLong(b)
        }
    }
    require(!name.isNullOrBlank()) { "无法读取文件名，请重新选择文件" }
    if (size == null) size = context.contentResolver.openAssetFileDescriptor(uri, "r")?.use { it.length }
    if (size == null || size!! < 0) {
        size = context.contentResolver.openInputStream(uri)?.use { input ->
            val buffer = ByteArray(64 * 1024)
            var count = 0L
            while (count <= MAX_FILE_BYTES) {
                coroutineContext.ensureActive()
                val n = input.read(buffer)
                if (n < 0) break
                count += n
            }
            count
        }
    }
    require(size != null && size!! >= 0) { "无法读取源文件，请重新选择文件" }
    return PendingImage(newRequestId(), uri.toString(), name, size!!)
}

internal fun ImageDraft.addDocuments(context: Context, uris: List<Uri>, remote: Remote, machine: String?) {
    if (restoring || readingFiles || uris.isEmpty()) return
    readingFiles = true
    notice = ""
    RaApp.instance.scope.launch {
        try { for (uri in uris) {
            try {
                val target = remote.state.value.machines[machine]
                check(filesSupported(target)) { "这台电脑不支持发送文件，请更新电脑上的 remote-agent" }
                if (documents.size >= maxFiles(target)) {
                    notice = "一条消息最多 ${maxFiles(target)} 个文件，多选的文件没有加入"
                    break
                }
                val entry = withContext(Dispatchers.IO) { documentEntry(context, uri) }
                fileLimitError(entry.fileBytes, documents.size, maxFiles(target), maxFileBytes(target))?.let { error(it) }
                require(entry.fileName!!.toByteArray().size <= 255) { "文件名过长，请重命名后重新选择" }
                if (AccountMemory.token !== accountToken) return@launch
                runCatching { context.contentResolver.takePersistableUriPermission(uri, Intent.FLAG_GRANT_READ_URI_PERMISSION) }
                // Same metadata persistence queue, with no file bytes copied.
                images += entry
                prepared(entry)
                machine?.let { upload(remote, entry, it) }
            } catch (e: kotlinx.coroutines.CancellationException) { throw e
            } catch (e: Exception) {
                notice = if (e is java.io.IOException || e is SecurityException) "无法读取源文件，请重新选择文件" else errorText(e)
            }
        } } finally { readingFiles = false }
    }
}

internal fun fileDraftBlocked(draft: ImageDraft, machine: MachineView?): String? {
    if (draft.documents.isEmpty()) return null
    if (!filesSupported(machine)) return "这台电脑不支持发送文件，请更新电脑上的 remote-agent"
    if (draft.documents.size > maxFiles(machine)) return "一条消息最多 ${maxFiles(machine)} 个文件"
    return draft.documents.firstNotNullOfOrNull { fileLimitError(it.fileBytes, 0, maxFiles(machine), maxFileBytes(machine)) }
}

internal fun ImageDraft.uploadDocument(remote: Remote, entry: PendingImage, target: String) {
    if (!needsUpload(entry, target)) return
    entry.job?.cancel()
    if (entry.machineId != target || entry.fileId != null && entry.expiresMs <= System.currentTimeMillis()) {
        entry.transferId = newRequestId()
    }
    entry.fileId = null
    entry.machineId = target
    entry.expiresMs = 0
    entry.state = PendingImage.State.Uploading(0f)
    entry.job = RaApp.instance.scope.launch {
        try {
            val machine = remote.state.value.machines[target]
            check(filesSupported(machine)) { "这台电脑不支持发送文件，请更新电脑上的 remote-agent" }
            fileLimitError(entry.fileBytes, 0, maxFiles(machine), maxFileBytes(machine))?.let { error(it) }
            val resolver = RaApp.instance.contentResolver
            val result = withContext(Dispatchers.IO) {
                val open = {
                    try { resolver.openInputStream(Uri.parse(entry.uri)) ?: error("missing source") }
                    catch (e: Exception) { throw IllegalStateException("无法读取源文件，请移除后重新选择文件", e) }
                }
                suspend fun send() = uploadFile(remote.uploadChannel(target), entry.transferId, entry.fileName!!, entry.fileBytes, open) { done, total ->
                    entry.state = PendingImage.State.Uploading(if (total > 0) done.toFloat() / total else 1f)
                }
                try { send() } catch (e: CommandException) {
                    if (e.code != "not_found") throw e
                    entry.transferId = newRequestId()
                    send()
                }
            }
            entry.fileId = result.fileId
            entry.expiresMs = result.expiresMs
            entry.state = PendingImage.State.Ready
        } catch (e: kotlinx.coroutines.CancellationException) { throw e
        } catch (e: Exception) {
            entry.state = PendingImage.State.Failed(if (e is java.io.IOException || e is SecurityException) "无法读取源文件，请移除后重新选择文件" else errorText(e))
        }
    }
}
