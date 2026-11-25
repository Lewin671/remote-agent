package io.github.lewin671.remoteagent.core

import com.connectrpc.Code
import com.connectrpc.Idempotency
import com.connectrpc.MethodSpec
import com.connectrpc.StreamType
import com.connectrpc.http.HTTPClientInterface
import com.connectrpc.http.HTTPMethod
import com.connectrpc.http.UnaryHTTPRequest
import com.google.protobuf.ByteString
import io.ktor.http.Url
import okio.Buffer
import io.github.lewin671.remoteagent.core.proto.Effect
import io.github.lewin671.remoteagent.core.proto.EffectBatch
import io.github.lewin671.remoteagent.core.proto.Event as CoreEvent
import io.github.lewin671.remoteagent.core.proto.RelayRpcCancelEffect
import io.github.lewin671.remoteagent.core.proto.RelayRpcEffect
import io.github.lewin671.remoteagent.core.proto.RelayRpcMethod
import io.github.lewin671.remoteagent.core.proto.RelayRpcResultEvent
import io.github.lewin671.remoteagent.core.proto.RpcErrorDetailBytes
import io.github.lewin671.remoteagent.core.proto.SecurityKeyAgreeEffect
import io.github.lewin671.remoteagent.core.proto.SecurityKeyAgreedEvent
import io.github.lewin671.remoteagent.core.proto.SecurityKeyDiscardEffect
import io.github.lewin671.remoteagent.core.proto.SecurityKeyEncapsulateEffect
import io.github.lewin671.remoteagent.core.proto.SecurityKeyEncapsulatedEvent
import io.github.lewin671.remoteagent.core.proto.SecurityKeyGenerateEffect
import io.github.lewin671.remoteagent.core.proto.SecurityKeyGeneratedEvent
import io.github.lewin671.remoteagent.core.proto.SecurityKeySignEffect
import io.github.lewin671.remoteagent.core.proto.SecurityKeySignedEvent
import io.github.lewin671.remoteagent.core.proto.SecurityNotifyDisplay
import io.github.lewin671.remoteagent.core.proto.SecurityOpenNotifyEvent
import io.github.lewin671.remoteagent.core.proto.SecurityRpcEffect
import io.github.lewin671.remoteagent.core.proto.SecurityRpcResultEvent
import io.github.lewin671.remoteagent.core.proto.SecurityStorageEffect
import io.github.lewin671.remoteagent.core.proto.SecurityStorageResultEvent
import io.github.lewin671.remoteagent.corebind.Corebind
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.NotifyKind
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.contentOrNull
import okhttp3.Authenticator
import okhttp3.Call
import okhttp3.Callback
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import java.io.IOException
import java.net.SocketTimeoutException
import java.net.UnknownHostException
import java.util.Base64
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import kotlin.coroutines.resume

private val PROTO_MEDIA_TYPE = "application/proto".toMediaType()

private val RELAY_RPC_PATHS: Map<RelayRpcMethod, String> = mapOf(
    RelayRpcMethod.RELAY_RPC_METHOD_GET_SECURITY_STATES to "/remoteagent.v1.ClientService/GetSecurityStates",
    RelayRpcMethod.RELAY_RPC_METHOD_PUT_SECURITY_STATE to "/remoteagent.v1.ClientService/PutSecurityState",
    RelayRpcMethod.RELAY_RPC_METHOD_PIN_SECURITY_DOMAIN to "/remoteagent.v1.ClientService/PinSecurityDomain",
    RelayRpcMethod.RELAY_RPC_METHOD_CREATE_PAIRING to "/remoteagent.v1.PairingService/CreatePairing",
    RelayRpcMethod.RELAY_RPC_METHOD_PEEK_PAIRING to "/remoteagent.v1.PairingService/PeekPairing",
    RelayRpcMethod.RELAY_RPC_METHOD_JOIN_PAIRING to "/remoteagent.v1.PairingService/JoinPairing",
    RelayRpcMethod.RELAY_RPC_METHOD_REVEAL_PAIRING to "/remoteagent.v1.PairingService/RevealPairing",
    RelayRpcMethod.RELAY_RPC_METHOD_APPROVE_PAIRING to "/remoteagent.v1.PairingService/ApprovePairing",
    RelayRpcMethod.RELAY_RPC_METHOD_GET_PAIRING to "/remoteagent.v1.PairingService/GetPairing",
    RelayRpcMethod.RELAY_RPC_METHOD_CANCEL_PAIRING to "/remoteagent.v1.PairingService/CancelPairing",
    RelayRpcMethod.RELAY_RPC_METHOD_GET_RECOVERY_CHALLENGE to "/remoteagent.v1.RecoveryService/GetRecoveryChallenge",
    RelayRpcMethod.RELAY_RPC_METHOD_GET_RECOVERY_CONTEXT to "/remoteagent.v1.RecoveryService/GetRecoveryContext",
    RelayRpcMethod.RELAY_RPC_METHOD_SUBMIT_RECOVERY to "/remoteagent.v1.RecoveryService/SubmitRecovery",
    RelayRpcMethod.RELAY_RPC_METHOD_COMPLETE_RECOVERY to "/remoteagent.v1.RecoveryService/CompleteRecovery",
    RelayRpcMethod.RELAY_RPC_METHOD_DELETE_MACHINE_RECORDS to "/remoteagent.v1.ClientService/DeleteMachineRecords",
    RelayRpcMethod.RELAY_RPC_METHOD_DELETE_ACCOUNT to "/remoteagent.v1.ClientService/DeleteAccount",
    RelayRpcMethod.RELAY_RPC_METHOD_PUT_DEVICE_META to "/remoteagent.v1.ClientService/PutDeviceMeta",
    RelayRpcMethod.RELAY_RPC_METHOD_LIST_DEVICES to "/remoteagent.v1.ClientService/ListDevices",
    RelayRpcMethod.RELAY_RPC_METHOD_LIST_MACHINES to "/remoteagent.v1.ClientService/ListMachines",
    RelayRpcMethod.RELAY_RPC_METHOD_GET_ACCOUNT_PREFS to "/remoteagent.v1.ClientService/GetAccountPrefs",
    RelayRpcMethod.RELAY_RPC_METHOD_PUT_ACCOUNT_PREFS to "/remoteagent.v1.ClientService/PutAccountPrefs",
    RelayRpcMethod.RELAY_RPC_METHOD_GET_CHALLENGE to "/remoteagent.v1.AuthService/GetChallenge",
    RelayRpcMethod.RELAY_RPC_METHOD_LOGIN to "/remoteagent.v1.AuthService/Login",
    RelayRpcMethod.RELAY_RPC_METHOD_GET_SIGNUP_CHALLENGE to "/remoteagent.v1.AuthService/GetSignupChallenge",
    RelayRpcMethod.RELAY_RPC_METHOD_REGISTER_DEVICE to "/remoteagent.v1.AuthService/RegisterDevice",
    RelayRpcMethod.RELAY_RPC_METHOD_LIST_REMOTE_SESSIONS to "/remoteagent.v1.ClientService/ListRemoteSessions",
    RelayRpcMethod.RELAY_RPC_METHOD_GET_EVENTS to "/remoteagent.v1.ClientService/GetEvents",
    RelayRpcMethod.RELAY_RPC_METHOD_FORWARD to "/remoteagent.v1.ClientService/Forward",
    RelayRpcMethod.RELAY_RPC_METHOD_GET_IMAGE to "/remoteagent.v1.ClientService/GetImage",
    RelayRpcMethod.RELAY_RPC_METHOD_GET_PUSH_KEY to "/remoteagent.v1.ClientService/GetPushKey",
    RelayRpcMethod.RELAY_RPC_METHOD_SET_PUSH_SUBSCRIPTION to "/remoteagent.v1.ClientService/SetPushSubscription",
    RelayRpcMethod.RELAY_RPC_METHOD_SET_PUSH_TARGET to "/remoteagent.v1.ClientService/SetPushTarget",
    RelayRpcMethod.RELAY_RPC_METHOD_HEALTHZ to "/healthz",
)

private val CONNECT_CODE_MAP: Map<String, Int> = mapOf(
    "canceled" to 1,
    "unknown" to 2,
    "invalid_argument" to 3,
    "deadline_exceeded" to 4,
    "not_found" to 5,
    "already_exists" to 6,
    "permission_denied" to 7,
    "resource_exhausted" to 8,
    "failed_precondition" to 9,
    "aborted" to 10,
    "out_of_range" to 11,
    "unimplemented" to 12,
    "internal" to 13,
    "unavailable" to 14,
    "data_loss" to 15,
    "unauthenticated" to 16,
)

private fun connectCodeFromHttpStatus(status: Int): Int = when (status) {
    400 -> 3
    401 -> 16
    403 -> 7
    404 -> 5
    408, 504 -> 4
    409 -> 10
    412 -> 9
    429, 502, 503 -> 14
    in 500..599 -> 13
    else -> 2
}

private fun errorMessage(t: Throwable): String = t.message?.takeIf { it.isNotEmpty() } ?: t.javaClass.simpleName

private data class ParsedRpcError(
    val code: Int,
    val message: String,
    val details: List<RpcErrorDetailBytes>,
)

private fun parseConnectErrorBody(
    httpCode: Int,
    bodyBytes: ByteArray,
    causeCode: Int? = null,
    causeMsg: String? = null,
    fallbackDetails: List<RpcErrorDetailBytes> = emptyList(),
): ParsedRpcError {
    var code = causeCode ?: connectCodeFromHttpStatus(httpCode)
    var msg = causeMsg?.takeIf { it.isNotEmpty() } ?: "HTTP $httpCode"
    val details = mutableListOf<RpcErrorDetailBytes>()
    val text = runCatching { bodyBytes.decodeToString() }.getOrNull()
    val parsed = if (!text.isNullOrBlank()) runCatching { Json.parseToJsonElement(text) as? JsonObject }.getOrNull() else null
    if (parsed != null) {
        (parsed["code"] as? JsonPrimitive)?.takeIf { it.isString }?.contentOrNull?.let { CONNECT_CODE_MAP[it]?.let { c -> code = c } }
        (parsed["message"] as? JsonPrimitive)?.takeIf { it.isString }?.contentOrNull?.takeIf { it.isNotEmpty() }?.let { msg = it }
        val rawDetails = parsed["details"] as? JsonArray
        if (rawDetails != null) {
            for (elem in rawDetails) {
                val obj = elem as? JsonObject ?: continue
                val typeName = (obj["type"] as? JsonPrimitive)?.takeIf { it.isString }?.contentOrNull?.takeIf { it.isNotEmpty() } ?: continue
                val b64 = (obj["value"] as? JsonPrimitive)?.takeIf { it.isString }?.contentOrNull?.replace(Regex("\\s+"), "")?.takeIf { it.isNotEmpty() } ?: continue
                val decoded = runCatching { Base64.getDecoder().decode(b64) }.getOrNull() ?: continue
                if (decoded.isNotEmpty()) {
                    details += RpcErrorDetailBytes.newBuilder()
                        .setTypeName(typeName)
                        .setValue(ByteString.copyFrom(decoded))
                        .build()
                }
            }
        }
    }
    if (details.isEmpty() && fallbackDetails.isNotEmpty()) {
        details += fallbackDetails
    }
    return ParsedRpcError(code, msg, details)
}

internal fun parseSecurityRpcError(
    opId: Long,
    httpCode: Int,
    bodyBytes: ByteArray,
    causeCode: Int? = null,
    causeMsg: String? = null,
    fallbackDetails: List<RpcErrorDetailBytes> = emptyList(),
): SecurityRpcResultEvent {
    val parsed = parseConnectErrorBody(httpCode, bodyBytes, causeCode, causeMsg, fallbackDetails)
    return SecurityRpcResultEvent.newBuilder()
        .setOpId(opId)
        .setConnectCode(parsed.code)
        .setError(parsed.message)
        .apply { if (parsed.details.isNotEmpty()) addAllErrorDetails(parsed.details) }
        .build()
}

/**
 * Executes all host-side security and universal relay byte-pipe effects emitted by Go `clientcore`:
 * - Key generation, Ed25519 signing, X25519 agreement, KEM encapsulation, discard
 * - CAS storage operations & bootstrap / clear-all
 * - Connect unary binary-protobuf RPC & /healthz transport with one-shot body guard and cancellation
 */
class SecurityExecutor(
    private val store: SecurityStoreV4,
    http: OkHttpClient,
    private val emit: (CoreEvent) -> Unit,
    parentScope: CoroutineScope,
    private val allowPlainHttp: Boolean = true,
    private val rpcTransport: HTTPClientInterface? = null,
    dispatcher: CoroutineDispatcher = Dispatchers.IO,
    @Volatile private var defaultServer: String = "",
    @Volatile private var defaultDeviceId: String = "",
) {
    fun setDefaultAccount(server: String, deviceId: String) {
        defaultServer = server
        defaultDeviceId = deviceId
    }
    private val closed = AtomicBoolean(false)
    private val scope = CoroutineScope(SupervisorJob(parentScope.coroutineContext[kotlinx.coroutines.Job]) + dispatcher)
    private val activeRelayJobs = ConcurrentHashMap<Long, Job>()
    private val rpcHttp = http.newBuilder()
        .retryOnConnectionFailure(false)
        .followRedirects(false)
        .followSslRedirects(false)
        .authenticator(Authenticator.NONE)
        .proxyAuthenticator(Authenticator.NONE)
        .apply { interceptors().add(0, OneShotRPCBody) }
        .build()

    fun close() {
        if (closed.compareAndSet(false, true)) {
            activeRelayJobs.values.forEach { it.cancel() }
            activeRelayJobs.clear()
            scope.cancel()
        }
    }

    private fun emitIfOpen(block: CoreEvent.Builder.() -> Unit) {
        if (!closed.get()) emit(CoreEvent.newBuilder().apply(block).build())
    }

    private inline fun <T> launchOp(
        crossinline onErr: (String) -> T,
        crossinline emitBlock: CoreEvent.Builder.(T) -> Unit,
        crossinline block: suspend () -> T,
    ) {
        if (closed.get()) return
        scope.launch {
            val ev = try {
                block()
            } catch (t: Throwable) {
                if (t is CancellationException) throw t
                onErr(errorMessage(t))
            }
            emitIfOpen { emitBlock(ev) }
        }
    }

    fun handleEffect(eff: Effect): Boolean {
        when (eff.effectCase) {
            Effect.EffectCase.SECURITY_KEY_GENERATE -> launchOp(
                { SecurityKeyGeneratedEvent.newBuilder().setOpId(eff.securityKeyGenerate.opId).setError(it).build() },
                { setSecurityKeyGenerated(it) },
            ) {
                val e = eff.securityKeyGenerate
                val pub = store.generateKey(e.keyKind, e.slot, e.persist)
                SecurityKeyGeneratedEvent.newBuilder().setOpId(e.opId).setPublicKey(ByteString.copyFrom(pub)).build()
            }
            Effect.EffectCase.SECURITY_KEY_SIGN -> launchOp(
                { SecurityKeySignedEvent.newBuilder().setOpId(eff.securityKeySign.opId).setError(it).build() },
                { setSecurityKeySigned(it) },
            ) {
                val e = eff.securityKeySign
                val msg = e.message.toByteArray()
                require(msg.isNotEmpty()) { "empty message to sign" }
                val sig = store.signKey(e.keyKind, e.slot, msg)
                SecurityKeySignedEvent.newBuilder().setOpId(e.opId).setSignature(ByteString.copyFrom(sig)).build()
            }
            Effect.EffectCase.SECURITY_KEY_AGREE -> launchOp(
                { SecurityKeyAgreedEvent.newBuilder().setOpId(eff.securityKeyAgree.opId).setError(it).build() },
                { setSecurityKeyAgreed(it) },
            ) {
                val e = eff.securityKeyAgree
                val shared = store.agreeKey(e.keyKind, e.slot, e.peerPublic.toByteArray())
                SecurityKeyAgreedEvent.newBuilder().setOpId(e.opId).setShared(ByteString.copyFrom(shared)).build()
            }
            Effect.EffectCase.SECURITY_KEY_ENCAPSULATE -> launchOp(
                { SecurityKeyEncapsulatedEvent.newBuilder().setOpId(eff.securityKeyEncapsulate.opId).setError(it).build() },
                { setSecurityKeyEncapsulated(it) },
            ) {
                val e = eff.securityKeyEncapsulate
                SecurityKeyEncapsulatedEvent.newBuilder().setOpId(e.opId).addAllPairs(store.encapsulate(e.recipientsList)).build()
            }
            Effect.EffectCase.SECURITY_KEY_DISCARD -> if (!closed.get()) {
                val e = eff.securityKeyDiscard
                scope.launch { runCatching { store.discardKey(e.keyKind, e.slot) } }
            }
            Effect.EffectCase.SECURITY_STORAGE -> launchOp(
                { SecurityStorageResultEvent.newBuilder().setOpId(eff.securityStorage.opId).setError(it).build() },
                { setSecurityStorageResult(it) },
            ) {
                val res = store.executeStorage(eff.securityStorage)
                if (eff.securityStorage.op == io.github.lewin671.remoteagent.core.proto.SecurityStorageOp.SECURITY_STORAGE_OP_READ_BOOTSTRAP &&
                    res.loginToken.isEmpty() &&
                    defaultServer.isNotEmpty() &&
                    defaultDeviceId.isNotEmpty()
                ) {
                    val saved = runCatching { store.loadLoginToken(defaultServer, defaultDeviceId) }.getOrNull()
                    if (saved != null && saved.token.isNotEmpty()) {
                        res.toBuilder()
                            .setLoginToken(saved.token)
                            .setLoginTokenExpiresMs(saved.expiresMs)
                            .build()
                    } else {
                        res
                    }
                } else {
                    res
                }
            }
            Effect.EffectCase.SECURITY_RPC -> launchOp(
                { rpcErr(eff.securityRpc.opId, 2, it) },
                { setSecurityRpcResult(it) },
            ) { executeRpcUnary(eff.securityRpc) }
            Effect.EffectCase.RELAY_RPC -> handleRelayRpc(eff.relayRpc)
            Effect.EffectCase.RELAY_RPC_CANCEL -> handleRelayRpcCancel(eff.relayRpcCancel)
            else -> return false
        }
        return true
    }

    fun handleRelayRpc(eff: RelayRpcEffect) {
        if (closed.get()) return
        val opId = eff.opId
        val job = scope.launch {
            val res = try {
                executeRelayUnary(
                    opId = opId,
                    method = eff.method,
                    server = eff.server,
                    authToken = eff.authToken,
                    timeoutMs = eff.timeoutMs,
                    attemptHeader = eff.attemptHeader,
                    requestBytes = eff.requestBytes.toByteArray(),
                )
            } catch (t: Throwable) {
                if (t is CancellationException) throw t
                relayErr(opId, 2, errorMessage(t))
            }
            emitIfOpen { setRelayRpcResult(res) }
        }
        activeRelayJobs[opId] = job
        job.invokeOnCompletion { activeRelayJobs.remove(opId, job) }
    }

    fun handleRelayRpcCancel(eff: RelayRpcCancelEffect) {
        activeRelayJobs.remove(eff.opId)?.cancel()
    }

    private fun rpcErr(
        opId: Long,
        code: Int,
        msg: String,
        networkError: Boolean = false,
        errorDetails: List<RpcErrorDetailBytes> = emptyList(),
    ): SecurityRpcResultEvent =
        SecurityRpcResultEvent.newBuilder()
            .setOpId(opId)
            .setConnectCode(code)
            .setError(msg)
            .apply {
                if (networkError) setNetworkError(true)
                if (errorDetails.isNotEmpty()) addAllErrorDetails(errorDetails)
            }
            .build()

    private fun relayOk(opId: Long, bodyBytes: ByteArray, relayEpoch: String): RelayRpcResultEvent =
        relayRpcOk(opId, bodyBytes, relayEpoch)

    private fun relayErr(
        opId: Long,
        code: Int,
        msg: String,
        relayEpoch: String = "",
        networkError: Boolean = false,
        unresolvedHost: Boolean = false,
        errorDetails: List<RpcErrorDetailBytes> = emptyList(),
    ): RelayRpcResultEvent = relayRpcErr(opId, code, msg, relayEpoch, networkError, unresolvedHost, errorDetails)

    internal suspend fun executeRelayUnary(
        opId: Long,
        method: RelayRpcMethod,
        server: String,
        authToken: String = "",
        timeoutMs: Long = 0L,
        attemptHeader: String = "",
        requestBytes: ByteArray = ByteArray(0),
    ): RelayRpcResultEvent = executeRelayRpcUnary(
        http = rpcHttp,
        method = method,
        server = server.ifEmpty { defaultServer },
        authToken = authToken,
        timeoutMs = timeoutMs,
        attemptHeader = attemptHeader,
        requestBytes = requestBytes,
        allowPlainHttp = allowPlainHttp,
        rpcTransport = rpcTransport,
        opId = opId,
        preconfiguredHttp = true,
    )

    private suspend fun executeRpcUnary(eff: SecurityRpcEffect): SecurityRpcResultEvent {
        val method = RelayRpcMethod.forNumber(eff.method.number)
            ?: return rpcErr(eff.opId, 12, "unknown rpc method: ${eff.method.number}")
        val res = executeRelayUnary(
            opId = eff.opId,
            method = method,
            server = eff.server,
            authToken = eff.authToken,
            requestBytes = eff.requestBytes.toByteArray(),
        )
        return if (res.connectCode == 0 && !res.networkError && !res.unresolvedHost && res.diagnosticMessage.isEmpty()) {
            SecurityRpcResultEvent.newBuilder().setOpId(eff.opId).setResponseBytes(res.responseBytes).build()
        } else {
            rpcErr(eff.opId, res.connectCode, res.diagnosticMessage, res.networkError, res.errorDetailsList)
        }
    }
}

private fun relayRpcOk(opId: Long, bodyBytes: ByteArray, relayEpoch: String): RelayRpcResultEvent =
    RelayRpcResultEvent.newBuilder()
        .setOpId(opId)
        .setResponseBytes(ByteString.copyFrom(bodyBytes))
        .apply { if (relayEpoch.isNotEmpty()) setRelayEpoch(relayEpoch) }
        .build()

private fun relayRpcErr(
    opId: Long,
    code: Int,
    msg: String,
    relayEpoch: String = "",
    networkError: Boolean = false,
    unresolvedHost: Boolean = false,
    errorDetails: List<RpcErrorDetailBytes> = emptyList(),
): RelayRpcResultEvent =
    RelayRpcResultEvent.newBuilder()
        .setOpId(opId)
        .setConnectCode(code)
        .setDiagnosticMessage(msg)
        .apply {
            if (relayEpoch.isNotEmpty()) setRelayEpoch(relayEpoch)
            if (networkError) setNetworkError(true)
            if (unresolvedHost) setUnresolvedHost(true)
            if (errorDetails.isNotEmpty()) addAllErrorDetails(errorDetails)
        }
        .build()

/** Executes a single unary relay ConnectRPC/HTTP call reusing [SecurityExecutor]'s path table and redirect/auth hardening. */
suspend fun executeRelayRpcUnary(
    http: OkHttpClient,
    method: RelayRpcMethod,
    server: String,
    authToken: String = "",
    timeoutMs: Long = 15_000L,
    attemptHeader: String = "",
    requestBytes: ByteArray = ByteArray(0),
    allowPlainHttp: Boolean = true,
    rpcTransport: HTTPClientInterface? = null,
    opId: Long = 0L,
    preconfiguredHttp: Boolean = false,
): RelayRpcResultEvent {
    val path = RELAY_RPC_PATHS[method]
        ?: return relayRpcErr(opId, 12, "unknown rpc method: ${method.number}")
    if (server.isEmpty()) {
        return relayRpcErr(opId, 3, "missing relay server")
    }
    val url = Secret.checkServer(server, allowPlainHttp = allowPlainHttp).trimEnd('/') + path
    val isHealthz = method == RelayRpcMethod.RELAY_RPC_METHOD_HEALTHZ
    if (rpcTransport != null) {
        val headers = buildMap<String, List<String>> {
            if (isHealthz) {
                put("Cache-Control", listOf("no-store"))
            } else {
                put("Content-Type", listOf("application/proto"))
                put("Connect-Protocol-Version", listOf("1"))
                if (authToken.isNotEmpty()) put("Authorization", listOf("Bearer $authToken"))
                if (attemptHeader.isNotEmpty()) put("Remote-Agent-Attempt", listOf(attemptHeader))
            }
        }
        val unaryReq = UnaryHTTPRequest(
            url = Url(url),
            contentType = if (isHealthz) "" else "application/proto",
            timeout = null,
            headers = headers,
            methodSpec = MethodSpec(
                path = path,
                requestClass = ByteArray::class,
                responseClass = ByteArray::class,
                streamType = StreamType.UNARY,
                idempotency = Idempotency.UNKNOWN,
            ),
            message = if (isHealthz) Buffer() else Buffer().write(requestBytes),
            httpMethod = if (isHealthz) HTTPMethod.GET else HTTPMethod.POST,
        )
        return suspendCancellableCoroutine { cont ->
            val cancel = rpcTransport.unary(unaryReq) { res ->
                val bodyBytes = runCatching { res.message.readByteArray() }.getOrElse { ByteArray(0) }
                val epoch = res.headers.entries
                    .firstOrNull { it.key.equals(RELAY_EPOCH_HEADER, ignoreCase = true) }
                    ?.value?.firstOrNull().orEmpty()
                val cause = res.cause
                val status = res.status
                val causeDetails = cause?.details?.mapNotNull { d ->
                    val bytes = d.payload.toByteArray()
                    if (d.type.isEmpty() || bytes.isEmpty()) null
                    else RpcErrorDetailBytes.newBuilder().setTypeName(d.type).setValue(ByteString.copyFrom(bytes)).build()
                }.orEmpty()
                val result = when {
                    status != null && status in 200..299 && cause == null -> relayRpcOk(opId, bodyBytes, epoch)
                    cause != null && status == null -> relayRpcErr(
                        opId = opId,
                        code = cause.code.value,
                        msg = cause.message ?: cause.code.codeName,
                        relayEpoch = epoch,
                        networkError = cause.code == Code.UNAVAILABLE || cause.code == Code.DEADLINE_EXCEEDED,
                        unresolvedHost = cause.exception is UnknownHostException,
                        errorDetails = causeDetails,
                    )
                    else -> {
                        val parsed = parseConnectErrorBody(status ?: 502, bodyBytes, cause?.code?.value, cause?.message, causeDetails)
                        relayRpcErr(opId, parsed.code, parsed.message, relayEpoch = epoch, errorDetails = parsed.details)
                    }
                }
                if (cont.isActive) cont.resume(result)
            }
            cont.invokeOnCancellation { cancel() }
        }
    }
    val reqBuilder = Request.Builder().url(url)
    if (isHealthz) {
        reqBuilder.header("Cache-Control", "no-store").get()
    } else {
        reqBuilder
            .header("Content-Type", "application/proto")
            .header("Connect-Protocol-Version", "1")
            .apply {
                if (authToken.isNotEmpty()) header("Authorization", "Bearer $authToken")
                if (attemptHeader.isNotEmpty()) header("Remote-Agent-Attempt", attemptHeader)
            }
            .post(requestBytes.toRequestBody(PROTO_MEDIA_TYPE))
    }
    val baseHttp = if (preconfiguredHttp) http else http.newBuilder()
        .retryOnConnectionFailure(false)
        .followRedirects(false)
        .followSslRedirects(false)
        .authenticator(Authenticator.NONE)
        .proxyAuthenticator(Authenticator.NONE)
        .apply { interceptors().add(0, OneShotRPCBody) }
        .build()
    val client = if (timeoutMs > 0L) {
        baseHttp.newBuilder().callTimeout(timeoutMs, TimeUnit.MILLISECONDS).build()
    } else {
        baseHttp
    }
    return suspendCancellableCoroutine { cont ->
        val call = client.newCall(reqBuilder.build())
        cont.invokeOnCancellation { call.cancel() }
        call.enqueue(object : Callback {
            override fun onFailure(call: Call, e: IOException) {
                if (!cont.isActive) return
                val isTimeout = e is SocketTimeoutException || (e is java.io.InterruptedIOException && !call.isCanceled())
                val code = when {
                    call.isCanceled() -> 1
                    isTimeout -> 4
                    else -> 14
                }
                cont.resume(
                    relayRpcErr(
                        opId = opId,
                        code = code,
                        msg = errorMessage(e),
                        networkError = !call.isCanceled(),
                        unresolvedHost = e is UnknownHostException,
                    ),
                )
            }

            override fun onResponse(call: Call, response: Response) {
                val result = response.use { res ->
                    val epoch = res.header(RELAY_EPOCH_HEADER).orEmpty()
                    val bodyBytes = res.body?.bytes() ?: ByteArray(0)
                    if (res.isSuccessful) {
                        relayRpcOk(opId, bodyBytes, epoch)
                    } else {
                        val parsed = parseConnectErrorBody(res.code, bodyBytes, causeMsg = res.message)
                        relayRpcErr(opId, parsed.code, parsed.message, relayEpoch = epoch, errorDetails = parsed.details)
                    }
                }
                if (cont.isActive) cont.resume(result)
            }
        })
    }
}

/**
 * Decodes, verifies, and projects a push notification [Envelope] using Go `clientcore` Path A (`notify_blob`).
 * Runs in a short-lived sandboxed Core instance without needing unlocked device private keys.
 */
fun openNotifyEnvelopeViaCore(
    notifyBlob: ByteArray,
    envelope: Envelope,
    nowMs: Long = System.currentTimeMillis(),
    sessionId: String = "",
    tag: String = "",
    machineId: String = "",
    expectedKind: NotifyKind = NotifyKind.NOTIFY_KIND_UNSPECIFIED,
    eventKey: String = "",
): SecurityNotifyDisplay? {
    if (notifyBlob.isEmpty()) return null
    val core = Corebind.new_()
    return try {
        val ev = CoreEvent.newBuilder()
            .setNowMs(nowMs)
            .setSecurityOpenNotify(
                SecurityOpenNotifyEvent.newBuilder()
                    .setOpId(1L)
                    .setNotifyBlob(ByteString.copyFrom(notifyBlob))
                    .setEnvelope(envelope)
                    .setNowMs(nowMs)
                    .setSessionId(sessionId)
                    .setTag(tag)
                    .setMachineId(machineId)
                    .setExpectedKind(expectedKind)
                    .setEventKey(eventKey),
            )
            .build()
        val batchBytes: ByteArray? = core.dispatch(ev.toByteArray())
        if (batchBytes == null || batchBytes.isEmpty()) return null
        val batch = EffectBatch.parseFrom(batchBytes)
        for (eff in batch.effectsList) {
            if (eff.hasSecurityNotifyOpened()) {
                val opened = eff.securityNotifyOpened
                if (opened.error.isEmpty() && opened.hasDisplay()) {
                    return opened.display
                }
            }
        }
        null
    } finally {
        core.close()
    }
}
