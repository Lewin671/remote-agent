// A computer in settings (design-system §6 「电脑卡与额度」): a `gbg` card
// of radius 28 with its name, whether it is online, and for each CLI it
// has one line of quota: 剩 N% of the tightest window, when that resets,
// and a thin bar of what is left, orange at 20 % and below. One that is
// not online is only an outline with when it was last seen, and no quota.
import RACore
import RAProto
import SwiftUI

struct MachineCard: View {
    let machine: MachineView
    let state: RemoteState
    let now: Int64
    @Environment(Navigator.self) private var nav
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let id = machine.id
        let known = asKnown(machine, state: state)
        let status = machineStatus(machine, state: state, now: now)
        let online = status?.online == true
        let shape = RoundedRectangle(cornerRadius: DS.Radius.card, style: .continuous)
        VStack(alignment: .leading, spacing: 0) {
            Button { nav.open(.machine(id: id)) } label: {
                VStack(alignment: .leading, spacing: 4) {
                    HStack(spacing: 6) {
                        Text(machineLabel(state, id: id)).font(.title3.bold()).foregroundStyle(DS.ink).lineLimit(2)
                        Image(systemName: "chevron.right").font(.footnote.weight(.semibold)).foregroundStyle(DS.ink3)
                    }
                    if let status {
                        HStack(spacing: 6) {
                            StatusMark(online ? .online : .off)
                            Text(online ? status.text : status.hasLastSeen ? L10n.settingsOnline(value: String(describing: status.text)) : L10n.settingsOffline())
                                .font(.subheadline).foregroundStyle(DS.ink2)
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
                let groups = quotaGroups(known, now: now)
                ForEach(machineAgents(known), id: \.self) { agent in
                    quota(agent, quotaCardLine(groups.filter { $0.agent == agent }), id: id)
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

    private func quota(_ agent: PBAgent, _ line: QuotaCardLine, id: String) -> some View {
        let ink = line.low ? DS.orangeInk : DS.ink2
        return Button { nav.open(.quota(machine: id)) } label: {
            VStack(alignment: .leading, spacing: 6) {
                if typeSize.isAccessibilitySize {
                    // At accessibility sizes the name, what is left and when it resets do not fit in a line: one under the other.
                    VStack(alignment: .leading, spacing: 2) {
                        HStack(alignment: .firstTextBaseline, spacing: 0) {
                            AgentMark(agent, style: .subheadline, named: true)
                            Text(agentName(agent)).fixedSize(horizontal: false, vertical: true)
                        }
                        .font(.subheadline.weight(.semibold)).foregroundStyle(DS.ink)
                        Text(line.text).font(.subheadline).foregroundStyle(ink).fixedSize(horizontal: false, vertical: true)
                        if !line.reset.isEmpty {
                            Text(line.reset).font(.subheadline).foregroundStyle(DS.ink2).fixedSize(horizontal: false, vertical: true)
                        }
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                } else {
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        HStack(alignment: .firstTextBaseline, spacing: 0) {
                            AgentMark(agent, style: .subheadline, named: true)
                            Text(agentName(agent))
                        }
                        .font(.subheadline.weight(.semibold)).foregroundStyle(DS.ink)
                        Text(line.text).font(.subheadline).foregroundStyle(ink)
                        if !line.reset.isEmpty {
                            Text(line.reset).font(.subheadline).foregroundStyle(DS.ink2)
                                .fixedSize(horizontal: false, vertical: true).layoutPriority(-1)
                        }
                        Spacer(minLength: 0)
                    }
                }
                if let left = line.left {
                    GeometryReader { box in
                        Capsule().fill(DS.fill2)
                            .overlay(alignment: .leading) {
                                Capsule().fill(line.stale ? DS.ink3 : line.low ? DS.orange : DS.ink)
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
