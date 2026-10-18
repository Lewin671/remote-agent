// What the image viewer says and does, apart from drawing: where a display
// copy stands, the words for each state, which image it opens on, and the
// names a picture leaves the app under. Android's ui/Viewer.kt.
import Foundation
import RAProto

/// Where a display copy stands while the viewer shows an image.
public enum BigImage: Equatable, Sendable {
    case loading
    case ready
    case unverifiedLegacy
    /// The event says there is no display copy.
    case notSynced
    case expired
    /// Never fetched and no connection to fetch it.
    case offline
    case failed(why: String)
    case corrupt(why: String)
}

/// What the viewer writes over the picture for a state; nil while there is nothing to say.
public func viewerNote(_ big: BigImage, sync: PBImageSync) -> String? {
    switch big {
    case .loading, .ready: return nil
    case .unverifiedLegacy: return "旧图片的内容和来源未验证，仅供阅读"
    case .notSynced: return sync == .nameOnly ? "图片没有同步：只有文件名，原图在电脑上" : "只有预览，原图在电脑上"
    case .expired: return "大图已过期，只剩预览"
    case .offline: return "连上后加载"
    case .failed(let why): return "没能下载：\(why) · 点按重试"
    case .corrupt(let why): return "图片校验失败：\(why)"
    }
}

/// A tap on the note asks for the display copy again.
public func viewerCanRetry(_ big: BigImage) -> Bool {
    switch big {
    case .failed, .offline: return true
    default: return false
    }
}

/// Whether the event says a display copy was uploaded at all.
public func hasDisplayCopy(_ ref: PBImageRef) -> Bool {
    ref.sync == .synced && !ref.imageID.isEmpty
}

/// How fetching a display copy ended, as the viewer's state; the payload when it is there to decode.
public func viewerOutcome(_ result: Result<ImageResult, any Error>, online: Bool) -> (
    state: BigImage, payload: PBImagePayload?
) {
    switch result {
    case .success(.loaded(let payload)): return (.ready, payload)
    case .success(.legacy(let payload)): return (.unverifiedLegacy, payload)
    case .success(.expired): return (.expired, nil)
    case .success(.missing): return (.notSynced, nil)
    case .success(.corrupt(let why)): return (.corrupt(why: why), nil)
    case .failure(let error): return (online ? .failed(why: errorText(error)) : .offline, nil)
    }
}

/// The page the viewer opens on: the `index`-th image of the event at `seq`, or the first when that event has none.
public func viewerStart(_ all: [SessionImage], seq: UInt64, index: Int) -> Int {
    var n = 0
    if let first = all.firstIndex(where: { $0.seq == seq }) { n = first + index }
    return min(max(n, 0), max(all.count - 1, 0))
}

/// Who put the image in the session, in words.
public func viewerSource(_ ref: PBImageRef, agent: PBAgent) -> String {
    switch ref.source {
    case .tool: return "\(agentName(agent)) 查看的图片"
    case .generated: return "\(agentName(agent)) 生成的图片"
    default: return "用户粘贴"
    }
}

/// The size the image had on the computer, "W × H"; nil when the event does not say.
public func viewerSize(_ ref: PBImageRef) -> String? {
    let w = ref.originalWidth > 0 ? ref.originalWidth : ref.width
    let h = ref.originalHeight > 0 ? ref.originalHeight : ref.height
    return w > 0 ? "\(w) × \(h)" : nil
}

/// The file extension of a format; JPEG's for one that is not named.
public func imageExtension(_ f: PBImageFormat) -> String {
    switch f {
    case .png: return "png"
    case .webp: return "webp"
    case .gif: return "gif"
    default: return "jpg"
    }
}

public func imageMime(_ f: PBImageFormat) -> String {
    switch f {
    case .jpeg, .unspecified: return "image/jpeg"
    default: return "image/" + imageExtension(f)
    }
}

/// The name a picture is saved or shared under: the file's own, without its extension, the time, and the format's extension.
public func savedImageName(path: String, format: PBImageFormat, nowMs: Int64 = nowMs()) -> String {
    let name = fileName(path)
    var base = name
    if let dot = name.utf8.lastIndex(of: UInt8(ascii: ".")) { base = String(name[..<dot]) }
    if base.isEmpty { base = "remote-agent" }
    return "\(base)-\(nowMs).\(imageExtension(format))"
}
