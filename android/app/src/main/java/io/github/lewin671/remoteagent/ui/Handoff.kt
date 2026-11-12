package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.clientName
import io.github.lewin671.remoteagent.ui.displayError
import io.github.lewin671.remoteagent.proto.DeviceKind
import kotlinx.coroutines.launch

/** The successor client only offers its public recovery key after explicit backup confirmation. */
@Composable
fun HandoffPreparation(onBack: (() -> Unit)? = null) {
    val remote = LocalRemote.current
    val state by remote.state.collectAsState()
    HandoffPreparationContent(state, remote.account.deviceId, remote::prepareHandoffCode, onBack = onBack, publishOffer = remote::offerHandoff)
}

@Composable
internal fun HandoffPreparationContent(
    state: RemoteState,
    self: String,
    prepareCode: () -> Pair<String, ByteArray>,
    onBack: (() -> Unit)? = null,
    publishOffer: suspend (ByteArray) -> Unit,
) {
    val copy = uiText()
    val scope = rememberAuthorityScope()
    var prepared by remember { mutableStateOf<Pair<String, ByteArray>?>(null) }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    PageScaffold(copy(UiMessages.securityTakeOverManagement()), onBack = onBack) {
        FormColumn(Modifier.fillMaxWidth().verticalScrollFaded().padding(vertical = 16.dp)) {
            Text(
                copy(UiMessages.securitySaveTheNewRecoveryCodeFirstThenAskThe()),
                style = T.s13,
                color = pal.ink2,
            )
            val value = prepared
            if (value != null) {
                RecoveryCodeFigure(value.first)
                PillButton(
                    copy(UiMessages.securitySavedNotifyManagingClient()),
                    {
                        busy = true
                        error = ""
                        scope.launch {
                            try {
                                publishOffer(value.second)
                                prepared = null
                            } catch (e: Exception) {
                                error = displayError(e, state)
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
                val offered =
                    state.devices[self]?.meta?.handoffRecoveryEd?.size() == 32
                if (offered)
                    Hint(copy(UiMessages.securityRecoveryCodeSavedAndConfirmedWaitingForTheCurrent()), Modifier.padding(vertical = 12.dp))
                PillButton(
                    if (offered) copy(UiMessages.securityRegenerateRecoveryCode()) else copy(UiMessages.securityGenerateNewRecoveryCode()),
                    {
                        error = ""
                        try {
                            prepared = prepareCode()
                        } catch (e: Exception) {
                            error = displayError(e, state)
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
    val copy = uiText()
    val remote = LocalRemote.current
    val state by remote.state.collectAsState()
    val scope = rememberAuthorityScope()
    var target by remember { mutableStateOf<String?>(null) }
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf("") }
    val candidates =
        state.devices.values.filter {
            it.id != remote.account.deviceId &&
                it.kind == DeviceKind.DEVICE_KIND_CLIENT &&
                it.meta?.handoffRecoveryEd?.size() == 32
        }
    GLabel(copy(UiMessages.securityTransferManagement()))
    Hint(copy(UiMessages.securityOnceTheSuccessorDeviceJoinsAndConfirmsSavingIts()), Modifier)
    for (d in candidates) PillButton(
        copy(UiMessages.securityAndroidTransferTo(clientName(d, d.id, state.devices.values, copy = copy))),
        { target = d.id },
        Modifier.fillMaxWidth().padding(top = 8.dp),
        ButtonKind.SECONDARY,
        enabled = !busy,
    )
    if (candidates.isEmpty()) Hint(copy(UiMessages.securityWaitingForTheSuccessorDeviceToGenerateAndSave()), Modifier.padding(vertical = 8.dp))
    if (error.isNotEmpty()) Notice(error, Modifier.padding(vertical = 8.dp), NoticeTone.ERROR)
    target?.let { id ->
        Alert(
            copy(UiMessages.securityTransferManagement()),
            copy(UiMessages.securityConfirmThatTheSuccessorDeviceSavedTheNewRecovery((clientName(state.devices[id], id, state.devices.values, copy = copy)).toString())),
            copy(UiMessages.securityConfirmTransfer()),
            danger = true,
            onDismiss = { target = null },
        ) {
            target = null
            busy = true
            scope.launch {
                try {
                    remote.handoffAuthority(id)
                } catch (e: Exception) {
                    error = displayError(e, remote.state.value)
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
    val state by remote.state.collectAsState()
    if (id in state.devices) return
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
                    error = displayError(e, remote.state.value)
                } finally {
                    busy = false
                }
            }
        }
}
