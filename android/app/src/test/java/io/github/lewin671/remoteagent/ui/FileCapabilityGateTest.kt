package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.proto.*
import kotlinx.coroutines.*
import org.junit.Assert.*
import org.junit.Test

@org.junit.runner.RunWith(org.robolectric.RobolectricTestRunner::class)
@org.robolectric.annotation.Config(sdk = [35], application = android.app.Application::class)
class FileCapabilityGateTest {
    @get:org.junit.Rule(order = 0) val sessionMessages = io.github.lewin671.remoteagent.ui.SessionMessageContextRule()
    private fun remote() = Remote(Account("http://127.0.0.1:1", "test"), okhttp3.OkHttpClient())
    private fun show(r: Remote, supported: Boolean?) = r.showForTest(RemoteState(
        machines = mapOf("computer" to MachineView("computer", true, 0, MachineMeta.newBuilder().apply {
            if (supported != null) setCapabilities(MachineCapabilities.newBuilder().setFileUpload(supported))
        }.build())), sessions = mapOf("capability" to SessionView("capability", "computer"))))
    private fun file() = PendingImage("file", "content://documents/file", "file.txt", 1).apply {
        machineId = "computer"; fileId = "original-file"; expiresMs = Long.MAX_VALUE; state = PendingImage.State.Ready
    }

    @Suppress("UNCHECKED_CAST")
    private fun tray(): ImageDraft {
        val drafts = ImageDrafts::class.java.getDeclaredField("drafts").apply { isAccessible = true }.get(ImageDrafts) as MutableMap<String, ImageDraft>
        return ImageDraft("session:capability", MemoryFiles()).also { drafts["session:capability"] = it }
    }

    @Test fun capabilityLostWhileSavingLeavesTheSourceAndTextInTheDraft() = runBlocking {
        val r = remote(); show(r, true); val f = file(); val disk = MemoryFiles(); val records = OutgoingRecords(disk)
        val done = CompletableDeferred<Result<Unit>>()
        try {
            Outbox.sendWithImages(this, r, "capability", "keep text", emptyList(), listOf(f), false,
                fileIds = listOf("original-file"),
                store = { sid, o, orig -> records.save(sid, o, orig); show(r, false) },
                discard = records::remove,
            ) { done.complete(it) }
            val result = done.await()
            assertTrue(result.isFailure); assertTrue(displayError(result.exceptionOrNull()!!).contains("更新电脑上的 remote-agent"))
            assertTrue(records.load("capability").isEmpty()); assertTrue(Outbox.of("capability").isEmpty())
            assertEquals("original-file", f.fileId); assertTrue(f.readyFor("computer"))
        } finally { r.close(); AccountMemory.clear() }
    }

    @Test fun unsupportedSendNeverSavesOrDetachesTheDraft() = runBlocking {
        val r = remote(); show(r, false); val done = CompletableDeferred<Result<Unit>>()
        try {
            Outbox.sendWithImages(this, r, "capability", "keep text", emptyList(), listOf(file()), false,
                fileIds = listOf("original-file"),
                store = { _, _, _ -> error("must gate before saving") },
            ) { done.complete(it) }
            assertTrue(displayError(done.await().exceptionOrNull()!!).contains("更新电脑上的 remote-agent"))
            assertFalse(Outbox.keeping("capability")); assertTrue(Outbox.of("capability").isEmpty())
        } finally { r.close(); AccountMemory.clear() }
    }

    @Test fun draftExceedingLoweredCapabilitiesReturnsCoreBlockReason() {
        val r = remote()
        try {
            r.showForTest(
                RemoteState(
                    conn = ConnState.ONLINE,
                    machines = mapOf(
                        "computer" to MachineView(
                            "computer",
                            true,
                            0,
                            MachineMeta.newBuilder()
                                .setCapabilities(
                                    MachineCapabilities.newBuilder()
                                        .setImageUpload(true)
                                        .setFileUpload(true)
                                        .setMaxImagesPerMessage(1)
                                        .setMaxFilesPerMessage(1)
                                        .setMaxFileUploadBytes(10L * 1024 * 1024),
                                )
                                .build(),
                        ),
                    ),
                    sessions = mapOf("capability" to SessionView("capability", "computer")),
                ),
            )
            val f1 = PendingImage("f1", "content://documents/f1", "a.txt", 10, retainSource = false)
            val f2 = PendingImage("f2", "content://documents/f2", "b.txt", 10, retainSource = false)
            val overCount = evaluateDraftAttachmentsBlocked(r, "computer", "capability", documents = listOf(f1, f2))
            assertNotNull(overCount)
            assertTrue("got=$overCount", overCount!!.contains("1"))

            val tooBig = PendingImage("big", "content://documents/big", "big.bin", 12L * 1024 * 1024, retainSource = false)
            val overSize = evaluateDraftAttachmentsBlocked(r, "computer", "capability", documents = listOf(tooBig))
            assertNotNull(overSize)
            assertTrue("got=$overSize", overSize!!.contains("big.bin") && overSize.contains("10"))

            val p1 = PendingImage("p1", null, retainSource = false)
            val p2 = PendingImage("p2", null, retainSource = false)
            val overImgs = evaluateDraftAttachmentsBlocked(r, "computer", "capability", documents = emptyList(), pictures = listOf(p1, p2))
            assertNotNull(overImgs)
            assertTrue("got=$overImgs", overImgs!!.contains("1"))
        } finally {
            r.close()
            AccountMemory.clear()
        }
    }
}
