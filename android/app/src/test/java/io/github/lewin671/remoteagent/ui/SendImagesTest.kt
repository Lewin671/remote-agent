package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.InputState
import io.github.lewin671.remoteagent.proto.InputStatus
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class SendImagesTest {
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
    fun theLatestStateOfEachMessageComesFromTheEvents() {
        fun input(seq: Long, id: String, st: InputStatus) = Event.newBuilder().setSeq(seq).setInputState(InputState.newBuilder().setRequestId(id).setStatus(st)).build()
        val m = inputStates(listOf(input(1, "a", InputStatus.INPUT_STATUS_QUEUED), input(2, "a", InputStatus.INPUT_STATUS_SUBMITTED), input(3, "b", InputStatus.INPUT_STATUS_QUEUED)))
        assertEquals(InputStatus.INPUT_STATUS_SUBMITTED, m["a"])
        assertEquals(InputStatus.INPUT_STATUS_QUEUED, m["b"])
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
        Outbox.settle(sid, mapOf("a" to InputStatus.INPUT_STATUS_SUBMITTED))
        // The one the events say nothing of stays, with its retry.
        assertEquals(listOf(failed), Outbox.of(sid).toList())
    }

    /** A Remote that is never reached when keeping the images fails. */
    private fun throwingRemote(): io.github.lewin671.remoteagent.core.Remote =
        io.github.lewin671.remoteagent.core.Remote(
            io.github.lewin671.remoteagent.core.Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "dev"),
            okhttp3.OkHttpClient(),
        )
}
