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
    }
}

/** Messages sent from this device, per session, for this run of the app. */
object Outbox {
    private val bySession = HashMap<String, androidx.compose.runtime.snapshots.SnapshotStateList<Outgoing>>()

    fun of(sid: String) = bySession.getOrPut(sid) { mutableStateListOf() }

    fun send(scope: CoroutineScope, remote: Remote, sid: String, text: String, imageIds: List<String> = emptyList(), images: List<PendingImage> = emptyList()) {
        val o = Outgoing(newRequestId(), text, imageIds, images)
        of(sid) += o
        deliver(scope, remote, sid, o)
    }

    fun deliver(scope: CoroutineScope, remote: Remote, sid: String, o: Outgoing) {
        o.state = Outgoing.State.SENDING
        o.error = ""
        scope.launch {
            try {
                remote.command(sid, remote.cmd { send = SendRequest.newBuilder().setSessionId(sid).setText(o.text).setRequestId(o.requestId).addAllImageIds(o.imageIds).build() })
                o.state = Outgoing.State.SENT
            } catch (e: Exception) {
                o.error = errorText(e)
                o.state = if ((e as? CommandException)?.unknown == true) Outgoing.State.UNKNOWN else Outgoing.State.FAILED
            }
        }
    }

    fun remove(sid: String, o: Outgoing) {
        of(sid).remove(o)
    }

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
        prompts.clear()
        drafts.clear()
    }

    /** Local text of a request, for queued messages. */
    fun text(sid: String, requestId: String): String? = bySession[sid]?.firstOrNull { it.requestId == requestId }?.text
}
