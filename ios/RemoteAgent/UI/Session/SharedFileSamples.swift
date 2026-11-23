// Sample data for looking at the cards of shared files without a computer to
// download from (debug builds only; see App/Launch.swift):
//
//   -demo -demoRoute session/sample-files        the mockup's chat: one video
//                                                (sample-files-states: the six states, sample-files-multi:
//                                                three files, sample-files-kinds: every colour of tile,
//                                                sample-files-plain: a computer that does not hand out files)
//   -sharedFileCards <id=state,…>                a card in a state: available, downloading, downloadingEta, paused,
//                                                downloaded, failed, offline, cleaned (the ids are the files', below)
//   -sharedFileMenu <id>                         the long-press menu of that card, opened once it is on screen
//   -sharedFileFake                              the sample session sample-files-live downloads for real, from a fake computer
//   sharedfile.tap.<id>, sharedfile.menu.<id>    a tap, or the long press, on that file's card (signals, see App/Launch.swift);
//   sharedfile.quiet.<id> downloads it as if the page had been left, sharedfile.photos.<id> saves its copy to Photos
#if DEBUG
import CryptoKit
import Foundation
import RACore
import RAProto
import UIKit

enum SharedFileDebug {
    private static let args = ProcessInfo.processInfo.arguments

    private static func value(_ name: String) -> String? {
        guard let i = args.firstIndex(of: name), i + 1 < args.count else { return nil }
        return args[i + 1]
    }

    /// `-sharedFileCards id=state`, where a downloading or paused state may say how far: `downloading:0.19`.
    private static let forced: [String: String] = {
        var out: [String: String] = [:]
        for pair in (value("-sharedFileCards") ?? "").split(separator: ",") {
            let parts = pair.split(separator: "=", maxSplits: 1).map(String.init)
            if parts.count == 2 { out[parts[0]] = parts[1] }
        }
        return out
    }()

    static var hasForcedStates: Bool { !forced.isEmpty }

    /// Produces a `PBSharedFileLocalStateEvent` driving Core into each forced debug state for demo sessions.
    static func localStateEvent(for sessionEvents: [String: [PBEvent]]) -> PBSharedFileLocalStateEvent? {
        guard !forced.isEmpty else { return nil }
        var updates: [PBSharedFileLocalStateUpdate] = []
        for (sid, events) in sessionEvents {
            for ev in events {
                guard case .toolCall(let tc)? = ev.payload else { continue }
                for f in tc.sharedFiles {
                    guard let word = forced[f.fileID] else { continue }
                    let parts = word.split(separator: ":").map(String.init)
                    let total = f.sizeBytes
                    let done = Int64(Double(total) * (parts.count > 1 ? Double(parts[1]) ?? 0.33 : 0.33))
                    var u = PBSharedFileLocalStateUpdate()
                    u.sessionID = sid
                    u.fileID = f.fileID
                    switch parts[0] {
                    case "available":
                        u.status = .unspecified
                    case "downloading":
                        u.status = .downloading
                        u.downloadedBytes = done
                    case "downloadingEta":
                        u.status = .downloading
                        u.downloadedBytes = done
                        u.hasEta_p = true
                        u.etaSeconds = 12
                    case "paused":
                        u.status = .paused
                        u.downloadedBytes = done
                    case "downloaded":
                        u.status = .downloaded
                        u.hasDuration_p = true
                        u.durationMs = 102_000
                    case "downloadedNoDuration":
                        u.status = .downloaded
                    case "failed":
                        u.status = .failed
                        u.failureKind = .disconnected
                    case "failedChecksum":
                        u.status = .failed
                        u.failureKind = .checksumMismatch
                    case "failedFull":
                        u.status = .failed
                        u.failureKind = .phoneStorageFull
                    case "cleaned":
                        u.status = .cleanedUp
                    default:
                        continue
                    }
                    updates.append(u)
                }
            }
        }
        guard !updates.isEmpty else { return nil }
        return .with {
            $0.replaceAll = true
            $0.updates = updates
        }
    }

    /// Demo-only per-card offline override when an online demo session (`sample-files-states`) displays
    /// both online and offline cards side by side via `-sharedFileCards id=offline`.
    static func overrideItem(_ item: PBSessionSharedFileItem) -> PBSessionSharedFileItem {
        guard forced[item.fileID] == "offline" else { return item }
        var copy = item
        copy.status = .offline
        copy.subtitle = .with { $0.key = "shared_file.subtitle_offline" }
        copy.subtitleIsError = false
        copy.primaryAction = .download
        copy.primaryLabel = .with { $0.key = "shared_file.action_download" }
        copy.primaryEnabled = false
        copy.primaryDisabledReason = .with { $0.key = "shared_file.subtitle_offline" }
        copy.menuItems = []
        copy.a11YLabel = .with {
            $0.key = "shared_file.a11y_card"
            $0.args = [
                .with { $0.name = "name"; $0.strValue = item.displayName },
                .with { $0.name = "kind"; $0.textValue = item.kindLabel },
                .with { $0.name = "size"; $0.textValue = item.sizeText },
                .with { $0.name = "state"; $0.textValue = .with { $0.key = "shared_file.subtitle_offline" } },
            ]
        }
        return copy
    }

    private static let menuFor = value("-sharedFileMenu")

    @MainActor
    static func autoMenu(for item: PBSessionSharedFileItem, open: @MainActor () -> Void) async {
        guard menuFor == item.fileID else { return }
        try? await Task.sleep(nanoseconds: 2_500_000_000)
        open()
    }

    /// Pictures like those of the mockup's `.thumb` (a video) and `.thumb2` (a picture), drawn here.
    @MainActor
    static func thumbnail(for kind: PBSharedFileKind, name: String) -> UIImage? {
        let (colors, locations, from, to): ([UIColor], [CGFloat], CGPoint, CGPoint)
        switch kind {
        case .video:
            colors = [0x27405C, 0x5D7F9C, 0xE7C08A, 0xB9703F].map(UIColor.init(hex:))
            (locations, from, to) = ([0, 0.38, 0.62, 1], CGPoint(x: 0, y: 0), CGPoint(x: 1, y: 1))
        case .image:
            colors = [0xF3D9B1, 0xC98B5E, 0x6B7F93].map(UIColor.init(hex:))
            (locations, from, to) = ([0, 0.45, 1], CGPoint(x: 0.34, y: 0), CGPoint(x: 0.66, y: 1))
        default: return nil
        }
        let size = CGSize(width: 132, height: 132)
        return UIGraphicsImageRenderer(size: size).image { ctx in
            let gradient = CGGradient(colorsSpace: CGColorSpaceCreateDeviceRGB(), colors: colors.map(\.cgColor) as CFArray, locations: locations)!
            ctx.cgContext.drawLinearGradient(gradient, start: CGPoint(x: from.x * size.width, y: from.y * size.height), end: CGPoint(x: to.x * size.width, y: to.y * size.height), options: [.drawsAfterEndLocation, .drawsBeforeStartLocation])
        }
    }
}

// MARK: A computer that is not there

extension SharedFileDebug {
    /// `-sharedFileFake`: the sample session's files are really downloaded, from a fake computer in this process, one
    /// block at a time and slowly, so that the whole path (blocks, check, copy, thumbnail, Quick Look) can be watched.
    static let fake = args.contains("-sharedFileFake")

    struct Fake: Sendable {
        let id: String
        let name: String
        let data: Data
        let path: String
        var sha256: Data { Data(SHA256.hash(data: data)) }
    }

    /// A picture that is a real PNG, a PDF that is a real PDF, and 12 MB that is only bytes.
    @MainActor
    static let fakes: [Fake] = {
        let size = CGSize(width: 600, height: 400)
        let png = UIGraphicsImageRenderer(size: size).image { ctx in
            let colors = [UIColor(red: 0.95, green: 0.85, blue: 0.69, alpha: 1), UIColor(red: 0.79, green: 0.55, blue: 0.37, alpha: 1), UIColor(red: 0.42, green: 0.5, blue: 0.58, alpha: 1)]
            let gradient = CGGradient(colorsSpace: CGColorSpaceCreateDeviceRGB(), colors: colors.map(\.cgColor) as CFArray, locations: [0, 0.45, 1])!
            ctx.cgContext.drawLinearGradient(gradient, start: .zero, end: CGPoint(x: size.width, y: size.height), options: [])
        }.pngData() ?? Data()
        let pdf = UIGraphicsPDFRenderer(bounds: CGRect(x: 0, y: 0, width: 612, height: 792)).pdfData { ctx in
            ctx.beginPage()
            ("季度报告 Quarterly report" as NSString).draw(at: CGPoint(x: 72, y: 72), withAttributes: [.font: UIFont.systemFont(ofSize: 28)])
        }
        let bytes = Data((0..<12_000_000).map { UInt8(truncatingIfNeeded: $0 &* 31 &+ ($0 >> 8)) })
        let home = "/Users/me"
        return [
            Fake(id: "x-png", name: "cover.png", data: png, path: home + "/Movies/daocheng/cover.png"),
            Fake(id: "x-pdf", name: "季度报告.pdf", data: pdf, path: home + "/Documents/季度报告.pdf"),
            Fake(id: "x-zip", name: "project.zip", data: bytes, path: home + "/Movies/daocheng/project.zip"),
        ]
    }()

    /// Serves `fakes`, a block every 700 ms.
    struct FakeComputer: SharedFileFetching {
        let files: [String: Data]
        let blockBytes: Int

        func fetchChunk(sessionId: String, fileId: String, offset: Int64) async throws -> PBGetSharedFileChunkResponse {
            try await Task.sleep(nanoseconds: 700_000_000)
            guard let data = files[fileId] else { throw CommandError("not_found", "no such file", fromDaemon: true, readOnly: true, outcome: .executed) }
            var block = Data(count: blockBytes)
            let from = Int(offset), take = max(0, min(blockBytes, data.count - from))
            if take > 0 { block.replaceSubrange(0..<take, with: data[from..<from + take]) }
            return .with { $0.data = block; $0.totalBytes = UInt64(data.count) }
        }
    }

    @MainActor
    static func fakeCenter(
        root: URL,
        report: @escaping @Sendable (PBSharedFileLocalStateEvent) async -> Void = { _ in }
    ) -> SharedFileCenter {
        let files = Dictionary(uniqueKeysWithValues: fakes.map { ($0.id, $0.data) })
        let store = SharedFileStore(root: root, fetcher: FakeComputer(files: files, blockBytes: defaultSharedFileBlockBytes), isMachineOnline: { _ in true })
        return SharedFileCenter(store: store, report: report)
    }
}

private extension UIColor {
    convenience init(hex: UInt32) {
        self.init(red: CGFloat(hex >> 16 & 0xff) / 255, green: CGFloat(hex >> 8 & 0xff) / 255, blue: CGFloat(hex & 0xff) / 255, alpha: 1)
    }
}
#endif
