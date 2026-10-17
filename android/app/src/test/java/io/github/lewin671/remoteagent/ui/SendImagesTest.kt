package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.InputState
import io.github.lewin671.remoteagent.proto.InputStatus
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test

class SendImagesTest {
    @Test
    fun anImageExpiringWhileItsRecordIsSavedDoesNotSendOrDetach() = kotlinx.coroutines.runBlocking {
        val image = PendingImage("picked", null).apply {
            processed = Processed(byteArrayOf(1), io.github.lewin671.remoteagent.proto.ImageFormat.IMAGE_FORMAT_PNG, 8, 8)
            machineId = "mac"
            uploaded(io.github.lewin671.remoteagent.core.UploadedImage("uploaded", Long.MAX_VALUE))
        }
        val disk = MemoryFiles()
        val records = OutgoingRecords(disk)
        val remote = throwingRemote()
        val done = kotlinx.coroutines.CompletableDeferred<Result<Unit>>()
        try {
            Outbox.sendWithImages(this, remote, "expires-during-save", "保留文字", listOf("uploaded"), listOf(image), false,
                store = { sid, o, orig -> records.save(sid, o, orig); image.expiresMs = 0 },
                discard = records::remove,
            ) {
                assertTrue("expired uploads must fail before detaching or sending", it.isFailure)
                done.complete(it)
            }
            assertTrue(done.await().isFailure)
            assertTrue(Outbox.of("expires-during-save").isEmpty())
            assertTrue(records.load("expires-during-save").isEmpty())
            assertNotNull(image.processed)
            assertTrue(needsUpload(image, "mac"))
        } finally { remote.close() }
    }

    @Test
    fun missingImagesOfferRecoveryWithTextAndOriginalBytes() = kotlinx.coroutines.runBlocking {
        val image = PendingImage("picked", null).apply {
            processed = Processed(byteArrayOf(1, 2), io.github.lewin671.remoteagent.proto.ImageFormat.IMAGE_FORMAT_PNG, 8, 8)
        }
        val outgoing = Outgoing("same-request", "保留文字", listOf("expired"), listOf(image), original = true)
        outgoing.deliver { throw io.github.lewin671.remoteagent.core.CommandException("not_found", "image expired is not on this machine; upload it again", fromDaemon = true) }
        assertEquals(Outgoing.State.FAILED, outgoing.state)
        assertTrue(outgoing.missingImages)
        assertTrue(outgoing.canPutBack)
        assertTrue(outgoing.error.contains("文字和图片都会保留"))
        val draft = ImageDraft("session:recover", MemoryFiles())
        assertTrue(draft.attach(outgoing.images, 6, outgoing.original))
        assertSame(image, draft.images.single())
        assertTrue(draft.original)
        assertEquals("保留文字", outgoing.text)
        assertTrue(needsUpload(image, "mac"))
    }

    @Test
    fun unknownMessagesRetryTheSameRequestAndImagesWithoutOfferingANewSend() = kotlinx.coroutines.runBlocking {
        val outgoing = Outgoing("same-request", "看图", listOf("original-id"), listOf(PendingImage("picked", null)))
        val requests = mutableListOf<io.github.lewin671.remoteagent.proto.SendRequest>()
        outgoing.deliver { requests += it; throw io.github.lewin671.remoteagent.core.CommandException(io.github.lewin671.remoteagent.core.CommandException.UNKNOWN, "reply lost") }
        assertEquals(Outgoing.State.UNKNOWN, outgoing.state)
        assertFalse(outgoing.canPutBack)
        outgoing.deliver { requests += it; throw io.github.lewin671.remoteagent.core.CommandException("unavailable", "offline") }
        assertEquals(Outgoing.State.UNKNOWN, outgoing.state)
        assertFalse(outgoing.canPutBack)
        outgoing.deliver { requests += it }
        assertEquals(Outgoing.State.SENT, outgoing.state)
        assertEquals(listOf(requests.first(), requests.first(), requests.first()), requests)
        assertEquals("same-request", requests.last().requestId)
        assertEquals(listOf("original-id"), requests.last().imageIdsList)
    }

    @Test
    fun aSaveThatFinishesAfterLogoutDoesNotSendOrCallTheOldPage() = kotlinx.coroutines.runBlocking {
        val entered = kotlinx.coroutines.CompletableDeferred<Unit>()
        val release = java.util.concurrent.CountDownLatch(1)
        val stored = kotlinx.coroutines.CompletableDeferred<Unit>()
        val remote = throwingRemote()
        Outbox.sendWithImages(this, remote, "old-account", "看图", listOf("i"), listOf(PendingImage("a", null)), false,
            store = { _, _, _ -> entered.complete(Unit); release.await(); stored.complete(Unit) },
        ) { throw AssertionError("old page called") }
        entered.await()
        AccountMemory.clear()
        release.countDown()
        stored.await()
        // runBlocking also waits for the sending job, whose callback must never run.
        assertTrue(Outbox.of("old-account").isEmpty())
        remote.close()
    }

    @Test
    fun aMessageWhoseImagesCouldNotBeKeptIsNotSent() {
        val result = kotlinx.coroutines.runBlocking {
            val done = kotlinx.coroutines.CompletableDeferred<Result<Unit>>()
            Outbox.sendWithImages(
                this, remote = throwingRemote(), sid = "s-keep-fails", text = "看图",
                imageIds = listOf("i1"), images = listOf(PendingImage("a", null)), original = false,
                store = { _, _, _ -> throw java.io.IOException("disk full") },
            ) { done.complete(it) }
            done.await()
        }
        assertTrue(result.isFailure)
        // Nothing went out, and nothing waits here as if it had.
        assertTrue(Outbox.of("s-keep-fails").isEmpty())
    }

    @Test
    fun aSecondSendWhileTheImagesAreBeingKeptSendsNothing() {
        val sid = "s-keep-twice"
        val disk = java.util.concurrent.CountDownLatch(1)
        val stored = java.util.concurrent.atomic.AtomicInteger()
        kotlinx.coroutines.runBlocking {
            val done = kotlinx.coroutines.CompletableDeferred<Result<Unit>>()
            // Keeping fails in the end, so that nothing here has to reach a computer.
            fun send(onKept: (Result<Unit>) -> Unit) = Outbox.sendWithImages(
                this, remote = throwingRemote(), sid = sid, text = "看图",
                imageIds = listOf("i1"), images = listOf(PendingImage("a", null)), original = false,
                store = { _, _, _ -> stored.incrementAndGet(); disk.await(); throw java.io.IOException("disk full") },
                onKept = onKept,
            )
            send { done.complete(it) }
            assertTrue(Outbox.keeping(sid))
            // The message box as it is composed again after a look at the terminal: the lock is not its own.
            send { throw AssertionError("a second message was kept") }
            disk.countDown()
            assertTrue(done.await().isFailure)
        }
        assertEquals(1, stored.get())
        assertFalse(Outbox.keeping(sid))
    }

    @Test
    fun theLatestStateOfEachMessageComesFromTheEvents() {
        fun input(seq: Long, id: String, st: InputStatus) = Event.newBuilder().setSeq(seq).setInputState(InputState.newBuilder().setRequestId(id).setStatus(st)).build()
        val m = inputStates(listOf(input(1, "a", InputStatus.INPUT_STATUS_QUEUED), input(2, "a", InputStatus.INPUT_STATUS_SUBMITTED), input(3, "b", InputStatus.INPUT_STATUS_QUEUED)))
        assertEquals(InputStatus.INPUT_STATUS_SUBMITTED, m["a"]?.status)
        assertEquals(InputStatus.INPUT_STATUS_QUEUED, m["b"]?.status)
    }

    @Test
    fun aMessageIsShownUnderTheChatUntilTheEventsHaveIt() {
        assertTrue(outgoingShown(null))
        assertFalse(outgoingShown(InputStatus.INPUT_STATUS_QUEUED))
        assertFalse(outgoingShown(InputStatus.INPUT_STATUS_HELD_BY_CLI))
        // Whatever the try from here ended with, lost answer included: the CLI recorded the message.
        assertFalse(outgoingShown(InputStatus.INPUT_STATUS_SUBMITTED))
        assertFalse(outgoingShown(InputStatus.INPUT_STATUS_CANCELED))
        assertTrue(outgoingShown(InputStatus.INPUT_STATUS_FAILED))
        assertTrue(outgoingShown(InputStatus.INPUT_STATUS_NEEDS_TERMINAL))
        assertTrue(outgoingShown(InputStatus.INPUT_STATUS_UNCONFIRMED))
    }

    @Test
    fun aMessageWhoseAnswerWasLostIsLetGoOnceTheCliRecordedIt() {
        val sid = "s-settle-text"
        val lost = Outgoing("a", "甲").also { it.state = Outgoing.State.UNKNOWN }
        val failed = Outgoing("b", "乙").also { it.state = Outgoing.State.FAILED }
        Outbox.of(sid) += listOf(lost, failed)
        Outbox.settle(sid, mapOf("a" to state(InputStatus.INPUT_STATUS_SUBMITTED)))
        // The one the events say nothing of stays, with its retry.
        assertEquals(listOf(failed), Outbox.of(sid).toList())
    }

    @Test
    fun aMessageCanceledElsewhereIsLetGoAfterItsAnswerWasLost() {
        val sid = "s-canceled-text"
        val lost = Outgoing("a", "甲").also { it.state = Outgoing.State.UNKNOWN }
        val failed = Outgoing("b", "乙").also { it.state = Outgoing.State.FAILED }
        Outbox.of(sid) += listOf(lost, failed)
        Outbox.settle(sid, mapOf("a" to state(InputStatus.INPUT_STATUS_CANCELED)))
        assertEquals(listOf(failed), Outbox.of(sid).toList())
    }

    @Test
    fun aMessageTheSessionEndedOnIsKeptToBePutBack() {
        val sid = "s-exited"
        val image = PendingImage("i1", null).apply {
            processed = Processed(byteArrayOf(1, 2, 3), io.github.lewin671.remoteagent.proto.ImageFormat.IMAGE_FORMAT_JPEG, 10, 20)
        }
        val waiting = Outgoing("a", "甲", listOf("on-mac"), listOf(image))
        Outbox.of(sid) += waiting
        Outbox.settle(sid, mapOf("a" to state(InputStatus.INPUT_STATUS_QUEUED)))
        Outbox.settle(sid, mapOf("a" to state(InputStatus.INPUT_STATUS_CANCELED, "exited")))
        // What 放回输入框 on the chat's row finds: the text and the images.
        assertSame(waiting, Outbox.find(sid, "a"))
        assertEquals("甲", Outbox.text(sid, "a"))
        assertEquals(listOf(image), Outbox.find(sid, "a")?.images)
        assertNotNull(image.processed)
        // Canceled by the user instead, it is let go of with its images.
        Outbox.settle(sid, mapOf("a" to state(InputStatus.INPUT_STATUS_CANCELED)))
        assertNull(Outbox.find(sid, "a"))
        assertNull(image.processed)
    }

    private fun state(status: InputStatus, reason: String = "") = InputState.newBuilder().setStatus(status).setReason(reason).build()

    /** A Remote that is never reached when keeping the images fails. */
    private fun throwingRemote(): io.github.lewin671.remoteagent.core.Remote =
        io.github.lewin671.remoteagent.core.Remote(
            io.github.lewin671.remoteagent.core.Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "dev"),
            okhttp3.OkHttpClient(),
        )
}
