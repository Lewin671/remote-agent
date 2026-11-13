// The long press of a file card (ui-spec §6, mockup §① 长按): the card floats
// where it was (its ground becomes `lift`, with the floating shadow), the page
// behind it is blurred 14 points and dimmed, and 10 points under the card is a
// menu 250 wide: the file's path on the computer and where it came from, then the
// actions of the card's state, words on the left and icons on the right, the
// destructive one apart and red.
//
// The system's context menu cannot be made to look like this (its width, its
// header and the side of its icons are the system's), so the page is a snapshot
// of the window, blurred, under a screen of its own that is presented over it.
import CoreImage
import CoreImage.CIFilterBuiltins
import RACore
import RAProto
import SwiftUI
import UIKit

struct SharedFileMenuRequest {
    let file: PBSharedFile
    let env: ChatEnv
    /// The card in the window.
    let frame: CGRect
    /// The window's safe area, which the screen of the menu draws under.
    let insets: UIEdgeInsets
    let context: AccountContext
}

@MainActor
enum SharedFileMenu {
    private final class Host: UIHostingController<AnyView> {
        override var prefersStatusBarHidden: Bool { false }
    }

    static func present(_ request: SharedFileMenuRequest) {
        guard let top = SystemSheets.top(), !(top is Host), let window = top.view.window else { return }
        let backdrop = snapshot(window)
        let host = Host(rootView: AnyView(EmptyView()))
        host.rootView = AnyView(
            SharedFileMenuView(request: request, backdrop: backdrop, close: { [weak host] after in
                host?.dismiss(animated: false) { after() }
            })
            .environment(request.context)
        )
        host.view.backgroundColor = .clear
        host.modalPresentationStyle = .overFullScreen
        top.present(host, animated: false)
    }

    /// The window as it is, blurred 14 points.
    private static func snapshot(_ window: UIWindow) -> UIImage? {
        let bounds = window.bounds
        let image = UIGraphicsImageRenderer(bounds: bounds).image { _ in
            window.drawHierarchy(in: bounds, afterScreenUpdates: false)
        }
        guard let input = CIImage(image: image) else { return image }
        let blur = CIFilter.gaussianBlur()
        blur.inputImage = input.clampedToExtent()
        blur.radius = Float(14 * image.scale)
        guard let output = blur.outputImage?.cropped(to: input.extent),
              let cg = CIContext().createCGImage(output, from: input.extent)
        else { return image }
        return UIImage(cgImage: cg, scale: image.scale, orientation: .up)
    }
}

private struct SharedFileMenuView: View {
    let request: SharedFileMenuRequest
    let backdrop: UIImage?
    let close: (@escaping () -> Void) -> Void

    @Environment(AccountContext.self) private var context
    @Environment(\.colorScheme) private var scheme
    @State private var shown = false
    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var menuHeight: CGFloat = 0

    private var key: SharedFileKey { SharedFileKey(sessionId: request.env.session, fileId: request.file.fileID) }

    var body: some View {
        let center = SharedFileCenter.of(context.remote)
        let card = resolvedSharedFileCard(center: center, key: key, file: request.file, env: request.env)
        let kind = classifySharedFile(request.file.name)
        let items = sharedFileMenu(card, kind: kind)
        GeometryReader { proxy in
            let screen = proxy.size
            let safe = request.insets
            let frame = request.frame
            // Room for the card and its menu: if the menu would run off the bottom they move up together.
            let bottom = screen.height - max(safe.bottom, 8) - 16
            let menuWidth: CGFloat = typeSize.isAccessibilitySize ? min(screen.width - 2 * 14, 400) : 250
            let cardWidth = typeSize.isAccessibilitySize ? screen.width - 2 * 14 : frame.width
            // The card's height is what it was in the list (the same words in the same width).
            let needed = frame.height + 10 + menuHeight
            let top = frame.minY + needed > bottom ? max(safe.top + 8, bottom - needed) : frame.minY
            // Where the menu cannot be all shown it scrolls.
            let room = max(160, bottom - (top + frame.height + 10))
            let x = typeSize.isAccessibilitySize ? 14 : min(frame.minX, max(8, screen.width - menuWidth - 8))
            ZStack(alignment: .topLeading) {
                backdropView.ignoresSafeArea().opacity(shown ? 1 : 0)
                    .contentShape(Rectangle())
                    .onTapGesture { dismiss() }
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: 10) {
                    SharedFileCardBody(file: request.file, card: card, thumbnail: resolvedSharedFileThumbnail(center: center, key: key, file: request.file, card: card), lifted: true, width: cardWidth)
                        .frame(width: cardWidth)
                        .shadow(color: .black.opacity(scheme == .dark ? 0.6 : 0.22), radius: shown ? 30 : 0, y: shown ? 20 : 0)
                        .overlay {
                            RoundedRectangle(cornerRadius: 20, style: .continuous)
                                .strokeBorder(scheme == .dark ? Color.white.opacity(0.10) : Color.black.opacity(0.08), lineWidth: 0.5)
                        }
                        .scaleEffect(shown && !Motion.reduced && !typeSize.isAccessibilitySize ? 1.03 : 1, anchor: .topLeading)
                        .allowsHitTesting(false)
                    menu(items: items, width: menuWidth, height: menuHeight == 0 ? nil : min(menuHeight, room))
                        .opacity(shown ? 1 : 0)
                        .scaleEffect(shown || Motion.reduced ? 1 : 0.96, anchor: .topLeading)
                }
                .offset(x: x, y: top)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        }
        .ignoresSafeArea()
        .accessibilityAddTraits(.isModal)
        .onAppear { withAnimation(Motion.enter()) { shown = true } }
        // What the card does meanwhile (a download that finishes) is shown by the card.
        .accessibilityAction(.escape) { dismiss() }
    }

    @ViewBuilder private var backdropView: some View {
        ZStack {
            if let backdrop { Image(uiImage: backdrop).resizable().scaledToFill() }
            Color.black.opacity(0.18)
        }
    }

    private func dismiss(then action: @escaping () -> Void = {}) {
        withAnimation(Motion.exit()) { shown = false }
        DispatchQueue.main.asyncAfter(deadline: .now() + Motion.base * 0.8) { close(action) }
    }

    private func choose(_ item: SharedFileMenuItem) {
        let center = SharedFileCenter.of(context.remote)
        let (key, file) = (key, request.file)
        dismiss {
            switch item {
            case .download, .resume: center.begin(key, file: file)
            case .pause: center.pause(key, file: file)
            case .cancelDownload: center.cancel(key)
            case .saveToPhotos: center.saveToPhotos(key, file: file)
            case .saveToFiles: center.saveToFiles(key, file: file)
            case .share: center.share(key, file: file)
            case .deleteLocal: center.deleteLocal(key)
            }
        }
    }

    // MARK: The menu

    private func menu(items: [SharedFileMenuItem], width: CGFloat, height: CGFloat?) -> some View {
        let state = context.remote.state.value
        let machineId = state.sessions[request.env.session]?.machineId ?? ""
        let path = displaySharedPath(request.file.path, home: state.machines[machineId]?.meta?.home ?? request.env.home, fallbackName: request.file.name)
        return ScrollView {
            VStack(spacing: 0) {
                VStack(alignment: .leading, spacing: 3) {
                    Text(verbatim: path)
                        .font(.system(size: 11.5, design: .monospaced)).lineSpacing(11.5 * 0.45 - 4)
                        .foregroundStyle(DS.ink2)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    Text(verbatim: L10n.sharedFileMenuFromMachine(machine: request.env.machine))
                        .font(.caption).foregroundStyle(DS.ink3)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .padding(.horizontal, 16).padding(.top, 11).padding(.bottom, 10)
                .accessibilityElement(children: .combine)
                ForEach(items, id: \.label) { item in
                    divider
                    if item == .deleteLocal { Rectangle().fill(DS.fill).frame(height: 8) }
                    row(item)
                }
            }
            .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { menuHeight = $0 }
        }
        .scrollBounceBehavior(.basedOnSize)
        .frame(width: width, height: height)
        .background(MenuInk.ground, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
        .clipShape(RoundedRectangle(cornerRadius: 14, style: .continuous))
        .shadow(color: .black.opacity(scheme == .dark ? 0.6 : 0.22), radius: 30, y: 20)
        .overlay {
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .strokeBorder(scheme == .dark ? Color.white.opacity(0.10) : Color.black.opacity(0.08), lineWidth: 0.5)
        }
    }

    @ViewBuilder private var divider: some View {
        Rectangle().fill(DS.sep).frame(height: 0.5)
    }

    private func row(_ item: SharedFileMenuItem) -> some View {
        let destructive = item == .deleteLocal
        return Button { choose(item) } label: {
            HStack(spacing: 8) {
                Text(verbatim: item.label).font(.body).multilineTextAlignment(.leading)
                Spacer(minLength: 8)
                Image(systemName: icon(item)).font(.system(size: 19)).accessibilityHidden(true)
            }
            .foregroundStyle(destructive ? DS.red : DS.ink)
            .padding(.horizontal, 16).frame(minHeight: 44)
            .contentShape(Rectangle())
        }
        .buttonStyle(RowPressStyle())
    }

    private func icon(_ item: SharedFileMenuItem) -> String {
        switch item {
        case .download, .resume: "arrow.down"
        case .pause: "pause"
        case .cancelDownload: "xmark"
        case .saveToPhotos: "photo"
        case .saveToFiles: "square.and.arrow.down"
        case .share: "square.and.arrow.up"
        case .deleteLocal: "trash"
        }
    }
}

/// The mockup's `--menu`: nearly opaque, light or dark.
private enum MenuInk {
    static let ground = Color(uiColor: UIColor { trait in
        trait.userInterfaceStyle == .dark
            ? UIColor(red: 44 / 255, green: 44 / 255, blue: 46 / 255, alpha: 0.86)
            : UIColor(red: 250 / 255, green: 250 / 255, blue: 252 / 255, alpha: 0.86)
    })
}
