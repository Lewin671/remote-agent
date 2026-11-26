// The sheets and the dialog of a session's ⋯ menu: 审批模式, 会话设置 (context,
// model and reasoning effort) and 重命名会话. Rendered from Core's
// `PBSessionViewModel.{header, actions}` and dispatched via Core semantic actions.
import RACore
import RAProto
import SwiftUI

/// 审批模式: the four modes; full access is confirmed first; only this session changes.
struct SessionModeSheet: View {
    let sessionID: String
    var startConfirm = false
    let onClose: () -> Void
    @Environment(AccountContext.self) private var context
    @State private var confirmFull = false
    @State private var initialSucceededSeq: UInt64?

    private var vm: PBSessionViewModel? { context.core.sessions[sessionID] }
    private var header: PBSessionHeaderView { vm?.header ?? PBSessionHeaderView() }
    private var actions: PBSessionActionsView { vm?.actions ?? PBSessionActionsView() }

    private func set(_ mode: PBApprovalMode, choice: PBSessionModeChoiceView?) {
        if let choice, !choice.available { return }
        if initialSucceededSeq == nil { initialSucceededSeq = actions.setMode.succeededSeq }
        Task {
            await context.coreHost.setApprovalMode(sessionID: sessionID, mode: mode)
        }
    }

    var body: some View {
        let choices = actions.modeChoices
        let current = header.approvalMode
        let cmd = actions.setMode
        let running = cmd.isRunning
        let errorText = cmd.renderedErrorText
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
        .onAppear {
            if initialSucceededSeq == nil { initialSucceededSeq = cmd.succeededSeq }
        }
        .onChange(of: cmd.succeededSeq) { _, newSeq in
            if shouldCloseAfterCommandSuccess(baseline: initialSucceededSeq, current: newSeq) {
                initialSucceededSeq = newSeq
                onClose()
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
    @State private var initialModelSeq: UInt64?
    @State private var initialCompactSeq: UInt64?

    private var vm: PBSessionViewModel? { context.core.sessions[sessionID] }
    private var header: PBSessionHeaderView { vm?.header ?? PBSessionHeaderView() }
    private var actions: PBSessionActionsView { vm?.actions ?? PBSessionActionsView() }

    private func setModel(_ slug: String, _ effort: String) {
        guard actions.canSwitchModel else { return }
        if initialModelSeq == nil { initialModelSeq = actions.setModel.succeededSeq }
        Task {
            await context.coreHost.setSessionModel(sessionID: sessionID, model: slug, effort: effort)
        }
    }

    var body: some View {
        let modelCmd = actions.setModel
        let modelRunning = modelCmd.isRunning
        let modelError = modelCmd.renderedErrorText
        let compactCmd = actions.compact
        let compactRunning = compactCmd.isRunning
        let compactError = compactCmd.renderedErrorText
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
                    if initialCompactSeq == nil { initialCompactSeq = compactCmd.succeededSeq }
                    Task {
                        await context.coreHost.sessionSimpleCommand(sessionID: sessionID, kind: .compact)
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
                    list(modelRunning: modelRunning)
                }
                let blockedNote = actions.hasModelSwitchBlockedReason ? actions.modelSwitchBlockedReason.rendered() : ""
                Hint(!blockedNote.isEmpty ? blockedNote : L10n.sessionIosEffortNote())
                if modelRunning { Hint(L10n.modesSwitching()) }
                if !modelRunning && !modelError.isEmpty { Hint(L10n.modesSwitchFailed(reason: modelError), color: DS.red) }
            }
            if actions.hasUsageEntry && actions.usageEntry.visible {
                usageEntrySection(actions.usageEntry)
            }
        }
        .onAppear {
            if initialModelSeq == nil { initialModelSeq = modelCmd.succeededSeq }
            if initialCompactSeq == nil { initialCompactSeq = compactCmd.succeededSeq }
        }
        .onChange(of: modelCmd.succeededSeq) { _, newSeq in
            if shouldCloseAfterCommandSuccess(baseline: initialModelSeq, current: newSeq) {
                initialModelSeq = newSeq
                onClose()
            }
        }
        .onChange(of: compactCmd.succeededSeq) { _, newSeq in
            if shouldCloseAfterCommandSuccess(baseline: initialCompactSeq, current: newSeq) {
                initialCompactSeq = newSeq
                onClose()
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

    @ViewBuilder private func list(modelRunning: Bool) -> some View {
        let disabled = !actions.canSwitchModel || modelRunning
        let offered = actions.modelChoices
        let folded = actions.foldedModelChoices
        let shown = (all || folded.isEmpty) ? offered : folded
        let hasMore = !all && actions.hasMoreModels_p
        let current = offered.first(where: \.selected)
        ForEach(Array(shown.enumerated()), id: \.element.slug) { i, m in
            let effortHint = m.hasMaxEffortHint ? m.maxEffortHint.rendered() : ""
            ListItem(
                m.displayName.isEmpty ? m.slug : m.displayName,
                checked: m.selected,
                enabled: !disabled,
                small: effortHint.isEmpty ? nil : effortHint,
                last: i == shown.count - 1 && !hasMore
            ) {
                if !m.selected {
                    setModel(m.slug, "")
                }
            }
        }
        if hasMore { TextAction(L10n.modelsAll(), style: .body, arrow: true) { all = true } }
        let efforts = actions.effortChoices
        if !efforts.isEmpty, let chosen = current {
            FieldLabel(L10n.modelsEffort())
            ChoiceGrid(
                efforts.map(\.id),
                selected: efforts.first(where: \.selected)?.id,
                enabled: !disabled,
                label: { id in
                    if let hit = efforts.first(where: { $0.id == id }), hit.hasLabel {
                        let s = hit.label.rendered()
                        if !s.isEmpty { return s }
                    }
                    return id
                }
            ) { setModel(chosen.slug, $0) }
        } else if let chosen = current, chosen.hasFixedEffortNote {
            let note = chosen.fixedEffortNote.rendered()
            if !note.isEmpty {
                FieldLabel(L10n.modelsEffort())
                Hint(note)
            }
        }
    }
}

// MARK: Renaming

private struct SessionRename: ViewModifier {
    @Binding var isPresented: Bool
    let sessionID: String
    @Environment(AccountContext.self) private var context
    @State private var initialSucceededSeq: UInt64?

    func body(content: Content) -> some View {
        let vm = context.core.sessions[sessionID]
        let rawTitle = vm?.header.found == true ? vm!.header.title.rendered().trimmingCharacters(in: .whitespacesAndNewlines) : ""
        let cmd = vm?.actions.rename ?? PBCoreSessionCommandState()
        let running = cmd.isRunning
        let errorText = running ? "" : cmd.renderedErrorText
        content
            .inputAlert(
                isPresented: $isPresented,
                title: L10n.sessionRenameTitle(),
                initial: titleInput(rawTitle),
                placeholder: L10n.sessionIosTitleField(),
                text: L10n.sessionAutoTitleHint(),
                limit: titleMax,
                lines: 1...1,
                filter: titleInput,
                debugSignal: "session.rename.save",
                debugText: { SessionDebug.text },
                running: running,
                error: errorText
            ) { title in
                if initialSucceededSeq == nil { initialSucceededSeq = cmd.succeededSeq }
                let host = context.coreHost
                let sid = sessionID
                Task { await host.renameSession(sessionID: sid, title: title) }
            }
            .onChange(of: isPresented) { _, shown in
                initialSucceededSeq = shown ? cmd.succeededSeq : nil
            }
            .onChange(of: cmd.succeededSeq) { _, newSeq in
                guard isPresented, shouldCloseAfterCommandSuccess(baseline: initialSucceededSeq, current: newSeq) else { return }
                initialSucceededSeq = newSeq
                Haptics.confirm()
                isPresented = false
            }
    }
}

extension View {
    /// 重命名会话 over this view.
    func sessionRename(isPresented: Binding<Bool>, sessionID: String) -> some View {
        modifier(SessionRename(isPresented: isPresented, sessionID: sessionID))
    }
}
