package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.DeviceKind
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch

sealed interface PairingUiState {
    data object Starting : PairingUiState
    data class Waiting(val code: String, val link: PairLink, val expiresMs: Long) : PairingUiState
    data class Compare(val emoji: List<String>, val kind: DeviceKind) : PairingUiState
    data class Sending(val emoji: List<String>, val kind: DeviceKind) : PairingUiState
    data object Done : PairingUiState
    data class Failed(val message: MessageText, val uncertain: Boolean, val canRetry: Boolean) : PairingUiState
}

/** Retained by the account on its UI dispatcher. Once consent is given, page
 * disposal must not withdraw a possibly published admission. Approval and delivery
 * use the account scope; Initiation guards every authority signature in foreground. */
class PairingController(
    private val scope: CoroutineScope,
    private val operationScope: CoroutineScope = scope,
    private val begin: suspend () -> PairingInitiation,
) {
    private val mutable = MutableStateFlow<PairingUiState>(PairingUiState.Starting)
    val state = mutable.asStateFlow()
    private var generation = 0
    private var current: PairingInitiation? = null
    private var consent: Pair<List<String>, DeviceKind>? = null

    /** Reopening a page keeps an in-flight approval and its eventual outcome. */
    fun enter() {
        if (current == null && mutable.value == PairingUiState.Starting) start()
    }

    fun start() {
        val run = ++generation
        val withdrawing = withdraw()
        mutable.value = PairingUiState.Starting
        scope.launch {
            try {
                withdrawing?.join()
                val pairing = begin()
                if (run != generation) { operationScope.launch { pairing.cancelOnServer() }; return@launch }
                current = pairing
                mutable.value = PairingUiState.Waiting(pairing.code, pairing.link, pairing.expiresMs)
                val (emoji, kind) = pairing.joined()
                if (run == generation) mutable.value = PairingUiState.Compare(emoji, kind)
            } catch (e: CancellationException) {
                if (run == generation) mutable.value = PairingUiState.Failed(UiMessages.securityAndroidPausedRetryAfterReturningToTheForeground().messageText(), false, false)
                throw e
            }
            catch (e: Exception) {
                if (run == generation) mutable.value = PairingUiState.Failed(words(e), false, false)
            }
        }
    }

    fun approve() {
        val comparing = mutable.value as? PairingUiState.Compare ?: return
        consent = comparing.emoji to comparing.kind
        sendApproval()
    }

    fun retry() {
        val failure = mutable.value as? PairingUiState.Failed ?: return
        if (failure.canRetry && current != null && consent != null) sendApproval() else start()
    }

    private fun sendApproval() {
        val pairing = current ?: return
        val (emoji, kind) = consent ?: return
        val run = generation
        mutable.value = PairingUiState.Sending(emoji, kind)
        operationScope.launch {
            try {
                pairing.approve()
                if (run == generation) { current = null; consent = null; mutable.value = PairingUiState.Done }
            } catch (e: CancellationException) {
                if (run == generation) mutable.value = PairingUiState.Failed(UiMessages.securityAndroidOutcomeUnconfirmedRetryTheSameApprovalAfterReturningToThe().messageText(), true, true)
                throw e
            }
            catch (e: Exception) {
                if (run == generation) {
                    val uncertain = e !is PairException || e.uncertain
                    val retryable = canRetryPairApproval(e)
                    val message = if (e is PairException) words(e)
                        else if (retryable) UiMessages.securityAndroidCannotConfirmWhetherTheNewDeviceJoinedCheckYourNetwork().messageText()
                        else UiMessages.securityAndroidTheServerDidNotAcceptThisApprovalCheckWhetherThe().messageText()
                    mutable.value = PairingUiState.Failed(message, uncertain, retryable)
                }
            }
        }
    }

    fun leave() {
        if (consent != null) return
        generation++
        withdraw()
        mutable.value = PairingUiState.Starting
    }

    private fun withdraw(): Job? {
        val old = current
        current = null; consent = null
        return old?.let { operationScope.launch { it.cancelOnServer() } }
    }

    private fun words(e: Exception) = if (e is PairException) e.display else UiMessages.securityAndroidPairingCannotFinishRightNowCheckYourNetworkThenStart().messageText()
}
