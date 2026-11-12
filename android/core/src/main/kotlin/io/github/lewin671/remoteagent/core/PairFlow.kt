// Pairing flows, as in internal/relay/pair.go and web/src/pairing.ts. Only the
// authority client initiates and approves; a joiner scans or types the code,
// both compare four emoji, and the phone signs the new member state.
package io.github.lewin671.remoteagent.core

import com.connectrpc.Code
import com.connectrpc.ConnectException
import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.proto.ApprovePairingRequest
import io.github.lewin671.remoteagent.proto.CancelPairingRequest
import io.github.lewin671.remoteagent.proto.CreatePairingRequest
import io.github.lewin671.remoteagent.proto.DeviceKind
import io.github.lewin671.remoteagent.proto.GetPairingRequest
import io.github.lewin671.remoteagent.proto.GetPairingResponse
import io.github.lewin671.remoteagent.proto.Header
import io.github.lewin671.remoteagent.proto.JoinPairingRequest
import io.github.lewin671.remoteagent.proto.PairApproval
import io.github.lewin671.remoteagent.proto.PairingPayload
import io.github.lewin671.remoteagent.proto.PairingState
import io.github.lewin671.remoteagent.proto.PayloadKind
import io.github.lewin671.remoteagent.proto.PeekPairingRequest
import io.github.lewin671.remoteagent.proto.RevealPairingRequest
import io.github.lewin671.remoteagent.proto.SecurityState
import java.net.URLDecoder
import java.net.URLEncoder
import java.util.Base64
import kotlinx.coroutines.delay
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import okhttp3.OkHttpClient
import org.bouncycastle.crypto.params.X25519PrivateKeyParameters

class PairException(message: String, val retryable: Boolean = true, val uncertain: Boolean = false) : Exception(message), MessageFailure {
    override var display: MessageText = message.messageText()
        private set
    constructor(message: MessageText, retryable: Boolean = true, uncertain: Boolean = false) : this("pairing_failure", retryable, uncertain) { display = message }
}

/** The approval binds this account, pairing, candidate, approver and expiry, as Go verifies it. */
internal fun validJoinApproval(
    approval: PairApproval,
    raw: ByteArray,
    signature: ByteArray,
    account: String,
    pairingId: String,
    approver: String,
    candidate: ByteArray,
    now: Long,
): Boolean =
    raw.size <= 1024 &&
        approval.accountId == account &&
        approval.pairingId == pairingId &&
        approval.candidateDevicePublicKey.toByteArray().contentEquals(candidate) &&
        approval.candidateKind == DeviceKind.DEVICE_KIND_CLIENT &&
        approval.replacesDeviceId.isEmpty() &&
        approval.approverDeviceId == approver &&
        approval.approverPublicKey.size() == 32 &&
        approval.nonce.size() == 16 &&
        approval.expiresMs > now &&
        deviceId(approval.approverPublicKey.toByteArray()) == approval.approverDeviceId &&
        approval.approverDeviceId != deviceId(candidate) &&
        verifyEd25519(
            approval.approverPublicKey.toByteArray(),
            "remote-agent/v1/pair-approval\u0000".toByteArray() + raw,
            signature,
        )

/** A single pairing operation, retained after uncertain approval outcomes. */
interface PairingInitiation {
    val code: String
    val expiresMs: Long
    val link: PairLink

    suspend fun joined(): Pair<List<String>, DeviceKind>

    suspend fun approve()

    suspend fun cancelOnServer()
}

fun canRetryPairApproval(error: Exception): Boolean {
    if (
        error is ConnectException &&
            error.code in
                setOf(
                    Code.NOT_FOUND,
                    Code.PERMISSION_DENIED,
                    Code.FAILED_PRECONDITION,
                    Code.INVALID_ARGUMENT,
                    Code.CANCELED,
                )
    )
        return false
    return (error as? PairException)?.retryable ?: true
}

/** What a QR code carries: the code, the initiator's commitment and the server. */
data class PairLink(val code: String, val server: String, val commit: ByteArray? = null) {
    fun url(web: String): String {
        val q = buildList {
            add("code=" + enc(code))
            commit?.let {
                add("c=" + Base64.getUrlEncoder().withoutPadding().encodeToString(it))
            }
            add("s=" + enc(server))
        }
            .joinToString("&")
        return web.trimEnd('/') + "/#/pair?" + q
    }

    companion object {
        private fun enc(s: String) = URLEncoder.encode(s, "UTF-8")

        fun parse(raw: String): PairLink? {
            val i = raw.indexOf("#/pair?")
            if (i < 0) return null
            try {
                val q =
                    raw.substring(i + 7)
                        .split("&")
                        .mapNotNull {
                            val kv = it.split("=", limit = 2)
                            if (kv.size == 2) kv[0] to URLDecoder.decode(kv[1], "UTF-8") else null
                        }
                        .toMap()
                val code = q["code"] ?: return null
                val server = q["s"] ?: return null
                val commit = q["c"]?.let { Base64.getUrlDecoder().decode(it.trimEnd('=')) }
                if (!Regex("[0-9]{8}").matches(code) || (commit != null && commit.size != 32))
                    return null
                return PairLink(code, Secret.checkServer(server, allowPlainHttp = true), commit)
            } catch (e: IllegalArgumentException) {
                return null
            }
        }
    }
}

private suspend fun poll(
    stopped: () -> Boolean,
    want: PairingState,
    intervalMs: Long,
    get: suspend () -> GetPairingResponse,
): GetPairingResponse {
    while (true) {
        if (stopped()) throw PairException(UiMessages.queueCanceled().messageText(), retryable = false)
        val res =
            try {
                get()
            } catch (e: ConnectException) {
                if (e.code == Code.NOT_FOUND) throw PairException(UiMessages.securityPairingExpiredOrWasCanceled().messageText(), retryable = false)
                throw e
            }
        if (res.state == PairingState.PAIRING_STATE_CANCELED) throw PairException(UiMessages.securityPairingCanceled().messageText(), retryable = false)
        if (res.stateValue >= want.number) return res
        delay(intervalMs)
    }
}

/** Adding a device from the authority client. */
class Initiation
internal constructor(
    override val code: String,
    override val link: PairLink,
    private val remote: Remote,
    private val pairingId: String,
    private val key: PairKey,
    override val expiresMs: Long,
    private val replacesDeviceId: String,
    private val pollIntervalMs: Long,
) : PairingInitiation {
    @Volatile private var stopped = false
    private var secrets: PairSecrets? = null
    private var candidateSeed: ByteArray? = null
    private var candidateX: ByteArray? = null
    private var candidateKind: DeviceKind? = null
    private val approvalLock = Mutex()
    private var approvalRequest: ApprovePairingRequest? = null

    override suspend fun joined(): Pair<List<String>, DeviceKind> {
        if (remote.security.head().core.authorityID != remote.account.deviceId)
            throw PairException(UiMessages.securityAndroidOnlyTheManagingClientCanAddDevices().messageText())
        val tok = remote.currentToken()
        val st =
            poll({ stopped }, PairingState.PAIRING_STATE_JOINED, pollIntervalMs) {
                remote.api.pairing
                    .getPairing(
                        GetPairingRequest.newBuilder().setPairingId(pairingId).build(),
                        Api.headers(tok),
                    )
                    .value()
            }
        val (pin, _) = remote.security.identity()
        val descriptor = DeviceSecurityRPC.pairTrustDescriptor(pin, remote.account.deviceId)
        val cp = st.devicePublicKey.toByteArray()
        val xp = st.deviceHpkePublicKey.toByteArray()
        requireMessage(cp.size == 32 && xp.size == 32 && st.joinerKindValue in 1..2) { UiMessages.securityAndroidTheCandidateDeviceNeedsAnUpdate().messageText() }
        remote.api.pairing
            .revealPairing(
                RevealPairingRequest.newBuilder()
                    .setPairingId(pairingId)
                    .setInitiatorPublic(ByteString.copyFrom(key.public))
                    .setTrust(ByteString.copyFrom(descriptor))
                    .build(),
                Api.headers(tok),
            )
            .value()
        if (replacesDeviceId.isNotEmpty() && st.joinerKind != DeviceKind.DEVICE_KIND_MACHINE)
            throw PairException(UiMessages.securityAndroidThisPairingOnlyAllowsTheOriginalComputerToRejoin().messageText())
        candidateSeed = cp
        candidateX = xp
        candidateKind = st.joinerKind
        val tr =
            Pairing.domainTranscript(
                pairingId,
                key.public,
                st.joinerPublic.toByteArray(),
                cp,
                xp,
                st.joinerKindValue,
                descriptor,
            )
        val s = key.derive(st.joinerPublic.toByteArray(), tr)
        secrets = s
        return s.emoji to st.joinerKind
    }

    /** A missing pairing does not prove failed registration: the receipt may be lost.
     * Resolve that separately before presenting a definite result to the user. */
    override suspend fun approve() {
        try {
            approveOnce()
        } catch (e: ConnectException) {
            if (e.code == Code.FAILED_PRECONDITION) {
                val status = try {
                    remote.api.pairing.getPairing(
                        GetPairingRequest.newBuilder().setPairingId(pairingId).build(),
                        Api.headers(remote.currentToken()),
                    ).value()
                } catch (statusError: ConnectException) {
                    if (statusError.code != Code.NOT_FOUND) throw statusError
                    resolveClosedPairing()
                    return
                }
                if (status.state != PairingState.PAIRING_STATE_CANCELED) throw e
            } else if (e.code !in setOf(Code.NOT_FOUND, Code.CANCELED)) throw e
            resolveClosedPairing()
        } catch (e: ClosedPairing) {
            resolveClosedPairing()
        }
    }

    private class ClosedPairing : Exception()

    private suspend fun resolveClosedPairing() {
        val candidate = candidateSeed?.let { deviceId(it) }
        val registered = try {
            remote.api.client.listDevices(
                io.github.lewin671.remoteagent.proto.ListDevicesRequest.getDefaultInstance(),
                Api.headers(remote.currentToken()),
            ).value().devicesList.any { it.id == candidate }
        } catch (e: kotlinx.coroutines.CancellationException) {
            throw e
        } catch (e: Exception) {
            throw PairException(UiMessages.securityAndroidThisPairingIsNoLongerValidCannotVerifyWhetherThe().messageText(), retryable = false, uncertain = true)
        }
        if (registered) {
            // Install and verify the published chain before clearing its durable intent.
            // syncSecurity() would also replay the now-missing pairing.
            remote.security.sync()
            remote.finishPairingOperation(pairingId)
            return
        }
        throw PairException(UiMessages.securityAndroidThisPairingIsNoLongerValidTheNewDeviceDid().messageText(), retryable = false)
    }

    /** Signs in foreground, uploads once, then waits without further signing. */
    private suspend fun approveOnce() {
        val s = secrets ?: throw PairException(UiMessages.securityAndroidNoDeviceHasJoinedYet().messageText())
        val acct = remote.account
        val cp = candidateSeed ?: throw PairException(UiMessages.securityAndroidNoCandidateDevice().messageText())
        if (deviceId(cp) == acct.deviceId)
            throw PairException(UiMessages.securityAndroidTheCandidateDeviceCannotUseTheManagingClientSIdentity().messageText())
        val tok = remote.currentToken()
        val request = approvalLock.withLock {
            approvalRequest
                ?: remote.pendingPairingRequest(pairingId)
                ?: run {
                    try {
                        remote.syncSecurity()
                    } catch (e: ConnectException) {
                        // This can be replay of a previous pairing's durable intent;
                        // it says nothing about the current candidate's registration.
                        if (e.code !in setOf(Code.NOT_FOUND, Code.CANCELED, Code.FAILED_PRECONDITION)) throw e
                        throw PairException(UiMessages.securityAndroidMembershipSyncFailedThereMayBeAnInvalidPairingCheck().messageText(), retryable = false, uncertain = true)
                    }
                    val xp = candidateX ?: throw PairException(UiMessages.securityAndroidNoCandidateDevice().messageText())
                    val kind = candidateKind ?: throw PairException(UiMessages.securityAndroidNoCandidateDevice().messageText())
                    val now = remote.security.signingTime(System.currentTimeMillis())
                    val head = remote.security.head()
                    if (head.core.authorityID != acct.deviceId) throw PairException(UiMessages.securityAndroidOnlyTheManagingClientCanApprove().messageText())
                    val old =
                        remote.security.contentKey(head.core.contentEpoch)
                            ?: throw PairException(UiMessages.securityAndroidCurrentContentKeyMissing().messageText())
                    val member = DeviceSecurity.Member(deviceId(cp), cp, xp, kind.number.toUInt())
                    val existing = head.core.member(member.id)
                    if (existing == null && member.id in head.core.removed)
                        throw PairException(UiMessages.securityAndroidThisDeviceWasRemovedItsOldIdentityCannotRejoinRejoin().messageText())
                    remote.authorityForeground()
                    val next =
                        if (existing == null)
                            Authority.pair(
                                head,
                                member,
                                old,
                                Authority.newSecret(),
                                SigningKey(acct.deviceSeed),
                                now,
                            )
                        else {
                            // Re-pairing keeps the Ed25519 identity and replaces X.
                            if (
                                existing.kind != member.kind ||
                                    !existing.ed.contentEquals(member.ed)
                            )
                                throw PairException(UiMessages.securityAndroidThisDeviceSIdentityDiffersFromItsMemberRecordCannot().messageText())
                            Authority.rotateX(
                                head,
                                member.id,
                                xp,
                                old,
                                Authority.newSecret(),
                                SigningKey(acct.deviceSeed),
                                now,
                            )
                        }
                    if (acct.accountPublicKey.size != 32)
                        throw PairException(UiMessages.securityAndroidThisPhoneHasNoAccountPublicKeyAndCannotRegister().messageText())
                    val chain = remote.security.chain() + next
                    val proofs = remote.security.chainProofs()
                    if (chain.size > DeviceSecurity.MAX_PAIR_STATES)
                        throw PairException(UiMessages.securityAndroidStateChainIsTooLargeToPairRecreateTheAccount().messageText())
                    val rawSize =
                        chain.sumOf { DeviceSecurity.stateBytes(it).size.toLong() } +
                            proofs.values.sumOf { it.size.toLong() }
                    if (rawSize > DeviceSecurity.MAX_PAIR_CHAIN_BYTES)
                        throw PairException(UiMessages.securityAndroidStateChainIsTooLargeToPairRecreateTheAccount().messageText())
                    val certificate = HeadLease.sign(next, SigningKey(acct.deviceSeed), now)
                    val (approval, signature) =
                        makePairApproval(acct, pairingId, cp, kind, expiresMs, replacesDeviceId)
                    val payload =
                        PairingPayload.newBuilder()
                            .setServer(acct.server)
                            .setApproval(ByteString.copyFrom(approval))
                            .setApprovalSignature(ByteString.copyFrom(signature))
                            .addAllSecurityStates(
                                chain.map { state ->
                                    SecurityState.newBuilder()
                                        .setState(
                                            ByteString.copyFrom(
                                                DeviceSecurity.stateBytes(state),
                                            ),
                                        )
                                        .apply {
                                            proofs[state.core.epoch]?.let {
                                                setRecovery(ByteString.copyFrom(it))
                                            }
                                        }
                                        .build()
                                },
                            )
                            .setAccountPublicKey(ByteString.copyFrom(acct.accountPublicKey))
                            .setLease(ByteString.copyFrom(certificate.bytes()))
                            .build()
                    val env =
                        Envelopes.seal(
                            s.key,
                            Header.newBuilder()
                                .setKind(PayloadKind.PAYLOAD_KIND_PAIRING)
                                .setRequestId(pairingId)
                                .setSender(acct.deviceId)
                                .build(),
                            payload,
                        )
                    ApprovePairingRequest.newBuilder()
                        .setPairingId(pairingId)
                        .setPayload(env)
                        .setApproval(ByteString.copyFrom(approval))
                        .setApprovalSignature(ByteString.copyFrom(signature))
                        .build()
                        .also {
                            remote.publishSecurityStates(
                                listOf(next),
                                delivery = it.toByteArray(),
                                certificate = certificate,
                            )
                        }
                }
                    .also { approvalRequest = it }
        }
        remote.api.pairing.approvePairing(request, Api.headers(tok)).value()
        while (System.currentTimeMillis() < expiresMs) {
            if (stopped) throw ClosedPairing()
            try {
                val res =
                    remote.api.pairing
                        .getPairing(
                            GetPairingRequest.newBuilder().setPairingId(pairingId).build(),
                            Api.headers(tok),
                        )
                        .value()
                if (res.state == PairingState.PAIRING_STATE_DELIVERED) {
                    remote.finishPairingOperation(pairingId)
                    return
                }
                if (res.state == PairingState.PAIRING_STATE_CANCELED)
                    throw ClosedPairing()
            } catch (e: ConnectException) {
                if (e.code in setOf(Code.NOT_FOUND, Code.CANCELED)) throw e
                if (e.code !in setOf(Code.UNAVAILABLE, Code.DEADLINE_EXCEEDED, Code.UNKNOWN)) throw e
            }
            delay(pollIntervalMs)
        }
        resolveClosedPairing()
    }

    fun cancel() {
        stopped = true
    }

    override suspend fun cancelOnServer() {
        stopped = true
        try {
            remote.api.pairing.cancelPairing(
                CancelPairingRequest.newBuilder().setPairingId(pairingId).build(),
                Api.headers(remote.currentToken()),
            )
        } catch (e: Exception) {
            // The pairing expires by itself.
        }
    }

    companion object {
        /** Starts adding a device; only the authority client may. */
        suspend fun start(
            remote: Remote,
            replacesDeviceId: String = "",
            pollIntervalMs: Long = 1000,
        ): Initiation {
            require(pollIntervalMs > 0)
            if (remote.security.head().core.authorityID != remote.account.deviceId)
                throw PairException(UiMessages.securityAndroidOnlyTheManagingClientCanAddDevices().messageText())
            if (replacesDeviceId.isNotEmpty()) {
                val head = remote.security.head()
                if (
                    head.core.member(replacesDeviceId) != null ||
                        replacesDeviceId !in head.core.removed ||
                        remote.security.chain().none {
                            it.core.member(replacesDeviceId)?.kind == 1u
                        }
                )
                    throw PairException(UiMessages.securityAndroidOnlyARemovedComputerSArchiveFromTheSameAccount().messageText())
            }
            val current = remote.security.chain()
            val currentBytes =
                current.sumOf { DeviceSecurity.stateBytes(it).size.toLong() } +
                    remote.security.chainProofs().values.sumOf { it.size.toLong() }
            if (
                current.size >= DeviceSecurity.MAX_PAIR_STATES ||
                    currentBytes >= DeviceSecurity.MAX_PAIR_CHAIN_BYTES
            )
                throw PairException(UiMessages.securityAndroidStateChainIsTooLargeToPairRecreateTheAccount().messageText())
            val key = PairKey()
            val identity = remote.security.identity()
            val commit =
                DeviceSecurityRPC.pairDomainCommit(
                    key.public,
                    DeviceSecurityRPC.pairTrustDescriptor(identity.first, remote.account.deviceId),
                )
            val created =
                remote.api.pairing
                    .createPairing(
                        CreatePairingRequest.newBuilder()
                            .setCommit(ByteString.copyFrom(commit))
                            .build(),
                        Api.headers(remote.currentToken()),
                    )
                    .value()
            return Initiation(
                created.code,
                PairLink(created.code, remote.account.server, commit),
                remote,
                created.pairingId,
                key,
                created.expiresMs,
                replacesDeviceId,
                pollIntervalMs,
            )
        }
    }
}

/**
 * Joins an account with a code or link from the authority client. [onEmoji] gets the emoji to
 * compare with the other device's.
 */
suspend fun joinByPairing(
    link: PairLink,
    http: OkHttpClient,
    onEmoji: (List<String>) -> Unit,
    stopped: () -> Boolean,
    onRegistering: () -> Unit = {},
    persist: suspend (Account, DeviceSecurity.Record) -> Unit = { _, _ ->
        throw DeviceSecurity.Failure("storage")
    },
    persistRegistration: (RegistrationIntent) -> Unit = { throw DeviceSecurity.Failure("storage") },
    clearRegistration: (RegistrationIntent) -> Unit = {},
    loadIdentity: (String, String) -> PairingIdentity? = { _, _ -> null },
    persistIdentity: (PairingIdentity) -> Unit = { throw DeviceSecurity.Failure("storage") },
    existing: Account? = null,
    existingRemoved: Boolean = false,
    confirm: suspend (List<String>) -> Boolean = { false },
    pollIntervalMs: Long = 1000,
): Account {
    require(pollIntervalMs > 0)
    if (existing != null && existing.server != link.server) throw PairException(UiMessages.securitySecuritySetupMustUseTheOriginalAccountSServer().messageText())
    val api = Api(link.server, http)
    val removed =
        if (existing != null) existingIdentityRemoved(api, existing) || existingRemoved else false
    val key = PairKey()
    // A removed device's identity is revoked; it rejoins with a fresh one.
    val peek =
        api.pairing
            .peekPairing(
                PeekPairingRequest.newBuilder()
                    .setCode(link.code.filterNot { it.isWhitespace() })
                    .setProtocolVersion(PROTOCOL_VERSION)
                    .build(),
                emptyMap(),
            )
            .value()
    if (
        peek.commit.size() != 32 ||
            peek.pairingId.isEmpty() ||
            link.commit != null && !link.commit.contentEquals(peek.commit.toByteArray())
    )
        throw PairException(UiMessages.securityAndroidPairingCommitmentMismatch().messageText(), retryable = false)
    val identity = loadIdentity(link.server, peek.pairingId)
    val deviceSeed =
        identity?.seed ?: if (existing != null && !removed) existing.deviceSeed else randomBytes(32)
    if (
        identity != null &&
            (identity.server != link.server ||
                identity.pairingId != peek.pairingId ||
                (existing != null && !removed && !identity.seed.contentEquals(existing.deviceSeed)))
    )
        throw DeviceSecurity.Failure("conflict")
    val device = SigningKey(deviceSeed)
    val devicePublic = device.publicKey
    val xPrivate = identity?.x ?: randomBytes(32)
    val xPublic = X25519PrivateKeyParameters(xPrivate, 0).generatePublicKey().encoded
    persistIdentity(PairingIdentity(link.server, peek.pairingId, deviceSeed, xPrivate))
    val joined =
        try {
            api.pairing
                .joinPairing(
                    JoinPairingRequest.newBuilder()
                        .setProtocolVersion(PROTOCOL_VERSION)
                        .setDeviceHpkePublicKey(ByteString.copyFrom(xPublic))
                        .setCode(link.code.filterNot { it.isWhitespace() })
                        .setJoinerPublic(ByteString.copyFrom(key.public))
                        .setJoinerKind(DeviceKind.DEVICE_KIND_CLIENT)
                        .setDevicePublicKey(ByteString.copyFrom(devicePublic))
                        .build(),
                    emptyMap(),
                )
                .value()
        } catch (e: ConnectException) {
            if (
                e.code == Code.NOT_FOUND ||
                    e.code == Code.INVALID_ARGUMENT ||
                    e.code == Code.FAILED_PRECONDITION
            )
                throw PairException(UiMessages.securityThePairingCodeIsWrongOrHasAlreadyBeen().messageText())
            if (e.code == Code.RESOURCE_EXHAUSTED) throw PairException(UiMessages.securityAndroidTooManyAttemptsWaitAMinuteAndRetry().messageText())
            throw e
        }
    if (joined.pairingId != peek.pairingId || !joined.commit.equals(peek.commit))
        throw PairException(UiMessages.securityAndroidPairingCommitmentChangedAfterTheEphemeralPublicKeyWasRevealed().messageText())
    val id = joined.pairingId
    val token = joined.candidateToken
    val get: suspend () -> GetPairingResponse = {
        api.pairing
            .getPairing(
                GetPairingRequest.newBuilder().setPairingId(id).setCandidateToken(token).build(),
                emptyMap(),
            )
            .value()
    }
    val st = poll(stopped, PairingState.PAIRING_STATE_REVEALED, pollIntervalMs, get)
    if (st.trust.size() > 8192) throw PairException(UiMessages.securityAndroidPairingTrustInformationIsTooLarge().messageText())
    val (pin, approver) =
        try {
            DeviceSecurityRPC.parsePairTrustDescriptor(st.trust.toByteArray())
        } catch (_: Exception) {
            throw PairException(UiMessages.securityInvalidPairingTrustInformation().messageText(), retryable = false)
        }
    if (
        !DeviceSecurityRPC.pairDomainCommit(
                st.initiatorPublic.toByteArray(),
                st.trust.toByteArray(),
            )
            .contentEquals(peek.commit.toByteArray())
    )
        throw PairException(UiMessages.securityAndroidTheManagingClientSTrustInformationDoesNotMatchIts().messageText(), retryable = false)
    val tr =
        Pairing.domainTranscript(
            id,
            st.initiatorPublic.toByteArray(),
            key.public,
            devicePublic,
            xPublic,
            DeviceKind.DEVICE_KIND_CLIENT.number,
            st.trust.toByteArray(),
        )
    val s = key.derive(st.initiatorPublic.toByteArray(), tr)
    onEmoji(s.emoji)
    if (stopped() || !confirm(s.emoji)) throw PairException(UiMessages.securityAndroidPatternsWereNotConfirmedOnThisDeviceSecureBindingStopped().messageText())
    onRegistering()
    val done = poll(stopped, PairingState.PAIRING_STATE_APPROVED, pollIntervalMs, get)
    val (_, payload) =
        Envelopes.openMessage(
            s.key,
            done.payload,
            Place(PayloadKind.PAYLOAD_KIND_PAIRING, requestId = id),
            PairingPayload::parseFrom,
        )
    if (payload.server != link.server) throw PairException(UiMessages.securityTheAccountBelongsToAnotherServer().messageText())
    if (stopped()) throw PairException(UiMessages.securityAndroidThisJoinAttemptWasCancelled().messageText(), retryable = false)
    val accountPublicKey = payload.accountPublicKey.toByteArray()
    if (accountPublicKey.size != 32 || deviceId(accountPublicKey) != pin.account)
        throw PairException(UiMessages.securityAndroidPairingPayloadIsMissingTheAccountPublicKey().messageText())
    val chain = ArrayList<DeviceSecurity.State>()
    val proofs = ArrayList<ByteArray?>()
    for (inState in payload.securityStatesList) {
        val state = DeviceSecurity.parseState(inState.state.toByteArray())
        chain.add(state)
        proofs.add(inState.recovery.takeIf { it.size() > 0 }?.toByteArray())
    }
    if (chain.isEmpty() || chain.size > DeviceSecurity.MAX_STATES)
        throw PairException(UiMessages.securityAndroidPairingPayloadStateChainIsIncomplete().messageText())
    if (chain.size > DeviceSecurity.MAX_PAIR_STATES) throw PairException(UiMessages.securityAndroidStateChainIsTooLargeToPairRecreateTheAccount().messageText())
    val chainBytes =
        chain.sumOf { DeviceSecurity.stateBytes(it).size.toLong() } +
            proofs.filterNotNull().sumOf { it.size.toLong() }
    if (chainBytes > DeviceSecurity.MAX_PAIR_CHAIN_BYTES) throw PairException(UiMessages.securityAndroidStateChainIsTooLargeToPairRecreateTheAccount().messageText())
    if (
        !DeviceSecurity.pinOf(chain[0]).let {
            it.account == pin.account &&
                it.domain.contentEquals(pin.domain) &&
                it.genesis.contentEquals(pin.genesis)
        }
    )
        throw PairException(UiMessages.securityAndroidPairingPayloadBelongsToAnotherSecurityDomain().messageText())
    val now = System.currentTimeMillis()
    chain.forEachIndexed { i, state ->
        DeviceSecurity.verifyState(pin, state)
        DeviceSecurity.requireNotFromFuture(state, now.toULong())
        if (i > 0) {
            val proof = proofs[i]?.let { DeviceSecurity.parseRecoveryStatement(it) }
            DeviceSecurity.verifyTransition(chain[i - 1], state, proof)
        }
    }
    if (chain.last().core.authorityID != approver) throw PairException(UiMessages.securityAndroidPairingPayloadManagingClientMismatch().messageText(), retryable = false)
    val member =
        chain.last().core.member(deviceId(devicePublic)) ?: throw PairException(UiMessages.securityAndroidJoinReceiptDoesNotContainThisDeviceSIdentity().messageText())
    if (
        !member.ed.contentEquals(devicePublic) ||
            !member.x.contentEquals(xPublic) ||
            member.kind != 2u
    )
        throw PairException(UiMessages.securityAndroidJoinReceiptDevicePublicKeyMismatch().messageText(), retryable = false)
    DeviceSecurity.openGrant(pin, chain.last(), member.id, xPrivate)
    if (payload.approval.size() > 1024) throw PairException(UiMessages.securityAndroidPairingApprovalDoesNotMatchThisDeviceOrItsSignature().messageText(), retryable = false)
    val approval = PairApproval.parseFrom(payload.approval)
    if (
        !validJoinApproval(
            approval,
            payload.approval.toByteArray(),
            payload.approvalSignature.toByteArray(),
            pin.account,
            id,
            approver,
            devicePublic,
            now,
        )
    )
        throw PairException(UiMessages.securityAndroidPairingApprovalDoesNotMatchThisDeviceOrItsSignature().messageText(), retryable = false)
    val acct =
        Account(
            link.server,
            existing?.root ?: ByteArray(0),
            deviceSeed,
            deviceId(devicePublic),
            pin.account,
            accountPublicKey,
        )
    val record =
        DeviceSecurity.Record.fromChain(
            pin.account,
            pin.domain,
            pin.genesis,
            acct.deviceId,
            xPrivate,
            chain.map { DeviceSecurity.stateBytes(it) },
            proofs,
        )
    val installed =
        record.installed(emptyList(), emptyList(), HeadLease.parse(payload.lease.toByteArray()))
    val registration =
        io.github.lewin671.remoteagent.proto.DeviceRegistration.newBuilder()
            .setAccountPublicKey(com.google.protobuf.ByteString.copyFrom(accountPublicKey))
            .setDevicePublicKey(
                com.google.protobuf.ByteString.copyFrom(SigningKey(deviceSeed).publicKey),
            )
            .setKind(DeviceKind.DEVICE_KIND_CLIENT)
            .setCreatedMs(System.currentTimeMillis())
            .build()
    val request =
        io.github.lewin671.remoteagent.proto.RegisterDeviceRequest.newBuilder()
            .setProtocolVersion(PROTOCOL_VERSION)
            .setRegistration(registration.toByteString())
            .setApproval(payload.approval)
            .setApprovalSignature(payload.approvalSignature)
            .build()
    val intent = RegistrationIntent(acct, installed, request)
    persistRegistration(intent)
    val registered = intent.resume(http, persist)
    clearRegistration(intent)
    check(registered.deviceId == acct.deviceId)
    try {
        api.pairing.getPairing(
            GetPairingRequest.newBuilder()
                .setPairingId(id)
                .setCandidateToken(token)
                .setReceived(true)
                .build(),
            emptyMap(),
        )
    } catch (e: Exception) {
        // The join itself stands.
    }
    return acct
}

/** Only a definitive identity rejection permits replacing the retained signing key. */
suspend fun existingIdentityRemoved(api: Api, existing: Account): Boolean =
    try {
        login(api, existing)
        false
    } catch (e: ConnectException) {
        if (loginDenied(e)) true else throw e
    }

/** Startup only: a fresh challenge/login retries one denial within one total deadline. */
suspend fun probeRetainedIdentity(attempt: suspend () -> Unit): Boolean =
    kotlinx.coroutines.withTimeoutOrNull(5_000L) {
        try {
            try {
                attempt()
            } catch (e: ConnectException) {
                if (!loginDenied(e)) throw e
                kotlinx.coroutines.currentCoroutineContext().ensureActive()
                attempt()
            }
            false
        } catch (e: kotlinx.coroutines.CancellationException) {
            throw e
        } catch (e: Exception) {
            loginDenied(e)
        }
    } ?: false
