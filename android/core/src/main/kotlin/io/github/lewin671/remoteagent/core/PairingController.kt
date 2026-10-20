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
    data class Failed(val message: String, val uncertain: Boolean, val canRetry: Boolean) : PairingUiState
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
        operationScope.launch {
            try {
                pairing.approve()
                if (run == generation) { current = null; consent = null; mutable.value = PairingUiState.Done }
            } catch (e: CancellationException) {
                if (run == generation) mutable.value = PairingUiState.Failed("结果尚未确定，回到前台后重试同一次批准", true, true)
                throw e
            }
            catch (e: Exception) {
                if (run == generation) {
                    val uncertain = e !is PairException || e.message.orEmpty().let { "不确定" in it || "可能" in it || "无法核实" in it }
                    val retryable = canRetryPairApproval(e)
                    val message = if (e is PairException) words(e)
                        else if (retryable) "暂时无法确认新设备是否已加入，请检查网络后重试同一次批准，或在新设备上查看结果"
                        else "服务器未接受这次批准，请在新设备核对是否已加入，再重新开始"
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

    private fun words(e: Exception) = if (e is PairException) e.message.orEmpty() else "配对暂时无法完成，请检查网络后重新开始"
}
