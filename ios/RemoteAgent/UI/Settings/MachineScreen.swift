import RACore
import RAProto
import SwiftUI

/// A computer's details and daemon settings (Android's DeviceScreen): what
/// it is, why it may be out of reach, its CLIs and their account quotas,
/// the defaults new projects start from, where it keeps uploaded images,
/// its projects, and removing it from the account.
struct MachineScreen: View {
    let id: String
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @State private var settings: MachineSettingsModel?
    @State private var remove = ActionRunner()
    @State private var confirming = false
    @State private var cleaning = false
    @State private var picking: Pick?

    /// What a sheet of this page chooses.
    private enum Pick: Identifiable, Hashable {
        case model(PBAgent), mode(PBAgent), timeout, imageQuota

        var id: String {
            switch self {
            case .model(let agent): "model-\(agent.rawValue)"
            case .mode(let agent): "mode-\(agent.rawValue)"
            case .timeout: "timeout"
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
        SettingsPage(refresh: refresh(live)) {
            if let m {
                Ticking { now in details(m, state: state, now: now) }
            } else {
                PageNote("这台设备已经移除了")
            }
        }
        .navBar(
            m == nil ? "电脑" : machineLabel(state, id: id),
            subtitle: m.flatMap { machineStatus($0, state: state, now: nowMs()) }.map { $0.online ? "在线" : "不在线" },
            dot: m.flatMap { machineStatus($0, state: state, now: nowMs()) }.map { $0.online ? Theme.green : Theme.gray })
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
            let now = approvalTimeoutMinutes.firstIndex(of: Int(current.approvalTimeoutSeconds) / 60) ?? -1
            let next = approvalTimeoutMinutes[(now + 1) % approvalTimeoutMinutes.count]
            Task { await settings.save(current.with(timeoutMinutes: next)) }
        }
        .debugSignal("machine.remove") {
            let remote = context.remote
            remove.run({ try await remote.removeDevice(id) }, onDone: nav.back)
        }
        .confirm(
            isPresented: $confirming, title: "移除 \(machineLabel(state, id: id))？", text: RemovalText.machineConfirm,
            confirm: "移除", danger: true
        ) {
            let remote = context.remote
            remove.run({ try await remote.removeDevice(id) }, onDone: nav.back)
        }
        .confirm(
            isPresented: $cleaning, title: "清理可回收图片？",
            text: "清除可回收原图和缓存，尚未同步的大图会只显示预览。排队和当前执行中的原图、会话历史保留。", confirm: "清理"
        ) {
            guard let settings, let current = settings.settings else { return }
            Task { await settings.save(current, clean: true) }
        }
    }

    // MARK: The page

    @ViewBuilder private func details(_ m: MachineView, state: RemoteState, now: Int64) -> some View {
        let name = machineLabel(state, id: id)
        let mismatch = protocolMismatch(m)
        let status = machineStatus(m, state: state, now: now)
        if let mismatch { GroupBanner(mismatch, tone: .warn) }
        if let status, !status.online { GroupBanner(offlineText(m, now: now)) }

        GroupLabel("电脑")
        GroupCard {
            GRow("名称", value: name)
            GRow("系统", value: osName(m.meta?.os ?? ""))
            GRow("设备 ID", value: "…" + shortId(id))
        }

        let groups = quotaGroups(asKnown(m, state: state), now: now)
        // A computer that has not said what it runs has no CLI to list a quota for.
        if !groups.isEmpty, m.meta != nil {
            // A line for each CLI's quota, leading to all of it.
            GroupLabel("账户额度")
            GroupCard {
                for (i, g) in groups.enumerated() {
                    GRow(quotaRowTitle(g, among: groups, at: i), value: quotaLine(g), chevron: true) { nav.open(.quota(machine: id)) }
                }
            }
        }

        if let meta = m.meta {
            let agents = shownAgents(meta)
            GroupLabel("CLI 版本")
            GroupCard {
                for a in agents {
                    GRow(agentName(a.agent), value: a.version.or("未知"))
                        .valueLead {
                            Text(a.verified ? "已验证 ·" : "未验证 ·").type(T.sm, a.verified ? Theme.muted : Theme.orangeInk)
                        }
                }
            }
            if agents.contains(where: { !$0.verified }) { GroupNote("未验证的版本请在电脑上运行 remote-agent doctor") }
        }

        daemonSettings(m, state: state, name: name, mismatch: mismatch != nil)

        let projects = machineProjects(state, machineId: id)
        if !projects.isEmpty {
            GroupLabel("项目")
            GroupCard {
                for cwd in projects {
                    GRow(projectName(cwd), value: shortPath(cwd), chevron: true) { nav.open(.project(key: cwd)) }
                }
            }
        }

        GroupCard {
            GRow(remove.running ? "正在移除…" : "移除这台机器…", danger: true, enabled: !remove.running && state.conn == .online) { confirming = true }
        }
        GroupNote(RemovalText.machineNote)
        if !remove.error.isEmpty { GroupBanner(remove.error, tone: .error) }
    }

    /// Why a computer is not there. The server only knows the daemon's
    /// connection is gone, so all three possibilities are said (architecture
    /// §9 「电脑为什么不在线」), in the words of the session's offline card.
    private func offlineText(_ m: MachineView, now: Int64) -> String {
        let since = whenText(m.lastSeenMs, now: now)
        return (since.isEmpty ? "" : "\(since)断开，") + "可能是合盖睡眠、断网，或 remote-agent 没在运行。电脑上的会话可能还在跑，重新连上后这里会自动更新。"
    }

    /// The CLIs the computer reports; one that reports none is an older Codex-only daemon's.
    private func shownAgents(_ meta: PBMachineMeta) -> [PBAgentInfo] {
        if !meta.agents.isEmpty { return meta.agents }
        return [.with {
            $0.agent = .codex
            $0.version = meta.codexVersion
            $0.verified = meta.codexVerified
        }]
    }

    /// The CLI's name; with several accounts of one CLI, which one, so two rows are not named alike.
    private func quotaRowTitle(_ g: QuotaGroup, among groups: [QuotaGroup], at index: Int) -> String {
        let name = agentName(g.agent)
        guard groups.filter({ $0.agent == g.agent }).count > 1 else { return name }
        let parts = g.title.components(separatedBy: " · ")
        return parts.count > 1 ? name + " · " + (parts.last ?? "") : name
    }

    // MARK: Daemon settings

    @ViewBuilder private func daemonSettings(_ m: MachineView, state: RemoteState, name: String, mismatch: Bool) -> some View {
        let s = settings?.settings
        let shown = reachable(state)
        if state.unlinked {
            GroupNote("连上服务器后才能读取和修改新会话默认设置")
        } else if !m.online {
            GroupNote("\(name) 不在线，连上后才能读取和修改新会话默认设置")
        } else if mismatch {
            // The notice at the top says which side to update; nothing is sent meanwhile.
            EmptyView()
        } else if let settings, let s {
            let busy = settings.saving || !shown
            let agents = machineAgents(m)
            ForEach(agents, id: \.rawValue) { agent in
                let d = s.defaults(for: agent)
                // What a session started without a mode really gets, as the machine reads it.
                let effective = defaultModeOf(m, agent: agent)
                let chosen = modeOf(d.mode)
                GroupLabel("\(agentName(agent)) · 新项目默认设置")
                GroupCard {
                    GRow("模型", value: d.model.or("默认"), chevron: true, enabled: !busy) { picking = .model(agent) }
                    GRow("推理强度", value: d.effort.or("默认"), chevron: true, enabled: !busy) { picking = .model(agent) }
                    // One line: that project settings may differ is said in the picker.
                    GRow(
                        "审批模式",
                        value: chosen?.text ?? modeLabel(chosen: nil, default: DefaultMode(mode: effective.mode, known: effective.known, varies: false), agent: agent),
                        chevron: true, enabled: !busy,
                        valueColor: chosen == nil && (effective.mode == .full || !effective.known) ? Theme.red : nil
                    ) { picking = .mode(agent) }
                    // The timeout is the daemon's, not a CLI's: it is listed once, with Codex or the only CLI there is.
                    if agent == .codex || agents.count == 1 {
                        GRow("审批超时", value: timeoutText(seconds: s.approvalTimeoutSeconds), chevron: true, enabled: !busy) { picking = .timeout }
                    }
                }
            }
            if settings.saving { LoadingRow("正在保存…").padding(.horizontal, 14).padding(.bottom, 4) }
            if !settings.saveError.isEmpty {
                if settings.saveUnknown {
                    GroupBanner(settings.saveError, tone: .warn, action: "重新读取") { Task { await settings.read() } }
                } else {
                    GroupBanner("没能保存：\(settings.saveError)", tone: .error)
                }
            }
            if !settings.readError.isEmpty {
                GroupBanner("没能读取设置：\(settings.readError)", tone: .error, action: "重试") { Task { await settings.read() } }
            }
            imageStorage(s, usage: settings.imageStorage, enabled: !busy)
        } else if let settings, !settings.readError.isEmpty {
            GroupLabel("新项目默认设置")
            GroupBanner("没能读取设置：\(settings.readError)", tone: .error, action: "重试") { Task { await settings.read() } }
        } else {
            GroupLabel("新项目默认设置")
            LoadingRow().padding(.horizontal, 14).padding(.bottom, 4)
        }
    }

    /// Where the computer keeps the images sent to its sessions (Android's MachineImageStorage).
    @ViewBuilder private func imageStorage(_ s: PBDaemonSettings, usage: PBImageStorageUsage?, enabled: Bool) -> some View {
        let free = usage.map { $0.reclaimableBytes + $0.cacheBytes } ?? 0
        GroupLabel("图片存储")
        GroupCard {
            GRow("上传的图片", value: usage.map(uploadsText) ?? "读取中…")
            GRow("空间上限", value: wholeBytes(s.imageUploadQuotaBytes), chevron: true, enabled: enabled) { picking = .imageQuota }
            GRow("预览缓存", value: usage.map { imageBytes($0.cacheBytes) } ?? "—")
            GRow(
                "清理…", value: usage.map { _ in free > 0 ? "可释放 \(imageBytes(free))" : "没有可清理的" }, action: true,
                enabled: enabled && free > 0
            ) { cleaning = true }
        }
        GroupNote("图片存在这台电脑上。正在发送和执行中用到的图片不会被清理；预览缓存最多 150 MiB，自动回收。")
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
                DefaultModelSheet(machine: id, agent: agent, current: s.defaults(for: agent)) { model, effort in
                    save(s.with(model: model, effort: effort, for: agent))
                }
            case .mode(let agent):
                let effective = defaultModeOf(state.machines[id], agent: agent)
                let current = modeOf(s.defaults(for: agent).mode)
                SheetHead("\(agentName(agent)) · 审批模式")
                ForEach(Array(defaultModeChoices.enumerated()), id: \.offset) { i, mode in
                    ListItem(
                        mode?.text ?? "默认",
                        desc: mode.map { modeHelp($0).replacingOccurrences(of: "CLI", with: agentName(agent)) }
                            ?? "不指定时实际用：" + modeLabel(chosen: nil, default: effective, agent: agent).removingPrefix("默认："),
                        checked: mode == current || (mode == nil && !defaultModeChoices.contains(current)),
                        last: i == defaultModeChoices.count - 1
                    ) { save(s.with(mode: mode, for: agent)) }
                }
            case .timeout:
                SheetHead("审批超时")
                ForEach(approvalTimeoutMinutes, id: \.self) { minutes in
                    ListItem(
                        "\(minutes) 分钟", checked: Int(s.approvalTimeoutSeconds) == minutes * 60,
                        last: minutes == approvalTimeoutMinutes.last
                    ) { save(s.with(timeoutMinutes: minutes)) }
                }
                Hint("没人处理的审批过了这么久就按拒绝算。")
            case .imageQuota:
                SheetHead("空间上限")
                ForEach(imageQuotaChoices, id: \.self) { bytes in
                    ListItem(wholeBytes(bytes), checked: s.imageUploadQuotaBytes == bytes, last: bytes == imageQuotaChoices.last) {
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
        case "failed": s.show(settings: nil, readError: "电脑不在线，指令没有发出")
        case "saveFailed": s.show(settings: SettingsDemo.daemonSettings, storage: SettingsDemo.imageStorage, saveError: "电脑不在线，指令没有发出")
        case "saveUnknown":
            s.show(
                settings: SettingsDemo.daemonSettings, storage: SettingsDemo.imageStorage,
                saveError: "不确定电脑有没有保存这次修改，重新读取后以电脑上的为准", saveUnknown: true)
        default: s.show(settings: SettingsDemo.daemonSettings, storage: SettingsDemo.imageStorage)
        }
    }
    #endif
}

private extension String {
    func removingPrefix(_ prefix: String) -> String { hasPrefix(prefix) ? String(dropFirst(prefix.count)) : self }
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

    var body: some View {
        let chosen = model ?? current.model
        SheetHead("\(agentName(agent)) · 新会话默认模型")
        Group {
            if !error.isEmpty {
                Banner("没能读取模型列表：\(error)", tone: .error, action: "重试") {
                    error = ""
                    attempt += 1
                }
            } else if let models {
                ListItem("默认（\(agentName(agent)) 自己的配置）", checked: chosen.isEmpty) { onPick("", "") }
                ForEach(Array(models.models.enumerated()), id: \.offset) { i, m in
                    let note = [contextText(tokens: m.contextWindow), m.efforts.isEmpty ? "无推理强度" : nil].compactMap { $0 }.joined(separator: " · ")
                    ListItem(
                        m.displayName.or(m.slug), checked: m.slug == chosen, small: note.isEmpty ? nil : note,
                        last: i == models.models.count - 1
                    ) {
                        // A model without efforts is chosen at once; one with them waits for the effort.
                        if m.efforts.isEmpty { onPick(m.slug, "") } else { model = m.slug }
                    }
                }
                if let m = models.models.first(where: { $0.slug == chosen }), !m.efforts.isEmpty {
                    FieldLabel("推理强度")
                    ChoiceGrid(m.efforts, selected: current.model == m.slug ? current.effort : nil) { onPick(m.slug, $0) }
                }
            } else {
                LoadingRow().padding(.horizontal, -Theme.Space.page)
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
