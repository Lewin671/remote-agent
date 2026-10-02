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
fun rememberAction(): Action {
    val scope = rememberCoroutineScope()
    return remember { Action(scope) }
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
}

/**
 * Text being written outside a session's message box: first messages and
 * deny reasons. It lives in memory for this run only, never in saved
 * instance state, which the system may write to disk in plaintext.
 */
object Drafts {
    /** A first message with the configuration it will start with, kept and restored together. */
    class NewSession(initial: NewConfig?) {
        val text = mutableStateOf("")
        val config = DraftConfig(initial)
    }

    private val texts = HashMap<String, androidx.compose.runtime.MutableState<String>>()
    private val newSessions = HashMap<String, NewSession>()

    fun of(key: String): androidx.compose.runtime.MutableState<String> = texts.getOrPut(key) { mutableStateOf("") }

    /** The new-session draft of a project; [initial] only seeds one that does not exist yet. */
    fun newSession(project: String, initial: NewConfig?): NewSession = newSessions.getOrPut(project) { NewSession(initial) }

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
    private var account: String? = null

    /** The account now in use; a different one than before starts from nothing. */
    fun use(account: String) {
        if (account != this.account) clear()
        this.account = account
    }

    /** Forgets everything, e.g. on logout. */
    fun clear() {
        account = null
        Drafts.clear()
        Outbox.clear()
        Starts.clear()
        Answers.clear()
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

    fun save(sid: String, o: Outgoing, original: Boolean) {
        val f = folder(sid, o.requestId)
        saveImages(files, f, o.images, original)
        files.put(f, "text", o.text.toByteArray())
        files.put(f, "ids", o.imageIds.joinToString("\n").toByteArray())
        files.put(f, "mode", o.mode.name.toByteArray())
        val line = "$sid\t${o.requestId}"
        val idx = index()
        if (line !in idx) writeIndex(idx + line)
    }

    fun load(sid: String, thumb: (ByteArray) -> androidx.compose.ui.graphics.ImageBitmap? = { null }): List<Outgoing> =
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

    fun remove(sid: String, requestId: String) {
        files.clearFolder(folder(sid, requestId))
        writeIndex(index().filter { it != "$sid\t$requestId" })
    }
}

/** The latest state of each message by request ID, from a session's events. */
fun inputStates(events: List<io.github.lewin671.remoteagent.proto.Event>): Map<String, io.github.lewin671.remoteagent.proto.InputStatus> {
    val out = HashMap<String, io.github.lewin671.remoteagent.proto.InputStatus>()
    for (e in events) if (e.kindCase == io.github.lewin671.remoteagent.proto.Event.KindCase.INPUT_STATE) out[e.inputState.requestId] = e.inputState.status
    return out
}

/** The messages the CLI has recorded: the ones to let go of. */
fun recorded(list: List<Outgoing>, inputs: Map<String, io.github.lewin671.remoteagent.proto.InputStatus>): List<Outgoing> =
    list.filter { inputs[it.requestId] == io.github.lewin671.remoteagent.proto.InputStatus.INPUT_STATUS_SUBMITTED }

/** Messages sent from this device, per session, for this run of the app (and those with images, from before it). */
object Outbox {
    private val bySession = HashMap<String, androidx.compose.runtime.snapshots.SnapshotStateList<Outgoing>>()
    private val records get() = OutgoingRecords(io.github.lewin671.remoteagent.RaApp.instance.pendingStore)

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
     */
    fun sendWithImages(
        scope: CoroutineScope, remote: Remote, sid: String, text: String,
        imageIds: List<String>, images: List<PendingImage>, original: Boolean,
        mode: io.github.lewin671.remoteagent.proto.SendMode = io.github.lewin671.remoteagent.proto.SendMode.SEND_MODE_STEER,
        store: (String, Outgoing, Boolean) -> Unit = { s, o, orig -> records.save(s, o, orig) },
        onKept: (Result<Unit>) -> Unit,
    ) {
        val o = Outgoing(newRequestId(), text, imageIds, images, original, mode)
        scope.launch {
            val kept = kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.IO) { runCatching { store(sid, o, original) } }
            onKept(kept)
            if (kept.isFailure) return@launch
            of(sid) += o
            watch(remote, sid)
            deliver(scope, remote, sid, o)
        }
    }

    private val watching = HashSet<String>()

    /**
     * Follows a session's events while messages with images wait here, and
     * lets them go once the CLI has recorded them, whether or not the chat
     * is on screen.
     */
    fun watch(remote: Remote, sid: String) {
        if (!watching.add(sid)) return
        io.github.lewin671.remoteagent.RaApp.instance.scope.launch {
            try {
                remote.events(sid).collect { ev ->
                    settle(sid, inputStates(ev.events))
                    if (of(sid).none { it.images.isNotEmpty() }) throw kotlinx.coroutines.CancellationException("nothing left to settle")
                }
            } finally {
                watching.remove(sid)
            }
        }
    }

    fun deliver(scope: CoroutineScope, remote: Remote, sid: String, o: Outgoing) {
        o.state = Outgoing.State.SENDING
        o.error = ""
        scope.launch {
            try {
                val machine = remote.state.value.sessions[sid]?.machineId?.let { remote.state.value.machines[it] }
                val wireMode = io.github.lewin671.remoteagent.core.wireSendMode(o.mode, io.github.lewin671.remoteagent.core.capabilities(machine)?.sendModes == true)
                remote.command(sid, remote.cmd {
                    send = SendRequest.newBuilder().setSessionId(sid).setText(o.text).setRequestId(o.requestId).addAllImageIds(o.imageIds)
                        .apply { if (wireMode != io.github.lewin671.remoteagent.proto.SendMode.SEND_MODE_UNSPECIFIED) mode = wireMode }.build()
                })
                o.state = Outgoing.State.SENT
            } catch (e: Exception) {
                o.error = errorText(e)
                o.state = if ((e as? CommandException)?.unknown == true) Outgoing.State.UNKNOWN else Outgoing.State.FAILED
            }
        }
    }

    /** Drops a message from here for good: its kept images and their bytes go too. */
    fun remove(sid: String, o: Outgoing) {
        of(sid).remove(o)
        if (o.images.isNotEmpty()) runCatching { records.remove(sid, o.requestId) }
    }

    /** Takes a message back for the message box: its record goes, its images live on in the draft. */
    fun takeBack(sid: String, o: Outgoing) {
        of(sid).remove(o)
        if (o.images.isNotEmpty()) runCatching { records.remove(sid, o.requestId) }
    }

    /**
     * Lets go of messages the CLI has recorded: their images' bytes,
     * thumbnails and picker grants, and the copies on disk. The chat shows
     * the images from the session's events from then on.
     */
    fun settle(sid: String, inputs: Map<String, io.github.lewin671.remoteagent.proto.InputStatus>) {
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
        watching.clear()
        prompts.clear()
        drafts.clear()
    }

    /** Local text of a request, for queued messages. */
    fun text(sid: String, requestId: String): String? = bySession[sid]?.firstOrNull { it.requestId == requestId }?.text
}
