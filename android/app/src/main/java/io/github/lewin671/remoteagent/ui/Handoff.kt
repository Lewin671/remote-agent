package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.HandoffCandidateView
import io.github.lewin671.remoteagent.core.proto.SettingsViewModel
import io.github.lewin671.remoteagent.core.proto.SyncStateView
import kotlinx.coroutines.launch

/** The successor client only offers its public recovery key after explicit backup confirmation. */
@Composable
fun HandoffPreparation(onBack: (() -> Unit)? = null) {
    val remote = LocalRemote.current
    val syncView by remote.coreHost.syncStateView.collectAsState()
    val settingsView by remote.settingsView.collectAsState()
    HandoffPreparationContent(
        syncView = syncView,
        hasHandoffOffer = settingsView.security.hasHandoffOffer,
        prepareCode = { remote.prepareHandoffCode(storagePersisted = true) },
        onBack = onBack,
        publishOffer = remote::publishHandoffOffer,
    )
}

@Composable
internal fun HandoffPreparationContent(
    syncView: SyncStateView = SyncStateView.getDefaultInstance(),
    hasHandoffOffer: Boolean = false,
    prepareCode: suspend () -> String,
    onBack: (() -> Unit)? = null,
    publishOffer: suspend () -> Unit,
) {
    val copy = uiText()
    val scope = rememberAuthorityScope()
    var prepared by remember { mutableStateOf<String?>(null) }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    PageScaffold(copy(UiMessages.securityTakeOverManagement()), onBack = onBack) {
        FormColumn(Modifier.fillMaxWidth().verticalScrollFaded().padding(vertical = 16.dp)) {
            Text(
                copy(UiMessages.securitySaveTheNewRecoveryCodeFirstThenAskThe()),
                style = T.s13,
                color = pal.ink2,
            )
            val codeValue = prepared
            if (codeValue != null) {
                RecoveryCodeFigure(codeValue)
                PillButton(
                    copy(UiMessages.securitySavedNotifyManagingClient()),
                    {
                        busy = true
                        error = ""
                        scope.launch {
                            try {
                                publishOffer()
                                prepared = null
                            } catch (e: Exception) {
                                error = displayError(e, syncView)
                            } finally {
                                busy = false
                            }
                        }
                    },
                    Modifier.fillMaxWidth().padding(top = 12.dp),
                    ButtonKind.INK,
                    enabled = !busy,
                )
            } else {
                if (hasHandoffOffer)
                    Hint(copy(UiMessages.securityRecoveryCodeSavedAndConfirmedWaitingForTheCurrent()), Modifier.padding(vertical = 12.dp))
                PillButton(
                    if (hasHandoffOffer) copy(UiMessages.securityRegenerateRecoveryCode()) else copy(UiMessages.securityGenerateNewRecoveryCode()),
                    {
                        busy = true
                        error = ""
                        scope.launch {
                            try {
                                prepared = prepareCode()
                            } catch (e: Exception) {
                                error = displayError(e, syncView)
                            } finally {
                                busy = false
                            }
                        }
                    },
                    Modifier.fillMaxWidth().padding(top = 12.dp),
                    ButtonKind.SECONDARY,
                    enabled = !busy,
                )
            }
            if (error.isNotEmpty()) Notice(error, Modifier.padding(top = 12.dp), NoticeTone.ERROR)
        }
    }
}

/** The current authority authenticates the successor client through ordinary pairing first. */
@Composable
fun HandoffControls() {
    val remote = LocalRemote.current
    val initialVm = remember(remote) { remote.coreHost.snapshotSettingsView() }
    val rawVm by remote.settingsView.collectAsState()
    val vm = if (rawVm != SettingsViewModel.getDefaultInstance()) rawVm else initialVm
    HandoffControlsContent(
        candidates = vm.security.handoffCandidatesList,
        onTransfer = { id -> remote.handoffAuthority(id) },
        syncViewProvider = { remote.coreHost.syncStateView.value },
    )
}

@Composable
internal fun HandoffControlsContent(
    candidates: List<HandoffCandidateView>,
    onTransfer: suspend (String) -> Unit,
    syncViewProvider: () -> SyncStateView = { SyncStateView.getDefaultInstance() },
) {
    val copy = uiText()
    val scope = rememberAuthorityScope()
    var target by remember { mutableStateOf<HandoffCandidateView?>(null) }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf("") }
    fun candidateLabel(c: HandoffCandidateView): String =
        if (c.hasDisplayNameText()) io.github.lewin671.remoteagent.core.coreTextToMessageText(c.displayNameText).resolve(copy)
        else c.displayName
    GLabel(copy(UiMessages.securityTransferManagement()))
    Hint(copy(UiMessages.securityOnceTheSuccessorDeviceJoinsAndConfirmsSavingIts()), Modifier)
    for (c in candidates) PillButton(
        copy(UiMessages.securityAndroidTransferTo(candidateLabel(c))),
        { target = c },
        Modifier.fillMaxWidth().padding(top = 8.dp),
        ButtonKind.SECONDARY,
        enabled = !busy,
    )
    if (candidates.isEmpty()) Hint(copy(UiMessages.securityWaitingForTheSuccessorDeviceToGenerateAndSave()), Modifier.padding(vertical = 8.dp))
    if (error.isNotEmpty()) Notice(error, Modifier.padding(vertical = 8.dp), NoticeTone.ERROR)
    target?.let { cand ->
        Alert(
            copy(UiMessages.securityTransferManagement()),
            copy(UiMessages.securityConfirmThatTheSuccessorDeviceSavedTheNewRecovery(candidateLabel(cand))),
            copy(UiMessages.securityConfirmTransfer()),
            danger = true,
            onDismiss = { target = null },
        ) {
            target = null
            busy = true
            scope.launch {
                try {
                    onTransfer(cand.deviceId)
                } catch (e: Exception) {
                    error = displayError(e, syncViewProvider())
                } finally {
                    busy = false
                }
            }
        }
    }
}

@Composable
fun DeleteMachineRecordsAction(id: String, authority: Boolean) {
    val copy = uiText()
    if (!authority) return
    val remote = LocalRemote.current
    val syncView by remote.coreHost.syncStateView.collectAsState()
    if (syncView.devicesList.any { it.deviceId == id }) return
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
                    error = displayError(e, remote.coreHost.syncStateView.value)
                } finally {
                    busy = false
                }
            }
        }
}
