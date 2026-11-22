package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.core.proto.Event as CoreEvent
import io.github.lewin671.remoteagent.core.proto.SecurityBootstrapKind
import io.github.lewin671.remoteagent.core.proto.SecurityPairingInfo
import io.github.lewin671.remoteagent.core.proto.SecurityWorkflowActionEvent
import io.github.lewin671.remoteagent.core.proto.SecurityWorkflowResultEffect
import io.github.lewin671.remoteagent.core.proto.SecurityWorkflowStatus
import io.github.lewin671.remoteagent.proto.DeviceKind
import java.util.concurrent.ConcurrentHashMap
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.cancelChildren
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class PairingControllerTest {
    @get:org.junit.Rule val hangGuard = hangGuard()

    private class FakeSecurityDriver {
        private val actions = ConcurrentHashMap<Long, CompletableDeferred<Unit>>()
        val startedCodes = mutableListOf<String>()
        val cancelledIds = mutableListOf<String>()
        val approvedIds = mutableListOf<String>()
        val finishDeliveryCalls = mutableListOf<String>()
        var nextApproveOutcome: (() -> SecurityWorkflowResultEffect.Builder)? = null
        var nextPollOutcome: (() -> SecurityWorkflowResultEffect.Builder)? = null
        private var startCount = 0

        lateinit var client: SecurityClient

        fun initClient(resolver: MessageResolver = ::messageTextForTest): SecurityClient {
            client = SecurityClient(
                dispatchEvent = { ev: CoreEvent ->
                    val act = ev.securityAction
                    handleAction(act)
                },
                setForegroundFn = {},
                isClosedFn = { false },
                messageResolver = resolver,
            )
            return client
        }

        private fun okStatus(): SecurityWorkflowStatus = SecurityWorkflowStatus.newBuilder()
            .setBootstrap(SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_READY)
            .build()

        private fun handleAction(act: SecurityWorkflowActionEvent) {
            val id = act.actionId
            when (act.actionCase) {
                SecurityWorkflowActionEvent.ActionCase.START_PAIRING -> {
                    val idx = ++startCount
                    val pid = "pair-$idx"
                    startedCodes += pid
                    client.handleWorkflowResult(
                        SecurityWorkflowResultEffect.newBuilder()
                            .setActionId(id)
                            .setOk(true)
                            .setStatus(okStatus())
                            .setPairing(
                                SecurityPairingInfo.newBuilder()
                                    .setPairingId(pid)
                                    .setCode("1234567$idx")
                                    .setUrl("https://app.example.com/#/pair?code=1234567$idx&s=https://relay.example.com")
                                    .setExpiresMs(1_800_000_000_000L),
                            )
                            .build(),
                    )
                }
                SecurityWorkflowActionEvent.ActionCase.POLL_PAIRING -> {
                    val pid = act.pollPairing.value
                    val builder = nextPollOutcome?.invoke() ?: SecurityWorkflowResultEffect.newBuilder()
                        .setOk(true)
                        .setStatus(okStatus())
                        .setPairing(
                            SecurityPairingInfo.newBuilder()
                                .setPairingId(pid)
                                .setJoined(true)
                                .setJoinerKind(DeviceKind.DEVICE_KIND_CLIENT)
                                .addAllSasEmoji(listOf("🐊", "🥯", "🐩", "🦌")),
                        )
                    client.handleWorkflowResult(builder.setActionId(id).build())
                }
                SecurityWorkflowActionEvent.ActionCase.APPROVE_PAIRING -> {
                    val pid = act.approvePairing.pairingId
                    approvedIds += pid
                    val outcome = nextApproveOutcome
                    if (outcome != null) {
                        nextApproveOutcome = null
                        client.handleWorkflowResult(outcome().setActionId(id).build())
                    } else {
                        val gate = CompletableDeferred<Unit>()
                        actions[id] = gate
                        gate.invokeOnCompletion { cause ->
                            if (cause == null) {
                                client.handleWorkflowResult(
                                    SecurityWorkflowResultEffect.newBuilder()
                                        .setActionId(id)
                                        .setOk(true)
                                        .setStatus(okStatus())
                                        .setPairing(
                                            SecurityPairingInfo.newBuilder()
                                                .setPairingId(pid)
                                                .setDelivered(true),
                                        )
                                        .build(),
                                )
                            }
                        }
                    }
                }
                SecurityWorkflowActionEvent.ActionCase.FINISH_PAIRING_DELIVERY -> {
                    finishDeliveryCalls += act.finishPairingDelivery.value
                    client.handleWorkflowResult(
                        SecurityWorkflowResultEffect.newBuilder()
                            .setActionId(id)
                            .setOk(true)
                            .setStatus(okStatus())
                            .setPairing(
                                SecurityPairingInfo.newBuilder()
                                    .setPairingId(act.finishPairingDelivery.value)
                                    .setDelivered(true),
                            )
                            .build(),
                    )
                }
                SecurityWorkflowActionEvent.ActionCase.CANCEL_PAIRING -> {
                    cancelledIds += act.cancelPairing.value
                    client.handleWorkflowResult(
                        SecurityWorkflowResultEffect.newBuilder()
                            .setActionId(id)
                            .setOk(true)
                            .setStatus(okStatus())
                            .build(),
                    )
                }
                else -> {}
            }
        }

        fun completePendingApproval() {
            val id = actions.keys.first()
            actions.remove(id)?.complete(Unit)
        }
    }

    @Test
    fun deliverySurvivesBackgroundAndPageDisposalAndReopeningShowsItsOutcome() = runTest {
        val foreground = CoroutineScope(coroutineContext + SupervisorJob(coroutineContext[Job]))
        val driver = FakeSecurityDriver()
        val client = driver.initClient()
        var completions = 0
        val model = PairingController(
            scope = foreground,
            operationScope = backgroundScope,
            client = client,
            webOrigin = "https://app.example.com",
            onCompleted = { completions++ },
        )
        try {
            model.enter(); runCurrent()
            advanceTimeBy(1_000L); runCurrent()
            model.approve(); runCurrent()
            foreground.coroutineContext.cancelChildren(); runCurrent()
            model.leave(); model.enter(); runCurrent()
            assertTrue(model.state.value is PairingUiState.Sending)
            assertEquals(1, driver.startedCodes.size)
            assertEquals(1, driver.approvedIds.size)
            assertTrue(driver.cancelledIds.isEmpty())
            driver.completePendingApproval(); runCurrent()
            model.enter(); runCurrent()
            assertEquals(PairingUiState.Done, model.state.value)
            assertEquals(1, completions)
        } finally {
            foreground.cancel()
        }
    }

    @Test
    fun leavingBeforeConsentStillWithdrawsThePairing() = runTest {
        val driver = FakeSecurityDriver()
        val model = PairingController(
            scope = this,
            operationScope = backgroundScope,
            client = driver.initClient(),
            webOrigin = "https://app.example.com",
        )
        model.enter(); runCurrent(); model.leave(); runCurrent()
        assertEquals(listOf("pair-1"), driver.cancelledIds)
        assertTrue(driver.approvedIds.isEmpty())
        assertEquals(PairingUiState.Starting, model.state.value)
    }

    @Test
    fun aRawServerErrorIsTranslatedWithoutUncertainRetry() = runTest {
        val driver = FakeSecurityDriver()
        val client = driver.initClient()
        driver.nextApproveOutcome = {
            SecurityWorkflowResultEffect.newBuilder()
                .setOk(false)
                .setError("security.pairing_expired_or_was_canceled")
                .setErrorCode("not_found")
                .setRetryable(false)
                .setUncertain(false)
        }
        val model = PairingController(this, backgroundScope, client, "https://app.example.com")
        model.start(); runCurrent(); advanceTimeBy(1_000L); runCurrent(); model.approve(); runCurrent()
        val failure = model.state.value as PairingUiState.Failed
        assertFalse(failure.message.resolve(::messageTextForTest).contains("no such pairing"))
        assertFalse(failure.canRetry)
    }

    @Test
    fun failureActionRetriesTheSameApprovedPairingWithoutWithdrawal() = runTest {
        val driver = FakeSecurityDriver()
        val client = driver.initClient()
        driver.nextApproveOutcome = {
            SecurityWorkflowResultEffect.newBuilder()
                .setOk(false)
                .setError("security.the_security_operation_s_outcome_is_unverified_continue_the")
                .setErrorCode("unavailable")
                .setRetryable(true)
                .setUncertain(true)
        }
        val model = PairingController(this, backgroundScope, client, "https://app.example.com")
        model.start(); runCurrent()
        advanceTimeBy(1_000L); runCurrent()
        model.approve(); runCurrent()
        assertTrue((model.state.value as PairingUiState.Failed).canRetry)
        model.retry(); runCurrent()
        assertEquals(2, driver.approvedIds.size)
        assertEquals(1, driver.startedCodes.size)
        assertTrue(driver.cancelledIds.isEmpty())
        driver.completePendingApproval(); runCurrent()
        assertEquals(PairingUiState.Done, model.state.value)
        model.leave(); runCurrent()
        assertTrue(driver.cancelledIds.isEmpty())
    }

    @Test
    fun expiredApprovalRequiresExplicitReplacementAndWithdrawsTheOldOne() = runTest {
        val driver = FakeSecurityDriver()
        val client = driver.initClient()
        driver.nextApproveOutcome = {
            SecurityWorkflowResultEffect.newBuilder()
                .setOk(false)
                .setError("security.pairing_expired_or_was_canceled")
                .setErrorCode("not_found")
                .setRetryable(false)
                .setUncertain(false)
        }
        val model = PairingController(this, backgroundScope, client, "https://app.example.com")
        model.start(); runCurrent(); advanceTimeBy(1_000L); runCurrent(); model.approve(); runCurrent()
        assertFalse((model.state.value as PairingUiState.Failed).canRetry)
        model.retry(); runCurrent()
        assertEquals(2, driver.startedCodes.size)
        assertEquals(listOf("pair-1"), driver.cancelledIds)
        assertEquals(listOf("pair-1"), driver.approvedIds)
        model.leave(); runCurrent()
        assertEquals(listOf("pair-1", "pair-2"), driver.cancelledIds)
    }
}
