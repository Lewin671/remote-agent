package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.proto.Effect
import io.github.lewin671.remoteagent.core.proto.EffectBatch
import io.github.lewin671.remoteagent.core.proto.EntropyProvided
import io.github.lewin671.remoteagent.core.proto.Event as CoreEvent
import io.github.lewin671.remoteagent.core.proto.SecurityActionEmpty
import io.github.lewin671.remoteagent.core.proto.SecurityApprovePairingAction
import io.github.lewin671.remoteagent.core.proto.SecurityBoolAction
import io.github.lewin671.remoteagent.core.proto.SecurityConfigEvent
import io.github.lewin671.remoteagent.core.proto.SecurityErrorCode
import io.github.lewin671.remoteagent.core.proto.SecurityExitAdvice
import io.github.lewin671.remoteagent.core.proto.SecurityForegroundEvent
import io.github.lewin671.remoteagent.core.proto.SecurityHandoffCandidate
import io.github.lewin671.remoteagent.core.proto.SecurityPairingInfo
import io.github.lewin671.remoteagent.core.proto.SecurityPrepareCreationAction
import io.github.lewin671.remoteagent.core.proto.SecurityRecoveryInfo
import io.github.lewin671.remoteagent.core.proto.SecurityRemoveDeviceAction
import io.github.lewin671.remoteagent.core.proto.SecurityServerCodeAction
import io.github.lewin671.remoteagent.core.proto.SecurityStartJoinPairingAction
import io.github.lewin671.remoteagent.core.proto.SecurityStartPairingAction
import io.github.lewin671.remoteagent.core.proto.SecurityStringAction
import io.github.lewin671.remoteagent.core.proto.SecuritySyncNowAction
import io.github.lewin671.remoteagent.core.proto.SecurityTokenUpdatedEffect
import io.github.lewin671.remoteagent.core.proto.SecurityUpdateDeviceMetaAction
import io.github.lewin671.remoteagent.core.proto.SecurityWorkflowActionEvent
import io.github.lewin671.remoteagent.core.proto.SecurityWorkflowResultEffect
import io.github.lewin671.remoteagent.core.proto.SecurityWorkflowStatus
import io.github.lewin671.remoteagent.core.proto.TimerFiredEvent
import io.github.lewin671.remoteagent.corebind.Core
import io.github.lewin671.remoteagent.corebind.Corebind
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.asCoroutineDispatcher
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import okhttp3.OkHttpClient
import java.net.URLDecoder
import java.net.URLEncoder
import java.security.SecureRandom
import java.util.Base64
import java.util.UUID
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.Executors
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicLong

/** Light QR/deep-link query parser for UI prefill; all cryptographic verification runs in Go clientcore. */
data class PairLink(val code: String, val server: String, val commit: ByteArray? = null) {
    fun url(web: String): String {
        val q = buildList {
            add("code=" + enc(code))
            commit?.let {
                add("c=" + Base64.getUrlEncoder().withoutPadding().encodeToString(it))
            }
            add("s=" + enc(server))
        }.joinToString("&")
        return web.trimEnd('/') + "/#/pair?" + q
    }

    override fun equals(other: Any?): Boolean {
        if (this === other) return true
        if (other !is PairLink) return false
        return code == other.code && server == other.server && commit.contentEquals(other.commit)
    }

    override fun hashCode(): Int {
        var result = code.hashCode()
        result = 31 * result + server.hashCode()
        result = 31 * result + (commit?.contentHashCode() ?: 0)
        return result
    }

    companion object {
        private fun enc(s: String) = URLEncoder.encode(s, "UTF-8")

        fun parse(raw: String): PairLink? {
            val i = raw.indexOf("#/pair?")
            if (i < 0) return null
            return try {
                val q = raw.substring(i + 7)
                    .split("&")
                    .mapNotNull {
                        val kv = it.split("=", limit = 2)
                        if (kv.size == 2) kv[0] to URLDecoder.decode(kv[1], "UTF-8") else null
                    }
                    .toMap()
                val code = q["code"] ?: return null
                val server = q["s"] ?: return null
                val commit = q["c"]?.let { Base64.getUrlDecoder().decode(it.trimEnd('=')) }
                if (!Regex("[0-9]{8}").matches(code) || (commit != null && commit.size != 32)) {
                    null
                } else {
                    PairLink(code, Secret.checkServer(server, allowPlainHttp = true), commit)
                }
            } catch (_: IllegalArgumentException) {
                null
            }
        }
    }
}

fun securityErrorCodeKey(code: SecurityErrorCode): String = when (code) {
    SecurityErrorCode.SECURITY_ERROR_CODE_UNTRUSTED -> "untrusted"
    SecurityErrorCode.SECURITY_ERROR_CODE_STALE -> "stale"
    SecurityErrorCode.SECURITY_ERROR_CODE_LEASE_EXPIRED -> "lease"
    SecurityErrorCode.SECURITY_ERROR_CODE_CLOCK_SKEWED -> "clock"
    SecurityErrorCode.SECURITY_ERROR_CODE_CAPACITY -> "capacity"
    SecurityErrorCode.SECURITY_ERROR_CODE_RECOVERY_UNAVAILABLE -> "unavailable"
    SecurityErrorCode.SECURITY_ERROR_CODE_UNSUPPORTED_HISTORY -> "unsupported_history"
    SecurityErrorCode.SECURITY_ERROR_CODE_STORAGE -> "storage"
    SecurityErrorCode.SECURITY_ERROR_CODE_CONFLICT -> "conflict"
    SecurityErrorCode.SECURITY_ERROR_CODE_PERMISSION_DENIED -> "permission_denied"
    SecurityErrorCode.SECURITY_ERROR_CODE_NOT_FOUND -> "not_found"
    SecurityErrorCode.SECURITY_ERROR_CODE_FAILED_PRECONDITION -> "failed_precondition"
    SecurityErrorCode.SECURITY_ERROR_CODE_RESOURCE_EXHAUSTED -> "resource_exhausted"
    SecurityErrorCode.SECURITY_ERROR_CODE_CANCELED -> "canceled"
    SecurityErrorCode.SECURITY_ERROR_CODE_UNAVAILABLE -> "unavailable"
    SecurityErrorCode.SECURITY_ERROR_CODE_FOREGROUND -> "foreground"
    SecurityErrorCode.SECURITY_ERROR_CODE_INVALID,
    SecurityErrorCode.SECURITY_ERROR_CODE_UNSPECIFIED,
    SecurityErrorCode.UNRECOGNIZED -> "invalid"
}

class SecurityWorkflowError(
    message: String,
    val code: SecurityErrorCode,
    val retryable: Boolean,
    val uncertain: Boolean,
    val errorAction: String,
) : Exception(message), MessageFailure {
    override val display: MessageText = message.messageText()
}

fun translateSecurityErrorMessage(
    res: SecurityWorkflowResultEffect,
    resolver: MessageResolver,
): String {
    if (res.hasErrorText()) {
        val rendered = coreTextToMessageText(res.errorText).resolve(resolver)
        if (rendered.isNotEmpty()) return rendered
    }
    return if (res.structuredErrorCode != SecurityErrorCode.SECURITY_ERROR_CODE_UNSPECIFIED) {
        securityErrorCodeKey(res.structuredErrorCode)
    } else {
        ""
    }
}

fun renderExitAdviceMessage(
    advice: SecurityExitAdvice,
    resolver: MessageResolver,
): String {
    if (advice.hasMessageText()) {
        return coreTextToMessageText(advice.messageText).resolve(resolver)
    }
    return ""
}

data class PreparedCreationResult(
    val recoveryCode: String,
    val resumedDraft: Boolean,
    val codeSaved: Boolean,
)

data class PreparedEmergencyRecoveryResult(
    val recoveryCode: String,
    val recovery: SecurityRecoveryInfo?,
)

/**
 * Promise/Deferred client for all S6 `SecurityWorkflowActionEvent` / `SecurityWorkflowResultEffect`
 * round-trips hosted in Go `clientcore`.
 */
class SecurityClient(
    private val dispatchEvent: (CoreEvent) -> Unit,
    private val setForegroundFn: (Boolean) -> Unit,
    private val isClosedFn: () -> Boolean,
    private val onDismissUpgradeNotice: (() -> Unit)? = null,
    private val messageResolver: MessageResolver = { it.key },
) {
    private val nextActionId = AtomicLong(1L)
    private val waiters = ConcurrentHashMap<Long, CompletableDeferred<SecurityWorkflowResultEffect>>()
    private val _status = MutableStateFlow<SecurityWorkflowStatus?>(null)
    val status: StateFlow<SecurityWorkflowStatus?> = _status.asStateFlow()

    val currentStatus: SecurityWorkflowStatus? get() = _status.value

    fun handleWorkflowResult(res: SecurityWorkflowResultEffect) {
        if (res.hasStatus()) {
            _status.value = res.status
        }
        val deferred = waiters.remove(res.actionId) ?: return
        if (!res.ok) {
            val msg = translateSecurityErrorMessage(res, messageResolver)
            deferred.completeExceptionally(
                SecurityWorkflowError(
                    message = msg,
                    code = res.structuredErrorCode,
                    retryable = res.retryable,
                    uncertain = res.uncertain,
                    errorAction = res.errorAction,
                ),
            )
            return
        }
        deferred.complete(res)
    }

    fun handleStatusChanged(status: SecurityWorkflowStatus) {
        _status.value = status
    }

    fun rejectAll(reason: String = "security client closed") {
        val entries = waiters.entries.toList()
        for ((id, d) in entries) {
            if (waiters.remove(id, d)) {
                d.completeExceptionally(IllegalStateException(reason))
            }
        }
    }

    private suspend fun run(
        configure: SecurityWorkflowActionEvent.Builder.() -> Unit,
    ): SecurityWorkflowResultEffect {
        if (isClosedFn()) throw IllegalStateException("security client closed")
        val actionId = nextActionId.getAndIncrement()
        val deferred = CompletableDeferred<SecurityWorkflowResultEffect>()
        waiters[actionId] = deferred
        try {
            val ev = CoreEvent.newBuilder()
                .setSecurityAction(
                    SecurityWorkflowActionEvent.newBuilder()
                        .setActionId(actionId)
                        .apply(configure)
                        .build(),
                )
                .build()
            dispatchEvent(ev)
            if (isClosedFn() && !deferred.isCompleted) {
                waiters.remove(actionId, deferred)
                throw IllegalStateException("security client closed")
            }
            return deferred.await()
        } catch (t: Throwable) {
            waiters.remove(actionId, deferred)
            throw t
        }
    }

    private suspend fun empty(set: SecurityWorkflowActionEvent.Builder.(SecurityActionEmpty) -> Unit) =
        run { set(SecurityActionEmpty.getDefaultInstance()) }

    private suspend fun bool(v: Boolean, set: SecurityWorkflowActionEvent.Builder.(SecurityBoolAction) -> Unit) =
        run { set(SecurityBoolAction.newBuilder().setFlag(v).build()) }

    private suspend fun str(v: String, set: SecurityWorkflowActionEvent.Builder.(SecurityStringAction) -> Unit) =
        run { set(SecurityStringAction.newBuilder().setValue(v).build()) }

    suspend fun bootstrap(): SecurityWorkflowStatus = empty { setBootstrap(it) }.status

    suspend fun getStatus(): SecurityWorkflowStatus = empty { setGetStatus(it) }.status

    suspend fun dismissUpgradeNotice(): SecurityWorkflowStatus =
        empty { setDismissUpgradeNotice(it) }.status.also { onDismissUpgradeNotice?.invoke() }

    suspend fun syncNow(touch: Boolean = false, authToken: String = ""): SecurityWorkflowStatus =
        run { setSyncNow(SecuritySyncNowAction.newBuilder().setTouch(touch).setAuthToken(authToken).build()) }.status

    suspend fun renewLease(force: Boolean = true): SecurityWorkflowStatus = bool(force) { setRenewLease(it) }.status

    suspend fun resumePendingOp(): SecurityWorkflowResultEffect = empty { setResumePendingOp(it) }

    suspend fun discardPendingOp(abandonHandoff: Boolean = false) {
        bool(abandonHandoff) { setDiscardPendingOp(it) }
    }

    suspend fun prepareCreation(server: String, storagePersisted: Boolean = true): PreparedCreationResult =
        run {
            setPrepareCreation(
                SecurityPrepareCreationAction.newBuilder().setServer(server).setStoragePersisted(storagePersisted).build(),
            )
        }.let { PreparedCreationResult(recoveryCode = it.recoveryCode, resumedDraft = it.resumedDraft, codeSaved = it.codeSaved) }

    suspend fun confirmCreationCodeSaved() { empty { setConfirmCreationCodeSaved(it) } }

    suspend fun continueCreation(setupToken: String = ""): SecurityWorkflowResultEffect =
        str(setupToken) { setContinueCreation(it) }

    suspend fun adoptCreationWithoutCode(): SecurityWorkflowResultEffect =
        empty { setAdoptCreationWithoutCode(it) }

    suspend fun removeDevice(deviceId: String) {
        run {
            setRemoveDevice(
                SecurityRemoveDeviceAction.newBuilder().setDeviceId(deviceId).build(),
            )
        }
    }

    suspend fun deleteMachineRecords(machineId: String) { str(machineId) { setDeleteMachineRecords(it) } }

    suspend fun deleteAccount() { empty { setDeleteAccount(it) } }

    suspend fun prepareRecoveryRotation(): String = empty { setPrepareRecoveryRotation(it) }.recoveryCode

    suspend fun confirmRecoveryRotation() { empty { setConfirmRecoveryRotation(it) } }

    suspend fun prepareHandoffCode(storagePersisted: Boolean = true): String =
        bool(storagePersisted) { setPrepareHandoffCode(it) }.recoveryCode

    suspend fun publishHandoffOffer() { empty { setPublishHandoffOffer(it) } }

    suspend fun listHandoffCandidates(): List<SecurityHandoffCandidate> =
        empty { setListHandoffCandidates(it) }.handoffCandidatesList

    suspend fun handoffAuthority(targetDeviceId: String) { str(targetDeviceId) { setHandoffAuthority(it) } }

    suspend fun completeAccountPublicProfile(server: String, code: String) {
        run { setCompleteAccountPublicProfile(SecurityServerCodeAction.newBuilder().setServer(server).setCode(code).build()) }
    }

    suspend fun startPairing(webOrigin: String, replacesDeviceId: String = ""): SecurityPairingInfo =
        run { setStartPairing(SecurityStartPairingAction.newBuilder().setWebOrigin(webOrigin).setReplacesDeviceId(replacesDeviceId).build()) }.pairing

    suspend fun pollPairing(pairingId: String): SecurityPairingInfo = str(pairingId) { setPollPairing(it) }.pairing

    suspend fun approvePairing(
        pairingId: String,
        sasConfirmed: Boolean,
        removeDeviceIds: List<String> = emptyList(),
    ): SecurityPairingInfo =
        run {
            setApprovePairing(
                SecurityApprovePairingAction.newBuilder()
                    .setPairingId(pairingId)
                    .setSasConfirmed(sasConfirmed)
                    .addAllRemoveDeviceIds(removeDeviceIds)
                    .build(),
            )
        }.pairing

    suspend fun finishPairingDelivery(pairingId: String): SecurityPairingInfo =
        str(pairingId) { setFinishPairingDelivery(it) }.pairing

    suspend fun cancelPairing(pairingId: String) { str(pairingId) { setCancelPairing(it) } }

    suspend fun startJoinPairing(
        server: String = "",
        code: String = "",
        commitHint: ByteArray? = null,
        existingRepair: Boolean = false,
        existingServer: String = "",
        rawLink: String = "",
        platform: String = "",
        description: String = "",
    ): SecurityPairingInfo =
        run {
            setStartJoinPairing(
                SecurityStartJoinPairingAction.newBuilder()
                    .setServer(server).setCode(code)
                    .apply { if (commitHint != null && commitHint.isNotEmpty()) setCommitHint(ByteString.copyFrom(commitHint)) }
                    .setExistingRepair(existingRepair).setExistingServer(existingServer).setRawLink(rawLink)
                    .setPlatform(platform).setDescription(description).build(),
            )
        }.pairing

    suspend fun confirmJoinSas(pairingId: String): SecurityWorkflowResultEffect = str(pairingId) { setConfirmJoinSas(it) }

    suspend fun cancelJoinPairing(pairingId: String) { str(pairingId) { setCancelJoinPairing(it) } }

    suspend fun prepareEmergencyRecovery(server: String, code: String): PreparedEmergencyRecoveryResult =
        run { setPrepareEmergencyRecovery(SecurityServerCodeAction.newBuilder().setServer(server).setCode(code).build()) }
            .let { PreparedEmergencyRecoveryResult(recoveryCode = it.recoveryCode, recovery = if (it.hasRecovery()) it.recovery else null) }

    suspend fun confirmRecoveryCodeSaved() { empty { setConfirmRecoveryCodeSaved(it) } }

    suspend fun continueRecoverySigning(oldCode: String): SecurityRecoveryInfo? =
        str(oldCode) { setContinueRecoverySigning(it) }.let { if (it.hasRecovery()) it.recovery else null }

    suspend fun resumeRecovery(): SecurityRecoveryInfo? =
        empty { setResumeRecovery(it) }.let { if (it.hasRecovery()) it.recovery else null }

    suspend fun completeRecovery(): SecurityWorkflowResultEffect = empty { setCompleteRecovery(it) }

    suspend fun discardUnsignedRecovery() { empty { setDiscardUnsignedRecovery(it) } }

    suspend fun discardRejectedRecovery() { empty { setDiscardRejectedRecovery(it) } }

    suspend fun checkExitAdvice(): SecurityExitAdvice = empty { setCheckExitAdvice(it) }.exitAdvice

    suspend fun clearSecurity() { empty { setClearSecurity(it) } }

    suspend fun checkRetainedDenied(): Boolean = empty { setCheckRetainedDenied(it) }.flag

    suspend fun updateDeviceMeta(
        deviceId: String = "",
        name: String = "",
        platform: String = "",
        description: String = "",
        describeSelfOnly: Boolean = false,
    ) {
        run {
            setUpdateDeviceMeta(
                SecurityUpdateDeviceMetaAction.newBuilder()
                    .setDeviceId(deviceId).setName(name).setPlatform(platform)
                    .setDescription(description).setDescribeSelfOnly(describeSelfOnly).build(),
            )
        }
    }

    suspend fun republishRecoveryObjects(flag: Boolean = false) { bool(flag) { setRepublishRecoveryObjects(it) } }

    fun setForeground(active: Boolean) {
        setForegroundFn(active)
    }
}

/**
 * Hosts a standalone Go `Core` + [SecurityExecutor] for pre-auth and unauthenticated screens
 * (bootstrap, account creation, pairing join, emergency recovery, exit cleanup).
 */
class StandaloneSecurityHost(
    private val store: SecurityStoreV4,
    http: OkHttpClient,
    messageResolver: MessageResolver = { it.key },
    allowPlainHttp: Boolean = true,
    private val clock: () -> Long = System::currentTimeMillis,
    private val monotonic: () -> Long = System::currentTimeMillis,
    private val onToken: ((SecurityTokenUpdatedEffect) -> Unit)? = null,
) {
    private val closed = AtomicBoolean(false)
    private val workerExecutor = Executors.newSingleThreadExecutor { r ->
        Thread(r, "ra-standalone-sec").apply { isDaemon = true }
    }
    private val workerDispatcher = workerExecutor.asCoroutineDispatcher()
    private val scope = CoroutineScope(SupervisorJob() + workerDispatcher)
    private val rng = SecureRandom()
    private val timers = ConcurrentHashMap<String, Pair<Long, Job>>()
    private val core: Core = Corebind.new_()
    private val executor = SecurityExecutor(
        store = store,
        http = http,
        emit = { ev -> dispatch(ev) },
        parentScope = scope,
        allowPlainHttp = allowPlainHttp,
    )

    val client: SecurityClient = SecurityClient(
        dispatchEvent = { ev -> dispatch(ev) },
        setForegroundFn = { active ->
            if (!closed.get()) {
                dispatch(
                    CoreEvent.newBuilder()
                        .setSecurityForeground(SecurityForegroundEvent.newBuilder().setForeground(active))
                        .build(),
                )
            }
        },
        isClosedFn = { closed.get() },
        onDismissUpgradeNotice = { store.dismissUpgradeNotice() },
        messageResolver = messageResolver,
    )

    init {
        dispatch(
            CoreEvent.newBuilder()
                .setSecurityConfig(
                    SecurityConfigEvent.newBuilder()
                        .setInlineCrypto(true)
                        .setForeground(true),
                )
                .build(),
        )
    }

    fun dispatch(ev: CoreEvent) {
        if (closed.get()) return
        val stamped = ev.toBuilder().apply {
            if (nowMs == 0L) nowMs = clock()
            if (monoMs == 0L) monoMs = monotonic()
        }.build()
        scope.launch {
            if (closed.get()) return@launch
            dispatchOnWorker(stamped)
        }
    }

    private fun dispatchOnWorker(ev: CoreEvent) {
        val queue = ArrayDeque<CoreEvent>()
        queue.addLast(ev)
        while (queue.isNotEmpty() && !closed.get()) {
            val next = queue.removeFirst()
            val bytes: ByteArray? = core.dispatch(next.toByteArray())
            if (bytes == null || bytes.isEmpty()) continue
            val batch = io.github.lewin671.remoteagent.core.proto.EffectBatch.parseFrom(bytes)
            for (eff in batch.effectsList) {
                if (closed.get()) break
                handleEffect(eff, queue)
            }
        }
    }

    private fun handleEffect(eff: Effect, inlineQueue: ArrayDeque<CoreEvent>) {
        if (executor.handleEffect(eff)) return
        when (eff.effectCase) {
            Effect.EffectCase.REQUEST_ENTROPY -> {
                val req = eff.requestEntropy
                val idCount = maxOf(req.minIds, 16)
                val floatCount = maxOf(req.minFloats, 16)
                val byteCount = if (req.minRandomBytes > 0) maxOf(req.minRandomBytes, 4096) else 0
                val eb = EntropyProvided.newBuilder()
                repeat(idCount) { eb.addRandomIds(UUID.randomUUID().toString()) }
                repeat(floatCount) {
                    eb.addUnitFloats((rng.nextInt().toLong() and 0xFFFFFFFFL).toDouble() / 4294967296.0)
                }
                if (byteCount > 0) {
                    val buf = ByteArray(byteCount)
                    rng.nextBytes(buf)
                    eb.setRandomBytes(ByteString.copyFrom(buf))
                }
                inlineQueue.addLast(
                    CoreEvent.newBuilder().setNowMs(clock()).setMonoMs(monotonic()).setEntropyProvided(eb).build(),
                )
            }
            Effect.EffectCase.SCHEDULE_TIMER -> {
                val st = eff.scheduleTimer
                val tk = "${st.kind}:${st.key}"
                timers.remove(tk)?.second?.cancel()
                val job = scope.launch {
                    val delayMs = maxOf(0L, st.delayMs)
                    if (delayMs > 0L) delay(delayMs)
                    val cur = timers[tk] ?: return@launch
                    if (cur.first != st.timerId) return@launch
                    timers.remove(tk, cur)
                    if (!closed.get()) {
                        dispatchOnWorker(
                            CoreEvent.newBuilder()
                                .setNowMs(clock()).setMonoMs(monotonic())
                                .setTimerFired(TimerFiredEvent.newBuilder().setTimerId(st.timerId).setKind(st.kind).setKey(st.key))
                                .build(),
                        )
                    }
                }
                timers[tk] = st.timerId to job
            }
            Effect.EffectCase.CANCEL_TIMER -> {
                val ct = eff.cancelTimer
                val tk = "${ct.kind}:${ct.key}"
                val cur = timers[tk]
                if (cur != null && (ct.timerId == 0L || cur.first == ct.timerId)) {
                    timers.remove(tk, cur)
                    cur.second.cancel()
                }
            }
            Effect.EffectCase.SECURITY_TOKEN_UPDATED -> onToken?.invoke(eff.securityTokenUpdated)
            Effect.EffectCase.SECURITY_WORKFLOW_RESULT -> client.handleWorkflowResult(eff.securityWorkflowResult)
            Effect.EffectCase.SECURITY_STATUS_CHANGED ->
                if (eff.securityStatusChanged.hasStatus()) client.handleStatusChanged(eff.securityStatusChanged.status)
            else -> {}
        }
    }

    fun close() {
        if (!closed.compareAndSet(false, true)) return
        timers.values.forEach { it.second.cancel() }
        timers.clear()
        executor.close()
        client.rejectAll()
        scope.launch {
            runCatching { core.close() }
        }.invokeOnCompletion {
            scope.cancel()
            workerExecutor.shutdown()
        }
    }
}
