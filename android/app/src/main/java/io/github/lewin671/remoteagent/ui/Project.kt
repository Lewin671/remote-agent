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

    val online = state.peer(machine).ready
    val machineOnline = machine != null && online && state.machines[machine]?.pathState?.reachable == true
    val blocked = uploadBlocked(machine?.let { state.machines[it] }, online, machine?.let { machineLabel(state, it) }.orEmpty())
    RefreshDraftUploads(images, remote, machine, blocked)
    val pick = rememberImagePicker(images, remote, machine, blocked, maxImages(machine?.let { state.machines[it] }), videos = filesSupported(machine?.let { state.machines[it] }))

    val uploadsReady =
        !images.restoring &&
            !images.readingFiles &&
            fileDraftBlocked(images, machine?.let { state.machines[it] }) == null &&
            (machine == null || images.images.all { it.readyFor(machine) })
    val canStart = view.canStart && uploadsReady

    fun start() {
        if (machine != null && blocked == null) images.sendAllTo(remote, machine)
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
            onFile = rememberDocumentPicker(images, remote, machine),
            plusEnabled = !images.restoring && (blocked == null || filesSupported(machine?.let { state.machines[it] }) && machineOnline),
            sendLabel = uiText(UiMessages.newSessionAndroidStart()),
            focusRequester = fr,
            hasDraft = text.isNotBlank() || images.images.isNotEmpty(),
            top = { ImageStrip(images, remote, machine, maxImages(machine?.let { state.machines[it] })) },
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
private fun NewSessionNoticeRow(
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

/**
 * 新会话 · 选项目 sheet from the new-session button on Home, driven by Core's [ProjectListingView].
 */
@Composable
fun PickProjectSheet(listing: ProjectListingView, onClose: () -> Unit, replace: Boolean = false) {
    val nav = LocalNav.current
    val remote = LocalRemote.current
    val projectView by remote.projectView.collectAsState()
    var browsing by remember { mutableStateOf(false) }
    Sheet(onClose, scroll = !browsing) {
        if (!browsing) {
            SheetHead(uiText(UiMessages.newSessionAndroidChooseProject()), uiText(UiMessages.commonCancel()), onClose)
            CoreProjectList(
                listing,
                onSearch = { remote.coreHost.setProjectSearch(it) },
                onNew = { remote.coreHost.openDirPicker(forNewTask = true); browsing = true },
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
                projectView.manage.dirPicker,
                onBack = { remote.coreHost.closeDirPicker(); browsing = false },
            ) { _, path ->
                remote.coreHost.addProject(path)
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
                        remote.coreHost.navigateDirPicker(m.machineId, "")
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
                onAction = { remote.coreHost.navigateDirPicker(picker.machineId, picker.currentPath) },
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
                                onClick = if (picker.loading) null else ({ remote.coreHost.navigateDirPicker(picker.machineId, picker.parentPath) }),
                                icon = Lucide.ChevronLeft,
                            )
                        }
                        itemsIndexed(picker.dirsList, key = { _, entry -> entry.path }) { i, entry ->
                            ListItem(
                                entry.name,
                                last = i == picker.dirsCount - 1,
                                onClick = if (picker.loading) null else ({ remote.coreHost.navigateDirPicker(picker.machineId, entry.path) }),
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
    val projectView by remote.projectView.collectAsState()
    LaunchedEffect(Unit) { remote.coreHost.openDirPicker(forNewTask = true) }
    PageScaffold(uiText(UiMessages.projectsNewChooseDirectory())) {
        CoreDirPickerBody(
            projectView.manage.dirPicker,
            modifier = Modifier.weight(1f).padding(horizontal = 16.dp).padding(bottom = 8.dp),
            title = "",
            onBack = null,
        ) { machine, path ->
            remote.coreHost.addProject(path)
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
