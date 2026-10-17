package io.github.lewin671.remoteagent.ui

import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import io.github.lewin671.remoteagent.core.CommandException
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.errorText
import io.github.lewin671.remoteagent.core.newRequestId
import io.github.lewin671.remoteagent.proto.Command
import io.github.lewin671.remoteagent.proto.CommandResult
import io.github.lewin671.remoteagent.proto.SendRequest
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.launch

/** One command with its progress: running, and the error of the last try. */
class Action(private val scope: CoroutineScope) {
    var running by mutableStateOf(false)
        private set
    var error by mutableStateOf("")
    /** The last failure left the outcome unknown. */
    var unknown by mutableStateOf(false)
        private set

    fun run(block: suspend () -> CommandResult?, onDone: (CommandResult?) -> Unit = {}) {
        if (running) return
        running = true
        error = ""
        unknown = false
        scope.launch {
            try {
                onDone(block())
            } catch (e: Exception) {
                error = errorText(e)
                unknown = (e as? CommandException)?.unknown == true
            } finally {
                running = false
            }
        }
    }
}

@Composable
fun rememberAction(vararg keys: Any?): Action {
    val scope = rememberCoroutineScope()
    return remember(*keys) { Action(scope) }
}

fun Remote.cmd(b: Command.Builder.() -> Unit): Command = Command.newBuilder().apply(b).build()

/**
 * A message sent from this device. Its text stays in memory only (the disk
 * never holds plaintext); the daemon dedupes by [requestId], so a retry
 * never submits it twice.
 */
class Outgoing(
    val requestId: String,
    val text: String,
    /** Image IDs on the machine, sent with the message; the same on every retry. */
    val imageIds: List<String> = emptyList(),
    /** The picked images, for their thumbnails while sending and to put them back. */
    val images: List<PendingImage> = emptyList(),
    /** They were sent as originals. */
    val original: Boolean = false,
    val mode: io.github.lewin671.remoteagent.proto.SendMode = io.github.lewin671.remoteagent.proto.SendMode.SEND_MODE_STEER,
) {
    enum class State { SENDING, SENT, FAILED, UNKNOWN }

    var state by mutableStateOf(State.SENDING)
    var error by mutableStateOf("")
    var missingImages by mutableStateOf(false)
        private set
    val canPutBack: Boolean get() = state == State.FAILED && images.isNotEmpty()

    /** Retries preserve the message request and image IDs, even when an upload has since expired. */
    internal suspend fun deliver(send: suspend (SendRequest) -> Unit) {
        if (state == State.SENDING && delivering) return
        val wasUnknown = state == State.UNKNOWN
        delivering = true
        state = State.SENDING
        error = ""
        missingImages = false
        try {
            send(SendRequest.newBuilder().setText(text).setRequestId(requestId).addAllImageIds(imageIds).setMode(mode).build())
            state = State.SENT
        } catch (e: Exception) {
            val ce = e as? CommandException
            val unknown = ce?.unknown == true || (wasUnknown && ce?.fromDaemon != true)
            state = if (unknown) State.UNKNOWN else State.FAILED
            missingImages = !unknown && ce?.fromDaemon == true && ce.code == "not_found" &&
                imageIds.isNotEmpty() && ce.message.orEmpty().contains("is not on this machine; upload it again")
            error = if (missingImages) "图片不在电脑上。放回输入框后会重新上传，文字和图片都会保留" else errorText(e)
        } finally {
            delivering = false
        }
    }

    private var delivering = false
}

/**
 * Text being written outside a session's message box: first messages and
 * deny reasons. It lives in memory for this run only, never in saved
 * instance state, which the system may write to disk in plaintext.
 */
object Drafts {
    /** A first message with the configuration it will start with, kept and restored together. */
    class NewSession(initial: NewConfig?, settled: Boolean = true) {
        val text = mutableStateOf("")
        val config = DraftConfig(initial, settled)
        /** The CLI picked next to the message box for this draft, or null to use the one the project remembers. */
        val agent = mutableStateOf<io.github.lewin671.remoteagent.proto.Agent?>(null)
    }

    private val texts = HashMap<String, androidx.compose.runtime.MutableState<String>>()
    private val newSessions = HashMap<String, NewSession>()

    fun of(key: String): androidx.compose.runtime.MutableState<String> = texts.getOrPut(key) { mutableStateOf("") }

    /** The new-session draft of a project; [initial] only seeds one that does not exist yet ([settled]: see [ProjectStart]). */
    fun newSession(project: String, initial: NewConfig?, settled: Boolean = true): NewSession = newSessions.getOrPut(project) { NewSession(initial, settled) }

    fun clear() {
        texts.clear()
        newSessions.clear()
    }
}

/**
 * What this run keeps in memory for the signed-in account: drafts, messages
 * sent from here, and starts and answers whose outcome may be unknown. They
 * are keyed by project path, session or question ID, which another account
 * can share, so all of it goes when the account changes or is logged out.
 */
object AccountMemory {
    var token: Any = Any()
        private set
    private var account: String? = null

    /** The account now in use; a different one than before starts from nothing. */
    fun use(account: String) {
        if (account != this.account) clear()
        this.account = account
    }

    /** Forgets everything, e.g. on logout. */
    fun clear() {
        token = Any()
        account = null
        Drafts.clear()
        Outbox.clear()
        Starts.clear()
        Forks.clear()
        Answers.clear()
        Plans.clear()
        Cancels.clear()
        PrefsChanges.clear()
        ImageDrafts.clear()
        ViewerImages.clear()
        Resolves.clear()
        QuestionPicks.clear()
    }
}

/**
 * Messages with images kept on disk, sealed, from send until the CLI has
 * them (architecture §17): a reclaimed process can then still retry the
 * same request or put the message back. An index lists them by session.
 */
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
            files.put(f, "text", o.text.toByteArray())
            files.put(f, "ids", o.imageIds.joinToString("\n").toByteArray())
            files.put(f, "mode", o.mode.name.toByteArray())
            val line = "$sid\t${o.requestId}"
            val idx = index()
            if (line !in idx) writeIndex(idx + line)
        } catch (e: Exception) {
            // Not in the index: nothing would find what was written, and it would take up room unseen.
            runCatching { files.clearFolder(f) }
            throw e
        }
    }

    fun load(sid: String, thumb: (ByteArray) -> androidx.compose.ui.graphics.ImageBitmap? = { null }): List<Outgoing> = files.transaction {
        index().mapNotNull { line ->
            val (s, req) = line.split("\t").takeIf { it.size == 2 } ?: return@mapNotNull null
            if (s != sid) return@mapNotNull null
            val f = folder(sid, req)
            val text = files.get(f, "text")?.toString(Charsets.UTF_8) ?: return@mapNotNull null
            val ids = files.get(f, "ids")?.toString(Charsets.UTF_8)?.split("\n")?.filter { it.isNotEmpty() }.orEmpty()
            val (images, original) = loadImages(files, f, thumb)
            val mode = files.get(f, "mode")?.toString(Charsets.UTF_8)?.let { runCatching { io.github.lewin671.remoteagent.proto.SendMode.valueOf(it) }.getOrNull() }
                ?: io.github.lewin671.remoteagent.proto.SendMode.SEND_MODE_STEER
            Outgoing(req, text, ids, images, original, mode).apply {
                // From a run that ended: whether it reached the computer is not known.
                state = Outgoing.State.UNKNOWN
                error = "App 重启过，不确定这条有没有送到"
            }
        }
    }

    fun remove(sid: String, requestId: String) = files.transaction {
        files.clearFolder(folder(sid, requestId))
        writeIndex(index().filter { it != "$sid\t$requestId" })
    }
}

/** The latest state of each message by request ID, from a session's events. */
fun inputStates(events: List<io.github.lewin671.remoteagent.proto.Event>): Map<String, io.github.lewin671.remoteagent.proto.InputState> {
    val out = HashMap<String, io.github.lewin671.remoteagent.proto.InputState>()
    for (e in events) if (e.kindCase == io.github.lewin671.remoteagent.proto.Event.KindCase.INPUT_STATE) out[e.inputState.requestId] = e.inputState
    return out
}

/**
 * The messages to let go of: submitted, or canceled by the user. One the
 * session ended on (CANCELED, reason "exited") is kept: the chat offers to
 * put it back into the message box, text and images.
 */
fun recorded(list: List<Outgoing>, inputs: Map<String, io.github.lewin671.remoteagent.proto.InputState>): List<Outgoing> =
    list.filter {
        val st = inputs[it.requestId]
        st?.status == io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_SUBMITTED ||
            (st?.status == io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_CANCELED && st.reason != "exited")
    }

/**
 * Whether a message sent from here is still shown under the chat: until the
 * daemon's events have it, and for as long as they say it did not go. The
 * events are the truth: once they have the message queued, submitted or
 * canceled, a try from here that failed or was left unknown did arrive.
 */
fun outgoingShown(input: io.github.lewin671.remoteagent.proto.InputStatus?): Boolean =
    input == null || input == io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_UNCONFIRMED ||
        input == io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_FAILED || input == io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_NEEDS_TERMINAL

/** Messages sent from this device, per session, for this run of the app (and those with images, from before it). */
object Outbox {
    private val bySession = HashMap<String, androidx.compose.runtime.snapshots.SnapshotStateList<Outgoing>>()
    private val records get() = OutgoingRecords(io.github.lewin671.remoteagent.RaApp.instance.pendingStore.scoped())

    fun of(sid: String) = bySession.getOrPut(sid) {
        mutableStateListOf<Outgoing>().also { list -> runCatching { list += records.load(sid, ::thumbnail) } }
    }

    fun send(scope: CoroutineScope, remote: Remote, sid: String, text: String, mode: io.github.lewin671.remoteagent.proto.SendMode = io.github.lewin671.remoteagent.proto.SendMode.SEND_MODE_STEER) {
        val o = Outgoing(newRequestId(), text, mode = mode)
        of(sid) += o
        deliver(scope, remote, sid, o)
    }

    /**
     * Sends a message with images. They are first kept on disk, sealed, off
     * the main thread; only once that worked is the message sent and
     * [onKept] told true, for the caller to take the images out of the draft.
     * If keeping them failed nothing is sent and the draft stays as it is.
     * Meanwhile [keeping] is true and a second send does nothing: the
     * images are still in the draft and would be taken a second time.
     */
    fun sendWithImages(
        scope: CoroutineScope, remote: Remote, sid: String, text: String,
        imageIds: List<String>, images: List<PendingImage>, original: Boolean,
        mode: io.github.lewin671.remoteagent.proto.SendMode = io.github.lewin671.remoteagent.proto.SendMode.SEND_MODE_STEER,
        store: ((String, Outgoing, Boolean) -> Unit)? = null,
        discard: ((String, String) -> Unit)? = null,
        onKept: (Result<Unit>) -> Unit,
    ) {
        if (keeping(sid)) return
        keepingNow[sid] = true
        val o = Outgoing(newRequestId(), text, imageIds, images, original, mode)
        val account = AccountMemory.token
        val ownedRecords = if (store == null) records else null
        val keep = store ?: { s: String, message: Outgoing, orig: Boolean -> ownedRecords!!.save(s, message, orig) }
        val drop = discard ?: { s: String, id: String -> ownedRecords?.remove(s, id); Unit }
        val machine = images.firstOrNull()?.machineId
        scope.launch {
            val kept = kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.IO) {
                runCatching {
                    keep(sid, o, original)
                    // Saving large originals can outlast the upload. Nothing has been sent yet:
                    // discard this record and leave the draft to upload again with a fresh ID.
                    if (machine == null || images.size != imageIds.size || images.indices.any { i ->
                        !images[i].readyFor(machine) || images[i].imageId != imageIds[i]
                    }) {
                        drop(sid, o.requestId)
                        throw IllegalStateException("图片需要重新传到电脑，请上传完成后再发送")
                    }
                }
            }
            if (account !== AccountMemory.token) return@launch
            keepingNow.remove(sid)
            onKept(kept)
            if (kept.isFailure) return@launch
            of(sid) += o
            watch(remote, sid)
            deliver(scope, remote, sid, o)
        }
    }

    private val keepingNow = androidx.compose.runtime.mutableStateMapOf<String, Boolean>()

    /**
     * A message's images are being kept before it is sent. The lock is the
     * session's, not the message box's: the box goes away and comes back
     * with the 聊天 / 终端 tabs while the images are still being kept.
     */
    fun keeping(sid: String): Boolean = keepingNow[sid] == true

    private val watching = HashMap<String, kotlinx.coroutines.Job>()

    /**
     * Follows a session's events while messages with images wait here, and
     * lets them go once the CLI has recorded them, whether or not the chat
     * is on screen.
     */
    fun watch(remote: Remote, sid: String) {
        if (sid in watching) return
        lateinit var job: kotlinx.coroutines.Job
        job = io.github.lewin671.remoteagent.RaApp.instance.scope.launch {
            try {
                remote.events(sid).collect { ev -> settle(sid, inputStates(ev.events)) }
            } finally {
                if (watching[sid] === job) watching.remove(sid)
            }
        }
        watching[sid] = job
    }

    /**
     * A session's last message with images is gone: its events are let go
     * of too. Waiting for the next batch to notice would keep the whole
     * history of a session that sends none, one that ended or whose
     * computer is offline.
     */
    private fun unwatch(sid: String) {
        if (bySession[sid]?.none { it.images.isNotEmpty() } != false) watching.remove(sid)?.cancel()
    }

    fun deliver(scope: CoroutineScope, remote: Remote, sid: String, o: Outgoing) {
        scope.launch {
            o.deliver { request ->
                remote.command(sid, remote.cmd { send = request.toBuilder().setSessionId(sid).build() })
            }
        }
    }

    /** Drops a message from here for good: its kept images and their bytes go too. */
    fun remove(sid: String, o: Outgoing) {
        of(sid).remove(o)
        if (o.images.isNotEmpty()) runCatching { records.remove(sid, o.requestId) }
        unwatch(sid)
    }

    /**
     * Takes a message back for the message box: its images live on in
     * [draft]. Its record goes once they are on disk there: until then it is
     * the only copy a reclaimed process would find.
     */
    fun takeBack(sid: String, o: Outgoing, draft: ImageDraft) {
        of(sid).remove(o)
        if (o.images.isNotEmpty()) {
            val kept = records
            draft.whenKept(o.images.map { it.id }) { runCatching { kept.remove(sid, o.requestId) } }
        }
        unwatch(sid)
    }

    /**
     * Lets go of messages submitted or canceled by the user ([recorded]):
     * their images' bytes, thumbnails and picker grants, and the copies on
     * disk.
     */
    fun settle(sid: String, inputs: Map<String, io.github.lewin671.remoteagent.proto.InputState>) {
        for (o in recorded(of(sid), inputs)) {
            remove(sid, o)
            o.images.forEach { it.release() }
        }
    }

    fun find(sid: String, requestId: String): Outgoing? = of(sid).firstOrNull { it.requestId == requestId }

    private val prompts = HashMap<String, String>()
    private val drafts = HashMap<String, androidx.compose.runtime.MutableState<String>>()

    /** What is being typed in a session's message box, so a message can be put back into it. */
    fun draft(sid: String) = drafts.getOrPut(sid) { mutableStateOf("") }

    /** The first message of a session started here, sent by the daemon once the CLI is ready. */
    fun rememberPrompt(sid: String, text: String) {
        if (text.isNotEmpty()) prompts[sid] = text
    }

    fun prompt(sid: String): String? = prompts[sid]

    fun clear() {
        bySession.clear()
        keepingNow.clear()
        watching.values.forEach { it.cancel() }
        watching.clear()
        prompts.clear()
        drafts.clear()
    }

    /** Local text of a request, for queued messages. */
    fun text(sid: String, requestId: String): String? = bySession[sid]?.firstOrNull { it.requestId == requestId }?.text
}
