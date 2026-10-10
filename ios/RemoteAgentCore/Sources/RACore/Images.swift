// Images sent from the phone (architecture §17): reading formats and
// orientation, taking metadata out of files sent as originals, the limits
// of one message, and uploading in chunks. Decoding and re-encoding pixels
// needs the platform and is in the app.
//
// The parsers read files people picked, so every offset is checked before
// it is used: Kotlin's version leans on array bounds exceptions in places,
// where an unchecked read here would stop the app.
import Foundation
import RAProto

/// At most this many images go with one message, whatever the daemon says or the picker allowed.
public let maxImages = 6

/// An original may be this large at most (architecture §17).
public let maxOriginalBytes: Int64 = 20 * 1024 * 1024

/// The long side of a compressed image.
public let compressedLongSide = 2048

private typealias Bytes = [UInt8]

/// Whether the bytes at `i` spell `text` (ASCII).
private func has(_ b: Bytes, _ i: Int, _ text: String) -> Bool {
    let t = Array(text.utf8)
    return i >= 0 && i + t.count <= b.count && b[i..<(i + t.count)].elementsEqual(t)
}

/// The four bytes at `i` as a chunk type; they are compared with ASCII names only.
private func tag(_ b: Bytes, _ i: Int) -> String { String(decoding: b[i..<(i + 4)], as: Unicode.ASCII.self) }

private func detect(_ b: Bytes) -> PBImageFormat? {
    if b.count >= 8 && b[0] == 0x89 && has(b, 1, "PNG") { return .png }
    if b.count >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF { return .jpeg }
    if b.count >= 12 && has(b, 0, "RIFF") && has(b, 8, "WEBP") { return .webp }
    if b.count >= 6 && (has(b, 0, "GIF87a") || has(b, 0, "GIF89a")) { return .gif }
    return nil
}

/// The format of encoded image bytes, by their signature; nil for anything else (HEIC and the like).
public func detectFormat(_ data: Data) -> PBImageFormat? { detect(Bytes(data.prefix(12))) }

/// Which images of a pick are taken: the first ones up to `max` in all, in
/// the order picked, and how many were left out. The system picker may not
/// enforce a count (its fallback does not), so the app does.
public func takePicked<T>(have: Int, picked: [T], max: Int = maxImages) -> (taken: [T], left: Int) {
    let room = Swift.max(max - have, 0)
    return (Array(picked.prefix(room)), Swift.max(picked.count - room, 0))
}

private func u16(_ b: Bytes, _ i: Int, le: Bool) -> Int? {
    guard i >= 0, i + 2 <= b.count else { return nil }
    return le ? Int(b[i]) | Int(b[i + 1]) << 8 : Int(b[i]) << 8 | Int(b[i + 1])
}

private func u32(_ b: Bytes, _ i: Int, le: Bool) -> Int? {
    guard let a = u16(b, i, le: le), let c = u16(b, i + 2, le: le) else { return nil }
    return le ? a | c << 16 : a << 16 | c
}

/// The orientation (1 to 8) a TIFF/Exif block records; 1 when it says none or cannot be read.
private func tiffOrientation(_ t: Bytes, _ off: Int, _ end: Int) -> Int {
    if end - off < 8 || end > t.count || off < 0 { return 1 }
    let le: Bool
    if has(t, off, "II") { le = true } else if has(t, off, "MM") { le = false } else { return 1 }
    guard u16(t, off + 2, le: le) == 42, let rel = u32(t, off + 4, le: le) else { return 1 }
    let ifd = off + rel
    if ifd + 2 > end { return 1 }
    guard let n = u16(t, ifd, le: le) else { return 1 }
    for i in 0..<n {
        let e = ifd + 2 + i * 12
        if e + 12 > end { return 1 }
        if u16(t, e, le: le) == 0x0112 {
            guard let v = u16(t, e + 8, le: le) else { return 1 }
            return (1...8).contains(v) ? v : 1
        }
    }
    return 1
}

/// The orientation (1 to 8) a TIFF/Exif block records; 1 when it says none or cannot be read.
public func tiffOrientation(_ data: Data) -> Int {
    let t = Bytes(data)
    return tiffOrientation(t, 0, t.count)
}

/// A JPEG's segments before the image data: marker, and where the segment starts and ends.
private struct Segment {
    var marker: Int
    var start: Int
    var end: Int
}

private func jpegSegments(_ b: Bytes) -> [Segment]? {
    if detect(b) != .jpeg { return nil }
    var out = [Segment]()
    var i = 2
    while i + 4 <= b.count {
        if b[i] != 0xFF { return nil }
        let marker = Int(b[i + 1])
        if marker == 0xFF {
            i += 1
            continue
        }
        // Start of scan: everything from here on is image data.
        if marker == 0xDA { return out }
        if marker == 0xD8 || (0xD0...0xD7).contains(marker) || marker == 0x01 {
            i += 2
            continue
        }
        let len = Int(b[i + 2]) << 8 | Int(b[i + 3])
        if len < 2 || i + 2 + len > b.count { return nil }
        out.append(Segment(marker: marker, start: i, end: i + 2 + len))
        i += 2 + len
    }
    return nil
}

private func jpegOrientation(_ b: Bytes) -> Int {
    guard let segs = jpegSegments(b) else { return 1 }
    for s in segs where s.marker == 0xE1 && s.end - s.start > 10 && has(b, s.start + 4, "Exif\0\0") {
        return tiffOrientation(b, s.start + 10, s.end)
    }
    return 1
}

/// A JPEG's Exif orientation, 1 when none.
public func jpegOrientation(_ data: Data) -> Int { jpegOrientation(Bytes(data)) }

/// Whether an application or comment segment is kept: JFIF, the color profile and Adobe's color transform.
private func keepJpegSegment(_ b: Bytes, marker: Int, start: Int, end: Int) -> Bool {
    switch marker {
    case 0xE0: return end - start >= 8 && has(b, start + 4, "JFIF")
    case 0xE2: return end - start >= 15 && has(b, start + 4, "ICC_PROFILE")
    case 0xEE: return true
    case 0xFE: return false
    case 0xE1...0xEF: return false
    default: return true
    }
}

private func stripJpeg(_ b: Bytes) throws -> Bytes {
    let bad = UserError("不是能读的 JPEG")
    if detect(b) != .jpeg { throw bad }
    var out = Bytes()
    out.reserveCapacity(b.count)
    out.append(contentsOf: b[0..<2])
    var i = 2
    while true {
        if i + 2 > b.count || b[i] != 0xFF { throw bad }
        let marker = Int(b[i + 1])
        if marker == 0xFF {
            // Fill bytes before a marker.
            i += 1
        } else if marker == 0xD9 {
            out.append(contentsOf: b[i..<(i + 2)])
            return out
        } else if (0xD0...0xD7).contains(marker) || marker == 0x01 {
            out.append(contentsOf: b[i..<(i + 2)])
            i += 2
        } else {
            if i + 4 > b.count { throw bad }
            let len = Int(b[i + 2]) << 8 | Int(b[i + 3])
            if len < 2 || i + 2 + len > b.count { throw bad }
            let end = i + 2 + len
            if keepJpegSegment(b, marker: marker, start: i, end: end) { out.append(contentsOf: b[i..<end]) }
            i = end
            if marker == 0xDA {
                // The scan's coded data runs to the next marker other than
                // a stuffed zero or a restart marker. Fill bytes (more
                // 0xFF) may come before a restart marker too.
                var j = i
                while j + 1 < b.count {
                    if b[j] == 0xFF {
                        var k = j
                        while k + 1 < b.count, b[k + 1] == 0xFF { k += 1 }
                        if k + 1 >= b.count { throw bad }
                        let n = Int(b[k + 1])
                        if n != 0x00 && !(0xD0...0xD7).contains(n) { break }
                        j = k + 2
                    } else {
                        j += 1
                    }
                }
                if j + 1 >= b.count { throw bad }
                out.append(contentsOf: b[i..<j])
                i = j
            }
        }
    }
}

/// A JPEG without its metadata: Exif and XMP (APP1), IPTC (APP13), comments
/// and the other application segments go wherever they are, before the
/// first scan, between the scans of a progressive JPEG or right before the
/// end; JFIF (APP0), the color profile (APP2 ICC_PROFILE) and Adobe's color
/// transform (APP14) stay, since the picture depends on them. Only the
/// entropy-coded data of each scan is copied without being parsed, up to the
/// next marker; anything after the end marker goes. Take the orientation
/// into account first: without Exif the picture shows as stored.
public func stripJpeg(_ data: Data) throws -> Data { Data(try stripJpeg(Bytes(data))) }

private struct Chunk {
    var type: String
    var start: Int
    var end: Int
}

private func pngChunks(_ b: Bytes) -> [Chunk]? {
    if detect(b) != .png { return nil }
    var out = [Chunk]()
    var i = 8
    while i + 12 <= b.count {
        guard let len = u32(b, i, le: false), len <= b.count else { return nil }
        let type = tag(b, i + 4)
        let end = i + 12 + len
        if end > b.count { return nil }
        out.append(Chunk(type: type, start: i, end: end))
        i = end
        if type == "IEND" { break }
    }
    return out
}

private func pngOrientation(_ b: Bytes) -> Int {
    guard let c = pngChunks(b)?.first(where: { $0.type == "eXIf" }) else { return 1 }
    return tiffOrientation(b, c.start + 8, c.end - 4)
}

/// A PNG's orientation from its eXIf chunk, 1 when none.
public func pngOrientation(_ data: Data) -> Int { pngOrientation(Bytes(data)) }

private func stripPng(_ b: Bytes) throws -> Bytes {
    guard let chunks = pngChunks(b) else { throw UserError("不是能读的 PNG") }
    let drop: Set<String> = ["tEXt", "zTXt", "iTXt", "eXIf", "tIME"]
    var out = Bytes()
    out.reserveCapacity(b.count)
    out.append(contentsOf: b[0..<8])
    for c in chunks where !drop.contains(c.type) { out.append(contentsOf: b[c.start..<c.end]) }
    return out
}

/// A PNG without text, time and Exif chunks; the image data is not touched.
public func stripPng(_ data: Data) throws -> Data { Data(try stripPng(Bytes(data))) }

private func webpChunks(_ b: Bytes) -> [Chunk]? {
    if detect(b) != .webp { return nil }
    var out = [Chunk]()
    var i = 12
    while i + 8 <= b.count {
        let type = tag(b, i)
        guard let len = u32(b, i + 4, le: true) else { return nil }
        let end = i + 8 + len + (len & 1)
        if len > b.count || end > b.count + 1 { return nil }
        out.append(Chunk(type: type, start: i, end: min(end, b.count)))
        i = end
    }
    return out
}

private func webpOrientation(_ b: Bytes) -> Int {
    guard let c = webpChunks(b)?.first(where: { $0.type == "EXIF" }) else { return 1 }
    let start = c.start + 8
    // Some writers put the Exif header in front of the TIFF block.
    let tiff = c.end - start > 6 && has(b, start, "Exif\0\0") ? start + 6 : start
    return tiffOrientation(b, tiff, c.end)
}

/// A WebP's orientation from its EXIF chunk, 1 when none.
public func webpOrientation(_ data: Data) -> Int { webpOrientation(Bytes(data)) }

private func stripWebp(_ b: Bytes) throws -> Bytes {
    guard let chunks = webpChunks(b) else { throw UserError("不是能读的 WebP") }
    var out = Bytes()
    out.reserveCapacity(b.count)
    out.append(contentsOf: b[0..<12])
    for c in chunks {
        if c.type == "EXIF" || c.type == "XMP " { continue }
        if c.type == "VP8X" && c.end - c.start >= 9 {
            var chunk = Bytes(b[c.start..<c.end])
            // Flags: bit 3 Exif, bit 2 XMP.
            chunk[8] &= ~0x0C
            out.append(contentsOf: chunk)
        } else {
            out.append(contentsOf: b[c.start..<c.end])
        }
    }
    let size = UInt32(truncatingIfNeeded: out.count - 8)
    out[4] = UInt8(truncatingIfNeeded: size)
    out[5] = UInt8(truncatingIfNeeded: size >> 8)
    out[6] = UInt8(truncatingIfNeeded: size >> 16)
    out[7] = UInt8(truncatingIfNeeded: size >> 24)
    return out
}

/// A WebP without its EXIF and XMP chunks, with the extended header's flags and the file size to match.
public func stripWebp(_ data: Data) throws -> Data { Data(try stripWebp(Bytes(data))) }

private func stripGif(_ b: Bytes) throws -> Bytes {
    if detect(b) != .gif || b.count < 13 { throw UserError("不是能读的 GIF") }
    let cut = UserError("GIF 被截断")
    var out = Bytes()
    out.reserveCapacity(b.count)
    var i = 13
    let flags = Int(b[10])
    if flags & 0x80 != 0 { i += 3 * (1 << ((flags & 7) + 1)) }
    if i > b.count { throw cut }
    out.append(contentsOf: b[0..<i])
    func subBlocks(_ from: Int) throws -> Int {
        var j = from
        while true {
            if j >= b.count { throw cut }
            let n = Int(b[j])
            j += 1 + n
            if n == 0 { return j }
        }
    }
    while i < b.count {
        switch b[i] {
        case 0x3B:
            out.append(0x3B)
            return out
        case 0x2C:
            if i + 10 > b.count { throw cut }
            let f = Int(b[i + 9])
            var j = i + 10
            if f & 0x80 != 0 { j += 3 * (1 << ((f & 7) + 1)) }
            j = try subBlocks(j + 1)
            out.append(contentsOf: b[i..<j])
            i = j
        case 0x21:
            if i + 1 >= b.count { throw cut }
            let label = Int(b[i + 1])
            let j = try subBlocks(i + 2)
            let app = label == 0xFF && i + 14 <= b.count && (has(b, i + 3, "NETSCAPE2.0") || has(b, i + 3, "ANIMEXTS1.0"))
            let keep: Bool
            switch label {
            case 0xFE: keep = false
            case 0xFF: keep = app
            default: keep = true
            }
            if keep { out.append(contentsOf: b[i..<j]) }
            i = j
        default:
            throw UserError("GIF 里有认不出的块")
        }
    }
    throw cut
}

/// A GIF without comments and without application blocks other than the
/// animation loop (NETSCAPE2.0), where XMP and the like live; frames are not
/// touched.
public func stripGif(_ data: Data) throws -> Data { Data(try stripGif(Bytes(data))) }

private func orientationOf(_ b: Bytes) -> Int {
    switch detect(b) {
    case .jpeg: return jpegOrientation(b)
    case .png: return pngOrientation(b)
    case .webp: return webpOrientation(b)
    default: return 1
    }
}

/// An image's orientation, by format; 1 for GIF and anything without one.
public func orientationOf(_ data: Data) -> Int { orientationOf(Bytes(data)) }

/// An original with its metadata taken out, or nil when that needs new
/// pixels: it is turned by an orientation tag (lossless rotation is not
/// done here; the caller re-encodes at the highest quality instead) or its
/// format is not one the CLIs read (HEIC). Throws for a file of a known
/// format that cannot be read.
public func stripOriginal(_ data: Data) throws -> Data? {
    let b = Bytes(data)
    guard let f = detect(b) else { return nil }
    if orientationOf(b) != 1 { return nil }
    switch f {
    case .jpeg: return Data(try stripJpeg(b))
    case .png: return Data(try stripPng(b))
    case .webp: return Data(try stripWebp(b))
    case .gif: return Data(try stripGif(b))
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

/// An upload that ended without the daemon refusing it: its answer was not one to go on from.
public struct UploadError: UploadFailure, LocalizedError, Equatable {
    public let code: String
    public let message: String

    public init(code: String, message: String) {
        self.code = code
        self.message = message
    }

    public var errorDescription: String? { message }
}

/// Sends one image to a machine in chunks and returns the image ID to send
/// with a message. Every step can be repeated: beginning again returns the
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
) async throws -> String {
    guard let format = detectFormat(data) else { throw UserError("不支持的图片格式") }
    let bytes = Data(data)
    let total = Int64(bytes.count)
    var tries = 0
    // Whether to go on after a failed step: only when its outcome is not known, and not forever.
    func again(_ error: any Error) -> Bool {
        guard let e = error as? any UploadFailure, e.unknown || e.code == "network", tries < retries else { return false }
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
        if off < 0 { throw UploadError(code: "internal", message: "电脑报告的上传进度不对") }
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
    if st.imageID.isEmpty { throw UploadError(code: "internal", message: "电脑没有给出图片 ID") }
    return st.imageID
}

// --- images in a session ---

/// What kind of event shows an image.
public enum SessionImageKind: Sendable { case userMessage, toolCall, imageGenerated }

/// One image as it appears in a session: the ref, the event it is in, and what shows it.
public struct SessionImage: Equatable, Sendable {
    public var ref: PBImageRef
    public var seq: UInt64
    public var kind: SessionImageKind
    public var toolTitle: String

    public init(ref: PBImageRef, seq: UInt64, kind: SessionImageKind, toolTitle: String = "") {
        self.ref = ref
        self.seq = seq
        self.kind = kind
        self.toolTitle = toolTitle
    }
}

/// Every image of a session in the order they appeared, for the viewer to page through.
public func sessionImages(_ events: some Sequence<PBEvent>) -> [SessionImage] {
    var out = [SessionImage]()
    for e in events {
        switch e.kind {
        case .userMessage(let m): for r in m.images { out.append(SessionImage(ref: r, seq: e.seq, kind: .userMessage)) }
        case .toolCall(let t): for r in t.images { out.append(SessionImage(ref: r, seq: e.seq, kind: .toolCall, toolTitle: t.title)) }
        case .imageGenerated(let g): for r in g.images { out.append(SessionImage(ref: r, seq: e.seq, kind: .imageGenerated)) }
        default: break
        }
    }
    return out
}

private let placeholder = Rx("\\[Image #(\(Jre.digit)+)\\]")
private let leadingPlaceholder = Rx("\\A\(Jre.space)*\\[Image #(\(Jre.digit)+)\\]")

/// A user message's text as shown next to its images. When the message is
/// its images followed by text, the leading "[Image #N]" placeholders are
/// dropped: the numbered previews stand for them. When the text refers to
/// images further in, every placeholder reads "图 N" so the references keep
/// their meaning. Placeholders the message has no image for, e.g. typed as
/// text, stay as written.
public func imageText(_ text: String, refs: [PBImageRef]) -> String {
    let known = Set(refs.map { Int($0.cliIndex) }.filter { $0 > 0 })
    if known.isEmpty { return text }
    // A number too large to be an image's index is no image's (Kotlin's toInt throws on one).
    func isKnown(_ m: Rx.Match) -> Bool { Int(m.text(1)).map(known.contains) ?? false }
    var t = text
    while let m = leadingPlaceholder.find(t), isKnown(m) {
        t = (t as NSString).substring(from: m.range.location + m.range.length)
    }
    let laterRefs = placeholder.findAll(t).contains(where: isKnown)
    let base = laterRefs ? text : t.jvmTrimStart()
    return placeholder.replace(base) { m in isKnown(m) ? "图 \(m.text(1))" : m.value }
}

/// Most pixels an image is decoded at on the phone, whatever its size: 40 MP, ARGB, is 160 MB.
public let maxDecodePixels: Int64 = 40_000_000

/// The power-of-two sample size to decode a `w`×`h` image at: small enough
/// that the decoded picture fits `pixelBudget` (so memory has a bound,
/// rotation's second copy included in the caller's budget), and no smaller
/// than needed for a long side of `minLong` (0: as large as the budget
/// allows).
public func decodeSample(w: Int, h: Int, minLong: Int, pixelBudget: Int64) throws -> Int {
    guard w > 0 && h > 0 else { throw UserError("图片没有尺寸") }
    var s = 1
    if minLong > 0 { while max(w, h) / (s * 2) >= minLong { s *= 2 } }
    // A budget below one pixel can never be met; the smallest picture is what is left.
    while s < max(w, h) && Int64(w / s) * Int64(h / s) > pixelBudget { s *= 2 }
    return s
}
