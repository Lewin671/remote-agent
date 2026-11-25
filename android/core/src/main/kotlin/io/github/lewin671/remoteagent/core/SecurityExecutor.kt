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
import io.github.lewin671.remoteagent.core.proto.SecurityRpcMethod
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
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.jsonPrimitive
import okhttp3.Authenticator
import okhttp3.Call
import okhttp3.Callback
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import java.io.IOException
import java.util.Base64
import java.util.concurrent.atomic.AtomicBoolean
import kotlin.coroutines.resume

private val PROTO_MEDIA_TYPE = "application/proto".toMediaType()

private val SECURITY_RPC_PATHS: Map<SecurityRpcMethod, String> = mapOf(
    SecurityRpcMethod.SECURITY_RPC_METHOD_GET_SECURITY_STATES to "/remoteagent.v1.ClientService/GetSecurityStates",
    SecurityRpcMethod.SECURITY_RPC_METHOD_PUT_SECURITY_STATE to "/remoteagent.v1.ClientService/PutSecurityState",
    SecurityRpcMethod.SECURITY_RPC_METHOD_PIN_SECURITY_DOMAIN to "/remoteagent.v1.ClientService/PinSecurityDomain",
    SecurityRpcMethod.SECURITY_RPC_METHOD_CREATE_PAIRING to "/remoteagent.v1.PairingService/CreatePairing",
    SecurityRpcMethod.SECURITY_RPC_METHOD_PEEK_PAIRING to "/remoteagent.v1.PairingService/PeekPairing",
    SecurityRpcMethod.SECURITY_RPC_METHOD_JOIN_PAIRING to "/remoteagent.v1.PairingService/JoinPairing",
    SecurityRpcMethod.SECURITY_RPC_METHOD_REVEAL_PAIRING to "/remoteagent.v1.PairingService/RevealPairing",
    SecurityRpcMethod.SECURITY_RPC_METHOD_APPROVE_PAIRING to "/remoteagent.v1.PairingService/ApprovePairing",
    SecurityRpcMethod.SECURITY_RPC_METHOD_GET_PAIRING to "/remoteagent.v1.PairingService/GetPairing",
    SecurityRpcMethod.SECURITY_RPC_METHOD_CANCEL_PAIRING to "/remoteagent.v1.PairingService/CancelPairing",
    SecurityRpcMethod.SECURITY_RPC_METHOD_GET_RECOVERY_CHALLENGE to "/remoteagent.v1.RecoveryService/GetRecoveryChallenge",
    SecurityRpcMethod.SECURITY_RPC_METHOD_GET_RECOVERY_CONTEXT to "/remoteagent.v1.RecoveryService/GetRecoveryContext",
    SecurityRpcMethod.SECURITY_RPC_METHOD_SUBMIT_RECOVERY to "/remoteagent.v1.RecoveryService/SubmitRecovery",
    SecurityRpcMethod.SECURITY_RPC_METHOD_COMPLETE_RECOVERY to "/remoteagent.v1.RecoveryService/CompleteRecovery",
    SecurityRpcMethod.SECURITY_RPC_METHOD_DELETE_MACHINE_RECORDS to "/remoteagent.v1.ClientService/DeleteMachineRecords",
    SecurityRpcMethod.SECURITY_RPC_METHOD_DELETE_ACCOUNT to "/remoteagent.v1.ClientService/DeleteAccount",
    SecurityRpcMethod.SECURITY_RPC_METHOD_PUT_DEVICE_META to "/remoteagent.v1.ClientService/PutDeviceMeta",
    SecurityRpcMethod.SECURITY_RPC_METHOD_LIST_DEVICES to "/remoteagent.v1.ClientService/ListDevices",
    SecurityRpcMethod.SECURITY_RPC_METHOD_LIST_MACHINES to "/remoteagent.v1.ClientService/ListMachines",
    SecurityRpcMethod.SECURITY_RPC_METHOD_GET_ACCOUNT_PREFS to "/remoteagent.v1.ClientService/GetAccountPrefs",
    SecurityRpcMethod.SECURITY_RPC_METHOD_PUT_ACCOUNT_PREFS to "/remoteagent.v1.ClientService/PutAccountPrefs",
    SecurityRpcMethod.SECURITY_RPC_METHOD_GET_CHALLENGE to "/remoteagent.v1.AuthService/GetChallenge",
    SecurityRpcMethod.SECURITY_RPC_METHOD_LOGIN to "/remoteagent.v1.AuthService/Login",
    SecurityRpcMethod.SECURITY_RPC_METHOD_GET_SIGNUP_CHALLENGE to "/remoteagent.v1.AuthService/GetSignupChallenge",
    SecurityRpcMethod.SECURITY_RPC_METHOD_REGISTER_DEVICE to "/remoteagent.v1.AuthService/RegisterDevice",
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

internal fun parseSecurityRpcError(
    opId: Long,
    httpCode: Int,
    bodyBytes: ByteArray,
    causeCode: Int? = null,
    causeMsg: String? = null,
    fallbackDetails: List<RpcErrorDetailBytes> = emptyList(),
): SecurityRpcResultEvent {
    var code = causeCode ?: connectCodeFromHttpStatus(httpCode)
    var msg = causeMsg?.takeIf { it.isNotEmpty() } ?: "HTTP $httpCode"
    val details = mutableListOf<RpcErrorDetailBytes>()
    val text = runCatching { bodyBytes.decodeToString() }.getOrNull()
    val parsed = if (!text.isNullOrBlank()) runCatching { Json.parseToJsonElement(text) as? JsonObject }.getOrNull() else null
    if (parsed != null) {
        parsed["code"]?.jsonPrimitive?.contentOrNull?.let { CONNECT_CODE_MAP[it]?.let { c -> code = c } }
        parsed["message"]?.jsonPrimitive?.contentOrNull?.takeIf { it.isNotEmpty() }?.let { msg = it }
        val rawDetails = parsed["details"] as? JsonArray
        if (rawDetails != null) {
            for (elem in rawDetails) {
                val obj = elem as? JsonObject ?: continue
                val typeName = obj["type"]?.jsonPrimitive?.contentOrNull?.takeIf { it.isNotEmpty() } ?: continue
                val b64 = obj["value"]?.jsonPrimitive?.contentOrNull?.replace(Regex("\\s+"), "")?.takeIf { it.isNotEmpty() } ?: continue
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
    return SecurityRpcResultEvent.newBuilder()
        .setOpId(opId)
        .setConnectCode(code)
        .setError(msg)
        .apply { if (details.isNotEmpty()) addAllErrorDetails(details) }
        .build()
}

/**
 * Executes all host-side S6 security effects emitted by Go `clientcore`:
 * - Key generation, Ed25519 signing, X25519 agreement, KEM encapsulation, discard
 * - CAS storage operations & bootstrap / clear-all
 * - Connect unary binary-protobuf RPC transport for security workflows
 */
internal class SecurityExecutor(
    private val store: SecurityStoreV4,
    http: OkHttpClient,
    private val emit: (CoreEvent) -> Unit,
    parentScope: CoroutineScope,
    private val allowPlainHttp: Boolean = true,
    private val rpcTransport: HTTPClientInterface? = null,
    dispatcher: CoroutineDispatcher = Dispatchers.IO,
) {
    private val closed = AtomicBoolean(false)
    private val scope = CoroutineScope(SupervisorJob(parentScope.coroutineContext[kotlinx.coroutines.Job]) + dispatcher)
    private val rpcHttp = http.newBuilder()
        .retryOnConnectionFailure(false)
        .followRedirects(false)
        .followSslRedirects(false)
        .authenticator(Authenticator.NONE)
        .proxyAuthenticator(Authenticator.NONE)
        .build()

    fun close() {
        if (closed.compareAndSet(false, true)) {
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
            ) { store.executeStorage(eff.securityStorage) }
            Effect.EffectCase.SECURITY_RPC -> launchOp(
                { rpcErr(eff.securityRpc.opId, 2, it) },
                { setSecurityRpcResult(it) },
            ) { executeRpcUnary(eff.securityRpc) }
            else -> return false
        }
        return true
    }

    private fun rpcOk(opId: Long, bodyBytes: ByteArray): SecurityRpcResultEvent =
        SecurityRpcResultEvent.newBuilder().setOpId(opId).setResponseBytes(ByteString.copyFrom(bodyBytes)).build()

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

    private fun parseRpcError(
        opId: Long,
        httpCode: Int,
        bodyBytes: ByteArray,
        causeCode: Int? = null,
        causeMsg: String? = null,
        fallbackDetails: List<RpcErrorDetailBytes> = emptyList(),
    ): SecurityRpcResultEvent = parseSecurityRpcError(opId, httpCode, bodyBytes, causeCode, causeMsg, fallbackDetails)

    private suspend fun executeRpcUnary(eff: SecurityRpcEffect): SecurityRpcResultEvent {
        val path = SECURITY_RPC_PATHS[eff.method]
            ?: return rpcErr(eff.opId, 12, "unknown rpc method: ${eff.method.number}")
        val url = Secret.checkServer(eff.server, allowPlainHttp = allowPlainHttp).trimEnd('/') + path
        if (rpcTransport != null) {
            val headers = buildMap<String, List<String>> {
                put("Content-Type", listOf("application/proto"))
                put("Connect-Protocol-Version", listOf("1"))
                if (eff.authToken.isNotEmpty()) put("Authorization", listOf("Bearer ${eff.authToken}"))
            }
            val unaryReq = UnaryHTTPRequest(
                url = Url(url),
                contentType = "application/proto",
                timeout = null,
                headers = headers,
                methodSpec = MethodSpec(
                    path = path,
                    requestClass = ByteArray::class,
                    responseClass = ByteArray::class,
                    streamType = StreamType.UNARY,
                    idempotency = Idempotency.UNKNOWN,
                ),
                message = Buffer().write(eff.requestBytes.toByteArray()),
                httpMethod = HTTPMethod.POST,
            )
            return suspendCancellableCoroutine { cont ->
                val cancel = rpcTransport.unary(unaryReq) { res ->
                    val bodyBytes = runCatching { res.message.readByteArray() }.getOrElse { ByteArray(0) }
                    val cause = res.cause
                    val status = res.status
                    val causeDetails = cause?.details?.mapNotNull { d ->
                        val bytes = d.payload.toByteArray()
                        if (d.type.isEmpty() || bytes.isEmpty()) null
                        else RpcErrorDetailBytes.newBuilder().setTypeName(d.type).setValue(ByteString.copyFrom(bytes)).build()
                    }.orEmpty()
                    val result = when {
                        status != null && status in 200..299 && cause == null -> rpcOk(eff.opId, bodyBytes)
                        cause != null && status == null ->
                            rpcErr(eff.opId, cause.code.value, cause.message ?: cause.code.codeName, cause.code == Code.UNAVAILABLE, causeDetails)
                        else -> parseRpcError(eff.opId, status ?: 502, bodyBytes, cause?.code?.value, cause?.message, causeDetails)
                    }
                    if (cont.isActive) cont.resume(result)
                }
                cont.invokeOnCancellation { cancel() }
            }
        }
        val req = Request.Builder()
            .url(url)
            .header("Content-Type", "application/proto")
            .header("Connect-Protocol-Version", "1")
            .apply { if (eff.authToken.isNotEmpty()) header("Authorization", "Bearer ${eff.authToken}") }
            .post(eff.requestBytes.toByteArray().toRequestBody(PROTO_MEDIA_TYPE))
            .build()
        return suspendCancellableCoroutine { cont ->
            val call = rpcHttp.newCall(req)
            cont.invokeOnCancellation { call.cancel() }
            call.enqueue(object : Callback {
                override fun onFailure(call: Call, e: IOException) {
                    if (cont.isActive) cont.resume(rpcErr(eff.opId, 14, errorMessage(e), networkError = true))
                }

                override fun onResponse(call: Call, response: Response) {
                    val result = response.use { res ->
                        val bodyBytes = res.body?.bytes() ?: ByteArray(0)
                        if (res.isSuccessful) rpcOk(eff.opId, bodyBytes)
                        else parseRpcError(eff.opId, res.code, bodyBytes, causeMsg = res.message)
                    }
                    if (cont.isActive) cont.resume(result)
                }
            })
        }
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
