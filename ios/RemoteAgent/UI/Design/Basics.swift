// The small shared pieces of docs/design/client-v58.html: lines, tap
// targets, dots, arrows, chips. What each is for is in docs/design-system.md §6; the Android
// counterparts are in ui/Kit.kt and ui/Common.kt.
import RACore
import RAMarkdownUI
import SwiftUI
import UIKit

extension View {
    /// A 1 pt line at the bottom of the element, as a CSS border-bottom.
    func bottomLine(_ color: Color = DS.sep, startInset: CGFloat = 0) -> some View {
        overlay(alignment: .bottom) {
            Rectangle().fill(color).frame(height: 1).padding(.leading, startInset).accessibilityHidden(true)
        }
    }

    /// A 1 pt line at the top, as a CSS border-top.
    func topLine(_ color: Color = DS.sep) -> some View {
        overlay(alignment: .top) { Rectangle().fill(color).frame(height: 1).accessibilityHidden(true) }
    }

    /// Makes the element at least 44 × 44 to the finger without changing what is drawn (design-system §4).
    func hitTarget(minWidth: CGFloat = DS.minHit, minHeight: CGFloat = DS.minHit, alignment: Alignment = .center) -> some View {
        frame(minWidth: minWidth, minHeight: minHeight, alignment: alignment).contentShape(Rectangle())
    }

    /// On a wide screen (an iPad, a phone on its side) settings rows keep a centred column instead of stretching across the window.
    func readableColumn() -> some View {
        frame(maxWidth: DS.column).frame(maxWidth: .infinity)
    }
}

/// How a tappable piece answers a touch: it dims, as the system's plain buttons do.
struct PressStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label.opacity(configuration.isPressed ? 0.55 : 1)
    }
}

/// How a tappable row answers a touch: its surface darkens under the finger, as a system list row's does.
struct RowPressStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .contentShape(Rectangle())
            .background(configuration.isPressed ? DS.fill : Color.clear, ignoresSafeAreaEdges: [])
    }
}

/// The taps the system's own controls answer with a tick. Confirmations and
/// actions that cannot be taken back get one (the dialogs and the action
/// menu send them); ordinary navigation does not.
@MainActor
enum Haptics {
    /// A confirmation went through.
    static func confirm() { UINotificationFeedbackGenerator().notificationOccurred(.success) }
    /// Something that cannot be taken back was confirmed.
    static func destructive() { UINotificationFeedbackGenerator().notificationOccurred(.warning) }
    /// One of several was chosen.
    static func selection() { UISelectionFeedbackGenerator().selectionChanged() }
}

/// A status dot: a drawn circle, not an icon; a status word always stands next to it (design-system §2, §5).
struct Dot: View {
    private let color: Color
    @ScaledMetric private var size: CGFloat

    init(_ color: Color, size: CGFloat = 8) {
        self.color = color
        _size = ScaledMetric(wrappedValue: size, relativeTo: .caption)
    }

    var body: some View {
        Circle().fill(color).frame(width: size, height: size).accessibilityHidden(true)
    }
}

/// Which way an arrow points: to the right it goes to another page; down and up open and close in place (design-system §5).
enum ArrowDirection: Sendable { case right, down, up }

/// An arrow as tall as text in `style`.
struct Arrow: View {
    private let color: Color?
    private let style: Font
    private let direction: ArrowDirection

    init(_ color: Color? = nil, style: Font = .subheadline, direction: ArrowDirection = .right) {
        self.color = color
        self.style = style
        self.direction = direction
    }

    var body: some View {
        let icon: String = switch direction {
        case .right: "chevron.right"
        case .down: "chevron.down"
        case .up: "chevron.up"
        }
        SymbolImage(icon, style: style, color: color)
    }
}

/// Text with a trailing arrow, as 处理 with one down or 管理 with one to the right.
struct ArrowText: View {
    private let text: String
    private let color: Color
    private let style: Font
    private let direction: ArrowDirection

    init(_ text: String, color: Color = DS.blue, style: Font = .subheadline, direction: ArrowDirection = .right) {
        self.text = text
        self.color = color
        self.style = style
        self.direction = direction
    }

    var body: some View {
        HStack(spacing: 4) {
            Text(text).font(style).ink(color)
            Arrow(color, style: style, direction: direction)
        }
    }
}

/// Text held to `lines` lines, which says whether that cut it. How many
/// lines a text takes is only known once it is laid out: one long
/// paragraph is one line to count and many to draw.
struct CutText: View {
    private let text: String
    private let style: Font
    private let color: Color
    private let lines: Int
    @Binding private var cut: Bool
    @Environment(\.chatDiagnosticEntity) private var diagnosticEntity
    @State private var shown: CGFloat = 0
    @State private var whole: CGFloat = 0

    init(_ text: String, style: Font, color: Color, lines: Int, cut: Binding<Bool>) {
        self.text = text
        self.style = style
        self.color = color
        self.lines = lines
        _cut = cut
    }

    var body: some View {
        let entity = diagnosticEntity
        Text(text).font(style).ink(color).lineLimit(lines)
            .onGeometryChange(for: CGFloat.self) { g in ChatDiagnosticRing.shared.measure(.cutShown, entity: entity) { g.size.height } } action: {
                ChatDiagnosticRing.shared.record(.cutShown, entity: entity, Double($0)); shown = $0
            }
            .background {
                // The same text at the same width with nothing held back, measured and not drawn.
                Text(text).font(style).ink(color).fixedSize(horizontal: false, vertical: true).hidden()
                    .onGeometryChange(for: CGFloat.self) { g in ChatDiagnosticRing.shared.measure(.cutWhole, entity: entity) { g.size.height } } action: {
                        ChatDiagnosticRing.shared.record(.cutWhole, entity: entity, Double($0)); whole = $0
                    }
                    .accessibilityHidden(true)
            }
            .onChange(of: whole > shown + 0.5, initial: true) { _, isCut in
                ChatDiagnosticRing.shared.record(.cutBinding, entity: entity, isCut ? 1 : 0); cut = isCut
            }
    }
}

/// A project's name: the last folder of its path.
func projectName(_ key: String) -> String {
    key.split(separator: "/").last.map(String.init) ?? "未知项目"
}

/// The icon at the head of a device's row (design-system §6 「设备行」): the
/// SF Symbol in `ink2`, no coloured tile. It grows with the row's text.
struct DeviceIcon: View {
    private let symbol: String

    init(_ symbol: String) { self.symbol = symbol }

    var body: some View {
        Image(systemName: symbol)
            .font(.body)
            .foregroundStyle(DS.ink2)
            .frame(minWidth: 26)
            .accessibilityHidden(true)
    }
}

/// A small pill of metadata; 44 pt tall to the finger when it can be tapped.
struct Chip: View {
    enum Tone: Sendable { case plain, warn, auto, danger }

    private let text: String
    private let tone: Tone
    private let action: (() -> Void)?

    init(_ text: String, tone: Tone = .plain, action: (() -> Void)? = nil) {
        self.text = text
        self.tone = tone
        self.action = action
    }

    var body: some View {
        let (ink, back): (Color, Color) = switch tone {
        case .plain: (DS.ink2, DS.fill)
        case .warn: (DS.orangeInk, DS.orangeSoft)
        case .auto: (DS.blue, DS.fill)
        case .danger: (DS.red, DS.fill)
        }
        let pill = Text(text).font(.footnote).ink(ink).lineLimit(1)
            .padding(.horizontal, 9).padding(.vertical, 4)
            .background(back, in: Capsule())
        if let action {
            Button(action: action) { pill.hitTarget(minWidth: 0) }.buttonStyle(PressStyle())
        } else {
            pill
        }
    }
}

/// A box with a 1 pt outline, radius 10.
struct Bordered<Content: View>: View {
    private let content: Content

    init(@ViewBuilder content: () -> Content) { self.content = content() }

    var body: some View {
        content.padding(12)
            .overlay(RoundedRectangle(cornerRadius: DS.Radius.small, style: .continuous).strokeBorder(DS.sep, lineWidth: 1))
    }
}

/// A row at least 44 pt tall with the page's side padding; tappable when it has an action.
struct Row44<Content: View>: View {
    private let action: (() -> Void)?
    private let content: Content

    init(action: (() -> Void)? = nil, @ViewBuilder content: () -> Content) {
        self.action = action
        self.content = content()
    }

    var body: some View {
        let row = HStack(spacing: 12) { content }
            .padding(.horizontal, DS.Space.l).padding(.vertical, 8)
            .frame(maxWidth: .infinity, minHeight: DS.minHit, alignment: .leading)
        if let action {
            Button(action: action) { row }.buttonStyle(RowPressStyle())
        } else {
            row
        }
    }
}

/// A code or command block outside the terminal: `gbg`, radius 14, mono (design-system §2, §6).
struct Code: View {
    private let text: String
    private let maxLines: Int?
    private let background: Color?
    private let border: Bool

    init(_ text: String, maxLines: Int? = nil, background: Color? = nil, border: Bool = false) {
        self.text = text
        self.maxLines = maxLines
        self.background = background
        self.border = border
    }

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: DS.Radius.code, style: .continuous)
        Text(text).font(.footnote.monospaced()).ink(DS.ink2)
            .lineLimit(maxLines)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 10).padding(.vertical, 7)
            .background(background ?? DS.gbg, in: shape)
            .overlay { if border { shape.strokeBorder(DS.sep, lineWidth: 1) } }
    }
}

/// The app's mark, the one on its icon: a terminal prompt and its cursor,
/// in the coordinates of the icon's 512 master drawing
/// (scripts/gen-icons.swift).
struct AppMark: Shape {
    private static let outline: Path = {
        var prompt = Path()
        prompt.move(to: CGPoint(x: 146, y: 176))
        prompt.addLine(to: CGPoint(x: 238, y: 256))
        prompt.addLine(to: CGPoint(x: 146, y: 336))
        var p = prompt.strokedPath(StrokeStyle(lineWidth: 42, lineCap: .round, lineJoin: .round))
        p.addRoundedRect(in: CGRect(x: 276, y: 314, width: 110, height: 42), cornerSize: CGSize(width: 21, height: 21))
        return p
    }()
    /// What the mark covers of the master drawing.
    private static let box = CGRect(x: 125, y: 155, width: 261, height: 202)

    func path(in rect: CGRect) -> Path {
        let k = min(rect.width / Self.box.width, rect.height / Self.box.height)
        let t = CGAffineTransform(
            translationX: rect.midX - Self.box.width * k / 2, y: rect.midY - Self.box.height * k / 2
        ).scaledBy(x: k, y: k).translatedBy(x: -Self.box.minX, y: -Self.box.minY)
        return Self.outline.applying(t)
    }

    /// The mark in `color`, `size` tall.
    static func view(size: CGFloat, color: Color) -> some View {
        AppMark().fill(color)
            .frame(width: size * 261 / 202, height: size)
            .accessibilityHidden(true)
    }
}

// MARK: - Scroll edge fades

/// Where a scrolling region is cut by the bar above or below it, its content fades into the surface it is on
/// instead of ending in a hard line (design-system §4 材质), over 24 pt. An edge fades only while there is
/// content past it, so a list at rest keeps its first and last rows whole. Before iOS 18 it does not fade.
private struct ScrollEdgeFades: ViewModifier {
    private static let depth: CGFloat = 24
    @State private var before: CGFloat = 0
    @State private var after: CGFloat = 0

    func body(content: Content) -> some View {
        if #available(iOS 18, *) {
            content
                .onScrollGeometryChange(for: [CGFloat].self) { g in
                    [g.contentOffset.y + g.contentInsets.top, g.contentSize.height - g.contentOffset.y - g.containerSize.height + g.contentInsets.bottom]
                } action: { _, new in
                    before = new[0]
                    after = new[1]
                }
                .mask {
                    VStack(spacing: 0) {
                        LinearGradient(colors: [.black.opacity(1 - min(max(before / Self.depth, 0), 1)), .black], startPoint: .top, endPoint: .bottom)
                            .frame(height: Self.depth)
                        Rectangle()
                        LinearGradient(colors: [.black, .black.opacity(1 - min(max(after / Self.depth, 0), 1))], startPoint: .top, endPoint: .bottom)
                            .frame(height: Self.depth)
                    }
                }
        } else {
            content
        }
    }
}

extension View {
    /// The edges of a scroll view that content passes under fade into the surface behind it.
    func scrollEdgeFades() -> some View { modifier(ScrollEdgeFades()) }
}
