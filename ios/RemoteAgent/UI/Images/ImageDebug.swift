// Launch arguments and signals for working on images without a finger on
// the screen (see App/Launch.swift). Debug builds only.
//
//   -imageAdd <path[,path…]>      once a message box is up: feeds these files through what the picker's
//                                 results go through (admit, prepare, upload). A simulator reads the Mac's files.
//   -imageDraft <state>           with -demo: the strip in a fixed state: ready, crowded, uploading,
//                                 failed, original, full, notice
//   -imageOutgoing                with -demo: a message with images on its way, in the session shown
//   -viewerState <state>          with -demo: the viewer's display copy: loading, ready, notSynced,
//                                 expired, offline, failed, corrupt
//
// Signals: images.add (the paths in `defaults write <bundle> debug.imagePaths`), images.pick (what ＋ does),
// images.original, images.retry, images.remove, images.expire; viewer.close, viewer.next, viewer.zoom, viewer.retry,
// viewer.save, viewer.share.
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

    /// The files `-imageAdd` names, once.
    static func launchFiles() -> [Data] {
        guard !added, let paths = value("-imageAdd") else { return [] }
        added = true
        return files(paths)
    }

    static func files(_ paths: String) -> [Data] {
        paths.split(whereSeparator: { $0 == "," || $0 == "\n" }).compactMap { path in
            let data = try? Data(contentsOf: URL(fileURLWithPath: String(path)))
            // The length only: what is in a picture is nobody's to log.
            print("IMAGES add \(data.map { "\($0.count) bytes" } ?? "unreadable")")
            return data
        }
    }

    /// A picture drawn here, as a prepared image would hold it.
    private static func picture(_ hue: CGFloat, w: Int = 240, h: Int = 180) -> Data {
        let format = UIGraphicsImageRendererFormat()
        format.scale = 1
        let size = CGSize(width: w, height: h)
        return UIGraphicsImageRenderer(size: size, format: format).image { ctx in
            let colors = [UIColor(hue: hue, saturation: 0.45, brightness: 0.95, alpha: 1), UIColor(hue: hue + 0.1, saturation: 0.7, brightness: 0.65, alpha: 1)]
            let gradient = CGGradient(colorsSpace: nil, colors: colors.map(\.cgColor) as CFArray, locations: nil)!
            ctx.cgContext.drawLinearGradient(gradient, start: .zero, end: CGPoint(x: size.width, y: size.height), options: [])
            UIColor.white.withAlphaComponent(0.85).setFill()
            ctx.cgContext.fillEllipse(in: CGRect(x: size.width * 0.55, y: size.height * 0.2, width: size.width * 0.25, height: size.width * 0.25))
        }.pngData() ?? Data()
    }

    private static func sample(_ n: Int, _ state: PendingImage.State, note: String = "", megabytes: Int = 0) -> PendingImage {
        let img = PendingImage(id: "sample-\(n)", uri: "sample:\(n)")
        var bytes = picture(CGFloat(n) * 0.14 + 0.05, w: n % 2 == 0 ? 240 : 160, h: n % 2 == 0 ? 180 : 260)
        // Padding after the picture: only so that the strip has megabytes to write.
        if megabytes > 0 { bytes.append(Data(count: megabytes * 1024 * 1024)) }
        img.processed = Processed(bytes: bytes, format: .png, width: 240, height: 180, note: note)
        img.state = state
        if state == .ready {
            img.machineId = SessionSamples.machine
            img.imageId = "sample-image-\(n)"
            img.expiresMs = Int64.max
        }
        return img
    }

    /// The strip in the state `-imageDraft` names, for looking at it with sample data.
    static func seed(_ center: ImageCenter, key: String, machine: String?) {
        guard !center.live, let state = value("-imageDraft") else { return }
        center.frozen = true
        func ready(_ n: Int) -> PendingImage {
            let img = sample(n, .ready)
            img.machineId = machine
            return img
        }
        switch state {
        case "crowded":
            center.debugShow(
                key, [ready(1), ready(2), sample(3, .uploading(progress: 0.4)), sample(4, .processing), sample(5, .failed(error: "电脑上传目录已满")), ready(6)],
                notice: "一条消息最多 6 张，多选的 2 张没有加入")
        case "uploading":
            center.debugShow(key, [ready(1), sample(2, .uploading(progress: 0.6)), sample(3, .processing)])
        case "failed":
            center.debugShow(key, [ready(1), sample(2, .failed(error: "MacBook 离线，指令没有发出"))])
        case "original":
            center.debugShow(
                key,
                [
                    sample(1, .uploading(progress: 0.35), note: "按拍摄方向转正后以最高画质重新编码", megabytes: 6),
                    sample(2, .uploading(progress: 0.8), note: "转成了 JPEG，保留原始尺寸", megabytes: 4), ready(3),
                ], original: true)
        case "full":
            center.debugShow(key, (1...6).map(ready))
        case "notice":
            center.say(key, "MacBook 离线，连上后才能添加图片")
        default:
            center.debugShow(key, [ready(1), ready(2), ready(3)])
        }
    }

    /// A message with images on its way, for `-imageOutgoing`.
    static func outgoing(_ center: ImageCenter, sid: String, store: SessionStore) {
        guard !center.live, args.contains("-imageOutgoing") else { return }
        func message(_ id: String, _ text: String, _ state: Outgoing.State, images: [Int], error: String = "") -> Outgoing {
            let o = Outgoing(requestId: id, text: text, imageIds: images.map { "sample-image-\($0)" }, images: images.map { sample($0 + 10, .ready) })
            o.state = state
            o.error = error
            return o
        }
        center.debugShow(sid, message("img-out-1", "这两个页面底部的按钮都点不到", .sending, images: [1, 2]), store: store)
        center.debugShow(sid, message("img-out-2", "", .failed, images: [3, 4, 5, 6, 7], error: "电脑不在线，指令没有发出"), store: store)
    }

    /// With sample data there is no server to fetch a display copy from: the state asked for, or what the event's sync says.
    static func viewerSample(_ ref: PBImageRef, center: ImageCenter, online: Bool) async -> (state: BigImage, image: ViewedImage?)? {
        guard !center.live else { return nil }
        switch value("-viewerState") ?? (hasDisplayCopy(ref) ? "ready" : "notSynced") {
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
            return (.ready, sharp(ref))
        }
    }

    /// The preview drawn large with fine lines over it, so that the display copy can be told from the preview it replaces.
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

/// What a script can do to the draft of the message box on screen.
private struct ImageDraftDebug: ViewModifier {
    let center: ImageCenter
    let key: String
    let machine: String?
    let blocked: String?
    let max: Int
    var session: (id: String, store: SessionStore)?
    var fileMachine: MachineView?

    func body(content: Content) -> some View {
        content
            .task(id: key) {
                ImageDebug.seed(center, key: key, machine: machine)
                if let session { ImageDebug.outgoing(center, sid: session.id, store: session.store) }
                let documents = ImageDebug.documentURLs()
                if !documents.isEmpty { center.addFiles(key, documents, machine: fileMachine, online: blocked == nil) }
                let files = ImageDebug.launchFiles()
                if !files.isEmpty { center.add(key, files.map { .data($0) }, max: max) }
            }
            .debugSignal("files.add") {
                let paths = UserDefaults.standard.string(forKey: "debug.filePaths") ?? ""
                let urls = paths.split(separator: ",").map { URL(fileURLWithPath: String($0)) }
                center.debugAddFiles(key, urls)
            }
            .debugSignal("images.add") {
                let files = ImageDebug.files(UserDefaults.standard.string(forKey: "debug.imagePaths") ?? "")
                if !files.isEmpty { center.add(key, files.map { .data($0) }, max: max) }
            }
            .debugSignal("images.pick") { center.plus(key, blocked: blocked, max: max, filesSupported: fileSupported(fileMachine)) }
            .debugSignal("images.original") { center.setOriginal(key, !center.draft(key).original) }
            .debugSignal("images.retry") {
                for img in center.draft(key).images {
                    if case .failed = img.state { center.retry(key, img) }
                }
            }
            .debugSignal("images.expire") {
                // Forces the client deadline without changing the daemon's retention policy.
                for img in center.draft(key).images { img.expiresMs = 0 }
                print("IMAGES expiry forced")
            }
            .debugSignal("images.remove") {
                if let first = center.draft(key).images.first { center.remove(key, first) }
            }
            .onChange(of: center.draft(key).images.map(\.state)) { _, states in
                // IDs and states only, for a script to wait on.
                print("IMAGES \(key.prefix(8)) \(states.map { "\($0)".prefix(24) }.joined(separator: " | "))")
            }
    }
}

extension View {
    func sessionImageDebug(_ context: SessionImageContext) -> some View {
        modifier(ImageDraftDebug(
            center: ImageCenter.of(context.remote), key: ImageCenter.sessionKey(context.sessionId), machine: context.machineId,
            blocked: context.blocked, max: context.limit, session: (context.sessionId, context.store), fileMachine: context.machine))
    }

    func projectImageDebug(_ context: ProjectImageContext) -> some View {
        modifier(ImageDraftDebug(
            center: ImageCenter.of(context.remote), key: ImageCenter.projectKey(context.key), machine: context.machineId,
            blocked: context.blocked, max: context.limit, fileMachine: context.machine))
    }
}
#else
extension View {
    func sessionImageDebug(_ context: SessionImageContext) -> some View { self }
    func projectImageDebug(_ context: ProjectImageContext) -> some View { self }
}
#endif
