// 电脑状态 (ui-spec A3; 界面稿 v2 第 ② 部分): renders Core's `PBMachineStatusViewModel` directly.
import RACore
import RAProto
import SwiftUI

/// What the UI tests look for.
enum StatusID: String {
    case chart = "status-chart"
    case diskCard = "status-disk-card"
    case cpuCard = "status-cpu-card"
    case memoryCard = "status-memory-card"
    case loading = "status-loading"
}

private let swapMs = 350.0

private struct StatusWatchTaskKey: Equatable {
    let id: String
    let foreground: Bool
}

private func swapOldAlpha(_ tMs: Double) -> Double {
    tMs <= 100 ? max(0, 1 - tMs / 100) : 0
}

private func swapNewAlpha(_ tMs: Double) -> Double {
    tMs >= 230 ? min(1, max(0, (tMs - 230) / 120)) : 0
}

// MARK: The swap of the main chart and a small card

private enum SwapRole: Hashable {
    case bg(StatusMetric), hero(StatusMetric), small(StatusMetric), disk
}

private struct SwapRoleKey: LayoutValueKey {
    static let defaultValue = SwapRole.disk
}

private func lerp(_ a: CGRect, _ b: CGRect, _ t: Double) -> CGRect {
    let f = CGFloat(t)
    return CGRect(
        x: a.minX + (b.minX - a.minX) * f,
        y: a.minY + (b.minY - a.minY) * f,
        width: a.width + (b.width - a.width) * f,
        height: a.height + (b.height - a.height) * f
    )
}

/// The main chart and the two small cards: interpolates all three frames during the CPU/memory swap.
private struct SwapLayout: Layout {
    var t: Double
    var stacked = false
    var animatableData: Double {
        get { t }
        set { t = newValue }
    }
    private let gap: CGFloat = 12

    private struct Solved {
        var cpu: CGRect, memory: CGRect, disk: CGRect, height: CGFloat
    }

    private func frames(hero: CGFloat, small: CGFloat, disk: CGFloat, width: CGFloat) -> (hero: CGRect, small: CGRect, disk: CGRect, height: CGFloat) {
        let heroR = CGRect(x: 0, y: 0, width: width, height: hero)
        if stacked {
            let smallR = CGRect(x: 0, y: hero + gap, width: width, height: max(168, small))
            let diskR = CGRect(x: 0, y: smallR.maxY + gap, width: width, height: max(168, disk))
            return (heroR, smallR, diskR, diskR.maxY)
        }
        let half = (width - gap) / 2
        let row = max(168, small, disk)
        return (heroR, CGRect(x: 0, y: hero + gap, width: half, height: row), CGRect(x: half + gap, y: hero + gap, width: half, height: row), hero + gap + row)
    }

    private func solve(_ subviews: Subviews, width: CGFloat) -> Solved {
        var map = [SwapRole: LayoutSubview]()
        for s in subviews { map[s[SwapRoleKey.self]] = s }
        let small = stacked ? width : (width - gap) / 2
        func height(_ role: SwapRole, _ w: CGFloat) -> CGFloat {
            map[role]?.sizeThatFits(ProposedViewSize(width: w, height: nil)).height ?? 0
        }
        let heroCPU = height(.hero(.cpu), width), heroMem = height(.hero(.memory), width)
        let smallCPU = height(.small(.cpu), small), smallMem = height(.small(.memory), small), disk = height(.disk, small)
        let a = frames(hero: heroCPU, small: smallMem, disk: disk, width: width)
        let b = frames(hero: heroMem, small: smallCPU, disk: disk, width: width)
        return Solved(
            cpu: lerp(a.hero, b.small, t),
            memory: lerp(a.small, b.hero, t),
            disk: lerp(a.disk, b.disk, t),
            height: a.height + (b.height - a.height) * CGFloat(t)
        )
    }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let w = proposal.width ?? 358
        return CGSize(width: w, height: solve(subviews, width: w).height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        let solved = solve(subviews, width: bounds.width)
        for s in subviews {
            let frame: CGRect
            switch s[SwapRoleKey.self] {
            case .bg(let m), .hero(let m), .small(let m): frame = m == .cpu ? solved.cpu : solved.memory
            case .disk: frame = solved.disk
            }
            s.place(at: CGPoint(x: bounds.minX + frame.minX, y: bounds.minY + frame.minY), anchor: .topLeading, proposal: ProposedViewSize(frame.size))
        }
    }
}

private struct SwapFade: ViewModifier, @preconcurrency Animatable {
    var t: Double
    let isNew: Bool
    var animatableData: Double {
        get { t }
        set { t = newValue }
    }

    func body(content: Content) -> some View {
        content.opacity(isNew ? swapNewAlpha(t * swapMs) : swapOldAlpha(t * swapMs))
    }
}

// MARK: Connected wrapper

struct MachineStatusScreen: View {
    /// `PrepareCleanupSessionEffect.prompt` carries the i18n key `machine_status.cleanup_prompt` from `clientcore`;
    /// resolve it through `L10n` instead of pasting the raw key into the draft.
    static func resolvedCleanupPrompt(_ raw: String) -> String {
        if raw.isEmpty || raw == "machine_status.cleanup_prompt" {
            return L10n.machineStatusCleanupPrompt()
        }
        return raw
    }

    let id: String
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @State private var cleanup: CleanupStep?
    @State private var main = StatusMetric.cpu

    private enum CleanupStep: Identifiable {
        case pickFolder
        case task(key: String, machine: String, prompt: String)
        var id: Int {
            switch self {
            case .pickFolder: 0
            case .task: 1
            }
        }
    }

    private var displayedViewModel: PBMachineStatusViewModel {
        let current = context.core.machineStatus
        if current.machineID == id { return current }
        let m = context.core.settings.machines.first { $0.machineID == id }
        return PBMachineStatusViewModel.with {
            $0.machineID = id
            $0.machineName = (m?.name ?? "").or(String(id.prefix(8))).or(L10n.commonUnknownComputer())
            $0.found = m != nil
            $0.supported = true
            $0.online = m?.online ?? false
            $0.pollState = .loading
        }
    }

    var body: some View {
        let vm = displayedViewModel
        MachineStatusScreenContent(
            vm: vm,
            main: $main,
            onRetry: {
                context.dispatch { $0.machineStatusRetry.machineID = id }
            },
            onScrub: { kind, index, clear in
                context.dispatch {
                    $0.machineStatusScrub = .with {
                        $0.machineID = id
                        $0.kind = kind
                        $0.pointIndex = index
                        $0.clear = clear
                    }
                }
            },
            onSelectTab: { tab in
                context.dispatch {
                    $0.machineStatusTab = .with {
                        $0.machineID = id
                        $0.tab = tab
                    }
                }
            },
            onOpenStorage: { nav.open(.machineStorage(id: id)) },
            onCleanup: {
                context.dispatch { $0.machineStatusCleanup.machineID = id }
            },
            onOpenSession: { sid in nav.open(.session(id: sid)) },
            onOpenUsage: { agent, model in nav.open(.usage(machine: id, agent: agent, model: model)) }
        )
        .task(id: id) {
            if HomeLaunch.value("-machineList") == "processes" {
                await context.coreHost.dispatch(PBCoreEvent.with {
                    $0.machineStatusTab = .with {
                        $0.machineID = id
                        $0.tab = .processes
                    }
                })
            }
            if let f = HomeLaunch.value("-machineScrub").flatMap(Double.init) {
                await applyLaunchScrub(fraction: f)
            }
        }
        .task(id: StatusWatchTaskKey(id: id, foreground: model.foreground)) {
            guard model.foreground else { return }
            let host = context.coreHost
            let token = await host.acquireMachineStatusWatch(id, mode: .live)
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(3600))
            }
            await host.releaseMachineStatusWatch(token)
        }
        .onChange(of: context.core.pendingCleanup?.id) { _, _ in
            guard let pending = context.core.pendingCleanup, pending.effect.machineID == id else { return }
            context.core.consumeCleanup(pending.id)
            let eff = pending.effect
            let prompt = Self.resolvedCleanupPrompt(eff.prompt)
            if eff.needsDirectoryPicker {
                cleanup = .pickFolder
            } else {
                cleanup = .task(key: eff.projectKey, machine: eff.machineID, prompt: prompt)
            }
        }
        .sheet(item: $cleanup) { step in
            NavigationStack {
                switch step {
                case .pickFolder:
                    VStack(spacing: 0) {
                        SheetHead(L10n.projectsNewChooseDirectory(), action: L10n.commonCancel()) { cleanup = nil }
                        CoreDirPicker(pickerKey: "machine-status-\(id)", machine: id, forNewTask: true) { pickedMachine, path in
                            cleanup = .task(key: path, machine: pickedMachine, prompt: L10n.machineStatusCleanupPrompt())
                        }
                    }
                case .task(let key, let machine, let prompt):
                    NewTaskScreen(key: key, machine: machine, cleanupPrompt: prompt, onClose: { cleanup = nil }) { sid in
                        cleanup = nil
                        nav.open(.session(id: sid))
                    }
                }
            }
            .sheetSurface()
            .presentationDetents([.large])
        }
    }

    private func applyLaunchScrub(fraction: Double) async {
        #if DEBUG
        for _ in 0..<200 {
            let chart = main == .cpu ? context.core.machineStatus.cpu.chart : context.core.machineStatus.memory.chart
            if context.core.machineStatus.machineID == id, !chart.points.isEmpty {
                let i = min(max(Int((Double(chart.points.count - 1) * fraction).rounded()), 0), chart.points.count - 1)
                await context.coreHost.dispatch(PBCoreEvent.with {
                    $0.machineStatusScrub = .with {
                        $0.machineID = id
                        $0.kind = main == .cpu ? .cpu : .memory
                        $0.pointIndex = Int32(i)
                        $0.clear = false
                    }
                })
                return
            }
            try? await Task.sleep(for: .milliseconds(20))
        }
        #endif
    }
}

// MARK: Pure presentation view

struct MachineStatusScreenContent: View {
    let vm: PBMachineStatusViewModel
    @Binding var main: StatusMetric
    let onRetry: () -> Void
    let onScrub: (PBMachineChartKind, Int32, Bool) -> Void
    let onSelectTab: (PBMachineUsageTab) -> Void
    let onOpenStorage: () -> Void
    let onCleanup: () -> Void
    let onOpenSession: (String) -> Void
    var onOpenUsage: (PBAgent, String) -> Void = { _, _ in }

    @Environment(\.accessibilityReduceMotion) private var reduce
    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var position = 0.0
    @State private var linear = 1.0
    @State private var swapping = false

    private var activeChart: PBMachineChartView {
        main == .cpu ? vm.cpu.chart : vm.memory.chart
    }

    var body: some View {
        let name = vm.machineName.or(vm.machineID)
        ZStack {
            DS.liftPage.ignoresSafeArea()
            content(name: name)
        }
        .navigationBarTitleDisplayMode(.inline)
        .navigationTitle(name)
        .toolbar { ToolbarItem(placement: .principal) { Color.clear.frame(width: 1, height: 1).accessibilityHidden(true) } }
        .onAppear { applyDebugSwapLaunch() }
    }

    @ViewBuilder private func content(name: String) -> some View {
        let hasData = vm.cpu.hasValue_p || vm.memory.hasValue_p
        let loading = vm.pollState == .loading && !hasData
        let unavailable = (!vm.found || !vm.supported || vm.pollState == .unavailable) && !hasData

        if loading {
            ScrollView {
                skeleton(name: name)
                    .padding(.horizontal, 16)
                    .padding(.bottom, 30)
                    .frame(maxWidth: DS.column)
            }
        } else if unavailable {
            VStack(alignment: .leading, spacing: 0) {
                header(name: name, badge: vm.statusBadge.rendered(), dot: vm.statusDot)
                Spacer()
                VStack(spacing: 2) {
                    Text(vm.unavailableTitle.rendered().or(L10n.machineStatusUnavailable()))
                        .font(.body)
                        .foregroundStyle(DS.ink2)
                        .multilineTextAlignment(.center)
                    if vm.showRetry {
                        LinkButton(L10n.commonRetry(), action: onRetry).font(.body)
                    }
                }
                .frame(maxWidth: .infinity)
                Spacer()
            }
            .padding(.horizontal, 16)
            .frame(maxWidth: DS.column)
        } else {
            ScrollView {
                page(vm, name: name)
                    .padding(.horizontal, 16)
                    .padding(.bottom, 30)
                    .frame(maxWidth: DS.column)
            }
            .frame(maxWidth: .infinity)
            .startAtEndForScreenshots()
        }
    }

    private func header(name: String, badge: String, dot: PBMachineStatusDot) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(name)
                .font(.largeTitle.bold())
                .foregroundStyle(DS.ink)
                .lineLimit(2)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityAddTraits(.isHeader)
            if !badge.isEmpty {
                LiveLine(dot: dot, text: badge)
            }
        }
        .padding(.horizontal, 4).padding(.top, 6)
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func skeleton(name: String) -> some View {
        let other: StatusMetric = main == .cpu ? .memory : .cpu
        return VStack(alignment: .leading, spacing: 0) {
            header(name: name, badge: L10n.machineStatusLoading(), dot: .grayStatic)
            VStack(alignment: .leading, spacing: 0) {
                Kick(symbol: main == .cpu ? .cpu : .memory, label: main == .cpu ? L10n.machineStatusCpu() : L10n.machineStatusMemory(), color: DS.ink2)
                Text("—").figure(52, style: .largeTitle, limit: 1.6).foregroundStyle(DS.ink3).padding(.top, 4)
                axis.padding(.top, 8 + 104)
            }
            .padding(.top, 16).padding(.horizontal, 18).frame(maxWidth: .infinity, alignment: .leading)
            .statusCard().padding(.top, 18)
            HStack(alignment: .top, spacing: 12) {
                tileSkeleton(symbol: other == .memory ? .memory : .cpu, label: other == .memory ? L10n.machineStatusMemory() : L10n.machineStatusCpu())
                tileSkeleton(symbol: .disk, label: L10n.machineStatusDisk())
            }
            .padding(.top, 12)
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier(StatusID.loading.rawValue)
    }

    private func tileSkeleton(symbol: StatusSymbol, label: String) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            Kick(symbol: symbol, label: label, color: DS.ink2)
            Text("—").figure(34, style: .title, limit: 1.6).foregroundStyle(DS.ink3).padding(.top, 6)
            Spacer(minLength: 0)
        }
        .padding(.init(top: 14, leading: 16, bottom: 15, trailing: 16))
        .frame(maxWidth: .infinity, minHeight: 168, alignment: .topLeading)
        .statusCard()
    }

    private var axis: some View {
        HStack {
            Text(L10n.machineStatusAxis2Min())
            Spacer(minLength: 4)
            Text(L10n.machineStatusAxis1Min())
            Spacer(minLength: 4)
            Text(L10n.machineStatusAxisNow())
        }
        .font(.caption2).foregroundStyle(DS.ink3).lineLimit(1).minimumScaleFactor(0.8)
        .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
        .padding(.top, 6).padding(.bottom, 13)
    }

    private func page(_ vm: PBMachineStatusViewModel, name: String) -> some View {
        let badge = activeChart.hasScrub_p ? activeChart.scrubAge.rendered() : vm.statusBadge.rendered()
        let footParts = [vm.uptimeText.rendered(), vm.batteryText.rendered()].filter { !$0.isEmpty }
        return VStack(alignment: .leading, spacing: 0) {
            header(name: name, badge: badge, dot: vm.statusDot)
            swapGrid(vm).padding(.top, 18)
            if vm.disk.showActions {
                diskActions(vm.disk).padding(.top, 12)
            }
            usageHeader(vm.topUsage).padding(.top, 26)
            usageList(vm.topUsage, dim: vm.dim)
            if vm.hasModelUsage && (vm.modelUsage.supported || vm.modelUsage.hasUnsupportedFootnote) {
                modelUsageSection(vm.modelUsage, dim: vm.dim).padding(.top, 26)
            }
            if !footParts.isEmpty {
                Text(footParts.joined(separator: " · "))
                    .font(.footnote)
                    .foregroundStyle(DS.ink3)
                    .multilineTextAlignment(.center)
                    .frame(maxWidth: .infinity)
                    .padding(.top, 22)
            }
        }
    }

    private func swapGrid(_ vm: PBMachineStatusViewModel) -> some View {
        let moving = vm.pollState == .live && !vm.dim && !reduce && !HomeLaunch.flag("-machineFrozen")
        return SwapLayout(t: position, stacked: typeSize.isAccessibilitySize) {
            ForEach([StatusMetric.cpu, .memory], id: \.self) { m in
                StatusCardBackground().layoutValue(key: SwapRoleKey.self, value: .bg(m))
                tileContent(m, hero: true, vm: vm, moving: moving)
                    .modifier(SwapFade(t: linear, isNew: main == m))
                    .allowsHitTesting(main == m)
                    .accessibilityHidden(main != m)
                    .layoutValue(key: SwapRoleKey.self, value: .hero(m))
                tileContent(m, hero: false, vm: vm, moving: moving)
                    .modifier(SwapFade(t: linear, isNew: main != m))
                    .allowsHitTesting(main != m)
                    .accessibilityHidden(main == m)
                    .layoutValue(key: SwapRoleKey.self, value: .small(m))
            }
            DiskTile(disk: vm.disk, dim: vm.dim, onTap: onOpenStorage)
                .layoutValue(key: SwapRoleKey.self, value: .disk)
        }
    }

    @ViewBuilder private func tileContent(_ m: StatusMetric, hero: Bool, vm: PBMachineStatusViewModel, moving: Bool) -> some View {
        if hero {
            let chartKind: PBMachineChartKind = m == .cpu ? .cpu : .memory
            let chart = m == .cpu ? vm.cpu.chart : vm.memory.chart
            HeroContent(
                metric: m,
                cpu: vm.cpu,
                mem: vm.memory,
                dim: vm.dim,
                chartOn: m == main || swapping,
                moving: moving,
                onScrub: { idx, clear in onScrub(chartKind, idx, clear) },
                onStep: { delta in step(chart: chart, kind: chartKind, delta: delta) }
            )
        } else {
            SmallTile(metric: m, cpu: vm.cpu, mem: vm.memory, dim: vm.dim) { pick(m) }
        }
    }

    private func step(chart: PBMachineChartView, kind: PBMachineChartKind, delta: Int) {
        guard !chart.points.isEmpty else { return }
        let base = chart.hasScrub_p ? Int(chart.scrubIndex) : chart.points.count - 1
        let next = min(max(base + delta, 0), chart.points.count - 1)
        onScrub(kind, Int32(next), false)
    }

    private func pick(_ next: StatusMetric) {
        guard next != main else { return }
        let prevKind: PBMachineChartKind = main == .cpu ? .cpu : .memory
        if activeChart.hasScrub_p {
            onScrub(prevKind, 0, true)
        }
        let target = next == .cpu ? 0.0 : 1.0
        if reduce {
            main = next
            position = target
            return
        }
        swapping = true
        main = next
        var instant = Transaction()
        instant.disablesAnimations = true
        withTransaction(instant) { linear = 0 }
        withAnimation(.timingCurve(0.2, 0.8, 0.2, 1, duration: swapMs / 1000)) { position = target }
        DispatchQueue.main.async { withAnimation(.linear(duration: swapMs / 1000)) { linear = 1 } }
        DispatchQueue.main.asyncAfter(deadline: .now() + swapMs / 1000 + 0.05) { swapping = false }
    }

    private func diskActions(_ disk: PBMachineDiskSection) -> some View {
        VStack(spacing: 0) {
            Button(action: onCleanup) {
                HStack {
                    Text(disk.cleanupActionLabel.rendered().or(L10n.machineStatusCleanupAction()))
                        .font(.body)
                        .foregroundStyle(DS.blue)
                    Spacer(minLength: 0)
                }
                .padding(.horizontal, 16).padding(.vertical, 11).frame(minHeight: 48)
                .contentShape(Rectangle())
            }
            .buttonStyle(RowPressStyle())
            Rectangle().fill(DS.sep).frame(height: 0.5).padding(.leading, 16)
            Button(action: onOpenStorage) {
                HStack(spacing: 8) {
                    Text(disk.agentUsageLabel.rendered().or(L10n.machineStatusAgentUsage()))
                        .font(.body)
                        .foregroundStyle(DS.ink)
                    Spacer(minLength: 8)
                    let val = disk.agentUsageValue.rendered()
                    if !val.isEmpty {
                        Text(val).figure(17, .regular, style: .body, limit: 1.6).foregroundStyle(DS.ink2)
                    }
                    Arrow(DS.ink3)
                }
                .padding(.horizontal, 16).padding(.vertical, 11).frame(minHeight: 48)
                .contentShape(Rectangle())
            }
            .buttonStyle(RowPressStyle())
        }
        .statusCard()
    }

    private func usageHeader(_ top: PBMachineTopUsageSection) -> some View {
        let isProcesses = top.selectedTab == .processes
        let title = Text(L10n.machineStatusTopUsage())
            .font(.title3.bold())
            .foregroundStyle(DS.ink)
            .accessibilityAddTraits(.isHeader)
        let toggle = Button {
            onSelectTab(isProcesses ? .sessions : .processes)
        } label: {
            Text(isProcesses ? L10n.machineStatusShowSessions() : L10n.machineStatusShowProcesses())
                .font(.subheadline)
                .foregroundStyle(DS.blue)
                .multilineTextAlignment(.leading)
                .frame(minHeight: DS.minHit)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)

        return ViewThatFits(in: .horizontal) {
            HStack(alignment: .firstTextBaseline) {
                title
                Spacer(minLength: 8)
                toggle
            }
            VStack(alignment: .leading, spacing: 0) {
                title.fixedSize(horizontal: false, vertical: true)
                toggle
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(.horizontal, 4).padding(.bottom, -2)
    }

    private func usageList(_ top: PBMachineTopUsageSection, dim: Bool) -> some View {
        let rows = top.rows
        return VStack(spacing: 0) {
            if rows.isEmpty {
                Text(top.emptyText.rendered().or(L10n.machineStatusNoRunningSessions()))
                    .font(.footnote)
                    .foregroundStyle(DS.ink2)
                    .multilineTextAlignment(.center)
                    .frame(maxWidth: .infinity)
                    .padding(16)
            }
            ForEach(Array(rows.enumerated()), id: \.element.id) { i, row in
                let bar = main == .cpu ? row.cpuBarFraction : row.memoryBarFraction
                let primary = main == .cpu ? row.cpuValueText.rendered() : row.memoryValueText.rendered()
                UsageRowView(
                    row: row,
                    primary: primary,
                    bar: bar,
                    top: i == 0,
                    last: i == rows.count - 1,
                    dim: dim
                ) {
                    if !row.sessionID.isEmpty { onOpenSession(row.sessionID) }
                }
            }
        }
        .padding(.vertical, 2)
        .frame(maxWidth: .infinity)
        .animation(reduce ? nil : .timingCurve(0.2, 0.8, 0.2, 1, duration: 0.3), value: rows.map(\.id))
        .statusCard()
    }

    private func modelUsageSection(_ preview: PBMachineModelUsagePreview, dim: Bool) -> some View {
        let windowText = preview.hasWindowLabel ? preview.windowLabel.rendered() : ""
        return VStack(alignment: .leading, spacing: 0) {
            HStack(alignment: .firstTextBaseline) {
                Text(L10n.usageTitle())
                    .font(.title3.bold())
                    .foregroundStyle(DS.ink)
                    .accessibilityAddTraits(.isHeader)
                Spacer(minLength: 8)
                if preview.supported {
                    Button {
                        onOpenUsage(.unspecified, "")
                    } label: {
                        HStack(spacing: 4) {
                            if !windowText.isEmpty {
                                Text(windowText)
                                    .font(.subheadline)
                                    .foregroundStyle(DS.ink2)
                            }
                            Arrow(DS.ink3)
                        }
                        .frame(minHeight: DS.minHit)
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                } else if !windowText.isEmpty {
                    Text(windowText)
                        .font(.subheadline)
                        .foregroundStyle(DS.ink2)
                }
            }
            .padding(.horizontal, 4)
            .padding(.bottom, 6)

            VStack(spacing: 0) {
                if preview.loading && preview.rows.isEmpty {
                    LoadingRow(inset: 16)
                } else if !preview.rows.isEmpty {
                    ForEach(Array(preview.rows.enumerated()), id: \.offset) { idx, row in
                        let title = row.hasTitleText ? row.titleText.rendered() : row.model
                        let tokens = row.hasTotalTokensText ? row.totalTokensText.rendered() : ""
                        let meta = row.hasMetaText ? row.metaText.rendered() : ""
                        Button {
                            onOpenUsage(row.agent, row.model)
                        } label: {
                            HStack(alignment: .center, spacing: 10) {
                                VStack(alignment: .leading, spacing: 2) {
                                    Text(title)
                                        .font(.callout.weight(.medium))
                                        .foregroundStyle(dim ? DS.ink3 : DS.ink)
                                        .lineLimit(1)
                                        .truncationMode(.middle)
                                    if !meta.isEmpty {
                                        Text(meta)
                                            .font(.footnote)
                                            .foregroundStyle(dim ? DS.ink3 : DS.ink2)
                                            .lineLimit(1)
                                    }
                                }
                                .frame(maxWidth: .infinity, alignment: .leading)
                                if !tokens.isEmpty {
                                    Text(tokens)
                                        .font(.callout.weight(.semibold))
                                        .monospacedDigit()
                                        .foregroundStyle(dim ? DS.ink3 : DS.ink)
                                        .lineLimit(1)
                                }
                                Arrow(DS.ink3)
                            }
                            .padding(.horizontal, 16)
                            .padding(.vertical, 11)
                            .contentShape(Rectangle())
                            .overlay(alignment: .bottom) {
                                if idx < preview.rows.count - 1 {
                                    Rectangle().fill(DS.sep).frame(height: 0.5).padding(.leading, 16)
                                }
                            }
                        }
                        .buttonStyle(RowPressStyle())
                    }
                } else if !preview.supported && preview.hasUnsupportedFootnote {
                    Text(preview.unsupportedFootnote.rendered())
                        .font(.footnote)
                        .foregroundStyle(DS.ink2)
                        .multilineTextAlignment(.center)
                        .frame(maxWidth: .infinity)
                        .padding(16)
                } else if preview.hasEmptyText {
                    Text(preview.emptyText.rendered())
                        .font(.footnote)
                        .foregroundStyle(DS.ink2)
                        .multilineTextAlignment(.center)
                        .frame(maxWidth: .infinity)
                        .padding(16)
                }
            }
            .padding(.vertical, 2)
            .frame(maxWidth: .infinity)
            .statusCard()
        }
    }

    private func applyDebugSwapLaunch() {
        #if DEBUG
        if HomeLaunch.value("-machineMain") == "memory" {
            main = .memory
            position = 1
        }
        if let ms = HomeLaunch.value("-machineSwap").flatMap(Double.init) {
            main = .memory
            swapping = true
            let t = min(max(ms / swapMs, 0), 1)
            linear = t
            position = UnitCurve.bezier(
                startControlPoint: UnitPoint(x: 0.2, y: 0.8),
                endControlPoint: UnitPoint(x: 0.2, y: 1)
            ).value(at: t)
        }
        #endif
    }
}

// MARK: Pieces

private struct LiveLine: View {
    let dot: PBMachineStatusDot
    let text: String
    @Environment(\.accessibilityReduceMotion) private var reduce
    @State private var breathe = false

    var body: some View {
        HStack(spacing: 6) {
            dotView
            Text(text).font(.subheadline).foregroundStyle(DS.ink2).lineLimit(1)
        }
        .accessibilityElement(children: .combine)
    }

    @ViewBuilder private var dotView: some View {
        let color: Color = switch dot {
        case .greenLive: DS.green
        case .orangeStatic: DS.orange
        case .redStatic: DS.red
        default: DS.ink3
        }
        ZStack {
            if dot == .greenLive {
                Circle()
                    .stroke(DS.green.opacity(breathe ? 0 : 0.22), lineWidth: breathe ? 5 : 3)
                    .frame(width: 7 + (breathe ? 5 : 3), height: 7 + (breathe ? 5 : 3))
            }
            Circle().fill(color).frame(width: 7, height: 7)
        }
        .frame(width: 17, height: 17)
        .onAppear {
            guard dot == .greenLive, !reduce else { return }
            withAnimation(.easeInOut(duration: 1.2).repeatForever(autoreverses: true)) { breathe = true }
        }
        .accessibilityHidden(true)
    }
}

private struct Kick: View {
    let symbol: StatusSymbol
    let label: String
    let color: Color

    var body: some View {
        HStack(spacing: 6) {
            Image(systemName: symbol.rawValue).font(.system(size: 14, weight: .semibold))
            Text(label).font(.footnote.weight(.semibold))
        }
        .foregroundStyle(color)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(label)
    }
}

/// Highlights digits inside `3.2 负载` / `Load 3.2`.
private func highlightDigits(_ full: String, bold: Color, rest: Color) -> Text {
    var out = AttributedString(full)
    out.foregroundColor = rest
    out.font = .footnote
    if let r = full.range(of: #"\d+(?:\.\d+)?"#, options: .regularExpression),
       let attrRange = Range(r, in: out)
    {
        out[attrRange].foregroundColor = bold
        out[attrRange].font = Font.system(.subheadline, design: .rounded).weight(.semibold)
    }
    return Text(out)
}

private struct HeroContent: View {
    let metric: StatusMetric
    let cpu: PBMachineCpuSection
    let mem: PBMachineMemorySection
    let dim: Bool
    let chartOn: Bool
    let moving: Bool
    let onScrub: (Int32, Bool) -> Void
    let onStep: (Int) -> Void

    private var chart: PBMachineChartView {
        metric == .cpu ? cpu.chart : mem.chart
    }

    private var figure: (value: String, number: Double, unit: String) {
        if chart.hasScrub_p {
            return (chart.scrubValueText.or("—"), chart.scrubNumericValue, chart.scrubUnit)
        }
        if metric == .cpu {
            return (cpu.valueText.or("—"), cpu.value, cpu.unit)
        }
        let unit = mem.unit == "GB" ? mem.caption.rendered().or("GB") : mem.unit
        return (mem.valueText.or("—"), mem.value, unit)
    }

    private func sideLines(_ alignment: HorizontalAlignment, ink: Color, ink2: Color) -> some View {
        VStack(alignment: alignment, spacing: 0) {
            if metric == .cpu {
                let d2 = cpu.detail2.rendered()
                let d1 = cpu.detail.rendered()
                if !d2.isEmpty { highlightDigits(d2, bold: ink, rest: ink2).monospacedDigit() }
                if !d1.isEmpty { Text(d1).font(.footnote).foregroundStyle(ink2) }
            } else {
                let total = mem.totalText.rendered()
                let swap = mem.swapText.rendered()
                if !total.isEmpty { Text(total).font(.footnote).foregroundStyle(ink2) }
                if !swap.isEmpty { Text(swap).font(.footnote).foregroundStyle(ink2) }
            }
        }
    }

    var body: some View {
        let ink = dim ? DS.ink3 : DS.ink
        let ink2 = dim ? DS.ink3 : DS.ink2
        let fig = figure
        let label = metric == .cpu ? L10n.machineStatusCpu() : L10n.machineStatusMemory()

        VStack(alignment: .leading, spacing: 0) {
            let figureBlock = VStack(alignment: .leading, spacing: 4) {
                Kick(symbol: metric == .cpu ? .cpu : .memory, label: label, color: ink2)
                HStack(alignment: metric == .cpu ? .top : .lastTextBaseline, spacing: 4) {
                    RollingText(text: fig.value, number: fig.number)
                        .figure(52, style: .largeTitle, limit: 1.7)
                        .foregroundStyle(ink)
                        .minimumScaleFactor(0.5)
                    if metric == .cpu {
                        Text(fig.unit.or("%"))
                            .figure(24, style: .title2, limit: 1.7)
                            .foregroundStyle(ink)
                            .padding(.top, 7)
                    } else if !fig.unit.isEmpty {
                        Text(fig.unit)
                            .figure(24, style: .title2, limit: 1.4)
                            .foregroundStyle(ink2)
                            .lineLimit(1)
                            .minimumScaleFactor(0.6)
                    }
                }
            }

            ViewThatFits(in: .horizontal) {
                HStack(alignment: .bottom, spacing: 12) {
                    figureBlock
                    Spacer(minLength: 0)
                    sideLines(.trailing, ink: ink, ink2: ink2)
                        .multilineTextAlignment(.trailing)
                        .padding(.bottom, 8)
                }
                VStack(alignment: .leading, spacing: 6) {
                    figureBlock
                    sideLines(.leading, ink: ink, ink2: ink2)
                        .multilineTextAlignment(.leading)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
            }
            .padding(.horizontal, 18)

            if dim || !chart.visible || chart.points.isEmpty {
                Color.clear.frame(height: 16)
            } else {
                if chartOn {
                    StatusChart(chart: chart, color: ink, moving: moving, onScrub: onScrub)
                        .padding(.top, 6)
                        .accessibilityElement(children: .ignore)
                        .accessibilityLabel(chart.a11YSummary.rendered())
                        .accessibilityValue(chart.hasScrub_p ? chart.scrubA11YValue.rendered() : "")
                        .accessibilityAdjustableAction { dir in onStep(dir == .increment ? 1 : -1) }
                        .accessibilityAction(named: Text(L10n.machineStatusA11YPrevSample())) { onStep(-1) }
                        .accessibilityAction(named: Text(L10n.machineStatusA11YNextSample())) { onStep(1) }
                        .accessibilityIdentifier(StatusID.chart.rawValue)
                } else {
                    Color.clear.frame(height: 104).padding(.top, 6)
                }
                if metric == .memory {
                    let word = mem.pressureText.rendered()
                    if !word.isEmpty {
                        Text(word)
                            .font(.footnote.weight(.semibold))
                            .foregroundStyle(mem.pressure.ink(DS.ink2))
                            .frame(maxWidth: .infinity, alignment: .trailing)
                            .padding(.trailing, 18)
                            .padding(.top, 2)
                    }
                }
                HStack {
                    Text(L10n.machineStatusAxis2Min())
                    Spacer(minLength: 4)
                    Text(L10n.machineStatusAxis1Min())
                    Spacer(minLength: 4)
                    Text(L10n.machineStatusAxisNow())
                }
                .font(.caption2).foregroundStyle(DS.ink3).lineLimit(1).minimumScaleFactor(0.8)
                .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
                .padding(.horizontal, 18).padding(.top, 6).padding(.bottom, 13)
                .accessibilityHidden(true)
            }
        }
        .padding(.top, 16)
        .frame(maxWidth: .infinity, alignment: .topLeading)
    }
}

private struct LargeFigure: View {
    let value: String
    let number: Double
    let unit: String
    let color: Color
    let unitColor: Color

    var body: some View {
        ViewThatFits(in: .horizontal) {
            HStack(alignment: .lastTextBaseline, spacing: 3) {
                RollingText(text: value, number: number).figure(34, style: .title, limit: 1.6).foregroundStyle(color)
                Text(unit).figure(17, style: .body, limit: 1.6).foregroundStyle(unitColor).lineLimit(1)
            }
            VStack(alignment: .leading, spacing: 0) {
                RollingText(text: value, number: number).figure(34, style: .title, limit: 1.6).foregroundStyle(color)
                Text(unit).figure(17, style: .body, limit: 1.6).foregroundStyle(unitColor)
            }
        }
        .padding(.top, 6)
    }
}

private struct SayLines: View {
    let first: String
    let firstColor: Color
    var second: String?
    var secondColor: Color = DS.ink2
    var third: String?
    var breakAtComma = false
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Text(first).font(.footnote.weight(.semibold)).foregroundStyle(firstColor)
            if let second, !second.isEmpty { line(second) }
            if let third, !third.isEmpty { line(third) }
        }
        .padding(.top, 10)
    }

    @ViewBuilder private func line(_ line: String) -> some View {
        if breakAtComma, let i = line.firstIndex(where: { $0 == "，" || $0 == "," }) {
            ViewThatFits(in: .horizontal) {
                Text(line).font(.footnote).foregroundStyle(secondColor).lineLimit(1)
                VStack(alignment: .leading, spacing: 0) {
                    Text(String(line[...i])).font(.footnote).foregroundStyle(secondColor)
                    Text(String(line[line.index(after: i)...]).trimmingCharacters(in: .whitespaces))
                        .font(.footnote)
                        .foregroundStyle(secondColor)
                }
            }
        } else if typeSize.isAccessibilitySize || typeSize >= .xxxLarge {
            Text(line).font(.footnote).foregroundStyle(secondColor)
        } else {
            Text(line).font(.footnote).foregroundStyle(secondColor).lineLimit(1).minimumScaleFactor(0.75)
        }
    }
}

private struct SmallTile: View {
    let metric: StatusMetric
    let cpu: PBMachineCpuSection
    let mem: PBMachineMemorySection
    let dim: Bool
    let onTap: () -> Void

    var body: some View {
        let ink = dim ? DS.ink3 : DS.ink
        let ink2 = dim ? DS.ink3 : DS.ink2
        let fig: (value: String, number: Double, unit: String) = if metric == .memory {
            (mem.valueText.or("—"), mem.value, mem.unit == "GB" ? mem.caption.rendered().or("GB") : mem.unit)
        } else {
            (cpu.valueText.or("—"), cpu.value, cpu.unit)
        }

        Button(action: onTap) {
            VStack(alignment: .leading, spacing: 0) {
                if metric == .memory {
                    let pressureWord = mem.pressureText.rendered()
                    let totalLine = [mem.totalText.rendered(), mem.swapText.rendered()]
                        .filter { !$0.isEmpty }
                        .joined(separator: "，")
                    Kick(symbol: .memory, label: L10n.machineStatusMemory(), color: ink2)
                    LargeFigure(value: fig.value, number: fig.number, unit: fig.unit, color: ink, unitColor: ink2)
                    if mem.hasPressurePosition_p {
                        PressureScale(position: mem.pressurePosition)
                            .padding(.top, 14)
                            .opacity(dim ? 0.5 : 1)
                    }
                    Spacer(minLength: 0)
                    SayLines(
                        first: pressureWord.or(totalLine),
                        firstColor: dim ? DS.ink3 : mem.pressure.ink(DS.ink),
                        second: pressureWord.isEmpty ? nil : totalLine,
                        secondColor: ink2,
                        breakAtComma: true
                    )
                } else {
                    let load = cpu.detail2.rendered()
                    let cores = cpu.detail.rendered()
                    Kick(symbol: .cpu, label: L10n.machineStatusCpu(), color: ink2)
                    LargeFigure(value: fig.value, number: fig.number, unit: fig.unit, color: ink, unitColor: ink2)
                    Spacer(minLength: 0)
                    SayLines(
                        first: load.or(cores),
                        firstColor: ink,
                        second: load.isEmpty ? nil : cores,
                        secondColor: ink2
                    )
                }
            }
            .padding(.init(top: 14, leading: 16, bottom: 15, trailing: 16))
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(metric == .cpu ? cpu.a11YLabel.rendered() : mem.a11YLabel.rendered())
        .accessibilityAddTraits(.isButton)
        .accessibilityIdentifier(metric == .cpu ? StatusID.cpuCard.rawValue : StatusID.memoryCard.rawValue)
    }
}

private struct DiskTile: View {
    let disk: PBMachineDiskSection
    let dim: Bool
    let onTap: () -> Void

    var body: some View {
        let bad = disk.state == .critical
        let ink = dim ? DS.ink3 : disk.state.ink(DS.ink)
        let ink2 = dim ? DS.ink3 : (bad ? DS.red : DS.ink2)
        let fig = (
            value: disk.valueText.or("—"),
            number: disk.value,
            unit: disk.caption.rendered().or(disk.unit)
        )

        Button(action: onTap) {
            VStack(alignment: .leading, spacing: 0) {
                Kick(symbol: .disk, label: L10n.machineStatusDisk(), color: dim ? DS.ink3 : DS.ink2)
                LargeFigure(value: fig.value, number: fig.number, unit: fig.unit, color: ink, unitColor: ink2)
                if disk.hasUsedFraction_p {
                    DiskBar(usedFraction: disk.usedFraction, state: disk.state)
                        .padding(.top, 12)
                        .opacity(dim ? 0.5 : 1)
                }
                Spacer(minLength: 0)
                SayLines(
                    first: disk.stateBadge.rendered(),
                    firstColor: ink,
                    second: disk.detail.rendered(),
                    secondColor: ink2,
                    third: disk.detail2.rendered()
                )
            }
            .padding(.init(top: 14, leading: 16, bottom: 15, trailing: 16))
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            .background(StatusCardBackground())
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(disk.a11YLabel.rendered())
        .accessibilityAddTraits(.isButton)
        .accessibilityHint(L10n.machineStatusAgentUsage())
        .accessibilityIdentifier(StatusID.diskCard.rawValue)
    }
}

private struct UsageRowView: View {
    let row: PBMachineUsageRow
    let primary: String
    let bar: Double
    let top: Bool
    let last: Bool
    let dim: Bool
    let onTap: () -> Void
    @Environment(\.accessibilityReduceMotion) private var reduce

    var body: some View {
        let tappable = !row.sessionID.isEmpty
        let title = row.title.rendered().or(row.id)
        let secondary = row.secondary.rendered()
        let spoken = [title, primary, secondary].filter { !$0.isEmpty }.joined(separator: L10n.commonIosSpokenSeparator())

        let inner = HStack(alignment: .top, spacing: 12) {
            icon.padding(.top, 1)
            VStack(alignment: .leading, spacing: 6) {
                HStack(alignment: .firstTextBaseline, spacing: 10) {
                    Text(title).font(.system(.callout)).foregroundStyle(dim ? DS.ink3 : DS.ink).lineLimit(1).truncationMode(.tail)
                    Spacer(minLength: 0)
                    Text(primary).figure(16, .medium, style: .callout, limit: 1.6).monospacedDigit().foregroundStyle(dim ? DS.ink3 : DS.ink).lineLimit(1).fixedSize()
                }
                UsageMeter(fraction: bar) {
                    Capsule().fill(dim || !top ? DS.ink3 : DS.ink).frame(height: 5)
                    Text(secondary).font(.system(size: 12.5)).monospacedDigit().foregroundStyle(dim ? DS.ink3 : DS.ink2).lineLimit(1)
                }
                .animation(reduce ? nil : .timingCurve(0.2, 0.8, 0.2, 1, duration: 0.3), value: bar)
            }
            .padding(.bottom, 12)
            .overlay(alignment: .bottom) { if !last { Rectangle().fill(DS.sep).frame(height: 0.5) } }
        }
        .padding(.leading, 16).padding(.trailing, 16).padding(.top, 11)
        .contentShape(Rectangle())

        if tappable {
            Button(action: onTap) { inner }
                .buttonStyle(RowPressStyle())
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(spoken)
                .accessibilityAddTraits(.isButton)
        } else {
            inner
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(spoken)
        }
    }

    @ViewBuilder private var icon: some View {
        let glyph = !row.sessionID.isEmpty && row.agent != .unspecified ? AgentGlyph(row.agent) : nil
        ZStack {
            RoundedRectangle(cornerRadius: 8, style: .continuous).fill(DS.fill2)
            if let glyph {
                AgentMarkShape(glyph: glyph).fill(dim ? DS.ink3 : DS.ink2).frame(width: 16, height: 16)
            } else {
                Image(systemName: StatusSymbol.process.rawValue).font(.system(size: 14, weight: .medium)).foregroundStyle(dim ? DS.ink3 : DS.ink2)
            }
        }
        .frame(width: 30, height: 30)
    }
}
