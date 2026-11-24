// Launch arguments and signals for working on images without a finger on
// the screen (see App/Launch.swift). Debug builds only.
import RACore
import RAProto
import SwiftUI
import UIKit

#if DEBUG
@MainActor
enum ImageDebug {
    private static let args = ProcessInfo.processInfo.arguments

    private static func value(_ name: String) -> String? {
        guard let i = args.firstIndex(of: name), i + 1 < args.count, !args[i + 1].hasPrefix("-") else { return nil }
        return args[i + 1]
    }

    private static var added = false
    private static var filesAdded = false

    static func documentURLs() -> [URL] {
        guard !filesAdded, let paths = value("-fileAdd") else { return [] }
        filesAdded = true
        return paths.split(separator: ",").map { URL(fileURLWithPath: String($0)) }
    }

    static func launchFiles() -> [Data] {
        guard !added, let paths = value("-imageAdd") else { return [] }
        added = true
        return files(paths)
    }

    static func files(_ paths: String) -> [Data] {
        paths.split(whereSeparator: { $0 == "," || $0 == "\n" }).compactMap { path in
            let data = try? Data(contentsOf: URL(fileURLWithPath: String(path)))
            // Log size only; never log image content.
            print("IMAGES add \(data.map { "\($0.count) bytes" } ?? "unreadable")")
            return data
        }
    }

    static func viewerSample(_ image: PBSessionViewerImageView, previewRef: PBImageRef?, center: ImageCenter, online: Bool) async -> (state: BigImage, image: ViewedImage?)? {
        guard !center.live else { return nil }
        switch value("-viewerState") ?? (image.hasDisplayCopy_p ? "ready" : "notSynced") {
        case "loading":
            try? await Task.sleep(for: .seconds(3600))
            return (.loading, nil)
        case "notSynced": return (.notSynced, nil)
        case "expired": return (.expired, nil)
        case "offline": return (.offline, nil)
        case "failed": return (.failed(why: "服务器没有回应"), nil)
        case "corrupt": return (.corrupt(why: "图片内容和格式对不上"), nil)
        default:
            try? await Task.sleep(for: .milliseconds(300))
            return (.ready, sharp(previewRef ?? image.ref))
        }
    }

    private static func sharp(_ ref: PBImageRef) -> ViewedImage? {
        guard let preview = UIImage(data: ref.preview) else { return nil }
        let format = UIGraphicsImageRendererFormat()
        format.scale = 1
        let size = CGSize(width: preview.size.width * 8, height: preview.size.height * 8)
        let image = UIGraphicsImageRenderer(size: size, format: format).image { ctx in
            preview.draw(in: CGRect(origin: .zero, size: size))
            ctx.cgContext.setStrokeColor(UIColor.white.withAlphaComponent(0.5).cgColor)
            ctx.cgContext.setLineWidth(1)
            for x in stride(from: CGFloat(0), through: size.width, by: 40) { ctx.cgContext.stroke(CGRect(x: x, y: 0, width: 0, height: size.height)) }
            for y in stride(from: CGFloat(0), through: size.height, by: 40) { ctx.cgContext.stroke(CGRect(x: 0, y: y, width: size.width, height: 0)) }
            let text = "显示版 \(Int(size.width)) × \(Int(size.height))" as NSString
            text.draw(at: CGPoint(x: 24, y: 24), withAttributes: [.font: UIFont.monospacedSystemFont(ofSize: 28, weight: .medium), .foregroundColor: UIColor.white])
        }
        guard let bytes = image.pngData() else { return nil }
        return ViewedImage(image: image, bytes: bytes, format: .png)
    }
}

private struct ImageDraftDebug: ViewModifier {
    let attachments: ComposerAttachments

    func body(content: Content) -> some View {
        let scope = attachments.scope
        let center = attachments.center
        let host = attachments.host
        let draft = attachments.draft
        content
            .task(id: scope.debugKey) {
                let documents = ImageDebug.documentURLs()
                if !documents.isEmpty { center.addFiles(scope: scope, documents) }
                let files = ImageDebug.launchFiles()
                if !files.isEmpty { center.addImageData(scope: scope, files) }
            }
            .debugSignal("files.add") {
                let paths = UserDefaults.standard.string(forKey: "debug.filePaths") ?? ""
                let urls = paths.split(separator: ",").map { URL(fileURLWithPath: String($0)) }
                if !urls.isEmpty { center.addFiles(scope: scope, urls) }
            }
            .debugSignal("images.add") {
                let files = ImageDebug.files(UserDefaults.standard.string(forKey: "debug.imagePaths") ?? "")
                if !files.isEmpty { center.addImageData(scope: scope, files) }
            }
            .debugSignal("images.pick") { center.plus(scope: scope, draft: draft) }
            .debugSignal("images.original") {
                Task { await host.setDraftOriginal(scope: scope, original: !draft.original) }
            }
            .debugSignal("images.retry") {
                for item in draft.items where item.canRetry {
                    let id = item.itemID
                    Task { await host.retryDraftItem(scope: scope, itemID: id) }
                }
            }
            .debugSignal("images.remove") {
                if let first = draft.items.first {
                    let id = first.itemID
                    Task { await host.removeDraftItem(scope: scope, itemID: id) }
                }
            }
    }
}

extension View {
    func imageDraftDebug(_ attachments: ComposerAttachments) -> some View {
        modifier(ImageDraftDebug(attachments: attachments))
    }
}
#else
extension View {
    func imageDraftDebug(_ attachments: ComposerAttachments) -> some View { self }
}
#endif
