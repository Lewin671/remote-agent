// The small shared pieces of docs/design/client-v37.html, sized as the
// mockup's CSS (1 CSS px = 1 pt): lines, tap targets, dots, arrows, avatars,
// chips. What each is for is in docs/design-system.md §6; the Android
// counterparts are in ui/Kit.kt and ui/Common.kt.
import SwiftUI
import UIKit

extension View {
    /// A 1 pt line at the bottom of the element, as a CSS border-bottom.
    func bottomLine(_ color: Color = Theme.line, startInset: CGFloat = 0) -> some View {
        overlay(alignment: .bottom) {
            Rectangle().fill(color).frame(height: 1).padding(.leading, startInset).accessibilityHidden(true)
        }
    }

    /// A 1 pt line at the top, as a CSS border-top.
    func topLine(_ color: Color = Theme.line) -> some View {
        overlay(alignment: .top) { Rectangle().fill(color).frame(height: 1).accessibilityHidden(true) }
    }

    /// Makes the element at least 44 × 44 to the finger without changing what is drawn (design-system §4).
    func hitTarget(minWidth: CGFloat = Theme.minHit, minHeight: CGFloat = Theme.minHit, alignment: Alignment = .center) -> some View {
        frame(minWidth: minWidth, minHeight: minHeight, alignment: alignment).contentShape(Rectangle())
    }

    /// On a wide screen (an iPad, a phone on its side) settings rows keep a centred column instead of stretching across the window.
    func readableColumn() -> some View {
        frame(maxWidth: Theme.readableWidth).frame(maxWidth: .infinity)
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
            .background(configuration.isPressed ? Theme.chip : Color.clear, ignoresSafeAreaEdges: [])
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

/// A status: a dot and a word, not colour alone.
struct StatusLabel: View {
    let text: String
    let tone: StatusTone

    init(_ text: String, tone: StatusTone) {
        self.text = text
        self.tone = tone
    }

    var body: some View {
        HStack(spacing: 5) {
            Dot(Theme.dot(tone))
            Text(text).type(T.meta, Theme.muted)
        }
        .accessibilityElement(children: .combine)
    }
}

/// Which way an arrow points: to the right it goes to another page; down and up open and close in place (design-system §5).
enum ArrowDirection: Sendable { case right, down, up }

/// An arrow as tall as text in `style`.
struct Arrow: View {
    private let color: Color?
    private let style: TypeStyle
    private let direction: ArrowDirection

    init(_ color: Color? = nil, style: TypeStyle = T.sm, direction: ArrowDirection = .right) {
        self.color = color
        self.style = style
        self.direction = direction
    }

    var body: some View {
        let icon: LucideIcon = switch direction {
        case .right: Lucide.chevronRight
        case .down: Lucide.chevronDown
        case .up: Lucide.chevronUp
        }
        Ic(icon, style: style, color: color)
    }
}

/// Text with a trailing arrow, as 处理 with one down or 管理 with one to the right.
struct ArrowText: View {
    private let text: String
    private let color: Color
    private let style: TypeStyle
    private let direction: ArrowDirection

    init(_ text: String, color: Color = Theme.blue, style: TypeStyle = T.sm, direction: ArrowDirection = .right) {
        self.text = text
        self.color = color
        self.style = style
        self.direction = direction
    }

    var body: some View {
        HStack(spacing: 4) {
            Text(text).type(style, color)
            Arrow(color, style: style, direction: direction)
        }
    }
}

/// Text after an icon of its height, as the plus of 添加设备 or the turning circle of 正在连接….
struct IconText: View {
    private let icon: LucideIcon
    private let text: String
    private let color: Color
    private let style: TypeStyle

    init(_ icon: LucideIcon, _ text: String, color: Color = Theme.muted, style: TypeStyle = T.sm) {
        self.icon = icon
        self.text = text
        self.color = color
        self.style = style
    }

    var body: some View {
        HStack(spacing: 6) {
            Mark(icon, color: color, size: style.size, relativeTo: style.relativeTo)
            Text(text).type(style, color)
        }
        .accessibilityElement(children: .combine)
    }
}

/// A project's name: the last folder of its path.
func projectName(_ key: String) -> String {
    key.split(separator: "/").last.map(String.init) ?? "未知项目"
}

/// Avatar colour index 0...3, stable per project and the same as on the web and on Android.
func avatarIndex(_ key: String) -> Int {
    var h: UInt64 = 0
    for c in key.utf16 { h = (h &* 31 &+ UInt64(c)) & 0xffff_ffff }
    return Int(h % 4)
}

/// A project's letter on a gradient picked by its name: 40 on lists, 22 in sheets, 52 on the project page, 16 in chips.
struct Avatar: View {
    private let letter: String
    private let gradient: LinearGradient
    private let size: CGFloat

    /// `key` is the project's path.
    init(_ key: String, size: CGFloat = 40) {
        let name = projectName(key)
        letter = name.prefix(1).lowercased()
        gradient = Theme.avatarGradients[avatarIndex(name) % Theme.avatarGradients.count]
        self.size = size
    }

    /// The grey 全 of 全部项目.
    static func all(size: CGFloat = 40) -> Avatar { Avatar(letter: "全", gradient: Theme.avatarAll, size: size) }

    private init(letter: String, gradient: LinearGradient, size: CGFloat) {
        self.letter = letter
        self.gradient = gradient
        self.size = size
    }

    var body: some View {
        let (radius, font): (CGFloat, CGFloat) =
            size >= 52 ? (15, 22) : size >= 36 ? (11, 17) : size >= 22 ? (6, 12) : (4, 9)
        // A badge of a fixed size: its letter does not grow with the text around it.
        Text(letter)
            .font(.system(size: font, weight: .bold))
            .foregroundStyle(Theme.onSolid)
            .frame(width: size, height: size)
            .background(gradient, in: RoundedRectangle(cornerRadius: radius, style: .continuous))
            .accessibilityHidden(true)
    }
}

/// A small coloured square with an icon, in front of a computer's row. It grows with the row's text.
struct IconBadge: View {
    private let icon: LucideIcon
    private let color: Color
    @ScaledMetric(relativeTo: .subheadline) private var side: CGFloat = 26

    init(_ icon: LucideIcon, color: Color) {
        self.icon = icon
        self.color = color
    }

    var body: some View {
        Ic(icon, size: 16, color: Theme.onSolid, relativeTo: .subheadline)
            .frame(width: side, height: side)
            .background(color, in: RoundedRectangle(cornerRadius: side * 7 / 26, style: .continuous))
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
        case .plain: (Theme.fg2, Theme.chip)
        case .warn: (Theme.orangeInk, Theme.orangeSoft)
        case .auto: (Theme.blue, Theme.blueSoft)
        case .danger: (Theme.red, Theme.redSoft)
        }
        let pill = Text(text).type(T.meta, ink).lineLimit(1)
            .padding(.horizontal, 9).padding(.vertical, 4)
            .background(back, in: Capsule())
        if let action {
            Button(action: action) { pill.hitTarget(minWidth: 0) }.buttonStyle(PressStyle())
        } else {
            pill
        }
    }
}

/// A card on a grey page.
struct Card<Content: View>: View {
    private let content: Content

    init(@ViewBuilder content: () -> Content) { self.content = content() }

    var body: some View {
        content
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(Theme.card, in: RoundedRectangle(cornerRadius: Theme.Radius.md, style: .continuous))
    }
}

/// A box with a 1 pt outline, radius 10.
struct Bordered<Content: View>: View {
    private let content: Content

    init(@ViewBuilder content: () -> Content) { self.content = content() }

    var body: some View {
        content.padding(12)
            .overlay(RoundedRectangle(cornerRadius: Theme.Radius.sm, style: .continuous).strokeBorder(Theme.line, lineWidth: 1))
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
            .padding(.horizontal, Theme.Space.page).padding(.vertical, 8)
            .frame(maxWidth: .infinity, minHeight: Theme.minHit, alignment: .leading)
        if let action {
            Button(action: action) { row }.buttonStyle(RowPressStyle())
        } else {
            row
        }
    }
}

/// A code or command block outside the terminal: one surface above the one it is on, radius 6, mono 12 (design-system §2, §6).
struct Code: View {
    private let text: String
    private let maxLines: Int?
    private let background: Color?
    private let border: Bool
    @Environment(\.onSoft) private var onSoft

    init(_ text: String, maxLines: Int? = nil, background: Color? = nil, border: Bool = false) {
        self.text = text
        self.maxLines = maxLines
        self.background = background
        self.border = border
    }

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: Theme.Radius.xs, style: .continuous)
        Text(text).type(T.mono, Theme.fg2)
            .lineLimit(maxLines)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.horizontal, 10).padding(.vertical, 7)
            .background(background ?? (onSoft ? Theme.card : Theme.soft), in: shape)
            .overlay { if border { shape.strokeBorder(Theme.line, lineWidth: 1) } }
    }
}

/// The app's mark, the one on its icon: a chat bubble with a terminal
/// prompt, from the Android launcher's vector
/// (android/app/src/main/res/drawable/ic_launcher_foreground.xml).
struct AppMark: Shape {
    private static let outline: Path = {
        var p = SVGPath.parse("M34,32h40a6,6 0,0 1,6 6v26a6,6 0,0 1,-6 6H48l-10,9v-9h-4a6,6 0,0 1,-6 -6V38a6,6 0,0 1,6 -6z")
        p.addPath(SVGPath.parse("M40,43l8,6 -8,6 -2.5,-3 4.5,-3 -4.5,-3zM52,54h14v4H52z"))
        return p
    }()
    /// What the mark covers of the drawable's 108 canvas.
    private static let box = CGRect(x: 28, y: 32, width: 52, height: 47)

    func path(in rect: CGRect) -> Path {
        let k = min(rect.width / Self.box.width, rect.height / Self.box.height)
        let t = CGAffineTransform(
            translationX: rect.midX - Self.box.width * k / 2, y: rect.midY - Self.box.height * k / 2
        ).scaledBy(x: k, y: k).translatedBy(x: -Self.box.minX, y: -Self.box.minY)
        return Self.outline.applying(t)
    }

    /// The bubble in `color`, the prompt cut out of it.
    static func view(size: CGFloat, color: Color) -> some View {
        AppMark().fill(color, style: FillStyle(eoFill: true))
            .frame(width: size * 52 / 47, height: size)
            .accessibilityHidden(true)
    }
}
