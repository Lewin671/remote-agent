package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages

import androidx.compose.runtime.*
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.ui.displayError
import kotlinx.coroutines.launch

internal fun authorityExitAdvice(authority: Boolean, unresolved: Boolean, copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText): String =
    when {
        unresolved -> copy(UiMessages.securityTheSecurityOperationSOutcomeIsUnverifiedContinueThe())
        authority -> copy(UiMessages.securityAndroidThisIsTheOnlyManagingClientTransferManagementFirstExiting())
        else -> copy(UiMessages.securityExitingDeletesLocalKeysYouWillNeedToPair())
    }

/** A failed read must never turn an unresolved operation into an ordinary logout. */
@Composable
internal fun AccountExitAction(
    state: RemoteState,
    readPending: suspend () -> Boolean,
    exit: suspend () -> Unit,
) {
    val copy = uiText()
    val scope = rememberCoroutineScope()
    var advice by remember { mutableStateOf<String?>(null) }
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
                    advice = authorityExitAdvice(state.isAuthority, readPending(), copy = copy)
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
    advice?.let { message ->
        Confirm(
            copy(UiMessages.securityExitAndDeleteLocalKeys()),
            message,
            copy(UiMessages.securityExit()),
            danger = true,
            onDismiss = { if (!busy) advice = null },
        ) {
            if (!busy) {
                busy = true
                scope.launch {
                    try {
                        // A publication may have started while the confirmation was open.
                        val current = authorityExitAdvice(state.isAuthority, readPending(), copy = copy)
                        if (current != message) advice = current
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
