import Observation
import RACore
import RAProto
import SwiftUI

/// One immutable opening snapshot owns the whole edit, including its compare-and-set revision.
@MainActor @Observable final class ScheduleEditDraft {
    private(set) var task: ScheduleTask?
    func open(_ task: ScheduleTask) { self.task = task }
    func command(spec: ScheduleSpec) throws -> PBCommand {
        guard let task else { throw UserError("请先打开任务编辑器") }
        return try scheduleCommand(operation: "update", taskID: task.id, spec: spec, revision: task.revision)
    }
}

func scheduleStopBanner(_ task: ScheduleTask, now: Date = Date(), zone: TimeZone = .current) -> String {
    if task.missingTarget {
        let failedAt = task.history.first { $0.status == "failed" && $0.reason == task.reason }
            .flatMap { scheduleDate($0.startedAt) }
        let prefix = failedAt.map { scheduleMoment($0, now: now, zone: zone) + " 的运行失败，" } ?? ""
        return prefix + "任务已停下：找不到运行的对话。这个任务只发到原来的对话，恢复后还会失败。"
    }
    let continuation: String
    if task.status == "uncertain" {
        continuation = "继续可能重复执行上次的消息，请先打开运行记录核实。"
    } else if task.status == "failed" && task.oneShot {
        continuation = "可立即额外执行一次，或改时间重排。"
    } else {
        continuation = "恢复或重新安排后从下一次自动时间开始，不重发上次消息。"
    }
    return scheduleReason(task.reason) + "。" + continuation + (task.reason == "agent_schedule_permissions_changed"
        ? "继续会由你接管任务，按任务中显示的权限运行。" : "")
}

struct ScheduleDetailScreen: View {
    let machine: String
    let id: String
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @Environment(\.page) private var page
    @State private var editor = ScheduleEditDraft()
    @State private var timing = false
    @State private var rename = false
    @State private var message = false
    @State private var permissions = false
    @State private var targetEdit = false
    @State private var delete = false
    @State private var expanded: Set<String> = []
    @State private var allHistory = false
    var body: some View {
        let store = context.schedules
        let task = store.task(machine: machine, id: id)
        let mutation = context.scheduleMutations.of(machine + "/" + id)
        Page(gray: true) {
            VStack(spacing: 0) {
                ConnectionBar()
                ScrollViewReader { proxy in
                    ScrollView {
                        if let task {
                            content(task, mutation: mutation)
                        } else {
                            Text(store.loading.contains(machine) ? "读取任务…" : "找不到这个任务").type(T.body, Theme.muted).padding(32)
                        }
                        Color.clear.frame(height: 1).id("schedule-detail-end")
                    }
                    .debugSignal("schedule.detail.end") { proxy.scrollTo("schedule-detail-end", anchor: .bottom) }
                }
            }
        }
        .background(ScheduleConflictObserver(mutation: mutation) {
            timing = false; targetEdit = false; permissions = false; rename = false; message = false
        })
        .navBar(task?.spec.title ?? "任务详情", subtitle: task.map { $0.stateText(online: store.online(machine)) })
        .kitSheet(isPresented: $timing, size: .large) {
            if let task = editor.task {
                ScheduleTimingSheet(trigger: task.spec.trigger, onSave: { t in var spec = task.spec; spec.trigger = t; update(task, spec: spec) }, onClose: { timing = false })
            }
        }
        .kitSheet(isPresented: $targetEdit, size: .large) {
            if let task = editor.task { ScheduleTargetSheet(machine: machine, target: task.spec.target, onSave: { target in var spec = task.spec; spec.target = target; update(task, spec: spec) }, onClose: { targetEdit = false }) }
        }
        .kitSheet(isPresented: $permissions) {
            if let task = editor.task {
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
        .inputAlert(isPresented: $rename, title: "重命名任务", initial: editor.task?.spec.title ?? "", placeholder: "留空就用消息的第一句", confirm: "保存", text: "留空保存会用消息的第一句", limit: 60, lines: 1...1, save: { typed in
            guard let task = editor.task else { return }
            var spec = task.spec; spec.title = scheduleTitle(typed, message: task.spec.message)
            guard spec.title != task.spec.title else { return }
            try await save(task, spec: spec)
        })
        .fullScreenCover(isPresented: $message) {
            if let snapshot = editor.task {
                ScheduleMessageEditor(initial: snapshot.spec.message,
                    enabled: ScheduleEditAccess(task: task ?? snapshot, online: store.online(machine), saving: mutation.running, unknown: mutation.frozen).definition,
                    onSave: { typed in var spec = snapshot.spec; spec.message = typed; try await save(snapshot, spec: spec) },
                    onClose: { message = false })
            }
        }
        .confirm(isPresented: $delete, title: "删除这个任务？", text: "停止之后的自动运行。已发出的消息和已开的对话会保留。", confirm: "删除", danger: true) {
            if let task { perform(task, operation: "cancel", done: { nav.back(from: page) }) }
        }
        .debugSignal("schedule.history.all") { allHistory = true }
        .debugSignal("schedule.edit-message") {
            if let current = store.task(machine: machine, id: id) { editor.open(current); message = true }
        }
        .debugSignal("schedule.edit-target") {
            if let current = store.task(machine: machine, id: id) { editor.open(current); targetEdit = true }
        }
        .debugSignal("schedule.open-run") {
            if let run = store.task(machine: machine, id: id)?.history.first(where: { !($0.sessionID ?? "").isEmpty }) { open(run) }
        }
        .debugSignal("schedule.pause") { if let task = store.task(machine: machine, id: id) { perform(task, operation: "pause") } }
        .debugSignal("schedule.run") { if let task = store.task(machine: machine, id: id) { perform(task, operation: "run") } }
        .task { await store.refresh(machine) }
        .onChange(of: task?.id, initial: true) { _, _ in
            #if DEBUG
            if model.isDemo, let task {
                editor.open(task)
                let panel = HomeLaunch.value("-schedulePanel")
                message = panel == "editor"; timing = panel?.hasPrefix("timing-") == true
            }
            #endif
        }
        .task(id: task?.spec.target.session) {
            if let sid = task?.spec.target.session, model.state.sessions[sid] == nil { _ = try? await context.remote.fetchSession(sid) }
        }
    }
    @ViewBuilder private func content(_ task: ScheduleTask, mutation: ScheduleMutation) -> some View {
        let online = context.schedules.online(machine)
        let failedOnce = task.status == "failed" && task.oneShot
        let recoveryLabel = failedOnce ? "立即额外执行" : "恢复"
        let access = ScheduleEditAccess(task: task, online: online, saving: mutation.running, unknown: mutation.frozen)
        let mutable = access.definition
        VStack(alignment: .leading, spacing: 0) {
            if access.showsReason {
                Banner(scheduleStopBanner(task), tone: .warn).padding(16)
            }
            if !mutation.error.isEmpty { Banner(mutation.error, tone: .error).padding(16) }
            if mutation.frozen {
                TextAction("重试同一请求", enabled: online && !mutation.running && !mutation.expired) { perform(task, operation: "get") }.padding(.horizontal, 16)
                Hint("电脑上可能已经执行，请先查看最新任务状态。").padding(.horizontal, 16)
                TextAction("放弃原请求", enabled: online && !mutation.running) { mutation.abandon() }.padding(.horizontal, 16)
            }
            if task.group == .attention && !task.missingTarget {
                Btn(recoveryLabel, kind: .primary, enabled: online && !mutation.running && !mutation.frozen && (task.active == nil || task.active?.status == "uncertain") && task.manualRequest == nil) { recover(task) }
                    .padding(.horizontal, 16).padding(.vertical, 12)
            }
            GroupCard {
                ToggleRow("完成后通知", isOn: Binding(get: { task.spec.notifyOnFinish }, set: { on in var spec = task.spec; spec.notifyOnFinish = on; update(task, spec: spec) })).disabled(!access.rename)
            }.padding(.top, 16)
            GroupCard {
                GRow("让 agent 做什么", value: "编辑", chevron: mutable, valueColor: mutable ? Theme.blue : Theme.muted, onTap: mutable ? { editor.open(task); message = true } : nil)
                ScheduleMessageText(text: task.spec.message).padding(14)
            }.padding(.top, 16)
            GroupLabel("运行记录", count: task.history.count).padding(.top, 16)
            history(task)
            Hint("“通知了你”表示已请求通知，不保证送达。记录保留最近 100 次，已开的对话仍可查看。").padding(.horizontal, 16).padding(.bottom, 16)
            GroupLabel("设置")
            GroupCard {
                GRow("什么时候", value: scheduleFrequency(task.spec.trigger, includeZone: true), chevron: mutable, onTap: mutable ? { editor.open(task); timing = true } : nil)
                let limits = scheduleLimits(task.spec.trigger)
                if !limits.isEmpty { GRow("结束", value: limits, chevron: mutable, onTap: mutable ? { editor.open(task); timing = true } : nil) }
                if task.spec.target.type == "new_session" {
                    GRow("在哪运行", value: scheduleLocation(task, state: model.state, machine: machine), chevron: mutable, onTap: mutable ? { editor.open(task); targetEdit = true } : nil)
                    GRow("权限", value: permission(task), chevron: mutable, valueColor: task.spec.target.mode == "full" ? Theme.red : nil, onTap: mutable ? { editor.open(task); permissions = true } : nil)
                } else {
                    GRow("发到", value: scheduleDestination(task, state: model.state), chevron: online && !task.missingTarget, onTap: online && !task.missingTarget ? {
                        if let sid = task.spec.target.session { nav.open(.session(id: sid)) }
                    } : nil)
                    GRow("权限", value: permission(task), valueColor: targetSession(task).map { modeOf($0) == .full } == true ? Theme.red : nil)
                }
                GRow("任务名", value: task.spec.title, chevron: access.rename, valueLines: 1, onTap: access.rename ? { editor.open(task); rename = true } : nil)
            }
            Hint((task.spec.target.type == "new_session" ? "每次新开一个对话，归在这个任务下面。" : "消息发到当前对话，等待当前轮结束。") + " " + task.creator + (task.createdAt.isEmpty ? "" : " · " + (scheduleDate(task.createdAt).map { scheduleMoment($0) } ?? ""))).padding(.horizontal, 16).padding(.bottom, 16)
            GroupCard {
                if task.group != .attention {
                    GRow("立即运行一次", action: true, enabled: access.run) { perform(task, operation: "run") }
                }
                if task.group == .attention && task.missingTarget {
                    GRow("恢复", action: true, enabled: online && !mutation.running && !mutation.frozen) { recover(task) }
                }
                if task.group != .ended && task.group != .attention {
                    GRow(task.group == .paused ? "恢复" : "暂停", action: true, enabled: online && !mutation.running && !mutation.frozen) { perform(task, operation: task.group == .paused ? "resume" : "pause") }
                }
                GRow("删除任务", action: true, danger: true, enabled: online && !mutation.running && !mutation.frozen) { delete = true }
            }
            if !online { Hint(machineLabel(model.state, id: machine) + " 连上后才能修改、运行或删除。").padding(.horizontal, 16).padding(.bottom, 16) }
        }
    }
    private func recover(_ task: ScheduleTask) {
        if task.status == "cancelled" && task.visible {
            if task.oneShot { editor.open(task); timing = true }
            else { perform(task, operation: "update", spec: task.spec) }
        } else { perform(task, operation: task.status == "failed" && task.oneShot ? "run" : "resume") }
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
            if allHistory {
                ForEach(task.history) { run in record(run, task: task) }
            } else {
                ForEach(Array(groups.prefix(5))) { group in
                    let run = group.runs[0]
                    if group.folded {
                        GRow(scheduleDay(run.date) + " · \(group.runs.count) 次，都没有通知", sub: "最近一次 \(scheduleTime(run.date))：\(run.foldedSummary)", chevron: false) {
                            if expanded.contains(group.id) { expanded.remove(group.id) } else { expanded.insert(group.id) }
                        }.trailing { Arrow(Theme.muted, direction: expanded.contains(group.id) ? .up : .down) }
                        if expanded.contains(group.id) { ForEach(group.runs) { record($0, task: task) } }
                    } else { record(run, task: task) }
                }
            }
            if task.history.count > 5 && !allHistory {
                GRow("查看全部 \(task.history.count) 次", action: true) { allHistory = true }
            }
        }
    }
    private func record(_ run: ScheduleRun, task: ScheduleTask) -> some View {
        let sid = run.sessionID ?? ""
        return GRow(scheduleMoment(run.date) + " · " + run.result + (run.notificationLabel.isEmpty ? "" : " · " + run.notificationLabel) + (run.manual == true ? " · 手动" : ""), sub: run.detail, chevron: !sid.isEmpty, danger: run.status == "failed", onTap: sid.isEmpty ? nil : {
            open(run)
        })
    }
    private func open(_ run: ScheduleRun) {
        guard let sid = run.sessionID, !sid.isEmpty else { return }
        context.sessionStore.jumpToScheduledRun(sid, runID: run.id, seq: run.firstSeq)
        nav.open(.session(id: sid))
    }
    private func save(_ task: ScheduleTask, spec: ScheduleSpec) async throws {
        guard context.schedules.online(machine) else { throw UserError("电脑不在线，连上后才能修改") }
        try await context.scheduleMutations.of(machine + "/" + id).execute(remote: context.remote, machine: machine, command: {
            try editor.command(spec: spec)
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

struct ScheduleConflictObserver: View {
    let mutation: ScheduleMutation
    let dismiss: () -> Void
    var body: some View { Color.clear.frame(width: 0, height: 0).onChange(of: mutation.conflicts) { _, _ in dismiss() } }
}
