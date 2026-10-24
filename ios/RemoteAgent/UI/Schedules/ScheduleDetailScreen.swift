import RACore
import RAProto
import SwiftUI

struct ScheduleDetailScreen: View {
    let machine: String
    let id: String
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @Environment(\.page) private var page
    @State private var timing = false
    @State private var rename = false
    @State private var message = false
    @State private var permissions = false
    @State private var targetEdit = false
    @State private var delete = false
    @State private var expanded: Set<String> = []
    var body: some View {
        let store = context.schedules
        let task = store.task(machine: machine, id: id)
        let mutation = context.scheduleMutations.of(machine + "/" + id)
        Page(gray: true) {
            VStack(spacing: 0) {
                ConnectionBar()
                ScrollView {
                    if let task {
                        content(task, mutation: mutation)
                    } else {
                        Text(store.loading.contains(machine) ? "读取任务…" : "找不到这个任务").type(T.body, Theme.muted).padding(32)
                    }
                }
            }
        }
        .navBar(task?.spec.title ?? "任务详情", subtitle: task.map { ($0.spec.target.type == "new_session" ? "新开对话" : "当前对话") + " · " + $0.stateText(online: store.online(machine)) })
        .kitSheet(isPresented: $timing, size: .large) {
            if let task {
                ScheduleTimingSheet(trigger: task.spec.trigger, onSave: { t in var spec = task.spec; spec.trigger = t; update(task, spec: spec) }, onClose: { timing = false })
            }
        }
        .kitSheet(isPresented: $targetEdit, size: .large) {
            if let task { ScheduleTargetSheet(machine: machine, target: task.spec.target, onSave: { target in var spec = task.spec; spec.target = target; update(task, spec: spec) }, onClose: { targetEdit = false }) }
        }
        .kitSheet(isPresented: $permissions) {
            if let task {
                ScheduleConfigSheet(page: .mode, config: Binding(get: {
                    NewConfig(machineId: machine, cwd: task.spec.target.cwd ?? "", agent: task.spec.target.agent == "claude" ? .claude : .codex,
                              model: task.spec.target.model ?? "", effort: task.spec.target.effort ?? "", mode: ["ask": Mode.ask, "auto": .auto, "full": .full][task.spec.target.mode ?? ""])
                }, set: { selected in
                    var spec = task.spec
                    spec.target.mode = selected?.mode.map { $0 == .full ? "full" : $0 == .auto ? "auto" : "ask" }
                    update(task, spec: spec)
                }), fixedMachine: machine) { permissions = false }
            }
        }
        .inputAlert(isPresented: $rename, title: "重命名任务", initial: task?.spec.title ?? "", placeholder: "留空就用消息的第一句", confirm: "保存", text: "留空保存会用消息的第一句", limit: 60, lines: 1...1, save: { typed in
            guard let task else { return }
            var spec = task.spec; spec.title = scheduleTitle(typed, message: task.spec.message)
            guard spec.title != task.spec.title else { return }
            try await save(task, spec: spec)
        })
        .inputAlert(isPresented: $message, title: "消息", initial: task?.spec.message ?? "", placeholder: "让 agent 做什么", confirm: "保存", limit: 20000, lines: 4...12, save: { typed in
            guard let task, !typed.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return }
            var spec = task.spec; spec.message = typed; try await save(task, spec: spec)
        })
        .confirm(isPresented: $delete, title: "删除任务？", text: "停止之后的自动运行。已发出的消息和已开的对话会保留。", confirm: "删除", danger: true) {
            if let task { perform(task, operation: "cancel", done: { nav.back(from: page) }) }
        }
        .debugSignal("schedule.open-run") {
            if let run = store.task(machine: machine, id: id)?.history.first(where: { !($0.sessionID ?? "").isEmpty }) { open(run) }
        }
        .debugSignal("schedule.pause") { if let task = store.task(machine: machine, id: id) { perform(task, operation: "pause") } }
        .debugSignal("schedule.run") { if let task = store.task(machine: machine, id: id) { perform(task, operation: "run") } }
        .task { await store.refresh(machine) }
        .task(id: task?.spec.target.session) {
            if let sid = task?.spec.target.session, model.state.sessions[sid] == nil { _ = try? await context.remote.fetchSession(sid) }
        }
    }
    @ViewBuilder private func content(_ task: ScheduleTask, mutation: ScheduleMutation) -> some View {
        let online = context.schedules.online(machine)
        let failedOnce = task.status == "failed" && task.oneShot
        let systemCancelled = task.status == "cancelled" && task.visible
        let recoveryLabel = systemCancelled ? "重新安排" : failedOnce ? "立即额外执行" : "恢复"
        let access = ScheduleEditAccess(task: task, online: online, saving: mutation.running, unknown: mutation.frozen)
        let mutable = access.definition
        VStack(alignment: .leading, spacing: 0) {
            if model.state.conn == .online && !online { Banner("显示的是上次的任务状态，连上后才能修改").padding(16) }
            if task.group == .attention || (task.group == .paused && !(task.reason ?? "").isEmpty) {
                Banner(scheduleReason(task.reason), tone: .warn).padding(16)
                Hint(failedOnce ? "确认后会额外执行一次，不会重新启用自动计划。" : "恢复后会重新安排下一次运行；已经发出的消息不会撤回。").padding(.horizontal, 16)
            }
            if !mutation.error.isEmpty { Banner(mutation.error, tone: .error).padding(16) }
            if mutation.frozen {
                TextAction("重试同一请求", enabled: online && !mutation.running && !mutation.expired) { perform(task, operation: "get") }.padding(.horizontal, 16)
                Hint("电脑上可能已经执行，请先查看最新任务状态。").padding(.horizontal, 16)
                TextAction("放弃原请求", enabled: !mutation.running) { mutation.abandon() }.padding(.horizontal, 16)
            }
            GroupCard {
                GRow("任务名", value: task.spec.title, chevron: access.rename, enabled: access.rename, onTap: access.rename ? { rename = true } : nil)
                GRow("什么时候", value: scheduleFrequency(task.spec.trigger, includeZone: true), chevron: mutable, enabled: mutable, onTap: mutable ? { timing = true } : nil)
                if let count = task.spec.trigger.maxRuns, count > 0 { GRow("结束", value: "自动运行 \(count) 次后", chevron: mutable, onTap: mutable ? { timing = true } : nil) }
                else if let until = task.spec.trigger.until.flatMap(scheduleDate) { GRow("结束", value: scheduleMoment(until), chevron: mutable, onTap: mutable ? { timing = true } : nil) }
                if task.spec.target.type == "new_session" {
                    GRow("在哪运行", value: scheduleLocation(task, state: model.state, machine: machine), chevron: mutable, enabled: mutable, onTap: mutable ? { targetEdit = true } : nil)
                    GRow("权限", value: permission(task), chevron: mutable, enabled: mutable, valueColor: task.spec.target.mode == "full" ? Theme.red : nil, onTap: mutable ? { permissions = true } : nil)
                } else {
                    GRow("发到", value: targetSession(task).map { titleOf($0) } ?? "对话", chevron: true) {
                        if let sid = task.spec.target.session { nav.open(.session(id: sid)) }
                    }
                    GRow("权限", value: permission(task), valueColor: targetSession(task).map { modeOf($0) == .full } == true ? Theme.red : nil)
                }
                GRow("消息", value: "编辑", chevron: mutable, enabled: mutable, onTap: mutable ? { message = true } : nil)
                Text(task.spec.message).type(T.body, Theme.fg).frame(maxWidth: .infinity, alignment: .leading).padding(14).textSelection(.enabled)
            }.padding(.top, 16)
            Hint((task.spec.target.type == "new_session" ? "每次新开一个对话" : "发到当前对话") + " · " + (task.source == "agent" ? "Agent 创建" : "你创建") + (task.createdAt.isEmpty ? "" : " · " + (scheduleDate(task.createdAt).map { scheduleMoment($0) } ?? ""))).padding(.horizontal, 16)
            GroupLabel("运行记录").padding(.top, 16)
            history(task)
            Hint("“通知了你”表示已请求通知，不保证送达。记录保留最近 100 次，已开的对话仍可查看。").padding(.horizontal, 16).padding(.bottom, 16)
            if task.group == .attention {
                Btn(recoveryLabel, kind: .primary, enabled: online && !mutation.running && !mutation.frozen && (task.active == nil || task.active?.status == "uncertain")) {
                    if systemCancelled && task.oneShot { timing = true }
                    else { perform(task, operation: systemCancelled ? "update" : failedOnce ? "run" : "resume", spec: systemCancelled ? task.spec : nil) }
                }.padding(.horizontal, 16).padding(.bottom, 12)
            }
            GroupCard {
                GRow("立即运行一次", action: true, enabled: mutable && task.group != .ended && task.group != .attention) { perform(task, operation: "run") }
                if task.group != .ended && task.group != .attention {
                    GRow(task.group == .paused ? "恢复" : "暂停", action: true, enabled: online && !mutation.running && !mutation.frozen) { perform(task, operation: task.group == .paused ? "resume" : "pause") }
                }
                GRow("删除任务", action: true, danger: true, enabled: online && !mutation.running && !mutation.frozen) { delete = true }
            }
        }
    }
    private func targetSession(_ task: ScheduleTask) -> SessionView? { task.spec.target.session.flatMap { model.state.sessions[$0] } }
    private func permission(_ task: ScheduleTask) -> String {
        if task.spec.target.type == "resume_session" { return targetSession(task).flatMap { modeLabel($0) }?.text ?? "请在对话里核实当前权限" }
        return ["ask": "询问", "auto": "自动", "full": "完全放行"][task.spec.target.mode ?? ""] ?? "跟随 CLI 设置"
    }
    @ViewBuilder private func history(_ task: ScheduleTask) -> some View {
        let groups = scheduleHistoryGroups(task.history)
        GroupCard {
            if groups.isEmpty { GRow("还没有运行") }
            ForEach(groups) { group in
                let run = group.runs[0]
                if group.folded {
                    GRow(scheduleDay(run.date) + " · \(group.runs.count) 次，都没有通知", sub: "最近一次 \(scheduleTime(run.date))：\(run.finalSummary ?? "")", chevron: false) {
                        if expanded.contains(group.id) { expanded.remove(group.id) } else { expanded.insert(group.id) }
                    }.trailing { Arrow(Theme.muted, direction: expanded.contains(group.id) ? .up : .down) }
                    if expanded.contains(group.id) { ForEach(group.runs) { record($0, task: task) } }
                } else { record(run, task: task) }
            }
        }
    }
    private func record(_ run: ScheduleRun, task: ScheduleTask) -> some View {
        let sid = run.sessionID ?? ""
        return GRow(scheduleMoment(run.date) + " · " + run.result + (run.manual == true ? " · 手动" : ""), sub: run.detail, chevron: !sid.isEmpty, enabled: !sid.isEmpty, onTap: sid.isEmpty ? nil : {
            open(run)
        })
    }
    private func open(_ run: ScheduleRun) {
        guard let sid = run.sessionID, !sid.isEmpty else { return }
        context.sessionStore.jumpToScheduledRun(sid, runID: run.id, seq: run.startSeq + 1)
        nav.open(.session(id: sid))
    }
    private func save(_ task: ScheduleTask, spec: ScheduleSpec) async throws {
        guard context.schedules.online(machine) else { throw UserError("电脑不在线，连上后才能修改") }
        try await context.scheduleMutations.of(machine + "/" + id).execute(remote: context.remote, machine: machine, command: {
            try scheduleCommand(operation: "update", taskID: id, spec: spec, revision: task.revision)
        }, refresh: { await context.schedules.refresh(machine) })
    }
    private func update(_ task: ScheduleTask, spec: ScheduleSpec) { perform(task, operation: "update", spec: spec) }
    private func perform(_ task: ScheduleTask, operation: String, spec: ScheduleSpec? = nil, done: @escaping () -> Void = {}) {
        guard context.schedules.online(machine) else { return }
        context.scheduleMutations.of(machine + "/" + id).run(remote: context.remote, machine: machine, command: {
            try scheduleCommand(operation: operation, taskID: id, spec: spec, revision: task.revision)
        }, refresh: { await context.schedules.refresh(machine) }, done: { _ in done() })
    }
}
