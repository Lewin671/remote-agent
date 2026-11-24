package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages

import androidx.compose.runtime.*
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.proto.SecurityExitAdvice
import io.github.lewin671.remoteagent.core.renderExitAdviceMessage
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

/** Core computes exit advice; a failed check must never downgrade an unresolved operation to ordinary logout. */
@Composable
internal fun AccountExitAction(
    state: RemoteState,
    readAdvice: suspend () -> SecurityExitAdvice,
    exit: suspend () -> Unit,
) {
    val copy = uiText()
    val scope = rememberCoroutineScope()
    var checked by remember { mutableStateOf<Pair<ExitRisk, String>?>(null) }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }

    fun toPair(a: SecurityExitAdvice): Pair<ExitRisk, String> {
        val risk = authorityExitRisk(a.authority, a.unresolved)
        val msg = renderExitAdviceMessage(a, copy).ifEmpty { authorityExitAdvice(risk, copy) }
        return risk to msg
    }

    // It asks first, as every row that ends in an ellipsis does (design-system §8).
    GRow(
        copy(UiMessages.settingsExitAndDeleteLocalKeys()),
        onClick = {
            busy = true
            error = ""
            scope.launch {
                try {
                    checked = toPair(readAdvice())
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
    checked?.let { (risk, message) ->
        Confirm(
            copy(UiMessages.securityExitAndDeleteLocalKeys()),
            message,
            copy(UiMessages.securityExit()),
            danger = true,
            onDismiss = { if (!busy) checked = null },
        ) {
            if (!busy) {
                busy = true
                scope.launch {
                    try {
                        val current = toPair(readAdvice())
                        if (current.first != risk) checked = current
                        else {
                            exit()
                            checked = null
                        }
                    } catch (e: Exception) {
                        checked = null
                        error = copy(UiMessages.securityAndroidExitIncompleteValue1((displayError(e, state)).toString()))
                    } finally {
                        busy = false
                    }
                }
            }
        }
    }
}
