import RACore
import RAProto
import SwiftUI
import UIKit

/// A computer's redesigned details and settings screen:
/// Header + Module 0 (`电脑状态`) + 4 core modules (`额度与用量`, `编程 CLI`, `运行与存储`, `管理`)
/// + bottom danger card (`DeviceManagementActions`).
struct MachineScreen: View {
    let id: String
    var initialDirect = false
    var initialCliAgent: String? = nil
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav

    var body: some View {
        let host = context.coreHost
        let watchKey = "\(id):\(model.foreground)"
        let current = context.core.machineSettings
        let online = current.machineID == id ? current.online : false
        let settingsKey = "\(id):\(online):\(current.appOnline)"
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
        return MachineScreenContent<DeviceManagementActions>(
            vm: vm,
            initialDirect: initialDirect,
            initialCliAgent: initialCliAgent,
            directDiagnostics: context.core.directDiagnostics,
            onOpenStatus: { nav.open(.machineStatus(id: id)) },
            onOpenQuota: { nav.open(.quota(machine: id)) },
            onOpenUsage: { nav.open(.usage(machine: id)) },
            onOpenStorage: { nav.open(.machineStorage(id: id)) },
            onOpenProjects: { nav.open(.machineProjects(id: id)) },
            onToggleDirect: { enabled in
                context.dispatch {
                    $0.updateDirectPreference = .with {
                        $0.machineID = id
                        $0.enabled = enabled
                    }
                }
            },
            onRetryDirect: { Task { await host.retryDirectNow(machineID: id) } },
            onOpenDirectDiag: { Task { await host.openDirectDiagnostics(machineID: id) } },
            onCloseDirectDiag: { Task { await host.closeDirectDiagnostics() } },
            onRefreshDirectDiag: { Task { await host.refreshDirectDiagnostics() } },
            onRetrySettings: { Task { await host.openMachineSettings(machineID: id) } },
            onRefresh: refreshAction,
            onSelectAgent: { agent in
                context.dispatch {
                    $0.machineSettingsSelectAgent = .with {
                        $0.machineID = id
                        $0.agent = agent
                    }
                }
            },
            onToggleAgent: { agent, enabled in
                context.dispatch {
                    $0.toggleMachineAgent = .with {
                        $0.machineID = id
                        $0.agent = agent
                        $0.enabled = enabled
                    }
                }
            },
            onUpdateAgentDefaults: { agent, modelID, effort, mode in
                context.dispatch {
                    $0.updateMachineAgentDefaults = .with {
                        $0.machineID = id
                        $0.agent = agent
                        if let modelID { $0.model = modelID }
                        if let effort { $0.effort = effort }
                        if let mode { $0.mode = mode }
                    }
                }
            },
            onUpdateDaemonSettings: { timeoutSeconds, idleCloseMinutes, imageQuotaBytes, cleanImages in
                context.dispatch {
                    $0.updateMachineDaemonSettings = .with {
                        $0.machineID = id
                        if let timeoutSeconds { $0.approvalTimeoutSeconds = timeoutSeconds }
                        if let idleCloseMinutes { $0.idleCloseMinutes = idleCloseMinutes }
                        if let imageQuotaBytes { $0.imageUploadQuotaBytes = imageQuotaBytes }
                        $0.cleanImages = cleanImages
                    }
                }
            },
            managementSection: { DeviceManagementActions(id: id, machine: true) }
        )
    }
}

/// Pure presentation body for `PBMachineSettingsViewModel`, usable directly by golden snapshot tests.
struct MachineScreenContent<Management: View>: View {
    let vm: PBMachineSettingsViewModel
    var initialDirect = false
    var initialCliAgent: String? = nil
    var directDiagnostics = PBDirectDiagnosticsViewModel()
    let onOpenStatus: () -> Void
    let onOpenQuota: () -> Void
    var onOpenUsage: () -> Void = {}
    var onOpenStorage: () -> Void = {}
    var onOpenProjects: () -> Void = {}
    let onToggleDirect: (Bool) -> Void
    let onRetryDirect: () -> Void
    var onOpenDirectDiag: () -> Void = {}
    var onCloseDirectDiag: () -> Void = {}
    var onRefreshDirectDiag: () -> Void = {}
    let onRetrySettings: () -> Void
    let onRefresh: (@MainActor @Sendable () async -> Void)?
    let onSelectAgent: (PBAgent) -> Void
    var onToggleAgent: (PBAgent, Bool) -> Void = { _, _ in }
    let onUpdateAgentDefaults: (PBAgent, _ model: String?, _ effort: String?, _ mode: PBApprovalMode?) -> Void
    let onUpdateDaemonSettings: (_ timeoutSeconds: Int32?, _ idleCloseMinutes: Int32?, _ imageQuotaBytes: Int64?, _ cleanImages: Bool) -> Void
    var onOpenProject: ((String) -> Void)? = nil
    @ViewBuilder let managementSection: () -> Management

    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var cliSheetAgent: CliSheetAgent?
    @State private var directSheetOpen = false
    @State private var daemonPick: DaemonPick?
    @State private var handledInitialDeepLink = false

    private struct CliSheetAgent: Identifiable, Hashable {
        let agent: PBAgent
        var id: Int { agent.rawValue }
    }

    private enum DaemonPick: Identifiable, Hashable {
        case timeout, idleClose
        var id: String {
            switch self {
            case .timeout: "timeout"
            case .idleClose: "idle-close"
            }
        }
    }

    var body: some View {
        let title = vm.found ? vm.name.or(vm.machineID) : L10n.settingsComputer()
        let subtitle: String? = if vm.found {
            vm.headerSubtitle.rendered().or(vm.online ? L10n.commonIosOnline() : L10n.commonIosOffline())
        } else {
            nil
        }
        let dotColor: Color? = if vm.found {
            statusDotColor(vm.headerDot, fallbackOnline: vm.online)
        } else {
            nil
        }

        SettingsPage(refresh: onRefresh) {
            if vm.found {
                details(vm)
            } else {
                PageNote(L10n.settingsIosThisComputerIsNotInTheCurrentTrustedDirectory())
            }
        }
        .navBar(title, subtitle: subtitle, dot: dotColor)
        .onChange(of: vm.found, initial: true) { _, found in
            tryConsumeInitialDeepLink(found: found, cliVersions: vm.cliVersions)
        }
        .onChange(of: vm.cliVersions.count, initial: true) { _, _ in
            tryConsumeInitialDeepLink(found: vm.found, cliVersions: vm.cliVersions)
        }
        .sheet(item: $cliSheetAgent) { item in
            CliSettingsSheet(
                vm: vm,
                agent: item.agent,
                onSelectAgent: onSelectAgent,
                onToggleAgent: onToggleAgent,
                onUpdateDefaults: onUpdateAgentDefaults,
                onRetrySettings: onRetrySettings,
                onClose: { cliSheetAgent = nil }
            )
        }
        .sheet(isPresented: $directSheetOpen) {
            DirectSettingsSheet(
                machineID: vm.machineID,
                machineName: vm.name.or(vm.machineID),
                view: vm.connection,
                diag: directDiagnostics,
                onToggle: onToggleDirect,
                onRetry: onRetryDirect,
                onOpenDiag: onOpenDirectDiag,
                onCloseDiag: onCloseDirectDiag,
                onRefreshDiag: onRefreshDirectDiag,
                onClose: { directSheetOpen = false }
            )
        }
        .kitSheet(item: $daemonPick) { pick in
            daemonPickerSheet(pick)
        }
        .debugSignal("machine.timeout") {
            let choices = vm.approvalTimeoutChoices
            guard !choices.isEmpty else { return }
            let idx = choices.firstIndex(where: \.selected) ?? -1
            let next = choices[(idx + 1) % choices.count]
            onUpdateDaemonSettings(next.seconds, nil, nil, false)
        }
    }

    private func tryConsumeInitialDeepLink(found: Bool, cliVersions: [PBCliVersionView]) {
        guard found, !handledInitialDeepLink else { return }
        if initialDirect {
            if vm.member && vm.hasConnection && vm.connection.toggleVisible {
                handledInitialDeepLink = true
                directSheetOpen = true
            }
            return
        }
        if let target = Self.parseAgentSlug(initialCliAgent) {
            guard !cliVersions.isEmpty else { return }
            handledInitialDeepLink = true
            if cliVersions.contains(where: { $0.agent == target }) {
                onSelectAgent(target)
                cliSheetAgent = CliSheetAgent(agent: target)
            }
            return
        }
        handledInitialDeepLink = true
    }

    static func parseAgentSlug(_ slug: String?) -> PBAgent? {
        guard let slug = slug?.trimmingCharacters(in: .whitespacesAndNewlines).lowercased(), !slug.isEmpty else {
            return nil
        }
        switch slug {
        case "codex": return .codex
        case "claude", "claude_code", "claudecode": return .claude
        case "antigravity": return .antigravity
        default:
            if let raw = Int(slug), let agent = PBAgent(rawValue: raw), agent != .unspecified {
                return agent
            }
            return nil
        }
    }

    static func agentSlug(_ agent: PBAgent) -> String? {
        switch agent {
        case .codex: "codex"
        case .claude: "claude"
        case .antigravity: "antigravity"
        default: nil
        }
    }

    private func statusDotColor(_ dot: PBMachineStatusDot, fallbackOnline: Bool) -> Color {
        switch dot {
        case .greenLive: DS.green
        case .orangeStatic: DS.orange
        case .redStatic: DS.red
        case .grayStatic: DS.ink3
        case .unspecified, .UNRECOGNIZED: fallbackOnline ? DS.green : DS.ink3
        }
    }

    // MARK: Sections

    @ViewBuilder private func details(_ vm: PBMachineSettingsViewModel) -> some View {
        let name = vm.name.or(vm.machineID)
        let removed = vm.removedBanner.rendered()
        let banner = vm.offlineOrMismatchBanner.rendered()
        if !removed.isEmpty {
            GroupBanner(removed, tone: .error)
        } else if !banner.isEmpty {
            GroupBanner(banner, tone: vm.protocolMismatch != 0 ? .warn : .plain)
        }

        // Module 0: 电脑状态 overview card
        if vm.showStatusEntry {
            let cardOnline = vm.online && vm.member && vm.protocolMismatch == 0
            MachineRingsCard(
                rings: vm.statusRings,
                online: cardOnline,
                showHeader: true,
                statusDot: vm.statusCardDot,
                statusBadge: vm.statusCardBadge.rendered(),
                statusWarn: vm.statusCardWarn,
                onOpen: onOpenStatus
            )
        }

        // Module 1: 额度与用量
        quotaAndUsageModule(vm)

        // Module 2: 编程 CLI
        codingCliModule(vm)

        // Module 3: 运行与存储
        runtimeAndStorageModule(vm, name: name)

        // Module 4: 管理 + Bottom Danger
        managementModule(vm)
        managementSection()
    }

    @ViewBuilder private func quotaAndUsageModule(_ vm: PBMachineSettingsViewModel) -> some View {
        if !vm.quotaRows.isEmpty || vm.showUsageEntry {
            GroupLabel(L10n.settingsSectionQuotaAndUsage())
            GroupCard(separatorLeading: 46) {
                ForEach(Array(vm.quotaRows.enumerated()), id: \.offset) { _, sec in
                    quotaRow(sec)
                }
                if vm.showUsageEntry {
                    GRow(
                        L10n.settingsUsageAndSpeed(),
                        value: usageEntrySummary(vm),
                        chevron: true,
                        valueLines: 1
                    ) {
                        onOpenUsage()
                    }
                    .lead {
                        SymbolImage("speedometer", size: 20, color: DS.ink2)
                            .frame(width: 22, alignment: .center)
                    }
                }
            }
        }
    }

    @ViewBuilder private func quotaRow(_ sec: PBQuotaCliSection) -> some View {
        let primaryText = sec.summaryPrimaryText.rendered()
        let windowText = sec.summaryWindowText.rendered()
        let summary = sec.hasSummaryRow ? sec.summaryRow : nil
        let rem = primaryText.or(summary?.remainingText.rendered() ?? "")
        let win = windowText.or(summary?.label.rendered() ?? "")
        let warn = !primaryText.isEmpty ? sec.summaryPrimaryWarn : (summary?.tone == .warn || summary?.tone == .full)
        let fallback = sec.emptyOrErrorText.rendered()

        if !rem.isEmpty && !win.isEmpty {
            GRow(sec.agentName, value: "· " + win, chevron: true, valueLines: 1) {
                onOpenQuota()
            }
            .lead {
                AgentMark(sec.agent, style: .body, named: true)
                    .frame(width: 22, alignment: .center)
            }
            .valueLead {
                Text(rem)
                    .font(.subheadline)
                    .fontWeight(warn ? .semibold : .regular)
                    .ink(warn ? DS.orangeInk : DS.ink2)
                    .lineLimit(typeSize.isAccessibilitySize ? nil : 1)
            }
        } else {
            let singleVal = rem.isEmpty ? fallback : rem
            GRow(
                sec.agentName,
                value: singleVal,
                chevron: true,
                valueColor: (!rem.isEmpty && warn) ? DS.orangeInk : DS.ink2,
                valueLines: 1
            ) {
                onOpenQuota()
            }
            .lead {
                AgentMark(sec.agent, style: .body, named: true)
                    .frame(width: 22, alignment: .center)
            }
        }
    }

    private func usageEntrySummary(_ vm: PBMachineSettingsViewModel) -> String {
        if vm.usageTotalTokens > 0, vm.hasUsageTokensText {
            let formatted = vm.usageTokensText.rendered()
            if !formatted.isEmpty {
                if vm.usageCachedOffline {
                    return L10n.settingsUsageSummaryCached(value: formatted)
                } else if vm.usageFallbackThirtyDays {
                    return L10n.settingsUsageSummary30D(value: formatted)
                } else {
                    return L10n.settingsUsageSummary7D(value: formatted)
                }
            }
        }
        return vm.usageEmptyText.rendered().or(L10n.settingsUsageSummaryEmpty())
    }

    @ViewBuilder private func codingCliModule(_ vm: PBMachineSettingsViewModel) -> some View {
        if !vm.cliVersions.isEmpty {
            GroupLabel(L10n.settingsSectionCodingClis())
            let conflictBanner = vm.cliSwitchesConflictBanner.rendered()
            if !conflictBanner.isEmpty {
                GroupBanner(conflictBanner, tone: .warn)
            }
            let disabledReason = vm.cliSwitchesDisabledReason.rendered()
            if !disabledReason.isEmpty {
                GroupBanner(disabledReason, tone: .warn)
            }

            GroupCard(separatorLeading: 46) {
                ForEach(Array(vm.cliVersions.enumerated()), id: \.offset) { _, a in
                    cliRow(a)
                }
            }

            let switchesFooter = vm.cliSwitchesFooter.rendered()
            if !switchesFooter.isEmpty {
                GroupNote(switchesFooter)
            } else if vm.cliSwitchesLeaseExpired {
                GroupNote(L10n.settingsCliSwitchesLeaseExpiredFooter())
            }
        }
    }

    @ViewBuilder private func cliRow(_ a: PBCliVersionView) -> some View {
        let ver = a.version.or(L10n.settingsIosUnknown())
        let unverifiedWord = a.unverifiedLabel.rendered().or(L10n.settingsCliUnverifiedShort())
            .trimmingCharacters(in: .whitespacesAndNewlines)
            .trimmingCharacters(in: CharacterSet(charactersIn: "·"))
            .trimmingCharacters(in: .whitespacesAndNewlines)
        let modelLabel = a.defaultModelLabel.rendered()
        let modeLabel = a.defaultModeLabel.rendered()
        let disabledLabel = a.disabledLabel.rendered().or(L10n.settingsCliDisabled())

        let (leadValue, trailingValue, trailingColor): (String?, String?, Color) = {
            if !a.enabled {
                return (nil, disabledLabel, DS.ink2)
            }
            if !modelLabel.isEmpty && !modeLabel.isEmpty {
                return (modelLabel + " · ", modeLabel, a.defaultModeDanger ? DS.red : DS.ink2)
            }
            if !modeLabel.isEmpty {
                return (nil, modeLabel, a.defaultModeDanger ? DS.red : DS.ink2)
            }
            return (nil, modelLabel.isEmpty ? nil : modelLabel, DS.ink2)
        }()

        GRow(
            a.name.or(agentName(a.agent)),
            value: trailingValue,
            chevron: true,
            valueColor: trailingColor,
            valueLines: 1
        ) {
            onSelectAgent(a.agent)
            cliSheetAgent = CliSheetAgent(agent: a.agent)
        }
        .lead {
            AgentMark(a.agent, style: .body, named: true)
                .frame(width: 22, alignment: .center)
                .opacity(a.enabled ? 1 : 0.45)
        }
        .subView {
            if !a.verified {
                let verSuffix = " · " + ver
                let line = Text(unverifiedWord).foregroundStyle(DS.orangeInk).fontWeight(.semibold)
                    + Text(verSuffix).foregroundStyle(DS.ink2)
                line
            }
        }
        .valueLead {
            if let leadValue {
                Text(leadValue)
                    .font(.subheadline)
                    .foregroundStyle(DS.ink2)
                    .lineLimit(1)
                    .truncationMode(.tail)
            }
        }
    }

    @ViewBuilder private func runtimeAndStorageModule(_ vm: PBMachineSettingsViewModel, name: String) -> some View {
        let busy = vm.savePending || !vm.editable
        let readError = vm.settingsError.rendered()
        let saveErr = vm.saveError.rendered()
        let showDirect = vm.member && vm.hasConnection && vm.connection.toggleVisible
        let showStorage = vm.hasAttachmentStorage
        let showDaemonPickers = vm.editable && !vm.settingsLoading && !vm.approvalTimeoutChoices.isEmpty

        if showDirect || showStorage || vm.editable || !vm.online || vm.unlinked {
            GroupLabel(L10n.settingsSectionRuntimeAndStorage())
            GroupCard {
                if showDirect {
                    let sub = vm.connection.rowSubtitleText.rendered()
                    GRow(
                        L10n.transportDirect(),
                        sub: sub.isEmpty ? nil : sub,
                        value: vm.connection.rowValueText.rendered(),
                        chevron: true,
                        valueLines: 1
                    ) {
                        directSheetOpen = true
                    }
                    .valueLead {
                        if vm.connection.rowSpinning {
                            ProgressView().controlSize(.small)
                        } else {
                            machineStatusDotView(vm.connection.rowDot, size: 8)
                        }
                    }
                }
                if showDaemonPickers {
                    let timeoutValue = vm.approvalTimeoutChoices.first(where: \.selected)?.label.rendered()
                        ?? L10n.quotaMin(count: Int(vm.selectedApprovalTimeoutSeconds / 60))
                    let idleValue = vm.idleCloseChoices.first(where: \.selected)?.label.rendered() ?? ""
                    GRow(L10n.settingsApprovalTimeout(), value: timeoutValue, chevron: true, enabled: !busy) {
                        daemonPick = .timeout
                    }
                    GRow(L10n.settingsIdleCloseShort(), value: idleValue, chevron: true, enabled: !busy) {
                        daemonPick = .idleClose
                    }
                }
                if showStorage {
                    let s = vm.attachmentStorage
                    GRow(
                        L10n.settingsAttachmentStorage(),
                        value: s.summaryText.rendered().or("—"),
                        chevron: true,
                        valueColor: (s.loaded && s.summaryWarn) ? DS.orangeInk : DS.ink2,
                        valueLines: 1
                    ) {
                        onOpenStorage()
                    }
                }
            }

            if !vm.editable {
                if vm.protocolMismatch == 0 && !vm.online {
                    GroupNote(L10n.settingsIosIsOfflineConnectToReadAndEditNewSession(value: name))
                } else if vm.protocolMismatch == 0 && vm.unlinked {
                    GroupNote(L10n.settingsIosConnectToTheServerToReadAndEditNew())
                }
            } else if vm.settingsLoading {
                LoadingRow(inset: 30).padding(.bottom, 4)
            } else {
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
            }
        }
    }

    @ViewBuilder private func managementModule(_ vm: PBMachineSettingsViewModel) -> some View {
        let rawCount = vm.projectCountText.rendered()
        let countNum = vm.projectCount > 0 ? Int(vm.projectCount) : vm.projects.count
        let showProjects = countNum > 0 || !rawCount.isEmpty
        let projectCount = rawCount.or("\(countNum)")
        let shortIdText = vm.shortID

        GroupLabel(L10n.settingsSectionManagement())
        GroupCard {
            if showProjects {
                GRow(L10n.settingsProjectsRow(), value: projectCount, chevron: true, valueLines: 1) {
                    onOpenProjects()
                }
            }
            GRow(L10n.settingsDeviceId()) {}
                .trailing {
                    Text(shortIdText)
                        .font(.subheadline.monospaced())
                        .foregroundStyle(DS.ink2)
                }
                .contextMenu {
                    Button(L10n.settingsIosCopyDeviceId()) {
                        UIPasteboard.general.string = vm.machineID
                    }
                }
        }
    }

    @ViewBuilder private func daemonPickerSheet(_ pick: DaemonPick) -> some View {
        switch pick {
        case .timeout:
            SheetHead(L10n.settingsApprovalTimeout())
            ForEach(Array(vm.approvalTimeoutChoices.enumerated()), id: \.offset) { idx, c in
                ListItem(
                    c.label.rendered(),
                    checked: c.selected,
                    last: idx == vm.approvalTimeoutChoices.count - 1
                ) {
                    daemonPick = nil
                    onUpdateDaemonSettings(c.seconds, nil, nil, false)
                }
            }
            Hint(L10n.settingsTimeoutFooter())

        case .idleClose:
            SheetHead(L10n.settingsIdleCloseShort())
            ForEach(Array(vm.idleCloseChoices.enumerated()), id: \.offset) { idx, c in
                ListItem(
                    c.label.rendered(),
                    checked: c.selected,
                    last: idx == vm.idleCloseChoices.count - 1
                ) {
                    daemonPick = nil
                    onUpdateDaemonSettings(nil, c.minutes, nil, false)
                }
            }
            Hint(L10n.settingsIosSessionsEndAutomaticallyAfterThisMuchInactivityNotRunning())
        }
    }
}

// MARK: - CLI Settings Sheet

private struct CliSettingsSheet: View {
    let vm: PBMachineSettingsViewModel
    let agent: PBAgent
    let onSelectAgent: (PBAgent) -> Void
    let onToggleAgent: (PBAgent, Bool) -> Void
    let onUpdateDefaults: (PBAgent, String?, String?, PBApprovalMode?) -> Void
    let onRetrySettings: () -> Void
    let onClose: () -> Void

    @Environment(\.horizontalSizeClass) private var sizeClass
    @State private var subpage: Subpage = .root

    private enum Subpage: Equatable {
        case root, model, effort, mode
    }

    private var cli: PBCliVersionView? {
        vm.cliVersions.first { $0.agent == agent }
    }

    private var defaultsView: PBMachineAgentDefaultsView? {
        if let cli, cli.hasDefaults {
            return cli.defaults
        }
        if vm.hasAgentDefaults && vm.agentDefaults.agent == agent {
            return vm.agentDefaults
        }
        return nil
    }

    var body: some View {
        let name = cli?.name.or(agentName(agent)) ?? agentName(agent)
        let busy = vm.savePending || !vm.editable
        ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                switch subpage {
                case .root:
                    SheetHead(name, action: L10n.commonDone(), onAction: onClose)
                    rootBody(cli: cli, busy: busy)
                case .model:
                    SheetHead(L10n.settingsIosDefaultModelForNewSessions(value: name), onBack: { subpage = .root })
                    modelPicker
                case .effort:
                    SheetHead(L10n.modelsEffort(), onBack: { subpage = .root })
                    effortPicker
                case .mode:
                    SheetHead(L10n.settingsDefaultApprovalMode(), onBack: { subpage = .root })
                    modePicker
                }
            }
            .padding(.init(top: 26, leading: 16, bottom: 24, trailing: 16))
        }
        .scrollBounceBehavior(.basedOnSize)
        .sheetSurface()
        .tint(DS.blue)
        .adaptiveModalPresentation(sizeClass: sizeClass)
        .onAppear { onSelectAgent(agent) }
    }

    @ViewBuilder private func rootBody(cli: PBCliVersionView?, busy: Bool) -> some View {
        let ver = cli?.version.or(L10n.settingsIosUnknown()) ?? L10n.settingsIosUnknown()
        let verified = cli?.verified ?? true
        let enabled = cli?.enabled ?? true
        let locked = cli?.lockedLastEnabled ?? false
        let saving = cli?.saving ?? false
        let showSwitch = vm.cliVersions.count >= 2 && vm.showCliSwitches
        let canToggle = vm.cliSwitchesEditable && !locked && !saving && vm.editable && !vm.savePending
        let conflictBanner = vm.cliSwitchesConflictBanner.rendered()
        let disabledReason = vm.cliSwitchesDisabledReason.rendered()
        let switchesFooter = vm.cliSwitchesFooter.rendered()
        let readError = vm.settingsError.rendered()

        if showSwitch && !conflictBanner.isEmpty {
            GroupBanner(conflictBanner, tone: .warn)
        }
        if showSwitch && !disabledReason.isEmpty {
            GroupBanner(disabledReason, tone: .warn)
        }

        GroupCard {
            if showSwitch {
                HStack(spacing: 10) {
                    VStack(alignment: .leading, spacing: 2) {
                        Text(L10n.settingsCliEnableOnComputer())
                            .font(.body)
                            .foregroundStyle(DS.ink)
                        if locked {
                            Text(L10n.settingsCliSwitchesKeepOne())
                                .font(.footnote)
                                .foregroundStyle(DS.ink2)
                        }
                    }
                    Spacer(minLength: 8)
                    if saving {
                        ProgressView().controlSize(.small)
                    }
                    Toggle("", isOn: Binding(
                        get: { enabled },
                        set: { onToggleAgent(agent, $0) }
                    ))
                    .labelsHidden()
                    .tint(locked ? DS.green.opacity(0.4) : DS.green)
                    .disabled(!canToggle)
                }
                .padding(.horizontal, 14)
                .padding(.vertical, 10)
                .frame(minHeight: DS.minHit)
            }

            GRow(L10n.settingsCliInstalledVersion(), value: ver)
                .valueLead {
                    if !verified {
                        Text(L10n.settingsCliUnverifiedBadge())
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(DS.orangeInk)
                            .padding(.trailing, 2)
                    }
                }
        }

        if !verified {
            GroupNote(L10n.settingsIosForUnverifiedVersionsRunRemoteAgentDoctorOnThe())
        }
        if showSwitch && !switchesFooter.isEmpty {
            GroupNote(switchesFooter)
        }

        if !enabled {
            GroupNote(L10n.settingsCliDisabledExplanation())
        } else if let d = defaultsView, cli?.hasDefaults != false {
            let modelValue = d.selectedModelLabel.rendered().or(d.selectedModel).or(L10n.modelsDefault())
            let selectedEffort = d.effortChoices.first(where: \.selected)
            let effortValue = selectedEffort?.label.rendered().or(d.selectedEffort) ?? d.selectedEffort.or(L10n.modelsDefault())
            let modeValue = d.modeChoices.first(where: \.selected)?.label.rendered() ?? ""

            GroupLabel(L10n.settingsNewProjectDefaults())
            GroupCard {
                GRow(L10n.settingsCliSheetModel(), value: modelValue, chevron: true, enabled: !busy) {
                    subpage = .model
                }
                if d.effortChoices.count > 1 {
                    GRow(L10n.settingsCliSheetEffort(), value: effortValue, chevron: true, enabled: !busy) {
                        subpage = .effort
                    }
                }
                GRow(
                    L10n.settingsCliSheetMode(),
                    value: modeValue,
                    chevron: true,
                    enabled: !busy,
                    valueColor: d.selectedMode == .fullAccess ? DS.red : nil
                ) {
                    subpage = .mode
                }
            }
            GroupNote(L10n.settingsCliNewProjectDefaultsFooter())
        } else if !readError.isEmpty {
            GroupBanner(readError, tone: .error, action: L10n.commonRetry(), onAction: onRetrySettings)
        }
    }

    @ViewBuilder private var modelPicker: some View {
        if let d = defaultsView {
            ForEach(Array(d.modelChoices.enumerated()), id: \.offset) { idx, c in
                ListItem(
                    c.label.rendered().or(c.id),
                    checked: c.selected,
                    small: c.desc.isEmpty ? nil : c.desc,
                    last: idx == d.modelChoices.count - 1
                ) {
                    onUpdateDefaults(agent, c.id, nil, nil)
                    subpage = .root
                }
            }
            let footer = d.footerText.rendered()
            if !footer.isEmpty { Hint(footer) }
        }
    }

    @ViewBuilder private var effortPicker: some View {
        if let d = defaultsView {
            ForEach(Array(d.effortChoices.enumerated()), id: \.offset) { idx, c in
                ListItem(
                    c.label.rendered().or(c.id),
                    checked: c.selected,
                    last: idx == d.effortChoices.count - 1
                ) {
                    onUpdateDefaults(agent, nil, c.id, nil)
                    subpage = .root
                }
            }
        }
    }

    @ViewBuilder private var modePicker: some View {
        if let d = defaultsView {
            ForEach(Array(d.modeChoices.enumerated()), id: \.offset) { idx, c in
                let disabledReason = c.disabledReason.rendered()
                ListItem(
                    c.label.rendered(),
                    checked: c.selected,
                    enabled: !c.disabled,
                    small: disabledReason.isEmpty ? nil : disabledReason,
                    last: idx == d.modeChoices.count - 1
                ) {
                    onUpdateDefaults(agent, nil, nil, c.mode)
                    subpage = .root
                }
            }
            let notice = d.autoUnavailableNotice.rendered()
            if !notice.isEmpty { Hint(notice) }
        }
    }
}

// MARK: - Unified Direct Connection Sheet

private struct DirectSettingsSheet: View {
    let machineID: String
    let machineName: String
    let view: PBConnectionGroupView
    let diag: PBDirectDiagnosticsViewModel
    let onToggle: (Bool) -> Void
    let onRetry: () -> Void
    let onOpenDiag: () -> Void
    let onCloseDiag: () -> Void
    let onRefreshDiag: () -> Void
    let onClose: () -> Void

    @Environment(\.horizontalSizeClass) private var sizeClass
    @State private var copied = false
    @State private var cooldown = false
    @State private var wasDiagShown = false

    var body: some View {
        let supported = view.toggleVisible
        let status = view.statusText.rendered()
        let detail = view.detailText.rendered()
        let metric = view.metricText.rendered()
        let fileSpeed = view.fileSpeedText.rendered()
        let readLatency = view.readLatencyText.rendered()
        let showStatus = !status.isEmpty || !detail.isEmpty || !metric.isEmpty
        let showSpeeds = view.stage != .disabled && view.stage != .unsupported && view.stage != .offline
        let matchingDiag = diag.machineID.isEmpty || diag.machineID == machineID
        let isDirect = (matchingDiag && diag.connected) || view.stage == .direct
        let isRunning = (matchingDiag && diag.trying) || view.stage == .connecting || view.stage == .measuring
        let peerOutdated = !isDirect && matchingDiag && diag.peerOutdated
        let showDiagSection = !isDirect && (view.showDiagnosticsEntry || (wasDiagShown && isRunning))
        let explanation = (matchingDiag ? diag.bodyText.rendered() : "").or(L10n.transportDiagExplainGeneric())

        ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                SheetHead(L10n.transportDirect(), action: L10n.commonDone(), onAction: onClose)

                GroupCard {
                    ToggleRow(
                        L10n.transportRowAllowDirect(),
                        isOn: Binding(
                            get: { supported && view.toggleChecked },
                            set: { onToggle($0) }
                        )
                    )
                    .disabled(!view.toggleEnabled)
                    .opacity(supported ? 1 : 0.4)

                    if showStatus {
                        DirectStatusRow(stage: view.stage, tone: view.tone, status: status, detail: detail, metric: metric)
                    }
                    if showSpeeds && !fileSpeed.isEmpty {
                        GRow(L10n.transportSpeedFile(), value: fileSpeed)
                    }
                    if showSpeeds && !readLatency.isEmpty {
                        GRow(L10n.transportSpeedRead(), value: readLatency)
                    }
                }

                if showDiagSection {
                    let checks = matchingDiag ? diag.checks : []
                    GroupLabel(L10n.transportDiagSectionAuto())
                    Text(explanation)
                        .font(.subheadline)
                        .foregroundStyle(DS.ink2)
                        .padding(.horizontal, 16)
                        .padding(.bottom, 10)
                    if !checks.isEmpty {
                        GroupCard(separatorLeading: 34) {
                            ForEach(Array(checks.enumerated()), id: \.offset) { _, row in
                                let dotColor: Color = switch row.mark {
                                case .online: DS.green
                                case .run: DS.ink
                                default: DS.ink3
                                }
                                let detailText = row.detailText.rendered()
                                HStack(alignment: .center, spacing: 10) {
                                    Dot(dotColor, size: 8)
                                    VStack(alignment: .leading, spacing: 2) {
                                        Text(row.labelText.rendered()).font(.body).foregroundStyle(DS.ink)
                                        if !detailText.isEmpty {
                                            Text(detailText).font(.footnote).foregroundStyle(DS.ink2)
                                        }
                                    }
                                    Spacer(minLength: 8)
                                    Text(row.statusText.rendered()).font(.subheadline).foregroundStyle(DS.ink2)
                                }
                                .padding(.horizontal, 16)
                                .padding(.vertical, 10)
                                .frame(minHeight: DS.minHit)
                            }
                        }
                    }

                    let canRetryVm = !matchingDiag || diag.machineID.isEmpty || (diag.supported && diag.canRetry)
                    let retryDisabled = isRunning || cooldown || isDirect || peerOutdated || !supported || !canRetryVm
                    VStack(spacing: 10) {
                        Btn(
                            isRunning ? L10n.transportDiagRetrying() : L10n.transportRowRetryDirect(),
                            kind: .primary,
                            enabled: !retryDisabled
                        ) {
                            cooldown = true
                            onRetry()
                            Task {
                                try? await Task.sleep(for: .seconds(2))
                                cooldown = false
                            }
                        }
                        Btn(
                            copied ? L10n.transportRowDiagCopied() : L10n.transportRowDiagCopySupport(),
                            kind: .secondary
                        ) {
                            UIPasteboard.general.string = diagnosticReport(matchingDiag: matchingDiag, verdict: detail.or(status))
                            copied = true
                            Task {
                                try? await Task.sleep(for: .seconds(1.8))
                                copied = false
                            }
                        }
                    }
                    .padding(.horizontal, 16)
                    .padding(.bottom, 12)
                }

                GroupNote(view.footerText.rendered().or(L10n.transportDiagPrivacyFooter()))
            }
            .padding(.init(top: 26, leading: 16, bottom: 24, trailing: 16))
        }
        .scrollBounceBehavior(.basedOnSize)
        .sheetSurface()
        .tint(DS.blue)
        .adaptiveModalPresentation(sizeClass: sizeClass)
        .task(id: machineID) {
            onOpenDiag()
        }
        .onDisappear {
            onCloseDiag()
        }
        .task(id: "\(machineID):\(isRunning)") {
            onRefreshDiag()
            guard isRunning else { return }
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(1))
                if !Task.isCancelled { onRefreshDiag() }
            }
        }
        .onChange(of: view.showDiagnosticsEntry, initial: true) { _, show in
            if isDirect || !view.toggleChecked {
                wasDiagShown = false
            } else if show {
                wasDiagShown = true
            }
        }
        .onChange(of: isDirect) { _, direct in
            if direct { wasDiagShown = false }
        }
        .onChange(of: view.toggleChecked) { _, checked in
            if !checked { wasDiagShown = false }
        }
    }

    private func diagnosticReport(matchingDiag: Bool, verdict: String) -> String {
        let fmt = DateFormatter()
        fmt.locale = Locale(identifier: "en_US_POSIX")
        fmt.dateFormat = "yyyy-MM-dd HH:mm:ss"
        let renderedLines = matchingDiag ? diag.reportLines.map { $0.rendered() }.filter { !$0.isEmpty } : []
        var lines: [String] = []
        if !renderedLines.isEmpty {
            lines.append(contentsOf: renderedLines)
        } else {
            let title = (matchingDiag ? diag.titleText.rendered().or(diag.verdictText.rendered()) : "").or(verdict)
            lines.append(L10n.transportDiagReportTitle())
            lines.append(L10n.transportDiagReportVerdict(verdict: title.or(L10n.transportDiagVerdictNone())))
            lines.append("Client: iOS \(UIDevice.current.systemVersion)")
        }
        if !machineName.isEmpty {
            lines.append("Computer: \(machineName)")
        }
        lines.append("Time: \(fmt.string(from: Date()))")
        return lines.joined(separator: "\n")
    }
}

private extension View {
    @ViewBuilder func adaptiveModalPresentation(sizeClass: UserInterfaceSizeClass?) -> some View {
        if sizeClass == .regular {
            if #available(iOS 18.0, *) {
                self.presentationSizing(.form)
            } else {
                self
            }
        } else {
            self.presentationDetents([.medium, .large])
        }
    }
}
