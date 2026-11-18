import RACore
import RAProto
import SwiftUI

private enum NewScheduleSheet: String, Identifiable { case timing, project, mode, model, machineAll; var id: String { rawValue } }

struct NewScheduleScreen: View {
    let sessionID: String
    var onClose: (() -> Void)? = nil
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @Environment(\.page) private var page
    @State private var sheet: NewScheduleSheet?
    @State private var confirmFullAccess = false

    private var vm: PBScheduleFormViewModel { context.core.scheduleForm }
    private var formKey: String { vm.formKey }
    private func close() {
        context.dispatch { $0.scheduleFormClose = .with { $0.formKey = formKey; $0.value = true } }
        if let onClose { onClose() } else { nav.back(from: page) }
    }

    var body: some View {
        NewScheduleContent(
            vm: vm,
            onSetMessage: { t in context.dispatch { $0.scheduleFormSetMessage = .with { $0.formKey = formKey; $0.value = t } } },
            onSetTitle: { t in context.dispatch { $0.scheduleFormSetTitle = .with { $0.formKey = formKey; $0.value = t } } },
            onSetNotify: { on in context.dispatch { $0.scheduleFormSetNotify = .with { $0.formKey = formKey; $0.value = on } } },
            onSelectAgent: { ag in context.dispatch { $0.scheduleFormSelectAgent = .with { $0.formKey = formKey; $0.agent = ag } } },
            onOpenTiming: { sheet = .timing }, onOpenProject: { sheet = .project },
            onOpenMode: { sheet = .mode }, onOpenModels: { sheet = vm.machineChoices.count > 1 ? .machineAll : .model },
            onRetryMutation: { context.dispatch { $0.scheduleRetryMutation = .with { $0.formKey = formKey } } },
            onAbandonMutation: { context.dispatch { $0.scheduleAbandonMutation = .with { $0.formKey = formKey } } },
            onClose: close,
            onCreate: {
                if vm.requiresFullAccessConfirm { confirmFullAccess = true }
                else { context.dispatch { $0.scheduleFormSubmit = .with { $0.formKey = formKey; $0.value = false } } }
            }
        )
        .confirm(isPresented: $confirmFullAccess, title: L10n.scheduleRunWithFullAccess(),
                 text: L10n.scheduleNoApprovalIsRequiredForAnyOperationWheneverThis(),
                 confirm: L10n.modesFullAccess(), danger: true) {
            context.dispatch { $0.scheduleFormSubmit = .with { $0.formKey = formKey; $0.value = true } }
        }
        .kitSheet(item: $sheet, size: .large) { which in
            switch which {
            case .timing: ScheduleTimingSheet(submitOnApply: false, onClose: { sheet = nil })
            case .project: ScheduleConfigSheet(focus: .project, showSaveButton: false, onClose: { sheet = nil })
            case .mode: ScheduleConfigSheet(focus: .mode, showSaveButton: false, onClose: { sheet = nil })
            case .model: ScheduleConfigSheet(focus: .model, showSaveButton: false, onClose: { sheet = nil })
            case .machineAll: ScheduleConfigSheet(focus: .all, showSaveButton: false, onClose: { sheet = nil })
            }
        }
        .task(id: sessionID) {
            await context.coreHost.openScheduleForm(mode: sessionID.isEmpty ? .createNew : .createForSession, sessionID: sessionID)
            #if DEBUG
            if HomeLaunch.value("-schedulePanel")?.hasPrefix("timing-") == true { sheet = .timing }
            if URL(string: context.account.server)?.host == "127.0.0.1", let cwd = HomeLaunch.value("-scheduleCwd") {
                let targetKey = context.core.scheduleForm.formKey
                let machineID = context.core.scheduleForm.machineID.isEmpty ? (context.core.scheduleForm.machineChoices.first?.machineID ?? "") : context.core.scheduleForm.machineID
                await context.coreHost.dispatch { @Sendable ev in ev.scheduleFormSelectProject = .with { $0.formKey = targetKey; $0.projectKey = cwd; if !machineID.isEmpty { $0.machineID = machineID } } }
                if let agentStr = HomeLaunch.value("-scheduleAgent") {
                    let ag: PBAgent = agentStr == "codex" ? .codex : agentStr == "antigravity" ? .antigravity : .claude
                    await context.coreHost.dispatch { @Sendable ev in ev.scheduleFormSelectAgent = .with { $0.formKey = targetKey; $0.agent = ag } }
                }
                let m = HomeLaunch.value("-scheduleModel") ?? context.core.scheduleForm.model
                let modeStr = HomeLaunch.value("-scheduleMode")
                let modeVal: PBApprovalMode? = modeStr == "full" ? .fullAccess : modeStr == "auto" ? .auto : modeStr == "ask" ? .ask : nil
                if HomeLaunch.value("-scheduleModel") != nil || modeVal != nil {
                    await context.coreHost.dispatch { @Sendable ev in ev.scheduleFormSelectModel = .with { $0.formKey = targetKey; $0.model = m; if let modeVal { $0.mode = modeVal } } }
                }
            }
            #endif
        }
        .debugSignal("schedule.create") {
            #if DEBUG
            let targetKey = context.core.scheduleForm.formKey
            let fallbackMachine = context.core.scheduleForm.machineID.isEmpty ? (context.core.scheduleForm.machineChoices.first?.machineID ?? "") : ""
            let message = HomeLaunch.value("-scheduleMessage"), name = HomeLaunch.value("-scheduleName")
            let atTiming: (date: String, time: String, zone: String)? = HomeLaunch.value("-scheduleAt").flatMap { raw in
                guard let parsed = ISO8601DateFormatter().date(from: raw) else { return nil }
                let tz = TimeZone.current, df = DateFormatter(), tf = DateFormatter()
                df.locale = Locale(identifier: "en_US_POSIX"); df.timeZone = tz; df.dateFormat = "yyyy-MM-dd"
                tf.locale = Locale(identifier: "en_US_POSIX"); tf.timeZone = tz; tf.dateFormat = "HH:mm"
                return (df.string(from: parsed), tf.string(from: parsed), tz.identifier)
            }
            let host = context.coreHost
            Task {
                if !fallbackMachine.isEmpty { await host.dispatch { @Sendable ev in ev.scheduleFormSelectMachine = .with { $0.formKey = targetKey; $0.value = fallbackMachine } } }
                if let message { await host.dispatch { @Sendable ev in ev.scheduleFormSetMessage = .with { $0.formKey = targetKey; $0.value = message } } }
                if let name { await host.dispatch { @Sendable ev in ev.scheduleFormSetTitle = .with { $0.formKey = targetKey; $0.value = name } } }
                if let atTiming {
                    await host.dispatch { @Sendable ev in
                        ev.scheduleFormUpdateTiming = .with { $0.formKey = targetKey; $0.kind = .once; $0.date = atTiming.date; $0.time = atTiming.time; $0.zone = atTiming.zone; $0.apply = true }
                    }
                }
                await host.dispatch { @Sendable ev in ev.scheduleFormSubmit = .with { $0.formKey = targetKey; $0.value = true } }
            }
            #endif
        }
    }
}

/// Pure presentation body for `NewScheduleScreen`, driven solely by `PBScheduleFormViewModel`.
struct NewScheduleContent: View {
    let vm: PBScheduleFormViewModel
    var onSetMessage: (String) -> Void = { _ in }
    var onSetTitle: (String) -> Void = { _ in }
    var onSetNotify: (Bool) -> Void = { _ in }
    var onSelectAgent: (PBAgent) -> Void = { _ in }
    var onOpenTiming: () -> Void = {}
    var onOpenProject: () -> Void = {}
    var onOpenMode: () -> Void = {}
    var onOpenModels: () -> Void = {}
    var onRetryMutation: () -> Void = {}
    var onAbandonMutation: () -> Void = {}
    var onClose: () -> Void = {}
    var onCreate: () -> Void = {}
    @State private var messageText = ""
    @State private var titleText = ""
    @State private var syncedFormKey = ""

    var body: some View {
        let busy = vm.mutation.busy
        Page(gray: true) {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    ForEach(Array(vm.notices.enumerated()), id: \.offset) { _, notice in
                        Banner(notice.text.rendered(), tone: notice.severity == .error ? .error : notice.severity == .warn ? .warn : .plain)
                    }
                    ScheduleMutationBanner(mutation: vm.mutation, onRetry: onRetryMutation, onAbandon: onAbandonMutation)
                    GroupCard {
                        TextField(L10n.scheduleWhatShouldTheAgentDo(), text: $messageText, axis: .vertical)
                            .lineLimit(3...8).padding(14).disabled(!vm.editable || busy)
                            .onChange(of: messageText) { _, new in if new != vm.message { onSetMessage(new) } }
                    }
                    if vm.messageTooLong && vm.hasMessageError { Hint(vm.messageError.rendered()) }
                    GroupCard {
                        GRow(L10n.scheduleWhen(), value: vm.timingSummary.rendered(), chevron: true) { if vm.editable && !busy { onOpenTiming() } }
                        ToggleRow(L10n.scheduleNotifyOnCompletion(), isOn: Binding(get: { vm.notifyOnFinish }, set: { onSetNotify($0) })).disabled(!vm.editable || busy)
                    }
                    if vm.mode == .createNew {
                        GroupCard {
                            GRow(L10n.scheduleProject(), value: vm.hasProject_p ? vm.projectName.rendered() : L10n.newSessionIosChooseProject(), chevron: true) {
                                if vm.editable && !busy { onOpenProject() }
                            }
                            ScheduleAgentRow(availableAgents: vm.availableAgents, selected: vm.agent, enabled: vm.editable && !busy, onSelect: onSelectAgent)
                            GRow(L10n.newSessionPermissions(), value: vm.hasApprovalModeLabel ? vm.approvalModeLabel.rendered() : L10n.newSessionFollowCli(),
                                 chevron: true, valueColor: vm.approvalModeIsDanger ? DS.red : nil) { if vm.editable && !busy { onOpenMode() } }
                            let modelValue = [vm.machineChoices.count > 1 ? vm.machineName : "", vm.hasModelLabel ? vm.modelLabel.rendered() : "", vm.effortLabel]
                                .filter { !$0.isEmpty }.joined(separator: " · ")
                            GRow(vm.machineChoices.count > 1 ? L10n.scheduleComputerAndModel() : L10n.modelsTitle(), value: modelValue, chevron: true) {
                                if vm.editable && !busy { onOpenModels() }
                            }
                        }
                    }
                    if vm.hasLocationText { Hint(vm.locationText.rendered()) }
                    GroupLabel(L10n.scheduleTaskName())
                    GroupCard {
                        let ph = vm.hasTitlePlaceholder ? vm.titlePlaceholder.rendered() : (vm.autoTitle.isEmpty ? L10n.scheduleTaskName() : vm.autoTitle)
                        TextField(ph, text: $titleText).padding(.horizontal, 14).padding(.vertical, 12).disabled(!vm.editable || busy)
                            .onChange(of: titleText) { _, new in let f = String(new.prefix(60)); if f != new { titleText = f; return }; if f != vm.title { onSetTitle(f) } }
                    }
                }.padding(.horizontal, 16).padding(.vertical, 12)
            }
        }
        .navigationTitle(L10n.homeNewSchedule()).navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .topBarLeading) {
                Button(action: onClose) { Image(systemName: "xmark") }.tint(DS.ink).accessibilityLabel(L10n.commonClose())
            }
            ToolbarItem(placement: .topBarTrailing) {
                Button(action: onCreate) {
                    Text(busy ? L10n.scheduleCreating() : vm.mutation.frozen ? L10n.commonRetry() : vm.hasSubmitLabel ? vm.submitLabel.rendered() : L10n.scheduleCreate())
                        .font(.body.weight(.semibold))
                }.buttonStyle(.borderedProminent).tint(DS.btn).foregroundStyle(DS.btnFg).disabled(!vm.canSubmit || !vm.editable || busy || vm.mutation.expired)
            }
        }
        .onChange(of: vm.formKey, initial: true) { _, key in
            if !key.isEmpty && key != syncedFormKey { syncedFormKey = key; messageText = vm.message; titleText = vm.title }
        }
        .onChange(of: vm.message) { _, new in if messageText != new { messageText = new } }
        .onChange(of: vm.title) { _, new in if titleText != new { titleText = new } }
    }
}
