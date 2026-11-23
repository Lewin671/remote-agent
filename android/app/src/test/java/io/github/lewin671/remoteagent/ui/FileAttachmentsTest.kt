package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.CommandException
import io.github.lewin671.remoteagent.proto.SendRequest
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test

@org.junit.runner.RunWith(org.robolectric.RobolectricTestRunner::class)
@org.robolectric.annotation.Config(sdk = [35], application = android.app.Application::class)
class FileAttachmentsTest {
    @get:org.junit.Rule(order = 0) val sessionMessages = io.github.lewin671.remoteagent.ui.SessionMessageContextRule()
    private fun file(id: String = "file") = PendingImage(id, "content://documents/$id", "季度\n说明😀.zip", 100L * 1024 * 1024).apply {
        machineId = "computer"
        fileId = "uploaded-$id"
        expiresMs = Long.MAX_VALUE
        state = PendingImage.State.Ready
    }

    @Test fun draftRestoresUriNameAndSizeWithoutCopyingFileBytes() = runBlocking {
        val store = MemoryFiles()
        val original = file()
        val d = ImageDraft("session:s", store)
        assertTrue(d.attach(listOf(original)))
        assertTrue(store.data.keys.any { it.endsWith(".file") })
        assertFalse(store.data.keys.any { it.endsWith(".img") })
        assertTrue(store.data.values.sumOf { it.size } < 1024)
        val restored = loadImages(store, "session:s").first.single()
        assertEquals(original.uri, restored.uri)
        assertEquals(original.fileName, restored.fileName)
        assertEquals(original.fileBytes, restored.fileBytes)
        assertTrue(restored.isFile)
        assertNull(restored.processed)
        assertTrue(needsUpload(restored, "computer"))
        assertTrue((restored.state as PendingImage.State.Failed).error.testText().contains("重新选择"))
    }

    @Test fun expiresSwitchesAndRestoresForceAFileReupload() {
        val f = file()
        assertTrue(f.readyFor("computer", 1))
        assertFalse(needsUpload(f, "computer"))
        assertTrue(needsUpload(f, "another"))
        f.expiresMs = 0
        assertTrue(needsUpload(f, "computer"))
        f.expiresMs = Long.MAX_VALUE
        f.state = PendingImage.State.Uploading(.5f)
        assertFalse(needsUpload(f, "computer"))
        assertTrue(needsUpload(f, "another"))
        val d = ImageDraft("session:s", MemoryFiles())
        assertTrue(d.attach(listOf(f)))
        assertNull(f.fileId)
        assertFalse(d.ready("computer"))
        assertTrue(needsUpload(f, "computer"))
    }

    @Test fun aFileExpiringWhileTheOutgoingRecordIsSavedStaysInTheDraft() = runBlocking {
        val f = file()
        val store = MemoryFiles()
        val records = OutgoingRecords(store)
        val remote = io.github.lewin671.remoteagent.core.Remote(
            io.github.lewin671.remoteagent.core.Account("http://127.0.0.1:1", "test"), okhttp3.OkHttpClient())
        remote.showForTest(io.github.lewin671.remoteagent.core.RemoteState(machines = mapOf("computer" to
            io.github.lewin671.remoteagent.core.MachineView("computer", true, 0, io.github.lewin671.remoteagent.proto.MachineMeta.newBuilder()
                .setCapabilities(io.github.lewin671.remoteagent.proto.MachineCapabilities.newBuilder().setFileUpload(true)).build()))))
        val result = kotlinx.coroutines.CompletableDeferred<Result<Unit>>()
        try {
            Outbox.sendWithImages(this, remote, "expired-file", "", emptyList(), listOf(f), false,
                fileIds = listOf("uploaded-file"),
                store = { sid, outgoing, original -> records.save(sid, outgoing, original); f.expiresMs = 0 },
                discard = records::remove,
            ) { result.complete(it) }
            assertTrue(result.await().isFailure)
            assertTrue(records.load("expired-file").isEmpty())
            assertTrue(Outbox.of("expired-file").isEmpty())
            assertEquals("content://documents/file", f.uri)
            assertTrue(needsUpload(f, "computer"))
        } finally { remote.close() }
    }
}
