package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.proto.*
import kotlinx.coroutines.*
import org.junit.Assert.*
import org.junit.Test
import java.io.ByteArrayInputStream

class FileUploadLifecycleTest {
    private open class Channel : UploadChannel {
        val begins = mutableListOf<String>()
        val aborted = mutableListOf<String>()
        var total = 0L
        var completeCalls = 0
        fun receiving(n: Long = 0) = ImageUploadStatus.newBuilder().setState(ImageUploadState.IMAGE_UPLOAD_STATE_RECEIVING).setPersistedBytes(n).build()
        override suspend fun begin(transferId: String, format: ImageFormat, total: Long, sha256: ByteArray): ImageUploadStatus = error("image")
        override suspend fun beginFile(transferId: String, name: String, total: Long, sha256: ByteArray): ImageUploadStatus {
            begins += transferId; this.total = total; return receiving()
        }
        override suspend fun put(transferId: String, offset: Long, data: ByteArray) = receiving(offset + data.size)
        override suspend fun status(transferId: String) = receiving(total)
        override suspend fun abort(transferId: String) { aborted += transferId }
        override suspend fun complete(transferId: String): ImageUploadStatus {
            completeCalls++
            return ImageUploadStatus.newBuilder().setState(ImageUploadState.IMAGE_UPLOAD_STATE_COMPLETE).setFileId("file-$transferId").setExpiresMs(Long.MAX_VALUE).build()
        }
    }
    private fun file(id: String) = PendingImage(id, "content://documents/$id", "$id.txt", 999)
    private fun draft(id: String, store: MemoryFiles = MemoryFiles()) = ImageDraft("session:$id", store)

    @Test fun sentFilesOnlyHaveNoEmptyTextBubble() {
        assertNull(sentBubbleText("", false, true))
        assertNull(sentBubbleText(" \n", false, true))
        assertEquals("hello", sentBubbleText("hello", false, true))
        assertEquals("", sentBubbleText("", false, false))
    }

    @Test fun actualSizeAndTransferStateSurviveRestartAndReceivingFullCompletesAgain() = runBlocking {
        val store = MemoryFiles()
        val d = draft("resume", store)
        val f = file("resume").apply { machineId = "m"; transferId = "same-transfer" }
        d.images += f
        val ch = object : Channel() {
            override suspend fun beginFile(transferId: String, name: String, total: Long, sha256: ByteArray): ImageUploadStatus {
                super.beginFile(transferId, name, total, sha256)
                return receiving(total)
            }
            override suspend fun complete(transferId: String): ImageUploadStatus {
                if (completeCalls++ == 0) return receiving(total)
                return ImageUploadStatus.newBuilder().setState(ImageUploadState.IMAGE_UPLOAD_STATE_COMPLETE).setFileId("ready").setExpiresMs(Long.MAX_VALUE).build()
            }
        }
        d.startDocumentUpload(f, "m", this, ch, 100, Dispatchers.Unconfined) { ByteArrayInputStream(byteArrayOf(1, 2, 3)) }
        f.job!!.join()
        assertEquals(3L, f.fileBytes)
        assertEquals(listOf("same-transfer"), ch.begins)
        assertEquals(2, ch.completeCalls)
        val back = loadImages(store, d.key).first.single()
        assertEquals("same-transfer", back.transferId)
        assertEquals("ready", back.fileId)
        assertTrue(back.readyFor("m"))
        assertTrue(back.sizeVerified)
        assertEquals(3L, back.fileBytes)
        // A receiving transfer restores its original ID as well.
        f.fileId = null; f.expiresMs = 0; f.state = PendingImage.State.Failed("连接中断"); d.persistOrder()
        assertEquals("same-transfer", loadImages(store, d.key).first.single().transferId)
    }

    @Test fun supersededCompletionCannotOverwriteTheNewMachineAndRemovalAborts() = runBlocking {
        val d = draft("race"); val f = file("race"); d.images += f
        val started = CompletableDeferred<Unit>(); val release = CompletableDeferred<Unit>()
        val old = object : Channel() {
            override suspend fun complete(transferId: String): ImageUploadStatus = withContext(NonCancellable) {
                started.complete(Unit); release.await(); super.complete(transferId)
            }
        }
        d.startDocumentUpload(f, "old", this, old, 100, Dispatchers.Unconfined) { ByteArrayInputStream(byteArrayOf(1)) }
        started.await()
        val oldId = f.transferId
        val newer = Channel()
        d.startDocumentUpload(f, "new", this, newer, 100, Dispatchers.Unconfined) { ByteArrayInputStream(byteArrayOf(2, 3)) }
        yield()
        assertEquals(PendingImage.State.Processing, f.state)
        release.complete(Unit)
        f.job!!.join()
        assertEquals("new", f.machineId)
        assertEquals(2L, f.fileBytes)
        assertEquals("file-${f.transferId}", f.fileId)
        assertTrue(oldId in old.aborted)
        val newId = f.transferId
        d.remove(f); yield()
        assertTrue(newId in newer.aborted)
        assertFalse(f.current(f.generation - 1, "new"))
    }

    @Test fun alreadyExistsAndHashMismatchAbortAndRestartOnce() = runBlocking {
        for (failure in listOf(CommandException("already_exists", "other parameters"), CommandException("invalid_argument", "the bytes do not match the hash"))) {
            val d = draft("retry-${failure.code}"); val f = file("retry"); d.images += f
            val ch = object : Channel() {
                override suspend fun complete(transferId: String): ImageUploadStatus {
                    if (begins.size == 1) throw failure
                    return super.complete(transferId)
                }
            }
            d.startDocumentUpload(f, "m", this, ch, 100, Dispatchers.Unconfined) { ByteArrayInputStream(byteArrayOf(1)) }
            f.job!!.join()
            assertEquals(2, ch.begins.size)
            assertNotEquals(ch.begins[0], ch.begins[1])
            assertEquals(listOf(ch.begins[0]), ch.aborted)
            assertTrue(f.readyFor("m"))
        }
    }

    @Test fun hashingAndQueuedFilesStayPreparingAndUploadsAreGloballySerial() = runBlocking {
        val d1 = draft("serial1"); val d2 = draft("serial2")
        val a = file("a"); val b = file("b"); d1.images += a; d2.images += b
        val start = CompletableDeferred<Unit>(); val release = CompletableDeferred<Unit>()
        val ch1 = object : Channel() {
            override suspend fun beginFile(transferId: String, name: String, total: Long, sha256: ByteArray): ImageUploadStatus {
                start.complete(Unit); release.await(); return super.beginFile(transferId, name, total, sha256)
            }
        }
        var preparingObserved = false
        d1.startDocumentUpload(a, "m", this, ch1, 100, Dispatchers.Unconfined) {
            preparingObserved = a.state == PendingImage.State.Processing
            ByteArrayInputStream(byteArrayOf(1))
        }
        start.await()
        var secondOpened = false
        d2.startDocumentUpload(b, "m", this, Channel(), 100, Dispatchers.Unconfined) { secondOpened = true; ByteArrayInputStream(byteArrayOf(2)) }
        yield()
        assertTrue(preparingObserved)
        assertFalse(secondOpened)
        assertEquals(PendingImage.State.Processing, b.state)
        release.complete(Unit); a.job!!.join(); b.job!!.join()
        assertTrue(secondOpened)
    }

    @Test fun anUnresolvedMachinePreservesRestoredCompletedFilesUntilARealSwitch() = runBlocking {
        val disk = MemoryFiles()
        val original = file("large").apply {
            fileBytes = MAX_FILE_BYTES; sizeVerified = true
            machineId = "saved-machine"; transferId = "saved-transfer"; fileId = "saved-file"
            expiresMs = Long.MAX_VALUE; state = PendingImage.State.Ready
        }
        saveImages(disk, "new:project", listOf(original), false)
        saveOrder(disk, "new:project", listOf(original), false)
        val restored = loadImages(disk, "new:project").first.single()
        val d = ImageDraft("new:project", disk).apply { images += restored }
        var aborted = 0
        restored.abortTransfer = { aborted++ }
        val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "test"), okhttp3.OkHttpClient())
        try {
            val turn = restored.generation
            d.retarget(remote, null) // ProjectStart/follow has not resolved yet after process death.
            assertEquals(0, aborted)
            assertEquals(turn, restored.generation)
            assertEquals("saved-transfer", restored.transferId)
            assertEquals("saved-file", restored.fileId)
            assertEquals(Long.MAX_VALUE, restored.expiresMs)
            assertTrue(restored.readyFor("saved-machine"))
            d.retarget(remote, "saved-machine")
            assertEquals(0, aborted)
            assertTrue(restored.readyFor("saved-machine"))
            d.retarget(remote, "other-machine")
            assertEquals(1, aborted)
            assertNull(restored.fileId)
            assertNotEquals("saved-transfer", restored.transferId)
        } finally { remote.close() }
    }

    @Test fun bindingARestoredOutboxEntryAbortsItsOldTransferWhenPuttingItBack() = runBlocking {
        val disk = MemoryFiles()
        val original = file("pending").apply {
            machineId = "m"; transferId = "old-transfer"; fileId = "old-file"
            expiresMs = Long.MAX_VALUE; state = PendingImage.State.Ready
        }
        val records = OutgoingRecords(disk)
        records.save("s", Outgoing("request", "message", images = listOf(original), fileIds = listOf("old-file")), false)
        val restored = records.load("s").single().images.single()
        assertNull(restored.abortTransfer)
        val channel = Channel()
        val d = draft("putback")
        d.bindAbort(restored, channel, this)
        assertTrue(d.attach(listOf(restored), 6))
        yield()
        assertEquals(listOf("old-transfer"), channel.aborted)
        assertNull(restored.fileId)
        assertNotEquals("old-transfer", restored.transferId)
    }

    @Test fun errorsAndUriOwnershipArePrecise() {
        assertEquals("上传连接中断，请重试", uploadErrorText(java.io.IOException("network")))
        assertTrue(uploadErrorText(FileSourceException(java.io.IOException())).contains("源文件"))
        assertTrue(uploadErrorText(CommandException("aborted", "the transfer changed")).contains("发生变化"))
        assertTrue(uploadErrorText(CommandException("invalid_argument", "the bytes do not match the hash")).contains("校验失败"))
        assertEquals(emptySet<String>(), unusedUriPermissions(setOf("u"), setOf("u"), emptySet()))
        assertEquals(emptySet<String>(), unusedUriPermissions(setOf("u"), emptySet(), setOf("u")))
        assertEquals(setOf("u"), unusedUriPermissions(setOf("u"), emptySet(), emptySet()))
        assertEquals(emptySet<String>(), unusedUriPermissions(setOf("u"), emptySet(), null))
    }
}
