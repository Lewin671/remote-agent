package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.CommandException
import io.github.lewin671.remoteagent.proto.SendRequest
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test

class FileAttachmentsTest {
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
        assertTrue(d.attach(listOf(original), 6))
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
        assertTrue((restored.state as PendingImage.State.Failed).error.contains("重新选择"))
    }

    @Test fun filesAndImagesHaveSeparateLimitsAndRestoringIsAtomic() {
        val d = ImageDraft("session:s", MemoryFiles())
        assertTrue(d.attach((1..6).map { PendingImage("image$it", null) }, 6))
        assertTrue(d.attach((1..6).map { file("file$it") }, 6))
        assertEquals(6, d.pictures.size)
        assertEquals(6, d.documents.size)
        assertFalse(d.attach(listOf(file("overflow")), 6))
        assertEquals(12, d.images.size)
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
        assertTrue(d.attach(listOf(f), 6))
        assertNull(f.fileId)
        assertFalse(d.ready("computer"))
        assertTrue(needsUpload(f, "computer"))
    }

    @Test fun outgoingRestoresTheFrozenFileIdsAndRetriesAnUnknownRequestVerbatim() = runBlocking {
        val store = MemoryFiles()
        val out = Outgoing("request", "text", images = listOf(file()), fileIds = listOf("first-id"))
        OutgoingRecords(store).save("s", out, false)
        val loaded = OutgoingRecords(store).load("s").single()
        assertEquals(listOf("first-id"), loaded.fileIds)
        assertEquals("content://documents/file", loaded.images.single().uri)
        val requests = mutableListOf<SendRequest>()
        loaded.deliver { requests += it; throw CommandException(CommandException.UNKNOWN, "reply lost") }
        assertFalse(loaded.canPutBack)
        loaded.images.single().fileId = "new-upload"
        loaded.deliver { requests += it }
        assertEquals(requests[0], requests[1])
        assertEquals(listOf("first-id"), requests.last().fileIdsList)
        assertTrue(requests.last().imageIdsList.isEmpty())
    }

    @Test fun aFileExpiringWhileTheOutgoingRecordIsSavedStaysInTheDraft() = runBlocking {
        val f = file()
        val store = MemoryFiles()
        val records = OutgoingRecords(store)
        val remote = io.github.lewin671.remoteagent.core.Remote(
            io.github.lewin671.remoteagent.core.Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "test"), okhttp3.OkHttpClient())
        remote.showForTest(io.github.lewin671.remoteagent.core.RemoteState(machines = mapOf("computer" to
            io.github.lewin671.remoteagent.core.MachineView("computer", true, 0, io.github.lewin671.remoteagent.proto.MachineMeta.newBuilder()
                .setCapabilities(io.github.lewin671.remoteagent.proto.MachineCapabilities.newBuilder().setFileUpload(true)).build()))))
        val result = kotlinx.coroutines.CompletableDeferred<Result<Unit>>()
        try {
            Outbox.sendWithImages(this, remote, "expired-file", "", emptyList(), listOf(f), false,
                store = { sid, outgoing, original -> records.save(sid, outgoing, original); f.expiresMs = 0 },
                discard = records::remove, fileIds = listOf("uploaded-file"),
            ) { result.complete(it) }
            assertTrue(result.await().isFailure)
            assertTrue(records.load("expired-file").isEmpty())
            assertTrue(Outbox.of("expired-file").isEmpty())
            assertEquals("content://documents/file", f.uri)
            assertTrue(needsUpload(f, "computer"))
        } finally { remote.close() }
    }

    @Test fun definitiveMissingFilesOfferRestoration() = runBlocking {
        val out = Outgoing("r", "keep", images = listOf(file()), fileIds = listOf("missing"))
        out.deliver { throw CommandException("not_found", "file missing is not on this machine; upload it again", fromDaemon = true) }
        assertTrue(out.canPutBack)
        assertTrue(out.missingImages)
        assertTrue(out.error.contains("附件"))
    }
}
