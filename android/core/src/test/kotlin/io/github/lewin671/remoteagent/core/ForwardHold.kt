package io.github.lewin671.remoteagent.core

import com.sun.net.httpserver.HttpServer
import io.github.lewin671.remoteagent.proto.ForwardRequest
import kotlinx.coroutines.CompletableDeferred
import okhttp3.Request
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.RequestBody.Companion.toRequestBody
import java.net.InetSocketAddress
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors

/** Executes the first Forward on the real relay, but withholds its response.
 * Cancellation is observed by the client's actual HTTP stack, not a mock job. */
internal class ForwardHold(private val target: String) : AutoCloseable {
    private val server = HttpServer.create(InetSocketAddress("127.0.0.1", 0), 0)
    private val executor = Executors.newCachedThreadPool()
    private val release = CountDownLatch(1)
    private val http = Api.defaultHttp()
    val received = CopyOnWriteArrayList<ForwardRequest>()
    val held = CompletableDeferred<Unit>()
    val url = "http://127.0.0.1:${server.address.port}"
    init {
        server.executor = executor
        server.createContext("/") { exchange ->
            try {
                val bytes = exchange.requestBody.readBytes()
                received += ForwardRequest.parseFrom(bytes)
                val request = Request.Builder().url(target + exchange.requestURI.path)
                    .post(bytes.toRequestBody("application/proto".toMediaType()))
                exchange.requestHeaders.forEach { (name, values) ->
                    if (name.lowercase() !in setOf("host", "content-length", "transfer-encoding", "connection")) values.forEach { request.addHeader(name,it) }
                }
                http.newCall(request.build()).execute().use { response ->
                    val body = response.body!!.bytes()
                    if (received.size == 1) { held.complete(Unit); release.await() }
                    response.headers.forEach { (name, value) ->
                        if (name.lowercase() !in setOf("content-length", "transfer-encoding", "connection")) exchange.responseHeaders.add(name,value)
                    }
                    exchange.sendResponseHeaders(response.code, body.size.toLong())
                    exchange.responseBody.write(body)
                }
            } catch (_: Exception) { }
            finally { exchange.close() }
        }
        server.start()
    }
    override fun close() {
        release.countDown(); server.stop(0); executor.shutdownNow()
        http.connectionPool.evictAll(); http.dispatcher.executorService.shutdown()
    }
}
