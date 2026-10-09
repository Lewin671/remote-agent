// Bottom sheets (docs/design-system.md §6): the system's sheet, with its
// grabber, swipe to dismiss and detents, on the theme's card with the top
// corners of r-lg. A sheet is as tall as what it holds unless told
// otherwise, as Android's are, and scrolls once that is more than the
// screen.
import SwiftUI

enum SheetSize: Sendable {
    /// As tall as its content, up to the whole screen.
    case fit
    /// Half the screen, and the whole of it when pulled up.
    case mediumOrLarge
    /// The whole screen.
    case large
}

private struct SheetBody<Content: View>: View {
    let size: SheetSize
    let content: Content
    @State private var height: CGFloat = 0

    private var detents: Set<PresentationDetent> {
        switch size {
        // Before the first layout the height is not known; a height that fits nothing would flash.
        case .fit: height > 0 ? [.height(height)] : [.medium]
        case .mediumOrLarge: [.medium, .large]
        case .large: [.large]
        }
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 0) { content }
                .frame(maxWidth: .infinity, alignment: .leading)
                // The grabber sits in the first 26 pt.
                .padding(.init(top: 26, leading: 16, bottom: 24, trailing: 16))
                .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { height = $0 }
        }
        .scrollBounceBehavior(.basedOnSize)
        // Whatever page it is over, a sheet's own surface is the card.
        .environment(\.onSoft, false)
        .presentationDetents(detents)
        .presentationDragIndicator(.visible)
        .presentationCornerRadius(Theme.Radius.lg)
        .presentationBackground(Theme.card)
        .tint(Theme.blue)
    }
}

extension View {
    /// A bottom sheet over this view. Start its content with a `SheetHead`.
    func kitSheet<Content: View>(isPresented: Binding<Bool>, size: SheetSize = .fit, onDismiss: (() -> Void)? = nil, @ViewBuilder content: @escaping () -> Content) -> some View {
        sheet(isPresented: isPresented, onDismiss: onDismiss) { SheetBody(size: size, content: content()) }
    }

    /// A bottom sheet shown while `item` is set, built from it.
    func kitSheet<Item: Identifiable, Content: View>(item: Binding<Item?>, size: SheetSize = .fit, onDismiss: (() -> Void)? = nil, @ViewBuilder content: @escaping (Item) -> Content) -> some View {
        sheet(item: item, onDismiss: onDismiss) { SheetBody(size: size, content: content($0)) }
    }
}

/// The title row of a bottom sheet: 17, with 完成 or 取消 at the right only
/// when the sheet has something to confirm, and a way back when it is a
/// second step.
struct SheetHead: View {
    private let title: String
    private let action: String?
    private let onAction: (() -> Void)?
    private let onBack: (() -> Void)?

    init(_ title: String, action: String? = nil, onAction: (() -> Void)? = nil, onBack: (() -> Void)? = nil) {
        self.title = title
        self.action = action
        self.onAction = onAction
        self.onBack = onBack
    }

    var body: some View {
        HStack(spacing: 10) {
            if let onBack {
                Button(action: onBack) {
                    Ic(Lucide.chevronLeft, size: 22, color: Theme.blue).hitTarget(minWidth: 28, alignment: .leading)
                }
                .buttonStyle(PressStyle())
                .accessibilityLabel("返回")
            }
            Text(title).type(T.h17, Theme.fg).frame(maxWidth: .infinity, alignment: .leading).accessibilityAddTraits(.isHeader)
            // As wide as its words: 完成 and 取消 fit in 44 pt, 以后再说 does not.
            if let action, let onAction { TextAction(action, style: T.body, alignment: .center, action: onAction) }
        }
        .frame(minHeight: action != nil || onBack != nil ? Theme.minHit : nil)
        .padding(.bottom, 10)
    }
}
