package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.HandoffActionKind
import io.github.lewin671.remoteagent.core.proto.HandoffViewModel
import kotlinx.coroutines.launch

/** The successor client only offers its public recovery key after explicit backup confirmation. */
@Composable
fun HandoffPreparation(onBack: (() -> Unit)? = null) {
    val remote = LocalRemote.current
    val host = remote.coreHost
    val vm by host.handoffView.collectAsState()
    LaunchedEffect(host) { host.handoffAction(HandoffActionKind.HANDOFF_ACTION_KIND_OPEN) }
    HandoffPreparationContent(
        vm = vm,
        onPrepareCode = { host.handoffAction(HandoffActionKind.HANDOFF_ACTION_KIND_PREPARE_CODE) },
        onPublishOffer = {
            host.handoffAction(HandoffActionKind.HANDOFF_ACTION_KIND_SET_CODE_SAVED, flag = true)
            host.handoffAction(HandoffActionKind.HANDOFF_ACTION_KIND_PUBLISH_OFFER)
        },
        onBack = onBack,
    )
}

@Composable
internal fun HandoffPreparationContent(
    vm: HandoffViewModel,
    onPrepareCode: () -> Unit,
    onPublishOffer: () -> Unit,
    onBack: (() -> Unit)? = null,
) {
    val copy = uiText()
    val err = if (vm.errorBanner.visible) uiText(vm.errorBanner.text) else ""
    val msg = uiText(vm.messageBanner)
    PageScaffold(copy(UiMessages.securityTakeOverManagement()), onBack = onBack) {
        FormColumn(Modifier.fillMaxWidth().verticalScrollFaded().padding(vertical = 16.dp)) {
            Text(
                copy(UiMessages.securitySaveTheNewRecoveryCodeFirstThenAskThe()),
                style = T.s13,
                color = pal.ink2,
            )
            if (vm.preparedCode.isNotEmpty()) {
                RecoveryCodeFigure(vm.preparedCode)
                PillButton(
                    copy(UiMessages.securitySavedNotifyManagingClient()),
                    onPublishOffer,
                    Modifier.fillMaxWidth().padding(top = 12.dp),
                    ButtonKind.INK,
                    enabled = !vm.busy,
                )
            } else {
                if (vm.hasPublishedOffer) {
                    Hint(copy(UiMessages.securityRecoveryCodeSavedAndConfirmedWaitingForTheCurrent()), Modifier.padding(vertical = 12.dp))
                }
                PillButton(
                    if (vm.hasPublishedOffer) copy(UiMessages.securityRegenerateRecoveryCode()) else copy(UiMessages.securityGenerateNewRecoveryCode()),
                    onPrepareCode,
                    Modifier.fillMaxWidth().padding(top = 12.dp),
                    ButtonKind.SECONDARY,
                    enabled = !vm.busy,
                )
            }
            if (msg.isNotEmpty()) Notice(msg, Modifier.padding(top = 12.dp), NoticeTone.PLAIN)
            if (err.isNotEmpty()) Notice(err, Modifier.padding(top = 12.dp), NoticeTone.ERROR)
        }
    }
}

/** The current authority authenticates the successor client through ordinary pairing first. */
@Composable
fun HandoffControls() {
    val remote = LocalRemote.current
    val host = remote.coreHost
    val vm by host.handoffView.collectAsState()
    LaunchedEffect(host) { host.handoffAction(HandoffActionKind.HANDOFF_ACTION_KIND_OPEN) }
    HandoffControlsContent(
        vm = vm,
        onRefresh = { host.handoffAction(HandoffActionKind.HANDOFF_ACTION_KIND_REFRESH_CANDIDATES) },
        onRequestTransfer = { id ->
            host.handoffAction(HandoffActionKind.HANDOFF_ACTION_KIND_REQUEST_TRANSFER, targetDeviceId = id)
        },
        onCancelTransfer = { host.handoffAction(HandoffActionKind.HANDOFF_ACTION_KIND_CANCEL_TRANSFER) },
        onConfirmTransfer = { id ->
            host.handoffAction(HandoffActionKind.HANDOFF_ACTION_KIND_CONFIRM_TRANSFER, targetDeviceId = id)
        },
    )
}

@Composable
internal fun HandoffControlsContent(
    vm: HandoffViewModel,
    onRefresh: () -> Unit = {},
    onRequestTransfer: (String) -> Unit = {},
    onCancelTransfer: () -> Unit = {},
    onConfirmTransfer: (String) -> Unit = {},
) {
    val copy = uiText()
    val err = if (vm.errorBanner.visible) uiText(vm.errorBanner.text) else ""
    val msg = uiText(vm.messageBanner)
    GLabel(copy(UiMessages.securityTransferManagement()))
    Hint(copy(UiMessages.securityOnceTheSuccessorDeviceJoinsAndConfirmsSavingIts()), Modifier)
    for (c in vm.candidatesList) {
        val label = uiText(c.displayName).ifEmpty { c.deviceId }
        PillButton(
            copy(UiMessages.securityAndroidTransferTo(label)),
            { onRequestTransfer(c.deviceId) },
            Modifier.fillMaxWidth().padding(top = 8.dp),
            ButtonKind.SECONDARY,
            enabled = vm.canTransfer && !vm.busy,
        )
    }
    if (vm.candidatesList.isEmpty()) {
        Hint(copy(UiMessages.securityWaitingForTheSuccessorDeviceToGenerateAndSave()), Modifier.padding(vertical = 8.dp))
    }
    if (vm.canRefresh) {
        TextAction(copy(UiMessages.securityRefreshSuccessorDevices()), onRefresh, enabled = !vm.busy)
    }
    if (msg.isNotEmpty()) Notice(msg, Modifier.padding(vertical = 8.dp), NoticeTone.PLAIN)
    if (err.isNotEmpty()) Notice(err, Modifier.padding(vertical = 8.dp), NoticeTone.ERROR)
    if (vm.confirmingTargetId.isNotEmpty()) {
        val targetId = vm.confirmingTargetId
        val body = uiText(vm.confirmDialogBody).ifEmpty {
            val candLabel = vm.candidatesList.firstOrNull { it.deviceId == targetId }?.let { uiText(it.displayName) }?.ifEmpty { targetId } ?: targetId
            copy(UiMessages.securityConfirmThatTheSuccessorDeviceSavedTheNewRecovery(candLabel))
        }
        Alert(
            copy(UiMessages.securityTransferManagement()),
            body,
            copy(UiMessages.securityConfirmTransfer()),
            danger = true,
            onDismiss = onCancelTransfer,
        ) {
            onConfirmTransfer(targetId)
        }
    }
}

@Composable
fun DeleteMachineRecordsAction(id: String, canDelete: Boolean) {
    val copy = uiText()
    if (!canDelete) return
    val remote = LocalRemote.current
    val scope = rememberAuthorityScope()
    var confirm by remember { mutableStateOf(false) }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf("") }
    Group(
        rows =
            listOf(
                {
                    // It asks first, as every row that ends in an ellipsis does (design-system §8).
                    GRow(copy(UiMessages.securityDeleteThisComputerSRecords()), onClick = { confirm = true }, danger = true, enabled = !busy)
                },
            ),
    )
    if (error.isNotEmpty()) Notice(error, Modifier.padding(16.dp), NoticeTone.ERROR)
    if (confirm)
        Alert(
            copy(UiMessages.securityDeleteThisComputerSRecordsDeviceManagement()),
            copy(UiMessages.securityThisPermanentlyDeletesSessionsEventsAndImagesOnThe()),
            copy(UiMessages.securityDeleteRecords()),
            danger = true,
            onDismiss = { confirm = false },
        ) {
            confirm = false
            busy = true
            scope.launch {
                try {
                    remote.deleteMachineRecords(id)
                } catch (e: Exception) {
                    val advice = remote.coreHost.connectionView.value.takeIf { it.hasLeaseRenewalAdvice() }?.leaseRenewalAdvice
                    error = displayError(e, advice)
                } finally {
                    busy = false
                }
            }
        }
}
