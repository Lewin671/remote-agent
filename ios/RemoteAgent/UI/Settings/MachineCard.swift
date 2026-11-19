// A computer row in settings (settings-redesign): a unified insetGrouped row
// with a 20 pt monochrome laptop icon (leading text/divider aligned at 46 pt),
// its name, online dot + state label, and an inline `subtitleParts` summary
// where only `warn` segments render in `orangeInk` 600. No multi-bar stacks.
import RACore
import RAProto
import SwiftUI

struct MachineCard: View {
    let machine: PBMachineListCardView
    @Environment(Navigator.self) private var nav
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let id = machine.machineID
        let statusText = machine.member ? machine.statusText.rendered() : L10n.settingsMachineSubRemoved()
        let activeIcon = machine.online && machine.member
        GRow(
            machine.name.or(id),
            value: statusText.isEmpty ? nil : statusText,
            chevron: true,
            valueColor: machine.statusWarn ? DS.orangeInk : DS.ink2,
            valueLines: 1
        ) {
            nav.open(.machine(id: id))
        }
        .lead {
            SymbolImage("laptopcomputer", size: 20, color: activeIcon ? DS.ink : DS.ink2)
                .frame(width: 22, alignment: .center)
        }
        .subView {
            subtitleView
        }
        .valueLead {
            if machine.member {
                if machine.statusDot != .unspecified {
                    machineStatusDotView(machine.statusDot)
                } else {
                    Dot(machine.online ? DS.green : DS.ink3)
                }
            }
        }
        .contextMenu {
            Button(L10n.settingsAccountQuota()) { nav.open(.quota(machine: id)) }
            Button(L10n.homeConnectionCheck()) { nav.open(.connectionCheck) }
        }
    }

    @ViewBuilder private var subtitleView: some View {
        let parts = machine.subtitleParts.compactMap { seg -> (text: String, warn: Bool)? in
            let rendered = seg.text.rendered()
            return rendered.isEmpty ? nil : (rendered, seg.warn)
        }
        if !parts.isEmpty {
            let line: Text = parts.enumerated().reduce(Text("")) { acc, item in
                let sep = item.offset > 0 ? Text(" · ").foregroundStyle(DS.ink2) : Text("")
                let piece = Text(item.element.text)
                    .foregroundStyle(item.element.warn ? DS.orangeInk : DS.ink2)
                    .fontWeight(item.element.warn ? .semibold : .regular)
                return acc + sep + piece
            }
            line
                .font(.subheadline)
                .lineLimit(typeSize.isAccessibilitySize ? nil : 2)
        }
    }
}
