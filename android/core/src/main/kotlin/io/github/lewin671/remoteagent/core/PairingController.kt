package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.proto.SecurityPairingInfo
import io.github.lewin671.remoteagent.core.proto.SecurityPairingReplacementCandidate
import io.github.lewin671.remoteagent.core.proto.Text
import io.github.lewin671.remoteagent.proto.DeviceKind
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch

sealed interface PairingUiState {
    data object Starting : PairingUiState
    data class Waiting(val code: String, val url: String, val expiresMs: Long) : PairingUiState
    data class Compare(
        val emoji: List<String>,
        val kind: DeviceKind,
        val replacementCandidates: List<SecurityPairingReplacementCandidate> = emptyList(),
        val selectedRemoveIds: Set<String> = emptySet(),
        val replaceOldLabel: Text? = null,
        val replaceOldHint: Text? = null,
    ) : PairingUiState
    data class Sending(val emoji: List<String>, val kind: DeviceKind) : PairingUiState
    data class Done(val warning: Text? = null) : PairingUiState
    data class Failed(val message: MessageText, val uncertain: Boolean, val canRetry: Boolean) : PairingUiState
}

/**
 * Retained by the account on its UI dispatcher. Delegates all pairing choreography and
 * state delivery to Core `SecurityClient` while preserving foreground pause / background finish semantics.
 */
class PairingController(
    private val scope: CoroutineScope,
    private val operationScope: CoroutineScope = scope,
    private val client: SecurityClient,
    private val webOrigin: String,
    private val replacesDeviceId: String = "",
    private val onCompleted: suspend () -> Unit = {},
) {
    private val mutable = MutableStateFlow<PairingUiState>(PairingUiState.Starting)
    val state = mutable.asStateFlow()
    private var generation = 0
    private var pairingId: String? = null
    private var consent: Triple<List<String>, DeviceKind, List<String>>? = null
    private var removeSelectionTouched = false

    fun enter() {
        if (pairingId == null && mutable.value == PairingUiState.Starting) start()
    }

    fun start() {
        val run = ++generation
        val cancellingId = if (consent == null) pairingId else null
        if (cancellingId != null) pairingId = null
        removeSelectionTouched = false
        mutable.value = PairingUiState.Starting
        scope.launch {
            try {
                if (cancellingId != null) {
                    runCatching { client.cancelPairing(cancellingId) }
                }
                val info = client.startPairing(webOrigin, replacesDeviceId)
                if (run != generation) {
                    operationScope.launch { runCatching { client.cancelPairing(info.pairingId) } }
                    return@launch
                }
                pairingId = info.pairingId
                mutable.value = PairingUiState.Waiting(info.code, info.url, info.expiresMs)
                var cur: SecurityPairingInfo = info
                while (isActive && run == generation && !cur.joined) {
                    delay(1_000L)
                    if (run != generation) return@launch
                    cur = client.pollPairing(info.pairingId)
                }
                if (run == generation && cur.joined) {
                    val kind = cur.joinerKind
                    mutable.value = compareState(cur, currentSelected = null)
                    var metaPolls = 0
                    while (isActive && run == generation && consent == null && !cur.joinerMetaReceived && metaPolls < 15) {
                        delay(1_000L)
                        if (run != generation || consent != null) return@launch
                        metaPolls++
                        val next = runCatching { client.pollPairing(info.pairingId) }.getOrNull() ?: break
                        cur = next
                        val prev = mutable.value as? PairingUiState.Compare ?: break
                        mutable.value = compareState(cur, currentSelected = if (removeSelectionTouched) prev.selectedRemoveIds else null)
                    }
                }
            } catch (e: CancellationException) {
                if (run == generation && consent == null) {
                    mutable.value = PairingUiState.Failed(
                        UiMessages.securityAndroidPausedRetryAfterReturningToTheForeground().messageText(),
                        uncertain = false,
                        canRetry = false,
                    )
                }
                throw e
            } catch (e: Exception) {
                if (run == generation && consent == null) {
                    val retryable = (e as? SecurityWorkflowError)?.retryable == true
                    val uncertain = (e as? SecurityWorkflowError)?.uncertain == true
                    mutable.value = PairingUiState.Failed(errorText(e), uncertain, retryable)
                }
            }
        }
    }

    private fun compareState(cur: SecurityPairingInfo, currentSelected: Set<String>?): PairingUiState.Compare {
        val candidates = cur.replacementCandidatesList.toList()
        val allowedIds = candidates.map { it.deviceId }.toSet()
        val selected = currentSelected?.intersect(allowedIds)
            ?: candidates.filter { it.defaultSelected }.map { it.deviceId }.toSet()
        return PairingUiState.Compare(
            emoji = cur.sasEmojiList.toList(),
            kind = cur.joinerKind,
            replacementCandidates = candidates,
            selectedRemoveIds = selected,
            replaceOldLabel = if (cur.hasReplaceOldLabel()) cur.replaceOldLabel else null,
            replaceOldHint = if (cur.hasReplaceOldHint()) cur.replaceOldHint else null,
        )
    }

    fun toggleRemoveCandidate(deviceId: String, checked: Boolean) {
        val cur = mutable.value as? PairingUiState.Compare ?: return
        removeSelectionTouched = true
        val next = if (checked) cur.selectedRemoveIds + deviceId else cur.selectedRemoveIds - deviceId
        mutable.value = cur.copy(selectedRemoveIds = next)
    }

    fun approve() {
        val comparing = mutable.value as? PairingUiState.Compare ?: return
        val removeIds = comparing.replacementCandidates
            .filter { it.deviceId in comparing.selectedRemoveIds }
            .map { it.deviceId }
        consent = Triple(comparing.emoji, comparing.kind, removeIds)
        sendApproval()
    }

    fun retry() {
        val failure = mutable.value as? PairingUiState.Failed ?: return
        if (failure.canRetry && pairingId != null && consent != null) sendApproval() else {
            consent = null
            start()
        }
    }

    private fun sendApproval() {
        val id = pairingId ?: return
        val (emoji, kind, removeIds) = consent ?: return
        val run = generation
        mutable.value = PairingUiState.Sending(emoji, kind)
        operationScope.launch {
            try {
                var res = client.approvePairing(id, sasConfirmed = true, removeDeviceIds = removeIds)
                while (!res.delivered) {
                    delay(1_200L)
                    res = client.finishPairingDelivery(id)
                }
                if (run == generation) {
                    pairingId = null
                    consent = null
                    mutable.value = PairingUiState.Done(
                        warning = if (res.hasRemoveOldNotice()) res.removeOldNotice else null,
                    )
                }
                runCatching { onCompleted() }
            } catch (e: CancellationException) {
                if (run == generation) {
                    mutable.value = PairingUiState.Failed(
                        UiMessages.securityAndroidOutcomeUnconfirmedRetryTheSameApprovalAfterReturningToThe().messageText(),
                        uncertain = true,
                        canRetry = true,
                    )
                }
                throw e
            } catch (e: Exception) {
                if (run == generation) {
                    val se = e as? SecurityWorkflowError
                    val uncertain = se?.uncertain ?: true
                    val retryable = se?.retryable ?: true
                    mutable.value = PairingUiState.Failed(errorText(e), uncertain, retryable)
                }
            }
        }
    }

    fun leave() {
        if (consent != null) return
        generation++
        val id = pairingId
        pairingId = null
        mutable.value = PairingUiState.Starting
        if (id != null) {
            operationScope.launch { runCatching { client.cancelPairing(id) } }
        }
    }
}
