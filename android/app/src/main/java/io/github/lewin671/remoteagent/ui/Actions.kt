package io.github.lewin671.remoteagent.ui

import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableLongStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.launch

/** One action with its progress: running, and the error of the last try. */
class Action(private val scope: CoroutineScope) {
    var running by mutableStateOf(false)
        private set
    var error by mutableStateOf("")

    fun run(block: suspend () -> Unit, onDone: () -> Unit = {}) {
        if (running) return
        running = true
        error = ""
        scope.launch {
            try {
                block()
                onDone()
            } catch (e: Exception) {
                error = displayError(e)
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

/**
 * Lag-free Compose text state synchronized with Core's [AttachmentDraftView.text] and
 * [AttachmentDraftView.textRevision]:
 * - Keeps keystrokes responsive without waiting for an async Core round-trip.
 * - Drops echoed Core renders matching in-flight local edits so fast typing never loses characters.
 * - Immediately reflects Core-initiated changes (disk restore, send clear, put-back merge).
 */
@Composable
internal fun rememberDraftTextState(
    scopeKey: String,
    coreText: String,
    coreRevision: Long,
    onEdit: (String) -> Unit,
): Pair<String, (String) -> Unit> {
    var localText by remember(scopeKey) { mutableStateOf(coreText) }
    var lastCoreText by remember(scopeKey) { mutableStateOf(coreText) }
    var lastCoreRev by remember(scopeKey) { mutableLongStateOf(coreRevision) }
    val pendingEdits = remember(scopeKey) { ArrayDeque<String>() }

    LaunchedEffect(scopeKey, coreText, coreRevision) {
        val coreChanged = coreText != lastCoreText || coreRevision != lastCoreRev
        lastCoreText = coreText
        lastCoreRev = coreRevision
        if (!coreChanged) return@LaunchedEffect
        val idx = pendingEdits.indexOf(coreText)
        if (idx >= 0) {
            repeat(idx + 1) { pendingEdits.removeFirst() }
        } else {
            pendingEdits.clear()
            localText = coreText
        }
    }

    val update: (String) -> Unit = { next ->
        if (localText != next) {
            localText = next
            pendingEdits.addLast(next)
            onEdit(next)
        }
    }
    return localText to update
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
        DraftUiNotices.clear()
        ViewerImages.clear()
    }
}
