// SF Symbols as the design system uses them (design-system §5): an icon
// beside words is decoration and hidden from VoiceOver; one that is a
// button by itself says what it does. Status marks beside a word: a check
// for done, an x for failed, a minus for canceled, the turning ring while
// in progress.
import SwiftUI

/// An SF Symbol `size` points at the default text size, growing with `relativeTo`, or set in `font`.
struct SymbolImage: View {
    private let name: String
    private let color: Color?
    private let label: String?
    private let font: Font?
    @ScaledMetric private var size: CGFloat

    init(_ name: String, size: CGFloat, color: Color? = nil, relativeTo: Font.TextStyle = .body, label: String? = nil) {
        self.name = name
        self.color = color
        self.label = label
        font = nil
        _size = ScaledMetric(wrappedValue: size, relativeTo: relativeTo)
    }

    /// As tall as the letters of `style`.
    init(_ name: String, style: Font, color: Color? = nil, label: String? = nil) {
        self.name = name
        self.color = color
        self.label = label
        font = style
        _size = ScaledMetric(wrappedValue: 17)
    }

    var body: some View {
        Image(systemName: name)
            .font(font ?? .system(size: size * 0.8, weight: .medium))
            .ink(color)
            .accessibilityLabel(label ?? "")
            .accessibilityHidden(label == nil)
    }
}

/// A status mark beside its words; `Mark.progress` turns, still when the system asks for less motion.
struct Mark: View {
    /// In progress: the turning ring.
    static let progress = "progress"
    private let name: String
    private let color: Color?
    @ScaledMetric private var size: CGFloat

    init(_ name: String, color: Color? = nil, size: CGFloat = 12, relativeTo: Font.TextStyle = .caption) {
        self.name = name
        self.color = color
        _size = ScaledMetric(wrappedValue: size, relativeTo: relativeTo)
    }

    var body: some View {
        if name == Self.progress {
            Spinner(color: color ?? DS.ink2).frame(width: size, height: size).accessibilityHidden(true)
        } else {
            SymbolImage(name, size: size / 0.8, color: color).frame(width: size, height: size)
        }
    }
}

/// An icon before its words, as tall as them: 正在连接… after the turning ring.
struct IconText: View {
    private let icon: String
    private let text: String
    private let color: Color
    private let style: Font

    init(_ icon: String, _ text: String, color: Color = DS.ink2, style: Font = .subheadline) {
        self.icon = icon
        self.text = text
        self.color = color
        self.style = style
    }

    var body: some View {
        HStack(spacing: 6) {
            if icon == Mark.progress {
                ProgressView().controlSize(.small).tint(color)
            } else {
                SymbolImage(icon, style: style, color: color)
            }
            Text(text).font(style).foregroundStyle(color)
        }
        .accessibilityElement(children: .combine)
    }
}

extension View {
    /// The foreground colour, or the surrounding one when there is none to set.
    @ViewBuilder func ink(_ color: Color?) -> some View {
        if let color { foregroundStyle(color) } else { self }
    }
}
