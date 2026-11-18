// The new-session box at the bottom of a project's page (and inside NewTaskScreen):
// renders `PBCoreNewSessionViewModel` directly and dispatches `Event`s back to `CoreHost`.
import RACore
import RAProto
import SwiftUI

/// Chrome passed by `NewTaskScreen` so `NewSessionBox` renders as a full-screen task sheet.
struct NewTaskChrome {
    @Binding var modelOpen: Bool
    var onMachine: () -> Void
    var onProject: () -> Void
    var onClose: () -> Void
}

struct NewSessionBox: View {
    let key: String
    let focus: Bool
    var onMachine: () -> Void = {}
    var onModel: () -> Void = {}
    var onMachineSettings: (String) -> Void = { _ in }
    var onMachineStatus: (String) -> Void = { _ in }
    var sheet: NewTaskChrome?

    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @State private var text = ""

    var body: some View {
        let vm = context.core.newSession
        let state = model.state
        let machineId = vm.config.machineID.isEmpty ? nil : vm.config.machineID
        let images = ComposerAttachments(
            state: state,
            key: ImageCenter.projectKey(vm.projectKey.ifEmpty(key)),
            machineId: machineId,
            remote: context.remote
        )
        NewSessionContent(
            vm: vm,
            text: $text,
            focus: focus,
            images: images,
            blockedReason: prefsBlocked(state),
            draftError: context.sessionStore.draftErrors["new:" + vm.projectKey.ifEmpty(key)],
            onPromptChanged: { prompt in
                let count = Int32(images.draft.images.count)
                context.dispatch {
                    $0.newSessionSetPrompt = .with {
                        $0.prompt = prompt
                        $0.attachmentCount = count
                    }
                }
            },
            onSelectAgent: { agent in
                context.dispatch { $0.newSessionSelectAgent.agent = agent }
            },
            onSelectMachineChoice: { machine in
                context.dispatch { $0.newSessionSelectMachine.machineID = machine }
            },
            onRetryModels: {
                context.dispatch { $0.newSessionRetryModels.projectKey = vm.projectKey }
            },
            onRetrySaveConfig: {
                context.dispatch { $0.newSessionRetrySaveConfig.projectKey = vm.projectKey }
            },
            onAbandonStart: {
                context.dispatch { $0.newSessionAbandonStart.projectKey = vm.projectKey }
            },
            onSelectModel: { slug, effort, standardContext in
                context.dispatch {
                    $0.newSessionSelectModel = .with {
                        $0.model = slug
                        $0.effort = effort
                        if let standardContext {
                            $0.standardContext = standardContext
                        }
                    }
                }
            },
            onSubmit: {
                images.refresh()
                guard vm.canStart, images.ready else { return }
                let attached = images.draft.images
                let submission = NewSessionAttachmentsSubmission(
                    draftKey: ImageCenter.projectKey(vm.projectKey.ifEmpty(key)),
                    machineId: machineId,
                    taken: attached,
                    imageIds: attached.filter { !$0.isFile }.compactMap(\.imageId),
                    fileIds: attached.filter(\.isFile).compactMap(\.imageId),
                    center: images.center
                )
                Haptics.confirm()
                context.submitNewSession(
                    projectKey: vm.projectKey.ifEmpty(key),
                    prompt: text,
                    attachmentCount: Int32(attached.count),
                    attachmentsMoved: false,
                    autoModeSupported: vm.modelPanel.canSelectAutoMode,
                    attachments: submission
                )
            },
            onMachine: onMachine,
            onModel: onModel,
            onMachineSettings: onMachineSettings,
            onMachineStatus: onMachineStatus,
            sheet: sheet
        )
        .onChange(of: vm.prompt, initial: true) { oldServerPrompt, serverPrompt in
            if text != serverPrompt && (serverPrompt.isEmpty || text.isEmpty || (!oldServerPrompt.isEmpty && serverPrompt != oldServerPrompt && text == oldServerPrompt)) {
                text = serverPrompt
            }
        }
        .onChange(of: images.draft.images.count) { _, count in
            context.dispatch {
                $0.newSessionSetPrompt = .with {
                    $0.prompt = text
                    $0.attachmentCount = Int32(count)
                }
            }
        }
    }
}

/// Pure presentation body for `NewSessionBox` and `NewTaskScreen`, driven solely by `PBCoreNewSessionViewModel`.
struct NewSessionContent: View {
    let vm: PBCoreNewSessionViewModel
    @Binding var text: String
    var focus = false
    var images: ComposerAttachments? = nil
    var blockedReason: String? = nil
    var draftError: String? = nil
    var onPromptChanged: (String) -> Void = { _ in }
    var onSelectAgent: (PBAgent) -> Void = { _ in }
    var onSelectMachineChoice: (String) -> Void = { _ in }
    var onRetryModels: () -> Void = {}
    var onRetrySaveConfig: () -> Void = {}
    var onAbandonStart: () -> Void = {}
    var onSelectModel: (_ slug: String, _ effort: String, _ standardContext: Bool?) -> Void = { _, _, _ in }
    var onSubmit: () -> Void = {}
    var onMachine: () -> Void = {}
    var onModel: () -> Void = {}
    var onMachineSettings: (String) -> Void = { _ in }
    var onMachineStatus: (String) -> Void = { _ in }
    var sheet: NewTaskChrome?

    @FocusState private var focused: Bool
    @State private var cliViewport: CGFloat?
    @State private var keyboardVisible = false
    @State private var modelPanelHeight: CGFloat = 0

    var body: some View {
        let canSend = vm.canStart && (images?.ready ?? true)
        Group {
            if let sheet {
                taskPage(sheet, canSend: canSend)
            } else {
                box(canSend: canSend)
            }
        }
        .safeAreaInset(edge: .bottom, spacing: 0) {
            if let draftError {
                Notice(draftError, tone: .error).padding(.horizontal, DS.Space.m).padding(.bottom, 6)
                    .accessibilityIdentifier("new-session-draft-error")
            }
        }
        .onChange(of: text) { _, next in
            onPromptChanged(next)
        }
        .debugSignal("project.start") { onSubmit() }
        .debugSignal("project.agent") {
            let agents = vm.availableAgents.map(\.agent)
            if let other = agents.first(where: { $0 != vm.config.agent }) {
                onSelectAgent(other)
            }
        }
        .task {
            #if DEBUG
            if let prefill = HomeLaunch.value("-projectDraft"), text.isEmpty {
                text = prefill
                onPromptChanged(prefill)
            }
            #endif
            guard focus else { return }
            try? await Task.sleep(for: .milliseconds(450))
            focused = true
        }
    }

    private func box(canSend: Bool) -> some View {
        VStack(spacing: 0) {
            if !vm.notices.isEmpty {
                VStack(spacing: 6) {
                    ForEach(Array(vm.notices.enumerated()), id: \.offset) { _, n in
                        noticeView(n, canSend: canSend)
                    }
                }
                .padding(.horizontal, 12).padding(.top, 6).padding(.bottom, 2)
            }
            let agents = vm.availableAgents
            if agents.count > 1 {
                SegPick(
                    agents.map(\.displayName),
                    marks: agents.map(\.agent),
                    selected: Binding(
                        get: { agents.firstIndex(where: { $0.selected }) ?? 0 },
                        set: { idx in
                            if agents.indices.contains(idx) { onSelectAgent(agents[idx].agent) }
                        }
                    )
                )
                .accessibilityLabel(L10n.newSessionIosCliLabel())
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, DS.Space.l)
            }
            MessageCapsule(
                text: $text,
                placeholder: L10n.newSessionIosDescribe(),
                label: L10n.newSessionIosDescribe(),
                identifier: "new-session-text",
                focus: $focused
            ) {
                if let images { images.strip(focus: $focused) }
            } plus: {
                if let images {
                    Button { images.pick() } label: { PlusCircle(dimmed: images.blocked != nil) }
                        .buttonStyle(.plain)
                        .accessibilityLabel(L10n.newSessionIosAttach())
                }
            } trailing: {
                Button {
                    focused = false
                    onSubmit()
                } label: { InkCircle(symbol: "arrow.up", enabled: canSend) }
                    .buttonStyle(.plain)
                    .disabled(!canSend)
                    .accessibilityLabel(L10n.newSessionIosStart())
                    .accessibilityIdentifier("new-session-start")
            }
        }
        .readableColumn()
    }

    private func taskPage(_ sheet: NewTaskChrome, canSend: Bool) -> some View {
        let modelOpen = sheet.modelOpen
        let agents = vm.availableAgents
        let summary = NewTaskModelSummary(vm: vm)
        return GeometryReader { viewport in
            VStack(spacing: 0) {
                ScrollView {
                    VStack(spacing: 6) {
                        ForEach(Array(vm.notices.enumerated()), id: \.offset) { _, n in
                            noticeView(n, canSend: canSend)
                        }
                    }
                    .padding(.horizontal, DS.Space.l).padding(.top, 8)
                }
                .scrollDismissesKeyboard(.interactively)
                MessageCard(
                    text: $text,
                    placeholder: L10n.newSessionPrompt(),
                    label: L10n.newSessionIosPromptLabel(),
                    identifier: "new-session-text",
                    focus: $focused
                ) {
                    if let images { images.strip(focus: $focused) }
                } accessories: {
                    if let images { AttachMenu(images: images) }
                    NewTaskToolsLayout {
                        if agents.count > 1 {
                            NewTaskCli(agents: agents.map(\.agent), agent: vm.config.agent) {
                                onSelectAgent($0)
                            }
                            .anchorPreference(key: NewTaskPanelAnchors.self, value: .bounds) { ["cli": $0] }
                        }
                        NewTaskModel(summary: summary, active: modelOpen) {
                            sheet.modelOpen.toggle()
                        }
                        .anchorPreference(key: NewTaskPanelAnchors.self, value: .bounds) { ["model": $0] }
                    }
                    .layoutPriority(1)
                    Spacer(minLength: 0)
                    Button {
                        focused = false
                        onSubmit()
                    } label: { InkCircle(symbol: "arrow.up", enabled: canSend) }
                        .buttonStyle(.plain)
                        .disabled(!canSend)
                        .accessibilityLabel(L10n.newSessionIosStart())
                        .accessibilityIdentifier("new-session-start")
                }
                .readableColumn()
                .transformAnchorPreference(key: NewTaskPanelAnchors.self, value: .bounds) { $0["composer"] = $1 }
            }
            .frame(maxWidth: .infinity)
            .frame(height: min(cliViewport ?? viewport.size.height, viewport.size.height))
            .frame(maxHeight: .infinity, alignment: .top)
            .background(DS.paper)
            .overlayPreferenceValue(NewTaskPanelAnchors.self) { anchors in
                GeometryReader { geometry in
                    if let composer = anchors["composer"], let chip = anchors["model"] {
                        let box = geometry[composer]
                        let height = min(600, max(0, box.minY - 8))
                        let width = min(420, max(0, box.width - 24))
                        let rect = CGRect(x: box.minX + 12, y: box.minY - 8 - height, width: width, height: height)
                        let panelRect = CGRect(x: rect.minX, y: rect.maxY - modelPanelHeight, width: width, height: modelPanelHeight)
                        NewTaskOutsideTouches(
                            panel: modelOpen ? panelRect : nil,
                            chip: geometry[chip],
                            cli: anchors["cli"].map { geometry[$0] },
                            onCLI: { if keyboardVisible { cliViewport = viewport.size.height } },
                            onClose: { sheet.modelOpen = false }
                        )
                        .allowsHitTesting(false)
                        if modelOpen {
                            NewTaskModelPanel(
                                vm: vm,
                                settled: vm.settled,
                                blocked: blockedReason,
                                onRetryModels: onRetryModels,
                                onSelectModel: onSelectModel,
                                onClose: { sheet.modelOpen = false }
                            )
                            .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { modelPanelHeight = $0 }
                            .frame(width: width, height: height, alignment: .bottom)
                            .position(x: rect.midX, y: rect.midY)
                            .transition(.opacity)
                        }
                    }
                }
                .animation(Motion.enter(Motion.fast), value: modelOpen)
            }
            .onChange(of: viewport.size.height) { _, height in
                if let held = cliViewport, height <= held { cliViewport = nil }
            }
            .onChange(of: viewport.size.width) { _, _ in cliViewport = nil }
        }
        .onReceive(NotificationCenter.default.publisher(for: UIResponder.keyboardWillShowNotification)) { _ in keyboardVisible = true }
        .onReceive(NotificationCenter.default.publisher(for: UIResponder.keyboardWillHideNotification)) { _ in
            keyboardVisible = false
            cliViewport = nil
        }
        .onChange(of: focused) { _, f in if !f { cliViewport = nil } }
        .onDisappear { sheet.modelOpen = false }
        .onChange(of: text) { _, _ in sheet.modelOpen = false }
        .toolbar {
            ToolbarItem(placement: .topBarLeading) {
                Button {
                    sheet.modelOpen = false
                    sheet.onClose()
                } label: { Image(systemName: "xmark") }
                    .tint(DS.ink)
                    .accessibilityLabel(L10n.commonClose())
            }
            ToolbarItem(placement: .principal) {
                NewTaskWhere(
                    key: vm.projectKey,
                    name: vm.projectName.rendered(),
                    machine: vm.machineSelectable ? vm.machineName : "",
                    onProject: {
                        sheet.modelOpen = false
                        sheet.onProject()
                    },
                    onMachine: {
                        sheet.modelOpen = false
                        sheet.onMachine()
                    }
                )
            }
        }
    }

    @ViewBuilder private func noticeView(_ n: PBCoreNewSessionNoticeView, canSend: Bool) -> some View {
        let message = n.text.rendered()
        if n.hasDraftChoices_p {
            VStack(alignment: .leading, spacing: 0) {
                Text(n.draftTitle.rendered()).font(.subheadline).ink(DS.orangeInk)
                Text(n.draftSubtitle.rendered()).font(.footnote).ink(DS.ink2)
                ForEach(n.draftChoices, id: \.machineID) { choice in
                    Button {
                        Haptics.selection()
                        onSelectMachineChoice(choice.machineID)
                    } label: {
                        HStack(spacing: 8) {
                            VStack(alignment: .leading, spacing: 0) {
                                Text(choice.title.rendered()).font(.body.weight(.semibold)).ink(DS.ink)
                                Text(choice.note.rendered()).font(.footnote).ink(DS.ink2)
                            }
                            .frame(maxWidth: .infinity, alignment: .leading)
                            Arrow(DS.ink2, style: .body)
                        }
                        .padding(.vertical, 4)
                        .frame(minHeight: DS.minHit)
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(PressStyle())
                }
                let noneText = n.draftNoneText.rendered()
                if !noneText.isEmpty { Text(noneText).font(.footnote).ink(DS.ink2) }
            }
            .padding(.horizontal, 12).padding(.vertical, 9)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(DS.gbg, in: RoundedRectangle(cornerRadius: DS.Radius.group, style: .continuous))
        } else {
            switch n.kind {
            case .readingPrevious:
                EarlierTail(message, retry: n.action == .retryReadSettings)
                    .padding(.horizontal, 4)
                    .accessibilityIdentifier("settings-unread")
            case .starting:
                Banner(message, icon: Mark.progress)
            case .diskFull:
                LaunchFailedCard(
                    onUsage: { onMachineStatus(n.targetMachineID) },
                    onRetry: canSend ? onSubmit : nil
                )
            case .unknownStart:
                Banner(message, tone: .warn, action: L10n.newSessionIosAbandon(), onAction: onAbandonStart)
            case .failed:
                Banner(message, tone: .error, action: canSend ? L10n.commonRetry() : nil, onAction: onSubmit)
            case .missingAgent:
                Banner(message, action: L10n.newSessionChangeMachine(), onAction: onMachine)
            case .autoUnavailable:
                if n.action == .changeModel {
                    Banner(message, tone: .warn, action: L10n.newSessionChangeModel()) {
                        if let sheet { sheet.modelOpen = true } else { onModel() }
                    }
                } else {
                    Banner(message, tone: .warn, action: L10n.newSessionChangeDefaults()) {
                        onMachineSettings(n.targetMachineID)
                    }
                }
            case .configSaveError:
                Banner(message, tone: .error, action: L10n.commonRetry(), onAction: onRetrySaveConfig)
            default:
                Banner(message, tone: n.severity == .error ? .error : n.severity == .warn ? .warn : .plain)
            }
        }
    }
}

/// The new-session settings as a button that opens the project's settings.
struct ProjectSummaryChip: View {
    let summary: String
    let action: () -> Void
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        Button(action: action) {
            HStack(spacing: 6) {
                Text(summary)
                    .font(.subheadline).ink(DS.ink).lineLimit(typeSize.isAccessibilitySize ? 4 : 2).multilineTextAlignment(.leading)
                    .fixedSize(horizontal: false, vertical: true)
                Arrow(DS.ink3)
            }
            .padding(.horizontal, 12).padding(.vertical, 6)
            .frame(minHeight: 36)
            .background(DS.fill, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
            .padding(.vertical, 6).contentShape(Rectangle()).padding(.vertical, -6)
        }
        .buttonStyle(PressStyle())
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(L10n.newSessionIosSettingsLabel(summary: summary))
        .accessibilityHint(L10n.projectsIosSettings())
        .accessibilityAddTraits(.isButton)
        .accessibilityIdentifier("project-settings-summary")
    }
}

/// The start that failed because the computer's disk is full.
private struct LaunchFailedCard: View {
    let onUsage: () -> Void
    let onRetry: (() -> Void)?

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            VStack(alignment: .leading, spacing: 3) {
                Text(L10n.machineStatusDiskFull()).font(.headline).foregroundStyle(DS.ink)
                Text(L10n.machineStatusLaunchFailedBody()).font(.subheadline).foregroundStyle(DS.ink2)
            }
            .padding(.horizontal, 16).padding(.top, 16).padding(.bottom, 14)
            .frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityElement(children: .combine)
            Rectangle().fill(DS.sep).frame(height: 0.5)
            HStack(spacing: 0) {
                button(L10n.machineStatusViewUsage(), weight: .regular, action: onUsage)
                if let onRetry {
                    Rectangle().fill(DS.sep).frame(width: 0.5)
                    button(L10n.commonRetry(), weight: .semibold, action: onRetry)
                }
            }
            .fixedSize(horizontal: false, vertical: true)
        }
        .background(DS.gbg, in: RoundedRectangle(cornerRadius: DS.Radius.group, style: .continuous))
        .accessibilityIdentifier("launch-failed-disk-full")
    }

    private func button(_ title: String, weight: Font.Weight, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Text(title).font(.body.weight(weight)).foregroundStyle(DS.blue).multilineTextAlignment(.center)
                .frame(maxWidth: .infinity, minHeight: 46).padding(.horizontal, 8).contentShape(Rectangle())
        }
        .buttonStyle(RowPressStyle())
    }
}
