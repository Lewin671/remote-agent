// The drawing of 电脑状态 (ui-spec A; 界面稿 v2 第 ② 部分): cards, rolling digits, status rings,
// memory pressure scale, disk bar, usage meter, and curve chart rendered from Core ViewModels.
import RACore
import RAProto
import SwiftUI
import UIKit

enum StatusMetric: Hashable {
    case cpu
    case memory
}

extension PBMachineRingTone {
    /// The colour of a figure at this tone: red, orange (its readable ink as text), or `normal`.
    func ink(_ normal: Color, text: Bool = true) -> Color {
        switch self {
        case .warn: text ? DS.orangeInk : DS.orange
        case .critical: DS.red
        default: normal
        }
    }
}

extension PBMachinePressureTone {
    func ink(_ normal: Color, text: Bool = true) -> Color {
        switch self {
        case .high: text ? DS.orangeInk : DS.orange
        case .critical: DS.red
        default: normal
        }
    }
}

extension PBDiskPressureState {
    func ink(_ normal: Color, text: Bool = true) -> Color {
        switch self {
        case .low: text ? DS.orangeInk : DS.orange
        case .critical: DS.red
        default: normal
        }
    }
}

/// A figure's type: SF Rounded at the mockup's size, growing with Dynamic Type from `style` and not past `limit` times
/// the mockup's size, so that a large figure keeps its room at the largest settings.
private struct StatusFigure: ViewModifier {
    @ScaledMetric private var size: CGFloat
    private let weight: Font.Weight
    private let rounded: Bool
    private let cap: CGFloat

    init(size: CGFloat, weight: Font.Weight, rounded: Bool, style: Font.TextStyle, limit: CGFloat) {
        _size = ScaledMetric(wrappedValue: size, relativeTo: style)
        self.weight = weight
        self.rounded = rounded
        cap = size * limit
    }

    func body(content: Content) -> some View {
        content.font(.system(size: min(size, cap), weight: weight, design: rounded ? .rounded : .default))
    }
}

extension View {
    /// SF Rounded for a number: `size` at the default text size, scaled with `style`, kept under `limit` times `size`.
    func figure(_ size: CGFloat, _ weight: Font.Weight = .semibold, style: Font.TextStyle = .body, limit: CGFloat = 2) -> some View {
        modifier(StatusFigure(size: size, weight: weight, rounded: true, style: style, limit: limit))
    }

    /// A card of 电脑状态: radius 22, `lift`, and in the light theme the mockup's soft shadow.
    func statusCard() -> some View { modifier(StatusCardSurface()) }
}

private struct StatusCardSurface: ViewModifier {
    @Environment(\.colorScheme) private var scheme

    func body(content: Content) -> some View {
        content
            .background(
                RoundedRectangle(cornerRadius: DS.Radius.group, style: .continuous).fill(DS.lift)
                    .shadow(color: .black.opacity(scheme == .dark ? 0 : 0.07), radius: 10, y: 6)
            )
            .clipShape(RoundedRectangle(cornerRadius: DS.Radius.group, style: .continuous))
    }
}

/// The surface alone, for a card whose content is laid out apart from it (the swap).
struct StatusCardBackground: View {
    @Environment(\.colorScheme) private var scheme

    var body: some View {
        RoundedRectangle(cornerRadius: DS.Radius.group, style: .continuous).fill(DS.lift)
            .shadow(color: .black.opacity(scheme == .dark ? 0 : 0.07), radius: 10, y: 6)
    }
}

/// A number whose digits roll (ui-spec A4): only the places that changed move, up when the number grew and down when it fell,
/// 200 ms. With 减弱动态效果 it is just text.
struct RollingText: View {
    let text: String
    let number: Double
    @Environment(\.accessibilityReduceMotion) private var reduce

    var body: some View {
        Text(text).monospacedDigit().lineLimit(1)
            .contentTransition(reduce ? .identity : .numericText(value: number))
            .animation(reduce ? nil : .easeOut(duration: 0.2), value: text)
    }
}

// MARK: The three rings (A2)

/// The SF Symbols of 电脑状态: a CPU, memory, a disk, a process.
enum StatusSymbol: String {
    case cpu
    case memory = "memorychip"
    case disk = "internaldrive"
    case process = "terminal"
}

private func ringSymbol(_ kind: PBMachineRingKind) -> StatusSymbol {
    switch kind {
    case .memory: .memory
    case .disk: .disk
    default: .cpu
    }
}

/// A progress ring: 54 across, line 5.5, round caps, from twelve o'clock, the icon in its middle. `fraction` nil draws only the track.
struct StatusRing: View {
    let fraction: Double?
    let tone: PBMachineRingTone
    let symbol: StatusSymbol
    @Environment(\.accessibilityReduceMotion) private var reduce

    var body: some View {
        ZStack {
            Circle().stroke(DS.fill2, lineWidth: 5.5)
            if let fraction {
                Circle().trim(from: 0, to: max(fraction, 0.002))
                    .stroke(tone.ink(DS.ink, text: false), style: StrokeStyle(lineWidth: 5.5, lineCap: .round))
                    .rotationEffect(.degrees(-90))
                    .animation(reduce ? nil : .timingCurve(0.2, 0.8, 0.2, 1, duration: 0.4), value: fraction)
            }
            Image(systemName: symbol.rawValue).font(.system(size: 18, weight: .regular)).foregroundStyle(tone.ink(DS.ink2, text: false))
        }
        .padding(2.75)
        .frame(width: 54, height: 54)
        .accessibilityHidden(true)
    }
}

/// The three rings at the top of a computer's page (design-system `rings`): one card, one target.
struct MachineRingsCard: View {
    let rings: [PBMachineRingView]
    let online: Bool
    var showHeader = false
    var statusDot: PBMachineStatusDot = .unspecified
    var statusBadge = ""
    var statusWarn = false
    let onOpen: () -> Void
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        VStack(spacing: 0) {
            if showHeader {
                HStack(spacing: 8) {
                    Text(L10n.settingsStatusCardTitle())
                        .font(.body.weight(.semibold))
                        .foregroundStyle(DS.ink)
                    Spacer(minLength: 0)
                    HStack(spacing: 5) {
                        machineStatusDotView(statusDot, size: 7)
                        if !statusBadge.isEmpty {
                            Text(statusBadge)
                                .font(.subheadline)
                                .foregroundStyle(statusWarn ? DS.orangeInk : DS.ink2)
                                .fontWeight(statusWarn ? .semibold : .regular)
                        }
                        if online {
                            Arrow(DS.ink2)
                        }
                    }
                }
                .padding(.horizontal, 14)
                .padding(.vertical, 12)
                Rectangle().fill(DS.sep).frame(height: 1)
                    .padding(.horizontal, 14)
                    .accessibilityHidden(true)
            }
            if typeSize.isAccessibilitySize {
                VStack(alignment: .leading, spacing: 14) {
                    ForEach(Array(rings.enumerated()), id: \.offset) { _, r in
                        HStack(spacing: 16) {
                            StatusRing(fraction: r.hasValue_p ? r.fraction : nil, tone: r.tone, symbol: ringSymbol(r.kind))
                            VStack(alignment: .leading, spacing: 2) {
                                Text(r.valueText.rendered().or("—")).figure(17, style: .body, limit: 3).foregroundStyle(r.tone.ink(DS.ink))
                                Text(r.label.rendered()).font(.caption).foregroundStyle(DS.ink2)
                            }
                            Spacer(minLength: 0)
                        }
                        .accessibilityElement(children: .ignore)
                        .accessibilityLabel(r.a11YLabel.rendered())
                        .accessibilityAddTraits(online ? .isButton : [])
                    }
                }
                .padding(.horizontal, 18).padding(.vertical, 16)
            } else {
                HStack(alignment: .top, spacing: 0) {
                    ForEach(Array(rings.enumerated()), id: \.offset) { _, r in
                        VStack(spacing: 8) {
                            StatusRing(fraction: r.hasValue_p ? r.fraction : nil, tone: r.tone, symbol: ringSymbol(r.kind))
                            VStack(spacing: 0) {
                                Text(r.valueText.rendered().or("—")).figure(17, style: .body).foregroundStyle(r.tone.ink(DS.ink)).lineLimit(1).minimumScaleFactor(0.7)
                                Text(r.label.rendered()).font(.caption).foregroundStyle(DS.ink2).multilineTextAlignment(.center)
                            }
                        }
                        .frame(maxWidth: .infinity)
                        .accessibilityElement(children: .ignore)
                        .accessibilityLabel(r.a11YLabel.rendered())
                        .accessibilityAddTraits(online ? .isButton : [])
                    }
                }
                .padding(.horizontal, 6).padding(.top, 16).padding(.bottom, 14)
            }
        }
        .frame(maxWidth: .infinity)
        .background(DS.gbg, in: RoundedRectangle(cornerRadius: DS.Radius.group, style: .continuous))
        .opacity(online ? 1 : 0.55)
        .ringsCardAction(enabled: online, action: onOpen)
        .padding(.init(top: 0, leading: 16, bottom: 12, trailing: 16))
        .accessibilityIdentifier("machine-rings")
    }
}

private extension View {
    @ViewBuilder func ringsCardAction(enabled: Bool, action: @escaping () -> Void) -> some View {
        if enabled {
            self
                .contentShape(RoundedRectangle(cornerRadius: DS.Radius.group, style: .continuous))
                .onTapGesture { action() }
                .accessibilityAction { action() }
        } else {
            self
        }
    }
}

// MARK: Scale, bar, meter

/// The memory pressure scale (ui-spec A3 ②): 6 high, green to yellow (at 70%) to red.
struct PressureScale: View {
    let position: Double

    var body: some View {
        GeometryReader { geo in
            let w = geo.size.width
            let x = min(max(position * w, 6), max(6, w - 6))
            ZStack(alignment: .leading) {
                Capsule().fill(
                    LinearGradient(
                        stops: [
                            .init(color: DS.green, location: 0),
                            .init(color: DS.green, location: 0.45),
                            .init(color: DS.yellow, location: 0.7),
                            .init(color: DS.red, location: 1),
                        ],
                        startPoint: .leading,
                        endPoint: .trailing
                    )
                )
                .frame(height: 6)
                Circle().fill(DS.ink).frame(width: 12, height: 12)
                    .background(Circle().fill(DS.lift).frame(width: 18, height: 18))
                    .position(x: x, y: 6)
            }
            .frame(height: 12)
        }
        .frame(height: 12)
        .accessibilityHidden(true)
    }
}

/// The disk bar (ui-spec A3 ②): 10 high; filled proportion in `ink` (or red when critical) against the groove.
struct DiskBar: View {
    let usedFraction: Double
    let state: PBDiskPressureState

    var body: some View {
        let clamped = min(max(usedFraction, 0), 1)
        let fillColor: Color = switch state {
        case .critical: DS.red
        case .low: DS.orange
        default: DS.ink
        }
        GeometryReader { geo in
            ZStack(alignment: .leading) {
                Capsule().fill(DS.fill2)
                if clamped > 0.0005 {
                    Capsule().fill(fillColor).frame(width: max(4, geo.size.width * clamped))
                }
            }
            .frame(width: geo.size.width, height: 10, alignment: .leading)
        }
        .frame(height: 10)
        .accessibilityHidden(true)
    }
}

/// The bar of a ranking row and the words after it: the bar is its share of the row's width (at least 5), and never crowds the words out.
struct UsageMeter: Layout {
    var fraction: Double

    var animatableData: Double {
        get { fraction }
        set { fraction = newValue }
    }

    private let gap: CGFloat = 10

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let w = proposal.width ?? 200
        let text = subviews[1].sizeThatFits(ProposedViewSize(width: max(0, w - gap - 5), height: nil))
        return CGSize(width: w, height: max(5, text.height))
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        let text = subviews[1].sizeThatFits(ProposedViewSize(width: max(0, bounds.width - gap - 5), height: nil))
        let wanted = max(5, bounds.width * fraction)
        let barW = max(5, min(wanted, bounds.width - text.width - gap))
        subviews[0].place(at: CGPoint(x: bounds.minX, y: bounds.midY), anchor: .leading, proposal: ProposedViewSize(width: barW, height: 5))
        subviews[1].place(at: CGPoint(x: bounds.minX + barW + gap, y: bounds.midY), anchor: .leading, proposal: ProposedViewSize(width: text.width, height: nil))
    }
}

// MARK: The chart (A3 ①, A4)

/// Smoothly advances the visible window end when new samples arrive in live mode.
@MainActor
final class ChartClock {
    private(set) var end: Double = 0
    private var lastLatest: Int64 = 0
    private var baseRealMs: Double = 0
    private var baseChartMs: Double = 0

    func advance(now: Date, first: Int64, latest: Int64, moving: Bool) -> Double {
        guard latest > 0 else {
            end = 0
            return 0
        }
        guard moving else {
            end = Double(latest)
            lastLatest = latest
            return end
        }
        let nowMs = now.timeIntervalSinceReferenceDate * 1000
        if lastLatest == 0 || latest < lastLatest {
            let start = first > 0 ? Double(min(latest, first + 2000)) : Double(latest)
            lastLatest = latest
            baseRealMs = nowMs
            baseChartMs = start
            end = start
            return end
        }
        if latest > lastLatest {
            let cur = min(Double(lastLatest), baseChartMs + max(0, nowMs - baseRealMs))
            baseChartMs = max(cur, Double(first))
            baseRealMs = nowMs
            lastLatest = latest
        }
        let elapsed = max(0, nowMs - baseRealMs)
        let target = Double(latest)
        let gap = target - baseChartMs
        let speed = gap > 3000 ? max(1.0, gap / 1500.0) : 1.0
        end = min(target, baseChartMs + elapsed * speed)
        return end
    }
}

/// The smooth curve through the points, without overshoot (a monotone cubic).
func smoothPath(_ pts: [CGPoint]) -> Path {
    var path = Path()
    guard let first = pts.first else { return path }
    path.move(to: first)
    guard pts.count > 1 else { return path }
    let n = pts.count
    let dx = (0..<n - 1).map { pts[$0 + 1].x - pts[$0].x }
    let d = (0..<n - 1).map { dx[$0] == 0 ? CGFloat(0) : (pts[$0 + 1].y - pts[$0].y) / dx[$0] }
    var m = [CGFloat](repeating: 0, count: n)
    m[0] = d[0]
    m[n - 1] = d[n - 2]
    for i in 1..<max(1, n - 1) where n > 2 { m[i] = d[i - 1] * d[i] <= 0 ? 0 : (d[i - 1] + d[i]) / 2 }
    for i in 0..<n - 1 {
        if d[i] == 0 { m[i] = 0; m[i + 1] = 0; continue }
        let a = m[i] / d[i], b = m[i + 1] / d[i]
        let s = a * a + b * b
        if s > 9 { let t = 3 / s.squareRoot(); m[i] = t * a * d[i]; m[i + 1] = t * b * d[i] }
    }
    for i in 0..<n - 1 {
        let h = dx[i] / 3
        path.addCurve(
            to: pts[i + 1],
            control1: CGPoint(x: pts[i].x + h, y: pts[i].y + m[i] * h),
            control2: CGPoint(x: pts[i + 1].x - h, y: pts[i + 1].y - m[i + 1] * h)
        )
    }
    return path
}

/// The chart driven by Core's `PBMachineChartView`. Dragging dispatches scrub index changes; releasing clears scrub.
struct StatusChart: View {
    let chart: PBMachineChartView
    let color: Color
    let moving: Bool
    let onScrub: (Int32, Bool) -> Void
    @State private var clock = ChartClock()
    @State private var width: CGFloat = 0
    @GestureState private var pressing = false
    @State private var lastIndex: Int?

    private var windowMs: Double {
        let span = chart.windowEndMs - chart.windowStartMs
        return span > 0 ? Double(span) : 116_000
    }

    private func selectedIndex(at x: CGFloat) -> Int? {
        guard width > 0, !chart.points.isEmpty else { return nil }
        let end = clock.end > 0 ? clock.end : Double(chart.windowEndMs)
        let clampedX = min(max(x, 0), width)
        let targetT = end - (1.0 - Double(clampedX / width)) * windowMs
        var bestIdx: Int?
        var bestDist = Double.infinity
        for (i, pt) in chart.points.enumerated() where Double(pt.timeMs) <= end {
            let dist = abs(Double(pt.timeMs) - targetT)
            if dist < bestDist {
                bestDist = dist
                bestIdx = i
            }
        }
        return bestIdx ?? (chart.points.isEmpty ? nil : chart.points.count - 1)
    }

    private func pick(_ x: CGFloat) {
        guard let idx = selectedIndex(at: x) else { return }
        if idx != lastIndex {
            lastIndex = idx
            onScrub(Int32(idx), false)
            UISelectionFeedbackGenerator().selectionChanged()
        }
    }

    var body: some View {
        TimelineView(.animation(minimumInterval: nil, paused: !moving)) { tl in
            let end = clock.advance(
                now: tl.date,
                first: chart.points.first?.timeMs ?? 0,
                latest: chart.points.last?.timeMs ?? chart.windowEndMs,
                moving: moving
            )
            Canvas { ctx, size in draw(&ctx, size, end: Int64(end)) }
        }
        .frame(height: 104)
        .background(
            GeometryReader { g in
                Color.clear
                    .onAppear { width = g.size.width }
                    .onChange(of: g.size.width) { _, w in width = w }
            }
        )
        .contentShape(Rectangle())
        .gesture(
            DragGesture(minimumDistance: 0)
                .updating($pressing) { _, state, _ in state = true }
                .onChanged { pick($0.location.x) }
        )
        .onChange(of: pressing) { _, now in
            if !now && lastIndex != nil {
                lastIndex = nil
                onScrub(0, true)
            }
        }
    }

    private func draw(_ ctx: inout GraphicsContext, _ size: CGSize, end: Int64) {
        let pts = chart.points
        let ceiling = max(chart.ceiling, 1)
        let win = Int64(windowMs)
        let start = end - win
        let gapThreshold = chart.gapThresholdMs > 0 ? chart.gapThresholdMs : 6_000
        let h = size.height
        let pad: CGFloat = 4
        func px(_ x: Double, _ y: Double) -> CGPoint {
            CGPoint(x: x * size.width, y: pad + (1 - y) * (h - 2 * pad))
        }

        // Split visible points into contiguous runs separated by gaps > gapThreshold.
        var runs: [[CGPoint]] = []
        var current: [CGPoint] = []
        var prevTime: Int64?
        for p in pts where p.timeMs >= start - gapThreshold && p.timeMs <= end {
            if let prev = prevTime, p.timeMs - prev > gapThreshold, !current.isEmpty {
                runs.append(current)
                current = []
            }
            let normX = Double(p.timeMs - start) / Double(win)
            let normY = min(max(p.value / ceiling, 0), 1)
            current.append(px(normX, normY))
            prevTime = p.timeMs
        }
        if !current.isEmpty { runs.append(current) }

        for runPts in runs where runPts.count >= 2 {
            let line = smoothPath(runPts)
            var fill = line
            fill.addLine(to: CGPoint(x: runPts.last!.x, y: h))
            fill.addLine(to: CGPoint(x: runPts.first!.x, y: h))
            fill.closeSubpath()
            let top = runPts.map(\.y).min() ?? 0
            ctx.fill(
                fill,
                with: .linearGradient(
                    Gradient(colors: [color.opacity(0.16), color.opacity(0)]),
                    startPoint: CGPoint(x: 0, y: top),
                    endPoint: CGPoint(x: 0, y: h)
                )
            )
            ctx.stroke(line, with: .color(color), style: StrokeStyle(lineWidth: 2, lineCap: .round, lineJoin: .round))
        }

        if let latest = pts.last(where: { $0.timeMs <= end }) {
            let o = px(1, min(max(latest.value / ceiling, 0), 1))
            ctx.fill(Path(ellipseIn: CGRect(x: size.width - 4 - 3.5, y: o.y - 3.5, width: 7, height: 7)), with: .color(color))
        }

        if chart.hasScrub_p {
            let idx = Int(chart.scrubIndex)
            if pts.indices.contains(idx) {
                let s = pts[idx]
                let x = CGFloat(Double(s.timeMs - start) / Double(win)) * size.width
                var rule = Path()
                rule.move(to: CGPoint(x: x, y: 0))
                rule.addLine(to: CGPoint(x: x, y: h))
                ctx.stroke(rule, with: .color(DS.ink3), lineWidth: 1)
                let o = px(0, min(max(s.value / ceiling, 0), 1))
                ctx.fill(Path(ellipseIn: CGRect(x: x - 3.5, y: o.y - 3.5, width: 7, height: 7)), with: .color(color))
            }
        }
    }
}
