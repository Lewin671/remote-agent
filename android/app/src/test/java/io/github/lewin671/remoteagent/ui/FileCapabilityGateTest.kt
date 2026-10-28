package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.proto.*
import kotlinx.coroutines.*
import org.junit.Assert.*
import org.junit.Test

class FileCapabilityGateTest {
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

    @Test fun putBackChecksBeforeDraftTransferAbortTextOrFailedRecordChanges() {
        val r = remote()
        tray()
        try {
            for (support in listOf(false, null)) for (local in listOf(false, true)) {
                show(r, support)
                val f = file(); var aborts = 0; f.abortTransfer = { aborts++ }
                val o = Outgoing("failed", "keep text", images = if (local) listOf(f) else emptyList(), fileIds = listOf("original-file")).apply { state = Outgoing.State.FAILED }
                Outbox.of("capability").clear(); Outbox.of("capability") += o
                Outbox.draft("capability").value = "existing text"
                val d = ImageDrafts.forSession("capability")
                val before = d.images.toList(); val generation = f.generation
                assertFalse(putBack(r, "capability", o))
                assertEquals(before, d.images.toList()); assertEquals("existing text", Outbox.draft("capability").value)
                assertSame(o, Outbox.find("capability", "failed")); assertEquals(generation, f.generation)
                assertEquals(0, aborts); assertEquals("original-file", f.fileId)
                assertTrue(d.notice.contains("更新电脑上的 remote-agent"))
            }
        } finally { r.close(); AccountMemory.clear() }
    }

    @Test fun historyRestoreGatesRemoteOnlyFilesBeforeChangingTheDraftAndRecoversAfterUpgrade() {
        val r = remote(); val d = tray(); val files = listOf(SentFile.newBuilder().setFileId("old-file").setName("keep.txt").setSizeBytes(42).build())
        try {
            Outbox.draft("capability").value = "existing"
            for (support in listOf(false, null)) {
                show(r, support)
                assertFalse(putBackReferences(r, "capability", "restored", files, listOf("old-image")))
                assertTrue(d.images.isEmpty()); assertEquals("existing", Outbox.draft("capability").value)
                assertTrue(d.notice.contains("更新电脑上的 remote-agent"))
            }
            show(r, true)
            assertTrue(putBackReferences(r, "capability", "restored", files, listOf("old-image")))
            assertEquals("restored", Outbox.draft("capability").value)
            assertEquals(2, d.images.size); assertTrue(d.images.all { it.state is PendingImage.State.Failed })
            assertFalse(d.ready("computer")); assertEquals("keep.txt", d.documents.single().fileName)
        } finally { r.close(); AccountMemory.clear() }
    }

    @Test fun capabilityLostWhileSavingLeavesTheSourceAndTextInTheDraft() = runBlocking {
        val r = remote(); show(r, true); val f = file(); val disk = MemoryFiles(); val records = OutgoingRecords(disk)
        val done = CompletableDeferred<Result<Unit>>()
        try {
            Outbox.sendWithImages(this, r, "capability", "keep text", emptyList(), listOf(f), false,
                store = { sid, o, orig -> records.save(sid, o, orig); show(r, false) }, discard = records::remove,
                fileIds = listOf("original-file")) { done.complete(it) }
            val result = done.await()
            assertTrue(result.isFailure); assertTrue(result.exceptionOrNull()!!.message.orEmpty().contains("更新电脑上的 remote-agent"))
            assertTrue(records.load("capability").isEmpty()); assertTrue(Outbox.of("capability").isEmpty())
            assertEquals("original-file", f.fileId); assertTrue(f.readyFor("computer"))
        } finally { r.close(); AccountMemory.clear() }
    }

    @Test fun unsupportedSendNeverSavesOrDetachesTheDraft() = runBlocking {
        val r = remote(); show(r, false); val done = CompletableDeferred<Result<Unit>>()
        try {
            Outbox.sendWithImages(this, r, "capability", "keep text", emptyList(), listOf(file()), false,
                store = { _, _, _ -> error("must gate before saving") }, fileIds = listOf("original-file")) { done.complete(it) }
            assertTrue(done.await().exceptionOrNull()!!.message.orEmpty().contains("更新电脑上的 remote-agent"))
            assertFalse(Outbox.keeping("capability")); assertTrue(Outbox.of("capability").isEmpty())
        } finally { r.close(); AccountMemory.clear() }
    }

    @Test fun failedAndUnknownOutboxRetriesGateWithoutLosingIdentityOrTreatingUnknownAsRejected() = runBlocking {
        val r = remote(); show(r, false)
        try {
            for (state in listOf(Outgoing.State.FAILED, Outgoing.State.UNKNOWN)) {
                val o = Outgoing("original-request", "keep text", fileIds = listOf("original-file")).apply { this.state = state }
                coroutineScope { Outbox.deliver(this, r, "capability", o) }
                assertEquals(state, o.state); assertTrue(o.error.contains("更新电脑上的 remote-agent"))
                assertEquals(listOf("original-file"), o.fileIds); assertEquals("original-request", o.requestId)
                assertEquals("keep text", o.text)
            }
        } finally { r.close(); AccountMemory.clear() }
    }

    @Test fun processDeathAndDraftChangesCannotChangeThePreparedSendIdentityOrDeadline() = runBlocking {
        val r = remote(); show(r, true); val f = file(); val store = MemoryFiles()
        val out = Outgoing("original-request", "keep text", images = listOf(f), fileIds = listOf("original-file"), targetMachineId = "computer")
        try {
            val request = SendRequest.newBuilder().setRequestId(out.requestId).setText(out.text).addAllFileIds(out.fileIds).build()
            val first = out.prepareDelivery(r, "capability", request)
            OutgoingRecords(store).save("capability", out, false)
            val back = OutgoingRecords(store).load("capability").single()
            assertEquals(out.commandRequestId, back.commandRequestId)
            assertEquals(out.commandDeadlineMs, back.commandDeadlineMs); assertEquals("computer", back.targetMachineId)
            back.images.single().fileId = "superseding-file"
            show(r, false)
            coroutineScope { Outbox.deliver(this, r, "capability", back) }
            assertEquals(Outgoing.State.UNKNOWN, back.state)
            show(r, true)
            var again: PreparedCommand? = null
            back.deliver { again = back.prepareDelivery(r, "capability", it) }
            assertEquals(first.requestId, again!!.requestId); assertEquals(first.deadlineMs, again!!.deadlineMs)
            assertEquals(listOf("original-file"), back.fileIds)
        } finally { r.close(); AccountMemory.clear() }
    }

    @Test fun aDefiniteFailureCanRenewAnExpiredCommandButAnUnknownResultCannot() = runBlocking {
        val r = remote(); show(r, true); val disk = MemoryFiles(); val records = OutgoingRecords(disk)
        val out = Outgoing("message", "keep text", fileIds = listOf("original-file"), commandDeadlineMs = 1, targetMachineId = "computer").apply { state = Outgoing.State.FAILED }
        try {
            val old = out.commandRequestId
            out.deliver { request ->
                val p = out.prepareDelivery(r, "capability", request)
                assertNotEquals(old, p.requestId); assertTrue(p.deadlineMs > System.currentTimeMillis())
                assertEquals("message", request.requestId)
                records.save("capability", out, false)
                throw CommandException(CommandException.UNKNOWN, "lost reply")
            }
            val frozenId = out.commandRequestId; val frozenDeadline = out.commandDeadlineMs
            out.deliver { throw CommandException("failed_precondition", "local capability rejected") }
            assertEquals(Outgoing.State.UNKNOWN, out.state)
            assertEquals(frozenId, out.commandRequestId); assertEquals(frozenDeadline, out.commandDeadlineMs)
            val back = records.load("capability").single()
            assertEquals(frozenId, back.commandRequestId); assertEquals(frozenDeadline, back.commandDeadlineMs)
            // The intent-only path also persists the renewed ID before another possible dispatch.
            back.state = Outgoing.State.FAILED
            back.deliver { records.saveIntent("capability", back); throw CommandException(CommandException.UNKNOWN, "lost reply") }
            assertEquals(back.commandRequestId, records.load("capability").single().commandRequestId)
        } finally { r.close(); AccountMemory.clear() }
    }
}
