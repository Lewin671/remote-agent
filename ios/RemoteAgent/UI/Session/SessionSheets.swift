// The sheets and the dialog of a session's ⋯ menu: 审批模式, 会话设置 (context,
// model and reasoning effort) and 重命名会话. Rendered from Core's
// `PBSessionViewModel.{header, actions}` and dispatched via `CoreHost`.
import RACore
import RAProto
import SwiftUI

private func commandPending(_ st: PBCoreSessionCommandState?) -> Bool {
    st?.status == .pending || st?.busy == true
}

private func commandError(_ st: PBCoreSessionCommandState?) -> String {
    guard let st else { return "" }
    if st.hasErrorText {
        let rendered = st.errorText.rendered()
        if !rendered.isEmpty { return rendered }
    }
    return st.errorCode
}

/// Awaits Core command settlement (when `status` leaves `.pending`/busy) on the MainActor without polling.
@MainActor
private func awaitSessionCommand(
    context: AccountContext,
    sessionID: String,
    extract: @MainActor @escaping (PBSessionViewModel) -> PBCoreSessionCommandState,
    send: @escaping () -> Void
) async throws {
    let initial = context.core.sessions[sessionID].map(extract)
    let wasFrozen = initial?.frozen == true
    send()
    for _ in 0..<256 {
        guard let vm = context.core.sessions[sessionID] else { return }
        let st = extract(vm)
        if !commandPending(st) {
            if st.frozen && !wasFrozen {
                let msg = commandError(st)
                throw CommandError(CommandError.unknownOutcome, msg.isEmpty ? L10nMessage.errorsTheRelayDidNotProvideAVerifiableExecutionResult() : LocalizedMessage(raw: msg), outcome: .unknown)
            }
            let msg = commandError(st)
            if !msg.isEmpty || st.status == .failed || st.status == .unknown {
                throw UserError(msg.isEmpty ? L10nMessage.errorsTheRelayDidNotProvideAVerifiableExecutionResult() : LocalizedMessage(raw: msg))
            }
            return
        }
        await withCheckedContinuation { (cont: CheckedContinuation<Void, Never>) in
            withObservationTracking {
                _ = context.core.sessions[sessionID].map(extract)
            } onChange: {
                cont.resume()
            }
        }
    }
}

/// 审批模式: the four modes; full access is confirmed first; only this session changes.
struct SessionModeSheet: View {
    let sessionID: String
    var startConfirm = false
    let onClose: () -> Void
    @Environment(AccountContext.self) private var context
    @State private var confirmFull = false
    @State private var waitingClose = false

    private var vm: PBSessionViewModel? { context.core.sessions[sessionID] }
    private var header: PBSessionHeaderView { vm?.header ?? PBSessionHeaderView() }
    private var actions: PBSessionActionsView { vm?.actions ?? PBSessionActionsView() }

    private func set(_ mode: PBApprovalMode, choice: PBSessionModeChoiceView?) {
        if let choice, !choice.available { return }
        waitingClose = true
        let machineID = header.machineID
        Task {
            await context.coreHost.submitSessionCommand(
                sessionID: sessionID,
                command: .with {
                    $0.setApprovalMode = .with {
                        $0.sessionID = sessionID
                        $0.mode = mode
                    }
                },
                machineID: machineID,
                slotKey: actions.setMode.slotKey
            )
        }
    }

    var body: some View {
        let choices = actions.modeChoices
        let current = header.approvalMode
        let cmd = actions.setMode
        let running = commandPending(cmd)
        let errorText = commandError(cmd)
        let outside: String? = header.terminalModeOverride ? header.approvalModeLabel.rendered() : nil
        Group {
            SheetHead(L10n.modesTitle())
            if let outside, !outside.isEmpty {
                Text(L10n.modesCurrentTerminal(mode: outside)).font(.footnote).ink(DS.ink2).padding(.bottom, 4)
            }
            ForEach(Array(choices.enumerated()), id: \.offset) { i, choice in
                let off = actions.modeLocked || !choice.available
                let reason = choice.hasUnavailableReason ? choice.unavailableReason.rendered() : ""
                let note = choice.hasNote ? choice.note.rendered() : ""
                let help = off && !reason.isEmpty ? reason : note
                ListItem(
                    choice.label.rendered(),
                    desc: help,
                    checked: choice.selected,
                    enabled: !off,
                    danger: choice.mode == .fullAccess && !off,
                    last: i == choices.count - 1,
                    onTap: running || off
                        ? nil
                        : {
                            if choice.mode == .fullAccess && current != .fullAccess {
                                confirmFull = true
                            } else {
                                set(choice.mode, choice: choice)
                            }
                        }
                )
            }
            if actions.hasModePermissionNotice {
                let notice = actions.modePermissionNotice.rendered()
                if !notice.isEmpty { Hint(notice) }
            }
            Hint(outside.map { L10n.modesIosOutsideNote(mode: $0) } ?? L10n.sessionOnlyCurrent())
            if running { Hint(L10n.modesSwitching()) }
            if !running && !errorText.isEmpty { Hint(L10n.modesSwitchFailed(reason: errorText), color: DS.red) }
        }
        .confirm(
            isPresented: $confirmFull, title: L10n.modesFullTitle(), text: L10n.modesFullCurrentNote(),
            confirm: L10n.modesSwitch(), danger: true
        ) {
            let fullChoice = choices.first { $0.mode == .fullAccess }
            set(.fullAccess, choice: fullChoice)
        }
        .onChange(of: cmd.status) { old, new in
            if waitingClose && old == .pending && new == .idle && !cmd.frozen && commandError(cmd).isEmpty {
                waitingClose = false
                onClose()
            } else if new == .failed || new == .unknown {
                waitingClose = false
            }
        }
        .task {
            let fullChoice = choices.first { $0.mode == .fullAccess }
            if startConfirm && fullChoice?.available == true { confirmFull = true }
        }
        .debugSignal("session.mode.pick") {
            let named: [String: PBApprovalMode] = [
                "ask": .ask, "auto": .auto, "plan": .plan, "full": .fullAccess,
            ]
            if let m = named[SessionDebug.text] {
                set(m, choice: choices.first { $0.mode == m })
            }
        }
    }
}

/// 会话设置: context and 压缩, the model and its reasoning effort.
struct SessionModelSheet: View {
    let sessionID: String
    var onUsage: (() -> Void)? = nil
    let onClose: () -> Void
    @Environment(AccountContext.self) private var context
    @State private var all = false
    @State private var picked = ""
    @State private var waitingModel = false
    @State private var waitingCompact = false

    private var vm: PBSessionViewModel? { context.core.sessions[sessionID] }
    private var header: PBSessionHeaderView { vm?.header ?? PBSessionHeaderView() }
    private var actions: PBSessionActionsView { vm?.actions ?? PBSessionActionsView() }

    private func setModel(_ slug: String, _ effort: String) {
        guard actions.canSwitchModel else { return }
        waitingModel = true
        let machineID = header.machineID
        Task {
            await context.coreHost.submitSessionCommand(
                sessionID: sessionID,
                command: .with {
                    $0.setModel = .with {
                        $0.sessionID = sessionID
                        $0.model = slug
                        if !effort.isEmpty { $0.effort = effort }
                    }
                },
                machineID: machineID,
                slotKey: actions.setModel.slotKey
            )
        }
    }

    var body: some View {
        let modelCmd = actions.setModel
        let modelRunning = commandPending(modelCmd)
        let modelError = commandError(modelCmd)
        let compactCmd = actions.compact
        let compactRunning = commandPending(compactCmd)
        let compactError = commandError(compactCmd)
        let running = header.status == .running || header.status == .starting
        Group {
            SheetHead(L10n.sessionSettings())
            if header.hasContext_p {
                let ctxDetail = header.hasContextText ? header.contextText.rendered() : L10n.sessionIosContextOccupancy()
                ContextBox(
                    percent: Int(header.contextPercent),
                    warn: header.highContextWarning,
                    detail: ctxDetail,
                    action: compactRunning ? L10n.sessionCompacting() : L10n.sessionCompact(),
                    enabled: actions.canCompact && !compactRunning
                ) {
                    waitingCompact = true
                    let machineID = header.machineID
                    Task {
                        await context.coreHost.submitSessionCommand(
                            sessionID: sessionID,
                            command: .with { $0.compact = .with { $0.sessionID = sessionID } },
                            machineID: machineID,
                            slotKey: compactCmd.slotKey
                        )
                    }
                }
                if !compactRunning && !compactError.isEmpty {
                    Hint(L10n.sessionCompactFailed(reason: compactError), color: DS.red)
                }
            }
            if !header.capabilities.supportsModelSwitch {
                let modelTitle = header.modelLabel.isEmpty
                    ? (header.model.isEmpty ? L10n.newSessionDefaultModel() : header.model)
                    : header.modelLabel
                let effortTitle = header.hasEffortLabel
                    ? header.effortLabel.rendered()
                    : (header.effort.isEmpty ? L10n.modelsDefault() : header.effort)
                FieldLabel(L10n.modelsTitle())
                ListItem(modelTitle, desc: L10n.sessionModelLocked(), enabled: false, last: true)
                FieldLabel(L10n.modelsEffort())
                ListItem(effortTitle, desc: L10n.sessionEffortLocked(), enabled: false, last: true)
            } else {
                FieldLabel(L10n.modelsTitle())
                if actions.hasModelsError && actions.modelChoices.isEmpty {
                    Banner(L10n.sessionIosModelsFailed(reason: actions.modelsError.rendered()), tone: .error, action: L10n.commonRetry()) {
                        Task { await context.coreHost.openSession(sessionID, refreshModels: true) }
                    }
                } else if !actions.modelsLoaded && actions.modelsLoading {
                    LoadingRow(inset: 0)
                } else {
                    list(actions.modelChoices, running: running, modelRunning: modelRunning)
                }
                let blockedNote = actions.hasModelSwitchBlockedReason ? actions.modelSwitchBlockedReason.rendered() : ""
                Hint(!blockedNote.isEmpty ? blockedNote : (running ? L10n.sessionIosChangeIdle() : L10n.sessionIosEffortNote()))
                if modelRunning { Hint(L10n.modesSwitching()) }
                if !modelRunning && !modelError.isEmpty { Hint(L10n.modesSwitchFailed(reason: modelError), color: DS.red) }
            }
            if actions.hasUsageEntry && actions.usageEntry.visible {
                usageEntrySection(actions.usageEntry)
            }
        }
        .onChange(of: actions.modelChoices, initial: true) { _, choices in
            if picked.isEmpty {
                picked = choices.first(where: \.selected)?.slug ?? ""
            }
        }
        .onChange(of: modelCmd.status) { old, new in
            if waitingModel && old == .pending && new == .idle && !modelCmd.frozen && commandError(modelCmd).isEmpty {
                waitingModel = false
                onClose()
            } else if new == .failed || new == .unknown {
                waitingModel = false
            }
        }
        .onChange(of: compactCmd.status) { old, new in
            if waitingCompact && old == .pending && new == .idle && !compactCmd.frozen && commandError(compactCmd).isEmpty {
                waitingCompact = false
                onClose()
            } else if new == .failed || new == .unknown {
                waitingCompact = false
            }
        }
        .task {
            await context.coreHost.openSession(sessionID, refreshModels: true)
        }
    }

    @ViewBuilder private func usageEntrySection(_ entry: PBSessionUsageEntryView) -> some View {
        FieldLabel(L10n.usageTitle())
        if entry.loading {
            LoadingRow(inset: 0)
        } else {
            let valueStr = entry.hasValueText ? entry.valueText.rendered() : ""
            let descStr = entry.hasSubtitle ? entry.subtitle.rendered() : ""
            ListItem(
                L10n.usageTitle(),
                desc: descStr,
                enabled: entry.supported && onUsage != nil,
                small: valueStr.isEmpty ? nil : valueStr,
                last: true,
                onTap: entry.supported ? onUsage : nil
            )
            if entry.hasFootnote {
                let foot = entry.footnote.rendered()
                if !foot.isEmpty { Hint(foot) }
            }
        }
    }

    @ViewBuilder private func list(_ offered: [PBSessionModelChoiceView], running: Bool, modelRunning: Bool) -> some View {
        let disabled = !actions.canSwitchModel || running || modelRunning
        let selectedChoice = offered.first { $0.slug == picked } ?? offered.first(where: \.selected)
        let shown = shownModels(offered, picked: selectedChoice, all: all)
        let few = shown.count == offered.count
        ForEach(Array(shown.enumerated()), id: \.element.slug) { i, m in
            let effortHint = m.hasMaxEffortHint ? m.maxEffortHint.rendered() : ""
            ListItem(
                m.displayName.isEmpty ? m.slug : m.displayName,
                checked: m.slug == (selectedChoice?.slug ?? picked),
                enabled: !disabled,
                small: effortHint.isEmpty ? nil : effortHint,
                last: i == shown.count - 1 && few
            ) {
                picked = m.slug
                if m.efforts.isEmpty && !m.selected {
                    setModel(m.slug, "")
                }
            }
        }
        if !few { TextAction(L10n.modelsAll(), style: .body, arrow: true) { all = true } }
        if let chosen = offered.first(where: { $0.slug == (picked.isEmpty ? (selectedChoice?.slug ?? "") : picked) }), !chosen.efforts.isEmpty {
            FieldLabel(L10n.modelsEffort())
            ChoiceGrid(
                chosen.efforts,
                selected: chosen.selected ? (header.effort.isEmpty ? nil : header.effort) : nil,
                enabled: !disabled
            ) { setModel(chosen.slug, $0) }
        }
    }
}

// MARK: Renaming

private struct SessionRename: ViewModifier {
    @Binding var isPresented: Bool
    let sessionID: String
    @Environment(AccountContext.self) private var context

    func body(content: Content) -> some View {
        let vm = context.core.sessions[sessionID]
        let rawTitle = vm?.header.found == true ? vm!.header.title.rendered() : ""
        let initial = titleInput(rawTitle.trimmingCharacters(in: .whitespacesAndNewlines))
        let machineID = vm?.header.machineID ?? ""
        let slotKey = vm?.actions.rename.slotKey ?? ""
        content.inputAlert(
            isPresented: $isPresented, title: L10n.sessionRenameTitle(), initial: initial, placeholder: L10n.sessionIosTitleField(), text: L10n.sessionAutoTitleHint(),
            limit: .max, lines: 1...1, filter: titleInput, debugSignal: "session.rename.save", debugText: { SessionDebug.text }
        ) { typed in
            let clean = titleInput(typed).trimmingCharacters(in: .whitespacesAndNewlines)
            guard clean != initial else { return }
            try await awaitSessionCommand(
                context: context,
                sessionID: sessionID,
                extract: { $0.actions.rename }
            ) {
                Task {
                    await context.coreHost.submitSessionCommand(
                        sessionID: sessionID,
                        command: .with {
                            $0.renameSession = .with {
                                $0.sessionID = sessionID
                                $0.title = clean
                            }
                        },
                        machineID: machineID,
                        slotKey: slotKey
                    )
                }
            }
        }
    }
}

extension View {
    /// 重命名会话 over this view.
    func sessionRename(isPresented: Binding<Bool>, sessionID: String) -> some View {
        modifier(SessionRename(isPresented: isPresented, sessionID: sessionID))
    }
}
