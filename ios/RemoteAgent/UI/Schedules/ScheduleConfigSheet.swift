import RACore
import RAProto
import SwiftUI

enum ScheduleConfigFocus: Sendable {
    case project, mode, model, target, all
}

struct ScheduleConfigSheet: View {
    let focus: ScheduleConfigFocus
    var showSaveButton: Bool = false
    let onClose: () -> Void

    @Environment(AccountContext.self) private var context
    @State private var browsingNewDir = false
    @State private var pickingProject = false
    @State private var showAllModels = false
    @State private var confirmFullAccess = false

    private var vm: PBScheduleFormViewModel { context.core.scheduleForm }
    private var formKey: String { vm.formKey }

    var body: some View {
        let busy = vm.mutation.busy
        let enabled = vm.editable && !busy && !vm.mutation.frozen
        if focus == .project || pickingProject {
            projectPickerView(enabled: enabled)
        } else {
            configBody(enabled: enabled, busy: busy)
                .confirm(
                    isPresented: $confirmFullAccess,
                    title: L10n.scheduleRunWithFullAccess(),
                    text: L10n.scheduleNoApprovalIsRequiredForAnyOperationWheneverThis(),
                    confirm: L10n.modesFullAccess(),
                    danger: true
                ) {
                    submitForm(confirmedFullAccess: true)
                }
        }
    }

    private func configBody(enabled: Bool, busy: Bool) -> some View {
        let title: String = switch focus {
        case .mode: L10n.newSessionPermissions()
        case .target: L10n.scheduleRunIn()
        default: L10n.modelsTitle()
        }
        return NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    if showSaveButton {
                        ScheduleMutationBanner(
                            mutation: vm.mutation,
                            onRetry: { context.dispatch { $0.scheduleRetryMutation = .with { $0.formKey = formKey } } },
                            onAbandon: { context.dispatch { $0.scheduleAbandonMutation = .with { $0.formKey = formKey } } }
                        )
                    }

                    if focus == .target {
                        GroupCard {
                            let proj = vm.hasProject_p ? vm.projectName.rendered() : L10n.newSessionIosChooseProject()
                            GRow(L10n.scheduleProject(), value: proj, chevron: enabled) {
                                if enabled { pickingProject = true }
                            }
                            ScheduleAgentRow(
                                availableAgents: vm.availableAgents,
                                selected: vm.agent,
                                enabled: enabled
                            ) { agent in
                                context.dispatch {
                                    $0.scheduleFormSelectAgent = .with {
                                        $0.formKey = formKey
                                        $0.agent = agent
                                    }
                                }
                            }
                        }
                    }

                    if (focus == .all || focus == .model) && !vm.lockMachine && vm.machineSelectable && vm.machineChoices.count > 1 {
                        GroupLabel(L10n.commonMachine())
                        GroupCard {
                            ForEach(vm.machineChoices, id: \.machineID) { m in
                                let note = m.hasNote ? m.note.rendered() : nil
                                ListItem(m.name, desc: note?.isEmpty == false ? note : nil, checked: m.selected, enabled: enabled && m.supported) {
                                    context.dispatch {
                                        $0.scheduleFormSelectMachine = .with {
                                            $0.formKey = formKey
                                            $0.value = m.machineID
                                        }
                                    }
                                }
                            }
                        }
                    }

                    if focus == .mode || focus == .all {
                        GroupLabel(L10n.newSessionPermissions())
                        GroupCard {
                            ForEach(vm.modeChoices, id: \.mode.rawValue) { opt in
                                let note = !opt.enabled && opt.hasDisabledReason ? opt.disabledReason.rendered() : nil
                                ListItem(
                                    opt.label.rendered(),
                                    desc: note?.isEmpty == false ? note : nil,
                                    checked: opt.selected,
                                    enabled: enabled && opt.enabled,
                                    danger: opt.isDanger
                                ) {
                                    selectModel(vm.model, effort: vm.effort, mode: opt.mode)
                                }
                            }
                        }
                    }

                    if focus != .mode {
                        modelSection(enabled: enabled)
                    }
                }
                .padding(.horizontal, 16).padding(.vertical, 12)
            }
            .background(DS.gbg.ignoresSafeArea())
            .navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button(L10n.commonCancel()) {
                        if showSaveButton {
                            context.dispatch {
                                $0.scheduleFormClose = .with {
                                    $0.formKey = formKey
                                    $0.value = true
                                }
                            }
                        }
                        onClose()
                    }
                    .disabled(busy)
                }
                ToolbarItem(placement: .topBarTrailing) {
                    if showSaveButton {
                        Button(busy ? L10n.commonIosSaving() : L10n.commonSave()) {
                            if vm.requiresFullAccessConfirm {
                                confirmFullAccess = true
                            } else {
                                submitForm(confirmedFullAccess: false)
                            }
                        }
                        .fontWeight(.semibold)
                        .disabled(!vm.canSubmit || !enabled)
                    } else {
                        Button(L10n.commonDone(), action: onClose).fontWeight(.semibold)
                    }
                }
            }
        }
    }

    @ViewBuilder private func modelSection(enabled: Bool) -> some View {
        let panel = vm.modelPanel
        let panelEnabled = enabled && panel.panelEnabled
        GroupLabel(L10n.modelsTitle())
        if panel.hasModelsError && panel.models.isEmpty {
            Banner(
                panel.modelsError.rendered(),
                tone: .warn,
                action: L10n.commonRetry(),
                onAction: {
                    context.dispatch {
                        $0.scheduleFormRetryModels = .with { $0.formKey = formKey }
                    }
                }
            )
        } else if panel.modelsLoading && panel.models.isEmpty {
            LoadingRow(L10n.modelsReadingMachine(name: vm.machineName))
        } else {
            let active = panel.models.first(where: \.checked)
            if let active, active.hasFixedEffortNote {
                Hint(active.fixedEffortNote.rendered())
            } else if panel.effortChoices.count > 1 {
                ChoiceGrid(
                    panel.effortChoices,
                    selected: panel.selectedEffort,
                    enabled: panelEnabled
                ) { eff in
                    selectModel(vm.model, effort: eff)
                }
            }
            let shown = visibleModels(panel.models)
            GroupCard {
                let defaultLabel = panel.hasDefaultModelLabel ? panel.defaultModelLabel.rendered() : L10n.modelsDefault()
                ListItem(defaultLabel, checked: panel.defaultModelChecked, enabled: panelEnabled) {
                    selectModel("", effort: "")
                }
                ForEach(shown, id: \.slug) { m in
                    ListItem(m.displayName.isEmpty ? m.slug : m.displayName, checked: m.checked, enabled: panelEnabled) {
                        selectModel(m.slug, effort: "")
                    }
                }
                if !showAllModels && shown.count < panel.models.count {
                    GRow(L10n.modelsAll(), chevron: true) { showAllModels = true }
                }
            }
        }
    }

    private func projectPickerView(enabled: Bool) -> some View {
        NavigationStack {
            Group {
                if browsingNewDir {
                    CoreDirPicker(
                        machine: vm.lockMachine && !vm.machineID.isEmpty ? vm.machineID : nil,
                        forNewTask: true
                    ) { machineID, path in
                        context.dispatch {
                            $0.scheduleFormSelectProject = .with {
                                $0.formKey = formKey
                                $0.projectKey = path
                                $0.machineID = machineID
                            }
                        }
                        finishProjectPick()
                    }
                } else {
                    CoreProjectPickList(
                        listing: context.core.manageProjects.listing,
                        onSearch: { q in context.dispatch { $0.projectSetSearch.query = q } },
                        onPick: { key in
                            guard enabled else { return }
                            context.dispatch {
                                $0.scheduleFormSelectProject = .with {
                                    $0.formKey = formKey
                                    $0.projectKey = key
                                }
                            }
                            finishProjectPick()
                        },
                        onNew: { browsingNewDir = true }
                    )
                    .task { await context.coreHost.setManageActive(true) }
                    .onDisappear { Task { await context.coreHost.setManageActive(false) } }
                }
            }
            .navigationTitle(browsingNewDir ? L10n.projectsNewChooseDirectory() : L10n.scheduleProject())
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button(L10n.commonCancel()) {
                        if browsingNewDir {
                            browsingNewDir = false
                        } else {
                            finishProjectPick()
                        }
                    }
                }
            }
        }
    }

    private func finishProjectPick() {
        browsingNewDir = false
        if focus == .project {
            onClose()
        } else {
            pickingProject = false
        }
    }

    private func visibleModels(_ all: [PBCoreModelOptionView]) -> [PBCoreModelOptionView] {
        let limit = 8
        if showAllModels || all.count <= limit { return all }
        var head = Array(all.prefix(limit))
        if let checked = all.first(where: \.checked), !head.contains(where: { $0.slug == checked.slug }) {
            head[limit - 1] = checked
        }
        return head
    }

    private func selectModel(_ model: String, effort: String, mode: PBApprovalMode? = nil) {
        context.dispatch {
            $0.scheduleFormSelectModel = .with {
                $0.formKey = formKey
                $0.model = model
                $0.effort = effort
                if let mode { $0.mode = mode }
            }
        }
    }

    private func submitForm(confirmedFullAccess: Bool) {
        context.dispatch {
            $0.scheduleFormSubmit = .with {
                $0.formKey = formKey
                $0.value = confirmedFullAccess
            }
        }
    }
}
