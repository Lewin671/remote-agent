package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages

import androidx.compose.foundation.layout.Column
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.*
import io.github.lewin671.remoteagent.RaApp
import kotlinx.coroutines.launch
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.fillMaxWidth

@Composable
fun AccountLoadFailure(message: String, retry: () -> Unit, discard: () -> Unit) {
    val copy = uiText()
    Column(Modifier.padding(24.dp)) {
        Text(message, style = T.s17, color = pal.ink)
        PillButton(copy(UiMessages.commonRetry()), retry, Modifier.fillMaxWidth().padding(top = 16.dp), ButtonKind.INK)
        DiscardSetupAction(discard)
    }
}

@Composable
fun DiscardSetupAction(discard: () -> Unit) {
    val copy = uiText()
    var asking by remember { mutableStateOf(false) }
    TextLink(copy(UiMessages.securityExitAndStartOver()), { asking = true }, Modifier.fillMaxWidth(), danger = true)
    if (asking)
        Confirm(
            copy(UiMessages.securityExitAndDeleteLocalKeys()),
            copy(UiMessages.securityTheLocalAccountUnfinishedRegistrationAndSecurityOperationsWill()),
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
    DiscardSetupAction {
        scope.launch {
            app.logout()
            onDiscarded()
        }
    }
}
