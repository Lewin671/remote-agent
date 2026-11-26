// The parts of the image viewer (design mock 「会话里的图片 · 查看大图」;
// Android's ui/Viewer.kt): one page per Core `PBSessionViewerImageView`, fetching and
// decrypting its display copy, and what leaves the app by the user's
// choice. The screen that puts them together is ImageViewerScreen.swift.
//
// The preview an event carries shows at once, enlarged, and the display
// copy replaces it once it is here. That copy is decrypted in memory only:
// what is cached on disk is the sealed envelope (`CipherCache.image`), and
// `Remote.image` opens and checks it each time. Both pictures are held by
// the account's `ImageCenter` and go when the account is left.
import Photos
import RACore
import RAProto
import SwiftUI
import UIKit
import UniformTypeIdentifiers

/// The viewer's colors: it is black in both appearances, as a photo viewer is.
enum ViewerInk {
    static let text = Color.white
    static let path = Color(red: 0xD5 / 255, green: 0xDA / 255, blue: 0xDE / 255)
    static let meta = Color(red: 0xB9 / 255, green: 0xBF / 255, blue: 0xC6 / 255)
    static let action = Color(red: 0x9E / 255, green: 0xC1 / 255, blue: 0xFF / 255)
    static let panel = Color.black.opacity(0.67)
    static let chip = Color.black.opacity(0.8)
}

/// Fetches display copies for the viewer.
@MainActor
struct ViewerLoader {
    let center: ImageCenter
    let session: String

    private func key(_ imageID: String) -> String { "\(session)/\(imageID)" }

    /// The display copy held in memory for an image, if it was loaded while the viewer was open.
    func held(_ imageID: String) -> ViewedImage? { center.viewedImage(key(imageID)) }

    /// Holds an image's display copy; whoever asked `held` for it is drawn again.
    func keep(_ imageID: String, _ image: ViewedImage) { center.keepViewed(key(imageID), image) }

    /// Where the image's display copy stands, fetching it if Core says there is one.
    func load(_ image: PBSessionViewerImageView, previewImage: PBSessionImageView? = nil, online: Bool) async -> (state: BigImage, image: ViewedImage?) {
        #if DEBUG
            if let sample = await ImageDebug.viewerSample(image, previewImage: previewImage, center: center, online: online) {
                return sample
            }
        #endif
        let imageID = image.imageID
        guard image.hasDisplayCopy_p, !imageID.isEmpty else { return (.notSynced, nil) }
        if let held = held(imageID) {
            return (.ready, held)
        }
        let fetched: Result<ImageResult, any Error>
        do {
            fetched = .success(try await center.remote.image(session, imageId: imageID))
        } catch {
            fetched = .failure(error)
        }
        if Task.isCancelled { return (.loading, nil) }
        let (state, payload) = viewerOutcome(fetched, online: online)
        guard let payload else { return (state, nil) }
        let (data, format) = (payload.data, payload.format)
        // Decoded off the main actor, and before it is shown rather than while it is drawn.
        let decoded = await Task.detached { () -> ViewedImage? in
            guard let image = UIImage(data: data) else { return nil }
            return ViewedImage(
                image: image.preparingForDisplay() ?? image, bytes: data, format: format)
        }.value
        guard let decoded else { return (.corrupt(why: L10n.imageIosCannotDecodeImage()), nil) }
        keep(imageID, decoded)
        return (state, decoded)
    }
}

/// One image of the session: its preview until the display copy is here, and what is said meanwhile.
struct ViewerPage: View {
    let loader: ViewerLoader
    let image: PBSessionViewerImageView
    /// Optional timeline image view carrying 320px preview bytes (stripped from `viewer_images`).
    var previewImage: PBSessionImageView? = nil
    let online: Bool
    /// The page is the one on screen: only then does it follow `pokes`.
    let current: Bool
    /// What a script asks of the page on screen (debug builds).
    let pokes: ViewerPokes
    let onDrag: (CGFloat) -> Void
    let onClose: () -> Void

    @State private var big = BigImage.loading
    @State private var sharp: UIImage?
    @State private var attempt = 0
    @State private var zoomToggles = 0
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private struct Ask: Equatable {
        var id: String
        var attempt: Int
        var online: Bool
    }

    var body: some View {
        let previewData = previewImage?.preview.isEmpty == false ? previewImage!.preview : image.preview
        let shown = sharp ?? loader.center.preview(imageID: image.imageID, displayName: image.displayName, data: previewData)
        let note = viewerNote(big, sync: image.syncState)
        let retry = viewerCanRetry(big)
        let label = image.displayName.isEmpty ? L10n.imageTitle() : image.displayName
        ZStack {
            if let shown {
                ZoomableImage(
                    image: shown, zoomToggles: zoomToggles, reduceMotion: reduceMotion,
                    onDrag: onDrag, onClose: onClose
                )
                .ignoresSafeArea()
                .accessibilityElement()
                .accessibilityLabel(label)
                .accessibilityValue(big == .loading ? L10n.imageIosDownloading() : note ?? "")
                .accessibilityAddTraits(.isImage)
                .accessibilityAction(named: L10n.imageIosZoom()) { zoomToggles += 1 }
            } else {
                // Not even a preview: the file's name is all there is.
                Text(label).font(.footnote.monospaced()).ink(ViewerInk.text).multilineTextAlignment(
                    .center
                ).padding(24)
            }
            if big == .loading {
                Mark(Mark.progress, color: ViewerInk.text, size: 36, relativeTo: .title)
                    .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
                    .accessibilityHidden(true)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .overlay(alignment: .top) {
            if let note {
                Button {
                    if retry { attempt += 1 }
                } label: {
                    Text(note).font(.subheadline).ink(ViewerInk.text).multilineTextAlignment(.center)
                        .padding(.horizontal, 12).padding(.vertical, 8)
                        .frame(minHeight: retry ? DS.minHit : 0)
                        .background(
                            ViewerInk.chip,
                            in: RoundedRectangle(cornerRadius: DS.Radius.small, style: .continuous))
                }
                .buttonStyle(PressStyle())
                .disabled(!retry)
                .accessibilityHint(retry ? L10n.commonRetry() : "")
                .padding(.horizontal, 24)
                // Under the bar with the close button and the count.
                .padding(.top, 56)
            }
        }
        .task(id: Ask(id: "\(image.seq)/\(image.eventImageIndex)/\(image.imageID)", attempt: attempt, online: online)) {
            // A copy already here is not asked for again when the connection comes and goes.
            if sharp != nil, big == .ready { return }
            if let held = loader.held(image.imageID) {
                (big, sharp) = (.ready, held.image)
                return
            }
            big = .loading
            let got = await loader.load(image, previewImage: previewImage, online: online)
            if Task.isCancelled { return }
            sharp = got.image?.image
            big = got.state
        }
        .onChange(of: pokes.zoom) { if current { zoomToggles += 1 } }
        .onChange(of: pokes.retry) { if current { attempt += 1 } }
    }
}

/// Counts what scripts ask of the page on screen, which they cannot tap.
@MainActor
@Observable
final class ViewerPokes {
    var zoom = 0
    var retry = 0
    var next = 0
    var save = 0
    var share = 0
}

/// A picture on its way out of the app.
struct LeavingImage: Identifiable {
    let id = UUID()
    let bytes: Data
    let format: PBImageFormat
    let name: String

    var type: UTType {
        switch format {
        case .png: .png
        case .webp: .webP
        case .gif: .gif
        default: .jpeg
        }
    }
}

/// The user did not let the app add to the photo library.
struct PhotoAccessDenied: Error {}

/// 保存 and 分享: the display copy (or the preview when that is all there
/// is) leaves the app here, by the user's choice (docs/session-images.md
/// 「保存和分享」).
enum ImageLeaving {
    /// What would leave for an image now: the display copy if it was loaded, else the preview; nil without either.
    @MainActor
    static func picture(_ image: PBSessionViewerImageView, previewImage: PBSessionImageView? = nil, loader: ViewerLoader) -> LeavingImage? {
        let name = image.downloadName
        if let held = loader.held(image.imageID) {
            return LeavingImage(bytes: held.bytes, format: held.format, name: name)
        }
        let previewData = previewImage?.preview.isEmpty == false ? previewImage!.preview : image.preview
        if previewData.isEmpty { return nil }
        let previewFormat = previewImage?.preview.isEmpty == false ? previewImage!.previewFormat : image.previewFormat
        return LeavingImage(bytes: previewData, format: previewFormat, name: name)
    }

    /// Adds the picture to the photo library, with the access that only adds: the app never reads the library.
    static func save(_ picture: LeavingImage) async throws {
        let status = await PHPhotoLibrary.requestAuthorization(for: .addOnly)
        guard status == .authorized || status == .limited else { throw PhotoAccessDenied() }
        let (bytes, name) = (picture.bytes, picture.name)
        try await PHPhotoLibrary.shared().performChanges {
            let options = PHAssetResourceCreationOptions()
            options.originalFilename = name
            // The bytes as they are: the format the computer encoded, not a copy re-encoded here.
            PHAssetCreationRequest.forAsset().addResource(
                with: .photo, data: bytes, options: options)
        }
    }
}

/// The system's share sheet for one picture, handed over as its bytes: no file of it is written by this app.
struct ShareSheet: UIViewControllerRepresentable {
    let picture: LeavingImage

    func makeUIViewController(context: Context) -> UIActivityViewController {
        let provider = NSItemProvider()
        provider.suggestedName = picture.name
        let bytes = picture.bytes
        provider.registerDataRepresentation(
            forTypeIdentifier: picture.type.identifier, visibility: .all
        ) { done in
            done(bytes, nil)
            return nil
        }
        return UIActivityViewController(activityItems: [provider], applicationActivities: nil)
    }

    func updateUIViewController(_ controller: UIActivityViewController, context: Context) {}
}
