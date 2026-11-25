package io.github.lewin671.remoteagent

import io.github.lewin671.remoteagent.core.*
import kotlinx.coroutines.test.*
import kotlinx.coroutines.*
import com.connectrpc.StreamResult
import com.connectrpc.http.*
import io.github.lewin671.remoteagent.proto.*
import com.google.protobuf.ByteString
import okio.Buffer
import org.junit.Assert.*
import org.junit.Test

@OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
@org.junit.runner.RunWith(org.robolectric.RobolectricTestRunner::class)
@org.robolectric.annotation.Config(sdk=[35], application=android.app.Application::class)
class NetworkAttachmentTest {
    @get:org.junit.Rule val fixture=RaAppTestRule()
    @Test fun networkObservedBeforeAccountLoadingSeedsEachEngine()=runTest {
        val app=fixture.app
        app.observeNetwork("wifi",true)
        var challenges=0
        val transport=object:HTTPClientInterface {
            override fun unary(request:UnaryHTTPRequest,onResult:(HTTPResponse)->Unit):()->Unit {
                val job=backgroundScope.launch {
                    val method=request.methodSpec.path.substringAfterLast('/')
                    if(method=="GetChallenge" && ++challenges==1) awaitCancellation()
                    val response=if(method=="GetChallenge") GetChallengeResponse.newBuilder().setChallenge(ByteString.copyFrom(ByteArray(32))).build()
                        else LoginResponse.newBuilder().setToken("fresh-token").setExpiresMs(System.currentTimeMillis()+3*86_400_000L).build()
                    onResult(HTTPResponse(trailers=emptyMap(),status=200,headers=mapOf("content-type" to listOf("application/proto")),message=Buffer().write(response.toByteArray())))
                }
                return {job.cancel()}
            }
            override fun stream(request:HTTPRequest,duplex:Boolean,onResult:suspend (StreamResult<Buffer>)->Unit):Stream=error("unused stream")
        }
        val store=MemorySecurityStoreV4().apply {
            generateKey(io.github.lewin671.remoteagent.core.proto.SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_ED, "", persist=true)
        }
        val dummySocketFactory = okhttp3.WebSocket.Factory { req, _ ->
            object : okhttp3.WebSocket {
                override fun request() = req
                override fun queueSize() = 0L
                override fun send(text: String) = true
                override fun send(bytes: okio.ByteString) = true
                override fun close(code: Int, reason: String?) = true
                override fun cancel() {}
            }
        }
        val r=Remote(Account("https://example.com","fixture-device"),Api.defaultHttp(),securityStore=store,dispatcher=StandardTestDispatcher(testScheduler),socketFactory=dummySocketFactory,rpcTransport=transport)
        try {
            app.attachNetwork(r);runCurrent()
            r.start();runCurrent();assertEquals(1,challenges)
            // Re-reporting the seeded network does not retire the cold login.
            r.networkChanged("wifi",true);runCurrent()
            assertEquals("",r.currentToken());assertEquals(1,challenges)
            // A real change before any Ready retires the blocked cold login.
            r.networkChanged("cellular",true);runCurrent()
            assertEquals("fresh-token",r.currentToken());assertEquals(2,challenges)
        }finally{r.close();runCurrent()}
    }

    @Test fun foregroundPrewarmRunsOffMainOnlyWhenForegroundAndAccountExists() = runBlocking {
        val app = fixture.app
        val prewarmCount = java.util.concurrent.atomic.AtomicInteger(0)
        val mainThread = Thread.currentThread()
        val prewarmThread = java.util.concurrent.atomic.AtomicReference<Thread?>()
        app.testRtcFactory = object : io.github.lewin671.remoteagent.core.transport.RtcFactory {
            override fun create(
                stunUrls: List<String>,
                listener: io.github.lewin671.remoteagent.core.transport.RtcPeerListener,
            ) = error("unused")

            override fun prewarm() {
                prewarmThread.set(Thread.currentThread())
                prewarmCount.incrementAndGet()
            }
        }
        val marker = java.io.File(app.noBackupFilesDir, "security-v4-buckets.bin")
        val notifyMarker = java.io.File(app.noBackupFilesDir, "security-v4-notify.bin")
        marker.delete()
        notifyMarker.delete()
        try {
            // No account present -> must not prewarm.
            app.prewarmRtcIfAccountPresent(foregroundOverride = true)?.join()
            assertEquals(0, prewarmCount.get())

            // Account exists, but background wakeup -> must not prewarm.
            marker.parentFile?.mkdirs()
            marker.writeBytes(byteArrayOf(1))
            assertNull(app.prewarmRtcIfAccountPresent(foregroundOverride = false))
            assertEquals(0, prewarmCount.get())

            // Account exists and foreground -> prewarms on IO thread.
            app.prewarmRtcIfAccountPresent(foregroundOverride = true)?.join()
            assertEquals(1, prewarmCount.get())
            assertTrue("expected prewarm off main thread", prewarmThread.get() !== mainThread)
        } finally {
            marker.delete()
            notifyMarker.delete()
        }
    }
}
