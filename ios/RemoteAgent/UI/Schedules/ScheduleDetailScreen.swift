import RACore
import RAProto
import SwiftUI

private enum ScheduleDetailEditor: String, Identifiable { case message, timing, target, mode; var id: String { rawValue } }

struct ScheduleDetailScreen: View {
    let machine: String
    let id: String
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @Environment(\.page) private var page
    @State private var editor: ScheduleDetailEditor?
    @State private var rename = false
    @State private var delete = false
    @State private var menu = false

    private var taskRef: PBScheduleTaskRef { .with { $0.machineID = machine; $0.taskID = id } }
    private func operate(_ op: PBScheduleOperationKind) {
        context.dispatch { $0.scheduleOperate = .with { $0.machineID = machine; $0.taskID = id; $0.operation = op } }
    }
    private func openEdit(_ kind: ScheduleDetailEditor, mode: PBScheduleFormMode) {
        editor = kind; Task { await context.coreHost.openScheduleForm(mode: mode, machineID: machine, taskID: id) }
    }
    private func openSession(_ sid: String, runID: String = "", seq: UInt64 = 0) {
        guard !sid.isEmpty else { return }
        if seq > 0 || !runID.isEmpty { context.sessionStore.jumpToScheduledRun(sid, runID: runID, seq: seq) }
        nav.open(.session(id: sid))
    }
    private func markUnreadAndBack() {
        guard nav.showing(page) else { return }
        context.dispatch { $0.scheduleMarkUnread = .with { $0.machineID = machine; $0.taskID = id; $0.value = true } }
        nav.back(from: page)
    }

    var body: some View {
        let vm = context.core.scheduleDetail, form = context.core.scheduleForm
        let displayed = (vm.machineID == machine && vm.taskID == id) ? vm : PBScheduleDetailViewModel()
        ScheduleDetailContent(
            vm: displayed, connectionBar: { ConnectionBar() },
            onRetryMachine: { context.dispatch { $0.scheduleRefresh.value = machine } },
            onRecover: { operate(.recover) },
            onRetryMutation: { context.dispatch { $0.scheduleRetryMutation = .with { $0.machineID = machine; $0.taskID = id } } },
            onAbandonMutation: { context.dispatch { $0.scheduleAbandonMutation = .with { $0.machineID = machine; $0.taskID = id } } },
            onShowLatestResult: { context.dispatch { $0.scheduleDetailShowLatestResult = taskRef } },
            onOpenSession: { sid, runID, seq in
                if !runID.isEmpty || seq > 0 { context.dispatch { $0.scheduleDetailOpenRun = .with { $0.machineID = machine; $0.taskID = id; $0.value = runID } } }
                openSession(sid, runID: runID, seq: seq)
            },
            onToggleQuietGroup: { key in context.dispatch { $0.scheduleDetailToggleQuietGroup = .with { $0.machineID = machine; $0.taskID = id; $0.value = key } } },
            onShowAllRuns: { context.dispatch { $0.scheduleDetailShowAllRuns = .with { $0.machineID = machine; $0.taskID = id; $0.value = true } } },
            onEditMessage: { openEdit(.message, mode: .editMessage) },
            onEditTiming: { openEdit(.timing, mode: .editTiming) },
            onSetNotify: { on in context.dispatch { $0.scheduleSetNotify = .with { $0.machineID = machine; $0.taskID = id; $0.value = on } } },
            onEditTarget: { openEdit(.target, mode: .editTarget) },
            onEditMode: { openEdit(.mode, mode: .editMode) },
            onRename: { rename = true }
        )
        .navBar(displayed.found ? displayed.title : L10n.scheduleIosTaskDetails(),
                subtitle: displayed.found ? displayed.subtitle.rendered() : nil, onMore: displayed.found ? { menu = true } : nil)
        .actionMenu(isPresented: $menu, actions: menuActions(displayed)) {
            if displayed.found {
                VStack(alignment: .leading, spacing: 4) {
                    Text(displayed.title).font(.body.weight(.semibold)).ink(DS.ink)
                    Text(displayed.menuSubtitle.rendered()).font(.footnote).ink(DS.ink2)
                }
            }
        }
        .kitSheet(item: $editor, size: .large, onDismiss: {
            let key = form.formKey; context.dispatch { $0.scheduleFormClose = .with { $0.formKey = key; $0.value = true } }
        }) { which in
            switch which {
            case .timing: ScheduleTimingSheet(submitOnApply: true, onClose: { editor = nil })
            case .target: ScheduleConfigSheet(focus: .target, showSaveButton: true, onClose: { editor = nil })
            case .mode: ScheduleConfigSheet(focus: .mode, showSaveButton: true, onClose: { editor = nil })
            case .message: EmptyView()
            }
        }
        .fullScreenCover(isPresented: Binding(get: { editor == .message }, set: { if !$0 { editor = nil } }), onDismiss: {
            let key = form.formKey; context.dispatch { $0.scheduleFormClose = .with { $0.formKey = key; $0.value = true } }
        }) { ScheduleMessageEditor(onClose: { editor = nil }) }
        .modifier(ScheduleRenameDialog(
            isPresented: $rename, initialTitle: displayed.taskName, mutation: displayed.mutation, enabled: displayed.actions.canRename,
            onSave: { t in context.dispatch { $0.scheduleRename = .with { $0.machineID = machine; $0.taskID = id; $0.value = t } } }
        ))
        .confirm(isPresented: $delete, title: L10n.scheduleIosDeleteThisTask(),
                 text: L10n.scheduleIosStopsFutureAutomaticRunsMessagesAlreadySentAndConversations(),
                 confirm: L10n.scheduleDelete(), danger: true) { operate(.cancel) }
        .onChange(of: form.open) { wasOpen, isOpen in if wasOpen && !isOpen { editor = nil } }
        .debugSignal("schedule.more") { menu = true }
        .debugSignal("schedule.result.latest") { context.dispatch { $0.scheduleDetailShowLatestResult = taskRef } }
        .debugSignal("schedule.mark-unread") { markUnreadAndBack() }
        .debugSignal("schedule.history.all") { context.dispatch { $0.scheduleDetailShowAllRuns = .with { $0.machineID = machine; $0.taskID = id; $0.value = true } } }
        .debugSignal("schedule.edit-message") { if displayed.found { openEdit(.message, mode: .editMessage) } }
        .debugSignal("schedule.edit-target") { if displayed.found { openEdit(.target, mode: .editTarget) } }
        .debugSignal("schedule.open-run") {
            if let r = displayed.earlierGroups.lazy.flatMap(\.runs).first(where: { $0.canOpenSession && !$0.sessionID.isEmpty }) {
                openSession(r.sessionID, runID: r.runID, seq: r.firstSeq)
            } else if displayed.resultCard.showConversationAction && !displayed.resultCard.sessionID.isEmpty {
                openSession(displayed.resultCard.sessionID, runID: displayed.resultCard.runID, seq: displayed.resultCard.firstSeq)
            }
        }
        .debugSignal("schedule.pause") { operate(.pause) }
        .debugSignal("schedule.run") { operate(.run) }
        .task(id: "\(machine)/\(id)") {
            await context.coreHost.openScheduleDetail(machineID: machine, taskID: id)
            #if DEBUG
            if HomeLaunch.value("-schedulePanel") == "editor" { openEdit(.message, mode: .editMessage) }
            else if HomeLaunch.value("-schedulePanel")?.hasPrefix("timing-") == true { openEdit(.timing, mode: .editTiming) }
            #endif
        }
        .onDisappear {
            let popped = !nav.path.contains(.schedule(machine: machine, id: id))
            Task { if popped { await context.coreHost.closeScheduleDetail(machineID: machine, taskID: id) } else { await context.coreHost.hideScheduleDetail(machineID: machine, taskID: id) } }
        }
    }

    private func menuActions(_ vm: PBScheduleDetailViewModel) -> [SheetAction] {
        guard vm.found else { return [] }
        let a = vm.actions
        var actions: [SheetAction] = [
            SheetAction(L10n.scheduleRunOnceNow(), enabled: a.canRunNow, note: a.canRunNow ? "" : a.runNowDisabledReason.rendered()) { operate(.run) }
        ]
        if a.canMarkUnread { actions.append(SheetAction(L10n.scheduleMarkAsUnread()) { afterMenu { markUnreadAndBack() } }) }
        if a.showToggleAction {
            actions.append(SheetAction(a.toggleLabel.rendered(), enabled: a.canToggle, note: a.canToggle ? "" : a.toggleDisabledReason.rendered()) {
                if a.isResumeAction { afterMenu { operate(.recover) } } else { operate(.pause) }
            })
        }
        actions.append(SheetAction(L10n.scheduleDeleteTask(), danger: true, enabled: a.canDelete, note: a.canDelete ? "" : a.deleteDisabledReason.rendered()) {
            afterMenu { delete = true }
        })
        return actions
    }
    private func afterMenu(_ action: @escaping @MainActor () -> Void) { Task { try? await Task.sleep(for: .milliseconds(220)); action() } }
}

/// Pure presentation body for `ScheduleDetailScreen`, driven solely by `PBScheduleDetailViewModel`.
struct ScheduleDetailContent<ConnectionView: View>: View {
    let vm: PBScheduleDetailViewModel
    @ViewBuilder var connectionBar: () -> ConnectionView
    var onRetryMachine: () -> Void = {}
    var onRecover: () -> Void = {}
    var onRetryMutation: () -> Void = {}
    var onAbandonMutation: () -> Void = {}
    var onShowLatestResult: () -> Void = {}
    var onOpenSession: (String, String, UInt64) -> Void = { _, _, _ in }
    var onToggleQuietGroup: (String) -> Void = { _ in }
    var onShowAllRuns: () -> Void = {}
    var onEditMessage: () -> Void = {}
    var onEditTiming: () -> Void = {}
    var onSetNotify: (Bool) -> Void = { _ in }
    var onEditTarget: () -> Void = {}
    var onEditMode: () -> Void = {}
    var onRename: () -> Void = {}

    var body: some View {
        Page(gray: true) {
            VStack(spacing: 0) {
                connectionBar()
                ScrollViewReader { proxy in
                    ScrollView {
                        if vm.found { taskBody }
                        else if vm.loading { Text(L10n.scheduleIosLoadingTasks()).font(.body).ink(DS.ink2).padding(32) }
                        else { Text(vm.hasNotFoundText ? vm.notFoundText.rendered() : L10n.scheduleIosTaskNotFound()).font(.body).ink(DS.ink2).padding(32) }
                        Color.clear.frame(height: 1).id("schedule-detail-end")
                    }
                    .debugSignal("schedule.detail.end") { proxy.scrollTo("schedule-detail-end", anchor: .bottom) }
                }
            }
        }
    }

    @ViewBuilder private var taskBody: some View {
        let a = vm.actions
        VStack(alignment: .leading, spacing: 0) {
            if vm.hasMachineError {
                Banner(vm.machineError.rendered(), tone: .error, action: vm.canRetryMachineList ? L10n.commonRetry() : nil,
                       onAction: vm.canRetryMachineList ? onRetryMachine : nil).padding(.horizontal, 16).padding(.top, 12)
            }
            if vm.readOnly && vm.hasReadOnlyNotice { Banner(vm.readOnlyNotice.rendered()).padding(.horizontal, 16).padding(.top, 12) }
            if vm.attention.show {
                ScheduleRecoveryNotice(text: vm.attention.message.rendered(), recovery: vm.attention.showPrimaryAction ? vm.attention.primaryActionLabel.rendered() : nil,
                                       enabled: vm.attention.primaryActionEnabled, action: onRecover)
            }
            ScheduleMutationBanner(mutation: vm.mutation, onRetry: onRetryMutation, onAbandon: onAbandonMutation).padding(.horizontal, 16).padding(.top, 12)
            if vm.resultCard.hasResult_p && vm.resultCard.hasNewerResult_p { TextAction(L10n.homeIosNewResults(), action: onShowLatestResult).padding(.horizontal, 16) }
            if vm.activeRun.hasActive_p {
                HStack(spacing: 6) {
                    SymbolImage(Mark.progress, style: .subheadline, color: DS.blue)
                    Text(vm.activeRun.statusText.rendered()).font(.subheadline).ink(DS.blue)
                    Spacer(minLength: 0)
                    if vm.activeRun.canOpenSession && !vm.activeRun.sessionID.isEmpty {
                        TextAction(L10n.scheduleConversation(), arrow: true) { onOpenSession(vm.activeRun.sessionID, vm.activeRun.runID, vm.activeRun.firstSeq) }
                    }
                }.padding(16).background(DS.gbg, in: RoundedRectangle(cornerRadius: DS.Radius.group)).padding(.horizontal, 16).padding(.top, 16)
            }
            if vm.resultCard.hasResult_p {
                ScheduleResultCard(card: vm.resultCard) { onOpenSession(vm.resultCard.sessionID, vm.resultCard.runID, vm.resultCard.firstSeq) }.id(vm.resultCard.runID)
            }
            if !vm.earlierGroups.isEmpty {
                HStack { Text(L10n.scheduleEarlier()).font(.subheadline).ink(DS.ink2); Spacer(); Text(vm.earlierCountText.rendered()).font(.subheadline).ink(DS.ink2) }
                    .padding(.horizontal, 30).padding(.top, 20).padding(.bottom, 6)
                historySection
            } else if vm.emptyHistory {
                GroupCard { GRow(vm.hasEmptyHistoryText ? vm.emptyHistoryText.rendered() : L10n.scheduleHasNotRunYet()) }.padding(.top, 16)
            }
            GroupCard {
                VStack(alignment: .leading, spacing: 0) {
                    HStack {
                        Text(L10n.scheduleWhatShouldTheAgentDo()).font(.subheadline.weight(.semibold)).ink(DS.ink); Spacer()
                        TextAction(L10n.toolsIosEdit(), enabled: a.canEditDefinition, action: onEditMessage)
                    }.padding(.horizontal, 14)
                    Text(vm.message).font(.body).ink(DS.ink).lineLimit(3).frame(maxWidth: .infinity, alignment: .leading).padding(14)
                }
            }.padding(.top, 16).padding(.bottom, 16)
            GroupLabel(L10n.commonSettings())
            GroupCard {
                GRow(L10n.scheduleWhen(), value: vm.timingText.rendered(), chevron: a.canEditDefinition, onTap: a.canEditDefinition ? onEditTiming : nil)
                ToggleRow(L10n.scheduleNotifyOnCompletion(), isOn: Binding(get: { vm.notifyOnFinish }, set: { onSetNotify($0) })).disabled(!a.canToggleNotify)
                if vm.hasLimits_p { GRow(L10n.sessionEnd(), value: vm.limitsText.rendered(), chevron: a.canEditDefinition, onTap: a.canEditDefinition ? onEditTiming : nil) }
                let targetEditable = vm.targetType == .newSession ? a.canEditDefinition : (vm.machineOnline && !vm.targetSessionID.isEmpty)
                GRow(vm.targetLabel.rendered(), value: vm.targetValue.rendered(), chevron: targetEditable, onTap: targetEditable ? {
                    if vm.targetType == .newSession { onEditTarget() } else if vm.canOpenTargetSession && !vm.targetSessionID.isEmpty { onOpenSession(vm.targetSessionID, "", 0) }
                } : nil)
                let permEditable = vm.canEditPermissions && a.canEditDefinition
                GRow(L10n.newSessionPermissions(), value: vm.permissionsText.rendered(), chevron: permEditable,
                     valueColor: vm.permissionsIsDanger ? DS.red : nil, onTap: permEditable ? onEditMode : nil)
                GRow(L10n.scheduleTaskName(), value: vm.taskName, chevron: a.canRename, valueLines: 1, onTap: a.canRename ? onRename : nil)
            }
            if vm.hasCreationFooter { Hint(vm.creationFooter.rendered()).padding(.horizontal, 16).padding(.bottom, 8) }
            if vm.hasRunOnceFooter { Hint(vm.runOnceFooter.rendered()).padding(.horizontal, 16).padding(.bottom, 16) }
        }
    }

    private var historySection: some View {
        GroupCard {
            ForEach(vm.earlierGroups, id: \.groupKey) { group in
                if group.foldedQuiet && group.runs.count > 1 {
                    GRow(group.foldedTitle.rendered(), sub: group.hasFoldedSummary ? group.foldedSummary.rendered() : nil, chevron: false) {
                        onToggleQuietGroup(group.groupKey)
                    }.trailing { Arrow(DS.ink2, direction: group.expanded ? .up : .down) }
                    if group.expanded { ForEach(group.runs, id: \.runID) { runRow($0) } }
                } else { ForEach(group.runs, id: \.runID) { runRow($0) } }
            }
            if !vm.showAllRuns && vm.hasMoreEarlierRuns_p {
                GRow(vm.hasViewAllText ? vm.viewAllText.rendered() : L10n.scheduleViewAllRuns(count: Int(vm.totalEarlierRuns)), action: true, onTap: onShowAllRuns)
            }
        }
    }

    private func runRow(_ run: PBScheduleRunRowView) -> some View {
        let canOpen = run.canOpenSession && !run.sessionID.isEmpty
        return Button { if canOpen { onOpenSession(run.sessionID, run.runID, run.firstSeq) } } label: {
            HStack(alignment: .top, spacing: 0) {
                ZStack { if run.unreadOnEntry { Circle().fill(DS.blue).frame(width: 8, height: 8).accessibilityLabel(L10n.scheduleUnreadOnEntry()) } }.frame(width: 22, height: 20)
                VStack(alignment: .leading, spacing: 4) {
                    Text(run.timeText.rendered()).font(.subheadline).ink(DS.ink2)
                    Text(run.summaryText.rendered()).font(.body).ink(DS.ink).lineLimit(2).frame(maxWidth: .infinity, alignment: .leading)
                }
                if canOpen { Arrow(DS.ink2).padding(.leading, 8).padding(.top, 12) }
            }.padding(.horizontal, 14).padding(.vertical, 12).contentShape(Rectangle())
        }.buttonStyle(RowPressStyle()).disabled(!canOpen).accessibilityElement(children: .combine)
    }
}

private struct ScheduleRenameDialog: ViewModifier {
    @Binding var isPresented: Bool
    let initialTitle: String
    let mutation: PBScheduleMutationView
    let enabled: Bool
    let onSave: (String) -> Void
    @State private var value = ""
    @State private var submitted = false
    @State private var sawBusy = false

    func body(content: Content) -> some View {
        let busy = mutation.busy, hasError = mutation.hasError
        content.modifier(OverlayCover(isPresented: $isPresented, alignment: .center, canDismiss: { !busy }) {
            DialogCard {
                Text(L10n.scheduleRenameTask()).font(.headline).ink(DS.ink).multilineTextAlignment(.center).padding(.bottom, 12).accessibilityAddTraits(.isHeader)
                InputField(text: $value, placeholder: L10n.scheduleLeaveBlankToUseTheMessageSFirstSentence(), lines: 1...1, focusOnAppear: true, onSubmit: { submit() })
                    .onChange(of: value) { _, new in let s = String(new.map { $0 == "\n" || $0 == "\r" ? " " : $0 }.prefix(60)); if s != new { value = s } }
                    .padding(.bottom, 8)
                if hasError {
                    Text(L10n.commonIosSaveFailed(reason: mutation.error.rendered())).font(.subheadline).ink(DS.red).multilineTextAlignment(.center).padding(.bottom, 8)
                } else {
                    Text(L10n.scheduleSavingBlankUsesTheMessageSFirstSentence()).font(.subheadline).ink(DS.ink2).multilineTextAlignment(.center).padding(.bottom, 8)
                }
                Btns {
                    Btn(L10n.commonCancel(), enabled: !busy) { isPresented = false }
                    Btn(busy ? L10n.commonIosSaving() : L10n.commonSave(), kind: .primary, enabled: enabled && !busy && !mutation.frozen, action: submit)
                }.padding(.top, 6)
            }
        })
        .onChange(of: isPresented) { _, open in if open { value = initialTitle; submitted = false; sawBusy = false } }
        .onChange(of: busy) { _, nowBusy in if nowBusy { sawBusy = true }; checkAutoClose() }
        .onChange(of: hasError) { _, _ in checkAutoClose() }
        .onChange(of: mutation.frozen) { _, _ in checkAutoClose() }
    }

    private func submit() {
        guard enabled && !mutation.busy && !mutation.frozen else { return }
        if value == initialTitle { isPresented = false; return }
        submitted = true; sawBusy = false; onSave(value)
    }
    private func checkAutoClose() {
        guard submitted, !mutation.busy && (sawBusy || (!mutation.hasError && !mutation.frozen)) else { return }
        submitted = false
        if !mutation.hasError && !mutation.frozen { Haptics.confirm(); isPresented = false }
    }
}

struct ScheduleRecoveryNotice: View {
    let text: String
    let recovery: String?
    let enabled: Bool
    let action: () -> Void
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(text).font(.subheadline).ink(DS.orangeInk).frame(maxWidth: .infinity, alignment: .leading).padding(16)
                .background(DS.gbg, in: RoundedRectangle(cornerRadius: DS.Radius.small))
            if let recovery { Btn(recovery, kind: .primary, enabled: enabled, action: action) }
        }.padding(.horizontal, 16).padding(.top, 16).padding(.bottom, 12)
    }
}
