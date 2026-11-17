package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.proto.AlwaysAllowKind
import io.github.lewin671.remoteagent.core.proto.AttentionCardView
import io.github.lewin671.remoteagent.core.proto.AttentionRowView
import io.github.lewin671.remoteagent.core.proto.AutoIssueKind
import io.github.lewin671.remoteagent.core.proto.ConnState
import io.github.lewin671.remoteagent.core.proto.ConnectionStatusView
import io.github.lewin671.remoteagent.core.proto.DefaultModeInfo
import io.github.lewin671.remoteagent.core.proto.DirCrumbView
import io.github.lewin671.remoteagent.core.proto.DirEntryView
import io.github.lewin671.remoteagent.core.proto.DirPickerMachineView
import io.github.lewin671.remoteagent.core.proto.DirPickerView
import io.github.lewin671.remoteagent.core.proto.EndedGroupKind
import io.github.lewin671.remoteagent.core.proto.EndedGroupView
import io.github.lewin671.remoteagent.core.proto.EndedRowView
import io.github.lewin671.remoteagent.core.proto.HeadlineKind
import io.github.lewin671.remoteagent.core.proto.HiddenProjectNoticeView
import io.github.lewin671.remoteagent.core.proto.HomeEmptyKind
import io.github.lewin671.remoteagent.core.proto.HomeEmptyView
import io.github.lewin671.remoteagent.core.proto.HomeHeadlineView
import io.github.lewin671.remoteagent.core.proto.HomeViewModel
import io.github.lewin671.remoteagent.core.proto.MachineChoiceView
import io.github.lewin671.remoteagent.core.proto.ManageProjectsView
import io.github.lewin671.remoteagent.core.proto.ModelOptionView
import io.github.lewin671.remoteagent.core.proto.NewSessionConfig
import io.github.lewin671.remoteagent.core.proto.NewSessionModelPanelView
import io.github.lewin671.remoteagent.core.proto.NewSessionNoticeView
import io.github.lewin671.remoteagent.core.proto.NewSessionViewModel
import io.github.lewin671.remoteagent.core.proto.NoticeActionKind
import io.github.lewin671.remoteagent.core.proto.NoticeKind
import io.github.lewin671.remoteagent.core.proto.NoticeSeverity
import io.github.lewin671.remoteagent.core.proto.OfflineMachineChoiceView
import io.github.lewin671.remoteagent.core.proto.ProjectDetailView
import io.github.lewin671.remoteagent.core.proto.ProjectListingView
import io.github.lewin671.remoteagent.core.proto.ProjectRowView
import io.github.lewin671.remoteagent.core.proto.ProjectViewKind
import io.github.lewin671.remoteagent.core.proto.ProjectViewModel
import io.github.lewin671.remoteagent.core.proto.ScopeRowView
import io.github.lewin671.remoteagent.core.proto.SessionDisplayStatus
import io.github.lewin671.remoteagent.core.proto.SessionRowView
import io.github.lewin671.remoteagent.core.proto.SessionTone
import io.github.lewin671.remoteagent.core.proto.TailKind
import io.github.lewin671.remoteagent.core.proto.TailRowView
import io.github.lewin671.remoteagent.core.proto.Text
import io.github.lewin671.remoteagent.core.proto.TextArg
import io.github.lewin671.remoteagent.core.proto.TimestampArg
import io.github.lewin671.remoteagent.core.proto.TimestampStyle
import io.github.lewin671.remoteagent.core.proto.AgentOptionView
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ApprovalMode
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.doubleOrNull
import kotlinx.serialization.json.intOrNull
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.longOrNull
import java.io.File

/**
 * Parses `testdata/screens/<scenario>.golden.json` named view snapshots into the
 * generated lite protobuf ViewModels so Robolectric UI tests render directly from Core goldens.
 */
object ScreenGoldenLoader {
    private val json = Json { ignoreUnknownKeys = true }

    private fun dir(): File {
        val prop = System.getProperty("screenGoldens")
        val f = if (!prop.isNullOrEmpty()) File(prop) else File("../testdata/screens")
        check(f.isDirectory) { "screenGoldens directory not found: ${f.absolutePath}" }
        return f
    }

    fun namedViews(scenario: String): Map<String, JsonObject> {
        val root = json.parseToJsonElement(File(dir(), "$scenario.golden.json").readText()).jsonObject
        val out = LinkedHashMap<String, JsonObject>()
        for (step in root["steps"]?.jsonArray.orEmpty()) {
            val views = step.jsonObject["views"] as? JsonObject ?: continue
            for ((k, v) in views) out[k] = v.jsonObject
        }
        return out
    }

    fun homeView(scenario: String, viewName: String): HomeViewModel {
        val v = namedViews(scenario).getValue(viewName)
        return parseHome(v["home"]!!.jsonObject)
    }

    fun projectView(scenario: String, viewName: String): ProjectViewModel {
        val v = namedViews(scenario).getValue(viewName)
        return parseProject(v["project"]!!.jsonObject)
    }

    fun newSessionView(scenario: String, viewName: String): NewSessionViewModel {
        val v = namedViews(scenario).getValue(viewName)
        return parseNewSession(v["newSession"]!!.jsonObject)
    }

    private fun JsonObject.str(k: String): String = (this[k]?.jsonPrimitive)?.contentOrNull.orEmpty()
    private fun JsonObject.bool(k: String): Boolean = (this[k]?.jsonPrimitive)?.booleanOrNull == true
    private fun JsonObject.i32(k: String): Int = (this[k]?.jsonPrimitive)?.intOrNull ?: 0
    private fun JsonObject.i64(k: String): Long = (this[k]?.jsonPrimitive)?.let { it.longOrNull ?: it.contentOrNull?.toLongOrNull() } ?: 0L
    private fun JsonObject.f64(k: String): Double = (this[k]?.jsonPrimitive)?.doubleOrNull ?: 0.0
    private fun JsonObject.obj(k: String): JsonObject? = this[k] as? JsonObject
    private fun JsonObject.arr(k: String): JsonArray = (this[k] as? JsonArray) ?: JsonArray(emptyList())
    private inline fun <reified E : Enum<E>> JsonObject.enum(k: String, default: E): E =
        str(k).takeIf { it.isNotEmpty() }?.let { name -> enumValues<E>().firstOrNull { it.name == name } } ?: default

    private fun parseTimestampArg(o: JsonObject): TimestampArg = TimestampArg.newBuilder()
        .setUnixMs(o.i64("unixMs"))
        .setStyle(o.enum("style", TimestampStyle.TIMESTAMP_STYLE_UNSPECIFIED))
        .build()

    private fun parseText(o: JsonObject?): Text {
        if (o == null) return Text.getDefaultInstance()
        return Text.newBuilder().apply {
            setKey(o.str("key"))
            setLiteral(o.str("literal"))
            setSeparatorKey(o.str("separatorKey"))
            setSeparatorLiteral(o.str("separatorLiteral"))
            o.obj("timestamp")?.let { setTimestamp(parseTimestampArg(it)) }
            o.arr("args").forEach { addArgs(parseArg(it.jsonObject)) }
            o.arr("join").forEach { addJoin(parseText(it.jsonObject)) }
        }.build()
    }

    private fun parseArg(o: JsonObject): TextArg = TextArg.newBuilder().apply {
        setName(o.str("name"))
        when {
            "strValue" in o -> setStrValue(o.str("strValue"))
            "intValue" in o -> setIntValue(o.i64("intValue"))
            "floatValue" in o -> setFloatValue(o.f64("floatValue"))
            "textValue" in o -> setTextValue(parseText(o.obj("textValue")))
            "timestampValue" in o -> setTimestampValue(parseTimestampArg(o.obj("timestampValue")!!))
        }
    }.build()

    private fun parseHome(o: JsonObject): HomeViewModel = HomeViewModel.newBuilder().apply {
        setScopeSelectable(o.bool("scopeSelectable"))
        setScopeKey(o.str("scopeKey"))
        o.obj("scopeTitle")?.let { setScopeTitle(parseText(it)) }
        setScopeShortPath(o.str("scopeShortPath"))
        o.obj("connection")?.let { c ->
            setConnection(
                ConnectionStatusView.newBuilder()
                    .setState(c.enum("state", ConnState.CONN_STATE_UNSPECIFIED))
                    .setBadgeText(parseText(c.obj("badgeText")))
                    .setDetailText(parseText(c.obj("detailText")))
                    .setLivePushConnected(c.bool("livePushConnected"))
                    .setReason(c.str("reason"))
                    .setDisconnectedAtMs(c.i64("disconnectedAtMs")),
            )
        }
        o.obj("headline")?.let { h ->
            setHeadline(
                HomeHeadlineView.newBuilder()
                    .setKind(h.enum("kind", HeadlineKind.HEADLINE_KIND_UNSPECIFIED))
                    .setTitle(parseText(h.obj("title")))
                    .setHighlightCount(h.i32("highlightCount"))
                    .setSub(parseText(h.obj("sub")))
                    .setSubError(h.bool("subError"))
                    .setSubPath(h.bool("subPath"))
                    .setDetailsAction(h.bool("detailsAction"))
                    .setUnlinked(h.bool("unlinked")),
            )
        }
        o.obj("empty")?.let { e ->
            setEmpty(
                HomeEmptyView.newBuilder()
                    .setKind(e.enum("kind", HomeEmptyKind.HOME_EMPTY_KIND_UNSPECIFIED))
                    .setMessage(parseText(e.obj("message")))
                    .setCommand(e.str("command"))
                    .setReadyMachineId(e.str("readyMachineId"))
                    .setReadyMachineName(e.str("readyMachineName"))
                    .setCanAddDevice(e.bool("canAddDevice"))
                    .setCanStartFirst(e.bool("canStartFirst"))
                    .setCanRetry(e.bool("canRetry")),
            )
        }
        setCanFilter(o.bool("canFilter"))
        setFilterQuery(o.str("filterQuery"))
        o.obj("filterSummary")?.let { setFilterSummary(parseText(it)) }
        setNoMatches(o.bool("noMatches"))
        setWaitingTotal(o.i32("waitingTotal"))
        setLiveTotal(o.i32("liveTotal"))
        setEndedTotal(o.i32("endedTotal"))
        setTimelineTotal(o.i32("timelineTotal"))
        setTopAttentionSessionId(o.str("topAttentionSessionId"))
        setTopAttentionActionable(o.bool("topAttentionActionable"))
        o.obj("topAttentionCard")?.let { c ->
            setTopAttentionCard(
                AttentionCardView.newBuilder()
                    .setSessionId(c.str("sessionId"))
                    .setMachineId(c.str("machineId"))
                    .setApprovalId(c.str("approvalId"))
                    .setTitle(parseText(c.obj("title")))
                    .setKindText(parseText(c.obj("kindText")))
                    .setWhere(parseText(c.obj("where")))
                    .setTime(parseText(c.obj("time")))
                    .setHot(c.bool("hot"))
                    .setBody(c.str("body"))
                    .setToolName(c.str("toolName"))
                    .setIsCommand(c.bool("isCommand"))
                    .setTruncated(c.bool("truncated"))
                    .setAlwaysKind(c.enum("alwaysKind", AlwaysAllowKind.ALWAYS_ALLOW_KIND_UNSPECIFIED))
                    .setAlwaysArg(c.str("alwaysArg"))
                    .setAlwaysLabel(parseText(c.obj("alwaysLabel"))),
            )
        }
        o.arr("attention").forEach { e ->
            val a = e.jsonObject
            addAttention(
                AttentionRowView.newBuilder()
                    .setSessionId(a.str("sessionId"))
                    .setMachineId(a.str("machineId"))
                    .setTitle(parseText(a.obj("title")))
                    .setKindText(parseText(a.obj("kindText")))
                    .setSummary(parseText(a.obj("summary")))
                    .setOutsideScope(a.bool("outsideScope")),
            )
        }
        o.arr("running").forEach { addRunning(parseSessionRow(it.jsonObject)) }
        o.arr("endedGroups").forEach { eg ->
            val g = eg.jsonObject
            addEndedGroups(
                EndedGroupView.newBuilder()
                    .setGroupKey(g.str("groupKey"))
                    .setGroupKind(g.enum("groupKind", EndedGroupKind.ENDED_GROUP_KIND_UNSPECIFIED))
                    .setLabel(parseText(g.obj("label")))
                    .addAllItems(g.arr("items").map { parseEndedRow(it.jsonObject) }),
            )
        }
        o.obj("endedTail")?.let { setEndedTail(parseTail(it)) }
        o.arr("scopeMenu").forEach { sm ->
            val s = sm.jsonObject
            addScopeMenu(
                ScopeRowView.newBuilder()
                    .setKey(s.str("key"))
                    .setName(parseText(s.obj("name")))
                    .setShortPath(s.str("shortPath"))
                    .setSubtitle(parseText(s.obj("subtitle")))
                    .setWaitingCount(s.i32("waitingCount"))
                    .setLiveCount(s.i32("liveCount"))
                    .setAllOffline(s.bool("allOffline"))
                    .setChecked(s.bool("checked"))
                    .setAllProjects(s.bool("allProjects")),
            )
        }
        o.obj("sidebarProjects")?.let { setSidebarProjects(parseListing(it)) }
        setCanStart(o.bool("canStart"))
        setSelectedSessionId(o.str("selectedSessionId"))
        setHasMoreEnded(o.bool("hasMoreEnded"))
    }.build()

    private fun parseSessionRow(r: JsonObject): SessionRowView = SessionRowView.newBuilder()
        .setSessionId(r.str("sessionId"))
        .setMachineId(r.str("machineId"))
        .setTitle(parseText(r.obj("title")))
        .setTime(parseText(r.obj("time")))
        .setStatus(r.enum("status", SessionDisplayStatus.SESSION_DISPLAY_STATUS_UNSPECIFIED))
        .setTone(r.enum("tone", SessionTone.SESSION_TONE_UNSPECIFIED))
        .setStatusText(parseText(r.obj("statusText")))
        .setPreview(parseText(r.obj("preview")))
        .setSecondLine(parseText(r.obj("secondLine")))
        .setAgent(r.enum("agent", Agent.AGENT_UNSPECIFIED))
        .setMultipleAgents(r.bool("multipleAgents"))
        .setMetaLine(parseText(r.obj("metaLine")))
        .setSelected(r.bool("selected"))
        .setPendingAction(r.bool("pendingAction"))
        .build()

    private fun parseEndedRow(r: JsonObject): EndedRowView = EndedRowView.newBuilder()
        .setSessionId(r.str("sessionId"))
        .setMachineId(r.str("machineId"))
        .setTitle(parseText(r.obj("title")))
        .setRight(parseText(r.obj("right")))
        .setSelected(r.bool("selected"))
        .build()

    private fun parseTail(t: JsonObject): TailRowView = TailRowView.newBuilder()
        .setKind(t.enum("kind", TailKind.TAIL_KIND_UNSPECIFIED))
        .setText(parseText(t.obj("text")))
        .setRetry(t.bool("retry"))
        .build()

    private fun parseProjectRow(r: JsonObject): ProjectRowView = ProjectRowView.newBuilder()
        .setKey(r.str("key"))
        .setName(parseText(r.obj("name")))
        .setShortPath(r.str("shortPath"))
        .setSubtitle(parseText(r.obj("subtitle")))
        .setWaitingCount(r.i32("waitingCount"))
        .setLiveCount(r.i32("liveCount"))
        .setAllOffline(r.bool("allOffline"))
        .setPinned(r.bool("pinned"))
        .setHidden(r.bool("hidden"))
        .setSyncing(r.bool("syncing"))
        .setSaveError(parseText(r.obj("saveError")))
        .build()

    private fun parseListing(l: JsonObject): ProjectListingView = ProjectListingView.newBuilder().apply {
        l.arr("pinned").forEach { addPinned(parseProjectRow(it.jsonObject)) }
        l.arr("others").forEach { addOthers(parseProjectRow(it.jsonObject)) }
        setTitled(l.bool("titled"))
        setCanSearch(l.bool("canSearch"))
        setSearching(l.bool("searching"))
        setSearchQuery(l.str("searchQuery"))
        l.obj("emptyMatch")?.let { setEmptyMatch(parseText(it)) }
        setWaitingTotal(l.i32("waitingTotal"))
        setLoading(l.bool("loading"))
        setSyncing(l.bool("syncing"))
        setLoadFailed(l.bool("loadFailed"))
    }.build()

    private fun parseDirPicker(d: JsonObject): DirPickerView = DirPickerView.newBuilder().apply {
        setOpen(d.bool("open"))
        setForNewTask(d.bool("forNewTask"))
        d.arr("machines").forEach { e ->
            val m = e.jsonObject
            addMachines(
                DirPickerMachineView.newBuilder()
                    .setMachineId(m.str("machineId"))
                    .setName(m.str("name"))
                    .setOnline(m.bool("online"))
                    .setStatusText(parseText(m.obj("statusText")))
                    .setHome(m.str("home")),
            )
        }
        setMachineId(d.str("machineId"))
        setCurrentPath(d.str("currentPath"))
        d.arr("crumbs").forEach { e ->
            val c = e.jsonObject
            addCrumbs(DirCrumbView.newBuilder().setLabel(parseText(c.obj("label"))).setPath(c.str("path")))
        }
        setParentPath(d.str("parentPath"))
        d.arr("dirs").forEach { e ->
            val de = e.jsonObject
            addDirs(DirEntryView.newBuilder().setName(de.str("name")).setPath(de.str("path")))
        }
        setLoading(d.bool("loading"))
        d.obj("error")?.let { setError(parseText(it)) }
        d.obj("confirmLabel")?.let { setConfirmLabel(parseText(it)) }
        setCanConfirm(d.bool("canConfirm"))
    }.build()

    private fun parseProject(o: JsonObject): ProjectViewModel = ProjectViewModel.newBuilder().apply {
        setKind(o.enum("kind", ProjectViewKind.PROJECT_VIEW_KIND_UNSPECIFIED))
        o.obj("manage")?.let { m ->
            setManage(
                ManageProjectsView.newBuilder().apply {
                    m.obj("listing")?.let { setListing(parseListing(it)) }
                    setCanEdit(m.bool("canEdit"))
                    m.obj("blockedReason")?.let { setBlockedReason(parseText(it)) }
                    setChangedElsewhere(m.bool("changedElsewhere"))
                    setSyncing(m.bool("syncing"))
                    m.obj("saveError")?.let { setSaveError(parseText(it)) }
                    m.obj("dirPicker")?.let { setDirPicker(parseDirPicker(it)) }
                    setProjectCount(m.i32("projectCount"))
                },
            )
        }
        o.obj("detail")?.let { d ->
            setDetail(
                ProjectDetailView.newBuilder().apply {
                    setKey(d.str("key"))
                    d.obj("name")?.let { setName(parseText(it)) }
                    setShortPath(d.str("shortPath"))
                    d.obj("subtitle")?.let { setSubtitle(parseText(it)) }
                    setAnyOnline(d.bool("anyOnline"))
                    setActiveCount8(d.i32("activeCount"))
                    setEndedTotal(d.i32("endedTotal"))
                    d.arr("active").forEach { addActive10(parseSessionRow(it.jsonObject)) }
                    d.arr("ended").forEach { addEnded(parseEndedRow(it.jsonObject)) }
                    setCanLoadEarlier(d.bool("canLoadEarlier"))
                    d.obj("tail")?.let { setTail(parseTail(it)) }
                    d.obj("emptyNote")?.let { setEmptyNote(parseText(it)) }
                    setIsHidden(d.bool("isHidden"))
                    setIsPinned(d.bool("isPinned"))
                    setCanStart(d.bool("canStart"))
                    setCanPin(d.bool("canPin"))
                    setCanRemove(d.bool("canRemove"))
                    setHasMoreEnded(d.bool("hasMoreEnded"))
                },
            )
        }
    }.build()

    private fun parseConfig(o: JsonObject?): NewSessionConfig {
        if (o == null) return NewSessionConfig.getDefaultInstance()
        return NewSessionConfig.newBuilder()
            .setMachineId(o.str("machineId"))
            .setAgent(o.enum("agent", Agent.AGENT_UNSPECIFIED))
            .setModel(o.str("model"))
            .setEffort(o.str("effort"))
            .setStandardContext(o.bool("standardContext"))
            .build()
    }

    private fun parseNewSession(o: JsonObject): NewSessionViewModel = NewSessionViewModel.newBuilder().apply {
        setProjectSelectable(o.bool("projectSelectable"))
        setProjectKey(o.str("projectKey"))
        o.obj("projectName")?.let { setProjectName(parseText(it)) }
        setShortPath(o.str("shortPath"))
        setHasProjects(o.bool("hasProjects"))
        setPrompt(o.str("prompt"))
        setDrafting(o.bool("drafting"))
        setBusy(o.bool("busy"))
        setSettled(o.bool("settled"))
        setFrozen(o.bool("frozen"))
        o.obj("config")?.let { setConfig(parseConfig(it)) }
        setMachineName(o.str("machineName"))
        setMachineSelectable(o.bool("machineSelectable"))
        setAgentName(o.str("agentName"))
        o.arr("availableAgents").forEach { e ->
            val a = e.jsonObject
            addAvailableAgents(
                AgentOptionView.newBuilder()
                    .setAgent(a.enum("agent", Agent.AGENT_UNSPECIFIED))
                    .setDisplayName(a.str("displayName"))
                    .setInstalled(a.bool("installed"))
                    .setSelected(a.bool("selected")),
            )
        }
        o.obj("modelLabel")?.let { setModelLabel(parseText(it)) }
        setEffortLabel(o.str("effortLabel"))
        o.obj("defaultMode")?.let { dm ->
            setDefaultMode(
                DefaultModeInfo.newBuilder()
                    .setMode(dm.enum("mode", ApprovalMode.APPROVAL_MODE_UNSPECIFIED))
                    .setKnown(dm.bool("known"))
                    .setVaries(dm.bool("varies"))
                    .setLabel(parseText(dm.obj("label"))),
            )
        }
        o.obj("locationText")?.let { setLocationText(parseText(it)) }
        o.arr("notices").forEach { e ->
            val n = e.jsonObject
            addNotices(
                NewSessionNoticeView.newBuilder()
                    .setKind(n.enum("kind", NoticeKind.NOTICE_KIND_UNSPECIFIED))
                    .setSeverity(n.enum("severity", NoticeSeverity.NOTICE_SEVERITY_UNSPECIFIED))
                    .setText(parseText(n.obj("text")))
                    .setAction(n.enum("action", NoticeActionKind.NOTICE_ACTION_KIND_UNSPECIFIED))
                    .setTargetMachineId(n.str("targetMachineId"))
                    .setHasDraftChoices(n.bool("hasDraftChoices"))
                    .setDraftTitle(parseText(n.obj("draftTitle")))
                    .setDraftSubtitle(parseText(n.obj("draftSubtitle")))
                    .addAllDraftChoices(
                        n.arr("draftChoices").map { dc ->
                            val d = dc.jsonObject
                            OfflineMachineChoiceView.newBuilder()
                                .setMachineId(d.str("machineId"))
                                .setTitle(parseText(d.obj("title")))
                                .setNote(parseText(d.obj("note")))
                                .setConfig(parseConfig(d.obj("config")))
                                .build()
                        },
                    )
                    .setDraftNoneText(parseText(n.obj("draftNoneText"))),
            )
        }
        setCanStart(o.bool("canStart"))
        o.obj("disabledReason")?.let { setDisabledReason(parseText(it)) }
        o.obj("modelPanel")?.let { mp ->
            setModelPanel(
                NewSessionModelPanelView.newBuilder().apply {
                    setModelsLoading(mp.bool("modelsLoading"))
                    mp.obj("modelsError")?.let { setModelsError(parseText(it)) }
                    mp.obj("defaultModelLabel")?.let { setDefaultModelLabel(parseText(it)) }
                    setDefaultModelChecked(mp.bool("defaultModelChecked"))
                    mp.arr("models").forEach { me ->
                        val m = me.jsonObject
                        addModels(
                            ModelOptionView.newBuilder()
                                .setSlug(m.str("slug"))
                                .setDisplayName(m.str("displayName"))
                                .setFixedEffortNote(parseText(m.obj("fixedEffortNote")))
                                .setChecked(m.bool("checked"))
                                .setSupportsAuto(m.bool("supportsAuto"))
                                .setSupports1M(m.bool("supports1M")),
                        )
                    }
                    mp.arr("effortChoices").forEach { addEffortChoices(it.jsonPrimitive.content) }
                    setSelectedEffort(mp.str("selectedEffort"))
                    mp.obj("effortTitle")?.let { setEffortTitle(parseText(it)) }
                    setSupportsContextWindow(mp.bool("supportsContextWindow"))
                    setContextFixed(mp.bool("contextFixed"))
                    setStandardContext(mp.bool("standardContext"))
                    mp.obj("contextNote")?.let { setContextNote(parseText(it)) }
                    setPanelEnabled(mp.bool("panelEnabled"))
                    mp.arr("machineChoices").forEach { mce ->
                        val mc = mce.jsonObject
                        addMachineChoices(
                            MachineChoiceView.newBuilder()
                                .setMachineId(mc.str("machineId"))
                                .setName(mc.str("name"))
                                .setOnline(mc.bool("online"))
                                .setSelected(mc.bool("selected"))
                                .setNote(parseText(mc.obj("note"))),
                        )
                    }
                    setCanSelectAutoMode(mp.bool("canSelectAutoMode"))
                },
            )
        }
        setProjectRemoved(o.bool("projectRemoved"))
        setAutoIssue(o.enum("autoIssue", AutoIssueKind.AUTO_ISSUE_KIND_UNSPECIFIED))
    }.build()
}
