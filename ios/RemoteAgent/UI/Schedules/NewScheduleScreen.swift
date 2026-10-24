import Observation
import RACore
import RAProto
import SwiftUI

@MainActor @Observable
final class ScheduleDraft {
    var name = ""
    var message = ""
    var trigger: ScheduleTrigger
    var config: NewConfig?
    private(set) var creationKey = UUID().uuidString
    private var submitted: ScheduleSpec?
    func key(for spec: ScheduleSpec) -> String {
        if let submitted, submitted != spec, !mutation.frozen { creationKey = UUID().uuidString }
        submitted = spec; return creationKey
    }
    let mutation = ScheduleMutation()
    init() {
        var t = ScheduleTrigger(type: "calendar"); t.time = "08:00"; t.timezone = TimeZone.current.identifier
        trigger = t
    }
    func spec(session: SessionView?, state: RemoteState) -> ScheduleSpec? {
        var target: ScheduleTarget
        if let session {
            target = ScheduleTarget(type: "resume_session"); target.session = session.id
        } else if let c = config {
            target = ScheduleTarget(type: "new_session"); target.cwd = c.cwd; target.agent = c.agent == .claude ? "claude" : "codex"
            target.model = c.model.isEmpty ? nil : c.model
            target.effort = c.effort.isEmpty ? nil : c.effort
            let mode = c.mode ?? defaultModeOf(state.machines[c.machineId], agent: c.agent).mode
            target.mode = mode.map { $0 == .full ? "full" : $0 == .auto ? "auto" : "ask" }
        } else { return nil }
        let title = scheduleTitle(name, message: message)
        guard !message.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty, !title.isEmpty else { return nil }
        return ScheduleSpec(title: title, message: message, trigger: trigger, target: target)
    }
}
@MainActor final class ScheduleDrafts {
    private var values: [String: ScheduleDraft] = [:]
    func of(_ key: String) -> ScheduleDraft { if let d = values[key] { return d }; let d = ScheduleDraft(); values[key] = d; return d }
    func clear(_ key: String) { values[key] = nil }
}
extension AccountContext { var scheduleDrafts: ScheduleDrafts { memory.value { ScheduleDrafts() } } }

struct NewScheduleScreen: View {
    let sessionID: String
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @Environment(\.page) private var page
    @State private var timing = false
    @State private var picker: ScheduleConfigPage?
    var body: some View {
        let draft = context.scheduleDrafts.of(sessionID)
        let state = model.state
        let session = state.sessions[sessionID]
        let machine = session?.machineId ?? draft.config?.machineId ?? ""
        let spec = draft.spec(session: session, state: state)
        let supported = state.machines[machine]?.meta?.capabilities.scheduledTasks == true
        let ready = (sessionID.isEmpty || session != nil) && context.schedules.online(machine) && supported && spec != nil
        Page(gray: true) {
            VStack(spacing: 0) {
                ConnectionBar()
                ScrollView {
                    VStack(alignment: .leading, spacing: 0) {
                        VStack(spacing: 12) {
                            InputField("任务名（可选）", text: Bindable(draft).name, placeholder: "留空就用消息的第一句").onChange(of: draft.name) { _, value in draft.name = titleInput(value) }
                            InputField("让 agent 做什么", text: Bindable(draft).message, placeholder: "检查什么，有结果时通知我…", lines: 4...12)
                        }.padding(16).disabled(draft.mutation.frozen)
                        GroupCard {
                            GRow("什么时候", value: scheduleFrequency(draft.trigger, includeZone: true), chevron: true) { timing = true }
                            if sessionID.isEmpty {
                                GRow("项目", value: draft.config.map { projectName($0.cwd) } ?? "选择项目", chevron: true) { picker = .project }
                                GRow("CLI", value: draft.config.map { agentName($0.agent) } ?? "选择 CLI", chevron: true) { picker = .agent }
                                GRow("权限", value: draft.config.map { effectiveModeText(chosen: $0.mode, default: defaultModeOf(state.machines[$0.machineId], agent: $0.agent)) } ?? "跟随 CLI 设置", chevron: true, valueColor: draft.config.map { settingsModeRed($0, default: defaultModeOf(state.machines[$0.machineId], agent: $0.agent)) } == true ? Theme.red : nil) { picker = .mode }
                                GRow("模型", value: draft.config?.model.isEmpty == false ? draft.config?.model : "默认", chevron: true) { picker = .model }
                                if state.machines.count > 1 { GRow("机器和模型", value: machineLabel(state, id: machine), chevron: true) { picker = .machine } }
                            }
                        }
                        .disabled(draft.mutation.frozen)
                        if !machine.isEmpty && !supported { Hint("这台电脑需要更新 remote-agent 才能使用定时任务。", color: Theme.orangeInk).padding(.horizontal, 16) }
                        if !machine.isEmpty && supported && !context.schedules.online(machine) { Hint("电脑不在线，连上后才能创建任务。").padding(.horizontal, 16) }
                        if sessionID.isEmpty { Hint("每次新开一个对话，归在任务下面。立即运行一次不占自动次数及日历当天名额。").padding(.horizontal, 16) }
                        if !draft.mutation.error.isEmpty { Banner(draft.mutation.error, tone: .error).padding(16) }
                        if draft.mutation.frozen {
                            TextAction("放弃原请求", enabled: !draft.mutation.running) { draft.mutation.abandon() }.padding(.horizontal, 16)
                        }
                        Btn(draft.mutation.running ? "正在创建…" : draft.mutation.frozen ? "重试同一请求" : "创建", kind: .primary, enabled: ready && !draft.mutation.running && !draft.mutation.expired) { create(draft, machine: machine, spec: spec) }.padding(16)
                    }.disabled(draft.mutation.running)
                }.scrollDismissesKeyboard(.interactively)
            }
        }
        .navBar(sessionID.isEmpty ? "新任务" : "定时发到这个对话")
        .kitSheet(isPresented: $timing, size: .large) {
            ScheduleTimingSheet(trigger: draft.trigger, onSave: { draft.trigger = $0 }, onClose: { timing = false })
        }
        .kitSheet(item: $picker, size: .large) { page in
            ScheduleConfigSheet(page: page, config: Bindable(draft).config) { picker = nil }
        }
        .task(id: state.machines.keys.sorted()) {
            if draft.config == nil, let p = listedProjects(state).first { draft.config = projectStart(state, key: p.key)?.config }
            #if DEBUG
            if URL(string: context.account.server)?.host == "127.0.0.1", let cwd = HomeLaunch.value("-scheduleCwd"), let machine = state.machines.keys.sorted().first {
                draft.config = projectStart(state, key: cwd, machine: machine)?.config ?? NewConfig(machineId: machine, cwd: cwd)
                if let agent = HomeLaunch.value("-scheduleAgent") { draft.config?.agent = agent == "claude" ? .claude : .codex }
                if let model = HomeLaunch.value("-scheduleModel") { draft.config?.model = model }
                if let mode = HomeLaunch.value("-scheduleMode") { draft.config?.mode = mode == "full" ? .full : mode == "auto" ? .auto : .ask }
            }
            #endif
        }
        .debugSignal("schedule.create") {
            #if DEBUG
            if let message = HomeLaunch.value("-scheduleMessage") { draft.message = message }
            if let name = HomeLaunch.value("-scheduleName") { draft.name = name }
            if let at = HomeLaunch.value("-scheduleAt"), let date = scheduleDate(at) {
                var t = ScheduleTrigger(type: "calendar"); t.time = scheduleTime(date); t.timezone = TimeZone.current.identifier; draft.trigger = t
            }
            if let sid = state.sessions[sessionID], !sessionID.isEmpty {
                create(draft, machine: sid.machineId, spec: draft.spec(session: sid, state: model.state))
            } else {
                create(draft, machine: draft.config?.machineId ?? "", spec: draft.spec(session: nil, state: model.state))
            }
            #endif
        }
    }
    private func create(_ draft: ScheduleDraft, machine: String, spec: ScheduleSpec?) {
        guard context.schedules.online(machine), let spec else { return }
        let key = draft.key(for: spec)
        draft.mutation.run(remote: context.remote, machine: machine, command: {
            try scheduleCommand(operation: "create", spec: spec, creationKey: key, sessionID: sessionID)
        }, refresh: { await context.schedules.refresh(machine) }) { result in
            guard case .schedule(let response) = result.kind, let task = try? JSONDecoder().decode(ScheduleTask.self, from: response.resultJson) else { return }
            context.scheduleDrafts.clear(sessionID)
            if nav.showing(page) {
                nav.back()
                nav.open(.schedule(machine: machine, id: task.id))
            }
        }
    }
}
