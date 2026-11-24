// 新任务 (design-system §6 「新任务」): renders `PBCoreNewSessionViewModel` directly.
import RACore
import RAProto
import SwiftUI

struct NewTaskScreen: View {
    @State var key: String
    @State var machine: String?
    var cleanupPrompt: String? = nil
    let onClose: () -> Void
    let onStarted: (String) -> Void
    @Environment(Navigator.self) private var nav
    @Environment(AccountContext.self) private var context
    @State private var settings: ProjectSettingsRequest?
    @State private var picking = false
    @State private var browsing = false
    @State private var modelOpen = false

    var body: some View {
        NewSessionBox(
            key: key,
            focus: true,
            onMachine: { settings = ProjectSettingsRequest(page: .machine) },
            onMachineSettings: { id in
                onClose()
                nav.open(.machine(id: id))
            },
            onEnableAgent: { id, agent in
                onClose()
                nav.open(.machine(id: id, initialCliAgent: MachineScreenContent<EmptyView>.agentSlug(agent)))
            },
            onMachineStatus: { id in
                onClose()
                nav.open(.machineStatus(id: id))
            },
            sheet: NewTaskChrome(
                modelOpen: $modelOpen,
                onMachine: { settings = ProjectSettingsRequest(page: .machine) },
                onProject: { picking = true },
                onClose: onClose
            )
        )
        .navigationBarTitleDisplayMode(.inline)
        .task(id: "\(key):\(machine ?? ""):\(cleanupPrompt ?? "")") {
            await context.coreHost.showNewSession(projectKey: key)
            if let machine, !machine.isEmpty {
                await context.coreHost.dispatch(PBCoreEvent.with {
                    $0.newSessionSelectMachine.machineID = machine
                })
            }
            if let cleanupPrompt, !cleanupPrompt.isEmpty {
                let scope = PBDraftScope.newSession(key)
                await context.coreHost.openDraft(scope: scope, machineID: machine ?? context.core.newSession.config.machineID)
                let existing = context.core.newSession.draft.text.isEmpty ? context.core.newSession.prompt : context.core.newSession.draft.text
                if !existing.contains(cleanupPrompt) {
                    let trimmed = existing.trimmingCharacters(in: .whitespacesAndNewlines)
                    let combined = trimmed.isEmpty ? cleanupPrompt : trimmed + "\n\n" + cleanupPrompt
                    await context.coreHost.setDraftText(scope: scope, text: combined)
                }
            }
        }
        .kitSheet(item: $settings) { request in
            CoreProjectSettingsSheet(open: request.page) { settings = nil }
        }
        .kitSheet(isPresented: $picking, size: .large, onDismiss: { browsing = false }) {
            Group {
                if browsing {
                    SheetHead(L10n.projectsNewChooseDirectory(), onBack: { browsing = false })
                    CoreDirPicker(pickerKey: "new-task", forNewTask: true) { pickedMachine, path in
                        choose(path, on: pickedMachine)
                    }
                } else {
                    SheetHead(L10n.newSessionIosWhere(), action: L10n.commonCancel(), onAction: { picking = false })
                    CoreProjectPickList(
                        listing: context.core.manageProjects.listing,
                        onSearch: { q in context.dispatch { $0.projectSetSearch.query = q } },
                        onPick: { choose($0, on: nil) },
                        onNew: { browsing = true }
                    )
                }
            }
            .task { await context.coreHost.setManageActive(true) }
            .onDisappear { Task { await context.coreHost.setManageActive(false) } }
        }
        #if DEBUG
        .task {
            if let page = HomeLaunch.value("-newTaskSheet").flatMap(ProjectSettingsPage.init(rawValue:)) {
                try? await Task.sleep(for: .milliseconds(900))
                if page == .model { modelOpen = true }
                else { settings = ProjectSettingsRequest(page: page) }
            }
        }
        #endif
    }

    private func choose(_ next: String, on nextMachine: String?) {
        picking = false
        browsing = false
        guard next != key || nextMachine != self.machine else { return }
        key = next
        machine = nextMachine
        Task {
            await context.coreHost.dispatch(PBCoreEvent.with {
                $0.newSessionSelectProject.projectKey = next
            })
            if let nextMachine, !nextMachine.isEmpty {
                await context.coreHost.dispatch(PBCoreEvent.with {
                    $0.newSessionSelectMachine.machineID = nextMachine
                })
            }
        }
    }
}

/// The title of 新任务: the project, and under it the computer where selectable.
struct NewTaskWhere: View {
    let key: String
    let name: String
    let machine: String
    let onProject: () -> Void
    let onMachine: () -> Void

    var body: some View {
        VStack(spacing: 0) {
            Button(action: onProject) {
                HStack(spacing: 6) {
                    Avatar(key, size: 22)
                    Text(name).font(.headline).ink(DS.ink).lineLimit(1)
                    Arrow(DS.ink3, style: .caption, direction: .down)
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(PressStyle())
            .frame(minHeight: machine.isEmpty ? 32 : 24)
            .accessibilityIdentifier("new-task-project")
            .accessibilityLabel(L10n.newSessionProjectLabel(name: name))
            .accessibilityHint(L10n.newSessionIosChangeProject())
            if !machine.isEmpty {
                Button(action: onMachine) {
                    HStack(spacing: 3) {
                        Text(machine).font(.caption).ink(DS.ink2).lineLimit(1)
                        Arrow(DS.ink3, style: .caption2, direction: .down)
                    }
                    .frame(minWidth: 44, minHeight: 24)
                    .contentShape(Rectangle())
                }
                .buttonStyle(PressStyle())
                .accessibilityIdentifier("new-task-machine")
                .accessibilityLabel(L10n.newSessionIosComputerLabel(name: machine))
                .accessibilityHint(L10n.newSessionIosChangeComputer())
            }
        }
    }
}

/// The CLI picker in 新任务's tools: a small capsule whose menu chooses the CLI.
struct NewTaskCli: View {
    let agents: [PBAgent]
    let agent: PBAgent
    let onSelect: (PBAgent) -> Void

    var body: some View {
        let checkSymbol = "checkmark"
        Menu {
            ForEach(agents, id: \.self) { a in
                Button {
                    Haptics.selection()
                    onSelect(a)
                } label: {
                    if a == agent { Label(agentName(a), systemImage: checkSymbol) }
                    else { Text(agentName(a)) }
                }
            }
        } label: {
            HStack(spacing: 5) {
                AgentMark(agent, style: .footnote, named: true)
                Text(agentName(agent)).font(.footnote).ink(DS.ink).lineLimit(1)
                Arrow(DS.ink3, style: .caption2, direction: .down)
            }
            .padding(.horizontal, 10)
            .frame(minHeight: 34)
            .background(DS.fill, in: Capsule())
            .contentShape(Rectangle())
        }
        .accessibilityLabel(L10n.newSessionIosCliNamed(name: agentName(agent)))
        .accessibilityIdentifier("new-task-cli")
    }
}

/// The model chip in 新任务's tools: `Model`, `CLI · Model`, or `Model · Effort` in a `fill` capsule.
struct NewTaskModel: View {
    let summary: NewTaskModelSummary
    let active: Bool
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 4) {
                Text(summary.prefix + summary.model)
                    .font(.footnote)
                    .ink(active ? DS.blue : DS.ink)
                    .lineLimit(1)
                if !summary.effort.isEmpty {
                    Text(summary.effort)
                        .font(.caption2)
                        .ink(active ? DS.blue : DS.ink2)
                        .padding(.horizontal, 6)
                        .padding(.vertical, 2)
                        .background(DS.popover, in: Capsule())
                }
                Arrow(active ? DS.blue : DS.ink3, style: .caption2, direction: active ? .up : .down)
            }
            .padding(.horizontal, 10)
            .frame(minHeight: 34)
            .background(DS.fill, in: Capsule())
            .overlay { if active { Capsule().strokeBorder(DS.blue.opacity(0.35), lineWidth: 1) } }
            .contentShape(Rectangle())
        }
        .buttonStyle(PressStyle())
        .accessibilityLabel(summary.accessibilityLabel)
        .accessibilityHint(L10n.modelsIosChooseHint())
        .accessibilityAddTraits(active ? .isSelected : [])
        .accessibilityIdentifier("new-task-model")
    }
}

/// Fits the CLI and model chips beside ＋ and ↑; the model truncates if tight.
struct NewTaskToolsLayout: Layout {
    static func widths(available: CGFloat, cli: CGFloat, compactCLI: CGFloat, model: CGFloat) -> (cli: CGFloat, model: CGFloat) {
        let gap: CGFloat = cli > 0 ? 2 : 0
        let used = cli + gap + model <= available ? cli : compactCLI
        return (used, min(model, max(0, available - used - gap)))
    }

    private func widths(_ proposal: ProposedViewSize, _ views: Subviews) -> (cli: CGFloat, model: CGFloat) {
        let cli = views.count == 2 ? views[0].sizeThatFits(.unspecified).width : 0
        let compact = views.count == 2 ? views[0].sizeThatFits(.init(width: 0, height: nil)).width : 0
        let model = views[views.count - 1].sizeThatFits(.unspecified).width
        return Self.widths(available: proposal.width ?? cli + model + 2, cli: cli, compactCLI: compact, model: model)
    }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let sizes = widths(proposal, subviews)
        return CGSize(width: sizes.cli + sizes.model + (subviews.count == 2 ? 2 : 0), height: DS.minHit)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        let sizes = widths(.init(width: bounds.width, height: bounds.height), subviews)
        if subviews.count == 2 {
            subviews[0].place(at: bounds.origin, anchor: .topLeading, proposal: .init(width: sizes.cli, height: bounds.height))
        }
        subviews[subviews.count - 1].place(
            at: CGPoint(x: bounds.minX + sizes.cli + (subviews.count == 2 ? 2 : 0), y: bounds.minY),
            anchor: .topLeading,
            proposal: .init(width: sizes.model, height: bounds.height)
        )
    }
}

#if DEBUG
#Preview("New Task") {
    let vm = ScreenGoldens.newSession("new_session_lifecycle", index: 0)
    NavigationStack {
        NewSessionContent(
            vm: vm,
            text: .constant(""),
            sheet: NewTaskChrome(modelOpen: .constant(false), onMachine: {}, onProject: {}, onClose: {})
        )
    }
}
#endif
