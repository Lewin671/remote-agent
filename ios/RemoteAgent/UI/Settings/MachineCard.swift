// A computer in settings (design-system §6 「电脑卡与额度」): a `gbg` card
// of radius 28 with its name, whether it is online, and for each CLI it
// has one line of quota: 剩 N% of the tightest window, when that resets,
// and a thin bar of what is left, orange at 20 % and below. One that is
// not online is only an outline with when it was last seen, and no quota.
import RACore
import RAProto
import SwiftUI

struct MachineCard: View {
    let machine: PBMachineListCardView
    @Environment(Navigator.self) private var nav
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let id = machine.machineID
        let online = machine.online && machine.member
        let shape = RoundedRectangle(cornerRadius: DS.Radius.card, style: .continuous)
        VStack(alignment: .leading, spacing: 0) {
            Button { nav.open(.machine(id: id)) } label: {
                VStack(alignment: .leading, spacing: 4) {
                    HStack(spacing: 6) {
                        Text(machine.name).font(.title3.bold()).foregroundStyle(DS.ink).lineLimit(2)
                        Image(systemName: "chevron.right").font(.footnote.weight(.semibold)).foregroundStyle(DS.ink3)
                    }
                    if machine.member {
                        let status = machine.statusText.rendered()
                        if !status.isEmpty {
                            HStack(spacing: 6) {
                                StatusMark(online ? .online : .off)
                                Text(status).font(.subheadline).foregroundStyle(DS.ink2)
                            }
                        }
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.bottom, online ? 6 : 0)
                .contentShape(Rectangle())
            }
            .buttonStyle(PressStyle())
            .accessibilityElement(children: .combine)
            if online {
                ForEach(machine.quotaSummaries, id: \.agent.rawValue) { sec in
                    if sec.hasSummaryRow {
                        quota(sec, row: sec.summaryRow, id: id)
                    }
                }
            }
        }
        .padding(.horizontal, 20).padding(.vertical, 18)
        .background(online ? DS.gbg : Color.clear, in: shape)
        .overlay { if !online { shape.strokeBorder(DS.sep, lineWidth: 1) } }
        .padding(.horizontal, DS.Space.l).padding(.bottom, 12)
        .contextMenu {
            Button(L10n.settingsAccountQuota()) { nav.open(.quota(machine: id)) }
            Button(L10n.homeConnectionCheck()) { nav.open(.connectionCheck) }
        }
    }

    private func quota(_ sec: PBQuotaCliSection, row: PBQuotaRowView, id: String) -> some View {
        let low = row.tone == .warn || row.tone == .full
        let stale = row.tone == .stale
        let ink = low ? DS.orangeInk : DS.ink2
        let text = row.remainingText.rendered()
        let reset = [row.resetText.rendered(), row.resetClockText.rendered()].filter { !$0.isEmpty }.joined(separator: " · ")
        let left = row.hasPercent_p ? min(max(Double(row.remainingPercent) / 100.0, 0), 1) : nil
        return Button { nav.open(.quota(machine: id)) } label: {
            VStack(alignment: .leading, spacing: 6) {
                if typeSize.isAccessibilitySize {
                    // At accessibility sizes the name, what is left and when it resets do not fit in a line: one under the other.
                    VStack(alignment: .leading, spacing: 2) {
                        HStack(alignment: .firstTextBaseline, spacing: 0) {
                            AgentMark(sec.agent, style: .subheadline, named: true)
                            Text(sec.agentName).fixedSize(horizontal: false, vertical: true)
                        }
                        .font(.subheadline.weight(.semibold)).foregroundStyle(DS.ink)
                        Text(text).font(.subheadline).foregroundStyle(ink).fixedSize(horizontal: false, vertical: true)
                        if !reset.isEmpty {
                            Text(reset).font(.subheadline).foregroundStyle(DS.ink2).fixedSize(horizontal: false, vertical: true)
                        }
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                } else {
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        HStack(alignment: .firstTextBaseline, spacing: 0) {
                            AgentMark(sec.agent, style: .subheadline, named: true)
                            Text(sec.agentName)
                        }
                        .font(.subheadline.weight(.semibold)).foregroundStyle(DS.ink)
                        Text(text).font(.subheadline).foregroundStyle(ink)
                        if !reset.isEmpty {
                            Text(reset).font(.subheadline).foregroundStyle(DS.ink2)
                                .fixedSize(horizontal: false, vertical: true).layoutPriority(-1)
                        }
                        Spacer(minLength: 0)
                    }
                }
                if let left {
                    GeometryReader { box in
                        Capsule().fill(DS.fill)
                            .overlay(alignment: .leading) {
                                Capsule().fill(stale ? DS.ink3 : low ? DS.orange : DS.barFg)
                                    .frame(width: box.size.width * left)
                            }
                    }
                    .frame(height: 4)
                    .accessibilityHidden(true)
                }
            }
            .padding(.vertical, 8)
            .frame(minHeight: DS.minHit)
            .contentShape(Rectangle())
        }
        .buttonStyle(PressStyle())
        .accessibilityElement(children: .combine)
    }
}
