package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.proto.*
import kotlinx.coroutines.*
import org.junit.Assert.*
import org.junit.Test
import java.io.ByteArrayInputStream

class AttachmentInvalidationTest {
    private fun ready(id: String = "file", image: Boolean = false) = PendingImage(id, "content://documents/$id", if (image) null else "$id.txt", 1).apply {
        if (image) processed = Processed(byteArrayOf(1), ImageFormat.IMAGE_FORMAT_PNG, 1, 1)
        machineId = "A"; transferId = "old-$id"
        imageId = if (image) "image-$id" else null; fileId = if (!image) "file-$id" else null
        expiresMs = Long.MAX_VALUE; state = PendingImage.State.Ready
    }
    private fun draft(key: String = "s", disk: MemoryFiles = MemoryFiles()) = ImageDraft("session:$key", disk)
    private fun invalid(entry: PendingImage) {
        assertFalse(entry.readyFor("A")); assertFalse(entry.readyFor("B"))
        assertNull(entry.imageId); assertNull(entry.fileId); assertNull(entry.machineId)
        assertEquals(0L, entry.expiresMs)
        assertEquals(PendingImage.State.Processing, entry.state)
    }
    private open class Channel : UploadChannel {
        val begins = mutableListOf<String>(); val aborted = mutableListOf<String>()
        var puts = 0; var size = 0L
        override suspend fun begin(transferId: String, format: ImageFormat, total: Long, sha256: ByteArray): ImageUploadStatus = error("unused image network")
        override suspend fun beginFile(transferId: String, name: String, total: Long, sha256: ByteArray): ImageUploadStatus {
            begins += transferId; size = total
            return ImageUploadStatus.newBuilder().setState(ImageUploadState.IMAGE_UPLOAD_STATE_RECEIVING).build()
        }
        override suspend fun put(transferId: String, offset: Long, data: ByteArray): ImageUploadStatus {
            puts++; return ImageUploadStatus.newBuilder().setState(ImageUploadState.IMAGE_UPLOAD_STATE_RECEIVING).setPersistedBytes(offset + data.size).build()
        }
        override suspend fun status(transferId: String) = ImageUploadStatus.newBuilder().setState(ImageUploadState.IMAGE_UPLOAD_STATE_RECEIVING).setPersistedBytes(size).build()
        override suspend fun complete(transferId: String) = ImageUploadStatus.newBuilder().setState(ImageUploadState.IMAGE_UPLOAD_STATE_COMPLETE).setFileId("done-$transferId").setExpiresMs(Long.MAX_VALUE).build()
        override suspend fun abort(transferId: String) { aborted += transferId }
    }

    @Test fun readinessAndIdsAreClearedBeforeCancellationAndAbortForBothAttachmentKinds() {
        for (image in listOf(false, true)) {
            val entry = ready(image = image)
            val d = draft().apply { images += entry }
            val old = entry.generation
            val order = mutableListOf<String>()
            entry.job = Job().also { job -> job.invokeOnCompletion { invalid(entry); order += "cancel" } }
            entry.abortTransfer = {
                invalid(entry)
                assertFalse(d.ready("A")); assertTrue(d.ids("A").isEmpty()); assertTrue(d.fileIds("A").isEmpty())
                assertFalse(entry.change(old, "A") { error("late result") })
                order += "abort"
            }
            entry.stopUpload(staleBytes = image)
            assertEquals(listOf("cancel", "abort"), order)
            if (image) assertTrue(entry.needsProcessing)
        }
    }

    @Test fun changingOriginalWithoutAReadableSourceInvalidatesBeforeAbortAndCannotUploadStaleBytes() = runBlocking {
        val disk = MemoryFiles()
        val entry = PendingImage("no-original", null).apply {
            processed = Processed(byteArrayOf(1), ImageFormat.IMAGE_FORMAT_PNG, 1, 1)
            machineId = "A"; imageId = "old-image"; expiresMs = Long.MAX_VALUE; state = PendingImage.State.Ready
        }
        val d = draft("no-original", disk).apply { images += entry }
        var aborted = false
        entry.abortTransfer = { invalid(entry); assertTrue(entry.needsProcessing); aborted = true }
        d.changeOriginal(true) { error("missing URI cannot be processed") }
        assertTrue(aborted)
        assertTrue(entry.state is PendingImage.State.Failed)
        val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "test"), okhttp3.OkHttpClient())
        try {
            d.upload(remote, entry, "A", forceFresh = true)
            assertNull(entry.job); assertNull(entry.imageId); assertFalse(d.ready("A"))
        } finally { remote.close() }
        val back = draft("no-original", disk)
        back.restore(this) { null }.join()
        assertEquals(1, back.images.size)
        assertTrue(back.images.single().needsProcessing)
        assertTrue(back.images.single().state is PendingImage.State.Failed)
        assertFalse(back.ready("A"))
    }

    @Test fun rapidMachineRoundTripWhileTheGlobalSlotIsBlockedCannotReuseOldReadiness() = runBlocking {
        val entered = CompletableDeferred<Unit>(); val release = CompletableDeferred<Unit>()
        val holder = launch { FileUploads.serial { entered.complete(Unit); release.await() } }
        entered.await()
        val entry = ready(); val d = draft().apply { images += entry }
        val original = entry.generation
        var originalAborted = false
        entry.abortTransfer = { invalid(entry); originalAborted = true }
        d.retarget("B")
        val b = Channel(); val a = Channel()
        d.startDocumentUpload(entry, "B", this, b, 100, Dispatchers.Unconfined) { error("superseded source") }
        yield()
        d.retarget("A")
        d.startDocumentUpload(entry, "A", this, a, 100, Dispatchers.Unconfined) { ByteArrayInputStream(byteArrayOf(1)) }
        invalid(entry)
        assertFalse(entry.change(original, "A") { error("old A result") })
        assertFalse(d.ready("A")); assertTrue(originalAborted)
        assertTrue(a.begins.isEmpty()); assertTrue(b.begins.isEmpty())
        release.complete(Unit); holder.join(); entry.job!!.join()
        assertTrue(entry.readyFor("A")); assertEquals(1, a.begins.size); assertTrue(b.begins.isEmpty())
        assertNotEquals("old-file", entry.transferId)
    }

    @Test fun retryAndExpiryInvalidateBeforeWaitingForTheSharedUploadSlot() = runBlocking {
        val entered = CompletableDeferred<Unit>(); val release = CompletableDeferred<Unit>()
        val holder = launch { FileUploads.serial { entered.complete(Unit); release.await() } }
        entered.await()
        val entries = mutableListOf<PendingImage>()
        for (retry in listOf(false, true)) {
            val entry = ready(if (retry) "retry" else "expiry")
            if (!retry) entry.expiresMs = 1
            var aborted = false
            entry.abortTransfer = { invalid(entry); aborted = true }
            val d = draft(entry.id).apply { images += entry }
            d.startDocumentUpload(entry, "A", this, Channel(), 100, Dispatchers.Unconfined, forceFresh = retry) { error("slot still occupied") }
            invalid(entry); assertTrue(aborted); assertFalse(d.ready("A"))
            entries += entry
        }
        entries.forEach { it.stopUpload() }
        release.complete(Unit); holder.join()
    }

    @Test fun coldDraftRestorationValidatesItsSourceBeforeReusingTheSameCompletedTransfer() = runBlocking {
        val disk = MemoryFiles(); val original = ready()
        saveImages(disk, "session:cold", listOf(original), false); saveOrder(disk, "session:cold", listOf(original), false)
        val d = draft("cold", disk)
        d.restore(this) { null }.join()
        val entry = d.images.single()
        invalid(entry); assertEquals("old-file", entry.transferId)
        assertEquals("A", entry.transportMachine())
        d.retarget(null); invalid(entry)
        d.retarget("A"); invalid(entry)
        val ch = object : Channel() {
            override suspend fun beginFile(transferId: String, name: String, total: Long, sha256: ByteArray): ImageUploadStatus {
                super.beginFile(transferId, name, total, sha256)
                return ImageUploadStatus.newBuilder().setState(ImageUploadState.IMAGE_UPLOAD_STATE_COMPLETE).setFileId("file-file").setExpiresMs(Long.MAX_VALUE).build()
            }
        }
        var opened = 0
        d.startDocumentUpload(entry, "A", this, ch, 100, Dispatchers.Unconfined) { opened++; ByteArrayInputStream(byteArrayOf(1)) }
        entry.job!!.join()
        assertEquals(listOf("old-file"), ch.begins); assertEquals(0, ch.puts); assertTrue(ch.aborted.isEmpty())
        assertEquals(1, opened); assertEquals("file-file", entry.fileId); assertTrue(d.ready("A"))
    }

    @Test fun restoredAttachmentsWithoutLocalSourcesFailImmediatelyAndKeepNoSendableIds() = runBlocking {
        val disk = MemoryFiles()
        val image = ready("image", image = true)
        // No encrypted picture bytes and no source URI survive.
        val noImageSource = PendingImage(image.id, null).apply {
            processed = image.processed; imageId = image.imageId; machineId = "A"; expiresMs = Long.MAX_VALUE
        }
        val file = PendingImage("file", null, "file.txt", 1).apply { machineId = "A"; fileId = "remote"; expiresMs = Long.MAX_VALUE }
        saveImages(disk, "session:missing", listOf(file, noImageSource), false)
        saveOrder(disk, "session:missing", listOf(file, noImageSource), false)
        disk.remove("session:missing", "image.img")
        val d = draft("missing", disk); d.restore(this) { null }.join()
        assertEquals(2, d.images.size)
        d.images.forEach {
            assertNull(it.imageId); assertNull(it.fileId); assertNull(it.machineId)
            assertTrue((it.state as PendingImage.State.Failed).error.contains("没有本机副本"))
        }
        assertFalse(d.ready("A"))
    }

    @Test fun restoringIntoAnExistingDraftPublishesTheInvalidEntryBeforeAbort() {
        for (image in listOf(false, true)) {
            val d = draft().apply { images += this@AttachmentInvalidationTest.ready("already-there") }
            val restored = ready("restored", image)
            var called = false
            restored.abortTransfer = {
                invalid(restored)
                assertTrue(restored in d.images)
                assertFalse(d.ready("A"))
                called = true
            }
            assertTrue(d.attach(listOf(restored), 6))
            assertTrue(called)
            assertFalse(d.ready("A"))
        }
    }

    @Test fun restoringOnlyHistoryReferencesShowsFailedEntriesAndBlocksSendingAcrossRestart() = runBlocking {
        val disk = MemoryFiles(); val d = draft("history", disk)
        val remoteFile = SentFile.newBuilder().setFileId("remote-id").setName("report.csv").setSizeBytes(42).build()
        assertTrue(d.restoreReferences(listOf(remoteFile), listOf("remote-image"), 6, 6))
        assertEquals(2, d.images.size)
        assertEquals("report.csv", d.documents.single().fileName)
        d.images.forEach { assertTrue(it.state is PendingImage.State.Failed); assertNull(it.imageId); assertNull(it.fileId) }
        assertFalse(d.ready("A"))
        val restored = draft("history", disk)
        restored.restore(this) { null }.join()
        assertEquals(2, restored.images.size)
        assertFalse(restored.ready("A"))
    }

    @Test fun detachingAndReleasingASentOrUnknownRequestNeverAbortItsFrozenSnapshot() = runBlocking {
        val entry = ready(); val d = draft().apply { images += entry }
        var aborts = 0; entry.abortTransfer = { aborts++ }
        val outgoing = Outgoing("same-request", "text", images = listOf(entry), fileIds = listOf(entry.fileId!!))
        val requests = mutableListOf<SendRequest>()
        outgoing.deliver { requests += it; throw CommandException(CommandException.UNKNOWN, "reply lost") }
        val old = entry.generation
        d.detach(listOf(entry))
        assertFalse(d.ready("A")); assertFalse(entry.change(old, "A") { error("late progress after send") })
        entry.release(abort = false)
        outgoing.deliver { requests += it }
        assertEquals(0, aborts); assertEquals(requests[0], requests[1])
        assertEquals(listOf("file-file"), requests.last().fileIdsList)
    }

    @Test fun aRejectedNewStartDoesNotUnprotectAnEarlierUnknownRequestUsingTheSameTransfer() {
        val entry = ready(); val d = draft().apply { images += entry }
        var aborts = 0; entry.abortTransfer = { aborts++ }
        val start = Starts.Start().apply { taken = listOf(entry) }
        val unknownOwner = start.transferOwner
        val frozen = start.fileIds.toList()
        start.taken = listOf(entry) // a later prepared request after abandoning the unknown outcome
        assertNotSame(unknownOwner, start.transferOwner)
        entry.allowAbort(start.transferOwner) // only the later request was definitely refused
        d.remove(entry)
        assertEquals(0, aborts)
        assertEquals(listOf("file-file"), frozen)
    }

    @Test fun anUnknownNewSessionOwnsItsOriginalTransferWhenItsDraftIsRemoved() {
        val entry = ready(); val d = draft().apply { images += entry }
        var aborts = 0; entry.abortTransfer = { aborts++ }
        val start = Starts.Start().apply { taken = listOf(entry) }
        d.remove(entry)
        assertEquals(0, aborts)
        assertEquals(listOf("file-file"), start.fileIds)
        assertFalse(d.ready("A"))
    }
}
