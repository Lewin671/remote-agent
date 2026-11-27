package io.github.lewin671.remoteagent

import io.github.lewin671.remoteagent.core.*
import kotlinx.coroutines.test.*
import kotlinx.coroutines.*
import io.github.lewin671.remoteagent.proto.*
import com.google.protobuf.ByteString
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.Protocol
import okhttp3.Response
import okhttp3.ResponseBody.Companion.toResponseBody
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
        val workerPool = java.util.concurrent.Executors.newCachedThreadPool()
        val callLatches = ThreadLocal<Pair<java.util.concurrent.CountDownLatch, java.util.concurrent.CountDownLatch>>()
        val inFlight = java.util.concurrent.ConcurrentHashMap<okhttp3.Call, Pair<Job, java.util.concurrent.CompletableFuture<Response>>>()
        val dispatcher = okhttp3.Dispatcher(
            object : java.util.concurrent.AbstractExecutorService() {
                override fun shutdown() {}
                override fun shutdownNow(): MutableList<Runnable> = mutableListOf()
                override fun isShutdown() = false
                override fun isTerminated() = false
                override fun awaitTermination(timeout: Long, unit: java.util.concurrent.TimeUnit) = true
                override fun execute(command: Runnable) {
                    val entered = java.util.concurrent.CountDownLatch(1)
                    val finished = java.util.concurrent.CountDownLatch(1)
                    workerPool.execute {
                        callLatches.set(entered to finished)
                        try {
                            command.run()
                        } finally {
                            callLatches.remove()
                            entered.countDown()
                            finished.countDown()
                        }
                    }
                    entered.await(5, java.util.concurrent.TimeUnit.SECONDS)
                }
            },
        ).apply {
            maxRequests = 128
            maxRequestsPerHost = 64
        }
        val testHttp = Api.defaultHttp().newBuilder()
            .dispatcher(dispatcher)
            .eventListenerFactory {
                object : okhttp3.EventListener() {
                    override fun canceled(call: okhttp3.Call) {
                        inFlight.remove(call)?.let { (job, done) ->
                            job.cancel()
                            done.completeExceptionally(java.io.IOException("Canceled"))
                        }
                    }
                }
            }
            .addInterceptor { chain ->
                val req = chain.request()
                val call = chain.call()
                val (entered, finished) = callLatches.get() ?: (null to null)
                val done = java.util.concurrent.CompletableFuture<Response>()
                val job = backgroundScope.launch {
                    try {
                        val method = req.url.encodedPath.substringAfterLast('/')
                        if (method == "GetChallenge" && ++challenges == 1) awaitCancellation()
                        val response = if (method == "GetChallenge") {
                            GetChallengeResponse.newBuilder().setChallenge(ByteString.copyFrom(ByteArray(32))).build()
                        } else {
                            LoginResponse.newBuilder().setToken("fresh-token").setExpiresMs(System.currentTimeMillis() + 3 * 86_400_000L).build()
                        }
                        val httpRes = Response.Builder()
                            .request(req)
                            .protocol(Protocol.HTTP_1_1)
                            .code(200)
                            .message("OK")
                            .header("content-type", "application/proto")
                            .body(response.toByteArray().toResponseBody("application/proto".toMediaType()))
                            .build()
                        done.complete(httpRes)
                        finished?.await(5, java.util.concurrent.TimeUnit.SECONDS)
                    } catch (t: Throwable) {
                        done.completeExceptionally(t)
                        if (t is CancellationException) throw t
                    }
                }
                inFlight[call] = job to done
                if (call.isCanceled()) {
                    inFlight.remove(call)
                    job.cancel()
                    done.completeExceptionally(java.io.IOException("Canceled"))
                }
                entered?.countDown()
                try {
                    done.get()
                } catch (e: java.util.concurrent.ExecutionException) {
                    val cause = e.cause
                    if (cause is java.io.IOException) throw cause
                    throw java.io.IOException(cause?.message ?: "interrupted", cause)
                } finally {
                    inFlight.remove(call)
                }
            }
            .build()
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
        val r=Remote(Account("https://example.com","fixture-device"),testHttp,securityStore=store,dispatcher=StandardTestDispatcher(testScheduler),socketFactory=dummySocketFactory)
        try {
            app.attachNetwork(r);runCurrent()
            r.start();runCurrent();assertEquals(1,challenges)
            // Re-reporting the seeded network does not retire the cold login.
            r.networkChanged("wifi",true);runCurrent()
            assertEquals("",r.currentToken());assertEquals(1,challenges)
            // A real change before any Ready retires the blocked cold login.
            r.networkChanged("cellular",true);runCurrent()
            assertEquals("fresh-token",r.currentToken());assertEquals(2,challenges)
        }finally{r.close();runCurrent();workerPool.shutdownNow()}
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
