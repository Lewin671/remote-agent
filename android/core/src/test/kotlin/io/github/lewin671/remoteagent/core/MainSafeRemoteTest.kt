package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.proto.*
import java.util.concurrent.Executors
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.first
import org.junit.Assert.*
import org.junit.Test

class MainSafeRemoteTest {
    private fun checkUiOperation(operation: String) = runBlocking {
        val server = TestServer()
        val http = Api.defaultHttp()
        val ui = Executors.newSingleThreadExecutor { Thread(it, "test-ui") }.asCoroutineDispatcher()
        val worker = Executors.newSingleThreadExecutor { Thread(it, "remote-state") }.asCoroutineDispatcher()
        var remote: Remote? = null
        var daemon: FakeDaemon? = null
        try {
            val fake = FakeDaemon.create(randomBytes(32), server.url, http, server.setupToken)
            daemon = fake
            fake.putMachine("test-machine")
            fake.putSession("session", Session.newBuilder().setStatus(SessionStatus.SESSION_STATUS_IDLE).build())
            fake.connect()
            val account = approvedClient(fake.phone, http)
            var securityReads = 0
            fun onWorker() { assertTrue("storage ran on ${Thread.currentThread().name}", Thread.currentThread().name.startsWith("remote-state")) }
            val store = TestSecurity.get(server.url).stores.getValue(account.deviceId)
            val guardedStore = object : DeviceSecurity.Storage by store {
                override fun readSecurity(): DeviceSecurity.Record {
                    onWorker(); securityReads++
                    return store.readSecurity()
                }
            }
            val envelopes = HashMap<String, ByteArray>()
            var imageReads = 0
            var imageWrites = 0
            val memory = MemoryCache()
            val cache = object : CipherCache by memory {
                override suspend fun image(key: String): ByteArray? {
                    onWorker(); imageReads++
                    return envelopes[key]
                }
                override suspend fun putImage(key: String, envelope: ByteArray) {
                    onWorker(); imageWrites++
                    envelopes[key] = envelope
                }
            }
            val engine = Remote(account, http, cache, securityStorage = guardedStore, dispatcher = worker)
            remote = engine
            engine.start()
            withTimeout(10_000) { engine.state.first { it.sessions["session"]?.info != null &&
                it.machines[fake.id]?.online == true } }
            val command = Command.newBuilder().setInterrupt(InterruptRequest.newBuilder().setSessionId("session")).build()
            val prepared = engine.prepare(fake.id, "session", command)
            val before = securityReads
            withContext(ui) {
                when (operation) {
                    "authorize" -> assertFalse(engine.authorize(prepared).securityHeader.isEmpty)
                    "forward" -> assertTrue(engine.forward(prepared).hasInterrupt())
                    "command" -> assertTrue(engine.command("session", command).hasInterrupt())
                    "image" -> {
                        val imageId = "00000000000000000000000000000001"
                        val png = byteArrayOf(0x89.toByte(), 80, 78, 71, 13, 10, 26, 10, 1)
                        fake.putImage("session", imageId, ImagePayload.newBuilder()
                            .setFormat(ImageFormat.IMAGE_FORMAT_PNG).setWidth(1).setHeight(1)
                            .setData(ByteString.copyFrom(png)).build())
                        repeat(2) { assertTrue(engine.image("session", imageId) is Remote.ImageResult.Loaded) }
                        assertEquals(2, imageReads)
                        assertEquals(2, imageWrites)
                    }
                    else -> error(operation)
                }
            }
            assertTrue(securityReads > before)
        } finally {
            remote?.close()
            // Drain the close job before shutting down its dispatcher.
            withContext(worker) { yield() }
            daemon?.close()
            server.close()
            worker.close(); ui.close()
        }
    }

    @Test fun authorizationUsesTheSerializedWorker() = checkUiOperation("authorize")
    @Test fun commandResultOpeningUsesTheSerializedWorker() = checkUiOperation("forward")
    @Test fun sessionCommandsUseTheSerializedWorker() = checkUiOperation("command")
    @Test fun cachedAndNetworkImagesUseTheSerializedWorker() = checkUiOperation("image")
}
