package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.SecurityExitAdvice
import io.github.lewin671.remoteagent.core.renderExitAdviceMessage

import androidx.compose.foundation.layout.Column
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import io.github.lewin671.remoteagent.RaApp
import kotlinx.coroutines.launch
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.fillMaxWidth

@Composable
fun AccountLoadFailure(
    message: String,
    retry: () -> Unit,
    discard: () -> Unit,
    canDiscard: Boolean = true,
    checkAdvice: (suspend () -> SecurityExitAdvice?)? = null,
) {
    val copy = uiText()
    Column(Modifier.padding(24.dp)) {
        Text(message, style = T.s17, color = pal.ink)
        PillButton(copy(UiMessages.commonRetry()), retry, Modifier.fillMaxWidth().padding(top = 16.dp), ButtonKind.INK)
        if (canDiscard) DiscardSetupAction(discard, checkAdvice)
    }
}

@Composable
fun DiscardSetupAction(
    discard: () -> Unit,
    checkAdvice: (suspend () -> SecurityExitAdvice?)? = null,
) {
    val copy = uiText()
    val scope = rememberCoroutineScope()
    var asking by remember { mutableStateOf(false) }
    var adviceText by remember { mutableStateOf("") }
    TextLink(
        copy(UiMessages.securityExitAndStartOver()),
        {
            if (checkAdvice == null) {
                adviceText = copy(UiMessages.securityTheLocalAccountUnfinishedRegistrationAndSecurityOperationsWill())
                asking = true
            } else {
                scope.launch {
                    val adv = runCatching { checkAdvice() }.getOrNull()
                    val resolved = adv?.takeIf { it.authority || it.unresolved }
                        ?.let { renderExitAdviceMessage(it, copy) }
                        ?.takeIf { it.isNotEmpty() }
                    adviceText = resolved
                        ?: copy(UiMessages.securityTheLocalAccountUnfinishedRegistrationAndSecurityOperationsWill())
                    asking = true
                }
            }
        },
        Modifier.fillMaxWidth(),
        danger = true,
    )
    if (asking)
        Confirm(
            copy(UiMessages.securityExitAndDeleteLocalKeys()),
            adviceText.ifEmpty { copy(UiMessages.securityTheLocalAccountUnfinishedRegistrationAndSecurityOperationsWill()) },
            copy(UiMessages.securityConfirmExit()),
            danger = true,
            onDismiss = { asking = false },
        ) {
            asking = false
            discard()
        }
}

@Composable
fun DiscardSetup(app: RaApp, onDiscarded: () -> Unit = {}) {
    val scope = rememberCoroutineScope()
    DiscardSetupAction(
        discard = {
            scope.launch {
                app.logout()
                onDiscarded()
            }
        },
        checkAdvice = { runCatching { app.checkExitAdvice() }.getOrNull() },
    )
}
