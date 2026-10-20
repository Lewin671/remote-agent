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
import io.github.lewin671.remoteagent.proto.RecoveryServiceClient
import io.github.lewin671.remoteagent.proto.RegisterDeviceRequest
import java.util.concurrent.TimeUnit
import okhttp3.OkHttpClient

/**
 * A device of the account. [root] and [deviceSeed] are secrets: the app keeps them wrapped with a
 * Keystore key and never logs them.
 */
class Account(
    val server: String,
    /** The account root is absent on a phone that only joined or recovered; empty bytes mean so. */
    val root: ByteArray,
    val deviceSeed: ByteArray,
    val deviceId: String,
    val accountId: String =
        if (root.size == KEY_SIZE) deviceId(Keys.derive(root).account.publicKey) else "",
    /** Public only; empty when this phone has neither the root nor a pairing payload with it. */
    val accountPublicKey: ByteArray =
        if (root.size == KEY_SIZE) Keys.derive(root).account.publicKey else ByteArray(0),
) {
    val hasRoot: Boolean
        get() = root.size == KEY_SIZE

    /** Local per-account state keys; rootless phones use the account ID so accounts stay apart. */
    val localKey: ByteArray
        get() = if (hasRoot) root else accountId.toByteArray(Charsets.UTF_8)

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
    private val source = transport ?: ConnectOkHttpClient(
        // Never replay login, registration or pairing bodies at another URL.
        http.newBuilder().followRedirects(false).followSslRedirects(false).build(),
    )
    private val authenticatedTransport = object : com.connectrpc.http.HTTPClientInterface by source {
        override fun unary(
            request: com.connectrpc.http.UnaryHTTPRequest,
            onResult: (com.connectrpc.http.HTTPResponse) -> Unit,
        ): () -> Unit = source.unary(request) { response ->
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
    enrollment: Enrollment? = null,
    seed: ByteArray? = null,
    signupProof: io.github.lewin671.remoteagent.proto.SignupProof? = null,
    request: RegisterDeviceRequest? = null,
    beforeRegister: (RegisterDeviceRequest) -> Unit = { throw DeviceSecurity.Failure("storage") },
): Account {
    val keys = Keys.derive(root)
    val deviceSeed = enrollment?.seed ?: seed ?: randomBytes(32)
    val device = SigningKey(deviceSeed)
    if (enrollment != null) verifyPairApproval(enrollment, root, kind, now)
    return registerDevice(
        server,
        http,
        keys.account.publicKey,
        deviceSeed,
        kind,
        now,
        setupToken,
        enrollment?.approval,
        enrollment?.signature,
        signupProof = signupProof,
        accountSignature =
            keys.account.sign(
                registerMessage(
                    registrationBytes(keys.account.publicKey, device.publicKey, kind, now),
                ),
            ),
        accountId = deviceId(keys.account.publicKey),
        root = root.copyOf(),
        accountKey = keys.account.publicKey,
        request = request,
        beforeRegister = beforeRegister,
    )
}

/** The account public key is enough to name an existing account; no root is sent or needed. */
suspend fun registerDevice(
    server: String,
    http: OkHttpClient,
    accountPublicKey: ByteArray,
    deviceSeed: ByteArray,
    kind: DeviceKind = DeviceKind.DEVICE_KIND_CLIENT,
    now: Long = System.currentTimeMillis(),
    setupToken: String = "",
    approval: ByteArray? = null,
    approvalSignature: ByteArray? = null,
    signupProof: io.github.lewin671.remoteagent.proto.SignupProof? = null,
    accountSignature: ByteArray = ByteArray(0),
    accountId: String = "",
    root: ByteArray = ByteArray(0),
    accountKey: ByteArray = ByteArray(0),
    request: RegisterDeviceRequest? = null,
    beforeRegister: (RegisterDeviceRequest) -> Unit = { throw DeviceSecurity.Failure("storage") },
): Account {
    val device = SigningKey(deviceSeed)
    val reg = registrationBytes(accountPublicKey, device.publicKey, kind, now)
    val exact =
        request
            ?: RegisterDeviceRequest.newBuilder()
                .setProtocolVersion(PROTOCOL_VERSION)
                .setRegistration(ByteString.copyFrom(reg))
                .setSignature(ByteString.copyFrom(accountSignature))
                .setSetupToken(setupToken)
                .setApproval(ByteString.copyFrom(approval ?: byteArrayOf()))
                .setApprovalSignature(ByteString.copyFrom(approvalSignature ?: byteArrayOf()))
                .apply { signupProof?.let { setSignupProof(it) } }
                .build()
    beforeRegister(exact)
    val res = Api(server, http).auth.registerDevice(exact, emptyMap()).value()
    if (res.deviceId != deviceId(device.publicKey))
        throw IllegalStateException("server 返回了意外的设备 ID")
    return Account(server, root, deviceSeed.copyOf(), res.deviceId, accountId, accountKey)
}

private fun registrationBytes(
    accountPublicKey: ByteArray,
    devicePublicKey: ByteArray,
    kind: DeviceKind,
    now: Long,
): ByteArray =
    DeviceRegistration.newBuilder()
        .setAccountPublicKey(ByteString.copyFrom(accountPublicKey))
        .setDevicePublicKey(ByteString.copyFrom(devicePublicKey))
        .setKind(kind)
        .setCreatedMs(now)
        .build()
        .toByteArray()

/** Logs in with the device key and returns a bearer token. */
suspend fun login(api: Api, account: Account): String = loginResponse(api, account).token

/** Logs in with the device key and returns the token with its expiry. */
suspend fun loginResponse(api: Api, account: Account): LoginResponse {
    val ch =
        api.auth
            .getChallenge(
                GetChallengeRequest.newBuilder().setDeviceId(account.deviceId).build(),
                emptyMap(),
            )
            .value()
    val sig = SigningKey(account.deviceSeed).sign(loginMessage(ch.challenge.toByteArray()))
    return api.auth
        .login(
            LoginRequest.newBuilder()
                .setProtocolVersion(PROTOCOL_VERSION)
                .setDeviceId(account.deviceId)
                .setChallenge(ch.challenge)
                .setSignature(ByteString.copyFrom(sig))
                .build(),
            emptyMap(),
        )
        .value()
}

/** A daemon's answer, or a failure to get one. [code] is a Connect code name. */
class CommandException(val code: String, message: String, val fromDaemon: Boolean = false) :
    Exception(message) {
    // fromDaemon: the daemon's own answer for this request, the final word
    // on it (except that it expired before reaching the daemon).

    /**
     * The command may or may not have run; retrying with the same request ID is safe. A daemon that
     * stopped halfway through it answers "aborted" ever after.
     */
    val unknown: Boolean
        get() = code == UNKNOWN || (fromDaemon && code == "aborted")

    companion object {
        const val UNKNOWN = "unknown_outcome"
        const val UNAVAILABLE = "unavailable"
        const val EXPIRED = "expired"
    }
}

/** Short text for an error, without content. */
fun errorText(e: Throwable, state: RemoteState? = null): String =
    when (e) {
        is DeviceSecurity.Failure ->
            when (e.code) {
                "unavailable" -> "等待所属电脑更新当前状态：这台设备没有当时的内容密钥"
                "invalid",
                "untrusted" -> "密文无效：签名、密钥或内容校验失败"
                "conflict" -> "成员链冲突，安全操作已停止；请核对设备后明确重新加入"
                "lease" -> "管理端租约已过期，无法保存更改、控制会话或上传新内容；${state?.renewalAdvice ?: "请在当前管理端续期"}后重试。历史仍可查看"
                "clock" -> "手机时钟比服务器快，成员状态暂不可用；请校准系统时间"
                else -> e.message ?: "安全状态不可用"
            }
        is CommandException -> e.message ?: e.code
        // A request that never reached the server carries the network failure
        // as its cause, and that failure's own message as its text.
        is ConnectException ->
            networkText(e.exception)
                ?: when (e.code) {
                    Code.UNAVAILABLE -> "连不上服务器" + (e.message?.let { "：$it" } ?: "")
                    Code.UNAUTHENTICATED -> "登录失效：" + (e.message ?: "")
                    else -> e.message ?: e.code.codeName
                }
        else -> networkText(e) ?: e.message ?: e.javaClass.simpleName
    }

/** The failure as the system worded it, for the details of a lost connection; without content. */
fun detailText(e: Throwable): String {
    val cause = (e as? ConnectException)?.exception ?: e
    return listOfNotNull(
            cause.javaClass.simpleName.takeIf { cause.javaClass != Exception::class.java },
            cause.message,
        )
        .joinToString(": ")
}

/**
 * The server's address did not resolve: on a phone, the network is down whatever the status bar
 * shows.
 */
fun unresolvedHost(e: Throwable): Boolean =
    ((e as? ConnectException)?.exception ?: e) is java.net.UnknownHostException

private fun networkText(e: Throwable?): String? =
    when (e) {
        // No address for the server: on a phone, nearly always no network.
        is java.net.UnknownHostException -> "找不到服务器，检查一下网络"
        is java.net.ConnectException -> "连不上服务器"
        is java.net.SocketTimeoutException -> "连接超时"
        is javax.net.ssl.SSLException -> "安全连接失败：" + (e.message ?: "")
        else -> null
    }
