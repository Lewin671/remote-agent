// Motion (design-system §4 动效): three durations and one curve, used wherever something comes, goes or
// changes size; nothing else is animated.
//
// - `fast` 120 ms: press feedback, colour and icon changes, a tool row or a line fading in
// - `base` 200 ms: an element appearing or going, a height change
// - `slow` 280 ms: a whole screen coming or going (sheet, page); those are the system's here
//
// Coming and changing use the deceleration curve cubic-bezier(.2, .8, .2, 1); going takes 0.8 of the
// same duration and ease-in, so it leaves quicker than it came. No bounce, no overshoot, nothing moves
// more than 12 points, and nothing scales below 0.96. With 减少动态效果 every move, scale and height
// change is dropped and only fades of at most `fast` are left.
import SwiftUI
import UIKit

enum Motion {
    static let fast = 0.12
    static let base = 0.20
    static let slow = 0.28

    /// How far a new bubble or 回到最新 rises while it fades in, and the dock.
    static let bubbleRise: CGFloat = 8
    static let dockRise: CGFloat = 12
    /// The smallest scale a pressed button shrinks to.
    static let pressScale: CGFloat = 0.96

    /// 减少动态效果 is on.
    static var reduced: Bool { UIAccessibility.isReduceMotionEnabled }

    /// Coming or changing: the one curve. With motion reduced, a fade of at most `fast`.
    static func enter(_ duration: Double = base) -> Animation {
        reduced ? .easeOut(duration: min(duration, fast)) : .timingCurve(0.2, 0.8, 0.2, 1, duration: duration)
    }

    /// Going: 0.8 of the duration, ease-in.
    static func exit(_ duration: Double = base) -> Animation {
        let d = duration * 0.8
        return reduced ? .easeIn(duration: min(d, fast)) : .timingCurve(0.42, 0, 1, 1, duration: d)
    }

    /// Something coming: it fades in and, unless reduced, rises `rise` points.
    static func appear(rise: CGFloat = 0) -> AnyTransition {
        reduced || rise == 0 ? .opacity : .opacity.combined(with: .offset(y: rise))
    }

    /// Something going: it fades out; a sliding one goes the other way, `rise` points down.
    static func vanish(drop: CGFloat = 0) -> AnyTransition {
        reduced || drop == 0 ? .opacity : .opacity.combined(with: .offset(y: drop))
    }

    /// The same curve for UIKit: coming and changing.
    static func curve(_ duration: Double = base) -> UIViewPropertyAnimator {
        UIViewPropertyAnimator(duration: duration, controlPoint1: CGPoint(x: 0.2, y: 0.8), controlPoint2: CGPoint(x: 0.2, y: 1))
    }
}

/// Lays out its one child at a fraction of its height, from the top: a view that opens or closes its room as
/// it comes or goes, so that what shares the screen with it (the chat above a dock) gives up the room by
/// degrees, not at once. Animatable, so a transition can drive it.
private struct Squeeze: Layout {
    var fraction: CGFloat
    var animatableData: CGFloat {
        get { fraction }
        set { fraction = newValue }
    }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let size = subviews.first?.sizeThatFits(proposal) ?? .zero
        return CGSize(width: size.width, height: size.height * fraction)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        subviews.first?.place(at: CGPoint(x: bounds.minX, y: bounds.minY), anchor: .topLeading, proposal: ProposedViewSize(width: bounds.width, height: nil))
    }
}

private struct SqueezeModifier: ViewModifier, @preconcurrency Animatable {
    var fraction: CGFloat
    var animatableData: CGFloat {
        get { fraction }
        set { fraction = newValue }
    }

    func body(content: Content) -> some View {
        // Clipped only while it opens or closes: at rest the card's shadow is its own to draw.
        if fraction < 1 {
            Squeeze(fraction: fraction) { content }.clipped()
        } else {
            Squeeze(fraction: fraction) { content }
        }
    }
}

extension Motion {
    /// A view that comes with its room opening, rising `rise` points and fading in; it goes with its room closing.
    static func roomy(rise: CGFloat = 0) -> AnyTransition {
        if reduced { return .opacity }
        return .asymmetric(
            insertion: .modifier(active: SqueezeModifier(fraction: 0), identity: SqueezeModifier(fraction: 1)).combined(with: appear(rise: rise)),
            removal: .modifier(active: SqueezeModifier(fraction: 0), identity: SqueezeModifier(fraction: 1)).combined(with: .opacity))
    }
}

/// A button that shrinks to 0.96 while pressed and comes back (120 ms); nothing else changes.
struct PressScale: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .scaleEffect(configuration.isPressed && !Motion.reduced ? Motion.pressScale : 1)
            .animation(Motion.enter(Motion.fast), value: configuration.isPressed)
    }
}
