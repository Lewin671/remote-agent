package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.proto.GetRecoveryContextResponse.Phase
import org.junit.Assert.*
import org.junit.Test

class Review4ActionsTest {
    @Test
    fun rejectedRecoveryOffersRestartInsteadOfCompletingTheVetoedOperation() {
        assertEquals(RecoveryNextAction.RESTART, recoveryNextAction(Phase.PHASE_REJECTED))
        for (phase in
            listOf(
                null,
                Phase.PHASE_PENDING,
                Phase.PHASE_READY,
                Phase.PHASE_PUBLISHED,
            )) assertEquals(RecoveryNextAction.COMPLETE, recoveryNextAction(phase))
    }

    @Test
    fun bothClientNameActionsRequireThisClientsOwnership() {
        assertFalse(clientCanRename("other-client", "this-client", ConnState.ONLINE))
        assertTrue(clientCanRename("this-client", "this-client", ConnState.ONLINE))
        assertFalse(clientCanRename("this-client", "this-client", ConnState.OFFLINE))
    }
}

class RejectedRecoveryRestartTest {
    @Test
    fun explicitRestartClearsDurableOperationBeforeResettingTheForm() =
        kotlinx.coroutines.runBlocking {
            val file = kotlin.io.path.createTempFile("rejected-recovery").toFile()
            file.writeBytes(byteArrayOf(1, 2, 3))
            var showsSubmission = true
            try {
                assertTrue(
                    restartRejectedRecovery(
                        Phase.PHASE_REJECTED,
                        clearPending = { check(file.delete()) },
                        resetScreen = {
                            assertFalse(file.exists())
                            showsSubmission = false
                        },
                    ),
                )
                assertFalse(showsSubmission)
            } finally {
                file.delete()
            }
        }

    @Test
    fun failedStorageClearPreservesTheFormAndOperation() =
        kotlinx.coroutines.runBlocking {
            var reset = false
            val error = runCatching {
                restartRejectedRecovery(
                    Phase.PHASE_REJECTED,
                    clearPending = { throw java.io.IOException("clear failed") },
                    resetScreen = { reset = true },
                )
            }
                .exceptionOrNull()
            assertTrue(error is java.io.IOException)
            assertFalse(reset)
        }

    @Test
    fun pendingReadyAndUnknownOperationsCannotBeDiscardedAsRejected() =
        kotlinx.coroutines.runBlocking {
            for (phase in
                listOf(
                    null,
                    Phase.PHASE_PENDING,
                    Phase.PHASE_READY,
                    Phase.PHASE_PUBLISHED,
                )) assertFalse(
                restartRejectedRecovery(
                    phase,
                    clearPending = { fail("discarded a non-rejected operation") },
                    resetScreen = { fail("reset a non-rejected form") },
                ),
            )
        }
}
