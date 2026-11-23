// A session: its navigation bar, the 聊天 / 终端 tabs, and under them the
// chat or the terminal, driven directly by Core's `PBSessionViewModel` and
// `CoreHost`.
import RACore
import RAMarkdownUI
import RAProto
import SwiftUI

private struct CoreSessionModifier: ViewModifier {
    let sessionID: String
    let refreshModels: Bool
    let window: PBSessionWindowRequest
    @Environment(AccountContext.self) private var context
    @State private var watchToken: UUID?

    func body(content: Content) -> some View {
        content
            .task(id: sessionID) {
                guard !sessionID.isEmpty else { return }
                await context.coreHost.openSession(sessionID, refreshModels: refreshModels)
                let token = await context.coreHost.acquireSessionWatch(sessionID, initialWindow: window)
                watchToken = token
                defer {
                    Task {
                        if watchToken == token { watchToken = nil }
                        await context.coreHost.releaseSessionWatch(token)
                    }
                }
                while !Task.isCancelled {
                    try? await Task.sleep(for: .seconds(3600))
                }
            }
            .onChange(of: refreshModels) { _, want in
                guard want, !sessionID.isEmpty else { return }
                Task { await context.coreHost.openSession(sessionID, refreshModels: true) }
            }
            .onChange(of: window) { _, next in
                guard let token = watchToken else { return }
                Task { await context.coreHost.updateSessionWindow(token: token, window: next) }
            }
    }
}

extension View {
    func followCoreSession(
        _ sessionID: String,
        refreshModels: Bool = false,
        window: PBSessionWindowRequest = .with { $0.limit = 80 }
    ) -> some View {
        modifier(CoreSessionModifier(sessionID: sessionID, refreshModels: refreshModels, window: window))
    }
}

private enum SessionSheet: String, Identifiable {
    case mode, model
    var id: String { rawValue }
}

private let sessionTabKey = "session-view.tab"

/// A session: its chat and its terminal. `terminal` opens it on the terminal.
struct SessionScreen: View {
    let id: String
    let terminal: Bool

    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @Environment(\.page) private var page
    @Environment(\.colorScheme) private var colorScheme
    @Environment(\.horizontalSizeClass) private var width
    @Environment(\.scenePhase) private var scenePhase
    @State private var tab: Int
    @State private var menu = false
    @State private var sheet: SessionSheet?
    @State private var scheduleSending = false
    @State private var rename = false
    @State private var projectSettings: ProjectSettingsRequest?
    @State private var confirmStop = false
    @State private var directPopoverOpen = false
    @State private var windowLimit: Int32 = 80
    @State private var expandedGroups: [String] = []

    init(id: String, terminal: Bool) {
        self.id = id
        self.terminal = terminal
        let saved = UserDefaults.standard.integer(forKey: sessionTabKey)
        _tab = State(initialValue: terminal ? 1 : (1...2).contains(saved) ? saved : 0)
    }

    private func pick(_ newTab: Int) {
        tab = newTab
        UserDefaults.standard.set(newTab, forKey: sessionTabKey)
    }

    private func afterMenu(_ action: @escaping @MainActor () -> Void) {
        menu = false
        Task { @MainActor in
            try? await Task.sleep(for: .milliseconds(350))
            action()
        }
    }

    private var windowRequest: PBSessionWindowRequest {
        let jump = context.sessionStore.scheduledJump(id)
        return PBSessionWindowRequest.with {
            $0.limit = windowLimit
            if let jump {
                if !jump.runID.isEmpty { $0.revealRunID = jump.runID }
                if jump.seq > 0 { $0.revealSeq = jump.seq }
            }
            $0.expandedGroupIds = expandedGroups
        }
    }

    var body: some View {
        let vm = context.core.sessions[id]
        let header = vm?.header ?? PBSessionHeaderView()
        let state = model.state
        Group {
            if let vm, header.found {
                sessionBody(vm: vm, header: header)
            } else {
                SessionAbsent(header: header, online: state.conn == .online) {
                    Task {
                        await context.coreHost.fetchSession(id)
                        await context.coreHost.openSession(id, refreshModels: false)
                    }
                }
                .navBar(L10n.homeSessionsTab())
            }
        }
        .followCoreSession(id, refreshModels: menu || sheet == .model, window: windowRequest)
        .sessionSamples(id)
        .onChange(of: context.core.pendingComposerRestores, initial: true) { _, restores in
            let matching = restores.filter { $0.effect.sessionID == id }
            guard !matching.isEmpty else { return }
            let caps = AttachmentCapabilities(
                header.capabilities,
                hasMachine: !header.machineID.isEmpty,
                online: header.computerOnline && !header.computerUnlinked && header.connState == .connected
            )
            for item in matching {
                SessionImages.applyRestore(
                    item.effect, session: id, caps: caps, remote: context.remote, store: context.sessionStore
                )
            }
            context.core.consumeComposerRestores(Set(matching.map(\.id)))
        }
        .onChange(of: context.core.pendingResendAttachments, initial: true) { _, resends in
            let matching = resends.filter { $0.effect.sessionID == id }
            guard !matching.isEmpty else { return }
            for item in matching {
                SessionImages.resendAttachments(item.effect, context: context) { message in
                    ImageCenter.of(context.remote).say(ImageCenter.sessionKey(id), message)
                }
            }
            context.core.consumeResendAttachments(Set(matching.map(\.id)))
        }
    }

    @ViewBuilder private func sessionBody(vm: PBSessionViewModel, header: PBSessionHeaderView) -> some View {
        let state = model.state
        let actions = vm.actions
        let hasTerminal = header.capabilities.terminalAvailable
        let wide = width == .regular
        let both = wide && tab == 2 && hasTerminal
        let term = tab == 1 && hasTerminal
        let views = wide ? [L10n.sessionChat(), L10n.sessionTerminal(), L10n.sessionSplit()] : [L10n.sessionChat(), L10n.sessionTerminal()]
        let view = Binding(get: { both ? 2 : term ? 1 : 0 }, set: { pick($0) })
        let titleText = header.title.rendered()
        let statusText = header.statusText.rendered()
        let modeText = header.hasApprovalModeLabel ? header.approvalModeLabel.rendered() : ""
        let modeDanger = header.approvalMode == .fullAccess
        let high = header.highContextWarning
        let live = !header.exited && header.computerOnline && !header.computerUnlinked
        let popup = header.status == .popup
        let machineID = header.machineID
        let actionError: String = {
            for cmd in [actions.interrupt, actions.end] {
                if cmd.status != .pending && !cmd.busy {
                    if cmd.hasErrorText {
                        let s = cmd.errorText.rendered()
                        if !s.isEmpty { return s }
                    }
                    if !cmd.errorCode.isEmpty { return cmd.errorCode }
                }
            }
            return ""
        }()

        Group {
            if both {
                HStack(spacing: 0) {
                    chatPane(vm: vm)
                    TerminalPane(sessionId: id, running: true)
                        .background(TermColors.bg)
                        .clipShape(RoundedRectangle(cornerRadius: DS.Radius.command, style: .continuous))
                        .padding(12)
                }
            } else if term {
                TerminalPane(sessionId: id, running: true)
            } else {
                chatPane(vm: vm)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        .background((term ? TermColors.bg : DS.paper).ignoresSafeArea())
        .markdownTheme(DS.markdown)
        .floatingBar(.top) {
            VStack(spacing: 8) {
                if !wide {
                    GlassSegmented(items: views, selected: view, enabled: { $0 == 0 || hasTerminal }, dot: popup ? 1 : nil)
                        .accessibilityIdentifier("session-view")
                }
                ConnectionStrip()
                if header.protocolMismatch {
                    GlassStrip(text: header.protocolMismatchText.rendered(), symbol: "exclamationmark.triangle", tone: .attention)
                }
                if !actionError.isEmpty { GlassStrip(text: actionError, symbol: "xmark", tone: .error) }
                if let notice = SharedFileCenter.of(context.remote).notice {
                    GlassStrip(text: notice.text, symbol: notice.symbol, tone: notice.failed ? .error : .plain)
                        .transition(Motion.appear(rise: -Motion.bubbleRise))
                }
                SessionScheduleBar(sessionID: id)
            }
            .padding(.horizontal, DS.Space.m)
            .padding(.top, 4).padding(.bottom, 8)
            .frame(maxWidth: DS.column)
            .environment(\.colorScheme, term ? .dark : colorScheme)
        }
        .tint(DS.blue)
        .task(id: header.scheduledTaskID) {
            guard header.taskOwned, !header.scheduledTaskID.isEmpty,
                  nav.path.last == page, !nav.path.dropLast().contains(.schedule(machine: machineID, id: header.scheduledTaskID)) else { return }
            nav.path.insert(.schedule(machine: machineID, id: header.scheduledTaskID), at: max(0, nav.path.count - 1))
        }
        .task(id: id) {
            SessionImages.restore(session: id, remote: context.remote)
        }
        .onDisappear { SharedFileCenter.of(context.remote).leftPage(id) }
        .onChange(of: scenePhase) { _, phase in if phase != .active { SharedFileCenter.of(context.remote).leftPage() } }
        .animation(Motion.enter(), value: SharedFileCenter.of(context.remote).notice)
        .navigationBarTitleDisplayMode(.inline)
        .navigationTitle(titleText)
        .toolbarRole(.editor)
        .onChange(of: header.exited) { _, exited in
            if exited { directPopoverOpen = false }
        }
        .onChange(of: header.computerOnline) { _, online in
            if !online { directPopoverOpen = false }
        }
        .onChange(of: header.computerUnlinked) { _, unlinked in
            if unlinked { directPopoverOpen = false }
        }
        .toolbar {
            ToolbarItem(placement: .principal) {
                let computer = header.hasComputerName ? header.computerName.rendered() : machineLabel(state, id: machineID)
                let demoChip: PBSessionDirectChipView? = {
                    #if DEBUG
                    if model.isDemo && SessionDebug.shows("direct") {
                        return PBSessionDirectChipView.with {
                            $0.visible = true
                            $0.active = true
                            $0.readDirect = true
                            $0.fileDirect = true
                        }
                    }
                    #endif
                    return nil
                }()
                let chip = demoChip ?? (header.hasDirectChip ? header.directChip : nil)
                let directActive = !term && !header.exited && header.computerOnline && !header.computerUnlinked
                    && chip?.visible == true && chip?.active == true
                let showDirectChip = !term && (directActive || directPopoverOpen)
                let chipLabel = chip?.hasLabelText == true ? chip!.labelText.rendered() : ""
                let titleView = SessionTitle(
                    title: titleText,
                    mark: StatusKind(header),
                    status: statusText,
                    mode: modeText,
                    danger: modeDanger,
                    showDirect: showDirectChip,
                    directLabel: chipLabel
                )
                if showDirectChip {
                    Button {
                        directPopoverOpen.toggle()
                    } label: {
                        titleView
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(L10n.transportPopoverA11Y())
                    .popover(isPresented: $directPopoverOpen, attachmentAnchor: .rect(.bounds), arrowEdge: .top) {
                        SessionDirectPopover(
                            computerName: computer,
                            directActive: directActive,
                            chip: chip,
                            onSettings: {
                                directPopoverOpen = false
                                nav.open(.machine(id: machineID, initialDirect: true))
                            }
                        )
                        .presentationCompactAdaptation(.popover)
                    }
                } else {
                    titleView
                }
            }
            if wide {
                ToolbarItem(placement: .topBarTrailing) {
                    GlassSegmented(items: views, selected: view, enabled: { $0 == 0 || hasTerminal }, dot: popup ? 1 : nil)
                        .accessibilityIdentifier("session-view")
                }
            }
            ToolbarItem(placement: .topBarTrailing) {
                Button { menu = true } label: {
                    Image(systemName: "ellipsis")
                        .foregroundStyle(DS.ink)
                        .overlay(alignment: .topTrailing) {
                            if high { Circle().fill(DS.orange).frame(width: 7, height: 7).offset(x: 5, y: -5) }
                        }
                }
                .accessibilityLabel(L10n.commonIosMore())
                .accessibilityValue(high ? L10n.commonIosAttention() : "")
            }
        }
        .toolbarBackground(.hidden, for: .navigationBar)
        .toolbarColorScheme(term ? .dark : nil, for: .navigationBar)
        .menuSheet(isPresented: $menu, groups: groups(header: header, actions: actions, live: live)) {
            let computerText = header.hasComputerName ? header.computerName.rendered() : machineLabel(state, id: machineID)
            let projectText = header.hasProjectName ? header.projectName.rendered() : projectName(header.projectKey)
            let parts = [header.agentName.isEmpty ? agentName(header.agent) : header.agentName, projectText]
                + (header.multipleMachines ? [computerText] : [])
            VStack(alignment: .leading, spacing: 2) {
                HStack(alignment: .firstTextBaseline, spacing: 0) {
                    AgentMark(header.agent, style: .headline, named: true)
                    Text(parts.filter { !$0.isEmpty }.joined(separator: " · "))
                }
                .font(.headline).foregroundStyle(DS.ink)
                Text(header.shortPath.isEmpty ? shortPath(header.cwd) : header.shortPath).font(.footnote.monospaced()).foregroundStyle(DS.ink2)
            }
        }
        .onChange(of: actions.fork.status) { old, new in
            if old == .pending && new == .idle && !actions.fork.frozen && commandErrorText(actions.fork).isEmpty {
                menu = false
            }
        }
        .kitSheet(item: $sheet) { which in
            switch which {
            case .mode: SessionModeSheet(sessionID: id, startConfirm: SessionDebug.shows("confirmFull")) { sheet = nil }
            case .model:
                SessionModelSheet(sessionID: id, onUsage: {
                    sheet = nil
                    nav.open(.usage(machine: header.machineID, agent: header.agent, model: header.model))
                }) { sheet = nil }
            }
        }
        .kitSheet(item: $projectSettings) { request in
            let key = header.projectKey
            if !key.isEmpty {
                CoreProjectSettingsSheet(open: request.page) { projectSettings = nil }
                    .task(id: key) {
                        await context.coreHost.showNewSession(projectKey: key)
                    }
            }
        }
        .sheet(isPresented: $scheduleSending) {
            NavigationStack {
                NewScheduleScreen(sessionID: id, onClose: { scheduleSending = false })
            }
            .onAppear { context.dismissNewSchedule = { scheduleSending = false } }
            .onDisappear { context.dismissNewSchedule = nil }
            .sheetSurface()
            .presentationDetents([.large])
        }
        .sessionRename(isPresented: $rename, sessionID: id)
        .confirm(
            isPresented: $confirmStop,
            title: L10n.sessionEndTitle(),
            text: actions.hasEndConfirmNote ? actions.endConfirmNote.rendered() : L10n.sessionEndNote(agent: header.agentName, name: header.computerName.rendered()),
            confirm: L10n.sessionEnd(),
            danger: true
        ) {
            Task {
                await context.coreHost.submitSessionCommand(
                    sessionID: id,
                    command: .with { $0.stopSession = .with { $0.sessionID = id } },
                    machineID: machineID,
                    slotKey: actions.end.slotKey
                )
            }
        }
        .task {
            #if DEBUG
            if model.isDemo, HomeLaunch.value("-schedulePanel") == "session-schedule" { scheduleSending = true }
            #endif
        }
        .debugSignal("session.schedule") {
            if header.computerOnline && !header.computerUnlinked && header.capabilities.supportsSchedule {
                scheduleSending = true
            }
        }
        .debugSignal("session.menu") { menu = true }
        .debugSignal("session.mode") { sheet = .mode }
        .debugSignal("session.model") { sheet = .model }
        .debugSignal("session.rename") { rename = true }
        .debugSignal("session.project") { projectSettings = ProjectSettingsRequest() }
        .debugSignal("session.close") {
            menu = false
            sheet = nil
            rename = false
            projectSettings = nil
        }
        .task {
            switch SessionDebug.shown {
            case "menu": menu = true
            case "mode", "confirmFull": sheet = .mode
            case "model": sheet = .model
            case "rename": rename = true
            case "project": projectSettings = ProjectSettingsRequest()
            case "stop": confirmStop = true
            default: break
            }
        }
    }

    @ViewBuilder private func chatPane(vm: PBSessionViewModel) -> some View {
        ChatPane(
            sessionID: id,
            vm: vm,
            onTerminal: { pick(1) },
            onLoadEarlier: {
                windowLimit += 80
                if vm.timeline.hasGap_p && !vm.timeline.backfilling && !vm.timeline.fetching {
                    Task { await context.coreHost.loadEarlierSessionHistory(id) }
                }
            },
            onToggleGroup: { groupID in
                if let idx = expandedGroups.firstIndex(of: groupID) {
                    expandedGroups.remove(at: idx)
                } else {
                    expandedGroups.append(groupID)
                }
            }
        )
    }

    private func commandErrorText(_ st: PBSessionCommandState) -> String {
        if st.hasErrorText {
            let rendered = st.errorText.rendered()
            if !rendered.isEmpty { return rendered }
        }
        return st.errorCode
    }

    private func groups(header: PBSessionHeaderView, actions: PBSessionActionsView, live: Bool) -> [[SheetAction]] {
        let forkCmd = actions.fork
        let forkAction = actions.forkAction
        let forking = forkAction.busy || forkCmd.status == .pending
        let running = header.status == .running || header.status == .starting
        var settings: [SheetAction] = []
        var list: [SheetAction] = []
        var end: [SheetAction] = []
        if live {
            if header.hasApprovalModeLabel {
                let modeLabel = header.approvalModeLabel.rendered()
                if !modeLabel.isEmpty {
                    settings.append(SheetAction(L10n.modesTitle(), value: modeLabel, danger: header.approvalMode == .fullAccess, enabled: !forking) {
                        afterMenu { sheet = .mode }
                    })
                }
            }
            let modelVal: String = {
                if header.modelLabel.isEmpty && header.model.isEmpty { return "" }
                let m = header.modelLabel.isEmpty ? header.model : header.modelLabel
                let eff = header.hasEffortLabel ? header.effortLabel.rendered() : ""
                return eff.isEmpty ? m : "\(m) · \(eff)"
            }()
            settings.append(SheetAction(
                modelVal.isEmpty ? L10n.sessionSettings() : L10n.modelsTitle(),
                value: modelVal,
                enabled: !forking
            ) {
                afterMenu { sheet = .model }
            })
            if header.hasContext_p {
                settings.append(SheetAction(
                    L10n.sessionContext(),
                    value: "\(header.contextPercent)%",
                    warn: header.highContextWarning,
                    enabled: !forking
                ) {
                    afterMenu { sheet = .model }
                })
            }
        }
        list.append(SheetAction(L10n.projectsIosSettings(), enabled: !forking && !header.projectKey.isEmpty) {
            afterMenu { projectSettings = ProjectSettingsRequest() }
        })
        let supportsSchedule = header.computerOnline && !header.computerUnlinked && header.capabilities.supportsSchedule
        list.append(SheetAction(
            L10n.sessionScheduleSend(),
            enabled: supportsSchedule,
            note: header.capabilities.supportsSchedule ? "" : L10n.sessionIosUpdateRequired()
        ) {
            afterMenu { scheduleSending = true }
        })
        if actions.canRename {
            list.append(SheetAction(L10n.sessionRename(), enabled: !forking) { afterMenu { rename = true } })
        }
        if forkAction.visible {
            let forkLabel = forkAction.hasLabel ? forkAction.label.rendered() : L10n.sessionFork()
            let forkNote = forkAction.hasNote ? forkAction.note.rendered() : ""
            list.append(SheetAction(
                forkLabel,
                enabled: forkAction.enabled || forkAction.busy,
                note: forkNote,
                noteError: forkAction.noteError,
                busy: forkAction.busy,
                keepOpen: true
            ) {
                guard !forkAction.busy, forkAction.enabled else { return }
                if forkAction.abandons && forkAction.canAbandon {
                    Task { await context.coreHost.abandonSessionCommand(sessionID: id, slotKey: forkCmd.slotKey) }
                } else {
                    Task {
                        await context.coreHost.submitSessionCommand(
                            sessionID: id,
                            command: .with { $0.forkSession = .with { $0.sessionID = id } },
                            machineID: header.machineID,
                            slotKey: forkCmd.slotKey
                        )
                    }
                }
            })
        }
        if running && actions.canInterrupt {
            list.append(SheetAction(L10n.sessionInterrupt(), danger: true, enabled: !forking) {
                Task {
                    await context.coreHost.submitSessionCommand(
                        sessionID: id,
                        command: .with { $0.interrupt = .with { $0.sessionID = id } },
                        machineID: header.machineID,
                        slotKey: actions.interrupt.slotKey
                    )
                }
            })
        }
        if actions.canEnd {
            end.append(SheetAction(L10n.sessionEndAction(), danger: true, enabled: !forking) { afterMenu { confirmStop = true } })
        }
        return [settings, list, end]
    }
}

/// Shown while a session is loading or missing from Core.
private struct SessionAbsent: View {
    let header: PBSessionHeaderView
    let online: Bool
    let onRetry: () -> Void
    @Environment(Navigator.self) private var nav

    var body: some View {
        Page {
            VStack(spacing: 0) {
                ConnectionBar()
                Spacer(minLength: 24)
                if header.lookupFailed {
                    let message = header.hasLookupErrorText ? header.lookupErrorText.rendered() : header.missingText.rendered()
                    Text(message.isEmpty ? L10n.sessionIosMissing() : message).font(.headline).ink(DS.ink).multilineTextAlignment(.center).accessibilityAddTraits(.isHeader)
                    TextAction(L10n.commonRetry(), alignment: .center, action: onRetry)
                    TextAction(L10n.commonBack(), alignment: .center) { nav.back() }
                } else if header.missing {
                    Text(header.missingText.rendered()).font(.headline).ink(DS.ink).multilineTextAlignment(.center).accessibilityAddTraits(.isHeader)
                    TextAction(L10n.commonBack(), alignment: .center) { nav.back() }
                } else {
                    Mark(Mark.progress, color: DS.ink2, size: 22, relativeTo: .body)
                    Text(online ? L10n.projectsReadingDirectory() : L10n.sessionIosConnectRead()).font(.subheadline).ink(DS.ink2).padding(.top, 12)
                        .multilineTextAlignment(.center)
                }
                Spacer(minLength: 24)
            }
            .padding(.horizontal, 24)
        }
    }
}

private let directChipSymbol = "bolt.horizontal"

private struct SessionTitle: View {
    let title: String
    let mark: StatusKind?
    let status: String
    let mode: String
    let danger: Bool
    var showDirect: Bool = false
    var directLabel: String = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 1) {
            Text(title).font(.headline).foregroundStyle(DS.ink).lineLimit(1)
            HStack(spacing: 5) {
                if let mark { StatusMark(mark).scaleEffect(0.8) }
                Text("\(Text(status).foregroundStyle(DS.ink2))\(Text(mode.isEmpty ? "" : " · ").foregroundStyle(DS.ink2))\(Text(mode).foregroundStyle(danger ? DS.red : DS.ink2))")
                    .font(.caption).lineLimit(1)
                if showDirect {
                    HStack(spacing: 3) {
                        Text("·").foregroundStyle(DS.ink2)
                        SymbolImage(directChipSymbol, style: .caption2, color: DS.ink2)
                        Text(directLabel.or(L10n.transportDirect())).foregroundStyle(DS.ink2)
                    }
                    .font(.caption)
                    .lineLimit(1)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .dynamicTypeSize(...DynamicTypeSize.accessibility1)
        .accessibilityElement(children: .combine)
        .accessibilityAddTraits(.isHeader)
    }
}

private struct SessionDirectPopover: View {
    let computerName: String
    let directActive: Bool
    let chip: PBSessionDirectChipView?
    let onSettings: () -> Void

    var body: some View {
        let titleText: String = {
            if let chip, chip.hasPopoverTitle {
                let text = chip.popoverTitle.rendered()
                if !text.isEmpty && (directActive || !chip.active) { return text }
            }
            return directActive
                ? L10n.transportPopoverDirectTitle(name: computerName)
                : L10n.transportPopoverRelayTitle(name: computerName)
        }()
        let bodyText: String = {
            if let chip, chip.hasPopoverBody {
                let text = chip.popoverBody.rendered()
                if !text.isEmpty && (directActive || !chip.active) { return text }
            }
            return directActive ? L10n.transportPopoverDirectBody() : L10n.transportPopoverRelayBody()
        }()
        let footerStatus: String = {
            if let chip, chip.hasPopoverFoot {
                let text = chip.popoverFoot.rendered()
                if !text.isEmpty && (directActive || !chip.active) { return text }
            }
            return directActive ? L10n.transportPopoverStable() : L10n.transportPopoverSwitchedRelay()
        }()

        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 8) {
                Dot(directActive ? DS.green : DS.ink3, size: 8)
                Text(titleText)
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(DS.ink)
                    .monospacedDigit()
            }
            Text(bodyText)
                .font(.footnote)
                .foregroundStyle(DS.ink2)
                .fixedSize(horizontal: false, vertical: true)
            Rectangle()
                .fill(DS.sep)
                .frame(height: 1)
                .accessibilityHidden(true)
            HStack(spacing: 10) {
                Text(footerStatus)
                    .font(.footnote)
                    .foregroundStyle(DS.ink2)
                Spacer(minLength: 8)
                Button(action: onSettings) {
                    ArrowText(L10n.transportPopoverSettings(), style: .footnote)
                }
                .buttonStyle(.plain)
            }
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 14)
        .frame(width: 300)
        .background(DS.lift)
    }
}
