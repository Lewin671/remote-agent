package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import org.junit.Assert.*
import org.junit.Test

class QueueSnapshotTest {
    private fun input(seq: Long, status: InputStatus, paused: Boolean = false) = Event.newBuilder()
        .setSeq(seq).setInputState(InputState.newBuilder().setRequestId("r").setStatus(status)
            .setText("example prompt").setPaused(paused)
            .setStage(if (paused) InputStage.INPUT_STAGE_SUBMITTING else InputStage.INPUT_STAGE_WAITING)
            .setMode(if (paused) SendMode.SEND_MODE_QUEUE else SendMode.SEND_MODE_STEER)).build()
    private fun live(seq: Long, vararg actions: Event, running: Boolean = true) = LiveSnapshot.newBuilder()
        .setSession(Session.newBuilder().setId("s").setLastSeq(seq))
        .setRunning(running).addAllActions(actions.toList()).build()

    @Test fun newerOutcomesCannotBeRevivedByQueuedOrHeldSnapshots() {
        for (pending in listOf(InputStatus.INPUT_STATUS_QUEUED, InputStatus.INPUT_STATUS_HELD_BY_CLI)) {
            for (status in listOf(InputStatus.INPUT_STATUS_SUBMITTED, InputStatus.INPUT_STATUS_CANCELED,
                InputStatus.INPUT_STATUS_FAILED, InputStatus.INPUT_STATUS_NEEDS_TERMINAL)) {
                val history = derive(listOf(input(1, InputStatus.INPUT_STATUS_QUEUED), input(2, status),
                    Event.newBuilder().setSeq(3).setTurnEnded(TurnEnded.getDefaultInstance()).build()))
                val result = currentActions(history, live(1, input(0, pending)))
                assertEquals(status, result.inputs["r"])
                assertTrue(pendingInputs(result).isEmpty())
                assertFalse(result.running)
            }
        }
    }

    @Test fun emptyOldSnapshotCannotHideNewQueueAndOldDetailsCannotReplaceNewDetails() {
        val history = derive(listOf(input(2, InputStatus.INPUT_STATUS_QUEUED, paused = true)))
        val empty = currentActions(history, live(1))
        assertEquals(InputStatus.INPUT_STATUS_QUEUED, empty.inputs["r"])
        assertEquals(1, pendingInputs(empty).size)
        val other = input(0, InputStatus.INPUT_STATUS_QUEUED).toBuilder()
            .setInputState(input(0, InputStatus.INPUT_STATUS_QUEUED).inputState.toBuilder().setRequestId("other")).build()
        val result = currentActions(history, live(1, input(0, InputStatus.INPUT_STATUS_HELD_BY_CLI), other))
        assertEquals(history.inputDetails["r"], result.inputDetails["r"])
        assertEquals(history.accepted["r"], result.accepted["r"])
        assertEquals(InputStatus.INPUT_STATUS_QUEUED, result.inputs["other"])
        assertNull(history.inputs["other"])
    }

    @Test fun refreshedSnapshotAtWatermarkCanRemoveOrRestorePendingState() {
        val history = derive(listOf(input(1, InputStatus.INPUT_STATUS_QUEUED)))
        assertEquals(InputStatus.INPUT_STATUS_UNCONFIRMED, currentActions(history, live(1)).inputs["r"])
        val restored = currentActions(history, live(1, input(0, InputStatus.INPUT_STATUS_HELD_BY_CLI, paused = true)))
        assertEquals(InputStatus.INPUT_STATUS_HELD_BY_CLI, restored.inputs["r"])
        assertTrue(restored.inputDetails["r"]!!.paused)
        assertEquals(SendMode.SEND_MODE_QUEUE, restored.inputDetails["r"]!!.mode)
        assertEquals(InputStage.INPUT_STAGE_SUBMITTING, restored.inputDetails["r"]!!.stage)
        assertEquals(1, pendingInputs(restored).size)
    }

    @Test fun runningUsesLastTransitionNotUnrelatedEventsAndProjectionPreservesWatermarks() {
        val start = Event.newBuilder().setSeq(2).setTurnStarted(TurnStarted.getDefaultInstance()).build()
        val end = Event.newBuilder().setSeq(3).setTurnEnded(TurnEnded.getDefaultInstance()).build()
        val unrelated = Event.newBuilder().setSeq(4).setAgentMessage(AgentMessage.newBuilder().setText("reply")).build()
        val projection = EventProjection()
        val first = projection.update(listOf(input(1, InputStatus.INPUT_STATUS_QUEUED), start))
        assertTrue(currentActions(first, live(1, running = false)).running)
        val later = projection.update(listOf(input(1, InputStatus.INPUT_STATUS_QUEUED), start, end, unrelated))
        assertFalse(currentActions(later, live(1)).running)
        assertTrue(currentActions(later, live(3)).running)
        assertEquals(1L, later.inputSeqs["r"])
        assertEquals(2L, first.runningSeq)
        assertTrue(first.running)
    }
}
