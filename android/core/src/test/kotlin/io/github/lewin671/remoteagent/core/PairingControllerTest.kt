package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.DeviceKind
import java.io.IOException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.Job
import kotlinx.coroutines.cancel
import kotlinx.coroutines.cancelChildren
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class PairingControllerTest {
    private class Pairing : PairingInitiation {
        override val expiresMs = 1_800_000_000_000L
        override val code = "12345678"
        override val link = PairLink(code, "https://relay.example.com")
        val approvals = mutableListOf<CompletableDeferred<Unit>>()
        var cancellations = 0
        override suspend fun joined() = listOf("🐊", "🥯", "🐩", "🦌") to DeviceKind.DEVICE_KIND_CLIENT
        override suspend fun approve() { CompletableDeferred<Unit>().also { approvals += it }.await() }
        override suspend fun cancelOnServer() { cancellations++ }
    }

    @Test fun deliverySurvivesBackgroundAndPageDisposalAndReopeningShowsItsOutcome() = runTest {
        val foreground = CoroutineScope(coroutineContext + SupervisorJob(coroutineContext[Job]))
        val pairing = Pairing()
        var begins = 0
        val model = PairingController(foreground, backgroundScope) { begins++; pairing }
        try {
            model.enter(); runCurrent()
            model.approve(); runCurrent()
            foreground.coroutineContext.cancelChildren(); runCurrent()
            model.leave(); model.enter(); runCurrent()
            assertTrue(model.state.value is PairingUiState.Sending)
            assertEquals(1, begins)
            assertEquals(1, pairing.approvals.size)
            assertEquals(0, pairing.cancellations)
            pairing.approvals.single().complete(Unit); runCurrent()
            model.enter(); runCurrent()
            assertEquals(PairingUiState.Done, model.state.value)
        } finally { foreground.cancel() }
    }

    @Test fun leavingBeforeConsentStillWithdrawsThePairing() = runTest {
        val pairing = Pairing()
        val model = PairingController(this, backgroundScope) { pairing }
        model.enter(); runCurrent(); model.leave(); runCurrent()
        assertEquals(1, pairing.cancellations)
        assertTrue(pairing.approvals.isEmpty())
        assertEquals(PairingUiState.Starting, model.state.value)
    }

    @Test fun aRawServerErrorIsNeverShownAsAnUncertainEnglishBanner() = runTest {
        val pairing = Pairing()
        val model = PairingController(this, backgroundScope) { pairing }
        model.start(); runCurrent(); model.approve(); runCurrent()
        pairing.approvals.single().completeExceptionally(
            com.connectrpc.ConnectException(com.connectrpc.Code.NOT_FOUND, "no such pairing, or it expired"),
        ); runCurrent()
        val failure = model.state.value as PairingUiState.Failed
        assertFalse(failure.message.contains("no such pairing"))
        assertFalse(failure.canRetry)
    }

    @Test fun failureActionRetriesTheSameApprovedPairingWithoutWithdrawal() = runTest {
        val pairing = Pairing(); var begins = 0
        val model = PairingController(this, backgroundScope) { begins++; pairing }
        model.start(); runCurrent()
        model.approve(); runCurrent()
        pairing.approvals[0].completeExceptionally(IOException("lost response")); runCurrent()
        assertTrue((model.state.value as PairingUiState.Failed).canRetry)
        model.retry(); runCurrent()
        assertEquals(2, pairing.approvals.size)
        assertEquals(1, begins)
        assertEquals(0, pairing.cancellations)
        pairing.approvals[1].complete(Unit); runCurrent()
        assertEquals(PairingUiState.Done, model.state.value)
        model.leave(); runCurrent()
        assertEquals(0, pairing.cancellations)
    }

    @Test fun expiredApprovalRequiresExplicitReplacementAndWithdrawsTheOldOne() = runTest {
        val first = Pairing(); val second = Pairing(); var begins = 0
        val model = PairingController(this, backgroundScope) { if (begins++ == 0) first else second }
        model.start(); runCurrent(); model.approve(); runCurrent()
        first.approvals[0].completeExceptionally(PairException("配对已过期")); runCurrent()
        assertFalse((model.state.value as PairingUiState.Failed).canRetry)
        model.retry(); runCurrent()
        assertEquals(2, begins)
        assertEquals(1, first.cancellations)
        assertTrue(second.approvals.isEmpty())
        model.leave(); runCurrent(); assertEquals(1, second.cancellations)
    }

    @Test fun aLateCreationAfterLeavingIsWithdrawnInsteadOfDisplayed() = runTest {
        val pending = CompletableDeferred<PairingInitiation>(); val pairing = Pairing()
        val model = PairingController(this, backgroundScope) { pending.await() }
        model.start(); runCurrent(); model.leave(); pending.complete(pairing); runCurrent()
        assertEquals(1, pairing.cancellations)
        assertEquals(PairingUiState.Starting, model.state.value)
    }

    @Test fun anOldApprovalCannotCompleteAReplacementAttempt() = runTest {
        val first = Pairing(); val second = Pairing(); var begins = 0
        val model = PairingController(this, backgroundScope) { if (begins++ == 0) first else second }
        model.start(); runCurrent(); model.approve(); runCurrent()
        model.start(); runCurrent()
        first.approvals[0].complete(Unit); runCurrent()
        assertTrue(model.state.value is PairingUiState.Compare)
        assertEquals(1, first.cancellations)
        assertTrue(second.approvals.isEmpty())
        model.leave(); runCurrent()
    }
}
