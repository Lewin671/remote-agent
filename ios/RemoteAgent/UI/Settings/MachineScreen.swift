import RACore
import RAProto
import SwiftUI

/// Where a link into this page scrolls to.
enum MachineAnchor: Hashable { case storage }

/// A computer's details and daemon settings: renders Core's `PBMachineSettingsViewModel` directly.
struct MachineScreen: View {
    let id: String
    /// Opened from the disk card or 「remote-agent 的占用」 of 电脑状态: scrolled to 附件存储.
    var scrollToStorage = false
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav

    var body: some View {
        let host = context.coreHost
        let watchKey = "\(id):\(model.foreground)"
        let current = context.core.machineSettings
        let online = current.machineID == id ? current.online : (model.state.machines[id]?.pathState.reachable ?? false)
        let settingsKey = "\(id):\(online):\(model.state.conn == .online)"
        contentView
            .task(id: settingsKey) { await host.openMachineSettings(machineID: id) }
            .task(id: watchKey) {
                guard model.foreground else { return }
                let token = await host.acquireMachineStatusWatch(id, mode: .overview)
                while !Task.isCancelled {
                    try? await Task.sleep(for: .seconds(3600))
                }
                await host.releaseMachineStatusWatch(token)
            }
    }

    private var contentView: some View {
        let current = context.core.machineSettings
        let vm = current.machineID == id ? current : PBMachineSettingsViewModel()
        let host = context.coreHost
        let refreshAction: (@MainActor @Sendable () async -> Void)? = if vm.editable {
            { @Sendable in await host.openMachineSettings(machineID: id) }
        } else {
            nil
        }
        let toggleDirect: (Bool) -> Void = { enabled in
            context.dispatch {
                $0.updateDirectPreference = .with {
                    $0.machineID = id
                    $0.enabled = enabled
                }
            }
        }
        let selectAgent: (PBAgent) -> Void = { agent in
            context.dispatch {
                $0.machineSettingsSelectAgent = .with {
                    $0.machineID = id
                    $0.agent = agent
                }
            }
        }
        let updateDefaults: (PBAgent, String?, String?, PBApprovalMode?) -> Void = { agent, modelID, effort, mode in
            context.dispatch {
                $0.updateMachineAgentDefaults = .with {
                    $0.machineID = id
                    $0.agent = agent
                    if let modelID { $0.model = modelID }
                    if let effort { $0.effort = effort }
                    if let mode { $0.mode = mode }
                }
            }
        }
        let updateDaemon: (Int32?, Int32?, Int64?, Bool) -> Void = { timeoutSeconds, idleCloseMinutes, imageQuotaBytes, cleanImages in
            context.dispatch {
                $0.updateMachineDaemonSettings = .with {
                    $0.machineID = id
                    if let timeoutSeconds { $0.approvalTimeoutSeconds = timeoutSeconds }
                    if let idleCloseMinutes { $0.idleCloseMinutes = idleCloseMinutes }
                    if let imageQuotaBytes { $0.imageUploadQuotaBytes = imageQuotaBytes }
                    $0.cleanImages = cleanImages
                }
            }
        }
        return MachineScreenContent<DeviceManagementActions>(
            vm: vm,
            scrollToStorage: scrollToStorage,
            onOpenStatus: { nav.open(.machineStatus(id: id)) },
            onOpenQuota: { nav.open(.quota(machine: id)) },
            onToggleDirect: toggleDirect,
            onRetryDirect: { context.remote.refreshDirect(id) },
            onRetrySettings: { Task { await host.openMachineSettings(machineID: id) } },
            onRefresh: refreshAction,
            onSelectAgent: selectAgent,
            onUpdateAgentDefaults: updateDefaults,
            onUpdateDaemonSettings: updateDaemon,
            onOpenProject: { key in nav.open(.project(key: key)) },
            managementSection: { DeviceManagementActions(id: id, machine: true) }
        )
    }
}

/// Pure presentation body for `PBMachineSettingsViewModel`, usable directly by golden snapshot tests.
struct MachineScreenContent<Management: View>: View {
    let vm: PBMachineSettingsViewModel
    var scrollToStorage = false
    let onOpenStatus: () -> Void
    let onOpenQuota: () -> Void
    let onToggleDirect: (Bool) -> Void
    let onRetryDirect: () -> Void
    let onRetrySettings: () -> Void
    let onRefresh: (@MainActor @Sendable () async -> Void)?
    let onSelectAgent: (PBAgent) -> Void
    let onUpdateAgentDefaults: (PBAgent, _ model: String?, _ effort: String?, _ mode: PBApprovalMode?) -> Void
    let onUpdateDaemonSettings: (_ timeoutSeconds: Int32?, _ idleCloseMinutes: Int32?, _ imageQuotaBytes: Int64?, _ cleanImages: Bool) -> Void
    let onOpenProject: (String) -> Void
    @ViewBuilder let managementSection: () -> Management

    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var picking: Pick?
    @State private var cleaning = false

    private enum Pick: Identifiable, Hashable {
        case model(PBAgent)
        case effort(PBAgent)
        case mode(PBAgent)
        case timeout, idleClose, imageQuota

        var id: String {
            switch self {
            case .model(let agent): "model-\(agent.rawValue)"
            case .effort(let agent): "effort-\(agent.rawValue)"
            case .mode(let agent): "mode-\(agent.rawValue)"
            case .timeout: "timeout"
            case .idleClose: "idle-close"
            case .imageQuota: "image-quota"
            }
        }
    }

    var body: some View {
        let title = vm.found ? vm.name.or(vm.machineID) : L10n.settingsComputer()
        let scrollReady = vm.hasAttachmentStorage && vm.attachmentStorage.loaded
        SettingsPage(
            refresh: onRefresh,
            scrollTo: scrollToStorage ? (id: MachineAnchor.storage, ready: scrollReady) : nil
        ) {
            if vm.found {
                details(vm)
            } else {
                PageNote(L10n.settingsIosThisComputerIsNotInTheCurrentTrustedDirectory())
            }
        }
        .navBar(
            title,
            subtitle: vm.found ? (vm.online ? L10n.commonIosOnline() : L10n.commonIosOffline()) : nil,
            dot: vm.found ? (vm.online ? DS.green : DS.ink3) : nil
        )
        .kitSheet(item: $picking) { pick in sheet(pick) }
        .debugSignal("machine.timeout") {
            let choices = vm.approvalTimeoutChoices
            guard !choices.isEmpty else { return }
            let idx = choices.firstIndex(where: \.selected) ?? -1
            let next = choices[(idx + 1) % choices.count]
            onUpdateDaemonSettings(next.seconds, nil, nil, false)
        }
        .confirm(
            isPresented: $cleaning,
            title: L10n.settingsIosClearReclaimableImages(),
            text: L10n.settingsIosClearsReclaimableOriginalsAndCacheUnsyncedFullSizeImages(),
            confirm: L10n.settingsClearSettings()
        ) {
            onUpdateDaemonSettings(nil, nil, nil, true)
        }
    }

    @ViewBuilder private func details(_ vm: PBMachineSettingsViewModel) -> some View {
        let name = vm.name.or(vm.machineID)
        let removed = vm.removedBanner.rendered()
        let banner = vm.offlineOrMismatchBanner.rendered()
        if !removed.isEmpty {
            GroupBanner(removed, tone: .error)
        } else if !banner.isEmpty {
            GroupBanner(banner, tone: vm.protocolMismatch != 0 ? .warn : .plain)
        }

        if vm.showStatusEntry {
            MachineRingsCard(
                rings: vm.statusRings,
                online: vm.online && vm.member && vm.protocolMismatch == 0,
                onOpen: onOpenStatus
            )
        }

        GroupLabel(L10n.settingsComputer())
        GroupCard {
            GRow(L10n.settingsName(), value: name)
            GRow(L10n.settingsDeviceId(), value: "…" + String(vm.machineID.suffix(8)))
        }

        if !vm.quotaRows.isEmpty {
            GroupLabel(L10n.settingsAccountQuota())
            GroupCard {
                ForEach(Array(vm.quotaRows.enumerated()), id: \.offset) { _, sec in
                    let summary = sec.hasSummaryRow ? sec.summaryRow : nil
                    let rem = summary?.remainingText.rendered() ?? ""
                    let win = summary?.label.rendered() ?? ""
                    let fallback = sec.emptyOrErrorText.rendered()
                    if !rem.isEmpty && !win.isEmpty {
                        let warn = summary?.tone == .warn || summary?.tone == .full
                        GRow(sec.agentName, value: "· " + win, chevron: true, valueLines: 1) {
                            onOpenQuota()
                        }
                        .valueLead {
                            Text(rem)
                                .font(.subheadline)
                                .ink(warn ? DS.orangeInk : DS.ink2)
                                .lineLimit(typeSize.isAccessibilitySize ? nil : 1)
                        }
                    } else {
                        GRow(sec.agentName, value: rem.isEmpty ? fallback : rem, chevron: true) {
                            onOpenQuota()
                        }
                    }
                }
            }
        }

        if vm.member && vm.hasConnection && vm.connection.toggleVisible {
            ConnectionGroup(view: vm.connection, onToggle: onToggleDirect, onRetryDirect: onRetryDirect)
        }

        if !vm.cliVersions.isEmpty {
            GroupLabel(L10n.settingsIosCliVersions())
            GroupCard {
                ForEach(Array(vm.cliVersions.enumerated()), id: \.offset) { _, a in
                    GRow(a.name, value: a.version.or(L10n.settingsIosUnknown()))
                        .valueLead {
                            Text(a.verified ? L10n.settingsIosVerified() : L10n.settingsIosUnverified())
                                .font(.subheadline)
                                .ink(a.verified ? DS.ink2 : DS.orangeInk)
                        }
                }
            }
            if vm.hasUnverifiedCli_p {
                GroupNote(L10n.settingsIosForUnverifiedVersionsRunRemoteAgentDoctorOnThe())
            }
        }

        daemonSettings(vm, name: name)

        if !vm.projects.isEmpty {
            GroupLabel(L10n.scheduleProject())
            GroupCard {
                ForEach(Array(vm.projects.enumerated()), id: \.offset) { _, p in
                    GRow(p.name, value: shortPath(p.path), chevron: true) {
                        onOpenProject(p.projectKey)
                    }
                }
            }
        }

        managementSection()
    }

    @ViewBuilder private func daemonSettings(_ vm: PBMachineSettingsViewModel, name: String) -> some View {
        let readError = vm.settingsError.rendered()
        let saveErr = vm.saveError.rendered()

        if !vm.editable {
            if vm.protocolMismatch == 0 && !vm.online {
                GroupNote(L10n.settingsIosIsOfflineConnectToReadAndEditNewSession(value: name))
            } else if vm.protocolMismatch == 0 && vm.unlinked {
                GroupNote(L10n.settingsIosConnectToTheServerToReadAndEditNew())
            }
        } else if vm.settingsLoading {
            GroupLabel(L10n.settingsNewProjectDefaults())
            LoadingRow(inset: 30).padding(.bottom, 4)
        } else if !readError.isEmpty && !vm.hasAgentDefaults {
            GroupLabel(L10n.settingsNewProjectDefaults())
            GroupBanner(readError, tone: .error, action: L10n.commonRetry(), onAction: onRetrySettings)
        } else if vm.hasAgentDefaults {
            let busy = vm.savePending || !vm.editable
            let agents = vm.availableAgents
            let d = vm.agentDefaults
            let activeAgent = agents.contains(vm.selectedAgent) ? vm.selectedAgent : (agents.first ?? d.agent)

            if agents.count > 1 {
                GroupLabel(L10n.settingsNewProjectDefaults())
                SegPick(
                    agents.map(agentName),
                    selected: Binding(
                        get: { agents.firstIndex(of: activeAgent) ?? 0 },
                        set: { onSelectAgent(agents[$0]) }
                    )
                )
                .padding(.horizontal, DS.Space.l)
            } else {
                GroupLabel(L10n.settingsNewProjectDefaultsSettings(value: d.agentName.or(agentName(activeAgent))))
            }

            let modelValue = d.selectedModelLabel.rendered().or(d.selectedModel).or(L10n.modelsDefault())
            let selectedEffort = d.effortChoices.first(where: \.selected)
            let effortValue = selectedEffort?.label.rendered().or(d.selectedEffort) ?? d.selectedEffort.or(L10n.modelsDefault())
            let selectedMode = d.modeChoices.first(where: \.selected)
            let modeValue = selectedMode?.label.rendered() ?? ""
            let timeoutValue = vm.approvalTimeoutChoices.first(where: \.selected)?.label.rendered()
                ?? L10n.quotaMin(count: Int(vm.selectedApprovalTimeoutSeconds / 60))
            let idleValue = vm.idleCloseChoices.first(where: \.selected)?.label.rendered() ?? ""

            GroupCard {
                GRow(L10n.modelsTitle(), value: modelValue, chevron: true, enabled: !busy) {
                    picking = .model(activeAgent)
                }
                GRow(L10n.modelsEffort(), value: effortValue, chevron: true, enabled: !busy) {
                    picking = d.effortChoices.count > 1 ? .effort(activeAgent) : .model(activeAgent)
                }
                GRow(
                    L10n.modesTitle(),
                    value: modeValue,
                    chevron: true,
                    enabled: !busy,
                    valueColor: d.selectedMode == .fullAccess ? DS.red : nil
                ) {
                    picking = .mode(activeAgent)
                }
            }

            GroupCard {
                GRow(L10n.settingsApprovalTimeout(), value: timeoutValue, chevron: true, enabled: !busy) {
                    picking = .timeout
                }
                GRow(L10n.settingsAutomaticallyEndIdleSessions(), value: idleValue, chevron: true, enabled: !busy) {
                    picking = .idleClose
                }
            }

            if vm.savePending {
                LoadingRow(L10n.settingsIosSaving(), inset: 30).padding(.bottom, 4)
            }
            if vm.saveUnknown {
                GroupBanner(
                    saveErr.or(L10n.settingsIosUnsureWhetherTheComputerSavedThisChangeReloadTo()),
                    tone: .warn,
                    action: L10n.settingsIosReload(),
                    onAction: onRetrySettings
                )
            } else if !saveErr.isEmpty {
                GroupBanner(saveErr, tone: .error)
            }
            if !readError.isEmpty {
                GroupBanner(readError, tone: .error, action: L10n.commonRetry(), onAction: onRetrySettings)
            }

            if vm.hasAttachmentStorage && vm.attachmentStorage.loaded {
                attachmentStorage(vm.attachmentStorage, enabled: !busy)
            }
        }
    }

    @ViewBuilder private func attachmentStorage(_ s: PBAttachmentStorageView, enabled: Bool) -> some View {
        let quotaLabel = s.quotaChoices.first(where: \.selected)?.label.rendered()
            ?? wholeBytes(s.selectedQuotaBytes)
        let uploaded = s.uploadedText.rendered()
        let previewCache = s.previewCacheText.rendered()
        let clearLabel = s.clearButtonLabel_p.rendered().or(L10n.settingsNothingToClear())
        let footer = s.footerText.rendered().or(L10n.settingsIosImagesAreStoredOnThisComputerImagesBeingSent())

        GroupLabel(L10n.settingsAttachmentStorage()).id(MachineAnchor.storage)
        GroupCard {
            GRow(L10n.settingsUploadedImagesAndFiles(), value: uploaded, valueLines: nil)
            GRow(L10n.settingsStorageLimit(), value: quotaLabel, chevron: true, enabled: enabled) {
                picking = .imageQuota
            }
            GRow(L10n.settingsPreviewCache(), value: previewCache.or("—"))
            if s.showEmergencyReserve {
                EmergencyReserveRow(text: s.emergencyReserveText.rendered())
            }
            GRow(
                L10n.settingsClear(),
                value: clearLabel,
                action: true,
                enabled: enabled && s.canClear && !s.cleaning
            ) {
                cleaning = true
            }
        }
        GroupNote(footer)
    }

    @ViewBuilder private func sheet(_ pick: Pick) -> some View {
        let d = vm.agentDefaults
        switch pick {
        case .model(let agent):
            SheetHead(L10n.settingsIosDefaultModelForNewSessions(value: d.agentName.or(agentName(agent))))
            ForEach(Array(d.modelChoices.enumerated()), id: \.offset) { idx, c in
                ListItem(
                    c.label.rendered().or(c.id),
                    checked: c.selected,
                    small: c.desc.isEmpty ? nil : c.desc,
                    last: idx == d.modelChoices.count - 1
                ) {
                    picking = nil
                    onUpdateAgentDefaults(agent, c.id, nil, nil)
                }
            }
            let defaultFooter = d.footerText.rendered()
            if !defaultFooter.isEmpty { Hint(defaultFooter) }

        case .effort(let agent):
            SheetHead(L10n.modelsEffort())
            ForEach(Array(d.effortChoices.enumerated()), id: \.offset) { idx, c in
                ListItem(
                    c.label.rendered().or(c.id),
                    checked: c.selected,
                    last: idx == d.effortChoices.count - 1
                ) {
                    picking = nil
                    onUpdateAgentDefaults(agent, nil, c.id, nil)
                }
            }

        case .mode(let agent):
            SheetHead(L10n.settingsDefaultApprovalMode())
            ForEach(Array(d.modeChoices.enumerated()), id: \.offset) { idx, c in
                let disabledReason = c.disabledReason.rendered()
                ListItem(
                    c.label.rendered(),
                    checked: c.selected,
                    enabled: !c.disabled,
                    small: disabledReason.isEmpty ? nil : disabledReason,
                    last: idx == d.modeChoices.count - 1
                ) {
                    picking = nil
                    onUpdateAgentDefaults(agent, nil, nil, c.mode)
                }
            }
            let notice = d.autoUnavailableNotice.rendered()
            if !notice.isEmpty {
                Hint(notice)
            }

        case .timeout:
            SheetHead(L10n.settingsApprovalTimeout())
            ForEach(Array(vm.approvalTimeoutChoices.enumerated()), id: \.offset) { idx, c in
                ListItem(
                    c.label.rendered(),
                    checked: c.selected,
                    last: idx == vm.approvalTimeoutChoices.count - 1
                ) {
                    picking = nil
                    onUpdateDaemonSettings(c.seconds, nil, nil, false)
                }
            }
            Hint(L10n.settingsIosUnansweredApprovalsAreDeniedAfterThisTime())

        case .idleClose:
            SheetHead(L10n.settingsAutomaticallyEndIdleSessions())
            ForEach(Array(vm.idleCloseChoices.enumerated()), id: \.offset) { idx, c in
                ListItem(
                    c.label.rendered(),
                    checked: c.selected,
                    last: idx == vm.idleCloseChoices.count - 1
                ) {
                    picking = nil
                    onUpdateDaemonSettings(nil, c.minutes, nil, false)
                }
            }
            Hint(L10n.settingsIosSessionsEndAutomaticallyAfterThisMuchInactivityNotRunning())

        case .imageQuota:
            let choices = vm.attachmentStorage.quotaChoices
            SheetHead(L10n.settingsStorageLimit())
            ForEach(Array(choices.enumerated()), id: \.offset) { idx, c in
                ListItem(
                    c.label.rendered().or(wholeBytes(c.bytes)),
                    checked: c.selected,
                    last: idx == choices.count - 1
                ) {
                    picking = nil
                    onUpdateDaemonSettings(nil, nil, c.bytes, false)
                }
            }
        }
    }
}

/// 应急预留 in 附件存储: its size and why it is there; no action.
private struct EmergencyReserveRow: View {
    let text: String

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(alignment: .firstTextBaseline, spacing: 10) {
                Text(L10n.machineStatusEmergencyReserve()).font(.body).foregroundStyle(DS.ink)
                Spacer(minLength: 0)
                if !text.isEmpty {
                    Text(text).font(.subheadline).foregroundStyle(DS.ink2)
                }
            }
            Text(L10n.machineStatusEmergencyReserveNote()).font(.footnote).foregroundStyle(DS.ink2)
        }
        .padding(.horizontal, 14).padding(.vertical, 11)
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .combine)
    }
}
