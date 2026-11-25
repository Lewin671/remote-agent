package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.GetChallengeRequest
import kotlinx.coroutines.runBlocking
import okhttp3.OkHttpClient
import org.junit.Assert.*
import org.junit.Test
import java.net.InetAddress
import java.net.ServerSocket
import java.util.concurrent.CompletableFuture
import java.util.concurrent.TimeUnit

class TransportSecurityTest {
    @get:org.junit.Rule val hangGuard = hangGuard()
    @Test fun rpcDoesNotFollowRedirectsEvenWithAnInjectedPermissiveClient() = runBlocking {
        ServerSocket(0, 4, InetAddress.getLoopbackAddress()).use { server ->
            server.soTimeout = 1500
            val requests = CompletableFuture.supplyAsync {
                var count = 0
                try {
                    while (true) {
                        server.accept().use { socket ->
                            count++
                            socket.soTimeout = 1500
                            val input = socket.getInputStream().bufferedReader()
                            while (!input.readLine().isNullOrEmpty()) { }
                            socket.getOutputStream().write(("HTTP/1.1 307 Temporary Redirect\r\n" +
                                "Location: http://127.0.0.1:${server.localPort}/leak\r\n" +
                                "Content-Length: 0\r\nConnection: close\r\n\r\n").toByteArray())
                        }
                    }
                } catch (_: java.net.SocketTimeoutException) { count }
            }
            val res = executeRelayRpcUnary(
                http = OkHttpClient(),
                method = io.github.lewin671.remoteagent.core.proto.RelayRpcMethod.RELAY_RPC_METHOD_GET_CHALLENGE,
                server = "http://127.0.0.1:${server.localPort}",
                requestBytes = GetChallengeRequest.newBuilder().setDeviceId("test").build().toByteArray(),
            )
            assertNotEquals(0, res.connectCode)
            assertEquals(1, requests.get(5, TimeUnit.SECONDS))
        }
    }

    @Test fun everyRpcEntryRejectsInsecureRemoteOrigins() = runBlocking {
        for (server in listOf("http://relay.example", "http://192.168.1.2:8080", "https://user:password@relay.example")) {
            try { Secret.checkServer(server, allowPlainHttp = true); fail("Accepted insecure origin") }
            catch (_: IllegalArgumentException) { }
            val res = runCatching {
                executeRelayRpcUnary(
                    http = OkHttpClient(),
                    method = io.github.lewin671.remoteagent.core.proto.RelayRpcMethod.RELAY_RPC_METHOD_GET_CHALLENGE,
                    server = server,
                )
            }
            assertTrue("Expected insecure origin rejected for $server", res.isFailure)
        }
        assertEquals("http://10.0.2.2:8080", Secret.checkServer("http://10.0.2.2:8080", allowPlainHttp = true))
    }

    @Test fun defaultClientNeverFollowsHttpOrTlsDowngradeRedirects() {
        val http = Api.defaultHttp()
        assertFalse(http.followRedirects)
        assertFalse(http.followSslRedirects)
    }
}
