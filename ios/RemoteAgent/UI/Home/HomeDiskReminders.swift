// The disk reminder cards at the top of 会话 (ui-spec B1, 界面稿 v2 「主页提醒」): one for each online computer whose disk is
// under 10 GB, the tightest first; orange, and red under 2 GB. A card opens that computer's 电脑状态. It has no close:
// it goes when the space is back. It sits under the sentence and the notices, above what waits for you, on the page's
// own card colour (ui-rulings 3).
import RACore
import RAProto
import SwiftUI

struct HomeDiskReminders: View {
    let reminders: [PBDiskReminderView]
    @Environment(Navigator.self) private var nav

    var body: some View {
        if !reminders.isEmpty {
            VStack(spacing: 8) {
                ForEach(reminders, id: \.machineID) { r in
                    let title = r.title.rendered()
                    let detail = r.detail.rendered()
                    Button { nav.open(.machineStorage(id: r.machineID)) } label: {
                        DiskReminderCard(critical: r.critical, title: title, detail: detail)
                    }
                    .buttonStyle(PressStyle())
                    .accessibilityElement(children: .ignore)
                    .accessibilityLabel([title, detail].filter { !$0.isEmpty }.joined(separator: L10n.commonIosSpokenSeparator()))
                    .accessibilityAddTraits(.isButton)
                    .accessibilityIdentifier("disk-reminder-\(r.machineID)")
                }
            }
            .padding(.horizontal, DS.Space.card).padding(.top, 12)
        }
    }
}

private struct DiskReminderCard: View {
    let critical: Bool
    let title: String
    let detail: String

    /// The mockup's gradients of the icon block (`.al .ic`, `.al .ic.r`).
    private var tile: LinearGradient {
        let top: Color, bottom: Color
        if critical {
            top = Color(red: 1, green: 0x6B / 255, blue: 0x60 / 255); bottom = Color(red: 0xE5 / 255, green: 0x34 / 255, blue: 0x2A / 255)
        } else {
            top = Color(red: 1, green: 0xB3 / 255, blue: 0x40 / 255); bottom = Color(red: 0xF0 / 255, green: 0x8A / 255, blue: 0)
        }
        return LinearGradient(colors: [top, bottom], startPoint: .top, endPoint: .bottom)
    }

    var body: some View {
        HStack(spacing: 13) {
            Image(systemName: StatusSymbol.disk.rawValue).font(.system(size: 19, weight: .medium)).foregroundStyle(.white)
                .frame(width: 36, height: 36)
                .background(tile, in: RoundedRectangle(cornerRadius: 9, style: .continuous))
            VStack(alignment: .leading, spacing: 1) {
                Text(title).font(.callout.weight(.semibold)).foregroundStyle(DS.ink).fixedSize(horizontal: false, vertical: true)
                Text(detail).font(.subheadline).foregroundStyle(DS.ink2).fixedSize(horizontal: false, vertical: true)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            Arrow(DS.ink3)
        }
        .padding(.init(top: 13, leading: 13, bottom: 13, trailing: 14))
        .frame(maxWidth: .infinity, minHeight: DS.minHit)
        .background(DS.gbg, in: RoundedRectangle(cornerRadius: DS.Radius.group, style: .continuous))
        .contentShape(Rectangle())
    }
}
