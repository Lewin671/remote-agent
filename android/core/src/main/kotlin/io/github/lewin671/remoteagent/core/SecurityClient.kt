package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.proto.Event as CoreEvent
import io.github.lewin671.remoteagent.core.proto.SecurityActionEmpty
import io.github.lewin671.remoteagent.core.proto.SecurityApprovePairingAction
import io.github.lewin671.remoteagent.core.proto.SecurityBoolAction
import io.github.lewin671.remoteagent.core.proto.SecurityErrorCode
import io.github.lewin671.remoteagent.core.proto.SecurityPairingInfo
import io.github.lewin671.remoteagent.core.proto.SecurityPrepareCreationAction
import io.github.lewin671.remoteagent.core.proto.SecurityRemoveDeviceAction
import io.github.lewin671.remoteagent.core.proto.SecurityStartJoinPairingAction
import io.github.lewin671.remoteagent.core.proto.SecurityStartPairingAction
import io.github.lewin671.remoteagent.core.proto.SecurityStringAction
import io.github.lewin671.remoteagent.core.proto.SecuritySyncNowAction
import io.github.lewin671.remoteagent.core.proto.SecurityUpdateDeviceMetaAction
import io.github.lewin671.remoteagent.core.proto.SecurityWorkflowActionEvent
import io.github.lewin671.remoteagent.core.proto.SecurityWorkflowResultEffect
import io.github.lewin671.remoteagent.core.proto.SecurityWorkflowStatus
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.atomic.AtomicLong

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

    suspend fun prepareCreation(server: String, storagePersisted: Boolean = true): String =
        run {
            setPrepareCreation(
                SecurityPrepareCreationAction.newBuilder().setServer(server).setStoragePersisted(storagePersisted).build(),
            )
        }.recoveryCode

    suspend fun confirmCreationCodeSaved() { empty { setConfirmCreationCodeSaved(it) } }

    suspend fun removeDevice(deviceId: String) {
        run {
            setRemoveDevice(
                SecurityRemoveDeviceAction.newBuilder().setDeviceId(deviceId).build(),
            )
        }
    }

    suspend fun deleteMachineRecords(machineId: String) { str(machineId) { setDeleteMachineRecords(it) } }

    suspend fun prepareHandoffCode(storagePersisted: Boolean = true): String =
        bool(storagePersisted) { setPrepareHandoffCode(it) }.recoveryCode

    suspend fun publishHandoffOffer() { empty { setPublishHandoffOffer(it) } }

    suspend fun handoffAuthority(targetDeviceId: String) { str(targetDeviceId) { setHandoffAuthority(it) } }

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

    fun setForeground(active: Boolean) {
        setForegroundFn(active)
    }
}
