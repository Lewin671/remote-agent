// Glass (design-system §4 「材质」): small pieces that float over the
// content, never a whole bar or screen. From iOS 26 they are the system's
// Liquid Glass, which takes care of Reduce Transparency, Increase Contrast
// and its own motion. Before it they are a system material with the
// design's thin line and shadow, opaque (`gbg`) when the system asks for
// less transparency.
//
// The edges of a page fade as content slides under what floats there: from
// iOS 26 that is the system's scroll edge effect under a `safeAreaBar`,
// before it a gradient of the page's colour.
//
// `-legacyGlass` (debug builds) draws the pre-26 look on a newer system, so
// that it can be looked at on the simulators there are.
import SwiftUI

enum Glass {
    /// Whether the system's Liquid Glass is used.
    static var system: Bool {
        #if DEBUG
        if ProcessInfo.processInfo.arguments.contains("-legacyGlass") { return false }
        #endif
        if #available(iOS 26, *) { return true }
        return false
    }
}

private struct GlassSurface<S: InsettableShape>: ViewModifier {
    let shape: S
    let interactive: Bool
    @Environment(\.accessibilityReduceTransparency) private var opaque
    @Environment(\.colorScheme) private var scheme

    func body(content: Content) -> some View {
        if #available(iOS 26, *), Glass.system {
            content.glassEffect(interactive ? .regular.interactive() : .regular, in: shape)
        } else {
            content
                .background {
                    if opaque { shape.fill(DS.gbg) } else { shape.fill(.regularMaterial) }
                }
                .overlay { shape.strokeBorder(scheme == .dark ? Color.white.opacity(0.13) : Color.black.opacity(0.07), lineWidth: 0.5) }
                .compositingGroup()
                .shadow(color: .black.opacity(scheme == .dark ? 0.5 : 0.12), radius: 12, y: 6)
        }
    }
}

extension View {
    /// Puts the view on a small piece of glass of `shape`. `interactive`: it answers a touch, as a button.
    func glass<S: InsettableShape>(_ shape: S, interactive: Bool = true) -> some View {
        modifier(GlassSurface(shape: shape, interactive: interactive))
    }

    /// Floats `bar` over the top or bottom edge of the page, with the content sliding under it and fading out there.
    func floatingBar<Bar: View>(_ edge: VerticalEdge, @ViewBuilder bar: () -> Bar) -> some View {
        modifier(FloatingBar(edge: edge, bar: bar()))
    }
}

private struct FloatingBar<Bar: View>: ViewModifier {
    let edge: VerticalEdge
    let bar: Bar

    func body(content: Content) -> some View {
        if #available(iOS 26, *), Glass.system {
            content.safeAreaBar(edge: edge, spacing: 0) { bar }
        } else {
            content.safeAreaInset(edge: edge, spacing: 0) {
                bar.background { EdgeFade(edge: edge) }
            }
        }
    }
}

/// The page's colour, solid at the screen's edge and gone a little past what floats there.
private struct EdgeFade: View {
    let edge: VerticalEdge

    var body: some View {
        let top = edge == .top
        LinearGradient(
            stops: [.init(color: DS.paper, location: 0), .init(color: DS.paper.opacity(0.85), location: 0.55), .init(color: DS.paper.opacity(0), location: 1)],
            startPoint: top ? .top : .bottom, endPoint: top ? .bottom : .top
        )
        .padding(top ? .bottom : .top, -20)
        .ignoresSafeArea(edges: top ? .top : .bottom)
        .allowsHitTesting(false)
        .accessibilityHidden(true)
    }
}

/// Glass pieces drawn side by side, which from iOS 26 the system lets merge and morph as one.
struct GlassGroup<Content: View>: View {
    private let spacing: CGFloat
    private let content: Content

    init(spacing: CGFloat = 8, @ViewBuilder content: () -> Content) {
        self.spacing = spacing
        self.content = content()
    }

    var body: some View {
        if #available(iOS 26, *), Glass.system {
            GlassEffectContainer(spacing: spacing) { content }
        } else {
            content
        }
    }
}

/// A round glass button of 44 with an SF Symbol: back, ⋯, the gear (design-system §4). Its label is what VoiceOver says.
struct GlassIconButton: View {
    private let symbol: String
    private let label: String
    private let action: () -> Void
    @ScaledMetric(relativeTo: .title3) private var side: CGFloat = DS.minHit

    init(_ symbol: String, label: String, action: @escaping () -> Void) {
        self.symbol = symbol
        self.label = label
        self.action = action
    }

    var body: some View {
        Button(action: action) {
            Image(systemName: symbol)
                .font(.title3)
                .foregroundStyle(DS.ink)
                .frame(width: side, height: side)
                .contentShape(Circle())
        }
        .buttonStyle(GlassPressStyle())
        .glass(Circle())
        .accessibilityLabel(label)
    }
}

/// How a piece of glass answers a touch before iOS 26 (Liquid Glass answers by itself): it dims a little.
struct GlassPressStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label.opacity(configuration.isPressed && !Glass.system ? 0.6 : 1)
    }
}
