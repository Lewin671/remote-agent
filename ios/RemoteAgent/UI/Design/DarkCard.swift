// The dark card (design-system §6 「终端卡」): what waits for you to allow
// it, dark in both themes. Its parts are here so that every place that
// shows an approval (the home page now, the session's dock and the wide
// layout later) draws the same card: the shell, the head, the command
// block and the card's buttons. What a card says and does is its user's.
import SwiftUI

/// The dark card's shell: `tc`, a radius of 30, a thin line in the dark
/// theme. Inside it everything is drawn as in the dark theme, so system
/// controls and the design's colours read on it. `floating`: docked over
/// the page, with the shadow of what floats.
struct DarkCard<Content: View>: View {
    private let floating: Bool
    private let content: Content

    init(floating: Bool = false, @ViewBuilder content: () -> Content) {
        self.floating = floating
        self.content = content()
    }

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: DS.Radius.darkCard, style: .continuous)
        VStack(alignment: .leading, spacing: 0) { content }
            .padding(18)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(DS.tc, in: shape)
            .overlay { shape.strokeBorder(DS.tcLine, lineWidth: 1) }
            .environment(\.colorScheme, .dark)
            .modifier(Floating(on: floating))
    }
}

private struct Floating: ViewModifier {
    let on: Bool

    func body(content: Content) -> some View {
        if on { content.floatShadow() } else { content }
    }
}

/// The card's head: the orange dot and the type word, and at the right the
/// time left, orange in its last minute (`hot`). The two go one under the
/// other when they do not fit side by side.
struct DarkCardHead: View {
    let kind: String
    var right = ""
    var hot = false

    var body: some View {
        let type = HStack(spacing: 6) {
            StatusMark(.waiting)
            Text(kind).fontWeight(.semibold).foregroundStyle(DS.orange).lineLimit(1)
        }
        let time = Text(right).foregroundStyle(hot ? DS.orange : DS.ink2)
        ViewThatFits(in: .horizontal) {
            HStack(spacing: 8) {
                type
                Spacer(minLength: 0)
                time.lineLimit(1)
            }
            VStack(alignment: .leading, spacing: 2) {
                type
                time
            }
        }
        .font(.footnote)
        .accessibilityElement(children: .combine)
    }
}

/// What would run: pure black, a radius of 16, mono 13, an orange ❯ before
/// the first line; it breaks anywhere and never widens the card. `lines`
/// holds it to that many whole lines as laid out; `total` is told how many
/// lines it takes and `more` how many of them are past `lines`, both
/// counted on the screen: one long command is one line to count and
/// several to draw. Both stay 0 until it is laid out.
struct CommandBlock: View {
    private let text: String
    private let lines: Int?
    @Binding private var more: Int
    @Binding private var total: Int
    @State private var shown: CGFloat = 0
    @State private var whole: CGFloat = 0
    @State private var one: CGFloat = 0
    @State private var two: CGFloat = 0

    init(_ text: String, lines: Int? = nil, more: Binding<Int> = .constant(0), total: Binding<Int> = .constant(0)) {
        self.text = text
        self.lines = lines
        _more = more
        _total = total
    }

    var body: some View {
        // Held by height to whole lines, never cut in the middle of one with an ellipsis.
        let held = lines.flatMap { n in one > 0 && two > one ? one + (two - one) * CGFloat(n - 1) : nil }
        line(text)
            .fixedSize(horizontal: false, vertical: true)
            .frame(maxHeight: held, alignment: .top)
            .clipped()
            .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { shown = $0; count() }
            .background {
                // The whole text, one line and two, at the same width: measured, not drawn.
                ZStack {
                    line(text).fixedSize(horizontal: false, vertical: true)
                        .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { whole = $0; count() }
                    line("x").fixedSize(horizontal: false, vertical: true)
                        .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { one = $0; count() }
                    line("x\nx").fixedSize(horizontal: false, vertical: true)
                        .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { two = $0; count() }
                }
                .hidden()
                .accessibilityHidden(true)
            }
            .padding(.horizontal, 14).padding(.vertical, 12)
            .background(Color.black, in: RoundedRectangle(cornerRadius: DS.Radius.command, style: .continuous))
    }

    private func count() {
        guard one > 0, two > one, whole > 0 else { return }
        // One line is past none of them.
        let all = max(1, linesPast(0, whole: whole, one: one, two: two))
        let past = whole > shown + 0.5 ? max(0, all - (lines ?? all)) : 0
        if all != total { total = all }
        if past != more { more = past }
    }

    private func line(_ text: String) -> some View {
        Text("\(Text("❯ ").foregroundStyle(DS.orange))\(Text(text).foregroundStyle(Color(white: 0.91)))")
            .font(.footnote.monospaced())
            .frame(maxWidth: .infinity, alignment: .leading)
    }
}

/// How many lines of a block `whole` tall lie past the first `lines`, where
/// the same block is `one` tall with one line and `two` with two.
func linesPast(_ lines: Int, whole: CGFloat, one: CGFloat, two: CGFloat) -> Int {
    let step = two - one
    guard step > 0, whole > one else { return 0 }
    return max(0, Int(((whole - one) / step).rounded()) + 1 - lines)
}

/// The card's buttons (design-system §6 「深色卡里的按钮」): the main one
/// white with black text, the others translucent white with white text.
/// Capsules 48 tall; disabled, 40 %.
struct DarkCardButtonStyle: ButtonStyle {
    var primary = false
    @Environment(\.isEnabled) private var enabled

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.headline)
            .multilineTextAlignment(.center)
            .foregroundStyle(primary ? Color.black : Color.white)
            .padding(.horizontal, 14).padding(.vertical, 6)
            .frame(maxWidth: .infinity, minHeight: 48)
            .background(primary ? Color.white : Color.white.opacity(0.14), in: Capsule())
            .contentShape(Capsule())
            .opacity(enabled ? (configuration.isPressed ? 0.7 : 1) : 0.4)
    }
}

extension ButtonStyle where Self == DarkCardButtonStyle {
    /// A button on the dark card; `primary` for 允许, 批准, 提交.
    static func darkCard(primary: Bool = false) -> DarkCardButtonStyle { DarkCardButtonStyle(primary: primary) }
}
