// ConnectRPC clients for the server's services, and registering a device.
package io.github.lewin671.remoteagent.core

import com.connectrpc.Code
import com.connectrpc.ConnectException
import com.connectrpc.ProtocolClientConfig
import com.connectrpc.ResponseMessage
import com.connectrpc.getOrThrow
import com.connectrpc.impl.ProtocolClient
import com.connectrpc.okhttp.ConnectOkHttpClient
import com.connectrpc.protocols.NetworkProtocol
import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.proto.SecurityKeyKind
import io.github.lewin671.remoteagent.proto.AuthServiceClient
import io.github.lewin671.remoteagent.proto.ClientServiceClient
import io.github.lewin671.remoteagent.proto.GetChallengeRequest
import io.github.lewin671.remoteagent.proto.LoginRequest
import io.github.lewin671.remoteagent.proto.LoginResponse
import io.github.lewin671.remoteagent.proto.PairingServiceClient
import io.github.lewin671.remoteagent.proto.RecoveryServiceClient
import java.util.concurrent.TimeUnit
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody
import okhttp3.Interceptor
import okhttp3.Authenticator
import okhttp3.Callback
import okhttp3.Call
import okhttp3.Response
import java.io.IOException
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.atomic.AtomicReference
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlin.coroutines.resume
import kotlin.coroutines.resumeWithException

class SecurityFailure(val code: String, message: String = code) : Exception(message)

/**
 * Identity of an enrolled account on this device. All private keys remain inside the host
 * [SecurityStoreV4] boundary and never cross into app memory via [Account].
 */
data class Account(
    val server: String,
    val deviceId: String,
    val accountId: String = "",
) {
    val localKey: ByteArray
        get() = accountId.ifEmpty { deviceId }.toByteArray(Charsets.UTF_8)

    override fun toString(): String = "Account(server=$server, device=$deviceId)"
}

/** The server's services over the Connect protocol with binary protobuf. */
class Api(
    server: String,
    http: OkHttpClient,
    onUnauthenticated: ((String) -> Unit)? = null,
    transport: com.connectrpc.http.HTTPClientInterface? = null,
) {
    // 10.0.2.2 is the emulator's host loopback. Release Android network
    // policy still blocks all cleartext; no remote HTTP origin is accepted.
    val server = Secret.checkServer(server, allowPlainHttp = true)
    private val observedEpoch = AtomicReference("")
    private val rpcHttp = http.newBuilder()
        .retryOnConnectionFailure(false).followRedirects(false).followSslRedirects(false)
        .authenticator(Authenticator.NONE).proxyAuthenticator(Authenticator.NONE)
        .apply { interceptors().add(0, OneShotRPCBody) }.build()
    private val source = transport ?: ConnectOkHttpClient(rpcHttp)

    suspend fun relayEpoch(): String {
        observedEpoch.get().takeIf { it.isNotEmpty() }?.let { return it }
        return suspendCancellableCoroutine { continuation ->
            val call = rpcHttp.newCall(Request.Builder().url(server.trimEnd('/') + "/healthz").build())
            continuation.invokeOnCancellation { call.cancel() }
            call.enqueue(object : Callback {
                override fun onFailure(call: Call, e: IOException) { if (continuation.isActive) continuation.resumeWithException(e) }
                override fun onResponse(call: Call, response: Response) {
                    val epoch = response.use { it.header(RELAY_EPOCH_HEADER).orEmpty() }
                    if (epoch.isNotEmpty()) observedEpoch.set(epoch)
                    if (continuation.isActive) {
                        if (epoch.isEmpty()) continuation.resumeWithException(CommandException("unavailable", UiMessages.errorsTheRelayDidNotProvideAVerifiableExecutionResult().messageText()))
                        else continuation.resume(epoch)
                    }
                }
            })
        }
    }
    private val authenticatedTransport = object : com.connectrpc.http.HTTPClientInterface by source {
        override fun unary(
            request: com.connectrpc.http.UnaryHTTPRequest,
            onResult: (com.connectrpc.http.HTTPResponse) -> Unit,
        ): () -> Unit = source.unary(request) { response ->
            response.headers.entries.firstOrNull { it.key.equals(RELAY_EPOCH_HEADER, true) }?.value?.firstOrNull()?.takeIf { it.isNotEmpty() }?.let(observedEpoch::set)
            if (response.status == 401) {
                request.headers.entries.firstOrNull { it.key.equals("Authorization", true) }
                    ?.value?.firstOrNull()?.removePrefix("Bearer ")?.takeIf { it.isNotEmpty() }
                    ?.let { onUnauthenticated?.invoke(it) }
            }
            onResult(response)
        }
    }
    private val protocol =
        ProtocolClient(
            authenticatedTransport,
            ProtocolClientConfig(
                host = this.server,
                serializationStrategy = LiteProtobufStrategy,
                networkProtocol = NetworkProtocol.CONNECT,
            ),
        )
    val auth = AuthServiceClient(protocol)
    val client = ClientServiceClient(protocol)
    val pairing = PairingServiceClient(protocol)
    val recovery = RecoveryServiceClient(protocol)

    companion object {
        fun headers(token: String): Map<String, List<String>> =
            if (token.isEmpty()) emptyMap() else mapOf("Authorization" to listOf("Bearer $token"))

        /** An HTTP client with the timeouts the protocol expects: Forward waits up to 30 s. */
        fun defaultHttp(): OkHttpClient =
            OkHttpClient.Builder()
                .retryOnConnectionFailure(false)
                .followRedirects(false)
                .followSslRedirects(false)
                .connectTimeout(15, TimeUnit.SECONDS)
                .readTimeout(45, TimeUnit.SECONDS)
                .writeTimeout(30, TimeUnit.SECONDS)
                .pingInterval(0, TimeUnit.SECONDS)
                .build()
    }
}

fun <T> ResponseMessage<T>.value(): T = getOrThrow()

/** Logs in using the host [SecurityStoreV4] device Ed25519 key and returns the token with its expiry. */
suspend fun loginWithStore(api: Api, deviceId: String, store: SecurityStoreV4): LoginResponse {
    val ch = api.auth
        .getChallenge(
            GetChallengeRequest.newBuilder().setDeviceId(deviceId).build(),
            emptyMap(),
        )
        .value()
    val sig = store.signKey(
        SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_ED,
        "",
        loginMessage(ch.challenge.toByteArray()),
    )
    return api.auth
        .login(
            LoginRequest.newBuilder()
                .setProtocolVersion(RELAY_PROTOCOL_VERSION)
                .setDeviceId(deviceId)
                .setChallenge(ch.challenge)
                .setSignature(ByteString.copyFrom(sig))
                .build(),
            emptyMap(),
        )
        .value()
}

/** A daemon's answer, or a failure to get one. [code] is a Connect code name. */
enum class CommandFailureKind { RAW, CONNECTION_DROPPED, REPLY_UNVERIFIED, UNKNOWN, COMPUTER_OFFLINE, IMAGE_MISSING, EXECUTED_RESULT_UNAVAILABLE, IDENTITY_CONFLICT }

class CommandException(
    val code: String,
    message: String,
    val fromDaemon: Boolean = false,
    val failureKind: CommandFailureKind = CommandFailureKind.RAW,
    override val imageKind: io.github.lewin671.remoteagent.core.proto.ImageProblemKind? = null,
) : Exception(message), MessageFailure {
    override var display: MessageText = when (code) {
        "disk_full" -> UiMessages.machineStatusDiskFull().messageText()
        "storage_exhausted" -> UiMessages.machineStatusStorageExhausted().messageText()
        else -> message.messageText()
    }
        private set

    constructor(
        code: String,
        message: MessageText,
        fromDaemon: Boolean = false,
        failureKind: CommandFailureKind = CommandFailureKind.RAW,
        imageKind: io.github.lewin671.remoteagent.core.proto.ImageProblemKind? = null,
    ) : this(code, "command_failure", fromDaemon, failureKind, imageKind) {
        display = message
    }

    companion object {
        const val UNKNOWN = "unknown_outcome"
        const val UNAVAILABLE = "unavailable"
        const val EXPIRED = "expired"
    }
}

/** Short text for an error, without content. */
fun errorText(e: Throwable, state: RemoteState? = null): MessageText = localized { copy ->
    when (e) {
        is MessageFailure -> e.display.resolve(copy)
        is SecurityFailure -> when (e.code) {
            "unavailable" -> copy(UiMessages.errorsWaitingForTheOwningComputerToUpdateTheCurrentRemote())
            "invalid", "untrusted" -> copy(UiMessages.errorsInvalidCiphertextSignatureKeyOrContentVerificationFailed())
            "conflict" -> copy(UiMessages.errorsMembershipChainConflictSecurityOperationsStoppedVerifyDevicesAnd())
            "lease" -> copy(UiMessages.errorsAndroidRemoteControlAuthorizationExpiredCannotSaveChangesControlSessionsOr(
                state?.renewalAdvice?.resolve(copy) ?: copy(UiMessages.securityAndroidRenewOnAuthority())))
            "clock" -> copy(UiMessages.errorsAndroidThePhoneClockIsAheadOfTheServerMembershipState())
            else -> e.message ?: copy(UiMessages.errorsAndroidSecurityStateUnavailable())
        }
        is ConnectException -> networkText(e.exception, copy) ?: when (e.code) {
            Code.UNAVAILABLE -> e.message?.let { copy(UiMessages.errorsAndroidServerUnavailableReason(it)) } ?: copy(UiMessages.connectionNoServer())
            Code.PERMISSION_DENIED -> if (e.message == LOGIN_FAILED) copy(UiMessages.errorsTheServerDoesNotRecognizeThisDeviceItMay()) else e.message ?: e.code.codeName
            Code.UNAUTHENTICATED -> copy(UiMessages.errorsAndroidLoginExpired(e.message.orEmpty()))
            else -> e.message ?: e.code.codeName
        }
        else -> networkText(e, copy) ?: e.message ?: e.javaClass.simpleName
    }
}

private const val LOGIN_FAILED = "login failed"

/**
 * The server's address did not resolve: on a phone, the network is down whatever the status bar
 * shows.
 */
fun unresolvedHost(e: Throwable): Boolean =
    ((e as? ConnectException)?.exception ?: e) is java.net.UnknownHostException

private fun networkText(e: Throwable?, copy: MessageResolver): String? = when (e) {
    is java.net.UnknownHostException -> copy(UiMessages.errorsAndroidCannotFindTheServerCheckYourNetwork())
    is java.net.ConnectException -> copy(UiMessages.connectionNoServer())
    is java.net.SocketTimeoutException -> copy(UiMessages.errorsConnectionTimedOut())
    is javax.net.ssl.SSLException -> copy(UiMessages.errorsAndroidSecureConnectionFailed(e.message.orEmpty()))
    else -> null
}

/** Only meaningful for an error returned by a login attempt. */
fun loginDenied(error: Throwable): Boolean =
    error is ConnectException && error.code == Code.PERMISSION_DENIED

internal const val RELAY_EPOCH_HEADER = "Remote-Agent-Relay-Epoch"

/** Outermost interceptor: inner interceptors/follow-ups share the same body guard. */
private object OneShotRPCBody : Interceptor {
    override fun intercept(chain: Interceptor.Chain): Response {
        val request = chain.request()
        val body = request.body ?: return chain.proceed(request)
        val written = AtomicBoolean()
        val once = object : RequestBody() {
            override fun contentType() = body.contentType()
            override fun contentLength() = body.contentLength()
            override fun isOneShot() = true
            override fun isDuplex() = body.isDuplex()
            override fun writeTo(sink: okio.BufferedSink) {
                if (!written.compareAndSet(false, true)) throw IOException("request_body_replayed")
                body.writeTo(sink)
            }
        }
        return chain.proceed(request.newBuilder().method(request.method, once).build())
    }
}
