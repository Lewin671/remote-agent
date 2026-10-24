package io.github.lewin671.remoteagent.push

import io.github.lewin671.remoteagent.core.CommandException
import io.github.lewin671.remoteagent.core.PreparedCommand
import io.github.lewin671.remoteagent.data.Settings
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.NotifyKind
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test

class ApprovalAttemptTest {
    private val command = PreparedCommand("m", "s", "r", 1, Envelope.getDefaultInstance())

    @Test fun noAccountAndLocallyDisabledKindsNeverShowPushes() {
        val attention = NotifyKind.NOTIFY_KIND_ATTENTION
        val turn = NotifyKind.NOTIFY_KIND_TURN_ENDED
        assertFalse(acceptPush(false, attention, Settings()))
        assertFalse(acceptPush(true, attention, Settings(notifyAttention = false)))
        assertFalse(acceptPush(true, turn, Settings()))
        assertTrue(acceptPush(true, attention, Settings()))
        assertTrue(acceptPush(true, turn, Settings(notifyTurnEnded = true)))
        assertFalse(acceptPush(true, NotifyKind.NOTIFY_KIND_UNSPECIFIED, Settings()))
    }

    @Test fun scheduledResultsAreIndependentOfOrdinaryTurnPreference() {
        val off = Settings(notifyAttention = false, notifyTurnEnded = false)
        assertTrue(acceptPush(true, NotifyKind.NOTIFY_KIND_SCHEDULED, off))
        assertFalse(acceptPush(false, NotifyKind.NOTIFY_KIND_SCHEDULED, off))
        assertFalse(acceptPush(true, NotifyKind.NOTIFY_KIND_SCHEDULED, off.copy(notifyScheduled = false)))
    }

    private var durable: ApprovalRecord? = null
    private val record = { r: ApprovalRecord? -> durable = r; true }

    @Test fun aProcessKilledDuringForwardLeavesAnUnknownMarker() = runBlocking {
        // Error models a killed process: ordinary exception handlers do not run.
        val killed = runCatching {
            sendApproval(command, durable, record) { _, retry ->
                assertEquals(ApprovalRecord.SENT, durable)
                assertFalse(retry)
                throw AssertionError("process killed")
            }
        }
        assertTrue(killed.exceptionOrNull() is AssertionError)
        assertEquals(ApprovalRecord.SENT, durable)
        val expired = runCatching {
            sendApproval(command, durable, record) { p, retry ->
                assertSame(command, p)
                assertTrue(retry)
                throw CommandException("deadline_exceeded", "expired", fromDaemon = true)
            }
        }.exceptionOrNull() as CommandException
        assertTrue(expired.unknown)
        assertEquals(ApprovalRecord.SENT, durable)
    }

    @Test fun aProcessKilledAfterTheAnswerStillKnowsTheReplyWentThrough() = runBlocking {
        // The computer confirmed it; the process dies before WorkManager records the worker's success.
        sendApproval(command, durable, record) { _, _ -> }
        // WorkManager runs the same input again, by now past the command's deadline.
        var sent = 0
        sendApproval(command, durable, record) { _, _ ->
            sent++
            throw CommandException(CommandException.EXPIRED, "expired, not sent")
        }
        assertEquals(0, sent)
        assertEquals(ApprovalRecord.DONE, durable)
    }

    @Test fun anUnrecordedAttemptCannotLeaveThePhone() = runBlocking {
        var sent = false
        assertTrue(runCatching {
            sendApproval(command, null, { false }) { _, _ -> sent = true }
        }.isFailure)
        assertFalse(sent)
    }

    @Test fun aDefiniteFailureForgetsTheReply() = runBlocking {
        val refused = runCatching {
            sendApproval(command, ApprovalRecord.SENT, record) { _, _ -> throw CommandException("permission_denied", "refused", fromDaemon = true) }
        }.exceptionOrNull() as CommandException
        assertFalse(refused.unknown)
        assertNull(durable)
    }
}
