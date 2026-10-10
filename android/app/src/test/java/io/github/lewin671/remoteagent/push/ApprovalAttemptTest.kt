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

    @Test fun aProcessKilledDuringForwardLeavesAnUnknownMarker() = runBlocking {
        var durable = false
        // Error models a killed process: ordinary exception handlers do not run.
        val killed = runCatching {
            sendApproval(command, false, { durable = true; true }, { durable = false }) { _, retry ->
                assertTrue(durable)
                assertFalse(retry)
                throw AssertionError("process killed")
            }
        }
        assertTrue(killed.exceptionOrNull() is AssertionError)
        assertTrue(durable)
        val expired = runCatching {
            sendApproval(command, durable, { durable = true; true }, { durable = false }) { p, retry ->
                assertSame(command, p)
                assertTrue(retry)
                throw CommandException("deadline_exceeded", "expired", fromDaemon = true)
            }
        }.exceptionOrNull() as CommandException
        assertTrue(expired.unknown)
        assertTrue(durable)
    }

    @Test fun anUnrecordedAttemptCannotLeaveThePhone() = runBlocking {
        var sent = false
        assertTrue(runCatching {
            sendApproval(command, false, { false }, {}) { _, _ -> sent = true }
        }.isFailure)
        assertFalse(sent)
    }

    @Test fun aDefiniteAnswerClearsTheMarker() = runBlocking {
        var durable = false
        val mark = { durable = true; true }
        val clear = { durable = false }
        sendApproval(command, false, mark, clear) { _, _ -> }
        assertFalse(durable)
        val refused = runCatching {
            sendApproval(command, true, mark, clear) { _, _ -> throw CommandException("permission_denied", "refused", fromDaemon = true) }
        }.exceptionOrNull() as CommandException
        assertFalse(refused.unknown)
        assertFalse(durable)
    }
}
