// Notices (docs/design-system.md §6): one grey surface, the text's colour
// alone telling a warning from an error.
import SwiftUI

enum BannerTone: Sendable { case plain, warn, error }

/// A notice: `gbg`, radius 18, one sentence, at most one text action at the
/// right; one that wants you to act (`.warn`) is on `orange-soft`.
struct Banner: View {
    private let text: String
    private let tone: BannerTone
    private let action: String?
    private let onAction: (() -> Void)?
    private let icon: String?

    init(_ text: String, tone: BannerTone = .plain, action: String? = nil, onAction: (() -> Void)? = nil, icon: String? = nil) {
        self.text = text
        self.tone = tone
        self.action = action
        self.onAction = onAction
        self.icon = icon
    }

    var body: some View {
        let ink: Color = switch tone {
        case .plain: DS.ink2
        case .warn: DS.orangeInk
        case .error: DS.red
        }
        HStack(spacing: 8) {
            if let icon { Mark(icon, color: ink, size: 13, relativeTo: .footnote) }
            Text(text).font(.subheadline).ink(ink).padding(.vertical, 9).frame(maxWidth: .infinity, alignment: .leading)
            if let action, let onAction {
                Button(action: onAction) {
                    Text(action).font(.subheadline.weight(.semibold)).ink(DS.blue).lineLimit(1).fixedSize().padding(.horizontal, 8).hitTarget()
                }
                .buttonStyle(PressStyle())
            }
        }
        .padding(.leading, 12).padding(.trailing, action != nil && onAction != nil ? 4 : 12)
        .frame(minHeight: 38)
        .background(tone == .warn ? DS.orangeSoft : DS.gbg, in: RoundedRectangle(cornerRadius: DS.Radius.notice, style: .continuous))
    }
}
