package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.PrefsOp
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.yield
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Test

class PrefsChangesTest {
    private val path = "/work/app"
    private val pin = PrefsOp.Pin(path)
    private val settings = PrefsOp.SetSettings(path, "mac")

    /** The server as the project preferences are written to it: it holds each write until the test lets it end. */
    private val asked = mutableListOf<PrefsOp>()
    private val open = ArrayDeque<CompletableDeferred<Unit>>()
    private val write: suspend (PrefsOp) -> Unit = { op ->
        asked += op
        CompletableDeferred<Unit>().also { open.addLast(it) }.await()
    }

    @After
    fun forget() = PrefsChanges.clear()

    /** Lets what the last answer started run: everything here is on the test's own thread. */
    private suspend fun settle() = repeat(20) { yield() }

    @Test
    fun repeatingTheOnlyPendingChangeDoesNotStartAnotherWorker() = runBlocking {
        val done = mutableListOf<String>()
        PrefsChanges.run(this, pin, { done += "pin" }, write)
        settle()
        PrefsChanges.run(this, pin, { done += "again" }, write)
        PrefsChanges.run(this, settings, { done += "settings" }, write)
        settle()
        val singleWorker = asked == listOf<PrefsOp>(pin) && open.size == 1
        if (!singleWorker) {
            PrefsChanges.clear()
            while (open.isNotEmpty()) open.removeFirst().completeExceptionally(java.io.IOException("连接超时"))
            settle()
        }
        assertEquals(true, singleWorker)
        open.removeFirst().complete(Unit)
        settle()
        assertEquals(listOf(pin, settings), asked)
        assertEquals(PrefsChanges.State.Saving, PrefsChanges.states[path])
        open.removeFirst().complete(Unit)
        settle()
        assertEquals(listOf("pin", "settings"), done)
        assertNull(PrefsChanges.states[path])
    }

    @Test
    fun aClearedWorkersSuccessDoesNotConsumeTheNewAccountsQueue() = runBlocking {
        var oldDone = false
        PrefsChanges.run(this, pin, { oldDone = true }, write)
        settle()
        val oldWrite = open.removeFirst()
        AccountMemory.clear()
        PrefsChanges.run(this, settings, write = write)
        PrefsChanges.run(this, PrefsOp.Hide(path), write = write)
        settle()
        oldWrite.complete(Unit)
        settle()
        val kept = asked == listOf(pin, settings) && open.size == 1 && !oldDone
        if (!kept) {
            PrefsChanges.clear()
            while (open.isNotEmpty()) open.removeFirst().completeExceptionally(java.io.IOException("连接超时"))
            settle()
        }
        assertEquals(true, kept)
        open.removeFirst().complete(Unit)
        settle()
        assertEquals(listOf(pin, settings, PrefsOp.Hide(path)), asked)
        open.removeFirst().complete(Unit)
        settle()
        assertNull(PrefsChanges.states[path])
        assertFalse(oldDone)
    }

    @Test
    fun aClearedWorkersFailureDoesNotRestoreTheOldAccountsOperation() = runBlocking {
        PrefsChanges.run(this, pin, write = write)
        settle()
        val oldWrite = open.removeFirst()
        AccountMemory.clear()
        oldWrite.completeExceptionally(java.io.IOException("连接超时"))
        settle()
        assertNull(PrefsChanges.states[path])
    }

    @Test
    fun aClearedWorkersFailureDoesNotReplaceTheNewAccountsState() = runBlocking {
        PrefsChanges.run(this, pin, write = write)
        settle()
        val oldWrite = open.removeFirst()
        AccountMemory.clear()
        PrefsChanges.run(this, settings, write = write)
        settle()
        oldWrite.completeExceptionally(java.io.IOException("连接超时"))
        settle()
        val state = PrefsChanges.states[path]
        open.removeFirst().complete(Unit)
        settle()
        assertEquals(PrefsChanges.State.Saving, state)
        assertNull(PrefsChanges.states[path])
    }

    @Test
    fun aChangeMadeWhileAnotherIsOnItsWayIsWrittenAfterItNotDropped() = runBlocking {
        val done = mutableListOf<String>()
        PrefsChanges.run(this, pin, { done += "pin" }, write)
        // 完成 in 项目设置 while the pin is still on its way.
        PrefsChanges.run(this, settings, { done += "settings" }, write)
        // A second tap on the same thing is not a second change.
        PrefsChanges.run(this, settings, { done += "again" }, write)
        settle()
        assertEquals(listOf<PrefsOp>(pin), asked)
        assertEquals(PrefsChanges.State.Saving, PrefsChanges.states[path])
        open.removeFirst().complete(Unit)
        settle()
        assertEquals(listOf(pin, settings), asked)
        assertEquals(listOf("pin"), done)
        assertEquals(PrefsChanges.State.Saving, PrefsChanges.states[path])
        open.removeFirst().complete(Unit)
        settle()
        assertNull(PrefsChanges.states[path])
        assertEquals(listOf("pin", "settings"), done)
        assertEquals(2, asked.size)
    }

    @Test
    fun repeatingTheOnlyInFlightChangeDoesNotStartAnotherWriter() = runBlocking {
        val done = mutableListOf<String>()
        PrefsChanges.run(this, pin, { done += "pin" }, write)
        settle()
        PrefsChanges.run(this, pin, { done += "duplicate" }, write)
        settle()
        if (asked.size != 1) {
            val count = asked.size
            while (open.isNotEmpty()) open.removeFirst().completeExceptionally(java.io.IOException("连接超时"))
            settle()
            assertEquals(1, count)
            return@runBlocking
        }
        PrefsChanges.run(this, settings, { done += "settings" }, write)
        PrefsChanges.run(this, PrefsOp.Hide(path), { done += "hide" }, write)
        open.removeFirst().complete(Unit)
        settle()
        assertEquals(listOf(pin, settings), asked)
        assertEquals(1, open.size)
        assertEquals(listOf("pin"), done)
        open.removeFirst().complete(Unit)
        settle()
        assertEquals(1, open.size)
        assertEquals(listOf("pin", "settings"), done)
        open.removeFirst().complete(Unit)
        settle()
        assertNull(PrefsChanges.states[path])
        assertEquals(listOf("pin", "settings", "hide"), done)
    }

    @Test
    fun aChangeThatFailsKeepsThoseBehindItUntilItIsTriedAgain() = runBlocking {
        val done = mutableListOf<String>()
        PrefsChanges.run(this, pin, { done += "pin" }, write)
        PrefsChanges.run(this, settings, { done += "settings" }, write)
        settle()
        open.removeFirst().completeExceptionally(java.io.IOException("连接超时"))
        settle()
        assertEquals(PrefsChanges.State.Failed(pin, "连接超时"), PrefsChanges.states[path])
        assertEquals("the settings wait behind the change that failed", listOf<PrefsOp>(pin), asked)
        // 重试: the pin again, once, and then the settings that waited.
        PrefsChanges.run(this, pin, write = write)
        settle()
        open.removeFirst().complete(Unit)
        settle()
        open.removeFirst().complete(Unit)
        settle()
        assertNull(PrefsChanges.states[path])
        assertEquals(listOf(pin, pin, settings), asked)
        assertEquals(listOf("pin", "settings"), done)
        // A change made after a failure takes the failed one along, in the order they were made.
        val unpin = PrefsOp.Unpin(path)
        val hide = PrefsOp.Hide(path)
        PrefsChanges.run(this, unpin, write = write)
        settle()
        open.removeFirst().completeExceptionally(java.io.IOException("连接超时"))
        settle()
        PrefsChanges.run(this, hide, write = write)
        settle()
        open.removeFirst().complete(Unit)
        settle()
        open.removeFirst().complete(Unit)
        settle()
        assertNull(PrefsChanges.states[path])
        assertEquals(listOf(unpin, unpin, hide), asked.takeLast(3))
    }
}
