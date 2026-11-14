// The drawing of 电脑状态 (ui-spec A; 界面稿 v2 第 ② 部分): the cards' surface, the rolling digits, a ring and the
// three-ring entrance, the memory pressure scale, the disk bar, the bar of a ranking row and the chart. Values come
// from RACore's MachineStatusModel; nothing here decides what a figure is.
import RACore
import RAProto
import SwiftUI
import UIKit

extension MachineTone {
    /// The colour of a figure at this tone: red, orange (its readable ink as text), or `normal`.
    func ink(_ normal: Color, text: Bool = true) -> Color {
        switch self {
        case .normal: normal
        case .warn: text ? DS.orangeInk : DS.orange
        case .critical: DS.red
        }
    }
}

/// A figure's type: SF Rounded at the mockup's size, growing with Dynamic Type from `style` and not past `limit` times
/// the mockup's size, so that a large figure keeps its room at the largest settings.
private struct StatusFigure: ViewModifier {
    @ScaledMetric private var size: CGFloat
    private let weight: Font.Weight
    private let rounded: Bool

    init(size: CGFloat, weight: Font.Weight, rounded: Bool, style: Font.TextStyle, limit: CGFloat) {
        _size = ScaledMetric(wrappedValue: size, relativeTo: style)
        self.weight = weight
        self.rounded = rounded
        cap = size * limit
    }

    private let cap: CGFloat

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
                RoundedRectangle(cornerRadius: 22, style: .continuous).fill(DS.lift)
                    .shadow(color: .black.opacity(scheme == .dark ? 0 : 0.07), radius: 10, y: 6))
            .clipShape(RoundedRectangle(cornerRadius: 22, style: .continuous))
    }
}

/// The surface alone, for a card whose content is laid out apart from it (the swap).
struct StatusCardBackground: View {
    @Environment(\.colorScheme) private var scheme

    var body: some View {
        RoundedRectangle(cornerRadius: 22, style: .continuous).fill(DS.lift)
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

private func ringSymbol(_ kind: RingKind) -> StatusSymbol {
    switch kind {
    case .cpu: .cpu
    case .memory: .memory
    case .disk: .disk
    }
}

/// A progress ring: 54 across, line 5.5, round caps, from twelve o'clock, the icon in its middle. `fraction` nil draws only the track.
struct StatusRing: View {
    let fraction: Double?
    let tone: MachineTone
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

/// The three rings at the top of a computer's page (design-system `rings`): one card, one target. A computer that is not
/// `online` draws tracks and dashes, at 55%, and cannot be opened.
struct MachineRingsCard: View {
    let rings: [RingSpec]
    let online: Bool
    let onOpen: () -> Void
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        Group {
            if typeSize.isAccessibilitySize {
                // Three rings side by side do not fit the figures of these sizes: one under another, the words beside each.
                VStack(alignment: .leading, spacing: 14) {
                    ForEach(rings, id: \.kind) { r in
                        HStack(spacing: 16) {
                            StatusRing(fraction: r.fraction, tone: r.tone, symbol: ringSymbol(r.kind))
                            VStack(alignment: .leading, spacing: 2) {
                                Text(r.value).figure(17, style: .body, limit: 3).foregroundStyle(r.tone.ink(DS.ink))
                                Text(r.name).font(.caption).foregroundStyle(DS.ink2)
                            }
                            Spacer(minLength: 0)
                        }
                        .accessibilityElement(children: .ignore)
                        .accessibilityLabel(r.spoken)
                        .accessibilityAddTraits(online ? .isButton : [])
                        .accessibilityAction { if online { onOpen() } }
                    }
                }
                .padding(.horizontal, 18).padding(.vertical, 16)
            } else {
                HStack(alignment: .top, spacing: 0) {
                    ForEach(rings, id: \.kind) { r in
                        VStack(spacing: 8) {
                            StatusRing(fraction: r.fraction, tone: r.tone, symbol: ringSymbol(r.kind))
                            VStack(spacing: 0) {
                                Text(r.value).figure(17, style: .body).foregroundStyle(r.tone.ink(DS.ink)).lineLimit(1).minimumScaleFactor(0.7)
                                Text(r.name).font(.caption).foregroundStyle(DS.ink2).multilineTextAlignment(.center)
                            }
                        }
                        .frame(maxWidth: .infinity)
                        .accessibilityElement(children: .ignore)
                        .accessibilityLabel(r.spoken)
                        .accessibilityAddTraits(online ? .isButton : [])
                        .accessibilityAction { if online { onOpen() } }
                    }
                }
                .padding(.horizontal, 6).padding(.top, 16).padding(.bottom, 14)
            }
        }
        .frame(maxWidth: .infinity)
        .background(DS.gbg, in: RoundedRectangle(cornerRadius: 22, style: .continuous))
        .opacity(online ? 1 : 0.55)
        .contentShape(RoundedRectangle(cornerRadius: 22, style: .continuous))
        .onTapGesture { if online { onOpen() } }
        .padding(.init(top: 0, leading: 16, bottom: 12, trailing: 16))
        .accessibilityIdentifier("machine-rings")
    }
}

// MARK: Scale, bar, meter

/// The memory pressure scale (ui-spec A3 ②): 6 high, green to yellow (at 70%) to red, with the current position marked
/// by a 12 dot in ink ringed by 3 of the card's colour.
struct PressureScale: View {
    let position: Double

    var body: some View {
        GeometryReader { geo in
            let w = geo.size.width
            let x = min(max(position * w, 6), max(6, w - 6))
            ZStack(alignment: .leading) {
                Capsule().fill(LinearGradient(
                    stops: [.init(color: DS.green, location: 0), .init(color: DS.green, location: 0.45),
                            .init(color: DS.yellow, location: 0.7), .init(color: DS.red, location: 1)],
                    startPoint: .leading, endPoint: .trailing))
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

/// The disk bar (ui-spec A3 ②): 10 high; remote-agent's share in ink, the rest used in `faint`, a 1.5 gap between, the free room
/// the groove. All red when the disk is 紧张.
struct DiskBar: View {
    let agentShare: Double
    let otherShare: Double
    let tone: MachineTone

    var body: some View {
        let red = tone == .critical
        let agent = min(max(agentShare, 0), 1)
        let other = min(max(otherShare, 0), 1 - agent)
        GeometryReader { geo in
            let gap: CGFloat = agent > 0.0005 && other > 0.0005 ? 1.5 : 0
            let room = geo.size.width - gap
            HStack(spacing: 0) {
                if agent > 0.0005 { Rectangle().fill(red ? DS.red : DS.ink).frame(width: room * agent) }
                if gap > 0 { Color.clear.frame(width: gap) }
                if other > 0.0005 { Rectangle().fill(red ? DS.red : DS.faint).frame(width: room * other) }
                Spacer(minLength: 0)
            }
            .frame(width: geo.size.width, height: 10, alignment: .leading)
            .background(DS.fill2)
            .clipShape(Capsule())
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

/// The view's clock delegates all timing, including poll-gap catch-up, to the tested core model.
@MainActor
final class ChartClock {
    private(set) var end: Double = 0
    private var timeline = ChartTimeline()

    func advance(now: Date, first: Int64, latest: Int64, moving: Bool) -> Double {
        end = timeline.advance(nowMs: now.timeIntervalSinceReferenceDate * 1000, firstMs: first, latestMs: latest, moving: moving)
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
            to: pts[i + 1], control1: CGPoint(x: pts[i].x + h, y: pts[i].y + m[i] * h),
            control2: CGPoint(x: pts[i + 1].x - h, y: pts[i + 1].y - m[i + 1] * h))
    }
    return path
}

/// The chart: the last two minutes (116 s), the vertical axis 0 to `ceiling`. Pressing and dragging picks a sample (`scrub`
/// gets it, nil on release) and draws a line and a dot on it. Nothing in it is a state of the page: that is the page's.
struct StatusChart: View {
    let points: [ChartPoint]
    let ceiling: Double
    let color: Color
    /// The curve slides evenly; false steps it a sample at a time (减弱动态效果, stale data, a still frame).
    let moving: Bool
    @Binding var scrub: ChartPoint?
    @State private var clock = ChartClock()
    @State private var width: CGFloat = 0
    @GestureState private var pressing = false
    @State private var lastIndex: Int?

    private func selected(at x: CGFloat) -> ChartPoint? {
        guard width > 0 else { return nil }
        let end = Int64(clock.end)
        let shown = points.filter { $0.timeMs <= end }
        let t = clock.end - (1 - Double(x / width)) * Double(chartWindowMs)
        return nearestSample(shown, at: t).map { shown[$0] }
    }

    private func pick(_ x: CGFloat) {
        let p = selected(at: x)
        if p?.timeMs != scrub?.timeMs {
            scrub = p
            if p != nil { UISelectionFeedbackGenerator().selectionChanged() }
        }
    }

    var body: some View {
        TimelineView(.animation(minimumInterval: nil, paused: !moving)) { tl in
            let end = clock.advance(now: tl.date, first: points.first?.timeMs ?? 0, latest: points.last?.timeMs ?? 0, moving: moving)
            Canvas { ctx, size in draw(&ctx, size, end: Int64(end)) }
        }
        .frame(height: 104)
        .background(GeometryReader { g in Color.clear.onAppear { width = g.size.width }.onChange(of: g.size.width) { _, w in width = w } })
        .contentShape(Rectangle())
        .gesture(
            DragGesture(minimumDistance: 0)
                .updating($pressing) { _, state, _ in state = true }
                .onChanged { pick($0.location.x) }
        )
        .onChange(of: pressing) { _, now in if !now { scrub = nil } }
    }

    private func draw(_ ctx: inout GraphicsContext, _ size: CGSize, end: Int64) {
        let h = size.height, pad: CGFloat = 4
        func px(_ x: Double, _ y: Double) -> CGPoint { CGPoint(x: x * size.width, y: pad + (1 - y) * (h - 2 * pad)) }
        for run in chartRuns(points, endMs: end, ceiling: ceiling) where run.count >= 2 {
            let pts = run.map { px($0.x, $0.y) }
            let line = smoothPath(pts)
            var fill = line
            fill.addLine(to: CGPoint(x: pts.last!.x, y: h))
            fill.addLine(to: CGPoint(x: pts.first!.x, y: h))
            fill.closeSubpath()
            let top = pts.map(\.y).min() ?? 0
            ctx.fill(fill, with: .linearGradient(
                Gradient(colors: [color.opacity(0.16), color.opacity(0)]), startPoint: CGPoint(x: 0, y: top), endPoint: CGPoint(x: 0, y: h)))
            ctx.stroke(line, with: .color(color), style: StrokeStyle(lineWidth: 2, lineCap: .round, lineJoin: .round))
        }
        if let v = chartValue(at: end, in: points) {
            let o = px(1, min(max(v / ceiling, 0), 1))
            ctx.fill(Path(ellipseIn: CGRect(x: size.width - 4 - 3.5, y: o.y - 3.5, width: 7, height: 7)), with: .color(color))
        }
        if let s = scrub {
            let x = CGFloat(Double(s.timeMs - (end - chartWindowMs)) / Double(chartWindowMs)) * size.width
            var rule = Path()
            rule.move(to: CGPoint(x: x, y: 0))
            rule.addLine(to: CGPoint(x: x, y: h))
            ctx.stroke(rule, with: .color(DS.ink3), lineWidth: 1)
            let o = px(0, min(max(s.value / ceiling, 0), 1))
            ctx.fill(Path(ellipseIn: CGRect(x: x - 3.5, y: o.y - 3.5, width: 7, height: 7)), with: .color(color))
        }
    }
}
