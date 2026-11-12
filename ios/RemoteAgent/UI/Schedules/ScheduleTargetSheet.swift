import RACore
import RAProto
import SwiftUI

struct ScheduleTargetSheet: View {
    let machine: String
    let target: ScheduleTarget
    let onSave: (ScheduleTarget) -> Void
    let onClose: () -> Void
    @Environment(AppModel.self) private var model
    @State private var config: NewConfig?
    @State private var picker: ScheduleConfigPage?
    @State private var models = SummaryModels()
    init(machine: String, target: ScheduleTarget, onSave: @escaping (ScheduleTarget) -> Void, onClose: @escaping () -> Void) {
        self.machine = machine; self.target = target; self.onSave = onSave; self.onClose = onClose
        let mode: Mode? = ["ask": .ask, "auto": .auto, "full": .full][target.mode ?? ""]
        _config = State(initialValue: NewConfig(machineId: machine, cwd: target.cwd ?? "", agent: agentById(target.agent).agent, model: target.model ?? "", effort: target.effort ?? "", mode: mode))
    }
    var body: some View {
        VStack(spacing: 0) {
            SheetHead(L10n.scheduleRunIn(), action: L10n.commonSave(), onAction: save)
            if let c = config {
                GRow(L10n.scheduleProject(), value: projectName(c.cwd), chevron: true) { picker = .project }
                ScheduleAgentRow(state: model.state, config: $config, fixedMachine: machine)
                GRow(L10n.newSessionPermissions(), value: effectiveModeText(chosen: c.mode, default: defaultModeOf(model.state.machines[machine], agent: c.agent)), chevron: true, valueColor: settingsModeRed(c, default: defaultModeOf(model.state.machines[machine], agent: c.agent)) ? DS.red : nil) { picker = .mode }
                GRow(L10n.modelsTitle(), value: modelValue(c.model, models: models.list), chevron: true) { picker = .model }
            }
            Hint(L10n.scheduleIosTheTaskIsStoredOnThisComputerChangesOnly())
            TextAction(L10n.commonCancel(), alignment: .center, action: onClose)
        }.kitSheet(item: $picker, size: .large) { page in
            ScheduleConfigSheet(page: page, config: $config, fixedMachine: machine) { picker = nil }
        }
        .modelNames(models, for: config)
    }
    private func save() {
        guard let c = config, c.machineId == machine else { return }
        var next = target
        next.cwd = c.cwd; next.agent = agentInfo(c.agent).id
        next.model = c.model.isEmpty ? nil : c.model; next.effort = c.effort.isEmpty ? nil : c.effort
        next.mode = c.mode.map { $0 == .full ? "full" : $0 == .auto ? "auto" : "ask" }
        onSave(next); onClose()
    }
}

extension View {
    /// Fetches the models of `config`'s computer and CLI into `models`, for the name of the one chosen.
    func modelNames(_ models: SummaryModels, for config: NewConfig?) -> some View {
        modifier(ModelNames(models: models, key: config.map { SummaryModels.Key(machine: $0.machineId, agent: $0.agent) }))
    }
}

private struct ModelNames: ViewModifier {
    let models: SummaryModels
    let key: SummaryModels.Key?
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context

    func body(content: Content) -> some View {
        content.task(id: key) {
            let remote = context.remote
            await models.load(key) { key in
                await HomeLaunch.settled(model)
                return try? await remote.listModels(key.machine, agent: key.agent)
            }
        }
    }
}

func scheduleEditingState(_ state: RemoteState, machine: String) -> RemoteState {
    var scoped = state
    scoped.machines = scoped.machines.filter { $0.key == machine }
    scoped.sessions = scoped.sessions.filter { $0.value.machineId == machine }
    if var prefs = scoped.prefs { prefs.prefs.settings = prefs.prefs.settings.filter { $0.machineID == machine }; scoped.prefs = prefs }
    return scoped
}
func scheduleDestination(_ task: ScheduleTask, state: RemoteState) -> String {
    guard let sid = task.spec.target.session, let session = state.sessions[sid], session.info != nil else { return L10n.scheduleConversation() }
    return titleOf(session)
}
