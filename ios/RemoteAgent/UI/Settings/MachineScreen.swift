import RACore
import RAProto
import SwiftUI

/// Where a link into this page scrolls to.
enum MachineAnchor: Hashable { case storage }

/// A computer's details and daemon settings (Android's DeviceScreen): what
/// it is, why it may be out of reach, its CLIs and their account quotas,
/// the defaults new projects start from, where it keeps uploaded images,
/// its projects, and removing it from the account.
struct MachineScreen: View {
    let id: String
    /// Opened from the disk card or 「remote-agent 的占用」 of 电脑状态: scrolled to 附件存储.
    var scrollToStorage = false
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @Environment(\.page) private var page
    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var settings: MachineSettingsModel?
    @State private var cleaning = false
    @State private var picking: Pick?
    @State private var selectedAgent: PBAgent?
    /// The three rings at the top, read every 10 seconds while this page is showing (ui-spec A2).
    @State private var ringsFeed = MachineStatusFeed(interval: .seconds(10))

    /// What a sheet of this page chooses.
    private enum Pick: Identifiable, Hashable {
        case model(PBAgent)
        case mode(PBAgent)
        case timeout, idleClose, imageQuota

        var id: String {
            switch self {
            case .model(let agent): "model-\(agent.rawValue)"
            case .mode(let agent): "mode-\(agent.rawValue)"
            case .timeout: "timeout"
            case .idleClose: "idle-close"
            case .imageQuota: "image-quota"
            }
        }
    }

    /// Commands reach the computer: this client is connected, the computer is, and both speak the same protocol.
    private func reachable(_ state: RemoteState) -> Bool {
        guard let m = state.machines[id] else { return false }
        return !state.unlinked && m.online && protocolMismatch(m) == nil
    }

    /// Pulling down reads the settings again, while commands reach the computer.
    private func refresh(_ live: Bool) -> (@MainActor @Sendable () async -> Void)? {
        guard live, let settings else { return nil }
        return { await settings.read() }
    }

    var body: some View {
        let state = model.state
        let m = state.machines[id]
        let live = reachable(state)
        SettingsPage(refresh: refresh(live), scrollTo: scrollToStorage ? (id: MachineAnchor.storage, ready: settings?.settings != nil) : nil) {
            if let m {
                Ticking { now in details(m, state: state, now: now) }
            } else {
                PageNote(L10n.settingsIosThisComputerIsNotInTheCurrentTrustedDirectory())
            }
        }
        .machineStatusPolling(
            ringsFeed, fetch: context.machineStatusFetch(id, demo: model.isDemo), online: m?.online == true && !state.unlinked,
            supported: m?.meta?.capabilities.machineStatus == true)
        .navBar(
            m == nil ? L10n.settingsComputer() : machineLabel(state, id: id),
            subtitle: m.flatMap { machineStatus($0, state: state, now: nowMs()) }.map {
                $0.online ? L10n.commonIosOnline() : L10n.commonIosOffline()
            },
            dot: m.flatMap { machineStatus($0, state: state, now: nowMs()) }.map {
                $0.online ? DS.green : DS.ink3
            }
        )
        // Read when the page opens and again whenever the computer comes back within reach.
        .task(id: live) {
            let s = settings ?? MachineSettingsModel(calls: .remote(context.remote, machine: id))
            settings = s
            #if DEBUG
                if model.isDemo {
                    if live { showDemo(s) }
                    return
                }
            #endif
            if live { await s.read() }
        }
        .kitSheet(item: $picking) { pick in sheet(pick, state: state) }
        // What a tap on a timeout, and on 移除 in the confirmation, does (debug builds).
        .debugSignal("machine.timeout") {
            guard let settings, let current = settings.settings else { return }
            let now =
                approvalTimeoutMinutes.firstIndex(of: Int(current.approvalTimeoutSeconds) / 60)
                ?? -1
            let next = approvalTimeoutMinutes[(now + 1) % approvalTimeoutMinutes.count]
            Task { await settings.save(current.with(timeoutMinutes: next)) }
        }

        .confirm(
            isPresented: $cleaning, title: L10n.settingsIosClearReclaimableImages(),
            text: L10n.settingsIosClearsReclaimableOriginalsAndCacheUnsyncedFullSizeImages(), confirm: L10n.settingsClearSettings()
        ) {
            guard let settings, let current = settings.settings else { return }
            Task { await settings.save(current, clean: true) }
        }
    }

    // MARK: The page

    @ViewBuilder private func details(_ m: MachineView, state: RemoteState, now: Int64) -> some View
    {
        let name = machineLabel(state, id: id)
        let mismatch = protocolMismatch(m)
        let status = machineStatus(m, state: state, now: now)
        if let mismatch { GroupBanner(mismatch, tone: .warn) }
        if let status, !status.online { GroupBanner(offlineText(m, now: now)) }

        // CPU, memory and disk at a glance; a daemon that cannot say has none (ui-spec A2).
        if m.meta?.capabilities.machineStatus == true {
            MachineRingsCard(
                rings: machineRings(status: ringsFeed.last, online: m.online && !state.unlinked, text: StatusText()),
                online: m.online && !state.unlinked && protocolMismatch(m) == nil
            ) { nav.open(.machineStatus(id: id)) }
        }

        GroupLabel(L10n.settingsComputer())
        GroupCard {
            GRow(L10n.settingsName(), value: name)
            GRow(L10n.settingsIosSystem(), value: osName(m.meta?.os ?? ""))
            GRow(L10n.settingsDeviceId(), value: "…" + shortId(id))
        }

        let groups = quotaGroups(asKnown(m, state: state), now: now)
        // A computer that has not said what it runs has no CLI to list a quota for.
        if !groups.isEmpty, m.meta != nil {
            // A line for each CLI's quota, leading to all of it.
            GroupLabel(L10n.settingsAccountQuota())
            GroupCard {
                for (i, g) in groups.enumerated() {
                    let title = quotaRowTitle(g, among: groups, at: i)
                    let line = quotaLine(g)
                    let sep = " · "
                    if let idx = line.range(of: sep) {
                        let usedPart = String(line[..<idx.lowerBound])
                        let winPart = "· " + String(line[idx.upperBound...])
                        GRow(title, value: winPart, chevron: true, valueLines: 1) {
                            nav.open(.quota(machine: id))
                        }
                        .valueLead {
                            Text(usedPart).font(.subheadline).ink(quotaSummaryRow(g).map { $0.tone == .warn || $0.tone == .full } == true ? DS.orangeInk : DS.ink2).lineLimit(typeSize.isAccessibilitySize ? nil : 1)
                        }
                    } else {
                        GRow(title, value: line, chevron: true) {
                            nav.open(.quota(machine: id))
                        }
                    }
                }
            }
        }

        if let meta = m.meta {
            let agents = shownAgents(meta)
            GroupLabel(L10n.settingsIosCliVersions())
            GroupCard {
                for a in agents {
                    GRow(agentName(a.agent), value: a.version.or(L10n.settingsIosUnknown()))
                        .valueLead {
                            Text(a.verified ? L10n.settingsIosVerified() : L10n.settingsIosUnverified()).font(.subheadline).ink(a.verified ? DS.ink2 : DS.orangeInk)
                        }
                }
            }
            if agents.contains(where: { !$0.verified }) {
                GroupNote(L10n.settingsIosForUnverifiedVersionsRunRemoteAgentDoctorOnThe())
            }
        }

        daemonSettings(m, state: state, name: name, mismatch: mismatch != nil)

        let projects = machineProjects(state, machineId: id)
        if !projects.isEmpty {
            GroupLabel(L10n.scheduleProject())
            GroupCard {
                for cwd in projects {
                    GRow(projectName(cwd), value: shortPath(cwd), chevron: true) {
                        nav.open(.project(key: cwd))
                    }
                }
            }
        }

        DeviceManagementActions(id: id, machine: true)
    }

    /// Why a computer is not there. The server only knows the daemon's
    /// connection is gone, so all three possibilities are said (architecture
    /// §9 「电脑为什么不在线」), in the words of the session's offline card.
    private func offlineText(_ m: MachineView, now: Int64) -> String {
        let since = whenText(m.lastSeenMs, now: now)
        return (since.isEmpty ? "" : L10n.sessionDisconnectedAgo(time: String(describing: since)))
            + L10n.settingsIosTheComputerMayBeAsleepOfflineOrNotRunning()
    }

    /// The CLIs the computer reports; one that reports none is an older Codex-only daemon's.
    private func shownAgents(_ meta: PBMachineMeta) -> [PBAgentInfo] {
        machineVersions(meta)
    }

    /// The CLI's name; with several accounts of one CLI, which one, so two rows are not named alike.
    private func quotaRowTitle(_ g: QuotaGroup, among groups: [QuotaGroup], at index: Int) -> String
    {
        let name = agentName(g.agent)
        guard groups.filter({ $0.agent == g.agent }).count > 1 else { return name }
        let parts = g.title.components(separatedBy: " · ")
        return parts.count > 1 ? name + " · " + (parts.last ?? "") : name
    }

    // MARK: Daemon settings

    @ViewBuilder private func daemonSettings(
        _ m: MachineView, state: RemoteState, name: String, mismatch: Bool
    ) -> some View {
        let s = settings?.settings
        let shown = reachable(state)
        if state.unlinked {
            GroupNote(L10n.settingsIosConnectToTheServerToReadAndEditNew())
        } else if !m.online {
            GroupNote(L10n.settingsIosIsOfflineConnectToReadAndEditNewSession(value: String(describing: name)))
        } else if mismatch {
            // The notice at the top says which side to update; nothing is sent meanwhile.
            EmptyView()
        } else if let settings, let s {
            let busy = settings.saving || !shown
            let agents = machineAgents(m)
            if !agents.isEmpty {
                let activeAgent = (selectedAgent != nil && agents.contains(selectedAgent!)) ? selectedAgent! : agents[0]
                if agents.count > 1 {
                    GroupLabel(L10n.settingsNewProjectDefaults())
                    SegPick(
                        agents.map(agentName),
                        selected: Binding(
                            get: { agents.firstIndex(of: activeAgent) ?? 0 },
                            set: { selectedAgent = agents[$0] }
                        )
                    )
                    .padding(.horizontal, DS.Space.l)
                } else {
                    GroupLabel(L10n.settingsNewProjectDefaultsSettings(value: String(describing: agentName(activeAgent))))
                }

                let d = s.defaults(for: activeAgent)
                let effective = defaultModeOf(m, agent: activeAgent)
                let chosen = modeOf(d.mode)
                GroupCard {
                    GRow(L10n.modelsTitle(), value: d.model.or(L10n.modelsDefault()), chevron: true, enabled: !busy) {
                        picking = .model(activeAgent)
                    }
                    GRow(L10n.modelsEffort(), value: d.effort.or(L10n.modelsDefault()), chevron: true, enabled: !busy) {
                        picking = .model(activeAgent)
                    }
                    // One line: that project settings may differ is said in the picker.
                    GRow(
                        L10n.modesTitle(),
                        value: chosen?.text
                            ?? modeLabel(
                                chosen: nil,
                                default: DefaultMode(
                                    mode: effective.mode, known: effective.known, varies: false),
                                agent: activeAgent),
                        chevron: true, enabled: !busy,
                        valueColor: chosen == nil && (effective.mode == .full || !effective.known)
                            ? DS.red : nil
                    ) { picking = .mode(activeAgent) }
                }

                GroupCard {
                    GRow(
                        L10n.settingsApprovalTimeout(), value: timeoutText(seconds: s.approvalTimeoutSeconds),
                        chevron: true, enabled: !busy
                    ) { picking = .timeout }
                    GRow(
                        L10n.settingsAutomaticallyEndIdleSessions(), value: idleCloseText(minutes: s.idleCloseMinutes),
                        chevron: true, enabled: !busy
                    ) { picking = .idleClose }
                }
            }
            if settings.saving { LoadingRow(L10n.settingsIosSaving(), inset: 30).padding(.bottom, 4) }
            if !settings.saveError.isEmpty {
                if settings.saveUnknown {
                    GroupBanner(settings.saveError, tone: .warn, action: L10n.settingsIosReload()) {
                        Task { await settings.read() }
                    }
                } else {
                    GroupBanner(L10n.settingsSaveError(reason: String(describing: settings.saveError)), tone: .error)
                }
            }
            if !settings.readError.isEmpty {
                GroupBanner(L10n.settingsReadError(reason: String(describing: settings.readError)), tone: .error, action: L10n.commonRetry()) {
                    Task { await settings.read() }
                }
            }
            imageStorage(s, usage: settings.imageStorage, enabled: !busy)
        } else if let settings, !settings.readError.isEmpty {
            GroupLabel(L10n.settingsNewProjectDefaults())
            GroupBanner(L10n.settingsReadError(reason: String(describing: settings.readError)), tone: .error, action: L10n.commonRetry()) {
                Task { await settings.read() }
            }
        } else {
            GroupLabel(L10n.settingsNewProjectDefaults())
            LoadingRow(inset: 30).padding(.bottom, 4)
        }
    }

    /// Where the computer keeps the images sent to its sessions (Android's MachineImageStorage).
    @ViewBuilder private func imageStorage(
        _ s: PBDaemonSettings, usage: PBImageStorageUsage?, enabled: Bool
    ) -> some View {
        let free = usage.map { $0.reclaimableBytes + $0.cacheBytes } ?? 0
        GroupLabel(L10n.settingsAttachmentStorage()).id(MachineAnchor.storage)
        GroupCard {
            GRow(L10n.settingsUploadedImagesAndFiles(), value: usage.map(uploadsText) ?? L10n.projectsReadingDirectory(), valueLines: nil)
            GRow(
                L10n.settingsStorageLimit(), value: wholeBytes(s.imageUploadQuotaBytes), chevron: true, enabled: enabled
            ) { picking = .imageQuota }
            GRow(L10n.settingsPreviewCache(), value: usage.map { imageBytes($0.cacheBytes) } ?? "—")
            // The space the daemon keeps back so that a session can still start on a full disk (ui-spec B2): not clearable; none while it is not kept.
            if let reserve = usage?.emergencyReserveBytes, reserve > 0 {
                EmergencyReserveRow(bytes: reserve)
            }
            GRow(
                L10n.settingsClear(), value: usage.map { _ in free > 0 ? L10n.settingsCanFree(value: String(describing: imageBytes(free))) : L10n.settingsNothingToClear() },
                action: true,
                enabled: enabled && free > 0
            ) { cleaning = true }
        }
        GroupNote(L10n.settingsIosImagesAreStoredOnThisComputerImagesBeingSent())
    }

    // MARK: Sheets

    @ViewBuilder private func sheet(_ pick: Pick, state: RemoteState) -> some View {
        if let settings, let s = settings.settings {
            let save: (PBDaemonSettings) -> Void = { next in
                picking = nil
                Task { await settings.save(next) }
            }
            switch pick {
            case .model(let agent):
                DefaultModelSheet(machine: id, agent: agent, current: s.defaults(for: agent)) {
                    model, effort in
                    save(s.with(model: model, effort: effort, for: agent))
                }
            case .mode(let agent):
                DefaultModeSheet(machine: id, agent: agent, settings: s, effective: defaultModeOf(state.machines[id], agent: agent)) {
                    save(s.with(mode: $0, for: agent))
                }
            case .timeout:
                SheetHead(L10n.settingsApprovalTimeout())
                ForEach(approvalTimeoutMinutes, id: \.self) { minutes in
                    ListItem(
                        L10n.quotaMin(count: Int(minutes)), checked: Int(s.approvalTimeoutSeconds) == minutes * 60,
                        last: minutes == approvalTimeoutMinutes.last
                    ) { save(s.with(timeoutMinutes: minutes)) }
                }
                Hint(L10n.settingsIosUnansweredApprovalsAreDeniedAfterThisTime())
            case .idleClose:
                SheetHead(L10n.settingsAutomaticallyEndIdleSessions())
                let choices = idleCloseChoices(current: s.idleCloseMinutes)
                ForEach(choices, id: \.self) { minutes in
                    ListItem(
                        idleCloseText(minutes: Int32(minutes)), checked: idleCloseIs(minutes, s.idleCloseMinutes),
                        small: minutes == idleCloseDefaultMinutes ? L10n.modelsDefault() : nil,
                        last: minutes == choices.last
                    ) { save(s.with(idleCloseMinutes: minutes)) }
                }
                Hint(L10n.settingsIosSessionsEndAutomaticallyAfterThisMuchInactivityNotRunning())
            case .imageQuota:
                SheetHead(L10n.settingsStorageLimit())
                ForEach(imageQuotaChoices, id: \.self) { bytes in
                    ListItem(
                        wholeBytes(bytes), checked: s.imageUploadQuotaBytes == bytes,
                        last: bytes == imageQuotaChoices.last
                    ) {
                        save(s.with(imageQuota: bytes))
                    }
                }
            }
        }
    }

    #if DEBUG
        private func showDemo(_ s: MachineSettingsModel) {
            switch SettingsDemo.state {
            case "loading": s.show(settings: nil, reading: true)
            case "failed": s.show(settings: nil, readError: L10n.errorsTheComputerIsOfflineTheCommandWasNotSent())
            case "saveFailed":
                s.show(
                    settings: SettingsDemo.daemonSettings, storage: SettingsDemo.imageStorage,
                    saveError: L10n.errorsTheComputerIsOfflineTheCommandWasNotSent())
            case "saveUnknown":
                s.show(
                    settings: SettingsDemo.daemonSettings, storage: SettingsDemo.imageStorage,
                    saveError: L10n.settingsIosUnsureWhetherTheComputerSavedThisChangeReloadTo(), saveUnknown: true)
            default:
                s.show(settings: SettingsDemo.daemonSettings, storage: SettingsDemo.imageStorage)
            }
        }
    #endif
}

/// A CLI's default model and effort for new sessions on a computer, from the models it offers.
private struct DefaultModelSheet: View {
    let machine: String
    let agent: PBAgent
    let current: PBAgentDefaults
    let onPick: (_ model: String, _ effort: String) -> Void
    @Environment(AccountContext.self) private var context
    @State private var models: PBListModelsResponse?
    @State private var error = ""
    @State private var attempt = 0
    /// The model whose efforts are being offered; the one in use to begin with.
    @State private var model: String?
    @State private var all = false

    var body: some View {
        let chosen = model ?? current.model
        SheetHead(L10n.settingsIosDefaultModelForNewSessions(value: String(describing: agentName(agent))))
        Group {
            if !error.isEmpty {
                Banner(L10n.sessionIosModelsFailed(reason: String(describing: error)), tone: .error, action: L10n.commonRetry()) {
                    error = ""
                    attempt += 1
                }
            } else if let models {
                ListItem(L10n.settingsIosDefaultSOwnConfiguration(value: String(describing: agentName(agent))), checked: chosen.isEmpty) {
                    onPick("", "")
                }
                let shown = shownModels(models.models, picked: models.models.first { $0.slug == chosen }, all: all)
                let few = shown.count == models.models.count
                ForEach(Array(shown.enumerated()), id: \.offset) { i, m in
                    let note = [
                        contextText(tokens: m.contextWindow), m.efforts.isEmpty ? L10n.sessionNoEffort() : nil,
                    ].compactMap { $0 }.joined(separator: " · ")
                    ListItem(
                        m.displayName.or(m.slug), checked: m.slug == chosen,
                        small: note.isEmpty ? nil : note,
                        last: few && i == shown.count - 1
                    ) {
                        // A model without efforts is chosen at once; one with them waits for the effort.
                        if m.efforts.isEmpty { onPick(m.slug, "") } else { model = m.slug }
                    }
                }
                if !few { TextAction(L10n.modelsAll(), style: .body, arrow: true) { all = true } }
                if let m = models.models.first(where: { $0.slug == chosen }), !m.efforts.isEmpty {
                    FieldLabel(L10n.modelsEffort())
                    ChoiceGrid(m.efforts, selected: current.model == m.slug ? current.effort : nil)
                    { onPick(m.slug, $0) }
                }
            } else {
                LoadingRow(inset: 0)
            }
        }
        .task(id: attempt) {
            do {
                models = try await context.remote.listModels(machine, agent: agent, fresh: true)
            } catch is CancellationError {
                // The sheet closed.
            } catch {
                self.error = errorText(error)
            }
        }
    }
}

/// 应急预留 in 附件存储: its size as the group writes sizes (MiB), and under it why it is there; no action.
private struct EmergencyReserveRow: View {
    let bytes: Int64

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(alignment: .firstTextBaseline, spacing: 10) {
                Text(L10n.machineStatusEmergencyReserve()).font(.body).foregroundStyle(DS.ink)
                Spacer(minLength: 0)
                Text(bytes % (1 << 20) == 0 ? wholeBytes(bytes) : imageBytes(bytes)).font(.subheadline).foregroundStyle(DS.ink2)
            }
            Text(L10n.machineStatusEmergencyReserveNote()).font(.footnote).foregroundStyle(DS.ink2)
        }
        .padding(.horizontal, 14).padding(.vertical, 11)
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .combine)
    }
}
