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
    private fun remote() = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "test"), okhttp3.OkHttpClient())
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
}
