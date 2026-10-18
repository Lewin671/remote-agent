package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.DeviceKind
import java.io.IOException
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class PairingControllerTest {
    private class Pairing : PairingInitiation {
        override val code = "12345678"
        override val link = PairLink(code, "https://relay.example.com")
        val approvals = mutableListOf<CompletableDeferred<Unit>>()
        var cancellations = 0
        override suspend fun joined() = listOf("🐊", "🥯", "🐩", "🦌") to DeviceKind.DEVICE_KIND_CLIENT
        override suspend fun approve() { CompletableDeferred<Unit>().also { approvals += it }.await() }
        override suspend fun cancelOnServer() { cancellations++ }
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
