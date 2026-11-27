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

private enum ForcedCardState: String {
    case available
    case downloading
    case downloadingEta
    case paused
    case downloaded
    case downloadedNoDuration
    case failed
    case failedChecksum
    case failedFull
    case offline
    case cleaned
}

private enum CardArgName {
    static let name = "name"
    static let kind = "kind"
    static let size = "size"
    static let state = "state"
    static let count = "count"
    static let downloaded = "downloaded"
    static let total = "total"
    static let eta = "eta"
    static let duration = "duration"
    static let reason = "reason"
}

enum SharedFileDebug {
    private static let args = ProcessInfo.processInfo.arguments

    private static func value(_ name: String) -> String? {
        guard let i = args.firstIndex(of: name), i + 1 < args.count else { return nil }
        return args[i + 1]
    }

    /// `-sharedFileCards id=state`, where a downloading or paused state may say how far: `downloading:0.19`.
    private static let forced: [String: (ForcedCardState, Double)] = {
        var out: [String: (ForcedCardState, Double)] = [:]
        for pair in (value("-sharedFileCards") ?? "").split(separator: ",") {
            let parts = pair.split(separator: "=", maxSplits: 1).map(String.init)
            guard parts.count == 2 else { continue }
            let spec = parts[1].split(separator: ":").map(String.init)
            guard let state = ForcedCardState(rawValue: spec[0]) else { continue }
            let ratio = spec.count > 1 ? min(1, max(0, Double(spec[1]) ?? 0.33)) : 0.33
            out[parts[0]] = (state, ratio)
        }
        return out
    }()

    static var hasForcedStates: Bool { !forced.isEmpty }

    private static func t(_ key: String, _ args: [(String, PBCoreTextArg)] = []) -> PBCoreText {
        .with {
            $0.key = key
            $0.args = args.map { name, val in
                var a = val
                a.name = name
                return a
            }
        }
    }

    private static func menuItem(
        _ action: PBSharedFileMenuAction,
        label: PBCoreText,
        destructive: Bool = false,
        separated: Bool = false
    ) -> PBSharedFileMenuItemView {
        .with {
            $0.action = action
            $0.label = label
            $0.enabled = true
            $0.destructive = destructive
            $0.separated = separated
        }
    }

    private static func cardA11y(_ item: PBSessionSharedFileItem, state: PBCoreText) -> PBCoreText {
        t("shared_file.a11y_card", [
            (CardArgName.name, .with { $0.strValue = item.displayName }),
            (CardArgName.kind, .with { $0.textValue = item.kindLabel }),
            (CardArgName.size, .with { $0.textValue = item.sizeText }),
            (CardArgName.state, .with { $0.textValue = state }),
        ])
    }

    /// Demo-only per-card ViewModel override for `-sharedFileCards id=state` (UI tests & visual previews).
    static func overrideItem(_ item: PBSessionSharedFileItem) -> PBSessionSharedFileItem {
        guard let (state, ratio) = forced[item.fileID] else { return item }
        let doneBytes = Int64(Double(item.sizeBytes) * ratio)
        let doneText = imageBytes(doneBytes)

        var copy = item
        copy.subtitleIsError = false
        copy.primaryDisabledReason = PBCoreText()
        switch state {
        case .available:
            return item
        case .downloading, .downloadingEta:
            copy.status = .downloading
            copy.progress = ratio
            if state == .downloadingEta {
                let eta = t("shared_file.eta_seconds", [(CardArgName.count, .with { $0.intValue = 12 })])
                copy.subtitle = t("shared_file.subtitle_downloading_eta", [
                    (CardArgName.downloaded, .with { $0.strValue = doneText }),
                    (CardArgName.total, .with { $0.textValue = item.sizeText }),
                    (CardArgName.eta, .with { $0.textValue = eta }),
                ])
            } else {
                copy.subtitle = t("shared_file.subtitle_downloading", [
                    (CardArgName.downloaded, .with { $0.strValue = doneText }),
                    (CardArgName.total, .with { $0.textValue = item.sizeText }),
                ])
            }
            copy.primaryAction = .pause
            copy.primaryLabel = t("shared_file.action_pause")
            copy.primaryEnabled = true
            copy.menuItems = [
                menuItem(.pause, label: t("shared_file.action_pause")),
                menuItem(.cancelDownload, label: t("shared_file.action_cancel_download"), destructive: true, separated: true),
            ]
            copy.a11YLabel = cardA11y(item, state: t("shared_file.a11y_state_downloading"))
        case .paused:
            copy.status = .paused
            copy.progress = ratio
            copy.subtitle = t("shared_file.subtitle_paused", [
                (CardArgName.downloaded, .with { $0.strValue = doneText }),
                (CardArgName.total, .with { $0.textValue = item.sizeText }),
            ])
            copy.primaryAction = .resume
            copy.primaryLabel = t("shared_file.action_resume")
            copy.primaryEnabled = true
            copy.menuItems = [
                menuItem(.resume, label: t("shared_file.action_resume")),
                menuItem(.cancelDownload, label: t("shared_file.action_cancel_download"), destructive: true, separated: true),
            ]
            copy.a11YLabel = cardA11y(item, state: t("shared_file.a11y_state_paused"))
        case .downloaded, .downloadedNoDuration:
            copy.status = .downloaded
            copy.progress = 1
            if state == .downloaded, item.kind == .video || item.kind == .audio {
                copy.subtitle = t("shared_file.subtitle_media_size", [
                    (CardArgName.duration, .with { $0.strValue = "01:42" }),
                    (CardArgName.size, .with { $0.textValue = item.sizeText }),
                ])
            }
            copy.primaryAction = .open
            copy.primaryLabel = t("shared_file.action_open")
            copy.primaryEnabled = true
            var menu: [PBSharedFileMenuItemView] = []
            if item.kind == .image || item.kind == .video {
                menu.append(menuItem(.saveToAlbum, label: t("shared_file.menu_save_to_album")))
            }
            menu.append(menuItem(.save, label: t("shared_file.menu_save_to_device")))
            menu.append(menuItem(.share, label: t("shared_file.menu_share")))
            menu.append(menuItem(.openWith, label: t("shared_file.menu_open_with")))
            menu.append(menuItem(.deleteLocal, label: t("shared_file.menu_delete_local"), destructive: true, separated: true))
            copy.menuItems = menu
            copy.a11YLabel = cardA11y(item, state: t("shared_file.a11y_state_downloaded"))
        case .failed, .failedChecksum, .failedFull:
            let reasonText: PBCoreText = switch state {
            case .failedChecksum: t("shared_file.reason_checksum_mismatch")
            case .failedFull: t("shared_file.reason_disk_full")
            default: t("shared_file.reason_disconnected")
            }
            let sub = t("shared_file.subtitle_failed", [(CardArgName.reason, .with { $0.textValue = reasonText })])
            copy.status = .failed
            copy.progress = 0
            copy.subtitle = sub
            copy.subtitleIsError = true
            copy.primaryAction = .retry
            copy.primaryLabel = t("shared_file.action_retry")
            copy.primaryEnabled = true
            copy.menuItems = [
                menuItem(.retry, label: t("shared_file.action_retry")),
            ]
            copy.a11YLabel = cardA11y(item, state: sub)
        case .offline:
            copy.status = .offline
            copy.subtitle = t("shared_file.subtitle_offline")
            copy.primaryAction = .download
            copy.primaryLabel = t("shared_file.action_download")
            copy.primaryEnabled = false
            copy.primaryDisabledReason = t("shared_file.subtitle_offline")
            copy.menuItems = []
            copy.a11YLabel = cardA11y(item, state: t("shared_file.subtitle_offline"))
        case .cleaned:
            copy.status = .cleaned
            copy.subtitle = t("shared_file.subtitle_cleaned")
            copy.primaryAction = .unspecified
            copy.primaryLabel = PBCoreText()
            copy.primaryEnabled = false
            copy.menuItems = []
            copy.a11YLabel = cardA11y(item, state: t("shared_file.subtitle_cleaned"))
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
    /// `-sharedFileFake`: the sample session's files are really downloaded through Core's download
    /// state machine from an in-process fake computer, one block every 700 ms.
    static let fake = args.contains("-sharedFileFake")

    static let demoBlockBytes = 786_432

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

    /// Builds a `CoreHost.DemoCommandAction` serving `fakes` one block every 700 ms.
    static func demoChunkAction(
        effect: PBCoreDispatchCommandEffect,
        request: PBGetSharedFileChunkRequest,
        files: [String: Data]
    ) -> CoreHost.DemoCommandAction {
        let blockBytes = demoBlockBytes
        return .asyncRespond {
            try? await Task.sleep(nanoseconds: 700_000_000)
            guard let data = files[request.fileID] else {
                var result = PBCommandResult()
                result.outcome = "executed"
                result.reason = "ok"
                result.resultAvailable = true
                result.errorCode = "not_found"
                result.errorMessage = "no such file"
                return .with {
                    $0.requestID = effect.requestID
                    $0.attemptID = effect.attemptID
                    $0.source = .daemon
                    $0.attemptBound = true
                    $0.hashesMatch = true
                    $0.outerMatchesInner = true
                    $0.signed = true
                    $0.outerOutcome = "executed"
                    $0.outerReason = "ok"
                    $0.rawResult = (try? result.serializedData()) ?? Data()
                }
            }
            var block = Data(count: blockBytes)
            let from = Int(request.offset)
            let take = max(0, min(blockBytes, data.count - from))
            if take > 0 { block.replaceSubrange(0..<take, with: data[from..<from + take]) }
            return CoreHost.demoExecutedOutcome(for: effect) {
                $0.getSharedFileChunk = .with {
                    $0.data = block
                    $0.totalBytes = UInt64(data.count)
                }
            }
        }
    }
}

private extension UIColor {
    convenience init(hex: UInt32) {
        self.init(red: CGFloat(hex >> 16 & 0xff) / 255, green: CGFloat(hex >> 8 & 0xff) / 255, blue: CGFloat(hex & 0xff) / 255, alpha: 1)
    }
}
#endif
