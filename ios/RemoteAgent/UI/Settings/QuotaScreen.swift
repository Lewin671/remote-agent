import RACore
import RAProto
import SwiftUI

/// 账户额度: each CLI's account usage on a computer, apart, as the daemon
/// reads it (architecture 「账户额度」, Android's QuotaScreen). What is not
/// known is said, never shown as 0%; an offline computer's or a failed
/// read's numbers are the last record, and a reset time that passed only
/// waits for an update. Nothing here switches CLI or model.
struct QuotaScreen: View {
    let machine: String
    @Environment(AppModel.self) private var model

    var body: some View {
        let state = model.state
        let m = state.machines[machine]
        let status = m.flatMap { machineStatus($0, state: state, now: nowMs()) }
        SettingsPage {
            if let m {
                Ticking { now in groups(m, state: state, now: now) }
            } else {
                PageNote(L10n.quotaIosThisComputerWasRemoved())
            }
        }
        .navBar(
            L10n.settingsAccountQuota(),
            subtitle: m.map { _ in machineLabel(state, id: machine) + (status.map { " · " + ($0.online ? L10n.commonIosOnline() : L10n.commonIosOffline()) } ?? "") })
    }

    @ViewBuilder private func groups(_ m: MachineView, state: RemoteState, now: Int64) -> some View {
        let groups = quotaGroups(asKnown(m, state: state), now: now)
        // Not connected, the connection bar says so and the numbers are what was last heard.
        if !state.unlinked && !m.online { GroupNote(L10n.quotaIosComputerOfflineShowingTheLastReading(), color: DS.orangeInk) }
        ForEach(Array(groups.enumerated()), id: \.offset) { _, g in
            if !g.subgroups.isEmpty {
                ForEach(Array(g.subgroups.enumerated()), id: \.offset) { index, sub in
                    GroupLabel(sub.title + (g.lastRecord && index == 0 ? L10n.quotaLastReading() : ""))
                    if index == 0 && g.status == .failed {
                        GroupNote(g.detail.isEmpty ? L10n.quotaFailureNoReason(name: agentName(g.agent)) : L10n.quotaFailure(detail: g.detail, name: agentName(g.agent)), color: DS.red)
                    }
                    GroupCard {
                        for row in sub.rows { QuotaWindow(row: row) }
                    }
                }
            } else {
                GroupLabel(g.title + (g.lastRecord ? L10n.quotaLastReading() : ""))
                // Under the label it belongs to: with no last record there is no card, and the failure stands in its place.
                if g.status == .failed {
                    GroupNote(g.detail.isEmpty ? L10n.quotaFailureNoReason(name: agentName(g.agent)) : L10n.quotaFailure(detail: g.detail, name: agentName(g.agent)), color: DS.red)
                }
                if g.status == .unsupported || g.status == .waiting || !g.rows.isEmpty {
                    GroupCard {
                        if g.status == .unsupported {
                            QuotaState(
                                L10n.quotaQuotaLookupIsNotSupportedYet(),
                                g.detail.or(L10n.quotaIosRemoteAgentOnThisComputerCannotReadSQuota(value: String(describing: agentName(g.agent))))
                                    + (agentInfo(g.agent).usageHint ?? ""))
                        } else if g.status == .waiting {
                            QuotaState(L10n.quotaQuotaNotAvailableYet(), g.detail.isEmpty ? L10n.quotaWaiting(name: agentName(g.agent)) : g.detail)
                        }
                        for row in g.rows { QuotaWindow(row: row) }
                    }
                }
            }
            // The warning belongs to this card, before observation metadata.
            let banner = quotaBanner(g)
            if !banner.isEmpty { GroupNote(banner, color: DS.orangeInk) }
            if !g.rows.isEmpty {
                if !g.freshness.isEmpty { GroupNote(g.freshness) }
                if !g.received.isEmpty { GroupNote(g.received) }
            }

        }
        if groups.isEmpty { GroupNote(L10n.quotaIosThisComputerHasNotReportedWhichClisAreInstalled()) }
        GroupNote(L10n.settingsTimesAreShownInBeijingTimeQuotasBelongTo())
    }
}

/// What a group says instead of windows: not supported, or not heard yet.
private struct QuotaState: View {
    private let title: String
    private let text: String

    init(_ title: String, _ text: String) {
        self.title = title
        self.text = text
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text(title).font(.body.weight(.semibold)).ink(DS.ink)
            Text(text).font(.footnote).ink(DS.ink2)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 14).padding(.vertical, 10)
        .accessibilityElement(children: .combine)
    }
}

/// One usage window: its name, what is left, a bar of it, and when it resets.
private struct QuotaWindow: View {
    let row: QuotaRow
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let ink = row.tone == .full || row.tone == .warn ? DS.orangeInk : row.tone == .stale ? DS.ink3 : DS.ink2
        let fill: Color = switch row.tone {
        case .stale: DS.ink3
        case .normal: DS.ink
        default: DS.orange
        }
        let sub = [row.reset, row.note].filter { !$0.isEmpty }.joined(separator: " · ")
        // Side by side, or the amount under the name when large text leaves no room for both.
        let head = typeSize.isAccessibilitySize ? AnyLayout(VStackLayout(alignment: .leading, spacing: 2)) : AnyLayout(HStackLayout(spacing: 8))
        VStack(alignment: .leading, spacing: 0) {
            head {
                Text(row.label).font(.body).ink(DS.ink).frame(maxWidth: .infinity, alignment: .leading)
                Text(row.left).font(.subheadline).ink(ink)
            }
            if let percent = row.percent {
                GeometryReader { geo in
                    Capsule().fill(DS.fill2)
                        .overlay(alignment: .leading) {
                            Capsule().fill(fill).frame(width: geo.size.width * min(1, max(0, (100 - percent) / 100)))
                        }
                }
                .frame(height: 6)
                .padding(.top, 6)
                .accessibilityHidden(true)
            }
            if !sub.isEmpty { Text(sub).font(.footnote).ink(DS.ink2).padding(.top, 8) }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 14).padding(.vertical, 10)
        .accessibilityElement(children: .combine)
    }
}
