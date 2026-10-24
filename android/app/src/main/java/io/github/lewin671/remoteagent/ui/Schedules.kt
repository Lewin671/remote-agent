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
        ConnBanner(state)
        ScheduleOffline(state, tasks)
        LazyColumn(Modifier.weight(1f)) {
            snapshots.forEach { (machine, snapshot) ->
                if (snapshot.error.isNotEmpty()) item("error-$machine") { Banner(snapshot.error, action = "重试", onAction = { scope.launch { remote.schedules.refresh(machine) } }) }
            }
            if (tasks.isEmpty()) item {
                Column(Modifier.padding(horizontal = 32.dp, vertical = 64.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                    Text(if (snapshots.values.any { it.loading }) "正在读取任务…" else "还没有定时任务", style = T.h17, color = pal.fg)
                    Text("在对话里跟 agent 说「每天 8 点帮我看看…」，它会替你建好；也可以在这里新建。", style = T.sm, color = pal.muted, modifier = Modifier.padding(top = 12.dp))
                }
            }
            TaskGroup.entries.forEach { group ->
                val rows = tasks.filter { taskGroup(it) == group }.sortedWith(compareBy<ScheduledTask> { it.next ?: Instant.MAX }.thenBy { it.title })
                if (rows.isNotEmpty()) {
                    item("group-$group") { Label(group.label, rows.size) }
                    items(rows, key = { "${it.machine}/${it.id}" }) { task ->
                        val where = listOf(taskLocation(task, state), scheduleLimits(task, t)).filter { it.isNotEmpty() }.joinToString(" · ")
                        TaskRow(task.title, scheduleFrequency(task.trigger) + " · " + where,
                            if (group == TaskGroup.ATTENTION) scheduleReason(task.reason) else scheduleLastResult(task, t),
                            scheduleNext(task, t),
                            when { !scheduleWritable(state, task.machine) -> Tone.OFF; group == TaskGroup.ATTENTION -> Tone.WAIT; task.active != null -> Tone.RUN; group == TaskGroup.ACTIVE -> Tone.IDLE; else -> Tone.OFF },
                            last = task == rows.last(),
                        ) { nav.task(task.machine, task.id) }
                    }
                }
            }
            item("new") { Row(Modifier.fillMaxWidth().heightIn(min = 44.dp).clickable { nav.newTask("") }.padding(16.dp), verticalAlignment = Alignment.CenterVertically) { IconText(Lucide.Plus, "新任务…", pal.blue, T.bodyB) } }
        }
    }
}

/** Same geometry and typography as the conversation rows, with status-independent task content. */
@Composable
fun TaskRow(title: String, frequency: String, result: String, next: String, tone: Tone, last: Boolean = false, onClick: () -> Unit) {
    SessionListRow(title, next, tone, last, onClick = onClick) {
        Text(frequency, style = T.sm, color = pal.muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
        Text(result, style = T.meta, color = if (tone == Tone.WAIT) pal.orangeInk else pal.muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
}


fun taskLocation(task: ScheduledTask, state: RemoteState): String {
    val target = task.target
    val location = if (target.text("type") == "resume_session") "在「${state.sessions[target.text("session")]?.let(::titleOf) ?: "当前对话"}」里" else "新开对话"
    return location + if (state.machines.size > 1) " · ${machineLabel(state, task.machine)}" else ""
}

@Composable
private fun ScheduleOffline(state: RemoteState, tasks: List<ScheduledTask>) {
    if (state.conn != ConnState.ONLINE) return
    tasks.groupBy { it.machine }.forEach { (machine, rows) ->
        if (state.machines[machine]?.online != true) Banner("${machineLabel(state, machine)} 不在线 · ${rows.size} 个任务显示上次的状态，连接后更新", tone = Tone2.WARN)
    }
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
    var editing by remember { mutableStateOf(false) }
    var deleting by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    val t = Instant.ofEpochMilli(now())
    fun operate(op: String) {
        if (busy || task == null) return
        busy = true; error = ""
        scope.launch {
            try { remote.schedules.operate(machine, op, task) }
            catch (e: kotlinx.coroutines.CancellationException) { throw e }
            catch (e: Exception) { error = errorText(e) }
            finally { busy = false }
        }
    }
    Column(Modifier.fillMaxSize().background(pal.bg)) {
        NavBar(task?.title ?: "定时任务", nav.back, subtitle = task?.let { taskLocation(it, state) + " · " + scheduleNext(it, t) })
        ConnBanner(state)
        if (task == null) {
            Banner(snapshots[machine]?.error?.ifEmpty { null } ?: if (snapshots[machine]?.loading == true) "正在读取任务…" else "任务未找到，可能已在另一端删除")
            return@Column
        }
        ScheduleOffline(state, listOf(task))
        Column(Modifier.weight(1f).fillMaxWidth().readableColumn().verticalScroll(rememberScrollState())) {
            if (error.isNotEmpty()) Banner(error, tone = Tone2.ERROR)
            if (taskGroup(task) == TaskGroup.ATTENTION) {
                Banner(scheduleAttentionText(task, state), tone = Tone2.WARN)
            }
            Card(Modifier.padding(16.dp)) {
                Column {
                    ScheduleSetting("什么时候", listOf(scheduleFrequency(task.trigger), scheduleLimits(task, t)).filter { it.isNotEmpty() }.joinToString(" · "), writable && !busy) { editing = true }
                    if (task.target.text("type") == "resume_session") {
                        ScheduleSetting("发到", state.sessions[task.target.text("session")]?.let(::titleOf) ?: "当前对话") { nav.session(task.target.text("session")) }
                    } else ScheduleSetting("在哪运行", projectName(task.target.text("cwd")) + " · " + if (task.target.text("agent") == "claude") "Claude Code" else "Codex", writable && !busy) { editing = true }
                    Column(Modifier.padding(16.dp)) {
                        Text("发给 agent 的话", style = T.meta, color = pal.muted)
                        Text(task.message, style = T.body, color = pal.fg, modifier = Modifier.padding(top = 8.dp))
                        TextAction("编辑", { editing = true }, enabled = writable && !busy)
                    }
                }
            }
            Hint(if (task.target.text("type") == "resume_session") "消息等当前这一轮结束后再发，不会打断正在做的事。" else "每次新开对话，回复后会移到会话列表。", Modifier.padding(horizontal = 16.dp))
            if (task.trigger.text("timezone").isNotEmpty()) Hint("时区：${task.trigger.text("timezone")}", Modifier.padding(horizontal = 16.dp))
            Hint(if (task.json.text("source") == "agent") "由 Agent 在对话里创建。" else "由你创建。", Modifier.padding(horizontal = 16.dp))
            Label("运行记录", task.allRuns.size)
            val groups = scheduleRunGroups(task.allRuns)
            if (groups.isEmpty()) Hint("尚未运行", Modifier.padding(horizontal = 16.dp))
            Card(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
                Column { groups.forEach { group -> ScheduleRunGroup(group, t) { run -> if (run.session.isNotEmpty()) nav.scheduledSession(run.session, run.seq) } } }
            }
            if (taskGroup(task) == TaskGroup.ATTENTION) {
                val onceFailed = task.status == "failed" && (task.trigger.text("type") == "at" || task.trigger.text("type") == "after_turn" && task.trigger["repeat"]?.jsonPrimitive?.booleanOrNull != true)
                Btn(if (onceFailed) "立即额外执行一次" else "恢复", { operate(if (onceFailed) "run" else "resume") }, Modifier.fillMaxWidth().padding(16.dp), BtnKind.PRIMARY, writable && !busy)
            }
            Card(Modifier.padding(16.dp)) {
                Column {
                    if (taskGroup(task) != TaskGroup.ATTENTION) {
                        TextAction("立即运行一次", { operate("run") }, Modifier.fillMaxWidth().padding(horizontal = 16.dp), enabled = writable && !busy && taskGroup(task) !in setOf(TaskGroup.ENDED))
                        if (taskGroup(task) != TaskGroup.ENDED) TextAction(if (task.status == "paused") "恢复" else "暂停", { operate(if (task.status == "paused") "resume" else "pause") }, Modifier.fillMaxWidth().padding(horizontal = 16.dp), enabled = writable && !busy)
                    }
                    TextAction("删除任务", { deleting = true }, Modifier.fillMaxWidth().padding(horizontal = 16.dp), color = pal.red, enabled = writable && !busy && task.status != "cancelled")
                }
            }
            Hint("立即运行一次不占自动次数和日历任务当天的名额。暂停或删除不撤回已经发出的消息。", Modifier.padding(horizontal = 16.dp, vertical = 8.dp))
        }
    }
    if (editing && task != null) ScheduleEditor(session = "", existing = task, onClose = { editing = false })
    if (deleting && task != null) Confirm("删除这个任务？", "停止以后的调度，已经开出的对话和运行记录保留。", "删除", true, { deleting = false }) { deleting = false; operate("cancel") }
}

fun scheduleAttentionText(task: ScheduledTask, state: RemoteState): String {
    val why = scheduleReason(task.reason)
    if (task.reason == "relay_session_limit") return "$why，任务已停下。清理不再需要的会话后再恢复；从下一次开始运行。"
    if (task.reason == "agent_schedule_permissions_changed") {
        val s = state.sessions[task.json.text("scoped_session").ifEmpty { task.target.text("session") }]
        val mode = if (task.target.text("type") == "new_session") task.target.text("mode") else s?.let(::modeOf)?.name?.lowercase().orEmpty()
        val label = when(mode) { "ask" -> "询问"; "auto" -> "自动"; "full" -> "完全放行"; else -> "当前对话的权限" }
        return "$why，任务已暂停。恢复后按「$label」继续运行，并由你接管这个任务。"
    }
    return "$why，任务已停下。请先查看运行记录；恢复后从下一次开始，不会重发无法确认的那次。"
}

@Composable
private fun ScheduleRunGroup(group: RunGroup, now: Instant, open: (ScheduledRun) -> Unit) {
    var expanded by rememberSaveable(group.key) { mutableStateOf(false) }
    if (group.quiet && group.runs.size > 1) {
        Box(Modifier.bottomLine(pal.line)) {
            GRow("${scheduleWhen(group.runs.first().time, now).substringBefore(' ')} · ${group.runs.size} 次，都没有通知", singleLineValue = true, onClick = { expanded = !expanded }, trailing = {
                ArrowText(if (expanded) "收起" else "展开", pal.blue, down = !expanded, up = expanded)
            })
        }
        if (!expanded) return
    }
    group.runs.forEach { run ->
        Column(Modifier.fillMaxWidth().then(if (run.session.isNotEmpty()) Modifier.clickable { open(run) } else Modifier).bottomLine(pal.line)) {
            GRow(
                "${scheduleWhen(run.time, now)} · ${runResult(run)}" + if (run.json["manual"]?.jsonPrimitive?.booleanOrNull == true) " · 额外运行" else "",
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

@Composable
fun NewScheduleScreen(session: String) = ScheduleEditor(session = session, onClose = LocalNav.current.back, page = true)

@Composable
fun ScheduleEditor(session: String, existing: ScheduledTask? = null, onClose: () -> Unit, page: Boolean = false) {
    val remote = LocalRemote.current
    val nav = LocalNav.current
    val state by remote.state.collectAsState()
    val scope = rememberCoroutineScope()
    val sessionView = state.sessions[session]
    val current = existing?.target?.text("type") == "resume_session" || session.isNotEmpty()
    val destination = existing?.target?.text("session") ?: session
    var title by remember { mutableStateOf(existing?.title.orEmpty()) }
    var message by remember { mutableStateOf(existing?.message.orEmpty()) }
    var project by remember { mutableStateOf(existing?.target?.text("cwd").orEmpty()) }
    var config by remember { mutableStateOf<NewConfig?>(existing?.takeIf { !current }?.let {
        NewConfig(it.machine, it.target.text("cwd"), if (it.target.text("agent") == "claude") Agent.AGENT_CLAUDE else Agent.AGENT_CODEX,
            it.target.text("model"), it.target.text("effort"), when(it.target.text("mode")) { "ask" -> Mode.ASK; "auto" -> Mode.AUTO; "full" -> Mode.FULL; else -> null })
    }) }
    var pickProject by remember { mutableStateOf(false) }
    var pickDirectory by remember { mutableStateOf(false) }
    var settings by remember { mutableStateOf<String?>(null) }
    var timeOpen by remember { mutableStateOf(false) }
    var triggerText by rememberSaveable { mutableStateOf((existing?.trigger ?: ScheduleTime().trigger()).toString()) }
    val trigger = remember(triggerText) { Json.parseToJsonElement(triggerText).jsonObject }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    // A stable key survives recomposition/configuration change and every retry. Freeze the spec
    // after an uncertain create so a retry cannot create another task or change the same key's meaning.
    val request = rememberSaveable { newRequestId() }
    var frozen by remember { mutableStateOf<String?>(null) }
    val start = if (project.isNotEmpty()) projectStart(state, project, config?.agent, config?.machineId) else null
    FetchEarlier(state, page = config == null && start?.settled == false)
    val cfg = config ?: start?.config
    val machine = existing?.machine ?: if (current) sessionView?.machineId else cfg?.machineId
    val writable = machine != null && scheduleWritable(state, machine)
    val blocked = machine?.let { if (!scheduleSupported(state.machines[it])) "这台电脑需要更新 remote-agent，才能管理定时任务" else if (!writable) "电脑不在线，连接后才能保存" else null }
    val content: @Composable ColumnScope.() -> Unit = {
        Column(Modifier.fillMaxWidth().readableColumn().then(if (page) Modifier.weight(1f) else Modifier).imePadding().verticalScroll(rememberScrollState()).padding(horizontal = 16.dp)) {
            if (error.isNotEmpty()) Banner(error, tone = Tone2.ERROR)
            if (blocked != null) Banner(blocked, tone = Tone2.WARN)
            if (!current || existing != null) {
                FieldLabel("任务名")
                ReasonField(title, { if (frozen == null && !busy) title = it }, "可不填，使用消息的第一行")
            }
            FieldLabel(if (current) "发给这个对话的话" else "让 agent 做什么")
            ReasonField(message, { if (frozen == null && !busy) message = it }, "有重要变化时通知我…")
            ScheduleSetting("什么时候", scheduleFrequency(trigger), frozen == null && !busy) { timeOpen = true }
            if (!current) {
                ScheduleSetting("项目", project.takeIf { it.isNotEmpty() }?.let(::projectName) ?: "选择项目", frozen == null && !busy && existing == null) { pickProject = true }
                if (cfg != null) {
                    val agents = machineAgents(state.machines[cfg.machineId])
                    ScheduleAgentSetting(agents, cfg.agent, frozen == null && !busy) { agent ->
                        config = projectConfig(state, project, agent, cfg.machineId)
                    }
                    ScheduleSetting("权限与模型", projectSummary(state, cfg), frozen == null && !busy) { settings = "mode" }
                    if (state.machines.size > 1) ScheduleSetting("电脑", machineLabel(state, cfg.machineId), frozen == null && !busy && existing == null) { settings = "machine" }
                }
                Hint("每次运行新开一个对话，归在这个任务下面。Agent 觉得值得说的时候才通知你。")
                if (machine == null && state.machines.values.any { !scheduleSupported(it) }) Hint("电脑需要更新 remote-agent，才能创建定时任务。")
            } else Hint("消息等这个对话当前这一轮结束后再发，不会打断你正在做的事。")
            Hint("电脑睡眠或关机时不会运行；醒来后 24 小时内补跑一次，再晚就跳过。")
            val ready = writable && !busy && (message.isNotBlank() || frozen != null) && (current || cfg != null && (config != null || start?.settled == true))
            Btn(if (busy) "保存中…" else if (frozen != null) "重试创建" else if (existing == null) "创建" else "保存", {
                if (!ready) return@Btn
                var spec = frozen?.let { Json.parseToJsonElement(it).jsonObject }
                if (spec == null) {
                    val target = if (current) buildJsonObject { put("type", "resume_session"); put("session", destination) }
                    else buildJsonObject {
                        put("type", "new_session"); put("cwd", project); put("agent", if (cfg!!.agent == Agent.AGENT_CLAUDE) "claude" else "codex")
                        if (cfg.model.isNotEmpty()) put("model", cfg.model)
                        if (cfg.effort.isNotEmpty()) put("effort", cfg.effort)
                        cfg.mode?.let { put("mode", it.name.lowercase()) }
                    }
                    spec = JsonObject((existing?.spec.orEmpty()) + taskSpec(title.trim().ifEmpty { message.lineSequence().first().take(60) }, message, trigger, target))
                    if (existing == null) frozen = spec.toString()
                }
                busy = true; error = ""
                val saving = spec
                scope.launch {
                    try {
                        val result = remote.schedules.operate(machine, if (existing == null) "create" else "update", existing, saving, request)
                        onClose()
                        if (existing == null && result != null) nav.task(machine, result.id)
                    } catch (e: kotlinx.coroutines.CancellationException) { throw e }
                    catch (e: Exception) {
                        error = errorText(e)
                        if (e is ScheduleConflictException && existing != null) {
                            val latest = remote.schedules.state.value[machine]?.tasks?.firstOrNull { it.id == existing.id }
                            if (latest != null) {
                                title = latest.title; message = latest.message; triggerText = latest.trigger.toString()
                                if (!current) config = NewConfig(latest.machine, latest.target.text("cwd"), if (latest.target.text("agent") == "claude") Agent.AGENT_CLAUDE else Agent.AGENT_CODEX,
                                    latest.target.text("model"), latest.target.text("effort"), when(latest.target.text("mode")) { "ask" -> Mode.ASK; "auto" -> Mode.AUTO; "full" -> Mode.FULL; else -> null })
                            }
                        }
                        if (e is CommandException && e.fromDaemon && !e.unknown) frozen = null
                    } finally { busy = false }
                }
            }, Modifier.fillMaxWidth().padding(vertical = 16.dp), BtnKind.PRIMARY, ready)
            if (frozen != null) Hint("创建结果尚未确认，再试一次使用同一个请求。返回后可先查看任务列表。")
        }
    }
    if (page) Column(Modifier.fillMaxSize().background(pal.card)) { NavBar(if (current) "定时发到这个对话" else "新任务", onClose); ConnBanner(state); content() }
    else Sheet(onClose) { SheetHead("编辑任务", "取消", onClose); content() }
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
                val picked = op.sets.firstOrNull { it.agent == cfg.agent } ?: op.sets.firstOrNull()
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
            GRow("结束", trailing = { SegPick(listOf("不结束", "次数", "日期"), value.end) { value = value.copy(end = it) } })
            if (value.end == 1) ReasonField(value.count, { value = value.copy(count = it) }, "自动运行次数")
            if (value.end == 2) ScheduleSetting("结束日期", value.until) { date(value.until) { value = value.copy(until = it, deadline = "") } }
        }
        FieldLabel("时区（IANA 名称）")
        ReasonField(value.zone, { value = value.copy(zone = it.trim(), deadline = "") }, "例如 Asia/Shanghai")
        Hint("每天和每周按所选时区运行；每隔按经过的时长计算，创建后先运行一次。电脑醒来后 24 小时内补跑一次。")
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
    val text = if (tasks.size == 1) "定时 · ${scheduleFrequency(next.trigger)}，${if (taskGroup(next) == TaskGroup.ACTIVE) "下次 " else ""}${scheduleNext(next)}" else "${tasks.size} 个定时 · 下次 ${scheduleNext(next)}"
    Row(Modifier.fillMaxWidth().heightIn(min = 44.dp).background(pal.soft).clickable {
        if (tasks.size == 1) nav.task(next.machine, next.id) else nav.schedules()
    }.padding(horizontal = 16.dp, vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
        Ic(Lucide.Clock, 13.dp, pal.muted)
        Text(text, Modifier.weight(1f).padding(start = 6.dp), style = T.meta, color = pal.muted, maxLines = 1, overflow = TextOverflow.Ellipsis)
        ArrowText("管理", pal.blue, T.meta)
    }
}
