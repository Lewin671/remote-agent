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
    data class Waiting(val code: String, val link: PairLink) : PairingUiState
    data class Compare(val emoji: List<String>, val kind: DeviceKind) : PairingUiState
    data class Sending(val emoji: List<String>, val kind: DeviceKind) : PairingUiState
    data object Done : PairingUiState
    data class Failed(val message: String, val uncertain: Boolean, val canRetry: Boolean) : PairingUiState
}

/** A view owns this on its UI dispatcher. Network failures retain the exact
 * initiation/consent. Only start (explicit replacement) or leave withdraws it.
 * cleanupScope must outlive the view so disposing it can still cancel on server. */
class PairingController(
    private val scope: CoroutineScope,
    private val cleanupScope: CoroutineScope = scope,
    private val begin: suspend () -> PairingInitiation,
) {
    private val mutable = MutableStateFlow<PairingUiState>(PairingUiState.Starting)
    val state = mutable.asStateFlow()
    private var generation = 0
    private var current: PairingInitiation? = null
    private var consent: Pair<List<String>, DeviceKind>? = null

    fun start() {
        val run = ++generation
        val withdrawing = withdraw()
        mutable.value = PairingUiState.Starting
        scope.launch {
            try {
                withdrawing?.join()
                val pairing = begin()
                if (run != generation) { cleanupScope.launch { pairing.cancelOnServer() }; return@launch }
                current = pairing
                mutable.value = PairingUiState.Waiting(pairing.code, pairing.link)
                val (emoji, kind) = pairing.joined()
                if (run == generation) mutable.value = PairingUiState.Compare(emoji, kind)
            } catch (e: CancellationException) {
                if (run == generation) mutable.value = PairingUiState.Failed("已暂停，回到前台后可重试", false, false)
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
        scope.launch {
            try {
                pairing.approve()
                if (run == generation) { current = null; consent = null; mutable.value = PairingUiState.Done }
            } catch (e: CancellationException) {
                if (run == generation) mutable.value = PairingUiState.Failed("结果尚未确定，回到前台后重试同一次批准", true, true)
                throw e
            }
            catch (e: Exception) {
                if (run == generation) {
                    val uncertain = e !is PairException || e.message.orEmpty().let { "不确定" in it || "可能" in it }
                    val message = if (e is PairException) words(e) else "不确定新设备收到没有，到新设备上看一眼是否已加入：" + words(e)
                    mutable.value = PairingUiState.Failed(message, uncertain, canRetryPairApproval(e))
                }
            }
        }
    }

    fun leave() { generation++; withdraw() }

    private fun withdraw(): Job? {
        val old = current
        current = null; consent = null
        return old?.let { cleanupScope.launch { it.cancelOnServer() } }
    }

    private fun words(e: Exception) = if (e is PairException) e.message.orEmpty() else errorText(e)
}
