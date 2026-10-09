// The four Lucide icons a reply needs (design-system §5), as their paths on
// the 24 grid: line width 2, round caps and joins, the color of the text
// beside them. Lucide is ISC licensed; the app ships the license text.
import SwiftUI

enum Icon {
    /// `square`: a task not done.
    case box
    /// `square-check`: a task done.
    case boxChecked
    case copy, check, x, chevronDown, chevronUp

    fileprivate func path() -> Path {
        var p = Path()
        func box() { p.addRoundedRect(in: CGRect(x: 3, y: 3, width: 18, height: 18), cornerSize: CGSize(width: 2, height: 2)) }
        func line(_ points: (CGFloat, CGFloat)...) {
            p.move(to: CGPoint(x: points[0].0, y: points[0].1))
            for q in points.dropFirst() { p.addLine(to: CGPoint(x: q.0, y: q.1)) }
        }
        switch self {
        case .box: box()
        case .boxChecked:
            box()
            line((9, 12), (11, 14), (15, 10))
        case .copy:
            p.addRoundedRect(in: CGRect(x: 8, y: 8, width: 14, height: 14), cornerSize: CGSize(width: 2, height: 2))
            // M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2
            p.move(to: CGPoint(x: 4, y: 16))
            p.addCurve(to: CGPoint(x: 2, y: 14), control1: CGPoint(x: 2.9, y: 16), control2: CGPoint(x: 2, y: 15.1))
            p.addLine(to: CGPoint(x: 2, y: 4))
            p.addCurve(to: CGPoint(x: 4, y: 2), control1: CGPoint(x: 2, y: 2.9), control2: CGPoint(x: 2.9, y: 2))
            p.addLine(to: CGPoint(x: 14, y: 2))
            p.addCurve(to: CGPoint(x: 16, y: 4), control1: CGPoint(x: 15.1, y: 2), control2: CGPoint(x: 16, y: 2.9))
        case .check: line((20, 6), (9, 17), (4, 12))
        case .x:
            line((18, 6), (6, 18))
            line((6, 6), (18, 18))
        case .chevronDown: line((6, 9), (12, 15), (18, 9))
        case .chevronUp: line((18, 15), (12, 9), (6, 15))
        }
        return p
    }
}

/// An icon `size` points square. Without a `label` it says nothing to VoiceOver: what it means is said by its button or its row.
struct IconView: View {
    let icon: Icon
    let size: CGFloat
    let color: Color
    var label: String?

    var body: some View {
        let scale = size / 24
        icon.path()
            .applying(CGAffineTransform(scaleX: scale, y: scale))
            .stroke(color, style: StrokeStyle(lineWidth: 2 * scale, lineCap: .round, lineJoin: .round))
            .frame(width: size, height: size)
            .accessibilityElement()
            .accessibilityLabel(label ?? "")
            .accessibilityAddTraits(.isImage)
            .accessibilityHidden(label == nil)
    }
}
