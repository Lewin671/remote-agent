// Images sent from the phone (architecture §17): container format sniffing,
// orientation inspection, and lossless metadata stripping delegate to
// clientcore/domain/imageparse via Corebind using header/seek windows and
// local Data range splicing (no full-file copy across FFI). Decoding and
// re-encoding pixels remain in ImagePrepare.swift.
import Corebind
import Darwin
import Foundation
import RAProto

/// The long side of a compressed image.
public let compressedLongSide = 2048

private let imgFmtUnknown: Int32 = 0
private let imgFmtJpeg: Int32 = 1
private let imgFmtPng: Int32 = 2
private let imgFmtWebp: Int32 = 3
private let imgFmtGif: Int32 = 4

private let imgModeInspect: Int32 = 1
private let imgModeStrip: Int32 = 2

private let imgStatusDone: UInt8 = 0
private let imgStatusNeedRead: UInt8 = 1
private let imgStatusNeedScanJpegEntropy: UInt8 = 2
private let imgStatusNeedSkipGifSubBlocks: UInt8 = 3
private let imgStatusError: UInt8 = 4

private let imgSniffHeaderBytes = 256
private let imgInitialHeaderBytes = 64 * 1024

private func formatFromCore(_ code: Int32) -> PBImageFormat? {
    switch code {
    case imgFmtJpeg: return .jpeg
    case imgFmtPng: return .png
    case imgFmtWebp: return .webp
    case imgFmtGif: return .gif
    default: return nil
    }
}

/// Returns a zero-based Data slice for [offset..<end] relative to `data.startIndex`.
private func subdataCopy(_ data: Data, _ offset: Int, _ end: Int) -> Data {
    let lo = max(0, min(offset, data.count))
    let hi = max(lo, min(end, data.count))
    let base = data.startIndex
    return Data(data[(base + lo)..<(base + hi)])
}

/// The format of encoded image bytes, by their signature; nil for anything else (HEIC and the like).
public func detectFormat(_ data: Data) -> PBImageFormat? {
    let head = subdataCopy(data, 0, min(data.count, imgSniffHeaderBytes))
    return formatFromCore(CorebindSniffImageFormat(head))
}

/// The orientation (1 to 8) a TIFF/Exif block records; 1 when it says none or cannot be read.
public func tiffOrientation(_ data: Data) -> Int {
    let raw = data.startIndex == 0 ? data : Data(data)
    return Int(CorebindTiffOrientation(raw))
}

private func u32le(_ d: Data, _ at: Int) -> Int {
    let base = d.startIndex + at
    return Int(d[base]) | (Int(d[base + 1]) << 8) | (Int(d[base + 2]) << 16) | (Int(d[base + 3]) << 24)
}

private func u64le(_ d: Data, _ at: Int) -> Int64 {
    let base = d.startIndex + at
    var v: UInt64 = 0
    for i in 0..<8 { v |= UInt64(d[base + i]) << (i * 8) }
    return Int64(bitPattern: v)
}

/// In-place memchr-accelerated scan for the first real JPEG marker after entropy-coded data; -1 on EOF.
private func scanJpegEntropy(_ data: Data, _ pos: Int) -> Int {
    guard pos >= 0, pos + 1 < data.count else { return -1 }
    return data.withUnsafeBytes { raw -> Int in
        guard let base = raw.baseAddress else { return -1 }
        let b = raw.bindMemory(to: UInt8.self)
        let n = b.count
        var i = pos
        while i + 1 < n {
            guard let found = memchr(base.advanced(by: i), 0xFF, n - i) else { return -1 }
            let idx = base.distance(to: found)
            var k = idx
            while k + 1 < n && b[k + 1] == 0xFF { k += 1 }
            if k + 1 >= n { return -1 }
            let next = b[k + 1]
            if next == 0x00 || (next >= 0xD0 && next <= 0xD7) {
                i = k + 2
                continue
            }
            return idx
        }
        return -1
    }
}

/// In-place skip over a GIF sub-block chain; returns offset after 0x00 terminator or -1 on EOF.
private func skipGifSubBlocks(_ data: Data, _ pos: Int) -> Int {
    guard pos >= 0, pos < data.count else { return -1 }
    return data.withUnsafeBytes { raw -> Int in
        let b = raw.bindMemory(to: UInt8.self)
        let n = b.count
        var p = pos
        while true {
            if p >= n { return -1 }
            let len = Int(b[p])
            p += 1 + len
            if p > n { return -1 }
            if len == 0 { return p }
        }
    }
}

private enum ImagePlanFailure: Error {
    case rejected(String)
}

private func runImagePlan(_ data: Data, mode: Int32, expectedFormat: Int32) throws -> Data {
    let maxSteps = CorebindImagePlanMaxSteps(Int64(data.count))
    guard let plan = CorebindNewImagePlan(mode, expectedFormat, Int64(data.count)) else {
        throw ImagePlanFailure.rejected("unavailable")
    }
    var offset: Int64 = 0
    var slice = subdataCopy(data, 0, min(imgInitialHeaderBytes, data.count))
    for _ in 0..<maxSteps {
        let res = try plan.step(offset, buf: slice)
        guard !res.isEmpty else { throw ImagePlanFailure.rejected("empty response") }
        let status = res[res.startIndex]
        if status == imgStatusDone { return res }
        if status == imgStatusError {
            let detail = res.count > 2 ? String(decoding: res[(res.startIndex + 2)...], as: UTF8.self) : ""
            throw ImagePlanFailure.rejected(detail)
        }
        switch status {
        case imgStatusNeedRead:
            offset = u64le(res, 1)
            let need = u32le(res, 9)
            let lo = Int(max(0, min(offset, Int64(data.count))))
            let hi = Int(min(Int64(data.count), offset + Int64(need)))
            slice = subdataCopy(data, lo, hi)
        case imgStatusNeedScanJpegEntropy:
            let from = Int(u64le(res, 1))
            let found = scanJpegEntropy(data, from)
            offset = Int64(found)
            slice = found >= 0 ? subdataCopy(data, found, min(found + imgInitialHeaderBytes, data.count)) : Data()
        case imgStatusNeedSkipGifSubBlocks:
            let from = Int(u64le(res, 1))
            let end = skipGifSubBlocks(data, from)
            offset = Int64(end)
            slice = end >= 0 ? subdataCopy(data, end, min(end + imgInitialHeaderBytes, data.count)) : Data()
        default:
            throw ImagePlanFailure.rejected("unexpected plan status \(status)")
        }
    }
    throw ImagePlanFailure.rejected("plan step limit exceeded")
}

private func spliceImagePlan(_ src: Data, _ done: Data) -> Data {
    let count = u32le(done, 12)
    let base = done.startIndex
    var at = 16
    var total = 0
    for _ in 0..<count {
        let kind = done[base + at]
        at += 1
        if kind == 0 {
            let s = Int(u64le(done, at))
            let e = Int(u64le(done, at + 8))
            total += e - s
            at += 16
        } else {
            let n = u32le(done, at)
            total += n
            at += 4 + n
        }
    }
    var out = Data(capacity: total)
    let srcBase = src.startIndex
    at = 16
    for _ in 0..<count {
        let kind = done[base + at]
        at += 1
        if kind == 0 {
            let s = Int(u64le(done, at))
            let e = Int(u64le(done, at + 8))
            out.append(src[(srcBase + s)..<(srcBase + e)])
            at += 16
        } else {
            let n = u32le(done, at)
            out.append(done[(base + at + 4)..<(base + at + 4 + n)])
            at += 4 + n
        }
    }
    return out
}

private func inspectOrientation(_ data: Data, expectedFormat: Int32) -> Int {
    guard let done = try? runImagePlan(data, mode: imgModeInspect, expectedFormat: expectedFormat), done.count >= 3 else {
        return 1
    }
    let o = Int(done[done.startIndex + 2])
    return (1...8).contains(o) ? o : 1
}

/// A JPEG's Exif orientation, 1 when none.
public func jpegOrientation(_ data: Data) -> Int { inspectOrientation(data, expectedFormat: imgFmtJpeg) }

/// A PNG's orientation from its eXIf chunk, 1 when none.
public func pngOrientation(_ data: Data) -> Int { inspectOrientation(data, expectedFormat: imgFmtPng) }

/// A WebP's orientation from its EXIF chunk, 1 when none.
public func webpOrientation(_ data: Data) -> Int { inspectOrientation(data, expectedFormat: imgFmtWebp) }

/// An image's orientation, by format; 1 for GIF and anything without one.
public func orientationOf(_ data: Data) -> Int { inspectOrientation(data, expectedFormat: imgFmtUnknown) }

/// A JPEG without its metadata (EXIF, XMP, IPTC, comments, trailing data); JFIF/ICC/Adobe stay.
public func stripJpeg(_ data: Data) throws -> Data {
    do {
        let done = try runImagePlan(data, mode: imgModeStrip, expectedFormat: imgFmtJpeg)
        return spliceImagePlan(data, done)
    } catch {
        throw UserError(L10nMessage.imageIosNotAReadableJpeg(), problemKind: .decode)
    }
}

/// A PNG without text, time and Exif chunks; the image data is not touched.
public func stripPng(_ data: Data) throws -> Data {
    do {
        let done = try runImagePlan(data, mode: imgModeStrip, expectedFormat: imgFmtPng)
        return spliceImagePlan(data, done)
    } catch {
        throw UserError(L10nMessage.imageIosNotAReadablePng(), problemKind: .decode)
    }
}

/// A WebP without its EXIF and XMP chunks, with the extended header's flags and the file size to match.
public func stripWebp(_ data: Data) throws -> Data {
    do {
        let done = try runImagePlan(data, mode: imgModeStrip, expectedFormat: imgFmtWebp)
        return spliceImagePlan(data, done)
    } catch {
        throw UserError(L10nMessage.imageIosNotAReadableWebp(), problemKind: .decode)
    }
}

/// A GIF without comments and non-loop application blocks; frames are not touched.
public func stripGif(_ data: Data) throws -> Data {
    do {
        let done = try runImagePlan(data, mode: imgModeStrip, expectedFormat: imgFmtGif)
        return spliceImagePlan(data, done)
    } catch let ImagePlanFailure.rejected(detail) {
        if detail.contains("unknown block") {
            throw UserError(L10nMessage.imageIosGifContainsAnUnrecognizedBlock(), problemKind: .decode)
        }
        if detail.contains("not a GIF") || detail.contains("empty") || detail.contains("does not match") {
            throw UserError(L10nMessage.imageIosNotAReadableGif(), problemKind: .decode)
        }
        throw UserError(L10nMessage.imageIosGifIsTruncated(), problemKind: .decode)
    } catch {
        throw UserError(L10nMessage.imageIosNotAReadableGif(), problemKind: .decode)
    }
}

/// An original with its metadata taken out, or nil when that needs new
/// pixels: it is turned by an orientation tag (lossless rotation is not
/// done here; the caller re-encodes at the highest quality instead) or its
/// format is not one the CLIs read (HEIC). Throws for a file of a known
/// format that cannot be read.
public func stripOriginal(_ data: Data) throws -> Data? {
    guard let f = detectFormat(data) else { return nil }
    if orientationOf(data) != 1 { return nil }
    switch f {
    case .jpeg: return try stripJpeg(data)
    case .png: return try stripPng(data)
    case .webp: return try stripWebp(data)
    case .gif: return try stripGif(data)
    default: return nil
    }
}

/// The size of a picture shrunk so its long side is at most `max`; never enlarged.
public func fitLongSide(w: Int, h: Int, max: Int = compressedLongSide) -> (w: Int, h: Int) {
    let long = Swift.max(w, h)
    if long <= max || long == 0 { return (w, h) }
    let scale = Double(max) / Double(long)
    return (Swift.max(1, jvmRound(Double(w) * scale)), Swift.max(1, jvmRound(Double(h) * scale)))
}

/// Orientations 5 to 8 swap width and height.
public func swapsSides(orientation: Int) -> Bool { (5...8).contains(orientation) }

// --- uploading ---

/// Chunks start at this size when the daemon names none: an encrypted command stays well under 1 MiB.
public let defaultChunkBytes = 768 * 1024

/// The daemon's upload commands for one machine; each call is one machine command.
public protocol UploadChannel: Sendable {
    func abort(transferId: String) async throws
    func beginFile(transferId: String, name: String, total: Int64, sha256: Data) async throws -> PBImageUploadStatus
    func begin(transferId: String, format: PBImageFormat, total: Int64, sha256: Data) async throws -> PBImageUploadStatus
    func put(transferId: String, offset: Int64, data: Data) async throws -> PBImageUploadStatus
    func status(transferId: String) async throws -> PBImageUploadStatus
    func complete(transferId: String) async throws -> PBImageUploadStatus
}

/// What an upload needs to know of a command that failed: its Connect code
/// name, and whether the command may or may not have run. The sync engine's
/// command error conforms. An unknown outcome and "network" are the two an
/// upload goes on after, by asking the daemon where it stands.
public protocol UploadFailure: Error {
    var code: String { get }
    var unknown: Bool { get }
}

extension UploadFailure {
    public var unknown: Bool { code == CommandError.unknownOutcome }
}

func uploadRetryAllowed(_ error: any Error) -> Bool {
    if let command = error as? CommandError { return command.canRetryUpload }
    guard let failure = error as? any UploadFailure else { return false }
    return failure.unknown || failure.code == "network"
}

/// An upload that ended without the daemon refusing it: its answer was not one to go on from.
public struct UploadError: UploadFailure, LocalizedError, Equatable {
    public let code: String
    let localized: LocalizedMessage
    public var message: String { localized.resolve() }
    public init(code: String, message: String) { self.code = code; localized = LocalizedMessage(raw: message) }
    public init(code: String, message: LocalizedMessage) { self.code = code; localized = message }
    public static func == (lhs: UploadError, rhs: UploadError) -> Bool { lhs.code == rhs.code && lhs.localized == rhs.localized }

    public var errorDescription: String? { message }
}

/// A completed upload and the deadline after which its image ID needs renewing.
public struct UploadedImage: Equatable, Sendable {
    public var imageId: String
    public var expiresMs: Int64
}

/// Sends one image to a machine in chunks and returns its image ID and deadline. Every step can be repeated: beginning again returns the
/// transfer's state, a chunk is resent from where the daemon has durably
/// stored bytes, and completing again returns the same ID. When a step's
/// outcome is unknown, the daemon's status says where to go on; a definite
/// refusal ends the upload with an error, never a partial success. It runs
/// where it is called from, so `onProgress` may touch the caller's state.
public func uploadImage(
    isolation: isolated (any Actor)? = #isolation,
    _ ch: some UploadChannel,
    transferId: String,
    data: Data,
    maxChunk: Int = defaultChunkBytes,
    retries: Int = 3,
    onProgress: (Int64, Int64) -> Void = { _, _ in }
) async throws -> UploadedImage {
    guard let format = detectFormat(data) else { throw UserError(L10nMessage.imageIosUnsupportedImageFormat(), problemKind: .decode) }
    let bytes = Data(data)
    let total = Int64(bytes.count)
    var tries = 0
    // Whether to go on after a failed step: only when its outcome is not known, and not forever.
    func again(_ error: any Error) -> Bool {
        guard uploadRetryAllowed(error), tries < retries else { return false }
        tries += 1
        return true
    }
    var st: PBImageUploadStatus
    while true {
        do {
            st = try await ch.begin(transferId: transferId, format: format, total: total, sha256: sha256(bytes))
            break
        } catch where again(error) {}
    }
    let chunk = Int64(min(maxChunk, st.maxChunkBytes > 0 ? Int(st.maxChunkBytes) : Int(Int32.max)))
    while st.state == .receiving && st.persistedBytes < total {
        let off = st.persistedBytes
        // Kotlin's copy of the range fails here; a slice of a negative range would stop the app.
        if off < 0 { throw UploadError(code: "internal", message: L10nMessage.attachmentIosTheComputerReportedInvalidUploadProgress()) }
        onProgress(off, total)
        let end = min(total, off + chunk)
        let part = bytes.subdata(in: Int(off)..<Int(max(off, end)))
        while true {
            do {
                st = try await ch.put(transferId: transferId, offset: off, data: part)
                break
            } catch where again(error) {
                st = try await ch.status(transferId: transferId)
            }
        }
    }
    onProgress(total, total)
    if st.state == .receiving {
        while true {
            do {
                st = try await ch.complete(transferId: transferId)
                break
            } catch where again(error) {
                st = try await ch.status(transferId: transferId)
            }
        }
    }
    if st.imageID.isEmpty { throw UploadError(code: "internal", message: L10nMessage.imageIosTheComputerDidNotProvideAnImageId()) }
    return UploadedImage(imageId: st.imageID, expiresMs: st.expiresMs)
}

/// Most pixels an image is decoded at on the phone, whatever its size: 40 MP, ARGB, is 160 MB.
public let maxDecodePixels: Int64 = 40_000_000

/// The power-of-two sample size to decode a `w`×`h` image at: small enough
/// that the decoded picture fits `pixelBudget` (so memory has a bound,
/// rotation's second copy included in the caller's budget), and no smaller
/// than needed for a long side of `minLong` (0: as large as the budget
/// allows).
public func decodeSample(w: Int, h: Int, minLong: Int, pixelBudget: Int64) throws -> Int {
    guard w > 0 && h > 0 else { throw UserError(L10nMessage.imageIosImageHasNoDimensions(), problemKind: .dimensions) }
    var s = 1
    if minLong > 0 { while max(w, h) / (s * 2) >= minLong { s *= 2 } }
    // A budget below one pixel can never be met; the smallest picture is what is left.
    while s < max(w, h) && Int64(w / s) * Int64(h / s) > pixelBudget { s *= 2 }
    return s
}
