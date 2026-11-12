import RACore
import RAProto
import SwiftUI

enum ScheduleConfigPage: String, Identifiable { case project, mode, model, machine; var id: String { rawValue } }
struct ScheduleConfigSheet: View {
    let page: ScheduleConfigPage
    @Binding var config: NewConfig?
    var fixedMachine: String? = nil
    let onClose: () -> Void
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @State private var models: PBListModelsResponse?
    @State private var settings: PBDaemonSettings?
    @State private var error = ""
    @State private var browsing = false
    private var selectionState: RemoteState {
        fixedMachine.map { scheduleEditingState(model.state, machine: $0) } ?? model.state
    }

    var body: some View {
        let state = selectionState
        let c = config
        SheetHead([ScheduleConfigPage.project: L10n.scheduleProject(), .mode: L10n.newSessionPermissions(), .model: L10n.modelsTitle(), .machine: L10n.scheduleComputerAndModel()][page] ?? L10n.commonSettings(), action: L10n.commonDone(), onAction: onClose)
        switch page {
        case .project:
            if browsing {
                ProjectDirPicker(state) { machine, path in
                    let selected = fixedMachine ?? machine
                    config = projectStart(state, key: path, machine: selected)?.config ?? NewConfig(machineId: selected, cwd: path)
                    onClose()
                }
            } else {
                ProjectPickList(state: state, onPick: { path in config = projectStart(state, key: path, machine: fixedMachine)?.config; onClose() }, onNew: { browsing = true })
            }
        case .machine:
            if let c {
                ForEach(machineRows(state, cwd: c.cwd, agent: c.agent)) { row in
                    let supported = state.machines[row.id]?.meta?.capabilities.scheduledTasks == true
                    ListItem(row.name, desc: supported ? row.note : L10n.scheduleIosUpdateRemoteAgentToUseScheduledTasks(), checked: row.id == c.machineId, enabled: row.online && supported) {
                        selectMachine(row.id, state: state)
                    }
                }
                FieldLabel(L10n.modelsTitle())
                modelChoices(c)
            }
        case .mode:
            if let c {
                ForEach(modeChoices(c, models: models, settings: settings)) { choice in
                    ListItem(choice.title, desc: choice.desc, checked: choice.checked, enabled: choice.enabled, danger: choice.mode == .full) {
                        config?.mode = choice.mode; onClose()
                    }
                }
            }
        case .model:
            modelChoices(c)
        }
        if !error.isEmpty { Hint(error, color: DS.red) }
        if c == nil && page != .project { Hint(L10n.scheduleIosChooseAProjectFirst()) }
        Group { EmptyView() }.task(id: (c?.machineId ?? "") + "/" + String(c?.agent.rawValue ?? 0)) {
            guard let c, page == .model || page == .mode || page == .machine else { return }
            models = nil; settings = nil; error = ""
            do {
                models = try await context.remote.listModels(c.machineId, agent: c.agent, fresh: true)
                if case .getSettings(let response)? = try? await context.remote.machineCommand(c.machineId, getSettingsCommand()).kind { settings = response.settings }
            } catch { self.error = errorText(error) }
        }
    }
    func selectMachine(_ id: String, state: RemoteState) {
        guard let c = config else { return }
        config = switchMachine(c, machineId: id, agents: machineAgents(state.machines[id]))
    }

    @ViewBuilder private func modelChoices(_ c: NewConfig?) -> some View {
        if let c, let models {
            ListItem(L10n.modelsDefault(), checked: c.model.isEmpty) { config?.model = ""; config?.effort = "" }
            ForEach(models.models, id: \.slug) { m in
                ListItem(m.displayName.isEmpty ? m.slug : m.displayName, checked: c.model == m.slug) { config = pickModel(c, m) }
            }
            if let efforts = effortChoices(c, models: models, settings: settings) {
                FieldLabel(L10n.modelsEffort())
                ChoiceGrid(efforts.efforts, selected: efforts.selected) { config?.effort = $0 }
            }
        } else if error.isEmpty { LoadingRow(L10n.scheduleIosLoadingModels()) }
    }

}
