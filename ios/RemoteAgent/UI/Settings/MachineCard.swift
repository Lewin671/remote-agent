// Plan B standalone computer card in Settings: R24 on phone (R14 on iPad regular),
// two-line header with status dot + calm ink2 status + optional direct marker or
// secondary note, orange-soft embedded alert strips, and full-width 4 pt CLI quota bars.
import RACore
import RAProto
import SwiftUI

struct DisambiguatorTag: View {
    let code: String

    var body: some View {
        if !code.isEmpty {
            Text(code)
                .font(.caption.monospaced())
                .foregroundStyle(DS.ink2)
                .padding(.horizontal, 6)
                .padding(.vertical, 2)
                .background(DS.fill, in: RoundedRectangle(cornerRadius: DS.Radius.inline, style: .continuous))
                .fixedSize()
        }
    }
}

struct MachineCard: View {
    let machine: PBMachineListCardView
    @Environment(Navigator.self) private var nav
    @Environment(\.horizontalSizeClass) private var sizeClass
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let id = machine.machineID
        let wide = sizeClass == .regular
        let isRemoved = !machine.member
        let isDimmed = machine.dimmed || isRemoved
        let radius: CGFloat = wide ? 14 : DS.Radius.card
        let useOutline = !wide && isDimmed
        let statusText = machine.statusText.rendered()
        let secondaryNote = machine.secondaryNote.rendered()

        Button {
            nav.open(.machine(id: id))
        } label: {
            VStack(alignment: .leading, spacing: 0) {
                headerRow(wide: wide, isDimmed: isDimmed)
                metaRow(statusText: statusText, secondaryNote: secondaryNote)
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
            .opacity(isRemoved ? 0.82 : 1)
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
        case (true, true): .system(size: 15, weight: .semibold)
        case (true, false): .system(size: 17, weight: .semibold)
        case (false, true): .system(size: 17, weight: .semibold)
        case (false, false): .system(size: 20, weight: .bold)
        }
        HStack(spacing: 8) {
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Text(machine.name.or(machine.machineID))
                    .font(nameFont)
                    .foregroundStyle(isDimmed ? DS.ink2 : DS.ink)
                    .lineLimit(typeSize.isAccessibilitySize ? 2 : 1)
                    .truncationMode(.tail)
                if !machine.disambiguator.isEmpty {
                    DisambiguatorTag(code: machine.disambiguator)
                }
            }
            Spacer(minLength: 4)
            SymbolImage("chevron.right", size: 15, color: DS.ink3)
        }
    }

    @ViewBuilder private func metaRow(statusText: String, secondaryNote: String) -> some View {
        let hasDot = machine.statusDot != .unspecified
        if hasDot || !statusText.isEmpty || machine.directActive || !secondaryNote.isEmpty {
            HStack(spacing: 6) {
                if hasDot {
                    machineStatusDotView(machine.statusDot, size: 8)
                }
                if !statusText.isEmpty {
                    Text(statusText)
                        .font(.footnote)
                        .foregroundStyle(DS.ink2)
                        .lineLimit(1)
                }
                if machine.directActive {
                    Text("·")
                        .font(.footnote)
                        .foregroundStyle(DS.ink2)
                        .accessibilityHidden(true)
                    HStack(spacing: 3) {
                        SymbolImage("bolt.horizontal", size: 12, color: DS.ink2, relativeTo: .footnote)
                        Text(L10n.transportDirect())
                            .font(.footnote)
                            .foregroundStyle(DS.ink2)
                            .lineLimit(1)
                    }
                }
                if !secondaryNote.isEmpty {
                    if !statusText.isEmpty {
                        Text("·")
                            .font(.footnote)
                            .foregroundStyle(DS.ink2)
                            .accessibilityHidden(true)
                    }
                    Text(secondaryNote)
                        .font(.footnote)
                        .foregroundStyle(DS.ink2)
                        .lineLimit(typeSize.isAccessibilitySize ? nil : 1)
                }
            }
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
            VStack(alignment: .leading, spacing: 11) {
                ForEach(machine.quotaItems, id: \.agent.rawValue) { item in
                    let pct = max(0, min(100, Double(item.remainingPercent))) / 100.0
                    VStack(alignment: .leading, spacing: 6) {
                        HStack(alignment: .firstTextBaseline, spacing: 8) {
                            HStack(alignment: .firstTextBaseline, spacing: 0) {
                                AgentMark(item.agent, style: .footnote, scale: 1.08, color: DS.ink, named: true)
                                Text(item.agentName)
                                    .font(.system(size: wide ? 13 : 14, weight: .medium))
                                    .foregroundStyle(DS.ink)
                                    .lineLimit(1)
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
