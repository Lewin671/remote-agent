package io.github.lewin671.remoteagent.core

import com.connectrpc.ProtocolClientConfig
import com.connectrpc.ResponseMessage
import com.connectrpc.getOrThrow
import com.connectrpc.impl.ProtocolClient
import com.connectrpc.okhttp.ConnectOkHttpClient
import com.connectrpc.protocols.NetworkProtocol
import io.github.lewin671.remoteagent.proto.AuthServiceClient
import io.github.lewin671.remoteagent.proto.ClientServiceClient
import io.github.lewin671.remoteagent.proto.PairingServiceClient
import io.github.lewin671.remoteagent.proto.RecoveryServiceClient
import okhttp3.Authenticator
import okhttp3.OkHttpClient

/** Test-only ConnectRPC client wrapper (production RPCs run through Core + SecurityExecutor). */
internal class TestApi(
    server: String,
    http: OkHttpClient,
    transport: com.connectrpc.http.HTTPClientInterface? = null,
) {
    val server = Secret.checkServer(server, allowPlainHttp = true)
    private val rpcHttp = http.newBuilder()
        .retryOnConnectionFailure(false).followRedirects(false).followSslRedirects(false)
        .authenticator(Authenticator.NONE).proxyAuthenticator(Authenticator.NONE)
        .apply { interceptors().add(0, OneShotRPCBody) }.build()
    private val source = transport ?: ConnectOkHttpClient(rpcHttp)
    internal val protocol = ProtocolClient(
        source,
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
    }
}

internal fun <T> ResponseMessage<T>.value(): T = getOrThrow()
