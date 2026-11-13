// 电脑状态 (ui-spec A3; 界面稿 v2 第 ② 部分): a computer's CPU, memory and disk, and what uses them. One big figure
// with its curve, two small cards, the disk's lines when it is short, the ranking, and a line at the foot. The polling is
// RACore's `MachineStatusController` (UI/Machine/MachineStatusFeed.swift); every number and word is from its model.
// The curve, the card swap and the rolling digits are in StatusParts.swift and below.
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

// MARK: The swap of the main chart and a small card

private enum SwapRole: Hashable {
    case bg(StatusMetric), hero(StatusMetric), small(StatusMetric), disk
}

private struct SwapRoleKey: LayoutValueKey {
    static let defaultValue = SwapRole.disk
}

private func lerp(_ a: CGRect, _ b: CGRect, _ t: Double) -> CGRect {
    let f = CGFloat(t)
    return CGRect(x: a.minX + (b.minX - a.minX) * f, y: a.minY + (b.minY - a.minY) * f, width: a.width + (b.width - a.width) * f, height: a.height + (b.height - a.height) * f)
}

/// The main chart and the two small cards. Each metric has a surface (`bg`) and two contents: its main chart and its small card.
/// The surfaces move to each other's place, `t` 0 (CPU is the main chart) to 1 (memory is), the contents stay at their own
/// size inside; `SwapFade` shows one set of figures at a time. All three frames are interpolated here, so rows below follow.
private struct SwapLayout: Layout {
    var t: Double
    /// At accessibility text sizes the two small cards do not fit side by side: each takes a row of its own.
    var stacked = false
    var animatableData: Double {
        get { t }
        set { t = newValue }
    }
    private let gap: CGFloat = 12

    private struct Solved {
        var cpu: CGRect, memory: CGRect, disk: CGRect, height: CGFloat
    }

    /// The main chart's frame, the small card's and the disk card's, for a main chart `hero` high, a small card `small` and a disk card `disk`.
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
        func height(_ role: SwapRole, _ w: CGFloat) -> CGFloat { map[role]?.sizeThatFits(ProposedViewSize(width: w, height: nil)).height ?? 0 }
        let heroCPU = height(.hero(.cpu), width), heroMem = height(.hero(.memory), width)
        let smallCPU = height(.small(.cpu), small), smallMem = height(.small(.memory), small), disk = height(.disk, small)
        // CPU is the main chart, then memory is.
        let a = frames(hero: heroCPU, small: smallMem, disk: disk, width: width)
        let b = frames(hero: heroMem, small: smallCPU, disk: disk, width: width)
        return Solved(
            cpu: lerp(a.hero, b.small, t), memory: lerp(a.small, b.hero, t), disk: lerp(a.disk, b.disk, t),
            height: a.height + (b.height - a.height) * CGFloat(t))
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

/// How visible a content of a swapping card is, from the swap's linear time `t` (0...1 of 350 ms): the old figures fade out in the
/// first 100, nothing shows, the new ones fade in over the last 120 (`swapOldAlpha`, `swapNewAlpha`).
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

// MARK: The page

struct MachineStatusScreen: View {
    let id: String
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @Environment(\.page) private var page
    @Environment(\.accessibilityReduceMotion) private var reduce
    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var feed = MachineStatusFeed()
    @State private var main = StatusMetric.cpu
    /// 0 while CPU is the main chart, 1 while memory is; animated by the swap.
    @State private var position = 0.0
    /// The swap's linear time, 0...1; 1 at rest.
    @State private var linear = 1.0
    @State private var swapping = false
    @State private var showSessions = HomeLaunch.value("-machineList") != "processes"
    @State private var scrub: ChartPoint?
    @State private var stepIndex: Int?
    @State private var order = UsageOrder()
    @State private var cleanup: CleanupStep?

    private enum CleanupStep: Identifiable {
        case pickFolder
        case task(key: String, machine: String)
        var id: Int {
            switch self {
            case .pickFolder: 0
            case .task: 1
            }
        }
    }

    var body: some View {
        let state = model.state
        let machine = state.machines[id]
        let supported = machine?.meta?.capabilities.machineStatus == true
        let online = machine?.online == true && !state.unlinked
        let name = machineLabel(state, id: id)
        let text = StatusText()
        ZStack {
            DS.liftPage.ignoresSafeArea()
            content(state: state, name: name, text: text)
        }
        .machineStatusPolling(feed, fetch: context.machineStatusFetch(id, demo: model.isDemo), online: online, supported: supported)
        .navigationBarTitleDisplayMode(.inline)
        .navigationTitle(name)
        .toolbar { ToolbarItem(placement: .principal) { Color.clear.frame(width: 1, height: 1).accessibilityHidden(true) } }
        .task { applyLaunch() }
        .sheet(item: $cleanup) { step in
            NavigationStack {
                switch step {
                case .pickFolder:
                    VStack(spacing: 0) {
                        SheetHead(L10n.projectsNewChooseDirectory(), action: L10n.commonCancel()) { cleanup = nil }
                        ProjectDirPicker(state, machine: id) { machine, path in startCleanup(path, machine: machine) }
                    }
                case .task(let key, let machine):
                    NewTaskScreen(key: key, machine: machine, onClose: { cleanup = nil }) { sid in
                        cleanup = nil
                        nav.open(.session(id: sid))
                    }
                }
            }
            .sheetSurface()
            .presentationDetents([.large])
        }
    }

    // MARK: Faces

    @ViewBuilder private func content(state: RemoteState, name: String, text: StatusText) -> some View {
        switch feed.view {
        case .unavailable:
            VStack(alignment: .leading, spacing: 0) {
                header(name: name, line: nil)
                Spacer()
                VStack(spacing: 2) {
                    Text(L10n.machineStatusUnavailable()).font(.body).foregroundStyle(DS.ink2).multilineTextAlignment(.center)
                    LinkButton(L10n.commonRetry()) { feed.retry() }.font(.body)
                }
                .frame(maxWidth: .infinity)
                Spacer()
            }
            .padding(.horizontal, 16)
            .frame(maxWidth: DS.column)
        case .loading:
            ScrollView { skeleton(name: name, text: text).padding(.horizontal, 16).padding(.bottom, 30).frame(maxWidth: DS.column) }
        case .live, .stale:
            if let s = feed.last {
                ScrollView {
                    page(s, state: state, name: name, text: text, stale: feed.view == .stale)
                        .padding(.horizontal, 16).padding(.bottom, 30).frame(maxWidth: DS.column)
                }
                .frame(maxWidth: .infinity)
                .startAtEndForScreenshots()
            }
        }
    }

    /// The title and, under it, the state of the figures: a dot and a line.
    private func header(name: String, line: LiveLine?) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(name).font(.largeTitle.bold()).foregroundStyle(DS.ink).lineLimit(2).fixedSize(horizontal: false, vertical: true)
                .accessibilityAddTraits(.isHeader)
            if let line { line }
        }
        .padding(.horizontal, 4).padding(.top, 6)
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    /// Nothing read yet (ui-rulings 6): the page's frame, dashes for the figures, no curve and no ranking.
    private func skeleton(name: String, text: StatusText) -> some View {
        let other: StatusMetric = main == .cpu ? .memory : .cpu
        return VStack(alignment: .leading, spacing: 0) {
            header(name: name, line: LiveLine(kind: .loading, text: L10n.machineStatusLoading()))
            VStack(alignment: .leading, spacing: 0) {
                Kick(symbol: main == .cpu ? .cpu : .memory, label: main == .cpu ? L10n.machineStatusCpu() : L10n.machineStatusMemory(), color: DS.ink2)
                Text(noValue).figure(52, style: .largeTitle, limit: 1.6).foregroundStyle(DS.ink3).padding(.top, 4)
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
            Text(noValue).figure(34, style: .title, limit: 1.6).foregroundStyle(DS.ink3).padding(.top, 6)
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

    // MARK: The page of figures

    private func page(_ s: PBMachineStatus, state: RemoteState, name: String, text: StatusText, stale: Bool) -> some View {
        let points = chartPoints(s, metric: main, sinceMs: feed.curveSinceMs)
        let latest = points.last?.timeMs ?? 0
        let lineKind: LiveLine.Kind = stale ? .stale : .live
        let lineText: String = {
            if stale { return "" }
            if let scrub { return text.lookback(ageMs: latest - scrub.timeMs) }
            return L10n.machineStatusLive()
        }()
        return VStack(alignment: .leading, spacing: 0) {
            header(name: name, line: LiveLine(kind: lineKind, text: lineText, receivedAtMs: feed.receivedAtMs, status: text))
            swapGrid(s, text: text, stale: stale, points: points).padding(.top, 18)
            if showDiskActions(s) { diskActions(s, state: state, text: text).padding(.top, 12) }
            usageHeader.padding(.top, 26)
            usageList(s, state: state, text: text, stale: stale)
            let foot = footerParts(s, text: text)
            if !foot.isEmpty {
                Text(foot.joined(separator: " · ")).font(.footnote).foregroundStyle(DS.ink3).multilineTextAlignment(.center)
                    .frame(maxWidth: .infinity).padding(.top, 22)
            }
        }
    }

    private func swapGrid(_ s: PBMachineStatus, text: StatusText, stale: Bool, points: [ChartPoint]) -> some View {
        let cpu = cpuFigures(s, text: text), mem = memoryCard(s, text: text), disk = diskCard(s, text: text)
        return SwapLayout(t: position, stacked: typeSize.isAccessibilitySize) {
            ForEach([StatusMetric.cpu, .memory], id: \.self) { m in
                StatusCardBackground().layoutValue(key: SwapRoleKey.self, value: .bg(m))
                tileContent(m, hero: true, s, cpu, mem, text, stale, points)
                    .modifier(SwapFade(t: linear, isNew: main == m))
                    .allowsHitTesting(main == m)
                    .accessibilityHidden(main != m)
                    .layoutValue(key: SwapRoleKey.self, value: .hero(m))
                tileContent(m, hero: false, s, cpu, mem, text, stale, points)
                    .modifier(SwapFade(t: linear, isNew: main != m))
                    .allowsHitTesting(main != m)
                    .accessibilityHidden(main == m)
                    .layoutValue(key: SwapRoleKey.self, value: .small(m))
            }
            DiskTile(s: s, card: disk, text: text, stale: stale) { nav.open(.machineStorage(id: id)) }
                .layoutValue(key: SwapRoleKey.self, value: .disk)
        }
    }

    @ViewBuilder private func tileContent(
        _ m: StatusMetric, hero: Bool, _ s: PBMachineStatus, _ cpu: CpuFigures, _ mem: MemoryCard, _ text: StatusText, _ stale: Bool, _ points: [ChartPoint]
    ) -> some View {
        if hero {
            HeroContent(
                metric: m, s: s, cpu: cpu, mem: mem, text: text, stale: stale, points: m == main ? points : chartPoints(s, metric: m, sinceMs: feed.curveSinceMs),
                chartOn: m == main || swapping, scrub: m == main ? $scrub : .constant(nil), moving: !stale && !reduce && !HomeLaunch.flag("-machineFrozen"),
                onStep: { step($0, points) })
        } else {
            SmallTile(metric: m, s: s, cpu: cpu, mem: mem, text: text, stale: stale) { pick(m) }
        }
    }

    private func step(_ delta: Int, _ points: [ChartPoint]) {
        guard !points.isEmpty else { return }
        let from = stepIndex.flatMap { points.indices.contains($0) ? $0 : nil } ?? points.count - 1
        let next = min(max(from + delta, 0), points.count - 1)
        stepIndex = next
        scrub = points[next]
    }

    /// Makes `next` the main chart: the surfaces swap places in 350 ms, the ranking follows (A3). With 减弱动态效果 at once.
    private func pick(_ next: StatusMetric) {
        guard next != main else { return }
        scrub = nil
        stepIndex = nil
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

    // MARK: Disk lines (A3 ③)

    private func diskActions(_ s: PBMachineStatus, state: RemoteState, text: StatusText) -> some View {
        VStack(spacing: 0) {
            Button { cleanUp(state) } label: {
                HStack {
                    Text(L10n.machineStatusCleanupAction()).font(.body).foregroundStyle(DS.blue)
                    Spacer(minLength: 0)
                }
                .padding(.horizontal, 16).padding(.vertical, 11).frame(minHeight: 48)
                .contentShape(Rectangle())
            }
            .buttonStyle(RowPressStyle())
            Rectangle().fill(DS.sep).frame(height: 0.5).padding(.leading, 16)
            Button { nav.open(.machineStorage(id: id)) } label: {
                HStack(spacing: 8) {
                    Text(L10n.machineStatusAgentUsage()).font(.body).foregroundStyle(DS.ink)
                    Spacer(minLength: 8)
                    if s.hasAgentBytes { Text(text.size(Int64(s.agentBytes))).figure(17, .regular, style: .body, limit: 1.6).foregroundStyle(DS.ink2) }
                    Arrow(DS.ink3)
                }
                .padding(.horizontal, 16).padding(.vertical, 11).frame(minHeight: 48)
                .contentShape(Rectangle())
            }
            .buttonStyle(RowPressStyle())
        }
        .statusCard()
    }

    /// 让 Agent 帮我清理: the new-session page with this computer, its most recent project and the prompt written (ui-spec B4).
    private func cleanUp(_ state: RemoteState) {
        guard let meta = state.machines[id]?.meta, let key = recentMachineProject(meta) else {
            // No project on this computer: choose a folder first (ui-rulings 8).
            cleanup = .pickFolder
            return
        }
        startCleanup(key, machine: id)
    }

    private func startCleanup(_ key: String, machine: String) {
        context.newSessionDrafts.prepareCleanup(model.state, key: key, machine: machine, prompt: L10n.machineStatusCleanupPrompt())
        cleanup = .task(key: key, machine: machine)
    }

    // MARK: The ranking (A3 ④)

    private var usageHeader: some View {
        let title = Text(L10n.machineStatusTopUsage()).font(.title3.bold()).foregroundStyle(DS.ink).accessibilityAddTraits(.isHeader)
        let toggle = Button { showSessions.toggle() } label: {
            Text(showSessions ? L10n.machineStatusShowProcesses() : L10n.machineStatusShowSessions()).font(.subheadline).foregroundStyle(DS.blue)
                .multilineTextAlignment(.leading)
                .frame(minHeight: DS.minHit).contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        // The title and its link share a line while both fit; otherwise the link goes under the title.
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

    private func usageList(_ s: PBMachineStatus, state: RemoteState, text: StatusText, stale: Bool) -> some View {
        let rows = usageRows(
            s, showSessions: showSessions, metric: main, order: order, text: text,
            titleOf: { state.sessions[$0].map { RACore.titleOf($0) } }, agentOf: { state.sessions[$0]?.agent })
        return VStack(spacing: 0) {
            if rows.isEmpty {
                Text(L10n.machineStatusNoRunningSessions()).font(.footnote).foregroundStyle(DS.ink2).multilineTextAlignment(.center)
                    .frame(maxWidth: .infinity).padding(16)
            }
            ForEach(Array(rows.enumerated()), id: \.element.id) { i, row in
                UsageRowView(row: row, last: i == rows.count - 1, dim: stale) {
                    if row.known, row.isSession { nav.open(.session(id: row.sessionId)) }
                }
            }
        }
        .padding(.vertical, 2)
        .frame(maxWidth: .infinity)
        .animation(reduce ? nil : .timingCurve(0.2, 0.8, 0.2, 1, duration: 0.3), value: rows.map(\.id))
        .statusCard()
    }

    // MARK: Launch arguments (debug builds)

    private func applyLaunch() {
        #if DEBUG
        if HomeLaunch.value("-machineMain") == "memory" {
            main = .memory
            position = 1
        }
        if let ms = HomeLaunch.value("-machineSwap").flatMap(Double.init) {
            // A frame of the swap to look at: memory is becoming the main chart, `ms` into its 350.
            main = .memory
            swapping = true
            let t = min(max(ms / swapMs, 0), 1)
            linear = t
            position = UnitCurve.bezier(startControlPoint: UnitPoint(x: 0.2, y: 0.8), endControlPoint: UnitPoint(x: 0.2, y: 1)).value(at: t)
        }
        if let f = HomeLaunch.value("-machineScrub").flatMap(Double.init) {
            Task {
                for _ in 0..<200 where feed.last == nil { try? await Task.sleep(for: .milliseconds(20)) }
                guard let s = feed.last else { return }
                let pts = chartPoints(s, metric: main, sinceMs: feed.curveSinceMs)
                guard !pts.isEmpty else { return }
                let i = min(max(Int((Double(pts.count - 1) * f).rounded()), 0), pts.count - 1)
                stepIndex = i
                scrub = pts[i]
            }
        }
        #endif
    }
}

// MARK: Pieces

/// The line under the title: a dot and what the figures are (ui-spec A3, A6).
private struct LiveLine: View {
    enum Kind { case loading, live, stale }
    let kind: Kind
    let text: String
    var receivedAtMs: Int64 = 0
    var status = StatusText()
    @Environment(\.accessibilityReduceMotion) private var reduce
    @State private var breathe = false

    var body: some View {
        HStack(spacing: 6) {
            dot
            switch kind {
            case .stale:
                TimelineView(.periodic(from: .now, by: 5)) { _ in
                    Text(status.stale(ageMs: nowMs() - receivedAtMs)).font(.subheadline).foregroundStyle(DS.ink2)
                }
            default:
                Text(text).font(.subheadline).foregroundStyle(DS.ink2).lineLimit(1)
            }
        }
        .accessibilityElement(children: .combine)
    }

    @ViewBuilder private var dot: some View {
        let color: Color = switch kind {
        case .loading: DS.ink3
        case .live: DS.green
        case .stale: DS.orange
        }
        ZStack {
            if kind == .live {
                Circle().stroke(DS.green.opacity(breathe ? 0 : 0.22), lineWidth: breathe ? 5 : 3).frame(width: 7 + (breathe ? 5 : 3), height: 7 + (breathe ? 5 : 3))
            }
            Circle().fill(color).frame(width: 7, height: 7)
        }
        .frame(width: 17, height: 17)
        .onAppear {
            guard kind == .live, !reduce else { return }
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

/// The text of a load line with its number in `ink`, 15 rounded, the rest `ink2` 13: 「3.2 负载」.
private func loadText(_ full: String, number: String, bold: Color, rest: Color) -> Text {
    var out = AttributedString(full)
    out.foregroundColor = rest
    out.font = .footnote
    if let r = out.range(of: number) {
        out[r].foregroundColor = bold
        out[r].font = Font.system(.subheadline, design: .rounded).weight(.semibold)
    }
    return Text(out)
}

/// The main chart of a metric: figure, side lines, curve, axis (the card's content; its surface is `StatusCardBackground`).
private struct HeroContent: View {
    let metric: StatusMetric
    let s: PBMachineStatus
    let cpu: CpuFigures
    let mem: MemoryCard
    let text: StatusText
    let stale: Bool
    let points: [ChartPoint]
    let chartOn: Bool
    @Binding var scrub: ChartPoint?
    let moving: Bool
    let onStep: (Int) -> Void

    /// The big figure and its number: the moment looked back to, or now.
    private var figure: (String, Double) {
        if metric == .cpu {
            let v = scrub.map { Int(min(max($0.value, 0), 100).rounded()) } ?? cpu.percent
            return (String(v), Double(v))
        }
        let bytes = scrub.map { Int64($0.value) } ?? Int64(s.memoryUsedBytes)
        return (text.gbNumber(bytes, oneDecimal: true, binary: true), Double(bytes) / gibibyte)
    }

    var body: some View {
        let ink = stale ? DS.ink3 : DS.ink
        let ink2 = stale ? DS.ink3 : DS.ink2
        let (value, number) = figure
        let label = metric == .cpu ? L10n.machineStatusCpu() : L10n.machineStatusMemory()
        VStack(alignment: .leading, spacing: 0) {
            HStack(alignment: .bottom, spacing: 12) {
                VStack(alignment: .leading, spacing: 4) {
                    Kick(symbol: metric == .cpu ? .cpu : .memory, label: label, color: ink2)
                    HStack(alignment: metric == .cpu ? .top : .lastTextBaseline, spacing: 4) {
                        RollingText(text: value, number: number).figure(52, style: .largeTitle, limit: 1.7).foregroundStyle(ink).minimumScaleFactor(0.5)
                        if metric == .cpu {
                            Text(text.percentSymbol).figure(24, style: .title2, limit: 1.7).foregroundStyle(ink).padding(.top, 7)
                        } else {
                            Text(L10n.machineStatusGbUsed()).figure(24, style: .title2, limit: 1.4).foregroundStyle(ink2).lineLimit(1).minimumScaleFactor(0.6)
                        }
                    }
                }
                Spacer(minLength: 0)
                VStack(alignment: .trailing, spacing: 0) {
                    if metric == .cpu {
                        if let load = cpu.load { loadText(L10n.machineStatusLoad(load: load), number: load, bold: ink, rest: ink2).monospacedDigit() }
                        if let cores = cpu.cores { Text(cores).font(.footnote).foregroundStyle(ink2) }
                    } else {
                        Text(L10n.machineStatusTotal(size: mem.total)).font(.footnote).foregroundStyle(ink2)
                        if let swap = mem.swap { Text(L10n.machineStatusSwap(size: swap)).font(.footnote).foregroundStyle(ink2) }
                    }
                }
                .multilineTextAlignment(.trailing)
                .padding(.bottom, 8)
            }
            .padding(.horizontal, 18)
            if stale || points.isEmpty {
                Color.clear.frame(height: 16)
            } else {
                if chartOn {
                    StatusChart(points: points, ceiling: chartCeiling(s, metric: metric), color: ink, moving: moving, scrub: $scrub)
                        .padding(.top, 6)
                        .accessibilityElement(children: .ignore)
                        .accessibilityLabel(chartSummary(points, metric: metric, text: text))
                        .accessibilityValue(scrub.map { text.lookback(ageMs: (points.last?.timeMs ?? 0) - $0.timeMs) } ?? "")
                        .accessibilityAdjustableAction { dir in onStep(dir == .increment ? 1 : -1) }
                        .accessibilityAction(named: Text(L10n.machineStatusA11YPrevSample())) { onStep(-1) }
                        .accessibilityAction(named: Text(L10n.machineStatusA11YNextSample())) { onStep(1) }
                        .accessibilityIdentifier(StatusID.chart.rawValue)
                } else {
                    Color.clear.frame(height: 104).padding(.top, 6)
                }
                if metric == .memory, let word = mem.pressureWord {
                    Text(word).font(.footnote.weight(.semibold)).foregroundStyle(mem.tone.ink(DS.ink2))
                        .frame(maxWidth: .infinity, alignment: .trailing).padding(.trailing, 18).padding(.top, 2)
                }
                HStack {
                    Text(L10n.machineStatusAxis2Min())
                    Spacer(minLength: 4)
                    Text(L10n.machineStatusAxis1Min())
                    Spacer(minLength: 4)
                    Text(L10n.machineStatusAxisNow())
                }
                .font(.caption2).foregroundStyle(DS.ink3).lineLimit(1).minimumScaleFactor(0.8)
                // Marks of the curve's width, not words to be read: they keep to the largest ordinary size.
                .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
                .padding(.horizontal, 18).padding(.top, 6).padding(.bottom, 13)
                .accessibilityHidden(true)
            }
        }
        .padding(.top, 16)
        .frame(maxWidth: .infinity, alignment: .topLeading)
    }
}

/// A figure and its unit side by side; under it, whole, when the two do not fit the card.
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

/// The two lines under a card's figure, pinned to its foot.
private struct SayLines: View {
    let first: String
    let firstColor: Color
    var second: String?
    var secondColor: Color = DS.ink2
    var third: String?
    /// The second line may break at its comma, never inside a figure.
    var breakAtComma = false
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Text(first).font(.footnote.weight(.semibold)).foregroundStyle(firstColor)
            if let second { line(second) }
            if let third { line(third) }
        }
        .padding(.top, 10)
    }

    @ViewBuilder private func line(_ line: String) -> some View {
        if breakAtComma, let i = line.firstIndex(where: { $0 == "，" || $0 == "," }) {
            ViewThatFits(in: .horizontal) {
                Text(line).font(.footnote).foregroundStyle(secondColor).lineLimit(1)
                VStack(alignment: .leading, spacing: 0) {
                    Text(String(line[...i])).font(.footnote).foregroundStyle(secondColor)
                    Text(String(line[line.index(after: i)...]).trimmingCharacters(in: .whitespaces)).font(.footnote).foregroundStyle(secondColor)
                }
            }
        } else if typeSize.isAccessibilitySize || typeSize >= .xxxLarge {
            Text(line).font(.footnote).foregroundStyle(secondColor)
        } else {
            // One line, as the mockup's two lines are: the type steps down a little before it wraps.
            Text(line).font(.footnote).foregroundStyle(secondColor).lineLimit(1).minimumScaleFactor(0.75)
        }
    }
}

/// A small card: CPU or memory as the card a tap makes the main chart.
private struct SmallTile: View {
    let metric: StatusMetric
    let s: PBMachineStatus
    let cpu: CpuFigures
    let mem: MemoryCard
    let text: StatusText
    let stale: Bool
    let onTap: () -> Void

    var body: some View {
        let ink = stale ? DS.ink3 : DS.ink
        let ink2 = stale ? DS.ink3 : DS.ink2
        Button(action: onTap) {
            VStack(alignment: .leading, spacing: 0) {
                if metric == .memory {
                    Kick(symbol: .memory, label: L10n.machineStatusMemory(), color: ink2)
                    LargeFigure(value: mem.used, number: mem.usedNumber, unit: L10n.machineStatusGbUsed(), color: ink, unitColor: ink2)
                    if let position = mem.position { PressureScale(position: position).padding(.top, 14).opacity(stale ? 0.5 : 1) }
                    Spacer(minLength: 0)
                    SayLines(first: mem.pressureWord ?? mem.detail, firstColor: stale ? DS.ink3 : mem.tone.ink(DS.ink),
                             second: mem.pressureWord == nil ? nil : mem.detail, secondColor: ink2, breakAtComma: true)
                } else {
                    Kick(symbol: .cpu, label: L10n.machineStatusCpu(), color: ink2)
                    LargeFigure(value: String(cpu.percent), number: Double(cpu.percent), unit: text.percentSymbol, color: ink, unitColor: ink2)
                    Spacer(minLength: 0)
                    SayLines(first: cpu.load.map { L10n.machineStatusLoadLine(load: $0) } ?? cpu.cores ?? "", firstColor: ink,
                             second: cpu.load == nil ? nil : cpu.cores, secondColor: ink2)
                }
            }
            .padding(.init(top: 14, leading: 16, bottom: 15, trailing: 16))
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(spoken)
        .accessibilityAddTraits(.isButton)
        .accessibilityIdentifier(metric == .cpu ? StatusID.cpuCard.rawValue : StatusID.memoryCard.rawValue)
    }

    private var spoken: String {
        if metric == .cpu { return L10n.machineStatusA11YCpuCard(value: "\(cpu.percent)%") }
        let used = L10n.machineStatusSizeGb(size: mem.used)
        if let word = mem.pressureWord { return L10n.machineStatusA11YMemoryCard(pressure: word, used: used) }
        return L10n.machineStatusA11YMemoryCardPlain(used: used)
    }
}

/// The disk card: its own surface, a tap goes to 附件存储 on the computer's page.
private struct DiskTile: View {
    let s: PBMachineStatus
    let card: DiskCard
    let text: StatusText
    let stale: Bool
    let onTap: () -> Void

    var body: some View {
        let bad = card.tone == .critical
        let ink = stale ? DS.ink3 : card.tone.ink(DS.ink)
        let ink2 = stale ? DS.ink3 : (bad ? DS.red : DS.ink2)
        Button(action: onTap) {
            VStack(alignment: .leading, spacing: 0) {
                Kick(symbol: .disk, label: L10n.machineStatusDisk(), color: stale ? DS.ink3 : DS.ink2)
                LargeFigure(value: card.free, number: Double(s.disk.availableBytes) / 1e9, unit: L10n.machineStatusGbAvailable(), color: ink, unitColor: ink2)
                DiskBar(agentShare: card.agentShare, otherShare: card.otherShare, tone: card.tone).padding(.top, 12).opacity(stale ? 0.5 : 1)
                Spacer(minLength: 0)
                SayLines(first: card.word, firstColor: ink, second: card.detail, secondColor: ink2, third: card.detail2)
            }
            .padding(.init(top: 14, leading: 16, bottom: 15, trailing: 16))
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            .background(StatusCardBackground())
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(L10n.machineStatusA11YDiskCard(size: text.size(Int64(s.disk.availableBytes)), state: card.word))
        .accessibilityAddTraits(.isButton)
        .accessibilityHint(L10n.machineStatusAgentUsage())
        .accessibilityIdentifier(StatusID.diskCard.rawValue)
    }
}

/// A ranking row: the icon, the title and main figure, a bar and the other figure.
private struct UsageRowView: View {
    let row: UsageRow
    let last: Bool
    let dim: Bool
    let onTap: () -> Void
    @Environment(\.accessibilityReduceMotion) private var reduce

    var body: some View {
        let tappable = row.isSession && row.known
        let inner = HStack(alignment: .top, spacing: 12) {
            icon.padding(.top, 1)
            VStack(alignment: .leading, spacing: 6) {
                HStack(alignment: .firstTextBaseline, spacing: 10) {
                    Text(row.title).font(.system(.callout)).foregroundStyle(dim ? DS.ink3 : DS.ink).lineLimit(1).truncationMode(.tail)
                    Spacer(minLength: 0)
                    Text(row.primary).figure(16, .medium, style: .callout, limit: 1.6).monospacedDigit().foregroundStyle(dim ? DS.ink3 : DS.ink).lineLimit(1).fixedSize()
                }
                UsageMeter(fraction: row.bar) {
                    Capsule().fill(dim || !row.top ? DS.ink3 : DS.ink).frame(height: 5)
                    Text(row.secondary).font(.system(size: 12.5)).monospacedDigit().foregroundStyle(dim ? DS.ink3 : DS.ink2).lineLimit(1)
                }
                .animation(reduce ? nil : .timingCurve(0.2, 0.8, 0.2, 1, duration: 0.3), value: row.bar)
            }
            .padding(.bottom, 12)
            .overlay(alignment: .bottom) { if !last { Rectangle().fill(DS.sep).frame(height: 0.5) } }
        }
        .padding(.leading, 16).padding(.trailing, 16).padding(.top, 11)
        .contentShape(Rectangle())
        if tappable {
            Button(action: onTap) { inner }.buttonStyle(RowPressStyle())
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(spoken)
                .accessibilityAddTraits(.isButton)
        } else {
            inner.accessibilityElement(children: .ignore).accessibilityLabel(spoken)
        }
    }

    private var spoken: String { [row.title, row.primary, row.secondary].joined(separator: L10n.commonIosSpokenSeparator()) }

    /// The icon block of a row (ui-rulings 9): `fill2`, radius 8, 30 square. A session shows its CLI's mark and a process the
    /// terminal, both in `ink2`: no brand colour or gradient on this page.
    @ViewBuilder private var icon: some View {
        let glyph = row.isSession && row.known ? row.agent.flatMap { AgentGlyph($0) } : nil
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
