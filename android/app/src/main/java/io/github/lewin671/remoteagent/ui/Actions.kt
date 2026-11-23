package io.github.lewin671.remoteagent.ui

import android.net.Uri
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.CommandException
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.messageText
import io.github.lewin671.remoteagent.core.newRequestId
import io.github.lewin671.remoteagent.core.proto.SessionResendAttachmentsEffect
import io.github.lewin671.remoteagent.core.uploadFile
import io.github.lewin671.remoteagent.core.uploadImage
import io.github.lewin671.remoteagent.proto.Command
import io.github.lewin671.remoteagent.proto.CommandResult
import io.github.lewin671.remoteagent.proto.SendMode
import io.github.lewin671.remoteagent.proto.SendRequest
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/** One command with its progress: running, and the error of the last try. */
class Action(private val scope: CoroutineScope) {
    var running by mutableStateOf(false)
        private set
    var error by mutableStateOf("")
    var unknown by mutableStateOf(false)
        private set
    var diskFull by mutableStateOf(false)
        private set

    fun run(block: suspend () -> CommandResult?, onDone: (CommandResult?) -> Unit = {}) {
        if (running) return
        running = true
        error = ""
        unknown = false
        diskFull = false
        scope.launch {
            try {
                onDone(block())
            } catch (e: Exception) {
                error = displayError(e)
                unknown = (e as? CommandException)?.unknown == true
                diskFull = (e as? CommandException)?.code == "disk_full"
            } finally {
                running = false
            }
        }
    }
}

@Composable
fun rememberAction(vararg keys: Any?, authority: Boolean = false): Action {
    val scope = if (authority) rememberAuthorityScope() else rememberCoroutineScope()
    return remember(*keys) { Action(scope) }
}

fun Remote.cmd(b: Command.Builder.() -> Unit): Command = Command.newBuilder().apply(b).build()

/**
 * Local attachment thumbnails and disk-sealed copies for outgoing messages with images/files
 * while they are in flight or failed/unknown, keyed by [requestId].
 * Command lifecycle, retries, and state belong to Core (`SessionViewModel.composer.outgoing`).
 */
class Outgoing(
    val requestId: String,
    val images: List<PendingImage> = emptyList(),
    val original: Boolean = false,
) {
    val hasLocalAttachments: Boolean get() = images.isNotEmpty() && images.all { it.hasSource() }
}

/**
 * Text being written outside a session's message box: deny reasons.
 * Lives in memory only for this run.
 */
object Drafts {
    private val texts = HashMap<String, androidx.compose.runtime.MutableState<String>>()

    fun of(key: String): androidx.compose.runtime.MutableState<String> = texts.getOrPut(key) { mutableStateOf("") }

    fun clear() {
        texts.clear()
    }
}

/**
 * What this run keeps in memory for the signed-in account. Cleared whenever the account changes.
 */
object AccountMemory {
    var token: Any = Any()
        private set
    private var account: String? = null

    fun use(account: String) {
        if (account != this.account) clear()
        this.account = account
    }

    fun clear() {
        token = Any()
        account = null
        Drafts.clear()
        CleanupDraft.clear()
        Outbox.clear()
        Starts.clear()
        ImageDrafts.clear()
        PendingUris.clear()
        ViewerImages.clear()
    }
}

/** Sealed disk store for local outgoing image thumbnails until Core confirms acceptance or dismisses them. */
class OutgoingRecords(private val files: SealedFiles) {
    private fun folder(sid: String, requestId: String) = "out:$sid:$requestId"

    private fun index(): MutableList<String> =
        files.get("outbox", "index")?.toString(Charsets.UTF_8)?.split("\n")?.filter { it.isNotEmpty() }?.toMutableList() ?: mutableListOf()

    private fun writeIndex(lines: List<String>) = files.put("outbox", "index", lines.joinToString("\n").toByteArray())

    fun save(sid: String, o: Outgoing, original: Boolean) = files.transaction {
        val f = folder(sid, o.requestId)
        try {
            saveImages(files, f, o.images, original)
            saveOrder(files, f, o.images, original)
            val line = "$sid\t${o.requestId}"
            val idx = index()
            if (line !in idx) writeIndex(idx + line)
        } catch (e: Exception) {
            runCatching { files.clearFolder(f) }
            throw e
        }
    }

    fun load(sid: String, thumb: (ByteArray) -> androidx.compose.ui.graphics.ImageBitmap? = { null }): List<Outgoing> = files.transaction {
        index().mapNotNull { line ->
            val (s, req) = line.split("\t").takeIf { it.size == 2 } ?: return@mapNotNull null
            if (s != sid) return@mapNotNull null
            val (images, original) = loadImages(files, folder(sid, req), thumb)
            Outgoing(req, images, original)
        }
    }

    fun removeSession(sid: String) = files.transaction {
        val old = index()
        old.filter { it.startsWith("$sid\t") }.forEach { line -> files.clearFolder(folder(sid, line.substringAfter('\t'))) }
        writeIndex(old.filterNot { it.startsWith("$sid\t") })
    }

    fun remove(sid: String, requestId: String) = files.transaction {
        files.clearFolder(folder(sid, requestId))
        writeIndex(index().filter { it != "$sid\t$requestId" })
    }
}

/** Local attachment bridge between [ImageDrafts] and Core's [SessionComposerView.outgoing]. */
object Outbox {
    private val forgotten = HashSet<String>()
    private val bySession = HashMap<String, androidx.compose.runtime.snapshots.SnapshotStateList<Outgoing>>()
    private val keepingNow = mutableStateMapOf<String, Boolean>()
    private val records get() = OutgoingRecords(io.github.lewin671.remoteagent.RaApp.instance.pendingStore.scoped())

    fun of(sid: String): androidx.compose.runtime.snapshots.SnapshotStateList<Outgoing> {
        bySession[sid]?.let { return it }
        val list = mutableStateListOf<Outgoing>()
        bySession[sid] = list
        io.github.lewin671.remoteagent.RaApp.scopeOrNull()?.let { scope -> restore(sid, scope, records) }
        return list
    }

    internal fun restore(
        sid: String,
        scope: CoroutineScope,
        records: OutgoingRecords,
        thumb: (ByteArray) -> androidx.compose.ui.graphics.ImageBitmap? = ::thumbnail,
    ) = scope.launch {
        val list = bySession.getOrPut(sid) { mutableStateListOf() }
        val account = AccountMemory.token
        val loaded = withContext(Dispatchers.IO) { runCatching { records.load(sid, thumb) }.getOrDefault(emptyList()) }
        if (account !== AccountMemory.token || bySession[sid] !== list) {
            loaded.flatMap { it.images }.forEach { PendingUris.release(it) }
            return@launch
        }
        val restored = loaded.filter { old -> list.none { it.requestId == old.requestId } }
        restored.flatMap { it.images }.forEach { PendingUris.retain(it) }
        list += restored
    }

    fun keeping(sid: String): Boolean = keepingNow[sid] == true

    /**
     * Seals local attachment copies on disk off the main thread before submitting `SendRequest` to Core.
     */
    fun sendWithImages(
        scope: CoroutineScope,
        remote: Remote,
        sid: String,
        text: String,
        imageIds: List<String>,
        images: List<PendingImage>,
        original: Boolean,
        mode: SendMode = SendMode.SEND_MODE_STEER,
        fileIds: List<String> = emptyList(),
        store: (String, Outgoing, Boolean) -> Unit = { s, outgoing, orig -> records.save(s, outgoing, orig) },
        discard: (String, String) -> Unit = { s, req -> records.remove(s, req) },
        onKept: (Result<Unit>) -> Unit,
    ) {
        if (keeping(sid)) return
        val machine = images.firstOrNull()?.machineId
        try {
            remote.requireFileSupport(machine, fileIds.isNotEmpty())
        } catch (e: CommandException) {
            onKept(Result.failure(e))
            return
        }
        keepingNow[sid] = true
        val reqId = newRequestId()
        val o = Outgoing(reqId, images, original)
        val account = AccountMemory.token
        scope.launch {
            val kept = withContext(Dispatchers.IO) {
                runCatching {
                    store(sid, o, original)
                    try {
                        remote.requireFileSupport(machine, fileIds.isNotEmpty())
                    } catch (e: CommandException) {
                        discard(sid, reqId)
                        throw e
                    }
                    if (machine == null || images.any { !it.readyFor(machine) } ||
                        images.filterNot { it.isFile }.mapNotNull { it.imageId } != imageIds ||
                        images.filter { it.isFile }.mapNotNull { it.fileId } != fileIds
                    ) {
                        discard(sid, reqId)
                        throw io.github.lewin671.remoteagent.core.MessageException(UiMessages.attachmentAndroidUploadRequired().messageText())
                    }
                }
            }
            if (account !== AccountMemory.token) return@launch
            if (sid in forgotten) {
                withContext(Dispatchers.IO) { runCatching { discard(sid, reqId) } }
                return@launch
            }
            keepingNow.remove(sid)
            onKept(kept)
            if (kept.isFailure) return@launch
            of(sid) += o
            remote.submitSessionCommand(
                sid,
                remote.cmd {
                    send = SendRequest.newBuilder()
                        .setSessionId(sid)
                        .setText(text)
                        .setRequestId(reqId)
                        .addAllImageIds(imageIds)
                        .addAllFileIds(fileIds)
                        .setMode(mode)
                        .build()
                },
                machineId = machine.orEmpty(),
            )
        }
    }

    fun remove(sid: String, requestId: String, releaseImages: Boolean = true) {
        val list = bySession[sid] ?: return
        val hit = list.firstOrNull { it.requestId == requestId } ?: return
        list.remove(hit)
        runCatching { records.remove(sid, requestId) }
        if (releaseImages) hit.images.forEach { it.release(abort = false) }
    }

    fun takeBack(sid: String, requestId: String, draft: ImageDraft, maxImages: Int = 6, maxFiles: Int = 6): Boolean {
        val hit = bySession[sid]?.firstOrNull { it.requestId == requestId } ?: return false
        if (!hit.hasLocalAttachments) return false
        val attached = draft.attach(hit.images, maxImages, hit.original, fileMax = maxFiles)
        if (!attached) return false
        bySession[sid]?.remove(hit)
        draft.whenKept(hit.images.map { it.id }) { runCatching { records.remove(sid, requestId) } }
        return true
    }

    /** Releases local attachment copies once Core no longer keeps the outgoing entry around. */
    fun pruneSettled(sid: String, activeRequestIds: Set<String>) {
        val list = bySession[sid] ?: return
        val stale = list.filter { it.requestId !in activeRequestIds }
        for (o in stale) remove(sid, o.requestId, releaseImages = true)
    }

    fun find(sid: String, requestId: String): Outgoing? = bySession[sid]?.firstOrNull { it.requestId == requestId }

    fun resendWithAttachments(
        scope: CoroutineScope,
        remote: Remote,
        sid: String,
        eff: SessionResendAttachmentsEffect,
    ) {
        val targetMachine = eff.machineId.ifEmpty { remote.state.value.sessions[sid]?.machineId.orEmpty() }
        val source = find(sid, eff.sourceRequestId)
        val account = AccountMemory.token
        scope.launch {
            var imgIds = eff.imageIdsList
            var fileIds = eff.fileIdsList
            if (source != null && source.images.isNotEmpty() && targetMachine.isNotEmpty()) {
                val uploadedImgs = ArrayList<String>()
                val uploadedFiles = ArrayList<String>()
                val clones = ArrayList<PendingImage>(source.images.size)
                val channel = remote.uploadChannel(targetMachine)
                val caps = remote.attachmentCapsFor(targetMachine, sid)
                val ok = runCatching {
                    withContext(Dispatchers.IO) {
                        for (orig in source.images) {
                            val clone = PendingImage(
                                id = newRequestId(),
                                uri = orig.uri,
                                fileName = orig.fileName,
                                fileBytes = orig.fileBytes,
                                retainSource = true,
                            )
                            if (orig.isFile) {
                                val u = orig.uri ?: throw java.io.FileNotFoundException()
                                val res = uploadFile(
                                    channel,
                                    newRequestId(),
                                    orig.fileName ?: "file",
                                    orig.fileBytes,
                                    { RaApp.instance.contentResolver.openInputStream(Uri.parse(u)) ?: throw java.io.FileNotFoundException() },
                                    maxBytes = caps.maxFileBytes,
                                    maxNameBytes = caps.maxFileNameBytes,
                                    maxSizeText = caps.maxFileSizeText,
                                )
                                clone.fileId = res.fileId
                                clone.machineId = targetMachine
                                clone.expiresMs = res.expiresMs
                                clone.sizeVerified = true
                                clone.state = PendingImage.State.Ready
                                uploadedFiles += res.fileId
                            } else {
                                val p = orig.processed ?: orig.uri?.let { processImage(RaApp.instance, Uri.parse(it), source.original) }
                                    ?: throw java.io.FileNotFoundException()
                                val res = uploadImage(channel, newRequestId(), p.bytes)
                                clone.processed = p
                                clone.thumb = orig.thumb
                                clone.imageId = res.imageId
                                clone.machineId = targetMachine
                                clone.expiresMs = res.expiresMs
                                clone.state = PendingImage.State.Ready
                                uploadedImgs += res.imageId
                            }
                            clones += clone
                        }
                    }
                }.isSuccess
                if (account !== AccountMemory.token) {
                    clones.forEach { it.release(abort = false) }
                    return@launch
                }
                if (ok) {
                    imgIds = uploadedImgs
                    fileIds = uploadedFiles
                    val newOutgoing = Outgoing(eff.newRequestId, clones, source.original)
                    runCatching { withContext(Dispatchers.IO) { records.save(sid, newOutgoing, source.original) } }
                    of(sid) += newOutgoing
                } else {
                    clones.forEach { it.release(abort = false) }
                }
            }
            if (account !== AccountMemory.token) return@launch
            remote.submitSessionCommand(
                sid,
                remote.cmd {
                    send = SendRequest.newBuilder()
                        .setSessionId(sid)
                        .setRequestId(eff.newRequestId)
                        .setText(eff.text)
                        .setMode(eff.mode)
                        .addAllImageIds(imgIds)
                        .addAllFileIds(fileIds)
                        .build()
                },
                machineId = targetMachine,
            )
        }
    }

    fun forgetSession(sid: String) {
        forgotten += sid
        bySession.remove(sid)?.flatMap { it.images }?.forEach { it.release(abort = false) }
        keepingNow.remove(sid)
    }

    fun clear() {
        forgotten.clear()
        bySession.clear()
        keepingNow.clear()
    }
}
