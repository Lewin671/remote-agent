package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.proto.ImageFormat
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

@org.junit.runner.RunWith(org.robolectric.RobolectricTestRunner::class)
@org.robolectric.annotation.Config(sdk = [35], application = android.app.Application::class)
class RestoreImagesTest {
    @get:org.junit.Rule(order = 0) val sessionMessages = io.github.lewin671.remoteagent.ui.SessionMessageContextRule()
    private fun image() = PendingImage("picked", "content://picked").apply {
        processed = Processed(byteArrayOf(1, 2, 3), ImageFormat.IMAGE_FORMAT_PNG, 8, 8)
    }

    /** Records both disk/decryption calls and thumbnail work, as distinct from publishing on the UI caller. */
    private class Reads(val backing: MemoryFiles) : SealedFiles by backing {
        val threads = CopyOnWriteArrayList<Thread>()
        val entered = CompletableDeferred<Unit>()
        val release = CountDownLatch(1)
        var block = false
        override fun get(folder: String, name: String): ByteArray? {
            threads += Thread.currentThread()
            if (block) {
                entered.complete(Unit)
                check(release.await(5, TimeUnit.SECONDS)) { "test did not release disk" }
            }
            return backing.get(folder, name)
        }
    }

    @Test
    fun draftReadsAndThumbnailsRunOffTheUiCallerAndPublishWhenReady() = runBlocking {
        val backing = MemoryFiles()
        saveImages(backing, "session:restore", listOf(image()), true)
        saveOrder(backing, "session:restore", listOf(image()), true)
        val reads = Reads(backing)
        val caller = Thread.currentThread()
        var thumbnails = 0
        val draft = ImageDraft("session:restore", reads, scope = null)
        val job = draft.restore(this) { reads.threads += Thread.currentThread(); thumbnails++; null }
        assertTrue(draft.restoring)
        assertTrue(draft.images.isEmpty())
        assertFalse(draft.attach(listOf(image()), 6))
        job.join()
        assertFalse(draft.restoring)
        assertEquals(listOf("picked"), draft.images.map { it.id })
        assertTrue(draft.original)
        assertEquals(1, thumbnails)
        assertTrue(reads.threads.isNotEmpty())
        assertTrue(reads.threads.all { it !== caller })
    }

    @Test fun typingWhileOrderIsBeingReadKeepsTheSavedImagesAndUsesTheNewText() = runBlocking {
        val backing = MemoryFiles()
        val image = image()
        saveImages(backing, "session:typing", listOf(image), true)
        saveOrder(backing, "session:typing", listOf(image), true)
        backing.put("session:typing", "text", "saved text".toByteArray())
        val reads = Reads(backing).apply { block = true }
        val draft = ImageDraft("session:typing", reads, scope = null)
        val job = draft.restore(this) { null }
        try {
            reads.entered.await()
            draft.text.value = "typed during restore"
            assertEquals("saved text", backing.get("session:typing", "text")!!.toString(Charsets.UTF_8))
        } finally { reads.release.countDown() }
        job.join()
        assertEquals(listOf(image.id), draft.images.map { it.id })
        assertEquals("typed during restore", draft.text.value)
        assertEquals("typed during restore", backing.get("session:typing", "text")!!.toString(Charsets.UTF_8))
        assertEquals(listOf(image.id), loadImages(backing, "session:typing").first.map { it.id })
    }

    @Test fun aFailedRestoreIsRetriedOnEditBeforeAnyDraftOverwrite() = runBlocking {
        val backing = MemoryFiles()
        val image = image()
        saveImages(backing, "session:retry-read", listOf(image), true)
        saveOrder(backing, "session:retry-read", listOf(image), true)
        backing.put("session:retry-read", "text", "saved text".toByteArray())
        var fail = true
        val reads = object : SealedFiles by backing {
            override fun get(folder: String, name: String): ByteArray? {
                if (fail) { fail = false; throw java.io.IOException("temporary read failure") }
                return backing.get(folder, name)
            }
        }
        val draft = ImageDraft("session:retry-read", reads, scope = null)
        draft.restore(this) { null }.join()
        assertTrue(draft.awaitingRestore)
        draft.text.value = "typed after read failure"
        kotlinx.coroutines.withTimeout(5000) { while (draft.restoring) kotlinx.coroutines.delay(1) }
        assertFalse(draft.awaitingRestore)
        assertEquals(listOf(image.id), draft.images.map { it.id })
        assertEquals("typed after read failure", draft.text.value)
        assertEquals("typed after read failure", backing.get("session:retry-read", "text")!!.toString(Charsets.UTF_8))
    }

    @Test
    fun draftFinishingAfterLogoutCannotRestoreOldAccountImages() = runBlocking {
        val backing = MemoryFiles()
        saveImages(backing, "session:logout", listOf(image()), true)
        saveOrder(backing, "session:logout", listOf(image()), true)
        val reads = Reads(backing).apply { block = true }
        val draft = ImageDraft("session:logout", reads, scope = null)
        val job = draft.restore(this) { null }
        try {
            reads.entered.await()
            AccountMemory.clear()
        } finally { reads.release.countDown() }
        job.join()
        assertTrue(draft.images.isEmpty())
        assertFalse(draft.original)
    }

    @Test
    fun outgoingReadsAndThumbnailsRunOffTheUiCallerAndMergeNewMessages() = runBlocking {
        AccountMemory.clear()
        val backing = MemoryFiles()
        val original = Outgoing("old", "看图", listOf("uploaded"), listOf(image()), true)
        OutgoingRecords(backing).save("restore-out", original, true)
        val reads = Reads(backing).apply { block = true }
        val caller = Thread.currentThread()
        var thumbnails = 0
        val list = Outbox.of("restore-out")
        val job = Outbox.restore("restore-out", this, OutgoingRecords(reads)) { reads.threads += Thread.currentThread(); thumbnails++; null }
        assertTrue(list.isEmpty())
        try {
            reads.entered.await()
            list += Outgoing("new", "新消息")
        } finally { reads.release.countDown() }
        job.join()
        assertEquals(listOf("new", "old"), list.map { it.requestId })
        assertEquals(Outgoing.State.UNKNOWN, list.last().state)
        assertEquals(listOf("uploaded"), list.last().imageIds)
        assertEquals(1, thumbnails)
        assertTrue(reads.threads.all { it !== caller })
        AccountMemory.clear()
    }

    @Test
    fun outgoingFinishingAfterAccountSwitchCannotRepopulateEitherList() = runBlocking {
        AccountMemory.clear()
        val backing = MemoryFiles()
        OutgoingRecords(backing).save("shared-session", Outgoing("old", "旧账户", listOf("uploaded"), listOf(image())), false)
        val reads = Reads(backing).apply { block = true }
        val oldList = Outbox.of("shared-session")
        val job = Outbox.restore("shared-session", this, OutgoingRecords(reads)) { null }
        try {
            reads.entered.await()
            AccountMemory.use("next-account")
        } finally { reads.release.countDown() }
        val nextList = Outbox.of("shared-session")
        job.join()
        assertTrue(oldList.isEmpty())
        assertTrue(nextList.isEmpty())
        AccountMemory.clear()
    }
}
