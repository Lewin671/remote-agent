package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.CoreHost
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.BootstrapErrorView
import io.github.lewin671.remoteagent.core.proto.ExitConfirmStep
import io.github.lewin671.remoteagent.core.proto.ExitConfirmView
import io.github.lewin671.remoteagent.core.proto.ExitIntentKind

@Composable
fun ExitConfirmDialog(
    view: ExitConfirmView,
    onConfirm: (Boolean) -> Unit,
) {
    val copy = uiText()
    if (view.step == ExitConfirmStep.EXIT_CONFIRM_STEP_FAILED) {
        val err = uiText(view.errorText)
        if (err.isNotEmpty()) {
            Notice(err, Modifier.padding(top = 8.dp), tone = NoticeTone.ERROR)
        }
        return
    }
    if (view.step != ExitConfirmStep.EXIT_CONFIRM_STEP_AWAITING_CONFIRM) return
    val title = when (view.intent) {
        ExitIntentKind.EXIT_INTENT_KIND_REJOIN_PAIRING -> copy(UiMessages.securityDeleteLocalKeysAndRejoin())
        else -> copy(UiMessages.securityExitAndDeleteLocalKeys())
    }
    val baseBody = uiText(view.warningBody).ifEmpty {
        copy(UiMessages.securityTheLocalAccountUnfinishedRegistrationAndSecurityOperationsWill())
    }
    val body = if (view.showRejoinDeletesCacheNote) {
        baseBody + "\n\n" + copy(UiMessages.securityThisDeletesTheAccountKeysAndCacheOnThis())
    } else {
        baseBody
    }
    val ok = when {
        view.busy -> copy(UiMessages.securityExiting())
        view.intent == ExitIntentKind.EXIT_INTENT_KIND_REJOIN_PAIRING -> copy(UiMessages.securityRejoin())
        view.intent == ExitIntentKind.EXIT_INTENT_KIND_LEAVE_ACCOUNT -> copy(UiMessages.securityExit())
        else -> copy(UiMessages.securityConfirmExit())
    }
    Confirm(
        title,
        body,
        ok,
        danger = true,
        onDismiss = { if (!view.busy) onConfirm(false) },
    ) {
        if (!view.busy) onConfirm(true)
    }
}

@Composable
fun ExitConfirmDialog(coreHost: CoreHost) {
    val view by coreHost.exitConfirmView.collectAsState()
    ExitConfirmDialog(view = view, onConfirm = coreHost::exitConfirm)
}

@Composable
fun AccountLoadFailure(
    error: BootstrapErrorView,
    coreHost: CoreHost,
) {
    val copy = uiText()
    val exitView by coreHost.exitConfirmView.collectAsState()
    AccountLoadFailureContent(
        message = uiText(error.message).ifEmpty { copy(UiMessages.errorsDisconnectedFromServer()) },
        canRetry = error.canRetry,
        canReset = error.canReset,
        exitView = exitView,
        onRetry = coreHost::appRootRetryBootstrap,
        onRequestReset = { coreHost.exitRequest(ExitIntentKind.EXIT_INTENT_KIND_SETUP_RESET) },
        onConfirmExit = coreHost::exitConfirm,
    )
}

@Composable
fun AccountLoadFailureContent(
    message: String,
    canRetry: Boolean = true,
    canReset: Boolean = true,
    exitView: ExitConfirmView = ExitConfirmView.getDefaultInstance(),
    onRetry: () -> Unit = {},
    onRequestReset: () -> Unit = {},
    onConfirmExit: (Boolean) -> Unit = {},
) {
    val copy = uiText()
    Column(Modifier.padding(24.dp)) {
        Text(message, style = T.s17, color = pal.ink)
        if (canRetry) {
            PillButton(
                copy(UiMessages.commonRetry()),
                onRetry,
                Modifier.fillMaxWidth().padding(top = 16.dp),
                ButtonKind.INK,
            )
        }
        if (canReset) {
            TextLink(
                copy(UiMessages.securityExitAndStartOver()),
                onRequestReset,
                Modifier.fillMaxWidth(),
                danger = true,
            )
        }
        ExitConfirmDialog(view = exitView, onConfirm = onConfirmExit)
    }
}

@Composable
fun DiscardSetup(coreHost: CoreHost) {
    val copy = uiText()
    val exitView by coreHost.exitConfirmView.collectAsState()
    TextLink(
        copy(UiMessages.securityExitAndStartOver()),
        { coreHost.exitRequest(ExitIntentKind.EXIT_INTENT_KIND_SETUP_RESET) },
        Modifier.fillMaxWidth(),
        danger = true,
    )
    ExitConfirmDialog(view = exitView, onConfirm = coreHost::exitConfirm)
}
