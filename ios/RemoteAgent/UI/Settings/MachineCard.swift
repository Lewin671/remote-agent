// Plan B standalone computer card in Settings: R24 on phone (R14 on iPad regular),
// two-line header with status dot + Core meta segments, orange-soft embedded alert strips,
// and full-width 4 pt CLI quota bars.
import RACore
import RAProto
import SwiftUI

struct DisambiguatorTag: View {
    let code: String
    var a11y: String? = nil

    var body: some View {
        if !code.isEmpty {
            let spoken = (a11y?.isEmpty == false) ? a11y! : L10n.settingsMachineDisambiguatorA11Y(value: code)
            Text(code)
                .font(.caption.monospaced())
                .foregroundStyle(DS.ink2)
                .padding(.horizontal, 6)
                .padding(.vertical, 2)
                .background(DS.fill, in: RoundedRectangle(cornerRadius: DS.Radius.inline, style: .continuous))
                .fixedSize()
                .accessibilityLabel(spoken)
        }
    }
}

/// Wrapping horizontal flow layout so status metadata never truncates at accessibility Dynamic Type sizes.
private struct FlowRowLayout: Layout {
    var spacing: CGFloat = 6
    var lineSpacing: CGFloat = 2

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let maxWidth = proposal.width ?? .infinity
        var x: CGFloat = 0
        var y: CGFloat = 0
        var rowHeight: CGFloat = 0
        var usedWidth: CGFloat = 0
        for sub in subviews {
            let size = sub.sizeThatFits(.unspecified)
            if x > 0 && x + spacing + size.width > maxWidth {
                usedWidth = max(usedWidth, x)
                x = 0
                y += rowHeight + lineSpacing
                rowHeight = 0
            }
            if x > 0 { x += spacing }
            x += size.width
            rowHeight = max(rowHeight, size.height)
        }
        return CGSize(width: max(usedWidth, x), height: y + rowHeight)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var x = bounds.minX
        var y = bounds.minY
        var row: [(LayoutSubview, CGSize)] = []
        var rowHeight: CGFloat = 0

        func flush() {
            var curX = bounds.minX
            for (sub, size) in row {
                let midY = y + (rowHeight - size.height) / 2
                sub.place(at: CGPoint(x: curX, y: midY), proposal: ProposedViewSize(size))
                curX += size.width + spacing
            }
            row.removeAll(keepingCapacity: true)
            y += rowHeight + lineSpacing
            rowHeight = 0
            x = bounds.minX
        }

        for sub in subviews {
            let size = sub.sizeThatFits(.unspecified)
            if !row.isEmpty && x + size.width > bounds.maxX {
                flush()
            }
            row.append((sub, size))
            x += size.width + spacing
            rowHeight = max(rowHeight, size.height)
        }
        if !row.isEmpty { flush() }
    }
}

struct MachineCard: View {
    let machine: PBMachineListCardView
    @Environment(Navigator.self) private var nav
    @Environment(\.horizontalSizeClass) private var sizeClass
    @Environment(\.dynamicTypeSize) private var typeSize
    @ScaledMetric(relativeTo: .footnote) private var phoneCliNameSize: CGFloat = 14
    @ScaledMetric(relativeTo: .footnote) private var wideCliNameSize: CGFloat = 13

    var body: some View {
        let id = machine.machineID
        let wide = sizeClass == .regular
        let isRemoved = machine.appearance == .removed
        let isDimmed = isRemoved || machine.appearance == .offline || machine.dimmed
        let radius: CGFloat = wide ? 14 : DS.Radius.card
        let useOutline = !wide && isDimmed

        Button {
            nav.open(.machine(id: id))
        } label: {
            VStack(alignment: .leading, spacing: 0) {
                headerRow(wide: wide, isDimmed: isDimmed)
                metaRow()
                alertsView(wide: wide)
                quotasView(wide: wide)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            .padding(.horizontal, wide ? 16 : 18)
            .padding(.vertical, (wide || isDimmed) ? 14 : 16)
            .background {
                if !useOutline {
                    RoundedRectangle(cornerRadius: radius, style: .continuous)
                        .fill(DS.gbg)
                }
            }
            .overlay {
                if useOutline {
                    RoundedRectangle(cornerRadius: radius, style: .continuous)
                        .strokeBorder(DS.sep, lineWidth: 1)
                }
            }
            .contentShape(RoundedRectangle(cornerRadius: radius, style: .continuous))
        }
        .buttonStyle(.plain)
        .contextMenu {
            Button(L10n.settingsAccountQuota()) { nav.open(.quota(machine: id)) }
            Button(L10n.homeConnectionCheck()) { nav.open(.connectionCheck) }
        }
    }

    @ViewBuilder private func headerRow(wide: Bool, isDimmed: Bool) -> some View {
        let nameFont: Font = switch (wide, isDimmed) {
        case (true, true): .subheadline.weight(.semibold)
        case (true, false): .headline
        case (false, true): .headline
        case (false, false): .title3.bold()
        }
        HStack(spacing: 8) {
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Text(machine.name.or(machine.machineID))
                    .font(nameFont)
                    .foregroundStyle(isDimmed ? DS.ink2 : DS.ink)
                    .lineLimit(typeSize.isAccessibilitySize ? 2 : 1)
                    .truncationMode(.tail)
                if !machine.disambiguator.isEmpty {
                    DisambiguatorTag(
                        code: machine.disambiguator,
                        a11y: machine.hasDisambiguatorA11Y ? machine.disambiguatorA11Y.rendered() : nil
                    )
                }
            }
            Spacer(minLength: 4)
            SymbolImage("chevron.right", size: 15, color: DS.ink3)
        }
    }

    @ViewBuilder private func metaRow() -> some View {
        let hasDot = machine.statusDot != .unspecified
        let segments: [(PBMachineCardMetaIcon, String)] = machine.metaSegments.compactMap { seg in
            let txt = seg.text.rendered()
            return txt.isEmpty ? nil : (seg.icon, txt)
        }
        if hasDot || !segments.isEmpty {
            FlowRowLayout(spacing: 6, lineSpacing: 2) {
                if hasDot {
                    machineStatusDotView(machine.statusDot, size: 8)
                }
                ForEach(Array(segments.enumerated()), id: \.offset) { idx, seg in
                    if idx > 0 && !machine.metaSeparator.isEmpty {
                        Text(machine.metaSeparator)
                            .font(.footnote)
                            .foregroundStyle(DS.ink2)
                            .accessibilityHidden(true)
                    }
                    if seg.0 == .direct {
                        HStack(spacing: 3) {
                            SymbolImage("bolt.horizontal", size: 12, color: DS.ink2, relativeTo: .footnote)
                            Text(seg.1)
                                .font(.footnote)
                                .foregroundStyle(DS.ink2)
                                .lineLimit(typeSize.isAccessibilitySize ? nil : 1)
                        }
                    } else {
                        Text(seg.1)
                            .font(.footnote)
                            .foregroundStyle(DS.ink2)
                            .lineLimit(typeSize.isAccessibilitySize ? nil : 1)
                    }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.top, 4)
        }
    }

    @ViewBuilder private func alertsView(wide: Bool) -> some View {
        ForEach(Array(machine.alerts.enumerated()), id: \.offset) { _, alert in
            let text = alert.text.rendered()
            if !text.isEmpty {
                HStack(spacing: 6) {
                    if alert.kind == .diskLow {
                        SymbolImage("internaldrive", size: 14, color: DS.orangeInk, relativeTo: .footnote)
                    }
                    Text(text)
                        .font(.footnote.weight(.semibold))
                        .foregroundStyle(DS.orangeInk)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .padding(.horizontal, wide ? 10 : 12)
                .padding(.vertical, wide ? 7 : 8)
                .background(
                    DS.orangeSoft,
                    in: RoundedRectangle(cornerRadius: wide ? 8 : DS.Radius.notice, style: .continuous)
                )
                .padding(.top, 12)
            }
        }
    }

    @ViewBuilder private func quotasView(wide: Bool) -> some View {
        if !machine.quotaItems.isEmpty {
            let cliSize = wide ? wideCliNameSize : phoneCliNameSize
            VStack(alignment: .leading, spacing: 11) {
                ForEach(machine.quotaItems, id: \.agent.rawValue) { item in
                    let pct = max(0, min(100, Double(item.remainingPercent))) / 100.0
                    VStack(alignment: .leading, spacing: 6) {
                        HStack(alignment: .firstTextBaseline, spacing: 8) {
                            HStack(alignment: .firstTextBaseline, spacing: 0) {
                                AgentMark(item.agent, style: .footnote, scale: 1.08, color: DS.ink, named: true)
                                Text(item.agentName)
                                    .font(.system(size: cliSize, weight: .medium))
                                    .foregroundStyle(DS.ink)
                                    .lineLimit(typeSize.isAccessibilitySize ? 2 : 1)
                            }
                            Spacer(minLength: 8)
                            Text(item.valueText.rendered())
                                .font(.footnote.weight(item.warn ? .semibold : .regular))
                                .monospacedDigit()
                                .foregroundStyle(item.warn ? DS.orangeInk : DS.ink2)
                                .lineLimit(1)
                        }
                        GeometryReader { geo in
                            ZStack(alignment: .leading) {
                                Capsule().fill(DS.fill2)
                                if pct > 0 {
                                    Capsule()
                                        .fill(item.warn ? DS.orange : DS.barFg)
                                        .frame(width: geo.size.width * pct)
                                }
                            }
                        }
                        .frame(height: 4)
                    }
                }
            }
            .padding(.top, 14)
        }
    }
}
