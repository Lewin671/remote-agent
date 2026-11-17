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

@org.junit.runner.RunWith(org.robolectric.RobolectricTestRunner::class)
@org.robolectric.annotation.Config(sdk = [35], application = android.app.Application::class)
class SendImagesTest {
    @get:org.junit.Rule(order = 0) val sessionMessages = io.github.lewin671.remoteagent.ui.SessionMessageContextRule()
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
    fun takeBackAttachesRetainedImagesToDraftWithoutDuplicates() {
        val sid = "s-takeback"
        val image = PendingImage("picked", null).apply {
            processed = Processed(byteArrayOf(1, 2), io.github.lewin671.remoteagent.proto.ImageFormat.IMAGE_FORMAT_PNG, 8, 8)
        }
        Outbox.of(sid) += Outgoing("same-request", listOf(image), original = true)
        val draft = ImageDraft("session:recover", MemoryFiles())
        assertTrue(Outbox.takeBack(sid, "same-request", draft))
        assertSame(image, draft.images.single())
        assertTrue(draft.original)
        assertTrue(needsUpload(image, "mac"))
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
    fun pruneSettledReleasesLocalCopiesWhenCoreNoLongerListsRequest() {
        val sid = "s-settled"
        val keepImg = PendingImage("i1", null).apply {
            processed = Processed(byteArrayOf(1, 2, 3), io.github.lewin671.remoteagent.proto.ImageFormat.IMAGE_FORMAT_JPEG, 10, 20)
        }
        val dropImg = PendingImage("i2", null).apply {
            processed = Processed(byteArrayOf(4, 5, 6), io.github.lewin671.remoteagent.proto.ImageFormat.IMAGE_FORMAT_JPEG, 10, 20)
        }
        Outbox.of(sid) += listOf(Outgoing("keep", listOf(keepImg)), Outgoing("drop", listOf(dropImg)))
        Outbox.pruneSettled(sid, setOf("keep"))
        assertNotNull(Outbox.find(sid, "keep"))
        assertNull(Outbox.find(sid, "drop"))
        assertNotNull(keepImg.processed)
        assertNull(dropImg.processed)
    }

    /** A Remote that is never reached when keeping the images fails. */
    private fun throwingRemote(): io.github.lewin671.remoteagent.core.Remote =
        io.github.lewin671.remoteagent.core.Remote(
            io.github.lewin671.remoteagent.core.Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "dev"),
            okhttp3.OkHttpClient(),
        )
}
