package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.selection.selectable
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.layout.boundsInWindow
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.core.proto.*
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch

/**
 * 新任务 in a project (design-system §6 新任务): renders Core's [NewSessionViewModel].
 */
@Composable
fun ProjectScreen(key: String, focus: Boolean) {
    val remote = LocalRemote.current
    val initialView = remember(remote, key) { remote.coreHost.snapshotNewSessionView(key) }
    LaunchedEffect(key) { remote.coreHost.openNewSession(key) }
    val rawView by remote.newSessionView.collectAsState()
    val view = if (key.isEmpty() || rawView.projectKey == key) rawView else initialView
    val state by remote.state.collectAsState()
    val home by remote.homeView.collectAsState()
    var picking by remember { mutableStateOf(false) }

    Column(Modifier.fillMaxSize().background(pal.paper)) {
        NewSessionBox(
            view = view,
            state = state,
            focus = focus,
            onProject = { picking = true },
        )
    }
    if (picking) PickProjectSheet(home.sidebarProjects, onClose = { picking = false }, replace = true)
}

/**
 * The new-session composer and notice column driven directly by Core's [NewSessionViewModel].
 * Preserves local attachment freezing & idempotent retry in [Starts] for image/file IDs.
 */
@Composable
fun NewSessionBox(
    view: NewSessionViewModel,
    state: RemoteState,
    focus: Boolean,
    onProject: (() -> Unit)? = null,
) {
    val p = pal
    val remote = LocalRemote.current
    val nav = LocalNav.current
    androidx.activity.compose.BackHandler { nav.back() }
    val key = view.projectKey
    val cfg = view.config
    val machine = cfg.machineId.takeIf { it.isNotEmpty() }
    val images = ImageDrafts.forProject(key)
    var text by images.text
    val fr = remember { FocusRequester() }
    var focused by rememberSaveable { mutableStateOf(false) }
    LaunchedEffect(focus) { if (focus && !focused) { focused = true; runCatching { fr.requestFocus() } } }

    LaunchedEffect(key, text, images.images.size) {
        if (key.isNotEmpty()) {
            remote.coreHost.setNewSessionPrompt(text, images.images.size, attachmentsMoved = false)
        }
    }

    val copy = uiText()
    val attachCaps = view.attachmentCaps()
    val pickState = rememberAttachmentPickState(remote, machine, "", images.pictures.size, images.documents.size, attachCaps)
    RefreshDraftUploads(images, remote, machine, attachCaps)
    val pick = rememberImagePicker(images, remote, machine, attachCaps)
    val draftBlocked = rememberDraftAttachmentsBlocked(remote, machine, "", images.documents, images.pictures, attachCaps)

    val uploadsReady =
        !images.restoring &&
            !images.readingFiles &&
            draftBlocked == null &&
            (machine == null || images.images.all { it.readyFor(machine) })
    val canStart = view.canStart && uploadsReady

    fun start() {
        val currentBlocked = evaluateDraftAttachmentsBlocked(remote, machine, "", images.documents, images.pictures, copy = copy)
        if (currentBlocked != null) {
            images.notice = currentBlocked
            return
        }
        if (machine != null && attachCaps.canUploadAnything) images.sendAllTo(remote, machine, attachCaps)
        if (!canStart) return
        val st = Starts.of(key)
        val cfgKey = Triple(cfg.machineId, cfg.agent.number, "${cfg.model}/${cfg.effort}/${cfg.standardContext}")
        if (st.frozen && !st.same(text.trim(), cfgKey, images.images)) {
            st.taken.forEach { it.allowAbort(st.transferOwner) }
            st.taken = emptyList()
            st.frozen = false
        }
        if (!st.frozen) {
            st.prompt = text.trim()
            st.configKey = cfgKey
            st.taken = images.images.filter { it.readyFor(cfg.machineId) }
            st.frozen = true
        }
        remote.coreHost.submitNewSession(
            projectKey = key,
            autoModeSupported = true,
            imageIds = st.imageIds,
            fileIds = st.fileIds,
        )
    }

    val picker = remember { ComposerPicker {} }
    val keyboard = LocalSoftwareKeyboardController.current
    val focusManager = LocalFocusManager.current
    val density = LocalDensity.current
    val ime = WindowInsets.ime
    val pickerScope = rememberCoroutineScope()
    var opening by remember { mutableStateOf<Job?>(null) }
    var machineSheetOpen by remember { mutableStateOf(false) }

    fun afterKeyboard(action: () -> Unit) {
        opening?.cancel()
        focusManager.clearFocus(force = true)
        keyboard?.hide()
        opening = pickerScope.launch {
            snapshotFlow { ime.getBottom(density) }.first { it == 0 }
            withFrameNanos { }
            action()
        }
    }

    LaunchedEffect(key, machine, cfg.agent) {
        opening?.cancel()
        picker.cancel()
        if (machine != null) {
            // Core triggers ListModels/GetSettings automatically on project/agent/machine switches.
        }
    }

    val projectTitle = uiText(view.projectName).ifEmpty { uiText(io.github.lewin671.remoteagent.core.listProjectName(key)) }

    ComposerPickerBack(picker)
    Column(Modifier.fillMaxSize().composerPickerEvents(picker)) {
        Row(
            Modifier.fillMaxWidth().onGloballyPositioned { picker.top = it.boundsInWindow().bottom }.padding(start = 16.dp, end = 16.dp, top = 6.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            GlassButton(Lucide.X, uiText(UiMessages.commonClose()), nav.back)
            Column(Modifier.weight(1f).padding(horizontal = 8.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                Row(
                    Modifier.heightIn(min = 32.dp).clip(Radii.Pill)
                        .then(if (onProject != null) Modifier.clickable(role = Role.Button, onClickLabel = uiText(UiMessages.newSessionAndroidChangeProject()), onClick = { afterKeyboard(onProject) }) else Modifier)
                        .padding(horizontal = 6.dp).testTag("new-project"),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(6.dp),
                ) {
                    ProjectAvatar(key, 22)
                    Text(projectTitle, style = T.s17b, color = p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
                    if (onProject != null) Arrow(p.ink3, T.s13, down = true)
                }
                if (view.machineSelectable && view.machineName.isNotEmpty()) Row(
                    Modifier.heightIn(min = 24.dp).clip(Radii.Pill)
                        .clickable(role = Role.Button, onClickLabel = uiText(UiMessages.newSessionAndroidChangeComputer()), onClick = { afterKeyboard { machineSheetOpen = true } })
                        .padding(horizontal = 6.dp).testTag("new-machine"),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(2.dp),
                ) {
                    Text(view.machineName, style = T.s12, color = p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
                    Arrow(p.ink3, T.s12, down = true)
                }
            }
            Spacer(Modifier.size(44.dp))
        }

        Column(Modifier.weight(1f).fillMaxWidth().verticalScroll(rememberScrollState()).padding(top = 10.dp)) {
            Column(Modifier.padding(horizontal = 12.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                for (n in view.noticesList) {
                    NewSessionNoticeRow(
                        n = n,
                        onAction = { act ->
                            when (act) {
                                NoticeActionKind.NOTICE_ACTION_KIND_RETRY_START -> start()
                                NoticeActionKind.NOTICE_ACTION_KIND_ABANDON_START -> remote.coreHost.abandonNewSessionStart(key)
                                NoticeActionKind.NOTICE_ACTION_KIND_CHANGE_MACHINE -> afterKeyboard { machineSheetOpen = true }
                                NoticeActionKind.NOTICE_ACTION_KIND_CHANGE_MODEL -> picker.toggle("model")
                                NoticeActionKind.NOTICE_ACTION_KIND_OPEN_MACHINE_SETTINGS -> nav.device(n.targetMachineId.ifEmpty { cfg.machineId })
                                NoticeActionKind.NOTICE_ACTION_KIND_RETRY_SAVE_CONFIG -> remote.coreHost.retryNewSessionSaveConfig(key)
                                NoticeActionKind.NOTICE_ACTION_KIND_READD_PROJECT -> remote.coreHost.addProject(key)
                                NoticeActionKind.NOTICE_ACTION_KIND_OPEN_DISK_USAGE -> nav.machineStatus(n.targetMachineId.ifEmpty { cfg.machineId })
                                NoticeActionKind.NOTICE_ACTION_KIND_RETRY_READ_SETTINGS -> remote.coreHost.retryNewSessionModels(key)
                                NoticeActionKind.NOTICE_ACTION_KIND_ENABLE_AGENT -> nav.deviceForAgent(n.targetMachineId.ifEmpty { cfg.machineId }, n.targetAgent)
                                else -> {}
                            }
                        },
                        onChooseOfflineMachine = { machineId -> remote.coreHost.selectNewSessionMachine(machineId) },
                    )
                }
            }
        }

        ComposeBox(
            text, { text = it },
            modifier = Modifier.onGloballyPositioned { picker.composer = it.boundsInWindow() }.testTag("new-composer"),
            placeholder = uiText(UiMessages.newSessionPrompt()),
            canSend = canStart,
            onSend = { start() },
            onPlus = pick,
            onPlusBlocked = if (!images.restoring) ({ showAttachmentBlockedNotice(images, remote, machine, copy = copy) }) else null,
            onFile = rememberDocumentPicker(images, remote, machine, attachCaps),
            fileMetadataNotice = attachCaps.fileMetadataNotice?.let { uiText(it) }.orEmpty(),
            plusEnabled = !images.restoring && pickState.canPickAny,
            sendLabel = uiText(UiMessages.newSessionAndroidStart()),
            focusRequester = fr,
            hasDraft = text.isNotBlank() || images.images.isNotEmpty(),
            top = { ImageStrip(images, remote, machine, attachCaps.maxImages, attachCaps) },
            tools = {
                NewTaskTools(
                    view = view,
                    modelOpen = picker.open == "model",
                    onAgent = { picker.toggle("agent") },
                    onModel = { picker.toggle("model") },
                    modifier = Modifier.weight(1f, fill = false),
                    agentModifier = Modifier.onGloballyPositioned { picker.agent = it.boundsInWindow() },
                    modelModifier = Modifier.onGloballyPositioned { picker.model = it.boundsInWindow() },
                )
            },
        )
    }

    if (picker.open == "agent") ComposerPopover(picker, uiText(UiMessages.newSessionCliPickerLabel()), panel = false) {
        view.availableAgentsList.forEach { opt ->
            Row(
                Modifier.fillMaxWidth().heightIn(min = 44.dp).clip(RoundedCornerShape(8.dp))
                    .selectable(selected = opt.selected, role = Role.RadioButton, onClick = {
                        picker.close()
                        remote.coreHost.selectNewSessionAgent(opt.agent)
                    }).padding(horizontal = 12.dp, vertical = 8.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                AgentMark(opt.agent, T.s17, p.ink)
                Text(opt.displayName, style = T.s17, color = p.ink, modifier = Modifier.weight(1f))
                if (opt.selected) Ic(Lucide.Check, 18.dp, p.blue)
            }
        }
    }

    if (picker.open == "model") ComposerPopover(picker, uiText(UiMessages.modelsTitle()), panel = true) {
        NewTaskModelPanel(
            panel = view.modelPanel,
            machineName = view.machineName,
            selectedModel = cfg.model,
            onSelectModel = { model, effort, standardContext ->
                remote.coreHost.selectNewSessionModel(model, effort, standardContext)
            },
            onRetryModels = { remote.coreHost.retryNewSessionModels(key) },
            onClose = picker::close,
            notice = {
                if (!view.settled) Text(uiText(UiMessages.newSessionReadingPrevious()), style = T.s13, color = p.ink2, modifier = Modifier.testTag("settings-unread"))
            },
        )
    }

    if (machineSheetOpen) Sheet({ machineSheetOpen = false }) {
        SheetHead(uiText(UiMessages.commonMachine()), uiText(UiMessages.commonCancel()), { machineSheetOpen = false })
        SheetList {
            view.modelPanel.machineChoicesList.forEachIndexed { i, mc ->
                ListItem(
                    mc.name,
                    uiText(mc.note),
                    checked = mc.selected,
                    enabled = mc.online,
                    last = i == view.modelPanel.machineChoicesCount - 1,
                    onClick = {
                        remote.coreHost.selectNewSessionMachine(mc.machineId)
                        machineSheetOpen = false
                    },
                    trailing = { StatusMark(if (mc.online) MarkKind.ONLINE else MarkKind.OFF) },
                )
            }
        }
        Hint(uiText(UiMessages.newSessionAndroidChangeMachineHint()))
    }
}

@Composable
private fun ProjectSettingsRow(
    label: String,
    value: String,
    onClick: (() -> Unit)?,
    dot: androidx.compose.ui.graphics.Color? = null,
) {
    val p = pal
    if (LocalDensity.current.fontScale >= 1.8f) {
        Column(
            Modifier.fillMaxWidth().heightIn(min = 44.dp)
                .then(if (onClick != null) Modifier.clickable(onClick = onClick) else Modifier)
                .padding(horizontal = 14.dp, vertical = 8.dp),
        ) {
            Text(label, style = T.s13, color = p.ink2, maxLines = 1)
            Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.End) {
                if (dot != null) StatusMark(MarkKind.ONLINE, color = dot)
                Text(
                    value,
                    style = T.s13,
                    color = p.ink,
                    textAlign = androidx.compose.ui.text.style.TextAlign.End,
                    modifier = Modifier.padding(start = 8.dp).weight(1f, fill = false),
                )
                Spacer(Modifier.width(8.dp))
                Arrow(p.ink3, ts(18.sp))
            }
        }
        return
    }
    Row(
        Modifier.fillMaxWidth().heightIn(min = 44.dp)
            .then(if (onClick != null) Modifier.clickable(onClick = onClick) else Modifier)
            .padding(horizontal = 14.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        Text(label, style = T.s13, color = p.ink2, maxLines = 1, modifier = Modifier.widthIn(min = 36.dp).weight(1f))
        if (dot != null) StatusMark(MarkKind.ONLINE, color = dot)
        Text(
            value,
            style = T.s13,
            color = p.ink,
            maxLines = 2,
            overflow = TextOverflow.Ellipsis,
            textAlign = androidx.compose.ui.text.style.TextAlign.End,
            modifier = Modifier.widthIn(max = 200.dp),
        )
        Arrow(p.ink3, ts(18.sp))
    }
}

/**
 * 项目设置 sheet backed directly by Core's [NewSessionViewModel] and [NewSessionModelPanelView].
 */
@Composable
fun CoreProjectSettingsSheet(
    projectKey: String,
    open: String = "",
    onClose: () -> Unit,
) {
    val p = pal
    val remote = LocalRemote.current
    val initialView = remember(remote, projectKey) { remote.coreHost.snapshotNewSessionView(projectKey) }
    LaunchedEffect(projectKey) { remote.coreHost.openNewSession(projectKey) }
    val rawView by remote.newSessionView.collectAsState()
    val view = if (projectKey.isEmpty() || rawView.projectKey == projectKey) rawView else initialView
    val syncView by remote.coreHost.syncStateView.collectAsState()
    val panel = view.modelPanel
    val blockedPrefsText = when {
        view.prefsBlocked && view.prefsBlockedReason.isCoreTextNotEmpty() -> uiText(view.prefsBlockedReason)
        view.prefsBlocked -> uiText(UiMessages.projectsConnectToEdit())
        syncView.connection != io.github.lewin671.remoteagent.core.proto.ConnState.CONN_STATE_CONNECTED ->
            uiText(UiMessages.projectsConnectToEdit())
        !syncView.prefsLoaded -> uiText(UiMessages.projectsLoading())
        else -> null
    }
    val manyMachines = panel.machineChoicesCount > 1
    val root = open.ifEmpty { if (!manyMachines) "model" else "" }
    var page by remember(open) { mutableStateOf(open) }
    val shown = page.ifEmpty { root }
    val back: (() -> Unit)? = if (shown == root) null else ({ page = "" })
    val editable = blockedPrefsText == null && view.settled
    val saveError = view.noticesList.firstOrNull { it.kind == NoticeKind.NOTICE_KIND_CONFIG_SAVE_ERROR }
    val agents = view.availableAgentsList

    @Composable
    fun agentSelector() {
        if (agents.size > 1) {
            Segmented(
                agents.map { it.displayName },
                agents.indexOfFirst { it.selected }.coerceAtLeast(0),
                { idx -> remote.coreHost.selectNewSessionAgent(agents[idx].agent) },
                Modifier.padding(top = 6.dp, bottom = 10.dp).fillMaxWidth(),
                enabled = { editable },
            )
        }
    }

    Sheet(onClose) {
        when (shown) {
            "" -> {
                SheetHead(uiText(UiMessages.projectsAndroidSettings()), uiText(UiMessages.commonDone()), onClose)
                if (blockedPrefsText != null) Notice(blockedPrefsText)
                if (!view.settled) Hint(uiText(UiMessages.newSessionReadingPrevious()))
                if (saveError != null) Notice(
                    uiText(saveError.text),
                    tone = NoticeTone.WARN,
                    action = uiText(UiMessages.commonRetry()),
                    onAction = { remote.coreHost.retryNewSessionSaveConfig(projectKey) },
                )
                FieldLabel(uiText(UiMessages.newSessionAndroidNewSession()))
                if (manyMachines) {
                    val selectedOnline = panel.machineChoicesList.firstOrNull { it.selected }?.online == true
                    SheetList {
                        ProjectSettingsRow(
                            uiText(UiMessages.commonMachine()),
                            view.machineName,
                            { page = "machine" },
                            dot = if (selectedOnline) p.green else p.faint,
                        )
                    }
                }
                agentSelector()
                val modelValue = listOf(
                    uiText(view.modelLabel).ifEmpty { uiText(UiMessages.newSessionDefaultModel()) },
                    view.effortLabel,
                ).filter { it.isNotEmpty() }.joinToString(" · ")
                SheetList {
                    ProjectSettingsRow(uiText(UiMessages.modelsTitle()), modelValue, { page = "model" })
                }
            }
            "machine" -> {
                SheetHead(uiText(UiMessages.commonMachine()), uiText(UiMessages.commonDone()), onClose, onBack = back)
                if (blockedPrefsText != null) Notice(blockedPrefsText)
                SheetList {
                    panel.machineChoicesList.forEachIndexed { i, mc ->
                        ListItem(
                            mc.name,
                            uiText(mc.note),
                            checked = mc.selected,
                            enabled = mc.online && editable,
                            last = i == panel.machineChoicesCount - 1,
                            onClick = {
                                remote.coreHost.selectNewSessionMachine(mc.machineId)
                                if (back != null) back() else onClose()
                            },
                            trailing = { StatusMark(if (mc.online) MarkKind.ONLINE else MarkKind.OFF) },
                        )
                    }
                }
                Hint(uiText(UiMessages.newSessionAndroidChangeMachineHint()))
            }
            "model" -> {
                val modelTitle = if (open.isEmpty() && back == null) UiMessages.projectsAndroidSettings() else UiMessages.modelsTitle()
                SheetHead(uiText(modelTitle), uiText(UiMessages.commonDone()), onClose, onBack = back)
                if (blockedPrefsText != null) Notice(blockedPrefsText)
                if (!view.settled) Hint(uiText(UiMessages.newSessionReadingPrevious()))
                if (saveError != null) Notice(
                    uiText(saveError.text),
                    tone = NoticeTone.WARN,
                    action = uiText(UiMessages.commonRetry()),
                    onAction = { remote.coreHost.retryNewSessionSaveConfig(projectKey) },
                )
                if (back == null) agentSelector()
                var allModels by remember(view.config.machineId, view.config.agent) { mutableStateOf(false) }
                val errText = uiText(panel.modelsError)
                val controlsEnabled = editable && panel.panelEnabled
                when {
                    panel.modelsLoading && panel.modelsCount == 0 -> Text(
                        errText.ifEmpty { uiText(UiMessages.modelsAndroidReadingMachine(name = view.machineName)) },
                        style = T.s13,
                        color = p.ink2,
                        modifier = Modifier.padding(vertical = 12.dp),
                    )
                    errText.isNotEmpty() && panel.modelsCount == 0 -> Notice(
                        errText,
                        tone = NoticeTone.WARN,
                        action = uiText(UiMessages.commonRetry()),
                        onAction = { remote.coreHost.retryNewSessionModels(projectKey) },
                    )
                    else -> {
                        val shownModels = panelVisibleModels(panel.modelsList, allModels)
                        SheetList {
                            ListItem(
                                uiText(panel.defaultModelLabel).ifEmpty { uiText(UiMessages.modelsDefault()) },
                                checked = panel.defaultModelChecked,
                                enabled = controlsEnabled,
                                last = shownModels.isEmpty(),
                                onClick = { remote.coreHost.selectNewSessionModel("", "", null) },
                            )
                            shownModels.forEachIndexed { i, m ->
                                ListItem(
                                    m.displayName.ifEmpty { m.slug },
                                    small = uiText(m.fixedEffortNote).ifEmpty { null },
                                    checked = m.checked,
                                    enabled = controlsEnabled,
                                    last = i == shownModels.lastIndex,
                                    onClick = { remote.coreHost.selectNewSessionModel(m.slug, "", null) },
                                )
                            }
                        }
                        if (!allModels && shownModels.size < panel.modelsCount) AllModels { allModels = true }
                        if (panel.effortChoicesCount > 1) {
                            FieldLabel(uiText(panel.effortTitle).ifEmpty { uiText(UiMessages.modelsEffort()) })
                            ChoiceGrid(panel.effortChoicesList, panel.selectedEffort, controlsEnabled) { effort ->
                                remote.coreHost.selectNewSessionModel(view.config.model, effort, null)
                            }
                        }
                    }
                }
                if (panel.supportsContextWindow) {
                    FieldLabel(uiText(UiMessages.modelsContextWindow()))
                    if (panel.contextFixed) Hint(uiText(panel.contextNote).ifEmpty { uiText(UiMessages.modelsStandardOnly()) })
                    else Segmented(
                        listOf(uiText(UiMessages.modelsExtendedWindow()), uiText(UiMessages.modelsStandardWindow())),
                        if (panel.standardContext) 1 else 0,
                        { remote.coreHost.selectNewSessionModel(view.config.model, panel.selectedEffort, it == 1) },
                        Modifier.fillMaxWidth(),
                        enabled = { controlsEnabled },
                    )
                }
            }
        }
    }
}

@Composable
internal fun NewSessionNoticeRow(
    n: NewSessionNoticeView,
    onAction: (NoticeActionKind) -> Unit,
    onChooseOfflineMachine: (String) -> Unit,
) {
    val p = pal
    if (n.hasDraftChoices) {
        Column(Modifier.fillMaxWidth().clip(Radii.R14).background(p.gbg).padding(horizontal = 12.dp, vertical = 9.dp)) {
            if (n.draftTitle.isCoreTextNotEmpty()) Text(uiText(n.draftTitle), style = T.s13, color = p.orangeInk)
            if (n.draftSubtitle.isCoreTextNotEmpty()) Text(uiText(n.draftSubtitle), style = T.s12, color = p.ink2)
            for (choice in n.draftChoicesList) {
                Row(
                    Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable { onChooseOfflineMachine(choice.machineId) },
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Column(Modifier.weight(1f)) {
                        Text(uiText(choice.title), style = T.s15b, color = p.ink)
                        Text(uiText(choice.note), style = T.s12, color = p.ink2)
                    }
                    Arrow(p.ink2, T.s15)
                }
            }
            if (n.draftChoicesCount == 0 && n.draftNoneText.isCoreTextNotEmpty()) {
                Text(uiText(n.draftNoneText), style = T.s12, color = p.ink2)
            }
        }
        return
    }
    val tone = when (n.severity) {
        NoticeSeverity.NOTICE_SEVERITY_ERROR -> NoticeTone.ERROR
        NoticeSeverity.NOTICE_SEVERITY_WARN -> NoticeTone.WARN
        else -> NoticeTone.PLAIN
    }
    val actionLabel = when (n.action) {
        NoticeActionKind.NOTICE_ACTION_KIND_RETRY_START,
        NoticeActionKind.NOTICE_ACTION_KIND_RETRY_SAVE_CONFIG,
        NoticeActionKind.NOTICE_ACTION_KIND_RETRY_READ_SETTINGS -> uiText(UiMessages.commonRetry())
        NoticeActionKind.NOTICE_ACTION_KIND_ABANDON_START -> uiText(UiMessages.newSessionAndroidAbandon())
        NoticeActionKind.NOTICE_ACTION_KIND_CHANGE_MACHINE -> uiText(UiMessages.newSessionChangeMachine())
        NoticeActionKind.NOTICE_ACTION_KIND_CHANGE_MODEL -> uiText(UiMessages.newSessionChangeModel())
        NoticeActionKind.NOTICE_ACTION_KIND_OPEN_MACHINE_SETTINGS -> uiText(UiMessages.newSessionChangeDefaults())
        NoticeActionKind.NOTICE_ACTION_KIND_READD_PROJECT -> uiText(UiMessages.projectsAddAgain())
        NoticeActionKind.NOTICE_ACTION_KIND_OPEN_DISK_USAGE -> uiText(UiMessages.machineStatusTitle())
        NoticeActionKind.NOTICE_ACTION_KIND_ENABLE_AGENT -> uiText(UiMessages.newSessionEnableAgent())
        else -> null
    }
    if (n.kind == io.github.lewin671.remoteagent.core.proto.NoticeKind.NOTICE_KIND_DISK_FULL) {
        DiskFullCard(
            onUsage = { onAction(NoticeActionKind.NOTICE_ACTION_KIND_OPEN_DISK_USAGE) },
            onRetry = if (n.action == NoticeActionKind.NOTICE_ACTION_KIND_RETRY_START) ({ onAction(NoticeActionKind.NOTICE_ACTION_KIND_RETRY_START) }) else null,
        )
        return
    }
    Notice(
        uiText(n.text),
        tone = tone,
        action = actionLabel,
        onAction = { onAction(n.action) },
    )
}

private const val PICK_PROJECT_PICKER_KEY = "pick-project"
private const val NEW_PROJECT_PICKER_KEY = "new-project"

/**
 * 新会话 · 选项目 sheet from the new-session button on Home, driven by Core's [ProjectListingView].
 */
@Composable
fun PickProjectSheet(listing: ProjectListingView, onClose: () -> Unit, replace: Boolean = false) {
    val nav = LocalNav.current
    val remote = LocalRemote.current
    val dirPicker by remember(remote) { remote.coreHost.watchDirPicker(PICK_PROJECT_PICKER_KEY) }
        .collectAsState(initial = DirPickerView.getDefaultInstance())
    var browsing by remember { mutableStateOf(false) }
    Sheet(onClose, scroll = !browsing) {
        if (!browsing) {
            SheetHead(uiText(UiMessages.newSessionAndroidChooseProject()), uiText(UiMessages.commonCancel()), onClose)
            CoreProjectList(
                listing,
                onSearch = { remote.coreHost.setProjectSearch(it) },
                onNew = { remote.coreHost.openDirPicker(PICK_PROJECT_PICKER_KEY, forNewTask = true); browsing = true },
            ) { pr ->
                CoreProjectRow(
                    pr,
                    retryEnabled = false,
                    onClick = {
                        onClose()
                        if (replace) nav.back()
                        nav.projectNew(pr.key)
                    },
                ) { Arrow(pal.ink3, ts(18.sp)) }
            }
        } else {
            CoreDirPickerBody(
                dirPicker,
                onBack = { remote.coreHost.closeDirPicker(PICK_PROJECT_PICKER_KEY); browsing = false },
            ) { _, path ->
                remote.coreHost.addProject(path, PICK_PROJECT_PICKER_KEY)
                onClose()
                nav.projectNew(path)
            }
        }
    }
}

/** 新会话 · 选项目 as a page. */
@Composable
fun PickProjectScreen() {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val home by remote.homeView.collectAsState()
    PageScaffold(uiText(UiMessages.newSessionAndroidChooseProject())) {
        LazyColumn(Modifier.weight(1f).padding(horizontal = 16.dp), contentPadding = androidx.compose.foundation.layout.PaddingValues(bottom = 24.dp)) {
            item {
                CoreProjectList(
                    home.sidebarProjects,
                    onSearch = { remote.coreHost.setProjectSearch(it) },
                    onNew = { nav.newProject() },
                    card = pal.gbg,
                ) { pr ->
                    CoreProjectRow(pr, retryEnabled = false, onClick = { nav.projectNew(pr.key) }) { Arrow(pal.ink3, ts(18.sp)) }
                }
            }
        }
    }
}

/** Directory picker rendered from Core's [DirPickerView]. */
@Composable
fun CoreDirPickerBody(
    picker: DirPickerView,
    modifier: Modifier = Modifier,
    title: String = uiText(UiMessages.projectsNewChooseDirectory()),
    onBack: (() -> Unit)? = null,
    onChosen: (machine: String, path: String) -> Unit,
) {
    val remote = LocalRemote.current
    val p = pal
    Column(modifier.fillMaxWidth()) {
        if (onBack != null) SheetHead(title, null, null, onBack = onBack)
        else if (title.isNotEmpty()) Text(title, style = T.s17b, color = p.ink, modifier = Modifier.padding(bottom = 10.dp))

        if (picker.machinesCount > 1) {
            MachineSegmented(
                picker.machinesList.map { it.name },
                picker.machinesList.map { it.online },
                picker.machinesList.indexOfFirst { it.machineId == picker.machineId },
                { i ->
                    val m = picker.machinesList[i]
                    if (picker.machineId != m.machineId) {
                        remote.coreHost.navigateDirPicker(picker.pickerKey, m.machineId, "")
                    }
                },
                Modifier.padding(bottom = 10.dp),
            )
        }
        when {
            picker.machineId.isEmpty() -> Notice(uiText(UiMessages.projectsAndroidNoOnline()), tone = NoticeTone.WARN)
            picker.error.isCoreTextNotEmpty() -> Notice(
                uiText(picker.error),
                tone = NoticeTone.ERROR,
                action = uiText(UiMessages.commonRetry()),
                onAction = { remote.coreHost.navigateDirPicker(picker.pickerKey, picker.machineId, picker.currentPath) },
            )
            picker.loading && picker.crumbsCount == 0 && picker.dirsCount == 0 -> Text(
                uiText(UiMessages.projectsReadingDirectory()),
                style = T.s13,
                color = p.ink2,
                modifier = Modifier.padding(vertical = 12.dp),
            )
            else -> {
                Text(
                    buildAnnotatedString {
                        picker.crumbsList.forEachIndexed { i, crumb ->
                            if (i > 0) append(" / ")
                            val label = uiText(crumb.label)
                            if (i == picker.crumbsCount - 1) {
                                pushStyle(SpanStyle(color = p.ink, fontWeight = platformWeight(FontWeight.SemiBold)))
                                append(label)
                                pop()
                            } else {
                                append(label)
                            }
                        }
                    },
                    style = T.mono13,
                    color = p.ink2,
                    modifier = Modifier.padding(bottom = 6.dp),
                )
                if (picker.dirsCount == 0) Text(
                    uiText(UiMessages.projectsNoSubdirectories()),
                    style = T.s13,
                    color = p.ink2,
                    modifier = Modifier.padding(vertical = 8.dp),
                )
                val listState = remember(picker.currentPath) { androidx.compose.foundation.lazy.LazyListState() }
                Box(Modifier.weight(1f).fillMaxWidth().alpha(if (picker.loading) .45f else 1f)) {
                    if (picker.parentPath.isNotEmpty() || picker.dirsCount > 0) LazyColumn(
                        Modifier.scrollEdgeFades(listState).clip(Radii.R20).background(if (LocalOnSoft.current) p.raised else p.gbg),
                        state = listState,
                    ) {
                        if (picker.parentPath.isNotEmpty()) item(key = "..") {
                            ListItem(
                                uiText(UiMessages.projectsParentDirectory()),
                                last = picker.dirsCount == 0,
                                onClick = if (picker.loading) null else ({ remote.coreHost.navigateDirPicker(picker.pickerKey, picker.machineId, picker.parentPath) }),
                                icon = Lucide.ChevronLeft,
                            )
                        }
                        itemsIndexed(picker.dirsList, key = { _, entry -> entry.path }) { i, entry ->
                            ListItem(
                                entry.name,
                                last = i == picker.dirsCount - 1,
                                onClick = if (picker.loading) null else ({ remote.coreHost.navigateDirPicker(picker.pickerKey, picker.machineId, entry.path) }),
                                trailing = { Arrow(p.ink2, T.s15) },
                            )
                        }
                    }
                }
                PillButton(
                    uiText(picker.confirmLabel).ifEmpty { uiText(UiMessages.projectsUseDirectory(name = picker.currentPath)) },
                    { onChosen(picker.machineId, picker.currentPath) },
                    Modifier.fillMaxWidth().padding(top = 10.dp),
                    ButtonKind.INK,
                    enabled = picker.canConfirm && !picker.loading,
                )
            }
        }
    }
}

/** 新项目 · 选目录 as a page. */
@Composable
fun NewProjectScreen() {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val dirPicker by remember(remote) { remote.coreHost.watchDirPicker(NEW_PROJECT_PICKER_KEY) }
        .collectAsState(initial = DirPickerView.getDefaultInstance())
    LaunchedEffect(Unit) { remote.coreHost.openDirPicker(NEW_PROJECT_PICKER_KEY, forNewTask = true) }
    PageScaffold(uiText(UiMessages.projectsNewChooseDirectory())) {
        CoreDirPickerBody(
            dirPicker,
            modifier = Modifier.weight(1f).padding(horizontal = 16.dp).padding(bottom = 8.dp),
            title = "",
            onBack = null,
        ) { machine, path ->
            remote.coreHost.addProject(path, NEW_PROJECT_PICKER_KEY)
            nav.newProjectAt(machine, path)
        }
    }
}

/**
 * Folder chosen for a new project: opens New Session directly at [path] on [machine].
 */
@Composable
fun NewProjectAtScreen(machine: String, path: String) {
    val remote = LocalRemote.current
    LaunchedEffect(machine, path) {
        remote.coreHost.addProject(path)
        remote.coreHost.openNewSession(path)
        remote.coreHost.selectNewSessionMachine(machine)
        CleanupDraft.take(machine)?.let {
            val draft = ImageDrafts.forProject(path).text
            draft.value = cleanupDraftText(draft.value, it)
        }
    }
    val view by remote.newSessionView.collectAsState()
    val state by remote.state.collectAsState()
    Column(Modifier.fillMaxSize().background(pal.paper)) {
        NewSessionBox(view = view, state = state, focus = true)
    }
}

/** New-session starts per project for this run of the app: holds frozen attachments for unknown outcomes. */
object Starts {
    class Start {
        var frozen = false
        var prompt = ""
        var configKey: Triple<String, Int, String>? = null
        var imageIds: List<String> = emptyList()
            private set
        var fileIds: List<String> = emptyList()
            private set
        internal var transferOwner: Any = Any()
            private set
        private var preparedImages: List<Processed?> = emptyList()
        var taken: List<PendingImage> = emptyList()
            set(value) {
                field = value
                transferOwner = Any()
                value.forEach { it.protectTransfer(transferOwner) }
                imageIds = value.filterNot { it.isFile }.mapNotNull { it.imageId }
                fileIds = value.filter { it.isFile }.mapNotNull { it.fileId }
                preparedImages = value.map { it.processed }
            }

        fun same(prompt: String, cfgKey: Triple<String, Int, String>, images: List<PendingImage>): Boolean =
            prompt == this.prompt && cfgKey == configKey && images.map { it.id } == taken.map { it.id } &&
                images.map { it.processed } == preparedImages
    }

    private val byProject = HashMap<String, Start>()

    fun of(project: String): Start = byProject.getOrPut(project) { Start() }

    fun clear() = byProject.clear()
}

internal fun settleNewSessionStart(settled: io.github.lewin671.remoteagent.core.proto.AttachmentsSettledEffect) {
    val key = settled.projectKey
    if (key.isEmpty()) return
    val images = ImageDrafts.forProject(key)
    val st = Starts.of(key)
    val taken = st.taken
    if (settled.consumed) {
        images.detach(taken)
        taken.forEach { it.release(abort = false) }
        images.whenRestored {
            if (images.text.value.trim() == st.prompt) images.text.value = ""
        }
    } else {
        taken.forEach { it.allowAbort(st.transferOwner) }
    }
    st.taken = emptyList()
    st.frozen = false
}
