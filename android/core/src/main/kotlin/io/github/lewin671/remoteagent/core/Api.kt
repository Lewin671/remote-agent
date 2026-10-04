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
import io.github.lewin671.remoteagent.proto.AuthServiceClient
import io.github.lewin671.remoteagent.proto.ClientServiceClient
import io.github.lewin671.remoteagent.proto.DeviceKind
import io.github.lewin671.remoteagent.proto.DeviceRegistration
import io.github.lewin671.remoteagent.proto.GetChallengeRequest
import io.github.lewin671.remoteagent.proto.LoginRequest
import io.github.lewin671.remoteagent.proto.LoginResponse
import io.github.lewin671.remoteagent.proto.PairingServiceClient
import io.github.lewin671.remoteagent.proto.RegisterDeviceRequest
import okhttp3.OkHttpClient
import java.util.concurrent.TimeUnit

/**
 * A device of the account. [root] and [deviceSeed] are secrets: the app keeps
 * them wrapped with a Keystore key and never logs them.
 */
class Account(
    val server: String,
    val root: ByteArray,
    val deviceSeed: ByteArray,
    val deviceId: String,
) {
    override fun toString(): String = "Account(server=$server, device=$deviceId)"
}

/** The server's services over the Connect protocol with binary protobuf. */
class Api(server: String, http: OkHttpClient) {
    // 10.0.2.2 is the emulator's host loopback. Release Android network
    // policy still blocks all cleartext; no remote HTTP origin is accepted.
    val server = Secret.checkServer(server, allowPlainHttp = true)
    private val protocol = ProtocolClient(
        // Never replay login, registration or pairing bodies at another URL.
        ConnectOkHttpClient(http.newBuilder().followRedirects(false).followSslRedirects(false).build()),
        ProtocolClientConfig(
            host = this.server,
            serializationStrategy = LiteProtobufStrategy,
            networkProtocol = NetworkProtocol.CONNECT,
        ),
    )
    val auth = AuthServiceClient(protocol)
    val client = ClientServiceClient(protocol)
    val pairing = PairingServiceClient(protocol)

    companion object {
        fun headers(token: String): Map<String, List<String>> =
            if (token.isEmpty()) emptyMap() else mapOf("Authorization" to listOf("Bearer $token"))

        /** An HTTP client with the timeouts the protocol expects: Forward waits up to 30 s. */
        fun defaultHttp(): OkHttpClient = OkHttpClient.Builder()
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

/** Registers a new device of the account with root key [root] and logs nothing secret. */
suspend fun joinWithRoot(
    root: ByteArray,
    server: String,
    http: OkHttpClient,
    setupToken: String = "",
    kind: DeviceKind = DeviceKind.DEVICE_KIND_CLIENT,
    now: Long = System.currentTimeMillis(),
): Account {
    val keys = Keys.derive(root)
    val seed = randomBytes(32)
    val device = SigningKey(seed)
    val reg = DeviceRegistration.newBuilder()
        .setAccountPublicKey(ByteString.copyFrom(keys.account.publicKey))
        .setDevicePublicKey(ByteString.copyFrom(device.publicKey))
        .setKind(kind)
        .setCreatedMs(now)
        .build()
        .toByteArray()
    val api = Api(server, http)
    val res = api.auth.registerDevice(
        RegisterDeviceRequest.newBuilder()
            .setRegistration(ByteString.copyFrom(reg))
            .setSignature(ByteString.copyFrom(keys.account.sign(registerMessage(reg))))
            .setSetupToken(setupToken)
            .build(),
        emptyMap(),
    ).value()
    if (res.deviceId != deviceId(device.publicKey)) throw IllegalStateException("server 返回了意外的设备 ID")
    return Account(server, root.copyOf(), seed, res.deviceId)
}

/** Logs in with the device key and returns a bearer token. */
suspend fun login(api: Api, account: Account): String = loginResponse(api, account).token

/** Logs in with the device key and returns the token with its expiry. */
suspend fun loginResponse(api: Api, account: Account): LoginResponse {
    val ch = api.auth.getChallenge(GetChallengeRequest.newBuilder().setDeviceId(account.deviceId).build(), emptyMap()).value()
    val sig = SigningKey(account.deviceSeed).sign(loginMessage(ch.challenge.toByteArray()))
    return api.auth.login(
        LoginRequest.newBuilder()
            .setDeviceId(account.deviceId)
            .setChallenge(ch.challenge)
            .setSignature(ByteString.copyFrom(sig))
            .build(),
        emptyMap(),
    ).value()
}

/** A daemon's answer, or a failure to get one. [code] is a Connect code name. */
class CommandException(val code: String, message: String, val fromDaemon: Boolean = false) : Exception(message) {
    // fromDaemon: the daemon's own answer for this request, the final word
    // on it (except that it expired before reaching the daemon).

    /** The command may or may not have run; retrying with the same request ID is safe. */
    val unknown: Boolean get() = code == UNKNOWN

    companion object {
        const val UNKNOWN = "unknown_outcome"
        const val UNAVAILABLE = "unavailable"
        const val EXPIRED = "expired"
    }
}

/** Short text for an error, without content. */
fun errorText(e: Throwable): String = when (e) {
    is CommandException -> e.message ?: e.code
    is ConnectException -> when (e.code) {
        Code.UNAVAILABLE -> "连不上服务器" + (e.message?.let { "：$it" } ?: "")
        Code.UNAUTHENTICATED -> "登录失效：" + (e.message ?: "")
        else -> e.message ?: e.code.codeName
    }
    is java.net.UnknownHostException -> "找不到服务器"
    is java.net.ConnectException -> "连不上服务器"
    is java.net.SocketTimeoutException -> "连接超时"
    is javax.net.ssl.SSLException -> "安全连接失败：" + (e.message ?: "")
    else -> e.message ?: e.javaClass.simpleName
}
