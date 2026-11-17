package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.DEFAULT_AGENT
import io.github.lewin671.remoteagent.core.MachineView
import io.github.lewin671.remoteagent.core.RemoteState
import io.github.lewin671.remoteagent.core.SessionView
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.core.agentName
import io.github.lewin671.remoteagent.core.proto.ConnState as ProtoConnState
import io.github.lewin671.remoteagent.core.proto.SessionActionsView
import io.github.lewin671.remoteagent.core.proto.SessionAgentMessageItem
import io.github.lewin671.remoteagent.core.proto.SessionApprovalDockView
import io.github.lewin671.remoteagent.core.proto.SessionCapabilitiesView
import io.github.lewin671.remoteagent.core.proto.SessionComposerView
import io.github.lewin671.remoteagent.core.proto.SessionDisplayStatus
import io.github.lewin671.remoteagent.core.proto.SessionDockKind
import io.github.lewin671.remoteagent.core.proto.SessionDockView
import io.github.lewin671.remoteagent.core.proto.SessionFileRefView
import io.github.lewin671.remoteagent.core.proto.SessionHeaderView
import io.github.lewin671.remoteagent.core.proto.SessionModeChoiceView
import io.github.lewin671.remoteagent.core.proto.SessionPendingInputView
import io.github.lewin671.remoteagent.core.proto.SessionPlanChoiceOptionView
import io.github.lewin671.remoteagent.core.proto.SessionPlanDockView
import io.github.lewin671.remoteagent.core.proto.SessionPlanItem
import io.github.lewin671.remoteagent.core.proto.SessionQuestionDockView
import io.github.lewin671.remoteagent.core.proto.SessionQuestionOptionView
import io.github.lewin671.remoteagent.core.proto.SessionQuestionStepView
import io.github.lewin671.remoteagent.core.proto.SessionTimelineItem
import io.github.lewin671.remoteagent.core.proto.SessionTimelineView
import io.github.lewin671.remoteagent.core.proto.SessionToolCallItem
import io.github.lewin671.remoteagent.core.proto.SessionToolGroupItem
import io.github.lewin671.remoteagent.core.proto.SessionToolStatus
import io.github.lewin671.remoteagent.core.proto.SessionTurnEndItem
import io.github.lewin671.remoteagent.core.proto.SessionUserMessageItem
import io.github.lewin671.remoteagent.core.proto.SessionViewModel
import io.github.lewin671.remoteagent.core.proto.Text
import io.github.lewin671.remoteagent.core.proto.TextArg
import io.github.lewin671.remoteagent.core.proto.TimestampArg
import io.github.lewin671.remoteagent.core.proto.TimestampStyle
import io.github.lewin671.remoteagent.proto.Agent
import io.github.lewin671.remoteagent.proto.ApprovalMode
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.InputStatus
import io.github.lewin671.remoteagent.proto.LiveSnapshot
import io.github.lewin671.remoteagent.proto.OriginKind
import io.github.lewin671.remoteagent.proto.PlanChoice
import io.github.lewin671.remoteagent.proto.SessionStatus
import io.github.lewin671.remoteagent.proto.ToolKind

internal fun sessionVmsForTest(
    state: RemoteState,
    history: Map<String, List<Event>>,
    live: Map<String, LiveSnapshot> = emptyMap(),
): Map<String, SessionViewModel> {
    val ids = (history.keys + live.keys).distinct()
    return ids.associateWith { sid ->
        val s = state.sessions[sid]
        val m = s?.let { state.machines[it.machineId] }
        sessionFixtureVm(sid, s, m, history[sid].orEmpty(), live[sid], state.conn == io.github.lewin671.remoteagent.core.ConnState.ONLINE)
    }
}

internal fun sessionFixtureVm(
    sid: String,
    session: SessionView?,
    machine: MachineView? = null,
    events: List<Event> = emptyList(),
    live: LiveSnapshot? = null,
    online: Boolean = true,
): SessionViewModel {
    val info = live?.takeIf { it.hasSession() }?.session ?: session?.info
    val agent = info?.agent?.takeIf { it != Agent.AGENT_UNSPECIFIED } ?: DEFAULT_AGENT
    val running = live?.running == true || info?.status == SessionStatus.SESSION_STATUS_RUNNING
    val exited = info?.status == SessionStatus.SESSION_STATUS_EXITED
    val computerName = machine?.meta?.hostname?.ifEmpty { machine.id }.orEmpty().ifEmpty { "Test computer" }

    val items = ArrayList<SessionTimelineItem>()
    val completedCalls = ArrayList<SessionToolCallItem>()
    fun flushToolGroup(seq: Long) {
        if (completedCalls.isEmpty()) return
        if (completedCalls.size == 1) {
            items += SessionTimelineItem.newBuilder()
                .setId("e$seq-tc")
                .setSeq(seq)
                .setToolCall(completedCalls[0])
                .build()
        } else {
            val n = completedCalls.size
            items += SessionTimelineItem.newBuilder()
                .setId("e$seq-tg")
                .setSeq(seq)
                .setToolGroup(
                    SessionToolGroupItem.newBuilder()
                        .setSummary(Text.newBuilder().setLiteral("运行了 $n 条命令"))
                        .setCommandCount(n)
                        .addAllCalls(completedCalls),
                )
                .build()
        }
        completedCalls.clear()
    }

    val pendingByReq = LinkedHashMap<String, SessionPendingInputView>()
    for (e in events) {
        when {
            e.hasUserMessage() -> {
                flushToolGroup(e.seq)
                val um = e.userMessage
                val b = SessionUserMessageItem.newBuilder()
                    .setText(um.text)
                    .addAllImages(um.imagesList)
                    .addAllFiles(um.filesList.map { SessionFileRefView.newBuilder().setName(it.name).setSizeBytes(it.sizeBytes).build() })
                if (e.hasOrigin() && e.origin.kind == OriginKind.ORIGIN_KIND_LOCAL) {
                    b.setOriginCaption(Text.newBuilder().setKey(UiMessages.sessionOriginTerminal("{name}").key).addArgs(TextArg.newBuilder().setName("name").setStrValue(computerName)))
                }
                if (e.hasOrigin() && e.origin.scheduledTaskId.isNotEmpty()) {
                    b.setIsScheduled(true)
                    b.setScheduledTaskId(e.origin.scheduledTaskId)
                    b.setScheduledRunId(e.origin.scheduledRunId)
                }
                items += SessionTimelineItem.newBuilder().setId("e${e.seq}").setSeq(e.seq).setUserMessage(b).build()
            }
            e.hasAgentMessage() -> {
                flushToolGroup(e.seq)
                items += SessionTimelineItem.newBuilder()
                    .setId("e${e.seq}").setSeq(e.seq)
                    .setAgentMessage(SessionAgentMessageItem.newBuilder().setMarkdown(e.agentMessage.text))
                    .build()
            }
            e.hasToolCall() -> {
                val tc = e.toolCall
                val status = if (tc.status == "failed" || tc.status == "error") SessionToolStatus.SESSION_TOOL_STATUS_ERROR else SessionToolStatus.SESSION_TOOL_STATUS_OK
                val callItem = SessionToolCallItem.newBuilder()
                    .setCallId(tc.callId)
                    .setToolKind(tc.kind)
                    .setTitle(tc.title)
                    .setRawInput(tc.input)
                    .setDetail(tc.output)
                    .setStatus(status)
                    .build()
                if (tc.kind == ToolKind.TOOL_KIND_FILE_CHANGE) {
                    flushToolGroup(e.seq)
                    items += SessionTimelineItem.newBuilder().setId("e${e.seq}").setSeq(e.seq).setToolCall(callItem).build()
                } else {
                    completedCalls += callItem
                }
            }
            e.hasToolStarted() -> {
                flushToolGroup(e.seq)
                val ts = e.toolStarted
                items += SessionTimelineItem.newBuilder()
                    .setId("e${e.seq}").setSeq(e.seq)
                    .setToolCall(
                        SessionToolCallItem.newBuilder()
                            .setCallId(ts.callId)
                            .setToolKind(ts.kind)
                            .setTitle(ts.title)
                            .setRawInput(ts.input)
                            .setStatus(SessionToolStatus.SESSION_TOOL_STATUS_RUNNING)
                            .setSinceMs(System.currentTimeMillis()),
                    )
                    .build()
            }
            e.hasPlanProposed() -> {
                flushToolGroup(e.seq)
                items += SessionTimelineItem.newBuilder()
                    .setId("e${e.seq}").setSeq(e.seq)
                    .setPlan(SessionPlanItem.newBuilder().setMarkdown(e.planProposed.text))
                    .build()
            }
            e.hasTurnEnded() -> {
                flushToolGroup(e.seq)
                items += SessionTimelineItem.newBuilder()
                    .setId("e${e.seq}").setSeq(e.seq)
                    .setTurnEnd(SessionTurnEndItem.newBuilder().setDurationMs(e.turnEnded.durationMs).setOutcome(e.turnEnded.outcome))
                    .build()
            }
            e.hasInputState() -> {
                val ip = e.inputState
                val scheduled = e.hasOrigin() && e.origin.scheduledTaskId.isNotEmpty()
                val label = when {
                    scheduled && e.hasTime() -> Text.newBuilder().setLiteral("定时任务 · 15:00 · 这一轮结束后发")
                    ip.paused -> Text.newBuilder().setKey(UiMessages.queuePaused().key)
                    else -> Text.newBuilder().setKey(UiMessages.queueAfterTurn().key)
                }
                if (ip.status in setOf(InputStatus.INPUT_STATUS_QUEUED, InputStatus.INPUT_STATUS_HELD_BY_CLI)) {
                    val prev = pendingByReq[ip.requestId]
                    pendingByReq[ip.requestId] = SessionPendingInputView.newBuilder()
                        .setRequestId(ip.requestId)
                        .setText(ip.text.ifEmpty { prev?.text.orEmpty() })
                        .setStatus(ip.status)
                        .setMode(ip.mode)
                        .setPaused(ip.paused)
                        .setCanCancel(true)
                        .setCanResume(ip.paused)
                        .setLabel(label)
                        .build()
                } else {
                    pendingByReq.remove(ip.requestId)
                }
            }
        }
    }
    flushToolGroup(events.lastOrNull()?.seq ?: 0L)

    val dockBuilder = SessionDockView.newBuilder()
    val approvalEv = live?.actionsList?.firstOrNull { it.hasApprovalRequested() } ?: events.lastOrNull { it.hasApprovalRequested() }
    val questionEv = live?.actionsList?.firstOrNull { it.hasQuestionAsked() } ?: events.lastOrNull { it.hasQuestionAsked() }
    val hasPlan = info?.planOpen == true
    when {
        approvalEv != null && (live != null || info?.pendingApprovals != 0) -> {
            val a = approvalEv.approvalRequested
            dockBuilder.setHasItem(true).setKind(SessionDockKind.SESSION_DOCK_KIND_APPROVAL).setItemId(a.approvalId).setObservedSeq(approvalEv.seq)
                .setTitle(Text.newBuilder().setKey(UiMessages.approvalTitle().key))
                .setSummary(Text.newBuilder().setLiteral(a.command.lines().firstOrNull().orEmpty()))
                .setMeta(Text.newBuilder().setLiteral(a.toolName))
                .setApproval(
                    SessionApprovalDockView.newBuilder()
                        .setApprovalId(a.approvalId)
                        .setCommandPreview(a.command)
                        .setBody(a.command)
                        .setDescription(a.description)
                        .setCanAlwaysAllow(true),
                )
        }
        questionEv != null && (live != null || (info?.pendingQuestions ?: 1) > 0) -> {
            val q = questionEv.questionAsked
            val first = q.questionsList.firstOrNull()
            dockBuilder.setHasItem(true).setKind(SessionDockKind.SESSION_DOCK_KIND_QUESTION).setItemId(q.questionId).setObservedSeq(questionEv.seq)
                .setTitle(Text.newBuilder().setKey(UiMessages.listStateQuestion().key))
                .setQuestion(
                    SessionQuestionDockView.newBuilder()
                        .setQuestionId(q.questionId)
                        .setActiveIndex(0)
                        .setTotalQuestions(q.questionsCount)
                        .apply {
                            if (first != null) {
                                setQuestion(
                                    SessionQuestionStepView.newBuilder()
                                        .setHeader(first.header)
                                        .setQuestionText(first.text)
                                        .setMultiSelect(first.multiSelect)
                                        .setCanFreeText(first.optionsCount == 0)
                                        .addAllOptions(
                                            first.optionsList.mapIndexed { idx, opt ->
                                                SessionQuestionOptionView.newBuilder()
                                                    .setOptionNum(idx + 1)
                                                    .setLabel(opt.label)
                                                    .setDescription(opt.description)
                                                    .build()
                                            },
                                        ),
                                )
                            }
                        },
                )
        }
        hasPlan -> {
            val planText = live?.planScreen?.takeIf { it.isNotEmpty() }
                ?: events.lastOrNull { it.hasPlanProposed() }?.planProposed?.text.orEmpty()
            dockBuilder.setHasItem(true).setKind(SessionDockKind.SESSION_DOCK_KIND_PLAN).setItemId("plan").setObservedSeq(live?.planEventSeq ?: 1L)
                .setTitle(Text.newBuilder().setLiteral("计划待批准"))
                .setPlan(
                    SessionPlanDockView.newBuilder()
                        .setMarkdown(planText)
                        .addChoices(SessionPlanChoiceOptionView.newBuilder().setChoice(PlanChoice.PLAN_CHOICE_IMPLEMENT).setLabel(Text.newBuilder().setLiteral("批准执行"))),
                )
        }
    }

    val titleText = info?.title.orEmpty().ifEmpty { info?.cwd?.substringAfterLast('/').orEmpty() }.ifEmpty { "会话" }
    val status = when {
        exited -> SessionDisplayStatus.SESSION_DISPLAY_STATUS_ENDED
        dockBuilder.kind == SessionDockKind.SESSION_DOCK_KIND_APPROVAL -> SessionDisplayStatus.SESSION_DISPLAY_STATUS_APPROVAL
        dockBuilder.kind == SessionDockKind.SESSION_DOCK_KIND_QUESTION -> SessionDisplayStatus.SESSION_DISPLAY_STATUS_QUESTION
        dockBuilder.kind == SessionDockKind.SESSION_DOCK_KIND_PLAN -> SessionDisplayStatus.SESSION_DISPLAY_STATUS_PLAN
        running -> SessionDisplayStatus.SESSION_DISPLAY_STATUS_RUNNING
        else -> SessionDisplayStatus.SESSION_DISPLAY_STATUS_IDLE
    }

    return SessionViewModel.newBuilder()
        .setSessionId(sid)
        .setHeader(
            SessionHeaderView.newBuilder()
                .setFound(info != null)
                .setSessionsListed(true)
                .setLiveLoaded(true)
                .setTitle(Text.newBuilder().setLiteral(titleText))
                .setProjectName(Text.newBuilder().setLiteral(info?.cwd?.substringAfterLast('/').orEmpty()))
                .setProjectKey(info?.cwd.orEmpty())
                .setCwd(info?.cwd.orEmpty())
                .setAgent(agent)
                .setAgentName(agentName(agent, copy = ::sessionText))
                .setStatus(status)
                .setApprovalMode(info?.mode ?: ApprovalMode.APPROVAL_MODE_ASK)
                .setModel(info?.model.orEmpty())
                .setModelLabel(info?.model.orEmpty())
                .setMachineId(session?.machineId.orEmpty())
                .setComputerName(Text.newBuilder().setLiteral(computerName))
                .setComputerOnline(machine?.online ?: online)
                .setConnState(if (online) ProtoConnState.CONN_STATE_CONNECTED else ProtoConnState.CONN_STATE_OFFLINE)
                .setExited(exited)
                .setEndedAtMs(if (exited) System.currentTimeMillis() else 0L)
                .setCapabilities(
                    SessionCapabilitiesView.newBuilder()
                        .setCanSend(!exited)
                        .setSupportsImages(true)
                        .setSupportsFiles(machine?.meta?.capabilities?.fileUpload ?: true)
                        .setSupportsSendModes(true)
                        .setSupportsCompact(true)
                        .setSupportsFork(!exited)
                        .setSupportsModeSwitch(!exited && !info?.modeLocked!!)
                        .setSupportsModelSwitch(!exited && !running)
                        .setSupportsSchedule(machine?.meta?.capabilities?.scheduledTasks ?: false)
                        .setTerminalAvailable(!exited && (machine?.online ?: online))
                        .setMaxImages(6),
                ),
        )
        .setActions(
            SessionActionsView.newBuilder()
                .setCanRename(true)
                .setCanFork(!exited)
                .setCanEnd(!exited)
                .setCanSwitchMode(!exited)
                .setCanSwitchModel(!exited && !running)
                .addModeChoices(SessionModeChoiceView.newBuilder().setMode(ApprovalMode.APPROVAL_MODE_ASK).setLabel(Text.newBuilder().setKey(UiMessages.modesAsk().key)).setNote(Text.newBuilder().setKey(UiMessages.modesAskDesc().key)).setAvailable(true).setSelected(info?.mode == ApprovalMode.APPROVAL_MODE_ASK))
                .addModeChoices(SessionModeChoiceView.newBuilder().setMode(ApprovalMode.APPROVAL_MODE_AUTO).setLabel(Text.newBuilder().setKey(UiMessages.modesAuto().key)).setNote(Text.newBuilder().setKey(UiMessages.modesAutoDesc(agent = "").key).addArgs(TextArg.newBuilder().setName("agent").setStrValue("CLI"))).setAvailable(true).setSelected(info?.mode == ApprovalMode.APPROVAL_MODE_AUTO))
                .addModeChoices(SessionModeChoiceView.newBuilder().setMode(ApprovalMode.APPROVAL_MODE_PLAN).setLabel(Text.newBuilder().setKey(UiMessages.modesPlan().key)).setNote(Text.newBuilder().setKey(UiMessages.modesPlanDesc().key)).setAvailable(true).setSelected(info?.mode == ApprovalMode.APPROVAL_MODE_PLAN))
                .addModeChoices(SessionModeChoiceView.newBuilder().setMode(ApprovalMode.APPROVAL_MODE_FULL_ACCESS).setLabel(Text.newBuilder().setKey(UiMessages.modesFullAccess().key)).setNote(Text.newBuilder().setKey(UiMessages.modesSkipAll().key)).setAvailable(true).setSelected(info?.mode == ApprovalMode.APPROVAL_MODE_FULL_ACCESS)),
        )
        .setDock(dockBuilder)
        .setComposer(
            SessionComposerView.newBuilder()
                .setEnabled(!exited)
                .setPlaceholder(Text.newBuilder().setKey(UiMessages.composerMessage().key))
                .setSupportsQueueMode(true)
                .setSteerModeDefault(false)
                .setCanAttachImages(!exited)
                .setCanAttachFiles(!exited && (machine?.meta?.capabilities?.fileUpload ?: true))
                .setHasActiveTurn(running)
                .addAllPendingInputs(pendingByReq.values),
        )
        .setTimeline(
            SessionTimelineView.newBuilder()
                .setLoaded(true)
                .setTotalItems(items.size)
                .addAllItems(items),
        )
        .build()
}
