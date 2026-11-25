@file:Suppress("DEPRECATION")

package io.github.lewin671.remoteagent.ui

import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.Crossfade
import androidx.compose.animation.core.tween
import androidx.compose.animation.expandVertically
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.shrinkVertically
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.clickable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import androidx.compose.runtime.DisposableEffect
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.ConnectionGroupStage
import io.github.lewin671.remoteagent.core.proto.ConnectionGroupTone
import io.github.lewin671.remoteagent.core.proto.ConnectionGroupView
import io.github.lewin671.remoteagent.core.proto.DiagCheckMark
import io.github.lewin671.remoteagent.core.proto.DirectDiagnosticsViewModel
import kotlinx.coroutines.delay
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * Unified direct-connection sheet/dialog rendered strictly from Core's [ConnectionGroupView] and
 * [DirectDiagnosticsViewModel] inside [DeviceScreen] (phone modal sheet / wide tablet centered dialog).
 */
private data class PlainDiagCheckRow(
    val mark: MarkKind,
    val label: String,
    val detail: String?,
    val status: String,
)

private enum class CopyFeedback { IDLE, OK, FAIL }

@Composable
internal fun DirectConnectionSheet(
    machineId: String,
    computerName: String = "",
    view: ConnectionGroupView,
    diagVm: DirectDiagnosticsViewModel,
    onToggle: (Boolean) -> Unit,
    onRetry: () -> Unit,
    onRefreshDiag: () -> Unit = {},
    onOpenDiag: () -> Unit = {},
    onCloseDiag: () -> Unit = {},
    onClose: () -> Unit,
) {
    val copy = uiText()
    val p = pal
    val supported = view.toggleVisible
    val statusText = uiText(view.statusText)
    val detailText = uiText(view.detailText)
    val metricText = uiText(view.metricText)
    val footerText = uiText(view.footerText).ifEmpty { copy(UiMessages.transportFooter()) }
    val hideStatus = view.stage == ConnectionGroupStage.CONNECTION_GROUP_STAGE_OFFLINE ||
        (view.stage == ConnectionGroupStage.CONNECTION_GROUP_STAGE_DISABLED && detailText.isEmpty())
    val showStatus = !hideStatus && (statusText.isNotEmpty() || detailText.isNotEmpty() || metricText.isNotEmpty())

    val enter = if (Motion.reduced) fadeIn(tween(150)) else expandVertically(tween(200)) + fadeIn(tween(200))
    val exit = if (Motion.reduced) fadeOut(tween(150)) else shrinkVertically(tween(200)) + fadeOut(tween(200))

    val clipboard = LocalClipboardManager.current
    var copyFeedback by remember { mutableStateOf(CopyFeedback.IDLE) }
    var cooldown by remember { mutableStateOf(false) }
    var prevRunning by remember { mutableStateOf(false) }

    val matchingDiag = diagVm.machineId.isEmpty() || diagVm.machineId == machineId
    val connected = (matchingDiag && diagVm.connected) ||
        view.stage == ConnectionGroupStage.CONNECTION_GROUP_STAGE_DIRECT
    val running = (matchingDiag && diagVm.trying) ||
        view.stage == ConnectionGroupStage.CONNECTION_GROUP_STAGE_CONNECTING ||
        view.stage == ConnectionGroupStage.CONNECTION_GROUP_STAGE_MEASURING
    val peerOutdated = !connected && matchingDiag && diagVm.peerOutdated
    var wasDiagShown by remember(machineId) { mutableStateOf(view.showDiagnosticsEntry) }
    LaunchedEffect(view.showDiagnosticsEntry, connected, view.toggleChecked) {
        if (connected || !view.toggleChecked) {
            wasDiagShown = false
        } else if (view.showDiagnosticsEntry) {
            wasDiagShown = true
        }
    }

    DisposableEffect(machineId) {
        onOpenDiag()
        onDispose { onCloseDiag() }
    }
    LaunchedEffect(machineId, running) {
        onRefreshDiag()
        if (running) {
            while (true) {
                delay(1000)
                onRefreshDiag()
            }
        }
    }
    LaunchedEffect(running, connected) {
        if (prevRunning && !running && !connected) {
            cooldown = true
            delay(2000)
            cooldown = false
        }
        prevRunning = running
    }
    LaunchedEffect(cooldown) {
        if (cooldown && !running) {
            delay(2000)
            cooldown = false
        }
    }
    LaunchedEffect(copyFeedback) {
        if (copyFeedback != CopyFeedback.IDLE) {
            delay(if (copyFeedback == CopyFeedback.FAIL) 2500L else 1500L)
            copyFeedback = CopyFeedback.IDLE
        }
    }

    val title = if (matchingDiag) {
        uiText(diagVm.titleText).ifEmpty { uiText(diagVm.verdictText) }
    } else ""
    val body = if (matchingDiag) {
        uiText(diagVm.bodyText)
    } else ""
    val checks = if (matchingDiag && diagVm.checksList.isNotEmpty()) {
        diagVm.checksList.map { c ->
            val mark = when (c.mark) {
                DiagCheckMark.DIAG_CHECK_MARK_ONLINE -> MarkKind.ONLINE
                DiagCheckMark.DIAG_CHECK_MARK_RUN -> MarkKind.RUNNING
                else -> MarkKind.OFF
            }
            PlainDiagCheckRow(
                mark = mark,
                label = uiText(c.labelText),
                detail = uiText(c.detailText).takeIf { it.isNotEmpty() },
                status = uiText(c.statusText),
            )
        }
    } else {
        emptyList()
    }

    val canRetryVm = !matchingDiag || diagVm.machineId.isEmpty() || (diagVm.supported && diagVm.canRetry)
    val retryDisabled = running || cooldown || connected || peerOutdated || !supported || !canRetryVm
    val diagLines = if (matchingDiag) diagVm.reportLinesList.map { uiText(it) } else emptyList()
    val reportPayload = remember(title, diagLines, computerName) {
        buildString {
            if (diagLines.any { it.isNotEmpty() }) {
                for (line in diagLines) appendLine(line)
            } else {
                appendLine(copy(UiMessages.transportDiagReportTitle()))
                if (title.isNotEmpty()) appendLine(copy(UiMessages.transportDiagReportVerdict(verdict = title)))
                appendLine("Client: Android ${android.os.Build.VERSION.RELEASE}")
                appendLine(copy(UiMessages.transportDiagVerdictNone()))
            }
            if (computerName.isNotEmpty()) appendLine("Computer: $computerName")
            appendLine("Time: ${SimpleDateFormat("yyyy-MM-dd HH:mm:ss", Locale.US).format(Date())}")
        }.trim()
    }

    AdaptiveSheet(onClose = onClose, scroll = true) {
        SheetHead(copy(UiMessages.transportDirect()), copy(UiMessages.commonDone()), onClose)
        SheetList {
            if (view.toggleVisible) {
                Box(Modifier.alpha(if (supported) 1f else 0.4f)) {
                    ToggleRow(
                        copy(UiMessages.transportRowAllowDirect()),
                        "",
                        value = supported && view.toggleChecked,
                        enabled = view.toggleEnabled,
                        onChange = onToggle,
                    )
                }
            }
            AnimatedVisibility(visible = showStatus, enter = enter, exit = exit) {
                Box(if (view.toggleVisible) Modifier.topLine(p.sep, 18.dp) else Modifier) {
                    DirectStatusRow(view.stage, view.tone, statusText, detailText, metricText)
                }
            }
        }
        if (view.showDiagnosticsEntry || (wasDiagShown && running && !connected)) {
            FieldLabel(copy(UiMessages.transportDiagSectionAuto()))
            if (body.isNotEmpty()) {
                Column(Modifier.semantics { liveRegion = LiveRegionMode.Polite }.padding(horizontal = 12.dp, vertical = 2.dp)) {
                    Text(body, style = T.s13, color = p.ink2)
                }
                Spacer(Modifier.height(6.dp))
            }
            if (checks.isNotEmpty()) {
                SheetList {
                    checks.forEachIndexed { idx, row ->
                        Row(
                            Modifier.fillMaxWidth().heightIn(min = 50.dp)
                                .then(if (idx > 0) Modifier.topLine(p.sep, 34.dp) else Modifier)
                                .padding(horizontal = 16.dp, vertical = 10.dp)
                                .semantics(mergeDescendants = true) {},
                            verticalAlignment = Alignment.CenterVertically,
                            horizontalArrangement = Arrangement.spacedBy(10.dp),
                        ) {
                            Box(Modifier.width(8.dp), contentAlignment = Alignment.Center) {
                                StatusMark(row.mark, color = if (row.mark == MarkKind.ONLINE) p.green else if (row.mark == MarkKind.RUNNING) p.ink else p.faint)
                            }
                            Column(Modifier.weight(1f)) {
                                Text(row.label, style = T.s15, color = p.ink)
                                if (!row.detail.isNullOrEmpty()) Text(row.detail, style = T.s13, color = p.ink2)
                            }
                            Text(row.status, style = T.s13, color = p.ink2)
                        }
                    }
                }
                Spacer(Modifier.height(14.dp))
            }
            Row(
                Modifier.fillMaxWidth().heightIn(min = 48.dp)
                    .disabledAlpha(!retryDisabled)
                    .clip(Radii.Pill)
                    .background(p.btn)
                    .clickable(enabled = !retryDisabled, role = Role.Button) {
                        cooldown = true
                        onRetry()
                        onRefreshDiag()
                    }
                    .padding(horizontal = 22.dp, vertical = 6.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(8.dp, Alignment.CenterHorizontally),
            ) {
                if (running) Spinner(p.btnFg, size = 16.dp)
                Text(
                    if (running) copy(UiMessages.transportDiagRetrying()) else copy(UiMessages.transportRowRetryDirect()),
                    style = T.s17b,
                    color = p.btnFg,
                    textAlign = TextAlign.Center,
                    maxLines = 2,
                )
            }
            Box(
                Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable(role = Role.Button) {
                    runCatching { clipboard.setText(AnnotatedString(reportPayload)) }
                        .onSuccess { copyFeedback = CopyFeedback.OK }
                        .onFailure { copyFeedback = CopyFeedback.FAIL }
                },
                contentAlignment = Alignment.Center,
            ) {
                Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                    when (copyFeedback) {
                        CopyFeedback.OK -> {
                            Ic(Lucide.Check, 15.dp, p.ink2)
                            Text(copy(UiMessages.transportRowDiagCopied()), style = T.s15, color = p.ink2)
                        }
                        CopyFeedback.FAIL -> {
                            Text(copy(UiMessages.transportDiagCopyPermission()), style = T.s15, color = p.red)
                        }
                        CopyFeedback.IDLE -> {
                            Ic(Lucide.Copy, 15.dp, p.blue)
                            Text(copy(UiMessages.transportRowDiagCopySupport()), style = T.s15b, color = p.blue)
                        }
                    }
                }
            }
            Hint(copy(UiMessages.transportDiagPrivacyFooter()))
        } else {
            Hint(footerText)
        }
    }
}

@Composable
private fun DirectStatusRow(
    stage: ConnectionGroupStage,
    tone: ConnectionGroupTone,
    word: String,
    detail: String,
    metric: String,
) {
    val p = pal
    val mark = when (stage) {
        ConnectionGroupStage.CONNECTION_GROUP_STAGE_DIRECT -> MarkKind.ONLINE
        ConnectionGroupStage.CONNECTION_GROUP_STAGE_CONNECTING,
        ConnectionGroupStage.CONNECTION_GROUP_STAGE_MEASURING -> MarkKind.RUNNING
        else -> MarkKind.OFF
    }
    val wordColor = when (tone) {
        ConnectionGroupTone.CONNECTION_GROUP_TONE_WARN -> p.orangeInk
        ConnectionGroupTone.CONNECTION_GROUP_TONE_LIVE -> p.ink
        else -> p.ink2
    }
    Column(
        Modifier.fillMaxWidth().heightIn(min = 50.dp).padding(start = 18.dp, end = 16.dp, top = 11.dp, bottom = 11.dp),
        verticalArrangement = Arrangement.Center,
    ) {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(10.dp)) {
            StatusMark(mark)
            Crossfade(targetState = stage to word, animationSpec = tween(200), modifier = Modifier.weight(1f)) { (_, w) ->
                Text(w, style = T.s17, color = wordColor)
            }
            if (metric.isNotEmpty()) {
                Text(metric, style = T.s13.digits(), color = p.ink2)
            }
        }
        if (detail.isNotEmpty()) {
            Text(detail, style = T.s13, color = p.ink2, modifier = Modifier.padding(start = 19.dp, top = 3.dp))
        }
    }
}
