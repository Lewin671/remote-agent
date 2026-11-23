package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.proto.*
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ApprovalMode
import io.github.lewin671.remoteagent.proto.ImageRef
import io.github.lewin671.remoteagent.proto.InputStatus
import io.github.lewin671.remoteagent.proto.Origin
import io.github.lewin671.remoteagent.proto.OriginKind
import io.github.lewin671.remoteagent.proto.PlanChoice
import io.github.lewin671.remoteagent.proto.SendMode
import io.github.lewin671.remoteagent.proto.SentFile
import io.github.lewin671.remoteagent.proto.ToolKind
import io.github.lewin671.remoteagent.proto.TurnOutcome
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

    fun machineStatusView(scenario: String, viewName: String): MachineStatusViewModel {
        val v = namedViews(scenario).getValue(viewName)
        return parseMachineStatus(v["machineStatus"]!!.jsonObject)
    }

    fun quotaView(scenario: String, viewName: String): QuotaViewModel {
        val v = namedViews(scenario).getValue(viewName)
        return parseQuota(v["quota"]!!.jsonObject)
    }

    fun settingsView(scenario: String, viewName: String): SettingsViewModel {
        val v = namedViews(scenario).getValue(viewName)
        return parseSettings(v["settings"]!!.jsonObject)
    }

    fun machineSettingsView(scenario: String, viewName: String): MachineSettingsViewModel {
        val v = namedViews(scenario).getValue(viewName)
        return parseMachineSettings(v["machineSettings"]!!.jsonObject)
    }

    fun devicesView(scenario: String, viewName: String): DevicesViewModel {
        val v = namedViews(scenario).getValue(viewName)
        return parseDevices(v["devices"]!!.jsonObject)
    }

    fun clientDetailView(scenario: String, viewName: String): ClientDetailViewModel {
        val v = namedViews(scenario).getValue(viewName)
        return parseClientDetail(v["clientDetail"]!!.jsonObject)
    }

    fun sessionView(scenario: String, viewName: String): SessionViewModel {
        val v = namedViews(scenario).getValue(viewName)
        return parseSession(v["session"]!!.jsonObject)
    }

    fun schedulesView(scenario: String, viewName: String): SchedulesViewModel {
        val v = namedViews(scenario).getValue(viewName)
        return parseSchedules(v["schedules"]!!.jsonObject)
    }

    fun scheduleDetailView(scenario: String, viewName: String): ScheduleDetailViewModel {
        val v = namedViews(scenario).getValue(viewName)
        return parseScheduleDetail(v["scheduleDetail"]!!.jsonObject)
    }

    fun scheduleFormView(scenario: String, viewName: String): ScheduleFormViewModel {
        val v = namedViews(scenario).getValue(viewName)
        return parseScheduleForm(v["scheduleForm"]!!.jsonObject)
    }

    fun scheduleSessionStripView(scenario: String, viewName: String): ScheduleSessionStripViewModel {
        val v = namedViews(scenario).getValue(viewName)
        return parseScheduleSessionStrip(v["scheduleSessionStrip"]!!.jsonObject)
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
                    .setHighlightArg(h.str("highlightArg"))
                    .setSub(parseText(h.obj("sub")))
                    .setSubError(h.bool("subError"))
                    .setSubPath(h.bool("subPath"))
                    .setDetailsAction(h.bool("detailsAction"))
                    .setUnlinked(h.bool("unlinked"))
                    .setIsConnecting(h.bool("isConnecting")),
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
                    .setAlwaysLabel(parseText(c.obj("alwaysLabel")))
                    .setLocked(c.bool("locked")),
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
                    .setDetail(parseText(a.obj("detail")))
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
        setPickerKey(d.str("pickerKey"))
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
                    setFilterMachineId(m.str("filterMachineId"))
                    setFilterMachineName(m.str("filterMachineName"))
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
                    d.arr("machineIds").forEach { addMachineIds(it.jsonPrimitive.content) }
                    d.arr("machineNames").forEach { addMachineNames(it.jsonPrimitive.content) }
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
                    d.obj("hiddenNotice")?.let { hn ->
                        setHiddenNotice(
                            HiddenProjectNoticeView.newBuilder()
                                .setActive(hn.bool("active"))
                                .setText(parseText(hn.obj("text")))
                                .setActionEnabled(hn.bool("actionEnabled"))
                                .setBusy(hn.bool("busy"))
                                .setError(parseText(hn.obj("error"))),
                        )
                    }
                    setSavingSettings(d.bool("savingSettings"))
                    d.obj("settingsError")?.let { setSettingsError(parseText(it)) }
                    setCanStart(d.bool("canStart"))
                    setCanPin(d.bool("canPin"))
                    setCanRemove(d.bool("canRemove"))
                    d.obj("saveStatus")?.let { setSaveStatus(parseText(it)) }
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
            .setMode(o.enum("mode", ApprovalMode.APPROVAL_MODE_UNSPECIFIED))
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
                    .setTargetAgent(n.enum("targetAgent", Agent.AGENT_UNSPECIFIED))
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
        setPrefsBlocked(o.bool("prefsBlocked"))
        o.obj("prefsBlockedReason")?.let { setPrefsBlockedReason(parseText(it)) }
        setCapabilitiesLoaded(o.bool("capabilitiesLoaded"))
        setSupportsImages(o.bool("supportsImages"))
        setSupportsFiles(o.bool("supportsFiles"))
        setMaxImages(o.i32("maxImages"))
        setMaxFiles(o.i32("maxFiles"))
        setMaxFileBytes(o.i64("maxFileBytes"))
        o.obj("imageUploadBlockedReason")?.let { setImageUploadBlockedReason(parseText(it)) }
        setDiskFull(o.bool("diskFull"))
        o.obj("fileUploadBlockedReason")?.let { setFileUploadBlockedReason(parseText(it)) }
        setGalleryAllowsVideo(o.bool("galleryAllowsVideo"))
        setGalleryAcceptMime(o.str("galleryAcceptMime"))
        setMaxFileNameBytes(o.i32("maxFileNameBytes"))
        setMaxFileSizeText(o.str("maxFileSizeText"))
        setOriginalImageMaxBytes(o.i64("originalImageMaxBytes"))
        o.obj("originalImageHint")?.let { setOriginalImageHint(parseText(it)) }
        o.obj("fileMetadataNotice")?.let { setFileMetadataNotice(parseText(it)) }
    }.build()

    private fun parseRing(o: JsonObject): MachineRingView = MachineRingView.newBuilder().apply {
        setKind(o.enum("kind", MachineRingKind.MACHINE_RING_KIND_UNSPECIFIED))
        o.obj("label")?.let { setLabel(parseText(it)) }
        setHasValue(o.bool("hasValue"))
        setFraction(o.f64("fraction"))
        o.obj("valueText")?.let { setValueText(parseText(it)) }
        setTone(o.enum("tone", MachineRingTone.MACHINE_RING_TONE_UNSPECIFIED))
        o.obj("a11yLabel")?.let { setA11YLabel(parseText(it)) }
    }.build()

    private fun parseChart(o: JsonObject): MachineChartView = MachineChartView.newBuilder().apply {
        setVisible(o.bool("visible"))
        o.arr("points").forEach { e ->
            val p = e.jsonObject
            addPoints(
                MachineChartPoint.newBuilder()
                    .setTimeMs(p.i64("timeMs"))
                    .setValue(p.f64("value")),
            )
        }
        setWindowStartMs(o.i64("windowStartMs"))
        setWindowEndMs(o.i64("windowEndMs"))
        setGapThresholdMs(o.i64("gapThresholdMs"))
        setCeiling(o.f64("ceiling"))
        setHasScrub(o.bool("hasScrub"))
        setScrubIndex(o.i32("scrubIndex"))
        o.obj("scrubValue")?.let { setScrubValue(parseText(it)) }
        o.obj("scrubAge")?.let { setScrubAge(parseText(it)) }
        o.obj("a11ySummary")?.let { setA11YSummary(parseText(it)) }
        setCanStepPrev(o.bool("canStepPrev"))
        setCanStepNext(o.bool("canStepNext"))
        setScrubNumericValue(o.f64("scrubNumericValue"))
        setScrubValueText(o.str("scrubValueText"))
        setScrubUnit(o.str("scrubUnit"))
    }.build()

    private fun parseMachineStatus(o: JsonObject): MachineStatusViewModel = MachineStatusViewModel.newBuilder().apply {
        setMachineId(o.str("machineId"))
        setMachineName(o.str("machineName"))
        setFound(o.bool("found"))
        setSupported(o.bool("supported"))
        setOnline(o.bool("online"))
        setPollState(o.enum("pollState", MachinePollState.MACHINE_POLL_STATE_UNSPECIFIED))
        setDim(o.bool("dim"))
        setStatusDot(o.enum("statusDot", MachineStatusDot.MACHINE_STATUS_DOT_UNSPECIFIED))
        o.obj("statusBadge")?.let { setStatusBadge(parseText(it)) }
        setShowRetry(o.bool("showRetry"))
        o.obj("unavailableTitle")?.let { setUnavailableTitle(parseText(it)) }
        o.arr("rings").forEach { addRings(parseRing(it.jsonObject)) }
        o.obj("cpu")?.let { c ->
            setCpu(
                MachineCpuSection.newBuilder()
                    .setHasValue(c.bool("hasValue"))
                    .setValue(c.f64("value"))
                    .setValueText(c.str("valueText"))
                    .setUnit(c.str("unit"))
                    .apply {
                        c.obj("detail")?.let { setDetail(parseText(it)) }
                        c.obj("detail2")?.let { setDetail2(parseText(it)) }
                        c.obj("a11yLabel")?.let { setA11YLabel(parseText(it)) }
                        c.obj("chart")?.let { setChart(parseChart(it)) }
                    },
            )
        }
        o.obj("memory")?.let { m ->
            setMemory(
                MachineMemorySection.newBuilder()
                    .setHasValue(m.bool("hasValue"))
                    .setPressure(m.enum("pressure", MachinePressureTone.MACHINE_PRESSURE_TONE_UNSPECIFIED))
                    .setHasPressurePosition(m.bool("hasPressurePosition"))
                    .setPressurePosition(m.f64("pressurePosition"))
                    .setValue(m.f64("value"))
                    .setValueText(m.str("valueText"))
                    .setUnit(m.str("unit"))
                    .setUsedBytes(m.i64("usedBytes"))
                    .setTotalBytes(m.i64("totalBytes"))
                    .apply {
                        m.obj("caption")?.let { setCaption(parseText(it)) }
                        m.obj("pressureText")?.let { setPressureText(parseText(it)) }
                        m.obj("totalText")?.let { setTotalText(parseText(it)) }
                        m.obj("swapText")?.let { setSwapText(parseText(it)) }
                        m.obj("a11yLabel")?.let { setA11YLabel(parseText(it)) }
                        m.obj("chart")?.let { setChart(parseChart(it)) }
                    },
            )
        }
        o.obj("disk")?.let { d ->
            setDisk(
                MachineDiskSection.newBuilder()
                    .setHasValue(d.bool("hasValue"))
                    .setState(d.enum("state", DiskPressureState.DISK_PRESSURE_STATE_UNSPECIFIED))
                    .setHasUsedFraction(d.bool("hasUsedFraction"))
                    .setUsedFraction(d.f64("usedFraction"))
                    .setShowActions(d.bool("showActions"))
                    .setValue(d.f64("value"))
                    .setValueText(d.str("valueText"))
                    .setUnit(d.str("unit"))
                    .setAvailableBytes(d.i64("availableBytes"))
                    .setTotalBytes(d.i64("totalBytes"))
                    .apply {
                        d.obj("caption")?.let { setCaption(parseText(it)) }
                        d.obj("stateBadge")?.let { setStateBadge(parseText(it)) }
                        d.obj("detail")?.let { setDetail(parseText(it)) }
                        d.obj("detail2")?.let { setDetail2(parseText(it)) }
                        d.obj("cleanupActionLabel")?.let { setCleanupActionLabel(parseText(it)) }
                        d.obj("agentUsageLabel")?.let { setAgentUsageLabel(parseText(it)) }
                        d.obj("agentUsageValue")?.let { setAgentUsageValue(parseText(it)) }
                        d.obj("a11yLabel")?.let { setA11YLabel(parseText(it)) }
                    },
            )
        }
        o.obj("topUsage")?.let { t ->
            setTopUsage(
                MachineTopUsageSection.newBuilder()
                    .setSelectedTab(t.enum("selectedTab", MachineUsageTab.MACHINE_USAGE_TAB_UNSPECIFIED))
                    .apply {
                        t.arr("rows").forEach { e ->
                            val r = e.jsonObject
                            addRows(
                                MachineUsageRow.newBuilder()
                                    .setId(r.str("id"))
                                    .setCpuPercent(r.f64("cpuPercent"))
                                    .setMemoryBytes(r.i64("memoryBytes"))
                                    .setAgent(r.enum("agent", Agent.AGENT_UNSPECIFIED))
                                    .setSessionId(r.str("sessionId"))
                                    .setCpuBarFraction(r.f64("cpuBarFraction"))
                                    .setMemoryBarFraction(r.f64("memoryBarFraction"))
                                    .apply {
                                        r.obj("title")?.let { setTitle(parseText(it)) }
                                        r.obj("secondary")?.let { setSecondary(parseText(it)) }
                                        r.obj("cpuValueText")?.let { setCpuValueText(parseText(it)) }
                                        r.obj("memoryValueText")?.let { setMemoryValueText(parseText(it)) }
                                    },
                            )
                        }
                        t.obj("emptyText")?.let { setEmptyText(parseText(it)) }
                    },
            )
        }
        o.obj("modelUsage")?.let { mu ->
            setModelUsage(
                MachineModelUsagePreview.newBuilder()
                    .setSupported(mu.bool("supported"))
                    .setLoading(mu.bool("loading"))
                    .setHasData(mu.bool("hasData"))
                    .setFallbackThirtyDays(mu.bool("fallbackThirtyDays"))
                    .setBackfilling(mu.bool("backfilling"))
                    .setCachedOffline(mu.bool("cachedOffline"))
                    .setCachedAtMs(mu.i64("cachedAtMs"))
                    .apply {
                        mu.arr("rows").forEach { re ->
                            val r = re.jsonObject
                            addRows(
                                MachineModelUsageRow.newBuilder()
                                    .setAgent(r.enum("agent", Agent.AGENT_UNSPECIFIED))
                                    .setAgentName(r.str("agentName"))
                                    .setModel(r.str("model"))
                                    .setTotalTokens(r.i64("totalTokens"))
                                    .setHasCacheHit(r.bool("hasCacheHit"))
                                    .setCacheHitRate(r.f64("cacheHitRate"))
                                    .setHasSpeed(r.bool("hasSpeed"))
                                    .setSpeedP50(r.f64("speedP50"))
                                    .setUsageRequestCount(r.i64("usageRequestCount"))
                                    .setSpeedRequestCount(r.i64("speedRequestCount"))
                                    .setFewSpeedSamples(r.bool("fewSpeedSamples"))
                                    .apply {
                                        r.obj("titleText")?.let { setTitleText(parseText(it)) }
                                        r.obj("totalTokensText")?.let { setTotalTokensText(parseText(it)) }
                                        r.obj("metaText")?.let { setMetaText(parseText(it)) }
                                    },
                            )
                        }
                        mu.obj("unsupportedFootnote")?.let { setUnsupportedFootnote(parseText(it)) }
                        mu.obj("emptyText")?.let { setEmptyText(parseText(it)) }
                        mu.obj("windowLabel")?.let { setWindowLabel(parseText(it)) }
                    },
            )
        }
        o.obj("uptimeText")?.let { setUptimeText(parseText(it)) }
        o.obj("batteryText")?.let { setBatteryText(parseText(it)) }
    }.build()

    private fun parseQuotaBanner(o: JsonObject): QuotaBannerView = QuotaBannerView.newBuilder().apply {
        setSeverity(o.enum("severity", QuotaBannerSeverity.QUOTA_BANNER_SEVERITY_UNSPECIFIED))
        o.obj("message")?.let { setMessage(parseText(it)) }
        setMachineId(o.str("machineId"))
        setAgent(o.enum("agent", Agent.AGENT_UNSPECIFIED))
        setWindowName(o.str("windowName"))
    }.build()

    private fun parseQuotaRow(o: JsonObject): QuotaRowView = QuotaRowView.newBuilder().apply {
        setWindowName(o.str("windowName"))
        o.obj("label")?.let { setLabel(parseText(it)) }
        o.obj("poolName")?.let { setPoolName(parseText(it)) }
        setHasPercent(o.bool("hasPercent"))
        setUsedPercent(o.i32("usedPercent"))
        setRemainingPercent(o.i32("remainingPercent"))
        setExhausted(o.bool("exhausted"))
        setPastReset(o.bool("pastReset"))
        setStale(o.bool("stale"))
        setTone(o.enum("tone", QuotaTone.QUOTA_TONE_UNSPECIFIED))
        o.obj("remainingText")?.let { setRemainingText(parseText(it)) }
        o.obj("resetText")?.let { setResetText(parseText(it)) }
        o.obj("resetClockText")?.let { setResetClockText(parseText(it)) }
        o.obj("footnote")?.let { setFootnote(parseText(it)) }
    }.build()

    private fun parseQuotaSection(o: JsonObject): QuotaCliSection = QuotaCliSection.newBuilder().apply {
        setAgent(o.enum("agent", Agent.AGENT_UNSPECIFIED))
        setAgentName(o.str("agentName"))
        setState(o.enum("state", QuotaSectionState.QUOTA_SECTION_STATE_UNSPECIFIED))
        setUnsupported(o.bool("unsupported"))
        o.arr("accounts").forEach { ae ->
            val a = ae.jsonObject
            addAccounts(
                QuotaAccountGroupView.newBuilder()
                    .setAccount(a.str("account"))
                    .setPlan(a.str("plan"))
                    .setState(a.enum("state", QuotaSectionState.QUOTA_SECTION_STATE_UNSPECIFIED))
                    .setLastRecord(a.bool("lastRecord"))
                    .apply {
                        a.obj("statusText")?.let { setStatusText(parseText(it)) }
                        a.obj("freshnessText")?.let { setFreshnessText(parseText(it)) }
                        a.obj("errorText")?.let { setErrorText(parseText(it)) }
                        a.arr("groups").forEach { we ->
                            val w = we.jsonObject
                            addGroups(
                                QuotaWindowGroupView.newBuilder()
                                    .apply {
                                        w.obj("groupLabel")?.let { setGroupLabel(parseText(it)) }
                                        w.arr("rows").forEach { addRows(parseQuotaRow(it.jsonObject)) }
                                    },
                            )
                        }
                    },
            )
        }
        o.obj("emptyOrErrorText")?.let { setEmptyOrErrorText(parseText(it)) }
        o.obj("usageHint")?.let { setUsageHint(parseText(it)) }
        o.obj("summaryRow")?.let { setSummaryRow(parseQuotaRow(it)) }
        o.obj("summaryPrimaryText")?.let { setSummaryPrimaryText(parseText(it)) }
        setSummaryPrimaryWarn(o.bool("summaryPrimaryWarn"))
        o.obj("summaryWindowText")?.let { setSummaryWindowText(parseText(it)) }
    }.build()

    private fun parseQuota(o: JsonObject): QuotaViewModel = QuotaViewModel.newBuilder().apply {
        setMachineId(o.str("machineId"))
        setMachineName(o.str("machineName"))
        setFound(o.bool("found"))
        setReachable(o.bool("reachable"))
        setUnlinked(o.bool("unlinked"))
        setMember(o.bool("member"))
        setHasAgentsReported(o.bool("hasAgentsReported"))
        o.obj("offlineNotice")?.let { setOfflineNotice(parseText(it)) }
        o.obj("banner")?.let { setBanner(parseQuotaBanner(it)) }
        o.arr("sections").forEach { addSections(parseQuotaSection(it.jsonObject)) }
        o.obj("footerNote")?.let { setFooterNote(parseText(it)) }
        o.obj("emptyText")?.let { setEmptyText(parseText(it)) }
    }.build()

    private fun parseClientRow(o: JsonObject): ClientRowView = ClientRowView.newBuilder().apply {
        setDeviceId(o.str("deviceId"))
        setDisplayName(o.str("displayName"))
        setDescription(o.str("description"))
        setShortId(o.str("shortId"))
        setIsSelf(o.bool("isSelf"))
        setIsAuthority(o.bool("isAuthority"))
        setMember(o.bool("member"))
        setPairingIncomplete(o.bool("pairingIncomplete"))
        o.obj("roleLabel")?.let { setRoleLabel(parseText(it)) }
        o.obj("lastUsedText")?.let { setLastUsedText(parseText(it)) }
    }.build()

    private fun parseMachineListCard(m: JsonObject): MachineListCardView = MachineListCardView.newBuilder()
        .setMachineId(m.str("machineId"))
        .setName(m.str("name"))
        .setOnline(m.bool("online"))
        .setMember(m.bool("member"))
        .setProtocolMismatch(m.i32("protocolMismatch"))
        .setLastSeenMs(m.i64("lastSeenMs"))
        .setStatusDot(m.enum("statusDot", MachineStatusDot.MACHINE_STATUS_DOT_UNSPECIFIED))
        .setStatusWarn(m.bool("statusWarn"))
        .setInDeviceDirectory(m.bool("inDeviceDirectory"))
        .setDirectReady(m.bool("directReady"))
        .apply {
            m.obj("statusText")?.let { setStatusText(parseText(it)) }
            m.arr("subtitleParts").forEach { se ->
                val seg = se.jsonObject
                addSubtitleParts(
                    SubtitleSegment.newBuilder()
                        .apply { seg.obj("text")?.let { setText(parseText(it)) } }
                        .setWarn(seg.bool("warn"))
                        .build(),
                )
            }
            m.arr("quotaSummaries").forEach { addQuotaSummaries(parseQuotaSection(it.jsonObject)) }
        }
        .build()

    private fun parseSettings(o: JsonObject): SettingsViewModel = SettingsViewModel.newBuilder().apply {
        setOnline(o.bool("online"))
        setClockSkewed(o.bool("clockSkewed"))
        setUnlinked(o.bool("unlinked"))
        setHasAccount(o.bool("hasAccount"))
        setIsManager(o.bool("isManager"))
        setAuthorityName(o.str("authorityName"))
        setRecoveryPending(o.bool("recoveryPending"))
        setRecoveryReadyMs(o.i64("recoveryReadyMs"))
        o.obj("recoveryBannerText")?.let { setRecoveryBannerText(parseText(it)) }
        o.obj("unlinkedBannerText")?.let { setUnlinkedBannerText(parseText(it)) }
        o.obj("clockSkewBannerText")?.let { setClockSkewBannerText(parseText(it)) }
        o.arr("machines").forEach { addMachines(parseMachineListCard(it.jsonObject)) }
        setMachinesLoading(o.bool("machinesLoading"))
        setCanAddDevice(o.bool("canAddDevice"))
        o.obj("addDeviceHint")?.let { setAddDeviceHint(parseText(it)) }
        o.obj("emptyMachinesText")?.let { setEmptyMachinesText(parseText(it)) }
        o.obj("emptyMachinesBody")?.let { setEmptyMachinesBody(parseText(it)) }
        o.obj("machineCountText")?.let { setMachineCountText(parseText(it)) }
        o.arr("recentClients").forEach { addRecentClients(parseClientRow(it.jsonObject)) }
        setHasMoreClients(o.bool("hasMoreClients"))
        setTotalDevicesCount(o.i32("totalDevicesCount"))
        o.obj("clientsSummaryText")?.let { setClientsSummaryText(parseText(it)) }
        setClientsSummaryWarn(o.bool("clientsSummaryWarn"))
        setUnfinishedPairingCount(o.i32("unfinishedPairingCount"))
        o.obj("security")?.let { s ->
            setSecurity(
                SecuritySummaryView.newBuilder()
                    .setIsManager(s.bool("isManager"))
                    .setUnlinked(s.bool("unlinked"))
                    .setAuthorityDeviceId(s.str("authorityDeviceId"))
                    .setAuthorityName(s.str("authorityName"))
                    .setRecoveryPending(s.bool("recoveryPending"))
                    .setRecoveryReadyMs(s.i64("recoveryReadyMs"))
                    .setHasHandoffOffer(s.bool("hasHandoffOffer"))
                    .setRecoveryStatusStale(s.bool("recoveryStatusStale"))
                    .setLeaseEndMs(s.i64("leaseEndMs"))
                    .apply {
                        s.obj("summaryText")?.let { setSummaryText(parseText(it)) }
                        s.obj("recoveryNotice")?.let { setRecoveryNotice(parseText(it)) }
                    },
            )
        }
        o.obj("notifications")?.let { n ->
            setNotifications(
                NotificationsView.newBuilder()
                    .setSupported(n.bool("supported"))
                    .setPermission(n.enum("permission", NotificationPermissionState.NOTIFICATION_PERMISSION_STATE_UNSPECIFIED))
                    .setPushState(n.enum("pushState", PushRegistrationState.PUSH_REGISTRATION_STATE_UNSPECIFIED))
                    .setNotifyAttention(n.bool("notifyAttention"))
                    .setNotifyTurnEnded(n.bool("notifyTurnEnded"))
                    .setNotifyScheduled(n.bool("notifyScheduled"))
                    .setShowContent(n.bool("showContent"))
                    .setErrorDetail(n.str("errorDetail"))
                    .setSummaryWarn(n.bool("summaryWarn"))
                    .apply {
                        n.obj("statusText")?.let { setStatusText(parseText(it)) }
                        n.obj("hintText")?.let { setHintText(parseText(it)) }
                        n.obj("summaryText")?.let { setSummaryText(parseText(it)) }
                    },
            )
        }
        o.obj("appearance")?.let { a ->
            setAppearance(
                AppearanceView.newBuilder()
                    .setTheme(a.enum("theme", ThemePreference.THEME_PREFERENCE_UNSPECIFIED))
                    .apply { a.obj("label")?.let { setLabel(parseText(it)) } },
            )
        }
        o.obj("update")?.let { u ->
            setUpdate(
                ClientUpdateView.newBuilder()
                    .setCurrentVersion(u.str("currentVersion"))
                    .setProtocolVersion(u.i32("protocolVersion"))
                    .setChannel(u.str("channel"))
                    .setChecking(u.bool("checking"))
                    .setHasUpdate(u.bool("hasUpdate"))
                    .setAvailableVersion(u.str("availableVersion"))
                    .setDownloadedVersion(u.str("downloadedVersion"))
                    .setDownloadProgressPercent(u.i32("downloadProgressPercent"))
                    .setProtocolChanged(u.bool("protocolChanged"))
                    .apply {
                        u.obj("statusText")?.let { setStatusText(parseText(it)) }
                        u.obj("errorText")?.let { setErrorText(parseText(it)) }
                    },
            )
        }
        o.obj("connCheck")?.let { c ->
            setConnCheck(
                ConnCheckSummaryView.newBuilder()
                    .setServerState(c.enum("serverState", ConnProbeState.CONN_PROBE_STATE_UNSPECIFIED))
                    .setLatencyMs(c.i32("latencyMs"))
                    .setConnState(c.enum("connState", ConnState.CONN_STATE_UNSPECIFIED))
                    .setNetworkAvailable(c.bool("networkAvailable"))
                    .setLivePushConnected(c.bool("livePushConnected"))
                    .setEverOnline(c.bool("everOnline"))
                    .setFailures(c.i32("failures"))
                    .setConnStartedAtMs(c.i64("connStartedAtMs"))
                    .setDisplayGrace(c.bool("displayGrace"))
                    .setIsConnecting(c.bool("isConnecting"))
                    .apply {
                        c.obj("statusWord")?.let { setStatusWord(parseText(it)) }
                        c.obj("summaryText")?.let { setSummaryText(parseText(it)) }
                        c.obj("lastFailureText")?.let { setLastFailureText(parseText(it)) }
                        c.obj("connStep")?.let { setConnStep(parseText(it)) }
                        c.obj("bannerText")?.let { setBannerText(parseText(it)) }
                    },
            )
        }
        o.arr("diskReminders").forEach { de ->
            val d = de.jsonObject
            addDiskReminders(
                DiskReminderView.newBuilder()
                    .setMachineId(d.str("machineId"))
                    .setMachineName(d.str("machineName"))
                    .setCritical(d.bool("critical"))
                    .setAvailableBytes(d.i64("availableBytes"))
                    .apply {
                        d.obj("title")?.let { setTitle(parseText(it)) }
                        d.obj("detail")?.let { setDetail(parseText(it)) }
                    },
            )
        }
        o.obj("quotaBanner")?.let { setQuotaBanner(parseQuotaBanner(it)) }
        setCanExitAccount(o.bool("canExitAccount"))
        setExitNeedsTransfer(o.bool("exitNeedsTransfer"))
    }.build()

    private fun parseConnectionGroup(o: JsonObject): ConnectionGroupView = ConnectionGroupView.newBuilder().apply {
        setToggleVisible(o.bool("toggleVisible"))
        setToggleEnabled(o.bool("toggleEnabled"))
        setToggleChecked(o.bool("toggleChecked"))
        setStage(o.enum("stage", ConnectionGroupStage.CONNECTION_GROUP_STAGE_UNSPECIFIED))
        setTone(o.enum("tone", ConnectionGroupTone.CONNECTION_GROUP_TONE_UNSPECIFIED))
        o.obj("statusText")?.let { setStatusText(parseText(it)) }
        o.obj("metricText")?.let { setMetricText(parseText(it)) }
        o.obj("detailText")?.let { setDetailText(parseText(it)) }
        o.obj("fileSpeedText")?.let { setFileSpeedText(parseText(it)) }
        o.obj("readLatencyText")?.let { setReadLatencyText(parseText(it)) }
        o.obj("footerText")?.let { setFooterText(parseText(it)) }
        setShowDiagnosticsEntry(o.bool("showDiagnosticsEntry"))
        setRowDot(o.enum("rowDot", MachineStatusDot.MACHINE_STATUS_DOT_UNSPECIFIED))
        setRowSpinning(o.bool("rowSpinning"))
        o.obj("rowValueText")?.let { setRowValueText(parseText(it)) }
        o.obj("rowSubtitleText")?.let { setRowSubtitleText(parseText(it)) }
    }.build()

    private fun parseAgentDefaultsView(ad: JsonObject): MachineAgentDefaultsView =
        MachineAgentDefaultsView.newBuilder()
            .setAgent(ad.enum("agent", Agent.AGENT_UNSPECIFIED))
            .setAgentName(ad.str("agentName"))
            .setSelectedModel(ad.str("selectedModel"))
            .setSelectedEffort(ad.str("selectedEffort"))
            .setSelectedMode(ad.enum("selectedMode", ApprovalMode.APPROVAL_MODE_UNSPECIFIED))
            .apply {
                ad.obj("selectedModelLabel")?.let { setSelectedModelLabel(parseText(it)) }
                ad.obj("autoUnavailableNotice")?.let { setAutoUnavailableNotice(parseText(it)) }
                ad.obj("footerText")?.let { setFooterText(parseText(it)) }
                ad.arr("modelChoices").forEach { me ->
                    val m = me.jsonObject
                    addModelChoices(
                        ModelChoiceView.newBuilder()
                            .setId(m.str("id"))
                            .setDesc(m.str("desc"))
                            .setSelected(m.bool("selected"))
                            .setIsDefault(m.bool("isDefault"))
                            .apply { m.obj("label")?.let { setLabel(parseText(it)) } },
                    )
                }
                ad.arr("effortChoices").forEach { ee ->
                    val e = ee.jsonObject
                    addEffortChoices(
                        EffortChoiceView.newBuilder()
                            .setId(e.str("id"))
                            .setSelected(e.bool("selected"))
                            .setIsDefault(e.bool("isDefault"))
                            .apply { e.obj("label")?.let { setLabel(parseText(it)) } },
                    )
                }
                ad.arr("modeChoices").forEach { me ->
                    val m = me.jsonObject
                    addModeChoices(
                        ModeChoiceView.newBuilder()
                            .setMode(m.enum("mode", ApprovalMode.APPROVAL_MODE_UNSPECIFIED))
                            .setSelected(m.bool("selected"))
                            .setDisabled(m.bool("disabled"))
                            .setDanger(m.bool("danger"))
                            .apply {
                                m.obj("label")?.let { setLabel(parseText(it)) }
                                m.obj("disabledReason")?.let { setDisabledReason(parseText(it)) }
                            },
                    )
                }
            }
            .build()

    private fun parseMachineSettings(o: JsonObject): MachineSettingsViewModel = MachineSettingsViewModel.newBuilder().apply {
        setMachineId(o.str("machineId"))
        setName(o.str("name"))
        setShortId(o.str("shortId"))
        setFound(o.bool("found"))
        setLoading(o.bool("loading"))
        setMember(o.bool("member"))
        setOnline(o.bool("online"))
        setUnlinked(o.bool("unlinked"))
        setProtocolMismatch(o.i32("protocolMismatch"))
        setEditable(o.bool("editable"))
        setHeaderDot(o.enum("headerDot", MachineStatusDot.MACHINE_STATUS_DOT_UNSPECIFIED))
        o.obj("headerSubtitle")?.let { setHeaderSubtitle(parseText(it)) }
        o.obj("offlineOrMismatchBanner")?.let { setOfflineOrMismatchBanner(parseText(it)) }
        o.obj("removedBanner")?.let { setRemovedBanner(parseText(it)) }
        setShowStatusEntry(o.bool("showStatusEntry"))
        setStatusCardDot(o.enum("statusCardDot", MachineStatusDot.MACHINE_STATUS_DOT_UNSPECIFIED))
        o.obj("statusCardBadge")?.let { setStatusCardBadge(parseText(it)) }
        setStatusCardWarn(o.bool("statusCardWarn"))
        o.arr("statusRings").forEach { addStatusRings(parseRing(it.jsonObject)) }
        o.arr("quotaRows").forEach { addQuotaRows(parseQuotaSection(it.jsonObject)) }
        setShowUsageEntry(o.bool("showUsageEntry"))
        setUsageTotalTokens(o.i64("usageTotalTokens"))
        setUsageFallbackThirtyDays(o.bool("usageFallbackThirtyDays"))
        setUsageCachedOffline(o.bool("usageCachedOffline"))
        o.obj("usageEmptyText")?.let { setUsageEmptyText(parseText(it)) }
        setAppOnline(o.bool("appOnline"))
        setIsManager(o.bool("isManager"))
        o.obj("usageTokensText")?.let { setUsageTokensText(parseText(it)) }
        o.obj("connection")?.let { setConnection(parseConnectionGroup(it)) }
        o.arr("cliVersions").forEach { ce ->
            val c = ce.jsonObject
            addCliVersions(
                CliVersionView.newBuilder()
                    .setAgent(c.enum("agent", Agent.AGENT_UNSPECIFIED))
                    .setName(c.str("name"))
                    .setVersion(c.str("version"))
                    .setVerified(c.bool("verified"))
                    .setEnabled(if (c.containsKey("enabled")) c.bool("enabled") else true)
                    .setLockedLastEnabled(c.bool("lockedLastEnabled"))
                    .setSaving(c.bool("saving"))
                    .setDefaultModeDanger(c.bool("defaultModeDanger"))
                    .apply {
                        c.obj("unverifiedLabel")?.let { setUnverifiedLabel(parseText(it)) }
                        c.obj("defaultModelLabel")?.let { setDefaultModelLabel(parseText(it)) }
                        c.obj("defaultModeLabel")?.let { setDefaultModeLabel(parseText(it)) }
                        c.obj("disabledLabel")?.let { setDisabledLabel(parseText(it)) }
                        c.obj("defaults")?.let { setDefaults(parseAgentDefaultsView(it)) }
                    },
            )
        }
        setShowCliSwitches(o.bool("showCliSwitches"))
        setCliSwitchesEditable(o.bool("cliSwitchesEditable"))
        setCliSwitchesLeaseExpired(o.bool("cliSwitchesLeaseExpired"))
        o.obj("cliSwitchesDisabledReason")?.let { setCliSwitchesDisabledReason(parseText(it)) }
        o.obj("cliSwitchesConflictBanner")?.let { setCliSwitchesConflictBanner(parseText(it)) }
        o.obj("cliSwitchesFooter")?.let { setCliSwitchesFooter(parseText(it)) }
        setHasUnverifiedCli(o.bool("hasUnverifiedCli"))
        setSettingsLoading(o.bool("settingsLoading"))
        o.obj("settingsError")?.let { setSettingsError(parseText(it)) }
        setSavePending(o.bool("savePending"))
        setSaveUnknown(o.bool("saveUnknown"))
        o.obj("saveError")?.let { setSaveError(parseText(it)) }
        o.arr("availableAgents").forEach { addAvailableAgents(Agent.valueOf(it.jsonPrimitive.content)) }
        setSelectedAgent(o.enum("selectedAgent", Agent.AGENT_UNSPECIFIED))
        o.obj("agentDefaults")?.let { setAgentDefaults(parseAgentDefaultsView(it)) }
        o.arr("approvalTimeoutChoices").forEach { te ->
            val t = te.jsonObject
            addApprovalTimeoutChoices(
                TimeoutChoiceView.newBuilder()
                    .setSeconds(t.i32("seconds"))
                    .setSelected(t.bool("selected"))
                    .apply { t.obj("label")?.let { setLabel(parseText(it)) } },
            )
        }
        setSelectedApprovalTimeoutSeconds(o.i32("selectedApprovalTimeoutSeconds"))
        o.arr("idleCloseChoices").forEach { ie ->
            val i = ie.jsonObject
            addIdleCloseChoices(
                IdleCloseChoiceView.newBuilder()
                    .setMinutes(i.i32("minutes"))
                    .setSelected(i.bool("selected"))
                    .apply { i.obj("label")?.let { setLabel(parseText(it)) } },
            )
        }
        setSelectedIdleCloseMinutes(o.i32("selectedIdleCloseMinutes"))
        o.obj("attachmentStorage")?.let { s ->
            setAttachmentStorage(
                AttachmentStorageView.newBuilder()
                    .setLoaded(s.bool("loaded"))
                    .setSelectedQuotaBytes(s.i64("selectedQuotaBytes"))
                    .setShowEmergencyReserve(s.bool("showEmergencyReserve"))
                    .setCanClear(s.bool("canClear"))
                    .setCleaning(s.bool("cleaning"))
                    .setSummaryWarn(s.bool("summaryWarn"))
                    .setUploadedPercent(s.i32("uploadedPercent"))
                    .setPreviewPercent(s.i32("previewPercent"))
                    .setReservePercent(s.i32("reservePercent"))
                    .apply {
                        s.obj("limitLabel")?.let { setLimitLabel(parseText(it)) }
                        s.obj("summaryText")?.let { setSummaryText(parseText(it)) }
                        s.obj("usedTotalText")?.let { setUsedTotalText(parseText(it)) }
                        s.obj("uploadedText")?.let { setUploadedText(parseText(it)) }
                        s.obj("uploadedDetail")?.let { setUploadedDetail(parseText(it)) }
                        s.arr("quotaChoices").forEach { qe ->
                            val q = qe.jsonObject
                            addQuotaChoices(
                                QuotaSizeChoiceView.newBuilder()
                                    .setBytes(q.i64("bytes"))
                                    .setSelected(q.bool("selected"))
                                    .apply { q.obj("label")?.let { setLabel(parseText(it)) } },
                            )
                        }
                        s.obj("previewCacheText")?.let { setPreviewCacheText(parseText(it)) }
                        s.obj("emergencyReserveText")?.let { setEmergencyReserveText(parseText(it)) }
                        s.obj("clearButtonLabel")?.let { setClearButtonLabel(parseText(it)) }
                        s.obj("footerText")?.let { setFooterText(parseText(it)) }
                    },
            )
        }
        o.arr("projects").forEach { pe ->
            val p = pe.jsonObject
            addProjects(
                MachineProjectView.newBuilder()
                    .setProjectKey(p.str("projectKey"))
                    .setName(p.str("name"))
                    .setPath(p.str("path"))
                    .apply { p.obj("lastActiveText")?.let { setLastActiveText(parseText(it)) } },
            )
        }
        setProjectCount(o.i32("projectCount"))
        o.obj("projectCountText")?.let { setProjectCountText(parseText(it)) }
        o.obj("management")?.let { mg ->
            setManagement(
                MachineManagementActionsView.newBuilder()
                    .setCanRejoin(mg.bool("canRejoin"))
                    .setCanDeleteRecords(mg.bool("canDeleteRecords"))
                    .setCanRemove(mg.bool("canRemove"))
                    .setIsAuthorityDevice(mg.bool("isAuthorityDevice"))
                    .apply { mg.obj("explanationText")?.let { setExplanationText(parseText(it)) } },
            )
        }
    }.build()

    private fun parseDevices(o: JsonObject): DevicesViewModel = DevicesViewModel.newBuilder().apply {
        setIsManager(o.bool("isManager"))
        setOnline(o.bool("online"))
        setUnlinked(o.bool("unlinked"))
        setAuthorityDeviceId(o.str("authorityDeviceId"))
        setAuthorityName(o.str("authorityName"))
        setCanAddDevice(o.bool("canAddDevice"))
        o.obj("addDeviceHint")?.let { setAddDeviceHint(parseText(it)) }
        setMachinesLoading(o.bool("machinesLoading"))
        o.obj("emptyComputersText")?.let { setEmptyComputersText(parseText(it)) }
        o.obj("clientCountText")?.let { setClientCountText(parseText(it)) }
        o.arr("computers").forEach { addComputers(parseMachineListCard(it.jsonObject)) }
        o.arr("clients").forEach { addClients(parseClientRow(it.jsonObject)) }
        o.arr("unfinishedPairings").forEach { addUnfinishedPairings(parseClientRow(it.jsonObject)) }
        o.obj("errorText")?.let { setErrorText(parseText(it)) }
        setClockSkewed(o.bool("clockSkewed"))
        setRecoveryPending(o.bool("recoveryPending"))
        setLeaseEndMs(o.i64("leaseEndMs"))
        setLoadFailed(o.bool("loadFailed"))
    }.build()

    private fun parseClientDetail(o: JsonObject): ClientDetailViewModel = ClientDetailViewModel.newBuilder().apply {
        setDeviceId(o.str("deviceId"))
        setFound(o.bool("found"))
        setIsSelf(o.bool("isSelf"))
        setIsAuthority(o.bool("isAuthority"))
        setMember(o.bool("member"))
        setDisplayName(o.str("displayName"))
        setCustomName(o.str("customName"))
        setDefaultName(o.str("defaultName"))
        setDescription(o.str("description"))
        setShortId(o.str("shortId"))
        setCanRename(o.bool("canRename"))
        setMaxNameLength(o.i32("maxNameLength"))
        setCanRestoreDefault(o.bool("canRestoreDefault"))
        setRenameSaving(o.bool("renameSaving"))
        setShowExitAction(o.bool("showExitAction"))
        setCanRemove(o.bool("canRemove"))
        o.obj("roleLabel")?.let { setRoleLabel(parseText(it)) }
        o.obj("lastUsedText")?.let { setLastUsedText(parseText(it)) }
        o.obj("addedText")?.let { setAddedText(parseText(it)) }
        o.obj("renameError")?.let { setRenameError(parseText(it)) }
        o.obj("managementHint")?.let { setManagementHint(parseText(it)) }
    }.build()

    private fun parseSession(o: JsonObject): SessionViewModel = SessionViewModel.newBuilder().apply {
        setSessionId(o.str("sessionId"))
        o.obj("header")?.let { h ->
            setHeader(
                SessionHeaderView.newBuilder().apply {
                    setFound(h.bool("found"))
                    setSessionsListed(h.bool("sessionsListed"))
                    setLiveLoaded(h.bool("liveLoaded"))
                    setMissing(h.bool("missing"))
                    h.obj("missingText")?.let { setMissingText(parseText(it)) }
                    h.obj("title")?.let { setTitle(parseText(it)) }
                    h.obj("projectName")?.let { setProjectName(parseText(it)) }
                    setProjectKey(h.str("projectKey"))
                    setCwd(h.str("cwd"))
                    setShortPath(h.str("shortPath"))
                    setAgent(h.enum("agent", Agent.AGENT_UNSPECIFIED))
                    setAgentName(h.str("agentName"))
                    setStatus(h.enum("status", SessionDisplayStatus.SESSION_DISPLAY_STATUS_UNSPECIFIED))
                    setTone(h.enum("tone", SessionTone.SESSION_TONE_UNSPECIFIED))
                    h.obj("statusText")?.let { setStatusText(parseText(it)) }
                    setApprovalMode(h.enum("approvalMode", ApprovalMode.APPROVAL_MODE_UNSPECIFIED))
                    setApprovalModeRaw(h.str("approvalModeRaw"))
                    h.obj("approvalModeLabel")?.let { setApprovalModeLabel(parseText(it)) }
                    setTerminalModeOverride(h.bool("terminalModeOverride"))
                    setModel(h.str("model"))
                    setModelLabel(h.str("modelLabel"))
                    setEffort(h.str("effort"))
                    h.obj("effortLabel")?.let { setEffortLabel(parseText(it)) }
                    setModelUnknown(h.bool("modelUnknown"))
                    setContextPercent(h.i32("contextPercent"))
                    setHasContext(h.bool("hasContext") || "contextPercent" in h)
                    h.obj("contextText")?.let { setContextText(parseText(it)) }
                    setMachineId(h.str("machineId"))
                    h.obj("computerName")?.let { setComputerName(parseText(it)) }
                    setComputerOnline(h.bool("computerOnline"))
                    setMultipleMachines(h.bool("multipleMachines"))
                    setConnState(h.enum("connState", ConnState.CONN_STATE_UNSPECIFIED))
                    setDaemonProtocolVersion(h.i32("daemonProtocolVersion"))
                    setClientProtocolVersion(h.i32("clientProtocolVersion"))
                    setForkedFromId(h.str("forkedFromId"))
                    setIsScheduled(h.bool("isScheduled"))
                    setTaskOwned(h.bool("taskOwned"))
                    setLookupFailed(h.bool("lookupFailed"))
                    h.obj("lookupErrorText")?.let { setLookupErrorText(parseText(it)) }
                    setScheduledTitle(h.str("scheduledTitle"))
                    setScheduledTaskId(h.str("scheduledTaskId"))
                    setExited(h.bool("exited"))
                    setEndedAtMs(h.i64("endedAtMs"))
                    setScheduledClosed(h.bool("scheduledClosed"))
                    setIdleClosed(h.bool("idleClosed"))
                    h.obj("endedText")?.let { setEndedText(parseText(it)) }
                    h.obj("capabilities")?.let { cp ->
                        setCapabilities(
                            SessionCapabilitiesView.newBuilder()
                                .setCanSend(cp.bool("canSend"))
                                .setSupportsImages(cp.bool("supportsImages"))
                                .setSupportsFiles(cp.bool("supportsFiles"))
                                .setSupportsSendModes(cp.bool("supportsSendModes"))
                                .setSupportsSharedFiles(cp.bool("supportsSharedFiles"))
                                .setSupportsAlwaysAllow(cp.bool("supportsAlwaysAllow"))
                                .setSupportsSteer(cp.bool("supportsSteer"))
                                .setSupportsCompact(cp.bool("supportsCompact"))
                                .setSupportsFork(cp.bool("supportsFork"))
                                .setSupportsModeSwitch(cp.bool("supportsModeSwitch"))
                                .setSupportsModelSwitch(cp.bool("supportsModelSwitch"))
                                .setSupportsSchedule(cp.bool("supportsSchedule"))
                                .setTerminalAvailable(cp.bool("terminalAvailable"))
                                .setMaxImages(cp.i32("maxImages"))
                                .setMaxFiles(cp.i32("maxFiles"))
                                .setMaxFileBytes(cp.i64("maxFileBytes"))
                                .setCapabilitiesLoaded(cp.bool("capabilitiesLoaded"))
                                .setGalleryAllowsVideo(cp.bool("galleryAllowsVideo"))
                                .setGalleryAcceptMime(cp.str("galleryAcceptMime"))
                                .setMaxFileNameBytes(cp.i32("maxFileNameBytes"))
                                .setMaxFileSizeText(cp.str("maxFileSizeText"))
                                .setOriginalImageMaxBytes(cp.i64("originalImageMaxBytes"))
                                .apply {
                                    cp.obj("imageUploadBlockedReason")?.let { setImageUploadBlockedReason(parseText(it)) }
                                    cp.obj("fileUploadBlockedReason")?.let { setFileUploadBlockedReason(parseText(it)) }
                                    cp.obj("originalImageHint")?.let { setOriginalImageHint(parseText(it)) }
                                    cp.obj("fileMetadataNotice")?.let { setFileMetadataNotice(parseText(it)) }
                                },
                        )
                    }
                },
            )
        }
        o.obj("actions")?.let { a ->
            setActions(
                SessionActionsView.newBuilder().apply {
                    setCanRename(a.bool("canRename"))
                    setCanFork(a.bool("canFork"))
                    a.obj("forkBlockedReason")?.let { setForkBlockedReason(parseText(it)) }
                    setCanCompact(a.bool("canCompact"))
                    setCanInterrupt(a.bool("canInterrupt"))
                    setCanEnd(a.bool("canEnd"))
                    setCanSwitchMode(a.bool("canSwitchMode"))
                    setCanSwitchModel(a.bool("canSwitchModel"))
                    a.obj("modelSwitchBlockedReason")?.let { setModelSwitchBlockedReason(parseText(it)) }
                    a.obj("endConfirmNote")?.let { setEndConfirmNote(parseText(it)) }
                    a.obj("resumeNote")?.let { setResumeNote(parseText(it)) }
                    a.obj("rename")?.let { setRename(parseCommandState(it)) }
                    a.obj("fork")?.let { setFork(parseCommandState(it)) }
                    a.obj("compact")?.let { setCompact(parseCommandState(it)) }
                    a.obj("interrupt")?.let { setInterrupt(parseCommandState(it)) }
                    a.obj("end")?.let { setEnd(parseCommandState(it)) }
                    a.obj("resume")?.let { setResume(parseCommandState(it)) }
                    a.obj("setMode")?.let { setSetMode(parseCommandState(it)) }
                    a.obj("setModel")?.let { setSetModel(parseCommandState(it)) }
                    setModelsLoading(a.bool("modelsLoading"))
                    a.obj("modelsError")?.let { setModelsError(parseText(it)) }
                    setModeLocked(a.bool("modeLocked"))
                    a.obj("modePermissionNotice")?.let { setModePermissionNotice(parseText(it)) }
                    a.obj("forkAction")?.let { fa ->
                        setForkAction(
                            SessionForkActionView.newBuilder()
                                .setVisible(fa.bool("visible"))
                                .setEnabled(fa.bool("enabled"))
                                .setBusy(fa.bool("busy"))
                                .setAbandons(fa.bool("abandons"))
                                .setCanAbandon(fa.bool("canAbandon"))
                                .setNoteError(fa.bool("noteError"))
                                .apply {
                                    fa.obj("label")?.let { setLabel(parseText(it)) }
                                    fa.obj("note")?.let { setNote(parseText(it)) }
                                },
                        )
                    }
                    a.obj("usageEntry")?.let { ue ->
                        setUsageEntry(
                            SessionUsageEntryView.newBuilder()
                                .setVisible(ue.bool("visible"))
                                .setSupported(ue.bool("supported"))
                                .setLoading(ue.bool("loading"))
                                .setSourceUnavailable(ue.bool("sourceUnavailable"))
                                .setHasData(ue.bool("hasData"))
                                .setFallbackThirtyDays(ue.bool("fallbackThirtyDays"))
                                .setFewSpeedSamples(ue.bool("fewSpeedSamples"))
                                .setBackfilling(ue.bool("backfilling"))
                                .setTotalTokens(ue.i64("totalTokens"))
                                .setHasCacheHit(ue.bool("hasCacheHit"))
                                .setCacheHitRate(ue.f64("cacheHitRate"))
                                .setHasSpeed(ue.bool("hasSpeed"))
                                .setRecent10SpeedP50(ue.f64("recent10SpeedP50"))
                                .setRecent10SpeedCount(ue.i64("recent10SpeedCount"))
                                .apply {
                                    ue.obj("subtitle")?.let { setSubtitle(parseText(it)) }
                                    ue.obj("valueText")?.let { setValueText(parseText(it)) }
                                    ue.obj("footnote")?.let { setFootnote(parseText(it)) }
                                },
                        )
                    }
                    a.arr("modeChoices").forEach { mce ->
                        val mc = mce.jsonObject
                        addModeChoices(
                            SessionModeChoiceView.newBuilder()
                                .setMode(mc.enum("mode", ApprovalMode.APPROVAL_MODE_UNSPECIFIED))
                                .setLabel(parseText(mc.obj("label")))
                                .setSelected(mc.bool("selected"))
                                .setAvailable(mc.bool("available"))
                                .setSwitchAfterTurn(mc.bool("switchAfterTurn"))
                                .apply { mc.obj("note")?.let { setNote(parseText(it)) } },
                        )
                    }
                },
            )
        }
        o.obj("dock")?.let { d ->
            setDock(
                SessionDockView.newBuilder().apply {
                    setHasItem(d.bool("hasItem"))
                    setKind(d.enum("kind", SessionDockKind.SESSION_DOCK_KIND_UNSPECIFIED))
                    setItemId(d.str("itemId"))
                    setObservedSeq(d.i64("observedSeq"))
                    setIsSkeleton(d.bool("isSkeleton"))
                    d.obj("title")?.let { setTitle(parseText(it)) }
                    d.obj("summary")?.let { setSummary(parseText(it)) }
                    d.obj("meta")?.let { setMeta(parseText(it)) }
                    d.obj("timeLeftPlain")?.let { setTimeLeftPlain(parseText(it)) }
                    d.obj("command")?.let { setCommand(parseCommandState(it)) }
                    d.obj("approval")?.let { ap ->
                        setApproval(
                            SessionApprovalDockView.newBuilder()
                                .setApprovalId(ap.str("approvalId"))
                                .setToolName(ap.str("toolName"))
                                .setDescription(ap.str("description"))
                                .setCommandPreview(ap.str("commandPreview"))
                                .setBody(ap.str("body"))
                                .setBodyTruncated(ap.bool("bodyTruncated"))
                                .setIsSkeleton(ap.bool("isSkeleton"))
                                .setCanAlwaysAllow(ap.bool("canAlwaysAllow"))
                                .apply {
                                    ap.obj("alwaysLabel")?.let { setAlwaysLabel(parseText(it)) }
                                    ap.obj("denyReasonPlaceholder")?.let { setDenyReasonPlaceholder(parseText(it)) }
                                },
                        )
                    }
                    d.obj("question")?.let { qd ->
                        setQuestion(
                            SessionQuestionDockView.newBuilder()
                                .setQuestionId(qd.str("questionId"))
                                .setActiveIndex(qd.i32("activeIndex"))
                                .setTotalQuestions(qd.i32("totalQuestions"))
                                .apply {
                                    qd.obj("progressText")?.let { setProgressText(parseText(it)) }
                                    qd.obj("question")?.let { setQuestion(parseQuestionStep(it)) }
                                },
                        )
                    }
                    d.obj("plan")?.let { pl ->
                        setPlan(
                            SessionPlanDockView.newBuilder()
                                .setScreenHash(com.google.protobuf.ByteString.copyFromUtf8(pl.str("screenHash")))
                                .setMarkdown(pl.str("markdown"))
                                .setClearContextAvailable(pl.bool("clearContextAvailable"))
                                .apply {
                                    pl.arr("choices").forEach { ce ->
                                        val c = ce.jsonObject
                                        addChoices(
                                            SessionPlanChoiceOptionView.newBuilder()
                                                .setChoice(c.enum("choice", PlanChoice.PLAN_CHOICE_UNSPECIFIED))
                                                .setLabel(parseText(c.obj("label")))
                                                .setRecommended(c.bool("recommended")),
                                        )
                                    }
                                },
                        )
                    }
                    d.obj("terminalPopup")?.let { tp ->
                        setTerminalPopup(
                            SessionTerminalPopupDockView.newBuilder()
                                .setCanGoTerminal(tp.bool("canGoTerminal"))
                                .setIsTrustFolder(tp.bool("isTrustFolder"))
                                .apply {
                                    tp.obj("popupTitle")?.let { setPopupTitle(parseText(it)) }
                                    tp.obj("hint")?.let { setHint(parseText(it)) }
                                },
                        )
                    }
                },
            )
        }
        o.obj("composer")?.let { cp ->
            fun parseTrayItem(itemObj: JsonObject): SessionTrayItemView =
                SessionTrayItemView.newBuilder()
                    .setRequestId(itemObj.str("requestId"))
                    .setSlotKey(itemObj.str("slotKey"))
                    .setGroup(itemObj.enum("group", SessionTrayGroupKind.SESSION_TRAY_GROUP_KIND_UNSPECIFIED))
                    .setTone(itemObj.enum("tone", SessionTrayTone.SESSION_TRAY_TONE_UNSPECIFIED))
                    .setActivity(itemObj.enum("activity", SessionTrayRowActivity.SESSION_TRAY_ROW_ACTIVITY_UNSPECIFIED))
                    .setQueueNumber(itemObj.i32("queueNumber"))
                    .setShowSpinner(itemObj.bool("showSpinner"))
                    .setText(itemObj.str("text"))
                    .setForeignDimmed(itemObj.bool("foreignDimmed"))
                    .setAttachmentCount(itemObj.i32("attachmentCount"))
                    .setImageCount(itemObj.i32("imageCount"))
                    .setFileCount(itemObj.i32("fileCount"))
                    .addAllImageIds(itemObj.arr("imageIds").map { it.jsonPrimitive.content })
                    .addAllFileIds(itemObj.arr("fileIds").map { it.jsonPrimitive.content })
                    .setMode(itemObj.enum("mode", SendMode.SEND_MODE_UNSPECIFIED))
                    .setCanCancel(itemObj.bool("canCancel"))
                    .setCanResume(itemObj.bool("canResume"))
                    .setCanRetry(itemObj.bool("canRetry"))
                    .setCanOpenTerminal(itemObj.bool("canOpenTerminal"))
                    .setCanIgnore(itemObj.bool("canIgnore"))
                    .setCanPromote(itemObj.bool("canPromote"))
                    .setCanRestore(itemObj.bool("canRestore"))
                    .setNetworkActionsDisabled(itemObj.bool("networkActionsDisabled"))
                    .setServerBacked(itemObj.bool("serverBacked"))
                    .setIsSkeleton(itemObj.bool("isSkeleton"))
                    .apply {
                        itemObj.obj("sourceLabel")?.let { setSourceLabel(parseText(it)) }
                        itemObj.obj("reasonText")?.let { setReasonText(parseText(it)) }
                    }
                    .build()

            setComposer(
                SessionComposerView.newBuilder().apply {
                    setEnabled(cp.bool("enabled"))
                    cp.obj("placeholder")?.let { setPlaceholder(parseText(it)) }
                    cp.obj("disabledReason")?.let { setDisabledReason(parseText(it)) }
                    setSupportsQueueMode(cp.bool("supportsQueueMode"))
                    setDefaultSendMode(cp.enum("defaultSendMode", SendMode.SEND_MODE_UNSPECIFIED))
                    setIsDefaultPlaceholder(cp.bool("isDefaultPlaceholder"))
                    cp.obj("slashWarningTitle")?.let { setSlashWarningTitle(parseText(it)) }
                    cp.obj("slashWarningText")?.let { setSlashWarningText(parseText(it)) }
                    setCanAttachImages(cp.bool("canAttachImages"))
                    setCanAttachFiles(cp.bool("canAttachFiles"))
                    setHasActiveTurn(cp.bool("hasActiveTurn"))
                    setInterruptVisible(cp.bool("interruptVisible"))
                    addAllRetainedAttachmentRequestIds(cp.arr("retainedAttachmentRequestIds").map { it.jsonPrimitive.content })
                    cp.obj("pendingTray")?.let { pt ->
                        setPendingTray(
                            SessionPendingTrayView.newBuilder().apply {
                                setTotalCount(pt.i32("totalCount"))
                                setFailedCount(pt.i32("failedCount"))
                                setOffline(pt.bool("offline"))
                                pt.arr("groups").forEach { ge ->
                                    val g = ge.jsonObject
                                    addGroups(
                                        SessionTrayGroupView.newBuilder()
                                            .setKind(g.enum("kind", SessionTrayGroupKind.SESSION_TRAY_GROUP_KIND_UNSPECIFIED))
                                            .setTone(g.enum("tone", SessionTrayTone.SESSION_TRAY_TONE_UNSPECIFIED))
                                            .setTotalCount(g.i32("totalCount"))
                                            .setCanCancelAll(g.bool("canCancelAll"))
                                            .apply {
                                                g.obj("title")?.let { setTitle(parseText(it)) }
                                                g.obj("headerText")?.let { setHeaderText(parseText(it)) }
                                                g.arr("items").forEach { ie -> addItems(parseTrayItem(ie.jsonObject)) }
                                            },
                                    )
                                }
                                pt.arr("orderedItems").forEach { ie -> addOrderedItems(parseTrayItem(ie.jsonObject)) }
                                pt.obj("undo")?.let { u ->
                                    setUndo(
                                        SessionTrayUndoView.newBuilder()
                                            .setVisible(u.bool("visible"))
                                            .setCount(u.i32("count"))
                                            .setCanUndo(u.bool("canUndo"))
                                            .apply { u.obj("label")?.let { setLabel(parseText(it)) } },
                                    )
                                }
                            },
                        )
                    }
                },
            )
        }
        o.obj("timeline")?.let { tl ->
            setTimeline(
                SessionTimelineView.newBuilder().apply {
                    tl.arr("items").forEach { ie -> addItems(parseTimelineItem(ie.jsonObject)) }
                    setWindowFirstSeq(tl.i64("windowFirstSeq"))
                    setWindowLastSeq(tl.i64("windowLastSeq"))
                    setAnchorSeq(tl.i64("anchorSeq"))
                    setAnchorItemId(tl.str("anchorItemId"))
                    setTotalItems(tl.i32("totalItems"))
                    setHasEarlier(tl.bool("hasEarlier"))
                    setCanLoadEarlier(tl.bool("canLoadEarlier"))
                    setHasGap(tl.bool("hasGap"))
                    setLoaded(tl.bool("loaded"))
                    setLoading(tl.bool("loading"))
                    setFetching(tl.bool("fetching"))
                    setAwaitingCache(tl.bool("awaitingCache"))
                    setNextBeforeSeq(tl.i64("nextBeforeSeq"))
                    tl.arr("viewerImages").forEach { ve -> addViewerImages(parseViewerImage(ve.jsonObject)) }
                },
            )
        }
        o.obj("imageViewer")?.let { iv ->
            setImageViewer(
                SessionImageViewerView.newBuilder().apply {
                    setOpen(iv.bool("open"))
                    setCurrentIndex(iv.i32("currentIndex"))
                    setTotalCount(iv.i32("totalCount"))
                    setHasPrev(iv.bool("hasPrev"))
                    setHasNext(iv.bool("hasNext"))
                    iv.obj("positionText")?.let { setPositionText(parseText(it)) }
                    iv.obj("currentImage")?.let { setCurrentImage(parseViewerImage(it)) }
                    iv.obj("emptyText")?.let { setEmptyText(parseText(it)) }
                },
            )
        }
    }.build()

    private fun parseViewerImage(o: JsonObject): SessionViewerImageView = SessionViewerImageView.newBuilder().apply {
        setViewerIndex(o.i32("viewerIndex"))
        setSeq(o.i64("seq"))
        setEventImageIndex(o.i32("eventImageIndex"))
        setTimestampMs(o.i64("timestampMs"))
        setKind(o.enum("kind", SessionImageSourceKind.SESSION_IMAGE_SOURCE_KIND_UNSPECIFIED))
        o.obj("ref")?.let { setRef(parseImageRef(it)) }
        setToolTitle(o.str("toolTitle"))
        setRevisedPrompt(o.str("revisedPrompt"))
        o.obj("sourceLabel")?.let { setSourceLabel(parseText(it)) }
        setSizeText(o.str("sizeText"))
        setAspectRatio(o.f64("aspectRatio"))
        setClampedAspectRatio(o.f64("clampedAspectRatio"))
        setHasDisplayCopy(o.bool("hasDisplayCopy"))
        setDisplayName(o.str("displayName"))
        setDisplayPath(o.str("displayPath"))
        setFileExtension(o.str("fileExtension"))
        setMimeType(o.str("mimeType"))
    }.build()

    private fun parseCommandState(o: JsonObject): SessionCommandState = SessionCommandState.newBuilder().apply {
        setSlotKey(o.str("slotKey"))
        setRequestId(o.str("requestId"))
        setStatus(o.enum("status", SessionCommandStatus.SESSION_COMMAND_STATUS_UNSPECIFIED))
        setBusy(o.bool("busy"))
        setFrozen(o.bool("frozen"))
        setCanRetry(o.bool("canRetry"))
        setCanAbandon(o.bool("canAbandon"))
        setErrorCode(o.str("errorCode"))
        o.obj("statusText")?.let { setStatusText(parseText(it)) }
        o.obj("errorText")?.let { setErrorText(parseText(it)) }
    }.build()

    private fun parseQuestionStep(q: JsonObject): SessionQuestionStepView = SessionQuestionStepView.newBuilder().apply {
        setHeader(q.str("header"))
        setQuestionText(q.str("questionText"))
        setMultiSelect(q.bool("multiSelect"))
        setCanFreeText(q.bool("canFreeText"))
        q.arr("options").forEach { oe ->
            val o = oe.jsonObject
            addOptions(
                SessionQuestionOptionView.newBuilder()
                    .setOptionNum(o.i32("optionNum"))
                    .setLabel(o.str("label"))
                    .setDescription(o.str("description"))
                    .setRecommended(o.bool("recommended")),
            )
        }
    }.build()

    private fun parseTimelineItem(o: JsonObject): SessionTimelineItem = SessionTimelineItem.newBuilder().apply {
        setId(o.str("id"))
        setSeq(o.i64("seq"))
        setEndSeq(o.i64("endSeq"))
        setTimestampMs(o.i64("timestampMs"))
        o.obj("userMessage")?.let { u ->
            setUserMessage(
                SessionUserMessageItem.newBuilder().apply {
                    setText(u.str("text"))
                    setTypedLocally(u.bool("typedLocally"))
                    setIsScheduled(u.bool("isScheduled"))
                    setScheduledTaskId(u.str("scheduledTaskId"))
                    setScheduledRunId(u.str("scheduledRunId"))
                    u.obj("originCaption")?.let { setOriginCaption(parseText(it)) }
                    u.obj("origin")?.let { orig ->
                        setOrigin(
                            Origin.newBuilder()
                                .setKind(orig.enum("kind", OriginKind.ORIGIN_KIND_UNSPECIFIED))
                                .setDeviceId(orig.str("deviceId"))
                                .setScheduledTaskId(orig.str("scheduledTaskId"))
                                .setScheduledRunId(orig.str("scheduledRunId")),
                        )
                    }
                    u.arr("images").forEach { ie -> addImages(parseImageRef(ie.jsonObject)) }
                    u.arr("files").forEach { fe ->
                        val f = fe.jsonObject
                        addFiles(
                            SessionFileRefView.newBuilder()
                                .setName(f.str("name"))
                                .setSizeBytes(f.i64("sizeBytes"))
                                .setMimeType(f.str("mimeType"))
                                .setDisplayName(f.str("displayName"))
                                .setNameHead(f.str("nameHead"))
                                .setNameTail(f.str("nameTail"))
                                .apply { f.obj("sizeText")?.let { setSizeText(parseText(it)) } },
                        )
                    }
                    u.arr("textSegments").forEach { se ->
                        val s = se.jsonObject
                        addTextSegments(
                            SessionMessageTextSegment.newBuilder()
                                .setText(s.str("text"))
                                .setImageIndex(s.i32("imageIndex"))
                                .apply { s.obj("imageLabel")?.let { setImageLabel(parseText(it)) } },
                        )
                    }
                },
            )
        }
        o.obj("agentMessage")?.let { a ->
            setAgentMessage(SessionAgentMessageItem.newBuilder().setMarkdown(a.str("markdown")))
        }
        o.obj("toolCall")?.let { tc ->
            setToolCall(parseToolCallItem(tc))
        }
        o.obj("toolGroup")?.let { tg ->
            setToolGroup(
                SessionToolGroupItem.newBuilder().apply {
                    tg.obj("summary")?.let { setSummary(parseText(it)) }
                    setCommandCount(tg.i32("commandCount"))
                    tg.arr("calls").forEach { ce -> addCalls(parseToolCallItem(ce.jsonObject)) }
                },
            )
        }
        o.obj("sharedFiles")?.let { sf ->
            setSharedFiles(
                SessionSharedFilesItem.newBuilder().apply {
                    sf.arr("files").forEach { fe ->
                        val f = fe.jsonObject
                        addFiles(
                            SessionSharedFileItem.newBuilder()
                                .setFileId(f.str("fileId"))
                                .setName(f.str("name"))
                                .setPath(f.str("path"))
                                .setSizeBytes(f.i64("sizeBytes"))
                                .setAvailable(f.bool("available"))
                                .setDisplayName(f.str("displayName"))
                                .setNameHead(f.str("nameHead"))
                                .setNameTail(f.str("nameTail"))
                                .setKind(f.enum("kind", SharedFileKind.SHARED_FILE_KIND_UNSPECIFIED))
                                .setDisplayPath(f.str("displayPath"))
                                .setStatus(f.enum("status", SharedFileCardStatus.SHARED_FILE_CARD_STATUS_UNSPECIFIED))
                                .setSubtitleIsError(f.bool("subtitleIsError"))
                                .setProgress(f.f64("progress"))
                                .setPrimaryAction(f.enum("primaryAction", SharedFilePrimaryAction.SHARED_FILE_PRIMARY_ACTION_UNSPECIFIED))
                                .setPrimaryEnabled(f.bool("primaryEnabled"))
                                .setSupportsAlbumSave(f.bool("supportsAlbumSave"))
                                .setHasMediaDuration(f.bool("hasMediaDuration"))
                                .setWebPreviewKind(f.enum("webPreviewKind", SharedFileWebPreviewKind.SHARED_FILE_WEB_PREVIEW_KIND_UNSPECIFIED))
                                .setWebPreviewMime(f.str("webPreviewMime"))
                                .setDownloadedBytes(f.i64("downloadedBytes"))
                                .setWaitingReconnect(f.bool("waitingReconnect"))
                                .setDurationText(f.str("durationText"))
                                .apply {
                                    f.str("sha256").takeIf { it.isNotEmpty() }?.let {
                                        setSha256(com.google.protobuf.ByteString.copyFrom(java.util.Base64.getDecoder().decode(it)))
                                    }
                                    f.obj("kindLabel")?.let { setKindLabel(parseText(it)) }
                                    f.obj("sizeText")?.let { setSizeText(parseText(it)) }
                                    f.obj("sourceCaption")?.let { setSourceCaption(parseText(it)) }
                                    f.obj("subtitle")?.let { setSubtitle(parseText(it)) }
                                    f.obj("a11yLabel")?.let { setA11YLabel(parseText(it)) }
                                    f.obj("primaryLabel")?.let { setPrimaryLabel(parseText(it)) }
                                    f.obj("primaryDisabledReason")?.let { setPrimaryDisabledReason(parseText(it)) }
                                    f.arr("menuItems").forEach { me ->
                                        val m = me.jsonObject
                                        addMenuItems(
                                            SharedFileMenuItemView.newBuilder()
                                                .setAction(m.enum("action", SharedFileMenuAction.SHARED_FILE_MENU_ACTION_UNSPECIFIED))
                                                .setDestructive(m.bool("destructive"))
                                                .setEnabled(m.bool("enabled"))
                                                .apply { m.obj("label")?.let { setLabel(parseText(it)) } },
                                        )
                                    }
                                },
                        )
                    }
                },
            )
        }
        o.obj("generatedImages")?.let { gi ->
            setGeneratedImages(
                SessionGeneratedImagesItem.newBuilder().apply {
                    setRevisedPrompt(gi.str("revisedPrompt"))
                    gi.arr("images").forEach { ie -> addImages(parseImageRef(ie.jsonObject)) }
                },
            )
        }
        o.obj("asyncQuestion")?.let { aq ->
            setAsyncQuestion(
                SessionAsyncQuestionItem.newBuilder().apply {
                    aq.arr("questions").forEach { qe -> addQuestions(parseQuestionStep(qe.jsonObject)) }
                },
            )
        }
        o.obj("plan")?.let { p ->
            setPlan(SessionPlanItem.newBuilder().setMarkdown(p.str("markdown")))
        }
        o.obj("note")?.let { n ->
            setNote(
                SessionNoteItem.newBuilder().apply {
                    setKind(n.enum("kind", SessionNoteKind.SESSION_NOTE_KIND_UNSPECIFIED))
                    setTone(n.enum("tone", SessionNoteTone.SESSION_NOTE_TONE_UNSPECIFIED))
                    n.obj("text")?.let { setText(parseText(it)) }
                },
            )
        }
        o.obj("notifyReceipt")?.let { r ->
            setNotifyReceipt(
                SessionNotifyReceiptItem.newBuilder().apply {
                    r.obj("label")?.let { setLabel(parseText(it)) }
                    r.obj("time")?.let { setTime(parseText(it)) }
                    setPreview(r.str("preview"))
                    setBody(r.str("body"))
                    setExpandable(r.bool("expandable"))
                    setScheduledRunId(r.str("scheduledRunId"))
                },
            )
        }
        o.obj("errorLine")?.let { e ->
            setErrorLine(
                SessionErrorLineItem.newBuilder().apply {
                    setRawReason(e.str("rawReason"))
                    e.obj("text")?.let { setText(parseText(it)) }
                },
            )
        }
        o.obj("inputState")?.let { ins ->
            setInputState(
                SessionInputStateItem.newBuilder().apply {
                    setKind(ins.enum("kind", SessionInputNoticeKind.SESSION_INPUT_NOTICE_KIND_UNSPECIFIED))
                    setRequestId(ins.str("requestId"))
                    setPreview(ins.str("preview"))
                    ins.obj("statusText")?.let { setStatusText(parseText(it)) }
                    ins.obj("reasonText")?.let { setReasonText(parseText(it)) }
                },
            )
        }
        o.obj("forkMarker")?.let { fm ->
            setForkMarker(
                SessionForkItem.newBuilder().apply {
                    setSourceSessionId(fm.str("sourceSessionId"))
                    fm.obj("sourceTitle")?.let { setSourceTitle(parseText(it)) }
                    fm.obj("text")?.let { setText(parseText(it)) }
                },
            )
        }
        o.obj("turnEnd")?.let { te ->
            setTurnEnd(
                SessionTurnEndItem.newBuilder().apply {
                    setDurationMs(te.i64("durationMs"))
                    setInterrupted(te.bool("interrupted"))
                    setOutcome(te.enum("outcome", TurnOutcome.TURN_OUTCOME_UNSPECIFIED))
                    te.obj("label")?.let { setLabel(parseText(it)) }
                },
            )
        }
        o.obj("quietScheduledGroup")?.let { qsg ->
            setQuietScheduledGroup(
                SessionQuietScheduledGroupItem.newBuilder().apply {
                    setGroupId(qsg.str("groupId"))
                    setRunCount(qsg.i32("runCount"))
                    setExpanded(qsg.bool("expanded"))
                    setScheduledTaskId(qsg.str("scheduledTaskId"))
                    qsg.arr("runIds").forEach { addRunIds(it.jsonPrimitive.content) }
                    qsg.arr("runTimesMs").forEach { addRunTimesMs(it.jsonPrimitive.longOrNull ?: 0L) }
                    qsg.obj("title")?.let { setTitle(parseText(it)) }
                    qsg.arr("items").forEach { ie -> addItems(parseTimelineItem(ie.jsonObject)) }
                },
            )
        }
    }.build()

    private fun parseToolCallItem(o: JsonObject): SessionToolCallItem = SessionToolCallItem.newBuilder().apply {
        setCallId(o.str("callId"))
        setToolKind(o.enum("toolKind", ToolKind.TOOL_KIND_UNSPECIFIED))
        setItemType(o.str("itemType"))
        setTitle(o.str("title"))
        setRawInput(o.str("rawInput"))
        setDetail(o.str("detail"))
        setStatus(o.enum("status", SessionToolStatus.SESSION_TOOL_STATUS_UNSPECIFIED))
        setExitCode(o.i32("exitCode"))
        setHasExitCode("exitCode" in o)
        setElapsedMs(o.i64("elapsedMs"))
        setFrozenAtMs(o.i64("frozenAtMs"))
        setNoResult(o.bool("noResult"))
        setDecisionAllowed(o.bool("decisionAllowed"))
        o.obj("decisionLabel")?.let { setDecisionLabel(parseText(it)) }
        o.obj("backgroundLabel")?.let { setBackgroundLabel(parseText(it)) }
        setSeq(o.i64("seq"))
        o.arr("images").forEach { ie -> addImages(parseImageRef(ie.jsonObject)) }
    }.build()

    private fun parseImageRef(o: JsonObject): ImageRef = ImageRef.newBuilder().apply {
        setImageId(o.str("imageId"))
        setSync(o.enum("sync", io.github.lewin671.remoteagent.proto.ImageSync.IMAGE_SYNC_UNSPECIFIED))
        setCliIndex(o.i32("cliIndex"))
        setWidth(o.i32("width"))
        setHeight(o.i32("height"))
        setPath(o.str("path"))
        setFormat(o.enum("format", io.github.lewin671.remoteagent.proto.ImageFormat.IMAGE_FORMAT_UNSPECIFIED))
        setSource(o.enum("source", io.github.lewin671.remoteagent.proto.ImageSource.IMAGE_SOURCE_UNSPECIFIED))
    }.build()

    private fun parseConnectionStatus(o: JsonObject): ConnectionStatusView = ConnectionStatusView.newBuilder()
        .setState(o.enum("state", ConnState.CONN_STATE_UNSPECIFIED))
        .setBadgeText(parseText(o.obj("badgeText")))
        .setDetailText(parseText(o.obj("detailText")))
        .setLivePushConnected(o.bool("livePushConnected"))
        .setReason(o.str("reason"))
        .setDisconnectedAtMs(o.i64("disconnectedAtMs"))
        .build()

    private fun parseScheduleTaskActions(o: JsonObject): ScheduleTaskActionsView = ScheduleTaskActionsView.newBuilder().apply {
        setCanRunNow(o.bool("canRunNow"))
        o.obj("runNowDisabledReason")?.let { setRunNowDisabledReason(parseText(it)) }
        setShowToggleAction(o.bool("showToggleAction"))
        setIsResumeAction(o.bool("isResumeAction"))
        o.obj("toggleLabel")?.let { setToggleLabel(parseText(it)) }
        setCanToggle(o.bool("canToggle"))
        o.obj("toggleDisabledReason")?.let { setToggleDisabledReason(parseText(it)) }
        setCanDelete(o.bool("canDelete"))
        o.obj("deleteDisabledReason")?.let { setDeleteDisabledReason(parseText(it)) }
        setCanMarkRead(o.bool("canMarkRead"))
        setCanMarkUnread(o.bool("canMarkUnread"))
        setCanRename(o.bool("canRename"))
        o.obj("renameDisabledReason")?.let { setRenameDisabledReason(parseText(it)) }
        setCanEditDefinition(o.bool("canEditDefinition"))
        o.obj("editDefinitionDisabledReason")?.let { setEditDefinitionDisabledReason(parseText(it)) }
        setCanToggleNotify(o.bool("canToggleNotify"))
        o.obj("notifyDisabledReason")?.let { setNotifyDisabledReason(parseText(it)) }
    }.build()

    private fun parseScheduleTakeover(o: JsonObject): ScheduleTakeoverView = ScheduleTakeoverView.newBuilder().apply {
        setHasTakeover(o.bool("hasTakeover"))
        setSessionId(o.str("sessionId"))
        setVia(o.enum("via", ScheduleTakeoverVia.SCHEDULE_TAKEOVER_VIA_UNSPECIFIED))
        o.obj("label")?.let { setLabel(parseText(it)) }
    }.build()

    private fun parseScheduleMutation(o: JsonObject): ScheduleMutationView = ScheduleMutationView.newBuilder().apply {
        setBusy(o.bool("busy"))
        setFrozen(o.bool("frozen"))
        setExpired(o.bool("expired"))
        o.obj("error")?.let { setError(parseText(it)) }
        o.obj("notice")?.let { setNotice(parseText(it)) }
        setCanRetry(o.bool("canRetry"))
        setCanAbandon(o.bool("canAbandon"))
        o.obj("frozenHint")?.let { setFrozenHint(parseText(it)) }
    }.build()

    private fun parseScheduleItem(o: JsonObject): ScheduleTaskItemView = ScheduleTaskItemView.newBuilder().apply {
        setMachineId(o.str("machineId"))
        setTaskId(o.str("taskId"))
        setTitle(o.str("title"))
        setGroup(o.enum("group", ScheduleDisplayGroup.SCHEDULE_DISPLAY_GROUP_UNSPECIFIED))
        setStatus(o.enum("status", ScheduleTaskStatus.SCHEDULE_TASK_STATUS_UNSPECIFIED))
        setTone(o.enum("tone", ScheduleTone.SCHEDULE_TONE_UNSPECIFIED))
        setHasUnread(o.bool("hasUnread"))
        setInProgress(o.bool("inProgress"))
        setPaused(o.bool("paused"))
        setMachineOffline(o.bool("machineOffline"))
        setSelected(o.bool("selected"))
        o.obj("timeText")?.let { setTimeText(parseText(it)) }
        o.obj("attentionLead")?.let { setAttentionLead(parseText(it)) }
        o.obj("attentionReason")?.let { setAttentionReason(parseText(it)) }
        o.obj("preview")?.let { setPreview(parseText(it)) }
        setPreviewIsPlaceholder(o.bool("previewIsPlaceholder"))
        o.obj("metadata")?.let { setMetadata(parseText(it)) }
        o.obj("timingText")?.let { setTimingText(parseText(it)) }
        o.obj("nextRunText")?.let { setNextRunText(parseText(it)) }
        setNextRunMs(o.i64("nextRunMs"))
        setHasNotification(o.bool("hasNotification"))
        setIsAgentCreated(o.bool("isAgentCreated"))
        o.obj("takeover")?.let { setTakeover(parseScheduleTakeover(it)) }
        setReadOnly(o.bool("readOnly"))
        o.obj("readOnlyReason")?.let { setReadOnlyReason(parseText(it)) }
        o.obj("actions")?.let { setActions(parseScheduleTaskActions(it)) }
        o.obj("menuSubtitle")?.let { setMenuSubtitle(parseText(it)) }
    }.build()

    private fun parseSchedules(o: JsonObject): SchedulesViewModel = SchedulesViewModel.newBuilder().apply {
        setAttentionCount1(o.i32("attentionCount"))
        setHasNewResults(o.bool("hasNewResults"))
        setAttentionBadgeText(o.str("attentionBadgeText"))
        setCanCreate(o.bool("canCreate"))
        o.obj("connection")?.let { setConnection(parseConnectionStatus(it)) }
        o.obj("headline")?.let { h ->
            setHeadline(
                ScheduleHeadlineView.newBuilder()
                    .setKind(h.enum("kind", ScheduleHeadlineKind.SCHEDULE_HEADLINE_KIND_UNSPECIFIED))
                    .setHighlightCount(h.i32("highlightCount"))
                    .apply {
                        h.obj("title")?.let { setTitle(parseText(it)) }
                        h.obj("sub")?.let { setSub(parseText(it)) }
                    },
            )
        }
        setAnyMachineSupported(o.bool("anyMachineSupported"))
        o.obj("unsupportedNotice")?.let { setUnsupportedNotice(parseText(it)) }
        o.obj("offlineNotice")?.let { setOfflineNotice(parseText(it)) }
        o.arr("machineErrors").forEach { me ->
            val obj = me.jsonObject
            addMachineErrors(
                ScheduleMachineErrorView.newBuilder()
                    .setMachineId(obj.str("machineId"))
                    .setMachineName(obj.str("machineName"))
                    .setCanRetry(obj.bool("canRetry"))
                    .apply { obj.obj("error")?.let { setError(parseText(it)) } },
            )
        }
        setLoading(o.bool("loading"))
        setShowEmptyHint(o.bool("showEmptyHint"))
        o.obj("emptyHint")?.let { setEmptyHint(parseText(it)) }
        o.arr("attention").forEach { addAttention14(parseScheduleItem(it.jsonObject)) }
        o.arr("fresh").forEach { addFresh(parseScheduleItem(it.jsonObject)) }
        o.arr("others").forEach { addOthers(parseScheduleItem(it.jsonObject)) }
        o.arr("ended").forEach { addEnded(parseScheduleItem(it.jsonObject)) }
        setTotalEnded(o.i32("totalEnded"))
        setHasMoreEnded(o.bool("hasMoreEnded"))
        setEndedExpanded(o.bool("endedExpanded"))
        o.obj("endedLabel")?.let { setEndedLabel(parseText(it)) }
        o.obj("othersSectionTitle")?.let { setOthersSectionTitle(parseText(it)) }
        setCanMarkAllRead(o.bool("canMarkAllRead"))
        setSelectedMachineId(o.str("selectedMachineId"))
        setSelectedTaskId(o.str("selectedTaskId"))
    }.build()

    private fun parseScheduleDetail(o: JsonObject): ScheduleDetailViewModel = ScheduleDetailViewModel.newBuilder().apply {
        setFound(o.bool("found"))
        setLoading(o.bool("loading"))
        o.obj("notFoundText")?.let { setNotFoundText(parseText(it)) }
        setMachineId(o.str("machineId"))
        setTaskId(o.str("taskId"))
        setRevision(o.i64("revision"))
        setTitle(o.str("title"))
        o.obj("subtitle")?.let { setSubtitle(parseText(it)) }
        o.obj("connection")?.let { setConnection(parseConnectionStatus(it)) }
        setMachineOnline(o.bool("machineOnline"))
        setMachineSupported(o.bool("machineSupported"))
        setReadOnly(o.bool("readOnly"))
        o.obj("readOnlyNotice")?.let { setReadOnlyNotice(parseText(it)) }
        o.obj("machineError")?.let { setMachineError(parseText(it)) }
        setCanRetryMachineList(o.bool("canRetryMachineList"))
        o.obj("mutation")?.let { setMutation(parseScheduleMutation(it)) }
        o.obj("attention")?.let { a ->
            setAttention(
                ScheduleAttentionView.newBuilder()
                    .setShow(a.bool("show"))
                    .setShowPrimaryAction(a.bool("showPrimaryAction"))
                    .setPrimaryActionKind(a.enum("primaryActionKind", ScheduleRecoveryActionKind.SCHEDULE_RECOVERY_ACTION_KIND_UNSPECIFIED))
                    .setPrimaryActionEnabled(a.bool("primaryActionEnabled"))
                    .setShowEnableCliAction(a.bool("showEnableCliAction"))
                    .setDisabledAgent(a.enum("disabledAgent", Agent.AGENT_UNSPECIFIED))
                    .apply {
                        a.obj("message")?.let { setMessage(parseText(it)) }
                        a.obj("primaryActionLabel")?.let { setPrimaryActionLabel(parseText(it)) }
                        a.obj("primaryActionDisabledReason")?.let { setPrimaryActionDisabledReason(parseText(it)) }
                    },
            )
        }
        o.obj("activeRun")?.let { ar ->
            setActiveRun(
                ScheduleActiveRunView.newBuilder()
                    .setHasActive(ar.bool("hasActive"))
                    .setRunId(ar.str("runId"))
                    .setSessionId(ar.str("sessionId"))
                    .setFirstSeq(ar.i64("firstSeq"))
                    .setStatus(ar.enum("status", ScheduleRunStatus.SCHEDULE_RUN_STATUS_UNSPECIFIED))
                    .setCanOpenSession(ar.bool("canOpenSession"))
                    .apply { ar.obj("statusText")?.let { setStatusText(parseText(it)) } },
            )
        }
        o.obj("resultCard")?.let { rc ->
            setResultCard(
                ScheduleResultCardView.newBuilder()
                    .setHasResult(rc.bool("hasResult"))
                    .setRunId(rc.str("runId"))
                    .setSessionId(rc.str("sessionId"))
                    .setFirstSeq(rc.i64("firstSeq"))
                    .setUnreadOnEntry(rc.bool("unreadOnEntry"))
                    .setHasNewerResult(rc.bool("hasNewerResult"))
                    .setResultTitle(rc.str("resultTitle"))
                    .setMarkdownBody(rc.str("markdownBody"))
                    .setResultLoading(rc.bool("resultLoading"))
                    .setShowConversationAction(rc.bool("showConversationAction"))
                    .apply {
                        rc.obj("timeText")?.let { setTimeText(parseText(it)) }
                        rc.obj("fallbackReasonText")?.let { setFallbackReasonText(parseText(it)) }
                    },
            )
        }
        o.arr("earlierGroups").forEach { ge ->
            val g = ge.jsonObject
            addEarlierGroups(
                ScheduleHistoryGroupView.newBuilder()
                    .setGroupKey(g.str("groupKey"))
                    .setFoldedQuiet(g.bool("foldedQuiet"))
                    .setExpanded(g.bool("expanded"))
                    .apply {
                        g.obj("foldedTitle")?.let { setFoldedTitle(parseText(it)) }
                        g.obj("foldedSummary")?.let { setFoldedSummary(parseText(it)) }
                        g.arr("runs").forEach { re ->
                            val r = re.jsonObject
                            addRuns(
                                ScheduleRunRowView.newBuilder()
                                    .setRunId(r.str("runId"))
                                    .setSessionId(r.str("sessionId"))
                                    .setFirstSeq(r.i64("firstSeq"))
                                    .setStatus(r.enum("status", ScheduleRunStatus.SCHEDULE_RUN_STATUS_UNSPECIFIED))
                                    .setUnreadOnEntry(r.bool("unreadOnEntry"))
                                    .setManual(r.bool("manual"))
                                    .setNotified(r.bool("notified"))
                                    .setCanOpenSession(r.bool("canOpenSession"))
                                    .apply {
                                        r.obj("notificationSourceText")?.let { setNotificationSourceText(parseText(it)) }
                                        r.obj("timeText")?.let { setTimeText(parseText(it)) }
                                        r.obj("summaryText")?.let { setSummaryText(parseText(it)) }
                                    },
                            )
                        }
                    },
            )
        }
        setTotalEarlierRuns(o.i32("totalEarlierRuns"))
        setHasMoreEarlierRuns(o.bool("hasMoreEarlierRuns"))
        setShowAllRuns(o.bool("showAllRuns"))
        o.obj("earlierCountText")?.let { setEarlierCountText(parseText(it)) }
        o.obj("viewAllText")?.let { setViewAllText(parseText(it)) }
        setEmptyHistory(o.bool("emptyHistory"))
        o.obj("emptyHistoryText")?.let { setEmptyHistoryText(parseText(it)) }
        setMessage(o.str("message"))
        o.obj("timingText")?.let { setTimingText(parseText(it)) }
        setNotifyOnFinish(o.bool("notifyOnFinish"))
        setHasLimits(o.bool("hasLimits"))
        o.obj("limitsText")?.let { setLimitsText(parseText(it)) }
        setTargetType(o.enum("targetType", ScheduleTargetType.SCHEDULE_TARGET_TYPE_UNSPECIFIED))
        setTargetSessionId(o.str("targetSessionId"))
        o.obj("targetLabel")?.let { setTargetLabel(parseText(it)) }
        o.obj("targetValue")?.let { setTargetValue(parseText(it)) }
        setCanOpenTargetSession(o.bool("canOpenTargetSession"))
        o.obj("permissionsText")?.let { setPermissionsText(parseText(it)) }
        setPermissionsIsDanger(o.bool("permissionsIsDanger"))
        setCanEditPermissions(o.bool("canEditPermissions"))
        setTaskName(o.str("taskName"))
        o.obj("creationFooter")?.let { setCreationFooter(parseText(it)) }
        o.obj("runOnceFooter")?.let { setRunOnceFooter(parseText(it)) }
        setSource(o.enum("source", ScheduleSource.SCHEDULE_SOURCE_UNSPECIFIED))
        o.obj("takeover")?.let { setTakeover(parseScheduleTakeover(it)) }
        setScopedSessionId(o.str("scopedSessionId"))
        setScopedMode(o.str("scopedMode"))
        o.obj("actions")?.let { setActions(parseScheduleTaskActions(it)) }
        o.obj("menuSubtitle")?.let { setMenuSubtitle(parseText(it)) }
    }.build()

    private fun parseScheduleForm(o: JsonObject): ScheduleFormViewModel = ScheduleFormViewModel.newBuilder().apply {
        setFormKey(o.str("formKey"))
        setMode(o.enum("mode", ScheduleFormMode.SCHEDULE_FORM_MODE_UNSPECIFIED))
        setOpen(o.bool("open"))
        setMachineId(o.str("machineId"))
        setTaskId(o.str("taskId"))
        setSessionId(o.str("sessionId"))
        setProjectKey(o.str("projectKey"))
        setLockMachine(o.bool("lockMachine"))
        setMessage(o.str("message"))
        setMessageValid(o.bool("messageValid"))
        setMessageTooLong(o.bool("messageTooLong"))
        o.obj("messageError")?.let { setMessageError(parseText(it)) }
        setTitle(o.str("title"))
        o.obj("titlePlaceholder")?.let { setTitlePlaceholder(parseText(it)) }
        setAutoTitle(o.str("autoTitle"))
        setNotifyOnFinish(o.bool("notifyOnFinish"))
        o.obj("timingSummary")?.let { setTimingSummary(parseText(it)) }
        o.obj("timing")?.let { t ->
            setTiming(
                ScheduleTimingDraftView.newBuilder()
                    .setKind(t.enum("kind", ScheduleTimingKind.SCHEDULE_TIMING_KIND_UNSPECIFIED))
                    .setAfterTurnUnselected(t.bool("afterTurnUnselected"))
                    .setDate(t.str("date"))
                    .setTime(t.str("time"))
                    .setZone(t.str("zone"))
                    .setWorkdays(t.bool("workdays"))
                    .setInterval(t.str("interval"))
                    .setUnit(t.enum("unit", ScheduleIntervalUnit.SCHEDULE_INTERVAL_UNIT_UNSPECIFIED))
                    .setEnd(t.enum("end", ScheduleTimingEnd.SCHEDULE_TIMING_END_UNSPECIFIED))
                    .setCount(t.str("count"))
                    .setUntilDate(t.str("untilDate"))
                    .setUntilMs(t.i64("untilMs"))
                    .setShowIntervalHint(t.bool("showIntervalHint"))
                    .setTimingDoneEnabled(t.bool("timingDoneEnabled"))
                    .setTimingApplied(t.bool("timingApplied"))
                    .apply {
                        t.obj("afterTurnHint")?.let { setAfterTurnHint(parseText(it)) }
                        t.arr("weekdays").forEach { addWeekdays(it.jsonPrimitive.intOrNull ?: 0) }
                        t.obj("intervalHint")?.let { setIntervalHint(parseText(it)) }
                        t.obj("timingError")?.let { setTimingError(parseText(it)) }
                    },
            )
        }
        o.obj("projectName")?.let { setProjectName(parseText(it)) }
        setProjectShortPath(o.str("projectShortPath"))
        setHasProject(o.bool("hasProject"))
        o.arr("projectChoices").forEach { pe ->
            val p = pe.jsonObject
            addProjectChoices(
                ScheduleProjectOptionView.newBuilder()
                    .setProjectKey(p.str("projectKey"))
                    .setShortPath(p.str("shortPath"))
                    .setSelected(p.bool("selected"))
                    .apply { p.obj("name")?.let { setName(parseText(it)) } },
            )
        }
        setMachineName(o.str("machineName"))
        setMachineSelectable(o.bool("machineSelectable"))
        o.arr("machineChoices").forEach { me ->
            val m = me.jsonObject
            addMachineChoices(
                ScheduleMachineOptionView.newBuilder()
                    .setMachineId(m.str("machineId"))
                    .setName(m.str("name"))
                    .setOnline(m.bool("online"))
                    .setSupported(m.bool("supported"))
                    .setSelected(m.bool("selected"))
                    .apply { m.obj("note")?.let { setNote(parseText(it)) } },
            )
        }
        setAgent(o.enum("agent", Agent.AGENT_UNSPECIFIED))
        setAgentName(o.str("agentName"))
        o.arr("availableAgents").forEach { ae ->
            val a = ae.jsonObject
            addAvailableAgents(
                AgentOptionView.newBuilder()
                    .setAgent(a.enum("agent", Agent.AGENT_UNSPECIFIED))
                    .setDisplayName(a.str("displayName"))
                    .setInstalled(a.bool("installed"))
                    .setSelected(a.bool("selected")),
            )
        }
        setModel(o.str("model"))
        setEffort(o.str("effort"))
        o.obj("modelLabel")?.let { setModelLabel(parseText(it)) }
        setEffortLabel(o.str("effortLabel"))
        o.obj("modelPanel")?.let { mp ->
            setModelPanel(
                ScheduleModelPanelView.newBuilder()
                    .setModelsLoading(mp.bool("modelsLoading"))
                    .setDefaultModelChecked(mp.bool("defaultModelChecked"))
                    .setSelectedEffort(mp.str("selectedEffort"))
                    .setPanelEnabled(mp.bool("panelEnabled"))
                    .apply {
                        mp.obj("modelsError")?.let { setModelsError(parseText(it)) }
                        mp.obj("defaultModelLabel")?.let { setDefaultModelLabel(parseText(it)) }
                        mp.arr("effortChoices").forEach { addEffortChoices(it.jsonPrimitive.content) }
                        mp.arr("models").forEach { me ->
                            val mo = me.jsonObject
                            addModels(
                                ModelOptionView.newBuilder()
                                    .setSlug(mo.str("slug"))
                                    .setDisplayName(mo.str("displayName"))
                                    .setChecked(mo.bool("checked"))
                                    .apply { mo.obj("fixedEffortNote")?.let { setFixedEffortNote(parseText(it)) } },
                            )
                        }
                    },
            )
        }
        setApprovalMode(o.enum("approvalMode", ApprovalMode.APPROVAL_MODE_UNSPECIFIED))
        o.obj("approvalModeLabel")?.let { setApprovalModeLabel(parseText(it)) }
        setApprovalModeIsDanger(o.bool("approvalModeIsDanger"))
        o.arr("modeChoices").forEach { mce ->
            val mc = mce.jsonObject
            addModeChoices(
                ScheduleModeOptionView.newBuilder()
                    .setMode(mc.enum("mode", ApprovalMode.APPROVAL_MODE_UNSPECIFIED))
                    .setIsDanger(mc.bool("isDanger"))
                    .setSelected(mc.bool("selected"))
                    .setEnabled(mc.bool("enabled"))
                    .apply {
                        mc.obj("label")?.let { setLabel(parseText(it)) }
                        mc.obj("disabledReason")?.let { setDisabledReason(parseText(it)) }
                    },
            )
        }
        setRequiresFullAccessConfirm(o.bool("requiresFullAccessConfirm"))
        o.obj("locationText")?.let { setLocationText(parseText(it)) }
        setEditable(o.bool("editable"))
        setCanSubmit(o.bool("canSubmit"))
        o.obj("submitLabel")?.let { setSubmitLabel(parseText(it)) }
        o.obj("submitDisabledReason")?.let { setSubmitDisabledReason(parseText(it)) }
        setHasUnsavedChanges(o.bool("hasUnsavedChanges"))
        o.arr("notices").forEach { ne ->
            val n = ne.jsonObject
            addNotices(
                ScheduleFormNoticeView.newBuilder()
                    .setKind(n.enum("kind", ScheduleFormNoticeKind.SCHEDULE_FORM_NOTICE_KIND_UNSPECIFIED))
                    .setSeverity(n.enum("severity", ScheduleFormNoticeSeverity.SCHEDULE_FORM_NOTICE_SEVERITY_UNSPECIFIED))
                    .apply { n.obj("text")?.let { setText(parseText(it)) } },
            )
        }
        o.obj("mutation")?.let { setMutation(parseScheduleMutation(it)) }
    }.build()

    private fun parseScheduleSessionStrip(o: JsonObject): ScheduleSessionStripViewModel = ScheduleSessionStripViewModel.newBuilder().apply {
        setSessionId(o.str("sessionId"))
        setVisible(o.bool("visible"))
        setCount(o.i32("count"))
        o.obj("summaryText")?.let { setSummaryText(parseText(it)) }
        setMachineOffline(o.bool("machineOffline"))
        o.arr("tasks").forEach { te ->
            val t = te.jsonObject
            addTasks(
                ScheduleSessionStripTaskView.newBuilder()
                    .setMachineId(t.str("machineId"))
                    .setTaskId(t.str("taskId"))
                    .setTitle(t.str("title"))
                    .setHasUnread(t.bool("hasUnread"))
                    .setStatus(t.enum("status", ScheduleTaskStatus.SCHEDULE_TASK_STATUS_UNSPECIFIED))
                    .apply {
                        t.obj("timingText")?.let { setTimingText(parseText(it)) }
                        t.obj("nextRunText")?.let { setNextRunText(parseText(it)) }
                    },
            )
        }
    }.build()
}
