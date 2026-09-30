package io.github.lewin671.remoteagent.ui

import androidx.compose.runtime.mutableStateMapOf
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.errorText
import io.github.lewin671.remoteagent.proto.CancelInputRequest
import kotlinx.coroutines.launch

/**
 * Cancels asked from here, per queued message, for this run. The daemon's
 * InputState says how each ended; this only holds what is in flight and why
 * a try failed, so the pending bubble can say so.
 */
object Cancels {
    sealed interface State {
        data object Canceling : State
        data class Failed(val error: String) : State
    }

    val states = mutableStateMapOf<String, State>()

    fun cancel(remote: Remote, sid: String, requestId: String) {
        if (states[requestId] == State.Canceling) return
        states[requestId] = State.Canceling
        RaApp.instance.scope.launch {
            try {
                remote.command(sid, remote.cmd { cancelInput = CancelInputRequest.newBuilder().setSessionId(sid).setRequestId(requestId).build() })
                states.remove(requestId)
            } catch (e: Exception) {
                // A failed_precondition can race with delivery; if the input
                // remains pending, keep a retry affordance until its next event.
                states[requestId] = State.Failed(errorText(e))
            }
        }
    }

    fun clear() {
        states.clear()
    }
}
