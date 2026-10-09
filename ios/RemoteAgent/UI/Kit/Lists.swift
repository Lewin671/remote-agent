// Lists (docs/design-system.md §6): section labels, grouped cards on a grey
// page and their rows, the choices of a sheet, and the notes that stand in
// for a list that is empty, loading or failed.
import SwiftUI

/// A section label: 13 semibold muted, a count or one link at the right.
struct SectionLabel: View {
    private let text: String
    private let count: Int?
    private let action: String?
    private let onAction: (() -> Void)?

    init(_ text: String, count: Int? = nil, action: String? = nil, onAction: (() -> Void)? = nil) {
        self.text = text
        self.count = count
        self.action = action
        self.onAction = onAction
    }

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Text(text).type(T.smB, Theme.muted).accessibilityAddTraits(.isHeader)
            Spacer(minLength: 0)
            // A long label wraps beside them; the count and the link are never squeezed to a character a line.
            if let count { Text("\(count)").type(T.sm, Theme.muted).lineLimit(1).fixedSize() }
            if let action, onAction != nil { ArrowText(action, style: T.smB).lineLimit(1).fixedSize() }
        }
        .padding(.init(top: 14, leading: 16, bottom: 6, trailing: 16))
        // The link's 44 pt target is drawn over the padding, so the row keeps the mockup's height.
        .overlay(alignment: .trailing) {
            if let action, let onAction {
                Button(action: onAction) { Color.clear.frame(width: 88, height: Theme.minHit).contentShape(Rectangle()) }
                    .accessibilityLabel(action)
            }
        }
    }
}

/// A label above a group on a grey page.
struct GroupLabel: View {
    private let text: String
    private let action: String?
    private let onAction: (() -> Void)?

    init(_ text: String, action: String? = nil, onAction: (() -> Void)? = nil) {
        self.text = text
        self.action = action
        self.onAction = onAction
    }

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 8) {
            Text(text).type(T.sm, Theme.muted).accessibilityAddTraits(.isHeader)
            Spacer(minLength: 0)
            if let action, onAction != nil { ArrowText(action, style: T.sm).lineLimit(1).fixedSize() }
        }
        .padding(.init(top: 4, leading: 30, bottom: 6, trailing: 30))
        .overlay(alignment: .trailing) {
            if let action, let onAction {
                Button(action: onAction) { Color.clear.frame(width: 104, height: Theme.minHit).contentShape(Rectangle()) }
                    .accessibilityLabel(action)
            }
        }
    }
}

/// Collects the rows of a `GroupCard` one by one, so that a line can be
/// drawn between each two. Rows may be written out, come from an `if` or a
/// `for`, or be handed over together: an array of views, or a `ForEach`,
/// whose every element is then a row of its own.
@resultBuilder
enum RowsBuilder {
    static func buildExpression<V: View>(_ row: V) -> [AnyView] { [AnyView(row)] }
    static func buildExpression<V: View>(_ rows: [V]) -> [AnyView] { rows.map { AnyView($0) } }
    static func buildExpression(_ rows: [AnyView]) -> [AnyView] { rows }
    static func buildExpression<Data: RandomAccessCollection, ID: Hashable, Row: View>(_ each: ForEach<Data, ID, Row>) -> [AnyView] {
        each.data.map { AnyView(each.content($0)) }
    }
    static func buildBlock(_ rows: [AnyView]...) -> [AnyView] { rows.flatMap { $0 } }
    static func buildOptional(_ rows: [AnyView]?) -> [AnyView] { rows ?? [] }
    static func buildEither(first rows: [AnyView]) -> [AnyView] { rows }
    static func buildEither(second rows: [AnyView]) -> [AnyView] { rows }
    static func buildArray(_ rows: [[AnyView]]) -> [AnyView] { rows.flatMap { $0 } }
}

/// A grouped list on a grey page: card, radius 14, 16 pt in from the sides, a 1 pt line between rows.
struct GroupCard: View {
    private let rows: [AnyView]

    init(@RowsBuilder rows: () -> [AnyView]) { self.rows = rows() }

    var body: some View {
        VStack(spacing: 0) {
            ForEach(rows.indices, id: \.self) { i in
                if i > 0 { Rectangle().fill(Theme.line).frame(height: 1).accessibilityHidden(true) }
                rows[i]
            }
        }
        .background(Theme.card)
        .clipShape(RoundedRectangle(cornerRadius: Theme.Radius.md, style: .continuous))
        .padding(.init(top: 0, leading: 16, bottom: 12, trailing: 16))
    }
}

/// Shares a row's width between its text and its value. Both on one line
/// when they fit. When they do not, the text takes the room: the value keeps
/// what the text leaves, and at least `valueShare` of the row, wrapping or
/// cut inside that. An `HStack` cannot say this: it hands a flexible value
/// its full share before it knows the text needs more, which set a long
/// computer name three lines deep beside a two-character 在线.
private struct RowSplit: Layout {
    var spacing: CGFloat = 10
    var valueShare: CGFloat = 0.4

    /// The widths of the text (the first subview) and the value (the second) in `width`.
    private func widths(_ subviews: Subviews, in width: CGFloat?) -> (text: CGFloat, value: CGFloat) {
        let text = subviews[0].sizeThatFits(.unspecified).width
        let value = subviews[1].sizeThatFits(.unspecified).width
        guard let width, width.isFinite else { return (text, value) }
        let room = max(0, width - spacing)
        if text + value <= room { return (text, value) }
        let kept = min(value, max(room * valueShare, room - text))
        return (room - kept, kept)
    }

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        guard subviews.count == 2 else { return subviews.first?.sizeThatFits(proposal) ?? .zero }
        let w = widths(subviews, in: proposal.width)
        let text = subviews[0].sizeThatFits(ProposedViewSize(width: w.text, height: nil))
        let value = subviews[1].sizeThatFits(ProposedViewSize(width: w.value, height: nil))
        let natural = w.text + spacing + w.value
        let width = proposal.width.flatMap { $0.isFinite ? $0 : nil } ?? natural
        return CGSize(width: width, height: max(text.height, value.height))
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        guard subviews.count == 2 else {
            subviews.first?.place(at: bounds.origin, proposal: ProposedViewSize(bounds.size))
            return
        }
        let w = widths(subviews, in: bounds.width)
        subviews[0].place(
            at: CGPoint(x: bounds.minX, y: bounds.midY), anchor: .leading, proposal: ProposedViewSize(width: w.text, height: nil))
        subviews[1].place(
            at: CGPoint(x: bounds.maxX, y: bounds.midY), anchor: .trailing, proposal: ProposedViewSize(width: w.value, height: nil))
    }
}

/// A row in a group: 15, with a value at the right.
///
/// The text has the row's room first: a long one runs to two lines beside
/// a short value before either is cut, and a long value (a version, a
/// path) keeps at least two fifths of the row.
///
/// A row that goes somewhere has a `chevron`, after its value, or at its end
/// without one; one that does something is blue (`action`) or red
/// (`danger`), its chevron right after the text and an `icon` such as a plus
/// before it. At accessibility text sizes the value moves under the text,
/// to the right, instead of squeezing it (design-system §6).
struct GRow: View {
    private let text: String
    private let sub: String?
    private let value: String?
    private let chevron: Bool
    private let isAction: Bool
    private let danger: Bool
    private let enabled: Bool
    private let valueColor: Color?
    private let icon: LucideIcon?
    private let onTap: (() -> Void)?
    private var lead: AnyView?
    private var valueLead: AnyView?
    private var trailing: AnyView?
    @Environment(\.dynamicTypeSize) private var typeSize

    /// `sub` is a second line under the text, 13 muted: what a client runs on, a computer's quota in short.
    init(
        _ text: String, sub: String? = nil, value: String? = nil, chevron: Bool = false, action: Bool = false, danger: Bool = false,
        enabled: Bool = true, valueColor: Color? = nil, icon: LucideIcon? = nil, onTap: (() -> Void)? = nil
    ) {
        self.text = text
        self.sub = sub
        self.value = value
        self.chevron = chevron
        isAction = action
        self.danger = danger
        self.enabled = enabled
        self.valueColor = valueColor
        self.icon = icon
        self.onTap = onTap
    }

    /// What stands before the text: an avatar, a computer's badge.
    func lead<V: View>(@ViewBuilder _ view: () -> V) -> GRow {
        var row = self
        row.lead = AnyView(view())
        return row
    }

    /// What stands right before the value and moves with it: a status dot, the turning circle of a value being read.
    func valueLead<V: View>(@ViewBuilder _ view: () -> V) -> GRow {
        var row = self
        row.valueLead = AnyView(view())
        return row
    }

    /// What stands at the row's end, after its value: a segmented pill, a check.
    func trailing<V: View>(@ViewBuilder _ view: () -> V) -> GRow {
        var row = self
        row.trailing = AnyView(view())
        return row
    }

    private var ink: Color { danger ? Theme.red : isAction ? Theme.blue : enabled ? Theme.fg : Theme.muted }

    private var title: some View {
        // At accessibility sizes nothing is cut: the row grows instead (design-system §6).
        let lines: Int? = typeSize.isAccessibilitySize ? nil : 2
        return VStack(alignment: .leading, spacing: 1) {
            HStack(spacing: 0) {
                if let icon { Ic(icon, style: T.body, color: ink).padding(.trailing, 6) }
                Text(text).type(T.body, ink).lineLimit(lines)
                if chevron && isAction { Arrow(ink, style: T.body).padding(.leading, 4) }
            }
            if let sub, !sub.isEmpty { Text(sub).type(T.sm, Theme.muted).lineLimit(lines) }
        }
    }

    @ViewBuilder private var row: some View {
        if value != nil, typeSize.isAccessibilitySize {
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 10) {
                    lead
                    title.frame(maxWidth: .infinity, alignment: .leading)
                    trailing
                }
                HStack(spacing: 4) {
                    Spacer(minLength: 0)
                    valueLead.padding(.trailing, 1)
                    Text(value ?? "").type(T.sm, valueColor ?? Theme.muted).multilineTextAlignment(.trailing)
                    if chevron { Arrow(Theme.muted) }
                }
            }
            .padding(.horizontal, 14).padding(.vertical, 8)
        } else {
            HStack(spacing: 10) {
                lead
                if let value {
                    RowSplit {
                        title.padding(.vertical, 11)
                        HStack(spacing: 4) {
                            valueLead.padding(.trailing, 1)
                            Text(value).type(T.sm, valueColor ?? Theme.muted).multilineTextAlignment(.trailing).lineLimit(2)
                            if chevron { Arrow(Theme.muted) }
                        }
                    }
                    .frame(maxWidth: .infinity)
                } else {
                    title.padding(.vertical, 11).frame(maxWidth: .infinity, alignment: .leading)
                }
                trailing
                if chevron && value == nil && !isAction { Arrow(Theme.muted) }
            }
            .padding(.horizontal, 14)
        }
    }

    var body: some View {
        let shown = row.frame(maxWidth: .infinity, minHeight: Theme.minHit, alignment: .leading).opacity(enabled ? 1 : 0.6)
        if let onTap {
            Button(action: onTap) { shown }.buttonStyle(RowPressStyle()).disabled(!enabled)
        } else {
            shown.accessibilityElement(children: .combine)
        }
    }
}

/// A row in a group that turns something on or off: the system's switch, green when on as in the mockup.
struct ToggleRow: View {
    private let title: String
    private let sub: String
    @Binding private var isOn: Bool

    init(_ title: String, sub: String = "", isOn: Binding<Bool>) {
        self.title = title
        self.sub = sub
        _isOn = isOn
    }

    var body: some View {
        Toggle(isOn: $isOn) {
            VStack(alignment: .leading, spacing: 0) {
                Text(title).type(T.body, Theme.fg)
                if !sub.isEmpty { Text(sub).type(T.meta, Theme.muted) }
            }
        }
        .tint(Theme.green)
        .padding(.horizontal, 14).padding(.vertical, 8)
        .frame(minHeight: Theme.minHit)
    }
}

/// A choice in a sheet's list: title, optional description, a check when chosen; `icon` goes before the title.
struct ListItem: View {
    private let title: String
    private let desc: String?
    private let checked: Bool
    private let enabled: Bool
    private let danger: Bool
    private let small: String?
    private let last: Bool
    private let icon: LucideIcon?
    private let onTap: (() -> Void)?
    private var trailing: AnyView?

    /// `small` is a note right after the title; `last` leaves out the line under the row.
    init(
        _ title: String, desc: String? = nil, checked: Bool = false, enabled: Bool = true, danger: Bool = false,
        small: String? = nil, last: Bool = false, icon: LucideIcon? = nil, onTap: (() -> Void)? = nil
    ) {
        self.title = title
        self.desc = desc
        self.checked = checked
        self.enabled = enabled
        self.danger = danger
        self.small = small
        self.last = last
        self.icon = icon
        self.onTap = onTap
    }

    /// What stands at the row's end, before the check.
    func trailing<V: View>(@ViewBuilder _ view: () -> V) -> ListItem {
        var row = self
        row.trailing = AnyView(view())
        return row
    }

    var body: some View {
        let ink = !enabled ? Theme.muted : danger ? Theme.red : Theme.fg
        let row = HStack(spacing: 10) {
            VStack(alignment: .leading, spacing: 0) {
                let name = Text(title).type(desc != nil ? T.bodyB : T.body, ink)
                if let small {
                    // Beside the title when both fit on a line, else under it: a
                    // long model name does not squeeze its note to a column.
                    ViewThatFits(in: .horizontal) {
                        HStack(alignment: .firstTextBaseline, spacing: 4) {
                            if let icon { Ic(icon, style: T.body, color: ink) }
                            name.lineLimit(1)
                            Text(small).type(T.meta, Theme.muted).lineLimit(1)
                        }
                        .fixedSize()
                        VStack(alignment: .leading, spacing: 0) {
                            HStack(alignment: .firstTextBaseline, spacing: 4) {
                                if let icon { Ic(icon, style: T.body, color: ink) }
                                name
                            }
                            Text(small).type(T.meta, Theme.muted)
                        }
                    }
                } else {
                    HStack(alignment: .firstTextBaseline, spacing: 4) {
                        if let icon { Ic(icon, style: T.body, color: ink) }
                        name
                    }
                }
                if let desc { Text(desc).type(T.meta.with(lineHeight: 1.45), Theme.muted) }
            }
            .padding(.vertical, 8)
            Spacer(minLength: 0)
            trailing
            if checked { Ic(Lucide.check, size: 16, color: Theme.blue) }
        }
        .padding(.horizontal, 2)
        .frame(maxWidth: .infinity, minHeight: Theme.minHit, alignment: .leading)
        .bottomLine(last ? .clear : Theme.line)
        Group {
            if let onTap {
                Button(action: onTap) { row }.buttonStyle(RowPressStyle()).disabled(!enabled)
            } else {
                row.accessibilityElement(children: .combine)
            }
        }
        .accessibilityAddTraits(checked ? .isSelected : [])
    }
}

/// A 12 grey caption under a sheet's content; red when it says how something failed.
struct Hint: View {
    private let text: String
    private let color: Color

    init(_ text: String, color: Color = Theme.muted) {
        self.text = text
        self.color = color
    }

    var body: some View {
        Text(text).type(T.meta, color).frame(maxWidth: .infinity, alignment: .leading).padding(.top, 8)
    }
}

/// A field title inside a sheet (模型, 推理强度).
struct FieldLabel: View {
    private let text: String

    init(_ text: String) { self.text = text }

    var body: some View {
        Text(text).type(T.metaB, Theme.muted)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.top, 12).padding(.bottom, 6)
            .accessibilityAddTraits(.isHeader)
    }
}

/// A centred note on a page that has nothing to list, or is still reading.
struct PageNote: View {
    private let text: String

    init(_ text: String) { self.text = text }

    var body: some View {
        Text(text).type(T.sm, Theme.muted).multilineTextAlignment(.center)
            .frame(maxWidth: .infinity).padding(24)
    }
}

/// A page that has nothing to show yet and says what to do about it: a
/// title, a sentence, and at most one main action with a text action under
/// it (the layout of 「这台设备已被移出账户」 in the mockup).
struct EmptyState: View {
    private let title: String
    private let text: String
    private let action: String?
    private let onAction: (() -> Void)?
    private let note: String?
    private let link: String?
    private let onLink: (() -> Void)?

    init(
        _ title: String, text: String, action: String? = nil, onAction: (() -> Void)? = nil, note: String? = nil,
        link: String? = nil, onLink: (() -> Void)? = nil
    ) {
        self.title = title
        self.text = text
        self.action = action
        self.onAction = onAction
        self.note = note
        self.link = link
        self.onLink = onLink
    }

    var body: some View {
        VStack(spacing: 0) {
            Text(title).type(T.h17, Theme.fg).multilineTextAlignment(.center).accessibilityAddTraits(.isHeader)
            Text(text).type(T.sm, Theme.muted).multilineTextAlignment(.center).padding(.top, 8).padding(.bottom, 20)
            if let action, let onAction { Btn(action, kind: .primary, action: onAction) }
            if let note { Text(note).type(T.sm, Theme.muted).multilineTextAlignment(.center).padding(.top, 10).padding(.bottom, 4) }
            if let link, let onLink { TextAction(link, alignment: .center, action: onLink) }
        }
        .padding(.horizontal, 24)
        .frame(maxWidth: 420)
        .frame(maxWidth: .infinity)
    }
}

/// A row that says something is being read: the turning circle and 读取中….
struct LoadingRow: View {
    private let text: String

    init(_ text: String = "读取中…") { self.text = text }

    var body: some View {
        IconText(Lucide.loaderCircle, text)
            .padding(.horizontal, Theme.Space.page).padding(.vertical, 12)
            .frame(maxWidth: .infinity, alignment: .leading)
    }
}

/// A row that says reading failed, with 重试: the error notice, at the page's side margin.
struct ErrorRow: View {
    private let text: String
    private let action: String
    private let onRetry: (() -> Void)?

    init(_ text: String, action: String = "重试", onRetry: (() -> Void)? = nil) {
        self.text = text
        self.action = action
        self.onRetry = onRetry
    }

    var body: some View {
        Banner(text, tone: .error, action: onRetry == nil ? nil : action, onAction: onRetry)
            .padding(.horizontal, Theme.Space.page).padding(.vertical, 4)
    }
}
