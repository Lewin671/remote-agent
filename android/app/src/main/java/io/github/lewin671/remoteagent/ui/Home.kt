package io.github.lewin671.remoteagent.ui

import android.content.Context
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.*
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import io.github.lewin671.remoteagent.core.HomeScopeStore
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.proto.*
import kotlinx.coroutines.delay

private const val ATTENTION_MAX = 3
private const val RUNNING_MAX = 5

/**
 * Device-local Home scope persistence storing only an HMAC token ("all" or "project:<hmac>")
 * in SharedPreferences, never plaintext project paths.
 */
object HomeScopes {
    private fun store(context: Context) =
        context.applicationContext.getSharedPreferences("home-scope", Context.MODE_PRIVATE)

    fun storeAdapter(context: Context): HomeScopeStore = object : HomeScopeStore {
        override fun readToken(): String? = store(context).getString("scope", null)
        override fun writeToken(token: String) {
            store(context).edit().putString("scope", token).apply()
        }
        override fun clear() {
            store(context).edit().clear().apply()
        }
    }

    /** Forgotten with the account: the mark is one of its projects'. */
    fun clear(context: Context) {
        store(context).edit().clear().apply()
    }
}

/**
 * 主页 (client-v58 #home): renders Core's [HomeViewModel] directly.
 */
@Composable
fun HomeScreen() {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val vm by remote.homeView.collectAsState()
    val state by remote.state.collectAsState()
    var filterText by remember(vm.scopeKey) { mutableStateOf(vm.filterQuery) }
    LaunchedEffect(vm.filterQuery) {
        if (vm.filterQuery.isEmpty() && filterText.isNotEmpty()) filterText = ""
    }
    val wide = LocalConfiguration.current.screenWidthDp >= 600
    var allAttention by remember(vm.scopeKey) { mutableStateOf(false) }
    var allRunning by remember(vm.scopeKey, vm.filterQuery) { mutableStateOf(false) }

    val showCard = vm.topAttentionActionable && vm.hasTopAttentionCard() && vm.topAttentionCard.approvalId.isNotEmpty()
    if (vm.topAttentionActionable && !showCard && vm.topAttentionSessionId.isNotEmpty()) {
        // Hydrate approval details for the top attention item when only metadata was available.
        remote.watchSession(vm.topAttentionSessionId).collectAsState(
            initial = io.github.lewin671.remoteagent.core.proto.SessionViewModel.getDefaultInstance(),
        )
    }
    val attentionRows =
        if (showCard && vm.attentionList.firstOrNull()?.sessionId == vm.topAttentionSessionId) {
            vm.attentionList.drop(1)
        } else {
            vm.attentionList
        }
    val visibleAttention =
        if (wide || allAttention || attentionRows.size <= ATTENTION_MAX) attentionRows
        else attentionRows.take(ATTENTION_MAX)
    val hiddenAttentionCount = attentionRows.size - visibleAttention.size

    val visibleRunning =
        if (wide || allRunning || vm.runningList.size <= RUNNING_MAX) vm.runningList
        else vm.runningList.take(RUNNING_MAX)
    val hiddenRunningCount = vm.runningList.size - visibleRunning.size

    val hasRows = showCard || visibleAttention.isNotEmpty() || visibleRunning.isNotEmpty() || vm.endedGroupsCount > 0 || vm.hasEndedTail()
    var calm by remember { mutableStateOf(false) }
    LaunchedEffect(vm.scopeKey, vm.filterQuery, hasRows) {
        calm = false
        if (hasRows) {
            delay(Motion.SLOW + 120L)
            calm = true
        }
    }

    var pick by rememberSaveable { mutableStateOf(false) }
    var menu by rememberSaveable { mutableStateOf(false) }
    var details by remember { mutableStateOf(false) }
    var updating by remember { mutableStateOf(false) }

    val hasHeadline = vm.hasHeadline() && (vm.headline.kind != HeadlineKind.HEADLINE_KIND_UNSPECIFIED || vm.headline.title.isCoreTextNotEmpty())

    ShellPage(
        Modifier.fillMaxSize(),
        top = {
            Box(Modifier.weight(1f)) {
                if (vm.scopeSelectable) {
                    ScopeCapsule(vm.scopeKey, uiText(vm.scopeTitle)) { menu = true }
                }
            }
            GlassButton(Lucide.Settings, uiText(UiMessages.commonSettings()), nav.settings)
        },
        bottom = {
            ShellBottom(
                ShellTab.SESSIONS, uiText(UiMessages.homeNewTask()), Lucide.SquarePen, enabled = vm.canStart,
                onAction = { if (vm.scopeKey.isNotEmpty()) nav.projectNew(vm.scopeKey) else pick = true },
            )
        },
    ) {
        LazyColumn(
            Modifier.fillMaxSize().align(Alignment.TopCenter).widthIn(max = 760.dp).testTag("home-list"),
            contentPadding = shellPadding(),
        ) {
            item(key = "headline") {
                Column {
                    if (hasHeadline) Headline(vm.headline) { details = true }
                    UpdateNotice(Modifier.padding(start = 16.dp, end = 16.dp, top = 10.dp)) { updating = true }
                    if (vm.canFilter) FilterField(
                        filterText,
                        { filterText = it; remote.coreHost.setHomeFilter(it) },
                        Modifier.padding(start = 16.dp, end = 16.dp, top = 18.dp),
                    )
                }
            }
            item(key = "disk-reminders") {
                val settingsView by LocalRemote.current.settingsView.collectAsState()
                DiskReminderCards(settingsView.diskRemindersList)
            }
            val emptyKind = if (vm.hasEmpty()) vm.empty.kind else HomeEmptyKind.HOME_EMPTY_KIND_UNSPECIFIED
            if (!hasHeadline && emptyKind == HomeEmptyKind.HOME_EMPTY_KIND_NONE && !hasRows) {
                item(key = "loading") { Skeleton() }
            } else if (emptyKind != HomeEmptyKind.HOME_EMPTY_KIND_UNSPECIFIED && emptyKind != HomeEmptyKind.HOME_EMPTY_KIND_NONE) {
                item(key = "empty") { EmptyHomeView(vm.empty, onPair = nav.pair, onRetry = { remote.coreHost.retryHomeLoad() }) }
            }

            if (showCard) {
                item(key = "card:${vm.topAttentionCard.approvalId}") {
                    val anim = if (calm) Modifier.animateItem(Motion.fadeInSpec(), Motion.enter(Motion.BASE), Motion.fadeOutSpec()) else Modifier
                    Box(anim) {
                        androidx.compose.runtime.key(vm.topAttentionCard.approvalId) {
                            ApprovalCard(
                                vm.topAttentionCard,
                                state,
                                onOpen = { nav.session(vm.topAttentionCard.sessionId) },
                                modifier = Modifier.padding(start = 16.dp, end = 16.dp, top = 20.dp),
                            )
                        }
                    }
                }
            }
            itemsIndexed(visibleAttention, key = { _, r -> "att:${r.sessionId}" }) { idx, row ->
                val first = !showCard && idx == 0
                val last = idx == visibleAttention.lastIndex && hiddenAttentionCount == 0
                val anim = if (calm) Modifier.animateItem(Motion.fadeInSpec(), Motion.enter(Motion.BASE), Motion.fadeOutSpec()) else Modifier
                Box(anim) {
                    PendingRow(row, first = first, last = last) { nav.session(row.sessionId) }
                }
            }
            if (hiddenAttentionCount > 0) {
                item(key = "more:attention") {
                    val anim = if (calm) Modifier.animateItem(Motion.fadeInSpec(), Motion.enter(Motion.BASE), Motion.fadeOutSpec()) else Modifier
                    Box(anim) {
                        MoreRow(uiText(UiMessages.homeMoreWaiting(count = hiddenAttentionCount))) { allAttention = true }
                    }
                }
            }

            if (visibleRunning.isNotEmpty()) {
                item(key = "label:running") {
                    val anim = if (calm) Modifier.animateItem(Motion.fadeInSpec(), Motion.enter(Motion.BASE), Motion.fadeOutSpec()) else Modifier
                    Box(anim) { GroupTitle(uiText(UiMessages.homeLive()), date = false) }
                }
                itemsIndexed(visibleRunning, key = { _, r -> "run:${r.sessionId}" }) { idx, row ->
                    val anim = if (calm) Modifier.animateItem(Motion.fadeInSpec(), Motion.enter(Motion.BASE), Motion.fadeOutSpec()) else Modifier
                    Box(anim) {
                        SessionRow(row, last = idx == visibleRunning.lastIndex) {
                            remote.coreHost.selectHomeSession(row.sessionId)
                            nav.session(row.sessionId)
                        }
                    }
                }
                if (hiddenRunningCount > 0) {
                    item(key = "more:running") {
                        val anim = if (calm) Modifier.animateItem(Motion.fadeInSpec(), Motion.enter(Motion.BASE), Motion.fadeOutSpec()) else Modifier
                        Box(anim) {
                            ListMore(uiText(UiMessages.homeMoreLive(count = hiddenRunningCount)), { allRunning = true })
                        }
                    }
                }
            }

            for (group in vm.endedGroupsList) {
                item(key = "label:${group.groupKey}") {
                    val anim = if (calm) Modifier.animateItem(Motion.fadeInSpec(), Motion.enter(Motion.BASE), Motion.fadeOutSpec()) else Modifier
                    Box(anim) { GroupTitle(uiText(group.label), date = true) }
                }
                itemsIndexed(group.itemsList, key = { _, r -> "end:${r.sessionId}" }) { idx, row ->
                    val anim = if (calm) Modifier.animateItem(Motion.fadeInSpec(), Motion.enter(Motion.BASE), Motion.fadeOutSpec()) else Modifier
                    Box(anim) {
                        EndedRow(row, last = idx == group.itemsList.lastIndex) {
                            remote.coreHost.selectHomeSession(row.sessionId)
                            nav.session(row.sessionId)
                        }
                    }
                }
            }

            if (vm.hasEndedTail() && vm.endedTail.text.isCoreTextNotEmpty()) {
                item(key = "tail") {
                    val tail = vm.endedTail
                    if (tail.kind == TailKind.TAIL_KIND_MORE_ON_SERVER) {
                        LaunchedEffect(vm.endedTotal, vm.endedGroupsCount) {
                            remote.coreHost.loadMoreHomeEnded()
                        }
                    }
                    val anim = if (calm) Modifier.animateItem(Motion.fadeInSpec(), Motion.enter(Motion.BASE), Motion.fadeOutSpec()) else Modifier
                    Box(anim) {
                        ListMore(
                            uiText(tail.text),
                            if (tail.retry) ({ remote.coreHost.retryHomeLoad() }) else null,
                            Modifier.testTag("timeline-tail"),
                        )
                    }
                }
            }
        }
    }

    if (updating) UpdateSheet { updating = false }
    if (details) ServerDetails(state) { details = false }
    if (pick) PickProjectSheet(vm.sidebarProjects, onClose = { pick = false })
    if (menu) ScopeSheet(vm, onPick = { remote.coreHost.setHomeScope(it) }) { menu = false }
}

/**
 * The sentence at the top (design-system §6 大标题): 34 / 700, count in orange, and subtitle.
 */
@Composable
internal fun Headline(h: HomeHeadlineView, onDetails: () -> Unit) {
    val p = pal
    val sub = uiText(h.sub)
    Text(
        uiHighlightedText(h.title, h.highlightArg, SpanStyle(color = p.orange)),
        style = T.title, color = p.ink,
        modifier = Modifier.fillMaxWidth().padding(start = 24.dp, end = 24.dp, top = 6.dp).semantics { heading() }.testTag("headline"),
    )
    Row(
        Modifier.fillMaxWidth().padding(start = 24.dp, end = 24.dp, top = 7.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            sub.ifEmpty { uiText(UiMessages.connectionPreviousBelow()) },
            minLines = 1,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
            style = if (h.subPath) T.mono13.copy(fontSize = 15.sp, lineHeight = 21.sp) else T.s17,
            color = if (h.subError) p.red else p.ink2,
            modifier = Modifier.weight(1f, fill = false).then(
                if (sub.isEmpty()) Modifier.alpha(0f).clearAndSetSemantics {} else Modifier,
            ),
        )
        if (h.detailsAction) Box(
            Modifier.heightIn(min = 44.dp).clickable(role = Role.Button, onClick = onDetails).padding(start = 12.dp, end = 4.dp),
            contentAlignment = Alignment.Center,
        ) { Text(uiText(UiMessages.commonDetails()), style = T.s17, color = p.blue) }
    }
}

@Composable
private fun EmptyNote(text: String) {
    Text(text, style = T.s17.copy(lineHeight = 24.sp), color = pal.ink2, modifier = Modifier.padding(start = 24.dp, end = 24.dp, top = 14.dp))
}

@Composable
internal fun EmptyHomeView(
    empty: HomeEmptyView,
    onPair: () -> Unit,
    onRetry: () -> Unit,
) {
    val msg = uiText(empty.message)
    when (empty.kind) {
        HomeEmptyKind.HOME_EMPTY_KIND_NO_MACHINES -> Column {
            if (msg.isNotEmpty()) EmptyNote(msg)
            if (empty.canAddDevice) PillButton(uiText(UiMessages.homeAddDevice()), onPair, Modifier.padding(start = 24.dp, top = 20.dp), ButtonKind.INK, icon = Lucide.Plus)
        }
        HomeEmptyKind.HOME_EMPTY_KIND_LIST_FAILED -> Column {
            if (msg.isNotEmpty()) EmptyNote(msg)
            if (empty.canRetry) PillButton(uiText(UiMessages.commonRetry()), onRetry, Modifier.padding(start = 24.dp, top = 20.dp), ButtonKind.INK)
        }
        else -> if (msg.isNotEmpty()) EmptyNote(msg)
    }
}

@Composable
private fun ScopeCapsule(scopeKey: String, title: String, modifier: Modifier = Modifier, onOpen: () -> Unit) {
    val p = pal
    GlassCapsule(onOpen, modifier.testTag("scope-title"), onClickLabel = uiText(UiMessages.homeAndroidChangeScope())) {
        ProjectAvatar(scopeKey, 28, all = scopeKey.isEmpty())
        Spacer(Modifier.size(7.dp))
        Text(title, style = T.s15b, color = p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
        Spacer(Modifier.size(4.dp))
        Ic(Lucide.ChevronDown, 15.dp, p.ink3)
    }
}

@Composable
private fun ScopeSheet(vm: HomeViewModel, onPick: (String) -> Unit, onClose: () -> Unit) {
    val p = pal
    val nav = LocalNav.current
    val remote = LocalRemote.current
    val projectView by remote.projectView.collectAsState()
    var browsing by remember { mutableStateOf(false) }
    Sheet(onClose, scroll = !browsing) {
        if (browsing) {
            CoreDirPickerBody(
                projectView.manage.dirPicker,
                onBack = { remote.coreHost.closeDirPicker(); browsing = false },
                onChosen = { _, path ->
                    remote.coreHost.addProject(path)
                    browsing = false
                    onClose()
                    nav.projectNew(path)
                },
            )
            return@Sheet
        }
        Box(Modifier.fillMaxWidth().padding(bottom = 10.dp)) {
            Text(uiText(UiMessages.homeChooseScope()), style = T.s17b, color = p.ink, textAlign = TextAlign.Center, modifier = Modifier.align(Alignment.Center))
            Box(Modifier.align(Alignment.CenterEnd).heightIn(min = 44.dp).clickable(role = Role.Button, onClick = onClose), contentAlignment = Alignment.Center) {
                Text(uiText(UiMessages.commonCancel()), style = T.s17, color = p.blue)
            }
        }
        @Composable
        fun check(on: Boolean) {
            val currentScope = uiText(UiMessages.homeAndroidCurrentScope())
            Box(Modifier.size(44.dp).semantics { if (on) contentDescription = currentScope }, contentAlignment = Alignment.Center) {
                if (on) Ic(Lucide.Check, 18.dp, p.blue)
            }
        }
        val allRow = vm.scopeMenuList.firstOrNull { it.allProjects }
        CoreProjectList(
            vm.sidebarProjects,
            onSearch = { remote.coreHost.setProjectSearch(it) },
            onNew = { remote.coreHost.openDirPicker(forNewTask = false); browsing = true },
            first = allRow?.let { all ->
                @Composable {
                    Row(
                        Modifier.fillMaxWidth().heightIn(min = 52.dp).clickable(role = Role.Button) {
                            onPick("")
                            onClose()
                        }.padding(vertical = 6.dp),
                        verticalAlignment = Alignment.CenterVertically,
                        horizontalArrangement = Arrangement.spacedBy(12.dp),
                    ) {
                        ProjectAvatar("", 28, all = true)
                        Column(Modifier.weight(1f)) {
                            Text(uiText(all.name), style = T.s17b, color = p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                            Text(uiText(all.subtitle), style = T.s13, color = p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        }
                        check(all.checked)
                    }
                }
            },
            foot = {
                Row(
                    Modifier.fillMaxWidth().heightIn(min = 50.dp)
                        .clickable(role = Role.Button) {
                            onClose()
                            nav.manageProjects()
                        }.padding(end = 12.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Text(uiText(UiMessages.homeManageProjects()), style = T.s17, color = p.ink, modifier = Modifier.weight(1f))
                    Ic(Lucide.ChevronRight, 18.dp, p.ink3)
                }
            },
        ) { pr ->
            CoreProjectRow(
                pr,
                retryEnabled = false,
                onClick = { onPick(pr.key); onClose() },
            ) { check(vm.scopeKey == pr.key) }
        }
    }
}
