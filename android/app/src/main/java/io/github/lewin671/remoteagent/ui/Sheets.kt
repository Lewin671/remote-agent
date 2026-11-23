package io.github.lewin671.remoteagent.ui

import android.content.ClipData
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.layout.wrapContentWidth
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.platform.ClipEntry
import androidx.compose.ui.platform.LocalClipboard
import androidx.compose.ui.platform.LocalWindowInfo
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.TextFieldValue
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.SessionCommandStatus
import io.github.lewin671.remoteagent.core.proto.SessionDockKind
import io.github.lewin671.remoteagent.core.proto.SessionModelChoiceView
import io.github.lewin671.remoteagent.core.proto.SessionViewModel
import io.github.lewin671.remoteagent.proto.ApprovalMode
import io.github.lewin671.remoteagent.proto.Command
import io.github.lewin671.remoteagent.proto.CompactRequest
import io.github.lewin671.remoteagent.proto.RenameSessionRequest
import io.github.lewin671.remoteagent.proto.SetApprovalModeRequest
import io.github.lewin671.remoteagent.proto.SetModelRequest

/** Approval mode picker rendered from [SessionViewModel.actions]. */
@Composable
fun ModeSheet(sid: String, vm: SessionViewModel, onClose: () -> Unit) {
    val copy = uiText()
    val p = pal
    val remote = LocalRemote.current
    var confirmFull by remember { mutableStateOf(false) }
    var submitted by remember(sid) { mutableStateOf(false) }
    val slot = vm.actions.setMode
    val busy = slot.busy || slot.status == SessionCommandStatus.SESSION_COMMAND_STATUS_PENDING
    LaunchedEffect(submitted, busy, slot.status) {
        if (submitted && !busy && slot.status == SessionCommandStatus.SESSION_COMMAND_STATUS_IDLE) onClose()
    }
    fun set(mode: ApprovalMode) {
        submitted = true
        remote.submitSessionCommand(
            sid,
            Command.newBuilder().setSetApprovalMode(SetApprovalModeRequest.newBuilder().setSessionId(sid).setMode(mode)).build(),
            machineId = vm.header.machineId,
        )
    }
    val raw = vm.header.approvalModeRaw
    Sheet(onClose) {
        SheetHead(copy(UiMessages.modesTitle()))
        if (vm.header.terminalModeOverride && raw.isNotEmpty()) {
            Text(
                copy(UiMessages.modesCurrentTerminal(mode = if (raw == "acceptEdits") "accept edits" else raw)),
                style = T.s12,
                color = p.ink2,
                modifier = Modifier.padding(bottom = 4.dp),
            )
        }
        val choices = vm.actions.modeChoicesList
        SheetList {
            choices.forEachIndexed { i, c ->
                val reason = if (!c.available && c.hasUnavailableReason()) uiText(c.unavailableReason) else null
                ListItem(
                    uiText(c.label),
                    reason ?: uiText(c.note),
                    checked = c.selected,
                    enabled = c.available && !busy,
                    danger = c.mode == ApprovalMode.APPROVAL_MODE_FULL_ACCESS && c.available,
                    last = i == choices.size - 1,
                    onClick = {
                        if (c.mode == ApprovalMode.APPROVAL_MODE_FULL_ACCESS && !c.selected) confirmFull = true
                        else set(c.mode)
                    },
                )
            }
        }
        val notice = when {
            vm.actions.hasModeSwitchBlockedReason() -> uiText(vm.actions.modeSwitchBlockedReason)
            vm.header.terminalModeOverride && raw.isNotEmpty() ->
                copy(UiMessages.modesAndroidPermissionNotice(mode = if (raw == "acceptEdits") "accept edits" else raw))
            else -> copy(UiMessages.sessionOnlyCurrent())
        }
        Hint(notice)
        if (busy) Hint(copy(UiMessages.modesSwitching()))
        if (slot.hasErrorText()) Hint(copy(UiMessages.modesSwitchFailed(reason = uiText(slot.errorText))), color = p.red)
    }
    if (confirmFull) {
        Confirm(
            copy(UiMessages.modesFullTitle()),
            copy(UiMessages.modesFullCurrentNote()),
            copy(UiMessages.modesSwitch()),
            danger = true,
            onDismiss = { confirmFull = false },
        ) {
            confirmFull = false
            set(ApprovalMode.APPROVAL_MODE_FULL_ACCESS)
        }
    }
}

private fun sanitizeTitle(text: String): String =
    text.replace('\r', ' ').replace('\n', ' ').trimStart()

@Composable
fun RenameDialog(sid: String, vm: SessionViewModel, onClose: () -> Unit) {
    val copy = uiText()
    val p = pal
    val remote = LocalRemote.current
    val titleText = uiText(vm.header.title)
    val initial = remember(sid) { titleText.trim().take(80) }
    var value by remember { mutableStateOf(TextFieldValue(initial, TextRange(0, initial.length))) }
    var submitted by remember(sid) { mutableStateOf(false) }
    val slot = vm.actions.rename
    val busy = slot.busy || slot.status == SessionCommandStatus.SESSION_COMMAND_STATUS_PENDING
    LaunchedEffect(submitted, busy, slot.status) {
        if (submitted && !busy && slot.status == SessionCommandStatus.SESSION_COMMAND_STATUS_IDLE) onClose()
    }
    val focus = remember { FocusRequester() }
    fun save() {
        if (busy) return
        val next = value.text.trim()
        if (next == initial) { onClose(); return }
        submitted = true
        remote.submitSessionCommand(
            sid,
            Command.newBuilder().setRenameSession(RenameSessionRequest.newBuilder().setSessionId(sid).setTitle(next)).build(),
            machineId = vm.header.machineId,
        )
    }
    Dialog(
        onDismissRequest = { if (!busy) onClose() },
        properties = DialogProperties(usePlatformDefaultWidth = false),
    ) {
        val window = LocalWindowInfo.current
        LaunchedEffect(window) {
            snapshotFlow { window.isWindowFocused }.collect { if (it) focus.requestFocus() }
        }
        Box(Modifier.fillMaxWidth().padding(horizontal = 30.dp).imePadding(), contentAlignment = Alignment.Center) {
            Column(
                Modifier.widthIn(max = 320.dp).fillMaxWidth().clip(Radii.R20).background(p.paper)
                    .padding(start = 16.dp, end = 16.dp, top = 18.dp, bottom = 12.dp),
                horizontalAlignment = Alignment.CenterHorizontally,
            ) {
                Text(copy(UiMessages.sessionRenameTitle()), style = T.s17b, color = p.ink, textAlign = TextAlign.Center, modifier = Modifier.padding(bottom = 12.dp))
                BasicTextField(
                    value,
                    { value = it.copy(text = sanitizeTitle(it.text)) },
                    Modifier.fillMaxWidth().padding(bottom = 8.dp).focusRequester(focus),
                    textStyle = T.s15.copy(color = p.ink),
                    cursorBrush = SolidColor(p.blue),
                    singleLine = true,
                    keyboardOptions = KeyboardOptions(imeAction = ImeAction.Done),
                    keyboardActions = KeyboardActions(onDone = { save() }),
                    decorationBox = { inner -> FieldBox { inner() } },
                )
                val err = if (slot.hasErrorText()) uiText(slot.errorText) else ""
                Text(
                    if (err.isNotEmpty()) copy(UiMessages.projectsAndroidSaveFailed(reason = err)) else copy(UiMessages.sessionAutoTitleHint()),
                    style = T.s13,
                    color = if (err.isNotEmpty()) p.red else p.ink2,
                    textAlign = TextAlign.Center,
                    modifier = Modifier.padding(bottom = 14.dp),
                )
                Btns {
                    PillButton(uiText(UiMessages.commonCancel()), onClose, Modifier.weight(1f), enabled = !busy)
                    PillButton(uiText(UiMessages.commonSave()), { save() }, Modifier.weight(1f), ButtonKind.INK, enabled = !busy)
                }
            }
        }
    }
}

@Composable
fun ContextBox(percent: Int, detail: String, action: String?, onAction: () -> Unit, enabled: Boolean = true) {
    val p = pal
    Row(
        Modifier.fillMaxWidth().clip(Radii.R14).background(if (LocalOnSoft.current) p.raised else p.gbg).padding(12.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Column(Modifier.weight(1f)) {
            Text("$percent%", style = T.h22, color = if (percent > 80) p.orangeInk else p.ink)
            Text(detail, style = T.s13, color = p.ink2)
        }
        if (action != null) {
            Box(Modifier.heightIn(min = 44.dp).clickable(enabled = enabled, onClick = onAction).padding(horizontal = 6.dp), contentAlignment = Alignment.Center) {
                Text(action, style = T.s15b, color = if (enabled) p.blue else p.ink2)
            }
        }
    }
}

@Composable
fun ChoiceGrid(items: List<String>, selected: String?, enabled: Boolean, onPick: (String) -> Unit) {
    val p = pal
    Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
        for (row in items.chunked(3)) Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
            for (e in row) {
                val on = e == selected
                Box(
                    Modifier.weight(1f).heightIn(min = 44.dp).clip(Radii.R14)
                        .background(if (on) p.fill else p.gbg)
                        .then(if (on) Modifier.border(1.dp, p.blue, Radii.R14) else Modifier)
                        .clickable(enabled = enabled) { onPick(e) },
                    contentAlignment = Alignment.Center,
                ) {
                    Text(e, style = T.s13, color = if (on) p.blue else if (enabled) p.ink else p.ink2)
                }
            }
            repeat(3 - row.size) { Box(Modifier.weight(1f)) }
        }
    }
}

private fun foldModelChoices(offered: List<SessionModelChoiceView>, pickedSlug: String, showAll: Boolean): List<SessionModelChoiceView> {
    if (showAll || offered.size <= 5) return offered
    val picked = offered.firstOrNull { it.slug == pickedSlug }
    val out = ArrayList<SessionModelChoiceView>(5)
    if (picked != null) out += picked
    for (m in offered) {
        if (out.size >= 5) break
        if (m.slug != picked?.slug) out += m
    }
    return out
}

@Composable
fun ModelSheet(sid: String, vm: SessionViewModel, onOpenUsage: (() -> Unit)? = null, onClose: () -> Unit) {
    val copy = uiText()
    val p = pal
    val remote = LocalRemote.current
    var all by remember { mutableStateOf(false) }
    var pickedModel by remember { mutableStateOf("") }
    var submittedModel by remember(sid) { mutableStateOf(false) }
    var submittedCompact by remember(sid) { mutableStateOf(false) }

    val actions = vm.actions
    val header = vm.header
    val modelSlot = actions.setModel
    val compactSlot = actions.compact
    val modelBusy = modelSlot.busy || modelSlot.status == SessionCommandStatus.SESSION_COMMAND_STATUS_PENDING
    val compactBusy = compactSlot.busy || compactSlot.status == SessionCommandStatus.SESSION_COMMAND_STATUS_PENDING
    val turnRunning = vm.composer.hasActiveTurn

    LaunchedEffect(submittedModel, modelBusy, modelSlot.status) {
        if (submittedModel && !modelBusy && modelSlot.status == SessionCommandStatus.SESSION_COMMAND_STATUS_IDLE) onClose()
    }
    LaunchedEffect(submittedCompact, compactBusy, compactSlot.status) {
        if (submittedCompact && !compactBusy && compactSlot.status == SessionCommandStatus.SESSION_COMMAND_STATUS_IDLE) onClose()
    }

    fun setModel(slug: String, effort: String) {
        submittedModel = true
        remote.submitSessionCommand(
            sid,
            Command.newBuilder().setSetModel(
                SetModelRequest.newBuilder().setSessionId(sid).setModel(slug).also { if (effort.isNotEmpty()) it.setEffort(effort) },
            ).build(),
            machineId = header.machineId,
        )
    }

    Sheet(onClose) {
        SheetHead(copy(UiMessages.sessionSettings()))
        if (header.hasContext) {
            val detail = if (header.hasContextText()) uiText(header.contextText)
            else if (header.contextWindow > 0) copy(UiMessages.sessionContextTokens(total = "${header.contextWindow / 1000}k", used = "${header.contextTokens / 1000}k"))
            else copy(UiMessages.sessionAndroidContextUsage())
            ContextBox(
                header.contextPercent,
                detail,
                if (actions.canCompact) (if (compactBusy) copy(UiMessages.sessionCompacting()) else copy(UiMessages.sessionCompact())) else null,
                enabled = actions.canCompact && !turnRunning && !compactBusy,
                onAction = {
                    submittedCompact = true
                    remote.submitSessionCommand(
                        sid,
                        Command.newBuilder().setCompact(CompactRequest.newBuilder().setSessionId(sid)).build(),
                        machineId = header.machineId,
                    )
                },
            )
            if (compactSlot.hasErrorText()) {
                Hint(copy(UiMessages.sessionCompactFailed(reason = uiText(compactSlot.errorText))), color = p.red)
            } else if (actions.hasCompactBlockedReason()) {
                Hint(uiText(actions.compactBlockedReason))
            }
        }
        FieldLabel(copy(UiMessages.modelsTitle()), Modifier.padding(top = 2.dp))
        val offered = actions.modelChoicesList
        when {
            !actions.canSwitchModel -> {
                val modelDisplay = header.modelLabel.ifEmpty { header.model }.ifEmpty { copy(UiMessages.newSessionDefaultModel()) }
                SheetList {
                    ListItem(
                        modelDisplay,
                        small = if (actions.hasModelSwitchBlockedReason()) uiText(actions.modelSwitchBlockedReason) else copy(UiMessages.sessionModelLocked()),
                        enabled = false,
                        last = true,
                        onClick = null,
                    )
                }
                FieldLabel(copy(UiMessages.modelsEffort()))
                val effortDisplay = if (header.hasEffortLabel()) uiText(header.effortLabel)
                else header.effort.ifEmpty { copy(UiMessages.modelsDefault()) }
                SheetList {
                    ListItem(effortDisplay, small = copy(UiMessages.sessionEffortLocked()), enabled = false, last = true, onClick = null)
                }
            }
            actions.hasModelsError() && offered.isEmpty() -> Notice(
                copy(UiMessages.sessionAndroidModelsFailed(reason = uiText(actions.modelsError))),
                tone = NoticeTone.ERROR,
                action = uiText(UiMessages.commonRetry()),
                onAction = { remote.openSession(sid, refreshModels = true) },
            )
            actions.modelsLoading && offered.isEmpty() -> Text(
                copy(UiMessages.projectsReadingDirectory()),
                style = T.s13,
                color = p.ink2,
                modifier = Modifier.padding(vertical = 12.dp),
            )
            else -> {
                val current = offered.firstOrNull { it.selected } ?: offered.firstOrNull { it.slug == header.model }
                if (pickedModel.isEmpty()) pickedModel = current?.slug.orEmpty()
                val list = foldModelChoices(offered, pickedModel, all)
                val few = list.size == offered.size
                SheetList {
                    list.forEachIndexed { i, m ->
                        val note = if (m.effortsCount == 0) copy(UiMessages.sessionNoEffort())
                        else m.effortsList.lastOrNull()?.takeIf { it != "high" }?.let { copy(UiMessages.sessionMaxEffort(effort = it)) }
                        ListItem(
                            m.displayName.ifEmpty { m.slug },
                            small = note,
                            checked = m.slug == pickedModel,
                            enabled = !turnRunning && !modelBusy,
                            last = i == list.size - 1,
                            onClick = {
                                pickedModel = m.slug
                                if (m.effortsCount == 0 && m.slug != current?.slug) setModel(m.slug, "")
                            },
                        )
                    }
                }
                if (!few) AllModels { all = true }
                val model = offered.firstOrNull { it.slug == pickedModel }
                if (model != null && model.effortsCount > 0) {
                    FieldLabel(copy(UiMessages.modelsEffort()))
                    ChoiceGrid(
                        model.effortsList,
                        if (model.slug == current?.slug) header.effort else null,
                        !turnRunning && !modelBusy,
                    ) { setModel(model.slug, it) }
                }
            }
        }
        if (actions.canSwitchModel) {
            Hint(if (turnRunning) copy(UiMessages.sessionAndroidSwitchAfterTurn()) else copy(UiMessages.sessionAndroidSwitchNote()))
        }
        if (modelBusy) Hint(copy(UiMessages.modesSwitching()))
        if (modelSlot.hasErrorText()) Hint(copy(UiMessages.modesSwitchFailed(reason = uiText(modelSlot.errorText))), color = p.red)
        if (actions.hasUsageEntry() && actions.usageEntry.visible) {
            val ue = actions.usageEntry
            val titleText = copy(UiMessages.sessionUsageEntryTitle())
            val rawSub = uiText(ue.subtitle)
            val rawVal = uiText(ue.valueText)
            val valText =
                if (ue.hasData && rawVal.isEmpty()) formatUsageTokens(ue.totalTokens, copy)
                else rawVal
            val sub =
                if (ue.hasData && rawSub.isEmpty()) {
                    val windowLabel =
                        if (ue.fallbackThirtyDays) copy(UiMessages.usageEntryLast30D())
                        else copy(UiMessages.usageEntryLast7D())
                    val hitPart =
                        if (ue.hasCacheHit) copy(UiMessages.usageEntryCachePct(formatUsageCacheHit(ue.cacheHitRate)))
                        else ""
                    val speedPart =
                        if (ue.hasSpeed) "${formatUsageSpeed(ue.recent10SpeedP50)} ${copy(UiMessages.usageKpiUnitToks())}"
                        else ""
                    when {
                        ue.hasSpeed && ue.fewSpeedSamples -> copy(
                            UiMessages.usageEntryFewSamples(
                                count = ue.recent10SpeedCount.toInt(),
                                detail = listOf(windowLabel, hitPart, speedPart).filter { it.isNotEmpty() }.joinToString(" · "),
                            ),
                        )
                        ue.hasSpeed -> listOf(windowLabel, hitPart, speedPart).filter { it.isNotEmpty() }.joinToString(" · ")
                        else -> copy(
                            UiMessages.usageEntryNoSpeed(
                                listOf(windowLabel, hitPart).filter { it.isNotEmpty() }.joinToString(" · "),
                            ),
                        )
                    }
                } else {
                    rawSub
                }
            val foot = uiText(ue.footnote)
            val canNav = ue.supported && onOpenUsage != null
            val a11yLabel = listOf(titleText, sub, valText).filter { it.isNotEmpty() }.joinToString(" · ")
            androidx.compose.foundation.layout.Spacer(Modifier.heightIn(min = 8.dp))
            SheetList {
                Row(
                    Modifier.fillMaxWidth()
                        .heightIn(min = 50.dp)
                        .then(
                            if (canNav) Modifier.clickable(role = Role.Button) {
                                onClose()
                                onOpenUsage?.invoke()
                            } else Modifier,
                        )
                        .semantics(mergeDescendants = true) {
                            contentDescription = a11yLabel
                        }
                        .padding(horizontal = 18.dp, vertical = 8.dp),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(10.dp),
                ) {
                    Column(Modifier.weight(1f)) {
                        Text(titleText, style = T.s17, color = if (canNav) p.ink else p.ink2)
                        if (sub.isNotEmpty()) {
                            Text(sub, style = T.s13, color = p.ink2)
                        }
                    }
                    if (valText.isNotEmpty()) {
                        Text(valText, style = T.s15.digits(), color = p.ink2)
                    }
                    if (canNav) {
                        Arrow(p.ink3, T.s15)
                    }
                }
            }
            if (foot.isNotEmpty()) Hint(foot)
        }
    }
}

@Composable
fun ApprovalPage(sid: String, aid: String) {
    val copy = uiText()
    val remote = LocalRemote.current
    LaunchedEffect(remote, sid) { remote.openSession(sid) }
    val vm by remote.watchSession(sid).collectAsState(initial = SessionViewModel.getDefaultInstance())
    val p = pal
    val t = now(15_000)
    val dock = vm.dock
    val hasApproval = dock.hasItem && dock.kind == SessionDockKind.SESSION_DOCK_KIND_APPROVAL &&
        (aid.isEmpty() || dock.approval.approvalId == aid)
    val a = if (hasApproval) dock.approval else null
    var reason by Drafts.of("deny:$aid")
    val clipboard = LocalClipboard.current
    val act = rememberAction()
    val subtitle = listOfNotNull(
        uiText(vm.header.title).ifEmpty { null },
        if (a != null && dock.hasTimeLeft()) uiText(dock.timeLeft).ifEmpty { null } else null,
    ).joinToString(" · ").ifEmpty { null }

    PageScaffold(copy(UiMessages.approvalDetails()), subtitle = subtitle) {
        if (a == null) {
            Text(
                if (vm.timeline.loaded) copy(UiMessages.sessionApprovalResolved()) else copy(UiMessages.projectsReadingDirectory()),
                style = T.s15,
                color = p.ink2,
                modifier = Modifier.padding(16.dp),
            )
            if (vm.timeline.loaded && reason.isNotEmpty()) {
                Column(Modifier.fillMaxWidth().verticalScroll(rememberScrollState()).padding(horizontal = 16.dp)) {
                    Code(reason)
                    TextAction(
                        uiText(UiMessages.commonCopy()),
                        { act.run({ clipboard.setClipEntry(ClipEntry(ClipData.newPlainText("", reason))); null }) },
                        alignment = Alignment.Center,
                    )
                }
            }
            return@PageScaffold
        }
        val decision = rememberApprovalReply(vm.header.machineId, sid, a.approvalId, dock.observedSeq, dock.command, a.isSkeleton)
        val card = Modifier.padding(start = 16.dp, end = 16.dp, top = 8.dp, bottom = 16.dp)
        if (decision.submitted) {
            ApprovalSubmitted(uiText(dock.title), decision, card)
            return@PageScaffold
        }
        var denying by androidx.compose.runtime.saveable.rememberSaveable(aid) { mutableStateOf(false) }
        val locked = vm.header.computerUnlinked || (dock.deadlineMs > 0 && t >= dock.deadlineMs)
        DarkCard(Modifier.weight(1f, fill = false).then(card)) {
            DarkCardHead(uiText(dock.title), uiText(dock.timeLeft))
            if (a.isSkeleton) {
                Text(copy(UiMessages.sessionSkeleton()), style = T.s15, color = CardColors.text2, modifier = Modifier.padding(top = 12.dp))
                return@DarkCard
            }
            Column(Modifier.weight(1f, fill = false).verticalScroll(rememberScrollState())) { ApprovalContent(a) }
            ApprovalButtons(
                approvalId = a.approvalId,
                d = decision,
                denying = denying,
                onDenying = { denying = it },
                locked = locked,
                truncated = a.bodyTruncated,
                always = a.canAlwaysAllow,
                alwaysLabel = uiText(a.alwaysLabel).ifEmpty { copy(UiMessages.approvalAlwaysAllow()) },
                denyPlaceholder = uiText(a.denyReasonPlaceholder).ifEmpty { copy(UiMessages.approvalReasonPlaceholder()) },
            )
        }
    }
}

@Composable
fun PlanPage(sid: String) {
    val copy = uiText()
    val remote = LocalRemote.current
    val nav = LocalNav.current
    LaunchedEffect(remote, sid) { remote.openSession(sid) }
    val vm by remote.watchSession(sid).collectAsState(initial = SessionViewModel.getDefaultInstance())
    val hasPlan = vm.dock.hasItem && vm.dock.kind == SessionDockKind.SESSION_DOCK_KIND_PLAN
    PageScaffold(copy(UiMessages.modesPlan()), subtitle = uiText(vm.header.title).ifEmpty { null }) {
        if (!hasPlan) {
            Text(
                if (vm.timeline.loaded) copy(UiMessages.projectsReadingDirectory()) else copy(UiMessages.projectsReadingDirectory()),
                style = T.s15,
                color = pal.ink2,
                modifier = Modifier.padding(16.dp),
            )
            return@PageScaffold
        }
        val md = vm.dock.plan.markdown
        if (md.isEmpty()) {
            Text(copy(UiMessages.sessionSkeleton()), style = T.s13, color = pal.ink2, modifier = Modifier.padding(16.dp))
            return@PageScaffold
        }
        Column(Modifier.weight(1f).verticalScrollFaded().padding(horizontal = 16.dp)) {
            Markdown(md, bleed = 16.dp)
        }
        Column(Modifier.padding(horizontal = 14.dp).topLine(pal.sep).padding(top = 8.dp, bottom = 16.dp)) {
            PlanButtons(sid, vm, full = true, onCard = false) { nav.back() }
        }
    }
}

@Composable
fun PageScaffold(
    title: String,
    subtitle: String? = null,
    readable: Boolean = false,
    action: (@Composable () -> Unit)? = null,
    onBack: (() -> Unit)? = null,
    wrapTitle: Boolean = false,
    subtitleDot: io.github.lewin671.remoteagent.core.proto.MachineStatusDot? = null,
    content: @Composable ColumnScope.() -> Unit,
) {
    val back = onBack ?: LocalNav.current.back
    if (onBack != null) androidx.activity.compose.BackHandler(onBack = onBack)
    val p = pal
    Column(Modifier.fillMaxSize().background(p.paper).imePadding()) {
        PageBar(title, back, subtitle = subtitle, right = action, wrapTitle = wrapTitle, subtitleDot = subtitleDot)
        if (readable) Column(Modifier.weight(1f).fillMaxWidth().readableColumn()) { content() }
        else content()
    }
}

fun Modifier.readableColumn(): Modifier =
    wrapContentWidth(Alignment.CenterHorizontally).widthIn(max = 672.dp)

@Composable
fun PageNote(text: String) {
    Text(
        text,
        style = T.s13,
        color = pal.ink2,
        textAlign = TextAlign.Center,
        modifier = Modifier.fillMaxWidth().padding(24.dp),
    )
}

@Composable
fun AllModels(onClick: () -> Unit) {
    Box(Modifier.heightIn(min = 44.dp).clickable(onClick = onClick), contentAlignment = Alignment.CenterStart) {
        ArrowText(uiText(UiMessages.modelsAll()), pal.blue, T.s15)
    }
}
