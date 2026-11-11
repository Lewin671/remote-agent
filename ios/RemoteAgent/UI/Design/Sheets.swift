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

/// Content that lays out its own scroll and keeps its head and foot in place (the folder picker) says so
/// with `sheetPinned()`: the sheet is then the whole screen and does not scroll its content.
private struct SheetPinnedKey: PreferenceKey {
    static let defaultValue = false
    static func reduce(value: inout Bool, nextValue: () -> Bool) { value = value || nextValue() }
}

extension View {
    func sheetPinned() -> some View { preference(key: SheetPinnedKey.self, value: true) }
}

private struct SheetBody<Content: View>: View {
    let size: SheetSize
    let content: Content
    @State private var height: CGFloat = 0
    @State private var pinned = false

    private var detents: Set<PresentationDetent> {
        if pinned { return [.large] }
        switch size {
        // Before the first layout the height is not known; a height that fits nothing would flash.
        case .fit: return height > 0 ? [.height(height)] : [.medium]
        case .mediumOrLarge: return [.medium, .large]
        case .large: return [.large]
        }
    }

    // The grabber sits in the first 26 pt.
    private let insets = EdgeInsets(top: 26, leading: 16, bottom: 24, trailing: 16)

    var body: some View {
        // One structure whether pinned or not, so what the content keeps in @State survives the change.
        GeometryReader { screen in
            ScrollView {
                VStack(alignment: .leading, spacing: 0) { content }
                    .frame(maxWidth: .infinity, maxHeight: pinned ? screen.size.height - insets.top - insets.bottom : nil, alignment: .topLeading)
                    .padding(insets)
                    .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { if !pinned { height = $0 } }
            }
            .scrollBounceBehavior(.basedOnSize)
            .scrollDisabled(pinned)
        }
        .onPreferenceChange(SheetPinnedKey.self) { pinned = $0 }
        .presentationDetents(detents)
        .sheetSurface()
        .tint(DS.blue)
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
                    SymbolImage("chevron.left", size: 22, color: DS.blue).hitTarget(minWidth: 28, alignment: .leading)
                }
                .buttonStyle(PressStyle())
                .accessibilityLabel("返回")
            }
            Text(title).font(.headline).ink(DS.ink).frame(maxWidth: .infinity, alignment: .leading).accessibilityAddTraits(.isHeader)
            // As wide as its words: 完成 and 取消 fit in 44 pt, 以后再说 does not.
            if let action, let onAction { TextAction(action, style: .body, alignment: .center, action: onAction) }
        }
        .frame(minHeight: action != nil || onBack != nil ? DS.minHit : nil)
        .padding(.bottom, 10)
    }
}
