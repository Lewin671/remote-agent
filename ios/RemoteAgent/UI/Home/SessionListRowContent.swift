import SwiftUI

/// The session row's shared three-line layout, also used for scheduled tasks.
struct SessionListRowContent<Second: View>: View {
    let title: String
    let right: String
    let third: String
    let color: Color
    let last: Bool
    @ViewBuilder let second: () -> Second
    @Environment(\.dynamicTypeSize) private var typeSize
    @ScaledMetric(relativeTo: .caption) private var dot: CGFloat = 8
    var body: some View {
        HStack(alignment: .top, spacing: 11) {
            Text(" ").type(T.bodyB).hidden().frame(width: dot).overlay { Dot(color) }.accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 0) {
                if typeSize.isAccessibilitySize {
                    Text(title).type(T.bodyB, Theme.fg).lineLimit(2)
                    second().lineLimit(2)
                    Text(third).type(T.meta, Theme.muted).lineLimit(2)
                    Text(right).type(T.meta, Theme.muted).frame(maxWidth: .infinity, alignment: .trailing)
                } else {
                    HStack(alignment: .firstTextBaseline, spacing: 8) {
                        Text(title).type(T.bodyB, Theme.fg).lineLimit(1).frame(maxWidth: .infinity, alignment: .leading)
                        Text(right).type(T.meta, Theme.muted).lineLimit(1).fixedSize()
                    }
                    second().lineLimit(1)
                    if !third.isEmpty { Text(third).type(T.meta, Theme.muted).lineLimit(1).padding(.top, 1) }
                }
            }.padding(.bottom, 10).frame(maxWidth: .infinity, alignment: .leading).bottomLine(last ? .clear : Theme.line)
        }.padding(.horizontal, Theme.Space.page).padding(.top, 10)
    }
}
