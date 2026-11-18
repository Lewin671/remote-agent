import RACore
import RAProto
import SwiftUI

/// 账户额度: renders Core's `PBQuotaViewModel` directly.
struct QuotaScreen: View {
    let machine: String
    @Environment(AccountContext.self) private var context

    var body: some View {
        let vm = context.core.quota
        QuotaScreenContent(vm: vm.machineID == machine ? vm : PBQuotaViewModel())
            .task(id: machine) { await context.coreHost.openQuota(machineID: machine) }
    }
}

/// Pure presentation body for `PBQuotaViewModel`, usable directly by golden snapshot tests.
struct QuotaScreenContent: View {
    let vm: PBQuotaViewModel

    var body: some View {
        SettingsPage {
            if !vm.found {
                PageNote(L10n.quotaIosThisComputerWasRemoved())
            } else {
                content(vm)
            }
        }
        .navBar(
            L10n.settingsAccountQuota(),
            subtitle: vm.found
                ? vm.machineName + " · " + (vm.reachable ? L10n.commonIosOnline() : L10n.commonIosOffline())
                : nil
        )
    }

    @ViewBuilder private func content(_ vm: PBQuotaViewModel) -> some View {
        let offlineNotice = vm.offlineNotice.rendered()
        if !offlineNotice.isEmpty {
            GroupNote(offlineNotice, color: DS.orangeInk)
        }
        if vm.hasBanner {
            let msg = vm.banner.message.rendered()
            if !msg.isEmpty {
                GroupNote(msg, color: vm.banner.severity == .full ? DS.red : DS.orangeInk)
            }
        }
        ForEach(Array(vm.sections.enumerated()), id: \.offset) { _, section in
            sectionView(section)
        }
        let emptyText = vm.emptyText.rendered()
        if !emptyText.isEmpty {
            GroupNote(emptyText)
        }
        let footer = vm.footerNote.rendered()
        if !footer.isEmpty {
            GroupNote(footer)
        }
    }

    @ViewBuilder private func sectionView(_ section: PBQuotaCliSection) -> some View {
        if !section.accounts.isEmpty {
            ForEach(Array(section.accounts.enumerated()), id: \.offset) { _, acct in
                accountView(section, acct)
            }
        } else {
            GroupLabel(section.agentName)
            let errText = section.emptyOrErrorText.rendered()
            if section.state == .failed && !errText.isEmpty {
                GroupNote(errText, color: DS.red)
            } else if section.state == .waiting || section.state == .unsupported {
                GroupCard {
                    QuotaState(
                        section.state == .unsupported
                            ? L10n.quotaQuotaLookupIsNotSupportedYet()
                            : L10n.quotaQuotaNotAvailableYet(),
                        errText.or(section.usageHint.rendered())
                    )
                }
            } else if !errText.isEmpty {
                GroupNote(errText)
            }
        }
    }

    @ViewBuilder private func accountView(_ section: PBQuotaCliSection, _ acct: PBQuotaAccountGroupView) -> some View {
        let headerBase = acct.account.isEmpty ? section.agentName : section.agentName + " · " + acct.account
        let titleSuffix = acct.lastRecord ? L10n.quotaLastReading() : ""
        let status = acct.statusText.rendered()
        let error = acct.errorText.rendered()
        let hint = acct.usageHint.rendered()
        let freshness = acct.freshnessText.rendered()

        if !acct.groups.isEmpty {
            ForEach(Array(acct.groups.enumerated()), id: \.offset) { idx, group in
                let label = group.groupLabel.rendered()
                let base = label.isEmpty ? headerBase : (acct.account.isEmpty ? label : label + " · " + acct.account)
                GroupLabel(base + (idx == 0 ? titleSuffix : ""))
                if idx == 0 && acct.state == .failed && !error.isEmpty {
                    GroupNote(error, color: DS.red)
                }
                GroupCard {
                    for row in group.rows {
                        QuotaWindow(row: row)
                    }
                }
            }
        } else {
            GroupLabel(headerBase + titleSuffix)
            if acct.state == .failed && !error.isEmpty {
                GroupNote(error, color: DS.red)
            }
            if acct.state == .unsupported || acct.state == .waiting {
                GroupCard {
                    QuotaState(
                        acct.state == .unsupported
                            ? L10n.quotaQuotaLookupIsNotSupportedYet()
                            : L10n.quotaQuotaNotAvailableYet(),
                        [status, hint].filter { !$0.isEmpty }.joined(separator: " ")
                    )
                }
            } else if !status.isEmpty && acct.state != .failed {
                GroupNote(status, color: acct.state == .ok ? DS.ink2 : DS.orangeInk)
            }
        }

        if !status.isEmpty && !acct.groups.isEmpty {
            GroupNote(status, color: DS.orangeInk)
        }
        if !freshness.isEmpty && !acct.groups.isEmpty {
            GroupNote(freshness)
        }
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
    let row: PBQuotaRowView
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let ink = row.tone == .full || row.tone == .warn ? DS.orangeInk : row.tone == .stale ? DS.ink3 : DS.ink2
        let fill: Color = switch row.tone {
        case .stale: DS.ink3
        case .normal: DS.ink
        default: DS.orange
        }
        let sub = [row.resetText.rendered(), row.footnote.rendered()].filter { !$0.isEmpty }.joined(separator: " · ")
        let head = typeSize.isAccessibilitySize
            ? AnyLayout(VStackLayout(alignment: .leading, spacing: 2))
            : AnyLayout(HStackLayout(spacing: 8))
        VStack(alignment: .leading, spacing: 0) {
            head {
                Text(row.label.rendered()).font(.body).ink(DS.ink).frame(maxWidth: .infinity, alignment: .leading)
                Text(row.remainingText.rendered()).font(.subheadline).ink(ink)
            }
            if row.hasPercent_p {
                let frac = min(1, max(0, Double(row.remainingPercent) / 100.0))
                GeometryReader { geo in
                    Capsule().fill(DS.fill2)
                        .overlay(alignment: .leading) {
                            Capsule().fill(fill).frame(width: geo.size.width * frac)
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
