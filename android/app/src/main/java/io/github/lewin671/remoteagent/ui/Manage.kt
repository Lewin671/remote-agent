package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.listProjectName
import io.github.lewin671.remoteagent.core.proto.ProjectListingView
import io.github.lewin671.remoteagent.core.proto.ProjectRowView

/** One project row rendered directly from Core's [ProjectRowView]. */
@Composable
fun CoreProjectRow(
    pr: ProjectRowView,
    onClick: () -> Unit,
    clickLabel: String? = null,
    retryEnabled: Boolean = true,
    onRetry: (() -> Unit)? = null,
    end: @Composable () -> Unit,
) {
    val p = pal
    Row(Modifier.fillMaxWidth().heightIn(min = 52.dp), verticalAlignment = Alignment.CenterVertically) {
        Row(
            Modifier.weight(1f).heightIn(min = 52.dp).clickable(onClickLabel = clickLabel, onClick = onClick).padding(vertical = 6.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            ProjectAvatar(pr.key, 28)
            Column(Modifier.weight(1f)) {
                Text(uiText(pr.name), style = T.s17b, color = p.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Text(uiText(pr.subtitle), style = T.s13, color = p.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
                if (pr.syncing) IconText(Lucide.LoaderCircle, uiText(UiMessages.projectsAndroidSyncing()), p.ink2, T.s12)
                if (pr.saveError.isCoreTextNotEmpty()) Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(uiText(pr.saveError), style = T.s12, color = p.red, modifier = Modifier.weight(1f, fill = false))
                    if (onRetry != null) {
                        Text(
                            " · " + uiText(UiMessages.commonRetry()),
                            style = T.s12b,
                            color = p.blue,
                            modifier = Modifier.clickable(enabled = retryEnabled, onClick = onRetry),
                        )
                    }
                }
            }
        }
        end()
    }
}

/**
 * The projects list driven by Core's [ProjectListingView]: pinned then others, search when
 * [ProjectListingView.canSearch] is true, empty match notice, and New Project + optional footer.
 */
@Composable
fun CoreProjectList(
    listing: ProjectListingView,
    onSearch: (String) -> Unit,
    onNew: () -> Unit,
    card: Color = pal.raised,
    first: (@Composable () -> Unit)? = null,
    foot: (@Composable () -> Unit)? = null,
    row: @Composable (ProjectRowView) -> Unit,
) {
    val p = pal
    var search by remember { mutableStateOf(listing.searchQuery) }
    LaunchedEffect(listing.searchQuery) {
        if (listing.searchQuery.isEmpty() && search.isNotEmpty()) search = ""
    }
    @Composable
    fun Card(rows: List<@Composable () -> Unit>) = Column(
        Modifier.fillMaxWidth().padding(bottom = 12.dp).clip(Radii.R20).background(card).padding(start = 18.dp, end = 4.dp),
    ) {
        rows.forEachIndexed { i, r -> Box(if (i > 0) Modifier.topLine(p.sep) else Modifier) { r() } }
    }
    @Composable
    fun Title(text: String) = Text(text, style = T.s13, color = p.ink2, modifier = Modifier.padding(start = 12.dp, top = 2.dp, bottom = 6.dp))

    if (listing.canSearch) FilterField(search, { search = it; onSearch(it) }, Modifier.padding(bottom = 12.dp), placeholder = uiText(UiMessages.projectsSearch()))
    if (!listing.searching && first != null) Card(listOf(first))
    if (listing.titled) Title(uiText(UiMessages.projectsPinned()))
    if (listing.pinnedCount > 0) Card(listing.pinnedList.map { pr -> @Composable { row(pr) } })
    if (listing.titled) Title(uiText(UiMessages.projectsOthers()))
    if (listing.othersCount > 0) Card(listing.othersList.map { pr -> @Composable { row(pr) } })
    if (listing.emptyMatch.isCoreTextNotEmpty()) Hint(uiText(listing.emptyMatch), Modifier.padding(bottom = 12.dp))
    Card(
        listOfNotNull(
            @Composable {
                Row(
                    Modifier.fillMaxWidth().heightIn(min = 52.dp).clickable(role = androidx.compose.ui.semantics.Role.Button, onClick = onNew),
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(12.dp),
                ) {
                    Box(Modifier.width(28.dp), contentAlignment = Alignment.Center) { Ic(Lucide.Plus, 20.dp, p.blue) }
                    Text(uiText(UiMessages.projectsNew()), style = T.s17, color = p.blue)
                }
            },
            foot,
        ),
    )
}

/** Asks before a project leaves the list: there is no undo, only adding it again. */
@Composable
fun RemoveProjectConfirm(key: String, onDismiss: () -> Unit, projectName: String = "", onConfirm: () -> Unit) {
    val label = projectName.ifEmpty { uiText(listProjectName(key)) }
    Confirm(
        uiText(UiMessages.projectsRemoveTitle(name = label)),
        uiText(UiMessages.projectsRemoveExplanation()),
        uiText(UiMessages.commonRemove()),
        danger = true,
        onDismiss = onDismiss,
    ) {
        onDismiss()
        onConfirm()
    }
}

/** 项目管理: renders Core's [io.github.lewin671.remoteagent.core.proto.ManageProjectsView]. */
@Composable
fun ManageProjectsScreen() {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val projectView by remote.projectView.collectAsState()
    LaunchedEffect(Unit) { remote.coreHost.openManageProjects() }
    val manage = projectView.manage
    val p = pal
    var menu by remember { mutableStateOf<ProjectRowView?>(null) }
    var removing by remember { mutableStateOf<ProjectRowView?>(null) }
    var adding by remember { mutableStateOf(false) }

    PageScaffold(uiText(UiMessages.homeManageProjects())) {
        LazyColumn(
            Modifier.weight(1f).padding(horizontal = 16.dp).testTag("manage-projects-list"),
            contentPadding = androidx.compose.foundation.layout.PaddingValues(bottom = 24.dp),
        ) {
            if (!manage.canEdit && manage.blockedReason.isCoreTextNotEmpty()) {
                item(key = "blocked") { Notice(uiText(manage.blockedReason), Modifier.padding(bottom = 12.dp)) }
            }
            if (manage.saveError.isCoreTextNotEmpty()) {
                item(key = "save-error") { Notice(uiText(manage.saveError), Modifier.padding(bottom = 12.dp), tone = NoticeTone.ERROR) }
            }
            item(key = "list") {
                CoreProjectList(
                    manage.listing,
                    onSearch = { remote.coreHost.setProjectSearch(it) },
                    onNew = { remote.coreHost.openDirPicker(forNewTask = false); adding = true },
                    card = p.gbg,
                ) { pr ->
                    CoreProjectRow(
                        pr,
                        retryEnabled = manage.canEdit,
                        onClick = { remote.coreHost.setHomeScope(pr.key); nav.home() },
                        clickLabel = uiText(UiMessages.projectsAndroidViewHome()),
                        onRetry = {
                            if (pr.pinned) remote.coreHost.toggleProjectPin(pr.key)
                            else remote.coreHost.toggleProjectHide(pr.key)
                        },
                    ) {
                        val name = uiText(pr.name)
                        Box(
                            Modifier.size(44.dp).clickable(enabled = manage.canEdit && !pr.syncing) { menu = pr },
                            contentAlignment = Alignment.Center,
                        ) {
                            Ic(Lucide.Ellipsis, 20.dp, if (manage.canEdit) p.blue else p.ink3, contentDescription = uiText(UiMessages.projectsManageNamed(name = name)))
                        }
                    }
                }
            }
        }
    }

    menu?.let { pr ->
        ActionSheet(
            info = { SheetInfo(uiText(pr.name), pr.shortPath, mono = true) },
            groups = listOf(
                listOf(
                    SheetAction(
                        if (pr.pinned) uiText(UiMessages.projectsUnpin()) else uiText(UiMessages.projectsPin()),
                        enabled = manage.canEdit,
                        note = if (!manage.canEdit) uiText(manage.blockedReason) else "",
                    ) {
                        remote.coreHost.toggleProjectPin(pr.key)
                    },
                ),
                listOf(
                    SheetAction(uiText(UiMessages.projectsRemoveAction()), danger = true, enabled = manage.canEdit) {
                        removing = pr
                    },
                ),
            ),
            onDismiss = { menu = null },
        )
    }
    removing?.let { pr ->
        RemoveProjectConfirm(pr.key, onDismiss = { removing = null }, projectName = uiText(pr.name)) {
            remote.coreHost.toggleProjectHide(pr.key)
        }
    }
    if (adding) Sheet({ remote.coreHost.closeDirPicker(); adding = false }, scroll = false) {
        CoreDirPickerBody(
            manage.dirPicker,
            onBack = { remote.coreHost.closeDirPicker(); adding = false },
        ) { _, path ->
            remote.coreHost.addProject(path)
            adding = false
        }
    }
}
