package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages

import androidx.compose.runtime.*
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.ui.displayError
import kotlinx.coroutines.launch

internal enum class ExitRisk { MEMBER, AUTHORITY, PENDING }

internal fun authorityExitRisk(authority: Boolean, unresolved: Boolean): ExitRisk = when {
    unresolved -> ExitRisk.PENDING
    authority -> ExitRisk.AUTHORITY
    else -> ExitRisk.MEMBER
}

internal fun authorityExitAdvice(risk: ExitRisk, copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText): String = when (risk) {
    ExitRisk.PENDING -> copy(UiMessages.securityTheSecurityOperationSOutcomeIsUnverifiedContinueThe())
    ExitRisk.AUTHORITY -> copy(UiMessages.securityAndroidThisIsTheOnlyManagingClientTransferManagementFirstExiting())
    ExitRisk.MEMBER -> copy(UiMessages.securityExitingDeletesLocalKeysYouWillNeedToPair())
}

internal fun authorityExitAdvice(authority: Boolean, unresolved: Boolean, copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText): String =
    authorityExitAdvice(authorityExitRisk(authority, unresolved), copy)

/** A failed read must never turn an unresolved operation into an ordinary logout. */
@Composable
internal fun AccountExitAction(
    state: RemoteState,
    readPending: suspend () -> Boolean,
    exit: suspend () -> Unit,
) {
    val copy = uiText()
    val scope = rememberCoroutineScope()
    var advice by remember { mutableStateOf<ExitRisk?>(null) }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    // It asks first, as every row that ends in an ellipsis does (design-system §8).
    GRow(
        copy(UiMessages.settingsExitAndDeleteLocalKeys()),
        onClick = {
            busy = true
            error = ""
            scope.launch {
                try {
                    advice = authorityExitRisk(state.isAuthority, readPending())
                } catch (e: Exception) {
                    error = copy(UiMessages.securityAndroidCouldNotReadSecurityOperationStateBeforeExitingValue1((displayError(e, state)).toString()))
                } finally {
                    busy = false
                }
            }
        },
        danger = true,
        enabled = !busy,
    )
    if (error.isNotEmpty()) Notice(error, tone = NoticeTone.ERROR)
    advice?.let { risk ->
        Confirm(
            copy(UiMessages.securityExitAndDeleteLocalKeys()),
            authorityExitAdvice(risk, copy),
            copy(UiMessages.securityExit()),
            danger = true,
            onDismiss = { if (!busy) advice = null },
        ) {
            if (!busy) {
                busy = true
                scope.launch {
                    try {
                        // A publication may have started while the confirmation was open.
                        val current = authorityExitRisk(state.isAuthority, readPending())
                        if (current != risk) advice = current
                        else {
                            exit()
                            advice = null
                        }
                    } catch (e: Exception) {
                        advice = null
                        error = copy(UiMessages.securityAndroidExitIncompleteValue1((displayError(e, state)).toString()))
                    } finally {
                        busy = false
                    }
                }
            }
        }
    }
}
