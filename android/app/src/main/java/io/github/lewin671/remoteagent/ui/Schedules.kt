package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.*
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.proto.Agent
import kotlinx.coroutines.launch
import kotlinx.serialization.json.*
import java.time.*

@Composable
fun SchedulesScreen() {
    val remote = LocalRemote.current
    val state by remote.state.collectAsState()
    val snapshots by remote.schedules.state.collectAsState()
    val tasks = remote.schedules.tasks(state)
    val targets = tasks.filter { it.target.text("type") == "resume_session" }.map { it.target.text("session") }.distinct()
    LaunchedEffect(targets, state.conn) {
        if (state.conn == ConnState.ONLINE) for (sid in targets) if (sid.isNotEmpty() && state.sessions[sid]?.info == null) {
            try { remote.fetchSession(sid) } catch (e: kotlinx.coroutines.CancellationException) { throw e } catch (_: Exception) { /* A tap retries through sessionLookup. */ }
        }
    }
    val nav = LocalNav.current
    val scope = rememberCoroutineScope()
    val t = Instant.ofEpochMilli(now())
    Column(Modifier.fillMaxSize().readableColumn()) {
        LargeTitle("定时任务")
        if (!state.loaded) ConnBanner(state) else ScheduleOffline(state, tasks)
        LazyColumn(Modifier.weight(1f)) {
            snapshots.forEach { (machine, snapshot) ->
                if (snapshot.error.isNotEmpty()) item("error-$machine") { Banner(snapshot.error, action = "重试", onAction = { scope.launch { remote.schedules.refresh(machine) } }) }
            }
            if (tasks.isEmpty()) item {
                Column(Modifier.padding(horizontal = 32.dp, vertical = 64.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                    Text(if (snapshots.values.any { it.loading }) "正在读取任务…" else "还没有定时任务", style = T.h17, color = pal.fg)
                    Text(SCHEDULE_EMPTY, style = T.sm, color = pal.muted, modifier = Modifier.padding(top = 12.dp))
                }
            }
            TaskGroup.entries.forEach { group ->
                val rows = scheduleSorted(tasks, group)
                if (rows.isNotEmpty()) {
                    item("group-$group") { Label(group.label, rows.size) }
                    items(rows, key = { "${it.machine}/${it.id}" }) { task ->
                        TaskRow(task, state, t, last = task == rows.last()) { nav.task(task.machine, task.id) }
                    }
                }
            }
            item("new") { Row(Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable { nav.newTask("") }.padding(16.dp), verticalAlignment = Alignment.CenterVertically) { IconText(Lucide.Plus, "新任务…", pal.blue, T.bodyB) } }
        }
    }
}

/** Same geometry and typography as the conversation rows, with status-independent task content. */
@Composable
fun TaskRow(task: ScheduledTask, state: RemoteState, now: Instant = Instant.now(), last: Boolean = false, onClick: () -> Unit) {
    val online = scheduleWritable(state, task.machine)
    val group = taskGroup(task)
    val tone = when { !online -> Tone.OFF; group == TaskGroup.ATTENTION -> Tone.WAIT; task.active != null -> Tone.RUN; group == TaskGroup.ACTIVE -> Tone.IDLE; else -> Tone.OFF }
    SessionListRow(task.title, if (online) scheduleNext(task, now) else SCHEDULE_OFFLINE, tone, last, onClick = onClick) {
        Text(scheduleTiming(task.trigger) + " · " + taskLocation(task, state), style = T.sm, color = pal.muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
        Text(scheduleLastResult(task, now), style = T.meta, color = pal.muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
}


@Composable
private fun ScheduleOffline(state: RemoteState, tasks: List<ScheduledTask>) {
    if (state.conn != ConnState.ONLINE || tasks.any { state.machines[it.machine]?.online != true })
        Banner(SCHEDULE_OFFLINE, tone = Tone2.WARN)
}

@Composable
fun ScheduleDetailScreen(machine: String, id: String) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val snapshots by remote.schedules.state.collectAsState()
    val task = snapshots[machine]?.tasks?.firstOrNull { it.id == id }?.takeIf { scheduleSupported(state.machines[machine]) }
    val writable = scheduleWritable(state, machine)
    val scope = rememberCoroutineScope()
    var edit by remember { mutableStateOf("") }
    var editBase by remember { mutableStateOf<ScheduledTask?>(null) }
    fun beginEdit(kind: String) { editBase = task; edit = kind }
    var deleting by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var uncertain by remember { mutableStateOf(false) }
    val t = Instant.ofEpochMilli(now())
    fun operate(op: String, spec: JsonObject? = null) {
        if (busy || task == null) return
        busy = true; error = ""
        scope.launch {
            try { remote.schedules.operate(machine, op, if (spec != null) editBase ?: task else task, spec); edit = ""; editBase = null; if (op == "cancel") nav.back() }
            catch (e: kotlinx.coroutines.CancellationException) { throw e }
            catch (e: Exception) { error = errorText(e); uncertain = (e as? CommandException)?.unknown == true; if (e is ScheduleConflictException) { edit = ""; editBase = null } }
            finally { busy = false }
        }
    }
    val enabled = writable && !busy && !uncertain
    val definitionEnabled = enabled && task?.let(::scheduleDefinitionAllowed) == true
    Column(Modifier.fillMaxSize().background(pal.bg)) {
        NavBar(task?.title ?: "定时任务", nav.back, subtitle = task?.let { if (writable) scheduleNext(it, t) else SCHEDULE_OFFLINE })
        if (!state.loaded) ConnBanner(state)
        if (task == null) {
            Banner(snapshots[machine]?.error?.ifEmpty { null } ?: if (snapshots[machine]?.loading == true) "正在读取任务…" else "任务未找到，可能已在另一端删除")
            return@Column
        }
        ScheduleOffline(state, listOf(task))
        Column(Modifier.weight(1f).fillMaxWidth().readableColumn().verticalScroll(rememberScrollState())) {
            if (error.isNotEmpty()) Banner(error, tone = Tone2.ERROR)
            if (uncertain) TextAction("重新读取", { scope.launch { remote.schedules.refresh(machine); if (remote.schedules.state.value[machine]?.error.isNullOrEmpty()) uncertain = false } }, Modifier.padding(horizontal = 16.dp))
            if (taskGroup(task) == TaskGroup.ATTENTION) Banner(scheduleAttentionText(task, state), tone = Tone2.WARN)
            else if (task.reason == "completed_while_paused") Banner(scheduleReason(task.reason), tone = Tone2.WARN)
            Card(Modifier.padding(16.dp)) {
                Column {
                    ScheduleSetting("任务名", task.title, scheduleRenameAllowed(writable, busy || uncertain)) { beginEdit("name") }
                    ScheduleSetting("什么时候", scheduleTiming(task.trigger), definitionEnabled) { beginEdit("time") }
                    val end = scheduleLimits(task, t)
                    if (end.isNotEmpty()) ScheduleSetting("结束", end, definitionEnabled) { beginEdit("time") }
                    if (task.target.text("type") == "resume_session") {
                        ScheduleSetting("发到", scheduleDestination(task, state)) { nav.session(task.target.text("session")) }
                    } else ScheduleSetting("在哪运行", taskLocation(task, state), definitionEnabled) { beginEdit("config") }
                    Box(Modifier.bottomLine(pal.line)) {
                        GRow("权限", taskPermission(task, state), onClick = if (task.target.text("type") == "new_session") ({ beginEdit("mode") }) else null,
                            chevron = task.target.text("type") == "new_session", enabled = definitionEnabled, singleLineValue = true)
                    }
                    ScheduleSetting("消息", "编辑", definitionEnabled) { beginEdit("message") }
                    Text(task.message, style = T.body, color = pal.fg, modifier = Modifier.padding(14.dp))
                }
            }
            Hint(scheduleCreation(task, t), Modifier.padding(horizontal = 16.dp))
            Label("运行记录", task.allRuns.size)
            if (task.allRuns.isEmpty()) Hint("还没有运行", Modifier.padding(horizontal = 16.dp))
            Card(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
                Column { scheduleRunGroups(task.allRuns).forEach { group -> ScheduleRunGroup(group, t) { run -> if (run.session.isNotEmpty()) nav.scheduledSession(run.session, run.seq) } } }
            }
            Hint("“通知了你”表示已请求通知，不保证送达。记录保留最近 100 次，已开的对话仍可查看。", Modifier.padding(horizontal = 16.dp))
            if (taskGroup(task) == TaskGroup.ATTENTION) {
                val onceFailed = task.status == "failed" && (task.trigger.text("type") == "at" || task.trigger.text("type") == "after_turn" && task.trigger["repeat"]?.jsonPrimitive?.booleanOrNull != true)
                val systemCancelled = task.status == "cancelled"
                Btn(if (systemCancelled) "重新安排" else if (onceFailed) "立即额外执行" else "恢复", {
                    if (systemCancelled && (task.trigger.text("type") == "at" || task.trigger.text("type") == "after_turn" && task.trigger["repeat"]?.jsonPrimitive?.booleanOrNull != true)) beginEdit("time")
                    else operate(if (systemCancelled) "update" else if (onceFailed) "run" else "resume", if (systemCancelled) task.spec else null)
                }, Modifier.fillMaxWidth().padding(16.dp), BtnKind.PRIMARY, enabled)
            }
            Card(Modifier.padding(16.dp)) {
                Column {
                    TextAction("立即运行一次", { operate("run") }, Modifier.fillMaxWidth().padding(horizontal = 16.dp), enabled = enabled && scheduleRunAllowed(task))
                    if (taskGroup(task) != TaskGroup.ENDED && taskGroup(task) != TaskGroup.ATTENTION) TextAction(if (task.status == "paused") "恢复" else "暂停", { operate(if (task.status == "paused") "resume" else "pause") }, Modifier.fillMaxWidth().padding(horizontal = 16.dp), enabled = enabled)
                    TextAction("删除任务", { deleting = true }, Modifier.fillMaxWidth().padding(horizontal = 16.dp), color = pal.red, enabled = enabled)
                }
            }
            Hint("立即运行一次不占自动次数及日历当天名额。删除任务会停止后续调度，已发出的消息和已开的对话保留。", Modifier.padding(horizontal = 16.dp, vertical = 8.dp))
        }
    }
    if (task != null) {
        val base = editBase ?: task
        if (edit == "name") ScheduleRenameDialog(base, { edit = "" }) { name ->
            check(scheduleRenameAllowed(writable, busy || uncertain)) { "当前任务不能单独改名" }
            busy = true
            try { remote.schedules.rename(base, name) }
            catch (e: Exception) { uncertain = (e as? CommandException)?.unknown == true; if (e is ScheduleConflictException) { edit = ""; editBase = null; error = errorText(e) }; throw e }
            finally { busy = false }
        }
        if (edit == "time") ScheduleTimeSheet(base.trigger, { operate("update", JsonObject(base.spec + ("trigger" to it))) }, { edit = "" })
        if (edit == "message") ScheduleMessageEditor(base.message, busy, error, { edit = "" }) { text -> operate("update", JsonObject(base.spec + ("message" to JsonPrimitive(text)))) }
        if (edit == "config" || edit == "mode") {
            val cfg = scheduleConfig(base)
            NewSettingsSheet(scheduleEditingState(state, machine), cfg.cwd, projectName(cfg.cwd), machine, cfg.agent, if (edit == "mode") "mode" else "",
                onDone = { op ->
                    val picked = op?.sets?.firstOrNull { it.agent == op.agent }
                    if (picked == null) edit = "" else operate("update", JsonObject(base.spec + ("target" to scheduleTarget(agentConfig(machine, cfg.cwd, picked), base.target))))
                }, onClose = { edit = "" }, initial = cfg, scheduling = true, lockMachine = true)
        }
        if (deleting) Confirm("删除这个任务？", "停止后续调度，已发出的消息和已开的对话保留。", "删除", true, { deleting = false }) { deleting = false; operate("cancel") }
    }
}

@Composable
private fun ScheduleMessageEditor(initial: String, busy: Boolean, error: String, close: () -> Unit, save: (String) -> Unit) {
    var text by remember { mutableStateOf(initial) }
    Sheet(close) {
        SheetHead("编辑消息", "取消", close)
        ReasonField(text, { text = it }, "让 agent 做什么")
        if (error.isNotEmpty()) Banner(error, tone = Tone2.ERROR)
        Btn("保存", { save(text) }, Modifier.fillMaxWidth(), BtnKind.PRIMARY, enabled = !busy && text.isNotBlank())
    }
}

fun scheduleAttentionText(task: ScheduledTask, state: RemoteState): String {
    val why = scheduleReason(task.reason)
    if (task.reason == "relay_session_limit") return "$why。删除不再需要的会话后可恢复；恢复后从下一次开始运行。"
    if (task.reason == "agent_schedule_permissions_changed") return "$why。恢复后按「${taskPermission(task, state)}」运行，并由你接管这个任务。"
    if (task.status == "failed" && (task.trigger.text("type") == "at" || task.trigger.text("type") == "after_turn" && task.trigger["repeat"]?.jsonPrimitive?.booleanOrNull != true))
        return "$why。查看运行记录后，可立即额外执行，或重新安排时间。"
    return "$why。请先查看运行记录；恢复后从下一次开始运行，不会重发无法确认的那次。"
}

@Composable
private fun ScheduleRunGroup(group: RunGroup, now: Instant, open: (ScheduledRun) -> Unit) {
    var expanded by rememberSaveable(group.key) { mutableStateOf(false) }
    if (group.quiet && group.runs.size > 1) {
        Box(Modifier.bottomLine(pal.line)) {
            GRow("${scheduleWhen(group.runs.first().time, now).substringBefore(' ')} · ${group.runs.size} 次，都没有通知", singleLineValue = true, onClick = { expanded = !expanded }, trailing = {
                Ic(if (expanded) Lucide.ChevronUp else Lucide.ChevronDown, 16.dp, pal.muted)
            })
        }
        val latest = group.runs.first()
        Text("最近一次 ${scheduleClock(latest.time)}：${latest.foldedSummary}", style = T.meta, color = pal.muted, modifier = Modifier.padding(start = 14.dp, end = 14.dp, bottom = 12.dp))
        if (!expanded) return
    }
    group.runs.forEach { run ->
        Column(Modifier.fillMaxWidth().then(if (run.session.isNotEmpty()) Modifier.clickable { open(run) } else Modifier).bottomLine(pal.line)) {
            GRow(
                "${scheduleWhen(run.time, now)} · ${runResult(run)}" + if (run.json["manual"]?.jsonPrimitive?.booleanOrNull == true) " · 手动" else "",
                chevron = run.session.isNotEmpty(), danger = run.status == "failed", singleLineValue = true,
            )
            val summary = run.summary.ifEmpty { run.json.text("reason").takeIf { it.isNotEmpty() }?.let(::scheduleReason).orEmpty() }
            if (summary.isNotEmpty()) Text(summary, style = T.meta, color = pal.muted, maxLines = 2, overflow = TextOverflow.Ellipsis, modifier = Modifier.padding(start = 14.dp, end = 14.dp, bottom = 16.dp))
        }
    }
}

@Composable
fun ScheduleSetting(label: String, value: String, enabled: Boolean = true, onClick: () -> Unit) {
    Box(Modifier.bottomLine(pal.line)) {
        GRow(label, value, onClick = onClick, chevron = true, enabled = enabled, singleLineValue = true)
    }
}

/** A CLI setting uses the same grouped row and small segments as the settings page. */
@Composable
internal fun ScheduleAgentSetting(agents: List<Agent>, selected: Agent, enabled: Boolean, onSelect: (Agent) -> Unit) {
    if (agents.size < 2) return
    Box(Modifier.bottomLine(pal.line)) {
        GRow("CLI", trailing = {
            SegPick(agents.map(::agentName), agents.indexOf(selected), enabled) { onSelect(agents[it]) }
        })
    }
}

fun scheduleConfig(task: ScheduledTask): NewConfig = NewConfig(task.machine, task.target.text("cwd"), if (task.target.text("agent") == "claude") Agent.AGENT_CLAUDE else Agent.AGENT_CODEX,
    task.target.text("model"), task.target.text("effort"), when(task.target.text("mode")) { "ask" -> Mode.ASK; "auto" -> Mode.AUTO; "full" -> Mode.FULL; else -> null })
fun scheduleTarget(cfg: NewConfig, base: JsonObject = JsonObject(emptyMap())): JsonObject {
    val fields = base.toMutableMap()
    fields.putAll(buildJsonObject {
        put("type", "new_session"); put("cwd", cfg.cwd); put("agent", if (cfg.agent == Agent.AGENT_CLAUDE) "claude" else "codex")
        put("model", cfg.model); put("effort", cfg.effort)
    })
    if (cfg.mode == null) fields.remove("mode") else fields["mode"] = JsonPrimitive(cfg.mode.name.lowercase())
    return JsonObject(fields)
}

@Composable
fun NewScheduleScreen(session: String) = ScheduleEditor(session, LocalNav.current.back)

@Composable
fun ScheduleEditor(session: String, onClose: () -> Unit) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val scope = rememberCoroutineScope()
    val current = session.isNotEmpty()
    var title by remember { mutableStateOf("") }
    var message by remember { mutableStateOf("") }
    var project by remember { mutableStateOf("") }
    var config by remember { mutableStateOf<NewConfig?>(null) }
    var pickProject by remember { mutableStateOf(false) }
    var pickDirectory by remember { mutableStateOf(false) }
    var settings by remember { mutableStateOf<String?>(null) }
    var timeOpen by remember { mutableStateOf(false) }
    var triggerText by rememberSaveable { mutableStateOf(ScheduleTime().trigger().toString()) }
    val trigger = remember(triggerText) { Json.parseToJsonElement(triggerText).jsonObject }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    val request = rememberSaveable { newRequestId() }
    var frozen by remember { mutableStateOf<String?>(null) }
    val start = if (project.isNotEmpty()) projectStart(state, project, config?.agent, config?.machineId) else null
    FetchEarlier(state, page = config == null && start?.settled == false)
    val cfg = config ?: start?.config
    val machine = if (current) state.sessions[session]?.machineId else cfg?.machineId
    val writable = machine != null && scheduleWritable(state, machine)
    val editable = frozen == null && !busy
    val blocked = machine?.let { if (!scheduleSupported(state.machines[it])) "这台电脑需要更新 remote-agent，才能管理定时任务" else if (!writable) SCHEDULE_OFFLINE else null }
    Column(Modifier.fillMaxSize().background(pal.card)) {
        NavBar(if (current) "定时发到这个对话" else "新任务", onClose)
        ConnBanner(state)
        Column(Modifier.weight(1f).fillMaxWidth().readableColumn().imePadding().verticalScroll(rememberScrollState()).padding(horizontal = 16.dp)) {
            if (error.isNotEmpty()) Banner(error, tone = Tone2.ERROR)
            if (blocked != null) Banner(blocked, tone = Tone2.WARN)
            FieldLabel("任务名")
            ReasonField(title, { if (editable) title = scheduleNameInput(it) }, "留空就用消息的第一句", singleLine = true)
            FieldLabel("让 agent 做什么")
            ReasonField(message, { if (editable) message = it }, "检查项目的更新，有值得注意的变化再通知我。")
            ScheduleSetting("什么时候", scheduleTiming(trigger), editable) { timeOpen = true }
            if (!current) {
                ScheduleSetting("项目", project.takeIf { it.isNotEmpty() }?.let(::projectName) ?: "选择项目", editable) { pickProject = true }
                if (cfg != null) {
                    ScheduleAgentSetting(machineAgents(state.machines[cfg.machineId]), cfg.agent, editable) { agent -> config = projectConfig(state, project, agent, cfg.machineId) }
                    val permission = effectiveModeText(cfg.mode, defaultModeOf(state.machines[cfg.machineId], cfg.agent))
                    ScheduleSetting("权限", permission, editable) { settings = "mode" }
                    val model = listOf(cfg.model.ifEmpty { "默认模型" }, cfg.effort).filter { it.isNotEmpty() }.joinToString(" · ")
                    ScheduleSetting(if (state.machines.size > 1) "机器和模型" else "模型", if (state.machines.size > 1) "${machineLabel(state, cfg.machineId)} · $model" else model, editable) { settings = if (state.machines.size > 1) "" else "model" }
                }
                Hint("每次运行新开一个对话，归在这个任务下面，不进会话列表。Agent 觉得值得说的时候才通知你。")
                if (machine == null && state.machines.values.any { !scheduleSupported(it) }) Hint("电脑需要更新 remote-agent，才能创建定时任务。")
            } else Hint("消息等当前这一轮结束后再发，不会打断正在做的事。")
            val plan = schedulePlanBlocked(current,cfg,state)
            if (plan) Hint("计划模式不能创建执行任务，请先选择其他权限")
            if (!current && cfg != null && resolvedScheduleConfig(cfg, state).mode == null) Hint("请选择明确的权限，再创建定时任务。")
            val ready = !plan && (current || cfg?.let { resolvedScheduleConfig(it, state).mode != null } == true) && writable && !busy && (message.isNotBlank() || frozen != null) && (current || cfg != null && (config != null || start?.settled == true))
            Btn(if (busy) "创建中…" else if (frozen != null) "重试创建" else "创建", {
                if (!ready) return@Btn
                val spec = frozen?.let { Json.parseToJsonElement(it).jsonObject } ?: taskSpec(scheduleName(title, message), message, trigger,
                    if (current) buildJsonObject { put("type", "resume_session"); put("session", session) } else scheduleTarget(resolvedScheduleConfig(cfg!!, state))).also { frozen = it.toString() }
                busy = true; error = ""
                scope.launch {
                    try {
                        val result = remote.schedules.operate(machine!!, "create", spec = spec, requestId = request)
                        onClose()
                        if (result != null) nav.task(machine, result.id)
                    } catch (e: kotlinx.coroutines.CancellationException) { throw e }
                    catch (e: Exception) { error = errorText(e); if (e is CommandException && e.fromDaemon && !e.unknown) frozen = null }
                    finally { busy = false }
                }
            }, Modifier.fillMaxWidth().padding(vertical = 16.dp), BtnKind.PRIMARY, ready)
            if (frozen != null) Hint("创建结果尚未确认，再试一次使用同一个请求。返回后可先查看任务列表。")
        }
    }
    if (timeOpen) ScheduleTimeSheet(trigger, { triggerText = it.toString(); timeOpen = false }, { timeOpen = false })
    if (pickProject) Sheet({ pickProject = false }) {
        SheetHead("选项目", "取消", { pickProject = false })
        ProjectPickList(state, { path -> project = path; config = null; pickProject = false }, { pickProject = false; pickDirectory = true })
    }
    if (pickDirectory) Sheet({ pickDirectory = false }) {
        SheetHead("选目录", "取消", { pickDirectory = false })
        DirPicker(state) { m, path -> project = path; config = NewConfig(m, path, machineAgents(state.machines[m]).first()); pickDirectory = false }
    }
    val settingsPage = settings
    if (settingsPage != null && cfg != null) NewSettingsSheet(state, project, projectName(project), cfg.machineId, cfg.agent, settingsPage,
        onDone = { op ->
            if (op != null) {
                val picked = op.sets.firstOrNull { it.agent == op.agent } ?: op.sets.firstOrNull()
                config = if (picked != null) agentConfig(op.machineId, project, picked) else cfg.copy(machineId = op.machineId)
            }
            settings = null
        }, onClose = { settings = null }, initial = cfg, scheduling = true)
}

@Composable
fun ScheduleTimeSheet(initial: JsonObject, done: (JsonObject) -> Unit, close: () -> Unit) {
    val original = remember { ScheduleTime.from(initial) }
    var value by remember { mutableStateOf(original) }
    var replaced by remember { mutableStateOf(false) }
    val keepTurnDelay = initial.text("type") == "after_turn" && !replaced
    var error by remember { mutableStateOf("") }
    val context = androidx.compose.ui.platform.LocalContext.current
    fun date(current: String, picked: (String) -> Unit) {
        val d = runCatching { LocalDate.parse(current) }.getOrElse { LocalDate.now() }
        android.app.DatePickerDialog(context, { _, y, m, day -> picked(LocalDate.of(y, m + 1, day).toString()) }, d.year, d.monthValue - 1, d.dayOfMonth).show()
    }
    Sheet(close) {
        SheetHead("什么时候", "完成", {
            try { done(if (keepTurnDelay || value == original && !replaced) initial else value.trigger()) } catch (e: Exception) { error = e.message ?: "请检查时间" }
        })
        SegPick(listOf("一次", "每天", "每周", "每隔"), if (keepTurnDelay) -1 else value.kind) { replaced = true; value = value.copy(kind = it) }
        if (keepTurnDelay) {
            Hint("当前是「${scheduleFrequency(initial)}」。直接完成会保留原规则；选择上面的时间类型后再更改。")
        } else {
        if (error.isNotEmpty()) Banner(error, tone = Tone2.ERROR)
        if (value.kind == 0) ScheduleSetting("日期", value.date) { date(value.date) { value = value.copy(date = it) } }
        if (value.kind != 3) {
            ScheduleSetting("时间", value.time) {
                val t = runCatching { LocalTime.parse(value.time) }.getOrElse { LocalTime.of(8, 0) }
                android.app.TimePickerDialog(context, { _, h, m -> value = value.copy(time = "%02d:%02d".format(h, m)) }, t.hour, t.minute, true).show()
            }
        } else {
            FieldLabel("间隔")
            ReasonField(value.interval, { value = value.copy(interval = it) }, "正整数")
            GRow("单位", trailing = { SegPick(listOf("分钟", "小时", "天", "秒"), value.unit) { value = value.copy(unit = it) } })
        }
        if (value.kind == 1) ToggleRow("只在工作日", "周一到周五", value.workdays) { value = value.copy(workdays = it) }
        if (value.kind == 2) {
            ChoiceGrid((1..7).map { "周" + listOf("一", "二", "三", "四", "五", "六", "日")[it - 1] }, null, true) { day ->
                val n = listOf("周一", "周二", "周三", "周四", "周五", "周六", "周日").indexOf(day) + 1
                value = value.copy(weekdays = if (n in value.weekdays) value.weekdays - n else value.weekdays + n)
            }
            Text("已选：" + value.weekdays.sorted().joinToString("、") { listOf("", "周一", "周二", "周三", "周四", "周五", "周六", "周日")[it] }, style = T.meta, color = pal.muted)
        }
        if (value.kind != 0) {
            GRow("结束", trailing = { SegPick(listOf("不结束", "运行指定次数", "截止日期"), value.end) { value = value.copy(end = it) } })
            if (value.end == 1) ReasonField(value.count, { value = value.copy(count = it) }, "自动运行次数")
            if (value.end == 2) ScheduleSetting("结束日期", value.until) { date(value.until) { value = value.copy(until = it, deadline = "") } }
        }
        FieldLabel("时区")
        ReasonField(value.zone, { value = value.copy(zone = it.trim(), deadline = "") }, "例如 Asia/Shanghai")
        Hint(SCHEDULE_INTERVAL_HINT)
        }
    }
}

@Composable
fun SessionScheduleStrip(sid: String) {
    val remote = LocalRemote.current
    val state by remote.state.collectAsState()
    val snapshots by remote.schedules.state.collectAsState()
    val tasks = snapshots.values.flatMap { it.tasks }.filter { scheduleSupported(state.machines[it.machine]) && it.target.text("type") == "resume_session" && it.target.text("session") == sid && taskGroup(it) != TaskGroup.ENDED }
    if (tasks.isEmpty()) return
    val nav = LocalNav.current
    val next = tasks.minByOrNull { it.next ?: Instant.MAX }!!
    val text = sessionScheduleText(tasks, state, Instant.ofEpochMilli(now()))
    var choosing by remember { mutableStateOf(false) }
    if (choosing) Sheet({ choosing = false }) {
        SheetHead("定时任务", "取消", { choosing = false })
        tasks.forEach { task -> GRow(task.title, onClick = { choosing = false; nav.task(task.machine, task.id) }, chevron = true, singleLineValue = true) }
    }
    Row(Modifier.fillMaxWidth().heightIn(min = 44.dp).background(pal.soft).clickable {
        if (tasks.size == 1) nav.task(next.machine, next.id) else choosing = true
    }.padding(horizontal = 16.dp, vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
        Ic(Lucide.Clock, 13.dp, pal.muted)
        Text(text, Modifier.weight(1f).padding(start = 6.dp), style = T.meta, color = pal.muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
        ArrowText("管理", pal.blue, T.meta)
    }
}
