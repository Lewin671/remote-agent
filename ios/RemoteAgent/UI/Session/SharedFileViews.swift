// The card of a file the agent handed over, rendered directly from Core's
// `PBSessionSharedFileItem`: a 44 point tile that becomes the file's own
// thumbnail once it is on the phone, two lines of words, and the one control
// that changes with the state: a download arrow, a progress ring, 打开 or 重试.
// The whole card is the control's button; a long press floats it with its menu
// (`SharedFileMenu`).
import RACore
import RAProto
import SwiftUI
import UIKit

/// What the card's colours are made of; the mockup's `fill2`, `bubble` and `lift` are `DS`'s own where it has them.
private enum CardInk {
    static let lift = Color(uiColor: UIColor { $0.userInterfaceStyle == .dark ? UIColor(red: 0x1C / 255, green: 0x1C / 255, blue: 0x1E / 255, alpha: 1) : .white })
}

// MARK: The row of the chat

/// The files of one `send_file` call: a card each, 8 apart, at the left with the agent's words.
struct SharedFilesRow: View {
    let item: PBSessionSharedFilesItem
    let env: ChatEnv

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            ForEach(item.files, id: \.fileID) { file in
                SharedFileCardView(file: file, env: env)
            }
        }
        // 8 from the next card, as the cards of one row are (the rows themselves are 4 + 4).
        .padding(.horizontal, chatMargin).padding(.vertical, 4)
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

/// Where a view is in the window, kept as it moves and asked when it is needed (a long press): a plain box, so that
/// a list scrolling does not draw anything again for it.
@MainActor
final class FrameBox {
    var frame = CGRect.zero
}

@MainActor
func resolvedSharedFileItem(_ item: PBSessionSharedFileItem) -> PBSessionSharedFileItem {
    #if DEBUG
    return SharedFileDebug.overrideItem(item)
    #else
    return item
    #endif
}

@MainActor
func resolvedSharedFileThumbnail(center: SharedFileCenter, key: SharedFileKey, item: PBSessionSharedFileItem) -> UIImage? {
    if let held = center.thumbnail(key) { return held }
    #if DEBUG
    if item.status == .downloaded { return SharedFileDebug.thumbnail(for: item.kind, name: item.displayName) }
    #endif
    return nil
}

// MARK: The card in the chat

struct SharedFileCardView: View {
    let file: PBSessionSharedFileItem
    let env: ChatEnv

    @Environment(AccountContext.self) private var context
    @State private var box = FrameBox()
    @State private var pressed = false

    private var key: SharedFileKey { SharedFileKey(sessionId: env.session, fileId: file.fileID) }

    var body: some View {
        let center = SharedFileCenter.of(context.remote)
        let item = resolvedSharedFileItem(file)
        let pbFile = sharedFileDescriptor(item)
        let transfer = center.transfer(key, file: pbFile)
        SharedFileCardBody(item: item, thumbnail: resolvedSharedFileThumbnail(center: center, key: key, item: item))
            .onGeometryChange(for: CGRect.self) { $0.frame(in: .global) } action: { box.frame = $0 }
            .opacity(pressed ? 0.7 : 1)
            .contentShape(RoundedRectangle(cornerRadius: 20, style: .continuous))
            .onTapGesture { center.tap(item, sessionId: env.session) }
            .onLongPressGesture(minimumDuration: 0.4, maximumDistance: 12) {
                openMenu(item: item)
            } onPressingChanged: { pressed = $0 }
            // The card is a button; its control reads as the action it does (ui-spec §9).
            .accessibilityElement(children: .contain)
            .accessibilityChildren {
                SharedFileAccessibility(
                    item: item,
                    act: { center.runMenuAction($0, item: item, sessionId: env.session) },
                    tap: { center.tap(item, sessionId: env.session) }
                )
            }
            .task(id: item.fileID + env.session) { await center.appeared(key, file: pbFile) }
            .task(id: DownloadedCopy(transfer)) {
                if case .downloaded(let url, _) = transfer { await center.loadMedia(key, url: url, kind: item.kind) }
            }
            .onChange(of: item.status == .downloaded) { was, now in
                // Finished while VoiceOver is reading: said once; no toast and no per-percent updates.
                if !was, now, UIAccessibility.isVoiceOverRunning {
                    AccessibilityNotification.Announcement(L10n.sharedFileA11YDownloadedAnnounce(name: item.displayName)).post()
                }
            }
            #if DEBUG
            .task { await SharedFileDebug.autoMenu(for: item, open: { openMenu(item: item) }) }
            .debugSignal("sharedfile.tap." + item.fileID) { center.tap(item, sessionId: env.session) }
            .debugSignal("sharedfile.menu." + item.fileID) { openMenu(item: item) }
            .debugSignal("sharedfile.quiet." + item.fileID) { center.begin(key, file: pbFile); center.leftPage() }
            .debugSignal("sharedfile.photos." + item.fileID) { center.saveToPhotos(key, item: item) }
            #endif
    }

    private struct DownloadedCopy: Equatable {
        let url: URL?
        init(_ t: SharedFileTransfer) { if case .downloaded(let u, _) = t { url = u } else { url = nil } }
    }

    private func openMenu(item: PBSessionSharedFileItem) {
        let frame = box.frame
        guard frame.width > 0 else { return }
        SharedFileMenu.present(SharedFileMenuRequest(item: item, env: env, frame: frame, insets: SystemSheets.top()?.view.window?.safeAreaInsets ?? .zero, context: context))
    }
}

/// VoiceOver's view of a card: the card as a button reading Core's `a11y_label` and doing its primary action,
/// the control as its own button, and the long press's menu as actions.
private struct SharedFileAccessibility: View {
    let item: PBSessionSharedFileItem
    let act: (PBSharedFileMenuAction) -> Void
    let tap: () -> Void

    var body: some View {
        Color.clear
            .accessibilityLabel(item.a11YLabel.rendered())
            .accessibilityAddTraits(.isButton)
            .accessibilityAction(.default, tap)
            .accessibilityActions {
                ForEach(item.menuItems.filter(\.enabled), id: \.action) { menuItem in
                    Button(menuItem.label.rendered()) { act(menuItem.action) }
                }
            }
        if item.primaryAction != .unspecified, item.hasPrimaryLabel {
            Color.clear
                .accessibilityLabel(item.primaryLabel.rendered())
                .accessibilityAddTraits(.isButton)
                .accessibilityAction(.default, tap)
        }
    }
}

// MARK: The card itself

/// The card as drawn: a Core ViewModel in, a picture out. `lifted` is how the long press shows it floating.
struct SharedFileCardBody: View {
    let item: PBSessionSharedFileItem
    var thumbnail: UIImage?
    var lifted = false
    var width: CGFloat = 296

    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let away = item.status == .offline || item.status == .cleaned
        let gone = item.status == .cleaned
        // At the accessibility sizes the words need the width of the column: the card spreads over it and the control
        // goes under the words, at the right, instead of taking a third of the 296 away from them.
        let stacked = typeSize.isAccessibilitySize
        let words = VStack(alignment: .leading, spacing: 2) {
            MiddleEllipsisName(name: item.displayName, head: item.nameHead, tail: item.nameTail, large: typeSize >= .xxxLarge)
                .foregroundStyle(gone ? DS.ink3 : away ? DS.ink2 : DS.ink)
            Text(verbatim: item.subtitle.rendered())
                .font(.footnote).monospacedDigit()
                .foregroundStyle(subtitleInk)
                .fixedSize(horizontal: false, vertical: true)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        Group {
            if stacked {
                VStack(alignment: .leading, spacing: 10) {
                    HStack(alignment: .top, spacing: 12) {
                        SharedFileTile(kind: item.kind, thumbnail: away ? nil : thumbnail, dimmed: away)
                        words
                    }
                    if item.primaryAction != .unspecified {
                        SharedFileControlView(item: item).frame(maxWidth: .infinity, alignment: .trailing)
                    }
                }
            } else {
                HStack(spacing: 12) {
                    SharedFileTile(kind: item.kind, thumbnail: away ? nil : thumbnail, dimmed: away)
                    words
                    SharedFileControlView(item: item)
                }
            }
        }
        .padding(.leading, 10).padding(.trailing, 12).padding(.vertical, 10)
        .frame(maxWidth: stacked ? .infinity : width, alignment: .leading)
        .background(lifted ? CardInk.lift : DS.bubble, in: RoundedRectangle(cornerRadius: 20, style: .continuous))
    }

    private var subtitleInk: Color {
        if item.subtitleIsError { return DS.red }
        if item.status == .cleaned { return DS.ink3 }
        return DS.ink2
    }
}

/// 15 points, 600: one line with the middle left out using Core's `name_head`/`name_tail`, or two lines at the largest sizes.
struct MiddleEllipsisName: View {
    let name: String
    let head: String
    let tail: String
    let large: Bool
    var font: Font = .subheadline.weight(.semibold)
    var tracking: CGFloat = -0.1

    var body: some View {
        if large {
            Text(verbatim: name).font(font).tracking(tracking)
                .lineLimit(2).truncationMode(.middle)
        } else {
            let h = head.isEmpty ? name : head
            HStack(spacing: 0) {
                Text(verbatim: h).lineLimit(1).truncationMode(.tail)
                if !tail.isEmpty { Text(verbatim: tail).lineLimit(1).fixedSize().layoutPriority(1) }
            }
            .font(font).tracking(tracking)
        }
    }
}

// MARK: The tile

/// The mockup's four gradients at 160 degrees, and audio's own (orange, as on Android).
private enum TileColors {
    static func colors(_ kind: PBSharedFileKind) -> [Color] {
        func c(_ a: UInt32, _ b: UInt32) -> [Color] { [Color(hex: a), Color(hex: b)] }
        switch kind {
        case .video: return c(0x7D7AFF, 0x5856D6)
        case .image: return c(0x5AC8FA, 0x2F9BF0)
        case .audio: return c(0xFFB45E, 0xFF9500)
        case .pdf, .doc: return c(0xFF8A80, 0xFF453A)
        case .archive, .other, .unspecified, .UNRECOGNIZED: return c(0xB0B0B8, 0x8E8E93)
        }
    }

    static func symbol(_ kind: PBSharedFileKind) -> String {
        switch kind {
        case .video: "video"
        case .image: "photo"
        case .audio: "music.note"
        case .archive: "doc.zipper"
        case .pdf, .doc, .other, .unspecified, .UNRECOGNIZED: "doc"
        }
    }
}

private extension Color {
    init(hex: UInt32) {
        self.init(red: Double(hex >> 16 & 0xff) / 255, green: Double(hex >> 8 & 0xff) / 255, blue: Double(hex & 0xff) / 255)
    }
}

struct SharedFileTile: View {
    let kind: PBSharedFileKind
    var thumbnail: UIImage?
    var dimmed = false

    var body: some View {
        ZStack {
            // `linear-gradient(160deg, …)` over a square: its line runs a little past the box.
            LinearGradient(colors: TileColors.colors(kind), startPoint: UnitPoint(x: 0.281, y: -0.102), endPoint: UnitPoint(x: 0.719, y: 1.102))
            Image(systemName: TileColors.symbol(kind))
                .font(.system(size: 22, weight: .regular))
                .foregroundStyle(.white)
            if let thumbnail {
                Image(uiImage: thumbnail).resizable().scaledToFill().frame(width: 44, height: 44).clipped()
                if kind == .video {
                    RadialGradient(colors: [.black.opacity(0.28), .clear], center: .center, startRadius: 0, endRadius: 26)
                    Image(systemName: "play.fill").font(.system(size: 16)).foregroundStyle(.white)
                }
            }
        }
        .frame(width: 44, height: 44)
        .clipShape(RoundedRectangle(cornerRadius: 11, style: .continuous))
        // The picture comes in by fading over the colour (200 ms).
        .animation(Motion.enter(), value: thumbnail != nil)
        .saturation(dimmed ? 0 : 1).opacity(dimmed ? 0.55 : 1)
        .accessibilityHidden(true)
    }
}

// MARK: The control

/// The one control at the card's right. Its pieces cross-fade, 0.9 to 1, 200 ms (ui-spec §4).
struct SharedFileControlView: View {
    let item: PBSessionSharedFileItem

    private enum Piece: Hashable { case arrow, grey, ring, open, retry, none }

    private var piece: Piece {
        switch item.status {
        case .available: .arrow
        case .offline: .grey
        case .downloading, .paused: .ring
        case .downloaded: .open
        case .failed: .retry
        case .cleaned, .unspecified, .UNRECOGNIZED: .none
        }
    }

    var body: some View {
        ZStack {
            switch item.status {
            case .available: ArrowCircle(color: DS.blue)
            case .offline: ArrowCircle(color: DS.ink3)
            case .downloading: ProgressRing(progress: item.progress, paused: false, enabled: item.primaryEnabled)
            case .paused: ProgressRing(progress: item.progress, paused: true, enabled: item.primaryEnabled)
            case .downloaded: Pill(text: item.primaryLabel.rendered())
            case .failed: Pill(text: item.primaryLabel.rendered())
            case .cleaned, .unspecified, .UNRECOGNIZED: EmptyView()
            }
        }
        .id(piece)
        .transition(Motion.reduced ? .opacity : .opacity.combined(with: .scale(scale: 0.9)))
        .animation(Motion.enter(), value: piece)
        .accessibilityHidden(true)
    }
}

/// The arrow of the mockup: a stem and a head, 2.4 of a 24 unit box.
private struct ArrowShape: Shape {
    func path(in rect: CGRect) -> Path {
        let s = rect.width / 24
        var p = Path()
        p.move(to: CGPoint(x: rect.minX + 12 * s, y: rect.minY + 5 * s))
        p.addLine(to: CGPoint(x: rect.minX + 12 * s, y: rect.minY + 18 * s))
        p.move(to: CGPoint(x: rect.minX + 7 * s, y: rect.minY + 13 * s))
        p.addLine(to: CGPoint(x: rect.minX + 12 * s, y: rect.minY + 18 * s))
        p.addLine(to: CGPoint(x: rect.minX + 17 * s, y: rect.minY + 13 * s))
        return p
    }
}

private struct ArrowCircle: View {
    let color: Color
    var body: some View {
        ZStack {
            Circle().fill(DS.fill2)
            // 15 points, drawn at 24 units: a line 1.5 points wide is 2.4 units.
            ArrowShape().stroke(color, style: StrokeStyle(lineWidth: 1.5, lineCap: .round, lineJoin: .round)).frame(width: 15, height: 15)
        }
        .frame(width: 30, height: 30)
    }
}

private struct ProgressRing: View {
    let progress: Double
    let paused: Bool
    let enabled: Bool

    var body: some View {
        let fraction = min(1, max(0, progress))
        let ink = enabled ? DS.blue : DS.ink3
        ZStack {
            Circle().stroke(DS.fill2, lineWidth: 2.6)
            Circle().trim(from: 0, to: fraction)
                .stroke(ink, style: StrokeStyle(lineWidth: 2.6, lineCap: .round))
                .rotationEffect(.degrees(-90))
                // Grows with the bytes, not in steps.
                .animation(Motion.reduced ? nil : .linear(duration: 0.4), value: fraction)
            if paused {
                ArrowShape().stroke(ink, style: StrokeStyle(lineWidth: 1.4, lineCap: .round, lineJoin: .round)).frame(width: 13, height: 13)
            } else {
                RoundedRectangle(cornerRadius: 2.5, style: .continuous).fill(ink).frame(width: 9, height: 9)
            }
        }
        // The mockup's ring is 30 across with its stroke on a radius of 13.
        .padding(2)
        .frame(width: 30, height: 30)
    }
}

private struct Pill: View {
    let text: String
    var body: some View {
        Text(verbatim: text).font(.subheadline.weight(.semibold)).foregroundStyle(DS.blue)
            .lineLimit(1).fixedSize()
            .padding(.horizontal, 14).frame(minHeight: 30)
            .background(DS.fill2, in: Capsule())
    }
}
