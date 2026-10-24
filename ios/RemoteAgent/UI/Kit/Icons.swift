// Every icon of the interface. They all come from Lucide (ISC, the text is
// Resources/Licenses/ISC-Lucide.txt), one per meaning (docs/design-system.md
// §5); nothing else draws an icon, and no font glyph or SF Symbol stands in
// for one. The path data is lucide-react's, the same strings as Android's
// ui/Icons.kt, so every client draws the same shapes: a 24 grid, a line of
// 2 with round caps and joins, nothing filled but the square that
// interrupts.
import SwiftUI
import UIKit

/// A Lucide icon: its name there and its outline on the 24 grid.
struct LucideIcon: Sendable, Equatable {
    let name: String
    let path: Path
    /// Also filled; only the square that interrupts a turn is.
    let filled: Bool

    init(_ name: String, _ paths: String..., filled: Bool = false) {
        self.name = name
        var all = Path()
        for d in paths { all.addPath(SVGPath.parse(d)) }
        path = all
        self.filled = filled
    }

    static func == (a: LucideIcon, b: LucideIcon) -> Bool { a.name == b.name && a.filled == b.filled }

    /// The outline scaled into `rect`, centred.
    func path(in rect: CGRect) -> Path {
        let side = min(rect.width, rect.height)
        let k = side / 24
        return path.applying(
            CGAffineTransform(translationX: rect.midX - side / 2, y: rect.midY - side / 2).scaledBy(x: k, y: k))
    }

    /// A template image, for the places that only take one: menu items, tab items, toolbar buttons.
    func image(size: CGFloat = 22) -> Image {
        let bounds = CGRect(x: 0, y: 0, width: size, height: size)
        let ui = UIGraphicsImageRenderer(bounds: bounds).image { ctx in
            let cg = ctx.cgContext
            cg.addPath(path(in: bounds).cgPath)
            cg.setLineWidth(2 * size / 24)
            cg.setLineCap(.round)
            cg.setLineJoin(.round)
            cg.setStrokeColor(UIColor.black.cgColor)
            cg.setFillColor(UIColor.black.cgColor)
            cg.drawPath(using: filled ? .fillStroke : .stroke)
        }
        return Image(uiImage: ui.withRenderingMode(.alwaysTemplate))
    }
}

enum Lucide {
    /// Goes to another page or sheet.
    static let chevronRight = LucideIcon("chevron-right", "m9 18 6-6-6-6")
    /// Back to the previous page, or up one folder.
    static let chevronLeft = LucideIcon("chevron-left", "m15 18-6-6 6-6")
    /// Opens in place, or the menu right under it.
    static let chevronDown = LucideIcon("chevron-down", "m6 9 6 6 6-6")
    /// Closes what opened in place.
    static let chevronUp = LucideIcon("chevron-up", "m18 15-6-6-6 6")
    /// More actions.
    static let ellipsis = LucideIcon(
        "ellipsis", SVGPath.circle(cx: 12, cy: 12, r: 1), SVGPath.circle(cx: 19, cy: 12, r: 1),
        SVGPath.circle(cx: 5, cy: 12, r: 1))
    /// Adds to the message, or to the end of a list.
    static let plus = LucideIcon("plus", "M5 12h14", "M12 5v14")
    /// A new session.
    static let squarePen = LucideIcon(
        "square-pen",
        "M12 3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7",
        "M18.375 2.625a1 1 0 0 1 3 3l-9.013 9.014a2 2 0 0 1-.853.505l-2.873.84a.5.5 0 0 1-.62-.62l.84-2.873a2 2 0 0 1 .506-.852z")
    /// Sends; the terminal's up key.
    static let arrowUp = LucideIcon("arrow-up", "m5 12 7-7 7 7", "M12 19V5")
    /// Interrupts the turn; the one filled icon. Lucide's rect of 18 at 3,3 with corners of 2.
    static let square = LucideIcon(
        "square", "M5 3h14a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2z", filled: true)
    /// Copies a code block of a reply. Lucide's rect of 14 at 8,8 with corners of 2.
    static let copy = LucideIcon(
        "copy", "M10 8h10a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H10a2 2 0 0 1-2-2V10a2 2 0 0 1 2-2z",
        "M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2")
    /// A task of a reply's task list, open and done: Lucide's square, not filled, and square-check.
    static let box = LucideIcon("square", "M5 3h14a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2z")
    static let boxChecked = LucideIcon(
        "square-check", "M5 3h14a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2z", "m9 12 2 2 4-4")
    /// Down to the newest message; the terminal's down key.
    static let arrowDown = LucideIcon("arrow-down", "M12 5v14", "m19 12-7 7-7-7")
    /// Done; also the chosen one of several.
    static let check = LucideIcon("check", "M20 6 9 17l-5-5")
    /// Failed; also closes and clears.
    static let x = LucideIcon("x", "M18 6 6 18", "m6 6 12 12")
    /// In progress; `Mark` turns it.
    static let loaderCircle = LucideIcon("loader-circle", "M21 12a9 9 0 1 1-6.219-8.56")
    static let clock = LucideIcon(
        "clock", "M12 2a10 10 0 1 0 0 20 10 10 0 0 0 0-20", "M12 6v6l4 2")
    /// Canceled.
    static let minus = LucideIcon("minus", "M5 12h14")
    /// Filters a list.
    static let search = LucideIcon("search", "m21 21-4.34-4.34", SVGPath.circle(cx: 11, cy: 11, r: 8))
    /// The sessions tab.
    static let messageSquare = LucideIcon(
        "message-square",
        "M22 17a2 2 0 0 1-2 2H6.828a2 2 0 0 0-1.414.586l-2.202 2.202A.71.71 0 0 1 2 21.286V5a2 2 0 0 1 2-2h16a2 2 0 0 1 2 2z")
    /// The settings tab.
    static let settings = LucideIcon(
        "settings",
        "M9.671 4.136a2.34 2.34 0 0 1 4.659 0 2.34 2.34 0 0 0 3.319 1.915 2.34 2.34 0 0 1 2.33 4.033 2.34 2.34 0 0 0 0 3.831 2.34 2.34 0 0 1-2.33 4.033 2.34 2.34 0 0 0-3.319 1.915 2.34 2.34 0 0 1-4.659 0 2.34 2.34 0 0 0-3.32-1.915 2.34 2.34 0 0 1-2.33-4.033 2.34 2.34 0 0 0 0-3.831A2.34 2.34 0 0 1 6.35 6.051a2.34 2.34 0 0 0 3.319-1.915",
        SVGPath.circle(cx: 12, cy: 12, r: 3))
    /// No network on this device, and a server out of reach.
    static let wifiOff = LucideIcon(
        "wifi-off",
        "M12 20h.01", "M8.5 16.429a5 5 0 0 1 7 0", "M5 12.859a10 10 0 0 1 5.17-2.69",
        "M19 12.859a10 10 0 0 0-2.007-1.523", "M2 8.82a15 15 0 0 1 4.177-2.643", "M22 8.82a15 15 0 0 0-11.288-3.764",
        "m2 2 20 20")
    static let cloudOff = LucideIcon(
        "cloud-off",
        "m2 2 20 20", "M5.782 5.782A7 7 0 0 0 9 19h8.5a4.5 4.5 0 0 0 1.307-.193",
        "M21.532 16.5A4.5 4.5 0 0 0 17.5 10h-1.79A7.008 7.008 0 0 0 10 5.07")
    /// A computer.
    static let laptop = LucideIcon(
        "laptop",
        "M18 5a2 2 0 0 1 2 2v8.526a2 2 0 0 0 .212.897l1.068 2.127a1 1 0 0 1-.9 1.45H3.62a1 1 0 0 1-.9-1.45l1.068-2.127A2 2 0 0 0 4 15.526V7a2 2 0 0 1 2-2z",
        "M20.054 15.987H3.946")
    /// A client: a browser, a phone, a tablet. Lucide's rects of 14 by 20 at 5,2 and 16 by 20 at 4,2 with corners of 2.
    static let globe = LucideIcon(
        "globe", SVGPath.circle(cx: 12, cy: 12, r: 10), "M12 2a14.5 14.5 0 0 0 0 20a14.5 14.5 0 0 0 0-20", "M2 12h20")
    static let smartphone = LucideIcon(
        "smartphone", "M7 2h10a2 2 0 0 1 2 2v16a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2z", "M12 18h.01")
    static let tablet = LucideIcon(
        "tablet", "M6 2h12a2 2 0 0 1 2 2v16a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2z", "M12 18h.01")
    /// The terminal's remaining arrow keys and its return key.
    static let arrowLeft = LucideIcon("arrow-left", "m12 19-7-7 7-7", "M19 12H5")
    static let arrowRight = LucideIcon("arrow-right", "M5 12h14", "m12 5 7 7-7 7")
    static let cornerDownLeft = LucideIcon("corner-down-left", "M20 4v7a4 4 0 0 1-4 4H4", "m9 10-5 5 5 5")
    /// Shows or hides the phone's keyboard under the terminal. Lucide's rect of 20 by 16 at 2,4 with corners of 2.
    static let keyboard = LucideIcon(
        "keyboard",
        "M10 8h.01", "M12 12h.01", "M14 8h.01", "M16 12h.01", "M18 8h.01", "M6 8h.01", "M7 16h10", "M8 12h.01",
        "M4 4h16a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2z")
    /// Fits the terminal to this screen.
    static let maximize2 = LucideIcon("maximize-2", "M15 3h6v6", "m21 3-7 7", "m3 21 7-7", "M9 21H3v-6")

    /// All of them, for the gallery and the tests.
    static let all: [LucideIcon] = [
        chevronRight, chevronLeft, chevronDown, chevronUp, ellipsis, plus, squarePen, arrowUp, square, copy, box,
        boxChecked, arrowDown, check, x, loaderCircle, minus, search, messageSquare, clock, settings, wifiOff, cloudOff,
        laptop, globe, smartphone, tablet, arrowLeft, arrowRight, cornerDownLeft, keyboard, maximize2,
    ]
}

private struct LucideShape: Shape {
    let icon: LucideIcon
    func path(in rect: CGRect) -> Path { icon.path(in: rect) }
}

/// An icon in the colour of the text it is with, or in `color`.
///
/// One that is a button by itself is 20 to 22 pt, 22 in the tab bar, and
/// says what it does in `label` unless its tap target already does; one
/// beside its words is decoration, has no label and is hidden from
/// VoiceOver. Its size is the one at the default text size and grows with
/// `relativeTo`, as the text next to it does.
struct Ic: View {
    private let icon: LucideIcon
    private let color: Color?
    private let label: String?
    @ScaledMetric private var size: CGFloat

    init(_ icon: LucideIcon, size: CGFloat, color: Color? = nil, relativeTo: Font.TextStyle = .body, label: String? = nil) {
        self.icon = icon
        self.color = color
        self.label = label
        _size = ScaledMetric(wrappedValue: size, relativeTo: relativeTo)
    }

    /// An icon in a line of text: as tall as `style`'s letters.
    init(_ icon: LucideIcon, style: TypeStyle, color: Color? = nil, label: String? = nil) {
        self.init(icon, size: style.size, color: color, relativeTo: style.relativeTo, label: label)
    }

    var body: some View {
        let stroke = StrokeStyle(lineWidth: 2 * size / 24, lineCap: .round, lineJoin: .round)
        ZStack {
            if icon.filled { LucideShape(icon: icon).fill() }
            LucideShape(icon: icon).stroke(style: stroke)
        }
        .frame(width: size, height: size)
        .ink(color)
        .accessibilityElement()
        .accessibilityLabel(label ?? "")
        .accessibilityHidden(label == nil)
    }
}

/// A status mark (design-system §5): check for done, x for failed, minus for
/// canceled, and loader-circle for in progress, which turns once in 0.9 s.
/// It stays still when the system asks for reduced motion.
struct Mark: View {
    private let icon: LucideIcon
    private let color: Color?
    private let size: CGFloat
    private let relativeTo: Font.TextStyle
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    init(_ icon: LucideIcon, color: Color? = nil, size: CGFloat = 12, relativeTo: Font.TextStyle = .caption) {
        self.icon = icon
        self.color = color
        self.size = size
        self.relativeTo = relativeTo
    }

    var body: some View {
        let mark = Ic(icon, size: size, color: color, relativeTo: relativeTo)
        if icon == Lucide.loaderCircle && !reduceMotion {
            TimelineView(.animation) { time in
                let turn = time.date.timeIntervalSinceReferenceDate.truncatingRemainder(dividingBy: 0.9) / 0.9
                mark.rotationEffect(.degrees(turn * 360))
            }
        } else {
            mark
        }
    }
}
