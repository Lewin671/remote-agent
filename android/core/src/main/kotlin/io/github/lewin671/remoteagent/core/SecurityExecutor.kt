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
import io.github.lewin671.remoteagent.core.proto.EffectBatch
import io.github.lewin671.remoteagent.core.proto.Event as CoreEvent
import io.github.lewin671.remoteagent.core.proto.SecurityKeyAgreeEffect
import io.github.lewin671.remoteagent.core.proto.SecurityKeyAgreedEvent
import io.github.lewin671.remoteagent.core.proto.SecurityKeyDiscardEffect
import io.github.lewin671.remoteagent.core.proto.SecurityKeyEncapsulateEffect
import io.github.lewin671.remoteagent.core.proto.SecurityKeyEncapsulatedEvent
import io.github.lewin671.remoteagent.core.proto.SecurityKeyGenerateEffect
import io.github.lewin671.remoteagent.core.proto.SecurityKeyGeneratedEvent
import io.github.lewin671.remoteagent.core.proto.SecurityKeySignEffect
import io.github.lewin671.remoteagent.core.proto.SecurityKeySignedEvent
import io.github.lewin671.remoteagent.core.proto.SecurityOpenNotifyEvent
import io.github.lewin671.remoteagent.core.proto.SecurityRpcEffect
import io.github.lewin671.remoteagent.core.proto.SecurityRpcMethod
import io.github.lewin671.remoteagent.core.proto.SecurityRpcResultEvent
import io.github.lewin671.remoteagent.core.proto.SecurityStorageEffect
import io.github.lewin671.remoteagent.core.proto.SecurityStorageResultEvent
import io.github.lewin671.remoteagent.corebind.Corebind
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.NotifyContent
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.serialization.json.Json
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
        if (closed.get()) return
        emit(CoreEvent.newBuilder().apply(block).build())
    }

    fun keyGenerate(eff: SecurityKeyGenerateEffect) {
        if (closed.get()) return
        scope.launch {
            val ev = try {
                val pub = store.generateKey(eff.keyKind, eff.slot, eff.persist)
                SecurityKeyGeneratedEvent.newBuilder()
                    .setOpId(eff.opId)
                    .setPublicKey(ByteString.copyFrom(pub))
                    .build()
            } catch (t: Throwable) {
                if (t is CancellationException) throw t
                SecurityKeyGeneratedEvent.newBuilder()
                    .setOpId(eff.opId)
                    .setError(errorMessage(t))
                    .build()
            }
            emitIfOpen { setSecurityKeyGenerated(ev) }
        }
    }

    fun keySign(eff: SecurityKeySignEffect) {
        if (closed.get()) return
        scope.launch {
            val ev = try {
                val msg = eff.message.toByteArray()
                if (msg.isEmpty()) throw IllegalArgumentException("empty message to sign")
                val sig = store.signKey(eff.keyKind, eff.slot, msg)
                SecurityKeySignedEvent.newBuilder()
                    .setOpId(eff.opId)
                    .setSignature(ByteString.copyFrom(sig))
                    .build()
            } catch (t: Throwable) {
                if (t is CancellationException) throw t
                SecurityKeySignedEvent.newBuilder()
                    .setOpId(eff.opId)
                    .setError(errorMessage(t))
                    .build()
            }
            emitIfOpen { setSecurityKeySigned(ev) }
        }
    }

    fun keyAgree(eff: SecurityKeyAgreeEffect) {
        if (closed.get()) return
        scope.launch {
            val ev = try {
                val shared = store.agreeKey(eff.keyKind, eff.slot, eff.peerPublic.toByteArray())
                SecurityKeyAgreedEvent.newBuilder()
                    .setOpId(eff.opId)
                    .setShared(ByteString.copyFrom(shared))
                    .build()
            } catch (t: Throwable) {
                if (t is CancellationException) throw t
                SecurityKeyAgreedEvent.newBuilder()
                    .setOpId(eff.opId)
                    .setError(errorMessage(t))
                    .build()
            }
            emitIfOpen { setSecurityKeyAgreed(ev) }
        }
    }

    fun keyEncapsulate(eff: SecurityKeyEncapsulateEffect) {
        if (closed.get()) return
        scope.launch {
            val ev = try {
                val pairs = store.encapsulate(eff.recipientsList)
                SecurityKeyEncapsulatedEvent.newBuilder()
                    .setOpId(eff.opId)
                    .addAllPairs(pairs)
                    .build()
            } catch (t: Throwable) {
                if (t is CancellationException) throw t
                SecurityKeyEncapsulatedEvent.newBuilder()
                    .setOpId(eff.opId)
                    .setError(errorMessage(t))
                    .build()
            }
            emitIfOpen { setSecurityKeyEncapsulated(ev) }
        }
    }

    fun keyDiscard(eff: SecurityKeyDiscardEffect) {
        if (closed.get()) return
        scope.launch {
            runCatching { store.discardKey(eff.keyKind, eff.slot) }
        }
    }

    fun storage(eff: SecurityStorageEffect) {
        if (closed.get()) return
        scope.launch {
            val res = try {
                store.executeStorage(eff)
            } catch (t: Throwable) {
                if (t is CancellationException) throw t
                SecurityStorageResultEvent.newBuilder()
                    .setOpId(eff.opId)
                    .setError(errorMessage(t))
                    .build()
            }
            emitIfOpen { setSecurityStorageResult(res) }
        }
    }

    fun rpc(eff: SecurityRpcEffect) {
        if (closed.get()) return
        scope.launch {
            val res = try {
                executeRpcUnary(eff)
            } catch (t: Throwable) {
                if (t is CancellationException) throw t
                SecurityRpcResultEvent.newBuilder()
                    .setOpId(eff.opId)
                    .setConnectCode(2)
                    .setError(errorMessage(t))
                    .build()
            }
            emitIfOpen { setSecurityRpcResult(res) }
        }
    }

    private suspend fun executeRpcUnary(eff: SecurityRpcEffect): SecurityRpcResultEvent {
        val path = SECURITY_RPC_PATHS[eff.method]
            ?: return SecurityRpcResultEvent.newBuilder()
                .setOpId(eff.opId)
                .setConnectCode(12)
                .setError("unknown rpc method: ${eff.method.number}")
                .build()
        val server = Secret.checkServer(eff.server, allowPlainHttp = allowPlainHttp)
        val url = server.trimEnd('/') + path
        if (rpcTransport != null) {
            val headers = buildMap<String, List<String>> {
                put("Content-Type", listOf("application/proto"))
                put("Connect-Protocol-Version", listOf("1"))
                if (eff.authToken.isNotEmpty()) {
                    put("Authorization", listOf("Bearer ${eff.authToken}"))
                }
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
                    val result = when {
                        status != null && status in 200..299 && cause == null ->
                            SecurityRpcResultEvent.newBuilder()
                                .setOpId(eff.opId)
                                .setResponseBytes(ByteString.copyFrom(bodyBytes))
                                .build()
                        cause != null && status == null ->
                            SecurityRpcResultEvent.newBuilder()
                                .setOpId(eff.opId)
                                .setConnectCode(cause.code.value)
                                .setError(cause.message ?: cause.code.codeName)
                                .setNetworkError(cause.code == Code.UNAVAILABLE)
                                .build()
                        else -> {
                            val httpCode = status ?: 502
                            var code = cause?.code?.value ?: connectCodeFromHttpStatus(httpCode)
                            var msg = cause?.message?.takeIf { it.isNotEmpty() } ?: "HTTP $httpCode"
                            val text = runCatching { bodyBytes.decodeToString() }.getOrNull()
                            if (!text.isNullOrBlank()) {
                                val parsed = runCatching { Json.parseToJsonElement(text) as? JsonObject }.getOrNull()
                                if (parsed != null) {
                                    parsed["code"]?.jsonPrimitive?.contentOrNull?.let { codeStr ->
                                        CONNECT_CODE_MAP[codeStr]?.let { code = it }
                                    }
                                    parsed["message"]?.jsonPrimitive?.contentOrNull?.takeIf { it.isNotEmpty() }?.let {
                                        msg = it
                                    }
                                }
                            }
                            SecurityRpcResultEvent.newBuilder()
                                .setOpId(eff.opId)
                                .setConnectCode(code)
                                .setError(msg)
                                .build()
                        }
                    }
                    if (cont.isActive) cont.resume(result)
                }
                cont.invokeOnCancellation { cancel() }
            }
        }
        val reqBuilder = Request.Builder()
            .url(url)
            .header("Content-Type", "application/proto")
            .header("Connect-Protocol-Version", "1")
            .post(eff.requestBytes.toByteArray().toRequestBody(PROTO_MEDIA_TYPE))
        if (eff.authToken.isNotEmpty()) {
            reqBuilder.header("Authorization", "Bearer ${eff.authToken}")
        }
        val req = reqBuilder.build()
        return suspendCancellableCoroutine { cont ->
            val call = rpcHttp.newCall(req)
            cont.invokeOnCancellation { call.cancel() }
            call.enqueue(object : Callback {
                override fun onFailure(call: Call, e: IOException) {
                    if (!cont.isActive) return
                    cont.resume(
                        SecurityRpcResultEvent.newBuilder()
                            .setOpId(eff.opId)
                            .setConnectCode(14)
                            .setError(errorMessage(e))
                            .setNetworkError(true)
                            .build(),
                    )
                }

                override fun onResponse(call: Call, response: Response) {
                    val result = response.use { res ->
                        val bodyBytes = res.body?.bytes() ?: ByteArray(0)
                        if (res.isSuccessful) {
                            SecurityRpcResultEvent.newBuilder()
                                .setOpId(eff.opId)
                                .setResponseBytes(ByteString.copyFrom(bodyBytes))
                                .build()
                        } else {
                            var code = connectCodeFromHttpStatus(res.code)
                            var msg = res.message.ifEmpty { "HTTP ${res.code}" }
                            val text = runCatching { bodyBytes.decodeToString() }.getOrNull()
                            if (!text.isNullOrBlank()) {
                                val parsed = runCatching { Json.parseToJsonElement(text) as? JsonObject }.getOrNull()
                                if (parsed != null) {
                                    parsed["code"]?.jsonPrimitive?.contentOrNull?.let { codeStr ->
                                        CONNECT_CODE_MAP[codeStr]?.let { code = it }
                                    }
                                    parsed["message"]?.jsonPrimitive?.contentOrNull?.takeIf { it.isNotEmpty() }?.let {
                                        msg = it
                                    }
                                }
                            }
                            SecurityRpcResultEvent.newBuilder()
                                .setOpId(eff.opId)
                                .setConnectCode(code)
                                .setError(msg)
                                .build()
                        }
                    }
                    if (cont.isActive) cont.resume(result)
                }
            })
        }
    }
}

/**
 * Decodes and verifies a push notification [Envelope] using Go `clientcore` Path A (`notify_blob`).
 * Runs in a short-lived sandboxed Core instance without needing unlocked device private keys.
 */
fun openNotifyEnvelopeViaCore(
    notifyBlob: ByteArray,
    envelope: Envelope,
    nowMs: Long = System.currentTimeMillis(),
    sessionId: String = "",
    tag: String = "",
    machineId: String = "",
): NotifyContent? {
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
                    .setMachineId(machineId),
            )
            .build()
        val batchBytes: ByteArray? = core.dispatch(ev.toByteArray())
        if (batchBytes == null || batchBytes.isEmpty()) return null
        val batch = EffectBatch.parseFrom(batchBytes)
        for (eff in batch.effectsList) {
            if (eff.hasSecurityNotifyOpened()) {
                val opened = eff.securityNotifyOpened
                if (opened.error.isEmpty() && opened.hasContent()) {
                    return opened.content
                }
            }
        }
        null
    } finally {
        core.close()
    }
}
