package io.github.lewin671.remoteagent.ui

import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import io.github.lewin671.remoteagent.core.CoreHost
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.ExitConfirmStep
import io.github.lewin671.remoteagent.core.proto.ExitConfirmView
import io.github.lewin671.remoteagent.core.proto.ExitIntentKind

/**
 * Core drives exit risk evaluation, two-phase re-check on confirm, push withdrawal,
 * key erasure, and cache cleanup through [ExitConfirmView].
 */
@Composable
internal fun AccountExitAction(coreHost: CoreHost) {
    val exitView by coreHost.exitConfirmView.collectAsState()
    AccountExitActionContent(
        exitView = exitView,
        onRequestExit = { coreHost.exitRequest(ExitIntentKind.EXIT_INTENT_KIND_LEAVE_ACCOUNT) },
        onConfirmExit = coreHost::exitConfirm,
    )
}

@Composable
internal fun AccountExitActionContent(
    exitView: ExitConfirmView,
    onRequestExit: () -> Unit,
    onConfirmExit: (Boolean) -> Unit,
) {
    val copy = uiText()
    val busy = exitView.busy ||
        exitView.step == ExitConfirmStep.EXIT_CONFIRM_STEP_CHECKING ||
        exitView.step == ExitConfirmStep.EXIT_CONFIRM_STEP_EXECUTING
    // It asks first, as every row that ends in an ellipsis does (design-system §8).
    GRow(
        copy(UiMessages.settingsExitAndDeleteLocalKeys()),
        onClick = onRequestExit,
        danger = true,
        enabled = !busy,
    )
    ExitConfirmDialog(view = exitView, onConfirm = onConfirmExit)
}
