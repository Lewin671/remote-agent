// 项目设置 backed directly by `PBCoreNewSessionViewModel` and `PBCoreNewSessionModelPanelView`
// for ProjectScreen, NewTaskScreen, and SessionScreen.
import RACore
import RAProto
import SwiftUI

enum ProjectSettingsPage: String, Sendable {
    case main, machine, model
}

/// The sheet as it was asked for: a page to open on, and a new one each time so that nothing of the last is left.
struct ProjectSettingsRequest: Identifiable {
    let id = UUID()
    var page = ProjectSettingsPage.main
}

struct CoreProjectSettingsSheet: View {
    let open: ProjectSettingsPage
    let onClose: () -> Void
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @State private var page: ProjectSettingsPage
    @State private var allModels = false

    init(open: ProjectSettingsPage = .main, onClose: @escaping () -> Void) {
        self.open = open
        self.onClose = onClose
        _page = State(initialValue: open)
    }

    var body: some View {
        let vm = context.core.newSession
        let panel = vm.modelPanel
        let blocked = prefsBlocked(model.state)
        let hasMachines = panel.machineChoices.count > 1
        let root = open == .main && !hasMachines ? ProjectSettingsPage.model : open
        let shown = page == .main ? root : page
        let back: (() -> Void)? = shown == root ? nil : { page = .main }

        Group {
            switch shown {
            case .main:
                mainPage(vm: vm, panel: panel, blocked: blocked)
            case .machine:
                machinesPage(vm: vm, panel: panel, blocked: blocked, back: back)
            case .model:
                modelPage(vm: vm, panel: panel, blocked: blocked, back: back)
            }
        }
        .debugSignal("settings.apply") {
            let cfg = vm.config
            var nextModel = cfg.model
            var nextEffort = cfg.effort
            var nextStandard = cfg.standardContext
            for word in (HomeLaunch.value("-settingsEdit") ?? "").split(separator: ",") {
                switch word {
                case "standard": nextStandard = true
                default:
                    if word.hasPrefix("effort=") { nextEffort = String(word.dropFirst(7)) }
                    if word.hasPrefix("model=") { nextModel = String(word.dropFirst(6)) }
                }
            }
            context.dispatch {
                $0.newSessionSelectModel = .with {
                    $0.model = nextModel
                    $0.effort = nextEffort
                    $0.standardContext = nextStandard
                }
            }
            onClose()
        }
    }

    @ViewBuilder private func mainPage(
        vm: PBCoreNewSessionViewModel,
        panel: PBCoreNewSessionModelPanelView,
        blocked: String?
    ) -> some View {
        SheetHead(L10n.projectsIosSettings(), action: L10n.commonDone(), onAction: onClose)
        if let blocked { Banner(blocked).padding(.bottom, 10) }
        if !vm.settled { Hint(L10n.newSessionReadingPrevious()) }
        let modelValue = vm.effortLabel.isEmpty
            ? vm.modelLabel.rendered()
            : "\(vm.modelLabel.rendered()) · \(vm.effortLabel)"
        if panel.machineChoices.count > 1 {
            GRow(L10n.commonMachine(), value: vm.machineName, chevron: true) { page = .machine }
        }
        GRow(L10n.modelsTitle(), value: modelValue, chevron: true) { page = .model }
        TextAction(L10n.commonClose(), alignment: .center, action: onClose)
    }

    @ViewBuilder private func machinesPage(
        vm: PBCoreNewSessionViewModel,
        panel: PBCoreNewSessionModelPanelView,
        blocked: String?,
        back: (() -> Void)?
    ) -> some View {
        SheetHead(
            L10n.commonMachine(),
            action: L10n.commonDone(),
            onAction: onClose,
            onBack: back
        )
        if let blocked { Banner(blocked).padding(.bottom, 10) }
        ScrollView {
            VStack(spacing: 0) {
                ForEach(Array(panel.machineChoices.enumerated()), id: \.element.machineID) { idx, choice in
                    let note = choice.note.rendered()
                    ListItem(
                        choice.name,
                        desc: note.isEmpty ? nil : note,
                        checked: choice.selected,
                        enabled: blocked == nil && vm.settled,
                        last: idx == panel.machineChoices.count - 1
                    ) {
                        Haptics.selection()
                        context.dispatch { $0.newSessionSelectMachine.machineID = choice.machineID }
                        if let back { back() } else { onClose() }
                    }
                }
            }
        }
    }

    @ViewBuilder private func modelPage(
        vm: PBCoreNewSessionViewModel,
        panel: PBCoreNewSessionModelPanelView,
        blocked: String?,
        back: (() -> Void)?
    ) -> some View {
        SheetHead(
            L10n.modelsTitle(),
            action: L10n.commonDone(),
            onAction: onClose,
            onBack: back
        )
        if let blocked { Banner(blocked).padding(.bottom, 10) }
        if !vm.settled { Hint(L10n.newSessionReadingPrevious()) }
        let errText = panel.modelsError.rendered()
        if !errText.isEmpty {
            Banner(
                errText,
                tone: .warn,
                action: L10n.commonRetry(),
                onAction: { context.dispatch { $0.newSessionRetryModels.projectKey = vm.projectKey } }
            )
        } else if panel.modelsLoading && panel.models.isEmpty {
            LoadingRow(L10n.modelsIosReadingMachine(name: vm.machineName), inset: 0)
        } else {
            ScrollView {
                VStack(alignment: .leading, spacing: 8) {
                    if !panel.effortChoices.isEmpty {
                        if panel.effortChoices.count == 1 {
                            Text(L10n.modelsFixedEffort(effort: panel.effortChoices[0]))
                                .font(.footnote)
                                .ink(DS.ink2)
                        } else {
                            FieldLabel(L10n.modelsEffort())
                            HStack(spacing: 8) {
                                ForEach(panel.effortChoices, id: \.self) { effort in
                                    Button {
                                        Haptics.selection()
                                        context.dispatch {
                                            $0.newSessionSelectModel = .with {
                                                $0.model = vm.config.model
                                                $0.effort = effort
                                                $0.standardContext = panel.standardContext
                                            }
                                        }
                                    } label: {
                                        Text(effort)
                                            .font(.subheadline)
                                            .padding(.horizontal, 12)
                                            .padding(.vertical, 6)
                                            .foregroundStyle(effort == panel.selectedEffort ? DS.blue : DS.ink)
                                            .background(effort == panel.selectedEffort ? DS.fill : DS.gbg, in: Capsule())
                                    }
                                    .buttonStyle(.plain)
                                }
                            }
                        }
                    }
                    if panel.supportsContextWindow {
                        let note = panel.contextNote.rendered()
                        if panel.contextFixed {
                            if !note.isEmpty { Text(note).font(.footnote).ink(DS.ink2) }
                        } else {
                            FieldLabel(L10n.modelsContextWindow())
                            HStack(spacing: 8) {
                                ForEach([false, true], id: \.self) { std in
                                    Button {
                                        Haptics.selection()
                                        context.dispatch {
                                            $0.newSessionSelectModel = .with {
                                                $0.model = vm.config.model
                                                $0.effort = vm.config.effort
                                                $0.standardContext = std
                                            }
                                        }
                                    } label: {
                                        Text(std ? L10n.modelsStandardWindow() : L10n.modelsExtendedWindow())
                                            .font(.subheadline)
                                            .padding(.horizontal, 12)
                                            .padding(.vertical, 6)
                                            .foregroundStyle(panel.standardContext == std ? DS.blue : DS.ink)
                                            .background(panel.standardContext == std ? DS.fill : DS.gbg, in: Capsule())
                                    }
                                    .buttonStyle(.plain)
                                }
                            }
                        }
                    }
                    modelRow(panel.defaultModelLabel.rendered(), checked: panel.defaultModelChecked) {
                        pickModel(slug: "", vm: vm, panel: panel)
                    }
                    let shown = allModels || panel.models.count <= 8 ? panel.models : Array(panel.models.prefix(8))
                    ForEach(shown, id: \.slug) { m in
                        modelRow(m.displayName.isEmpty ? m.slug : m.displayName, checked: m.checked) {
                            pickModel(slug: m.slug, vm: vm, panel: panel)
                        }
                    }
                    if !allModels && shown.count < panel.models.count {
                        LinkButton(L10n.modelsAll(), chevron: true) { allModels = true }
                    }
                }
                .disabled(!vm.settled || blocked != nil)
            }
        }
    }

    private func pickModel(slug: String, vm: PBCoreNewSessionViewModel, panel: PBCoreNewSessionModelPanelView) {
        Haptics.selection()
        context.dispatch {
            $0.newSessionSelectModel = .with {
                $0.model = slug
                $0.effort = ""
                $0.standardContext = panel.standardContext
            }
        }
    }

    private func modelRow(_ title: String, checked: Bool, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack(spacing: 8) {
                Text(title).lineLimit(1).ink(DS.ink)
                Spacer(minLength: 0)
                if checked { Image(systemName: "checkmark").foregroundStyle(DS.blue) }
            }
            .frame(height: 44)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(checked ? .isSelected : [])
    }
}
