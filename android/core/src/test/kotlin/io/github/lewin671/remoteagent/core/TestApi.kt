package io.github.lewin671.remoteagent.core

import com.google.protobuf.MessageLite
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.Authenticator
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody

private val TEST_PROTO_MEDIA_TYPE = "application/proto".toMediaType()

internal class TestRpcException(val connectCode: Int, message: String) : Exception(message)

/** Test-only unary HTTP+protobuf client (production RPCs run through Core + SecurityExecutor). */
internal class TestApi(
    server: String,
    http: OkHttpClient,
) {
    val server = Secret.checkServer(server, allowPlainHttp = true)
    private val rpcHttp = http.newBuilder()
        .retryOnConnectionFailure(false).followRedirects(false).followSslRedirects(false)
        .authenticator(Authenticator.NONE).proxyAuthenticator(Authenticator.NONE)
        .apply { interceptors().add(0, OneShotRPCBody) }.build()

    suspend fun <Resp : MessageLite> unary(
        path: String,
        request: MessageLite,
        parse: (ByteArray) -> Resp,
        token: String = "",
    ): Resp = withContext(Dispatchers.IO) {
        val req = Request.Builder()
            .url(server.trimEnd('/') + path)
            .header("Content-Type", "application/proto")
            .header("Connect-Protocol-Version", "1")
            .apply { if (token.isNotEmpty()) header("Authorization", "Bearer $token") }
            .post(request.toByteArray().toRequestBody(TEST_PROTO_MEDIA_TYPE))
            .build()
        rpcHttp.newCall(req).execute().use { res ->
            val body = res.body?.bytes() ?: ByteArray(0)
            if (!res.isSuccessful) {
                val parsed = parseRelayRpcError(0L, res.code, body, causeMsg = res.message)
                throw TestRpcException(parsed.connectCode, parsed.diagnosticMessage)
            }
            parse(body)
        }
    }
}
