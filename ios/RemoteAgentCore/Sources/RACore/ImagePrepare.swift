// Preparing a picked image for the computer (architecture §17,
// docs/session-images.md 「从客户端发图片」): Android's ui/ImageProcess.kt.
//
// Pixels are read and written with ImageIO and CoreGraphics, which the
// phone and the Mac both have, so `swift test` holds this to its rules:
//
//   - a photo is never decoded at its full size unless it is sent at that
//     size: ImageIO samples it down while decoding, to the long side asked
//     for and never past the pixel budget (a 48 MP photo costs what a
//     2048 px one costs);
//   - the picture is turned upright by its orientation tag, since the tag
//     goes with the rest of the metadata;
//   - nothing of the source's metadata is written, and what ImageIO adds on
//     its own is taken out again (`stripJpeg`, `stripPng`), so no location
//     leaves the phone;
//   - one image is prepared at a time: several large decodes at once are
//     what runs a phone out of memory.
import CoreGraphics
import Foundation
import ImageIO
import RAProto
import UniformTypeIdentifiers

/// A picked image that is too large to read or to send.
public struct ImageTooLarge: Error, LocalizedError, Equatable {
    public let message: String

    public init(_ message: String) {
        self.message = message
    }

    public var errorDescription: String? { message }
}

/// A compressed image aims at this size, so it goes in one command.
public let compressedTargetBytes = 900 * 1024

/// The most bytes read of what was picked: three times an original's limit
/// for an original (metadata may be most of it), 80 MB for one to compress.
public func pickedReadLimit(original: Bool) -> Int64 { original ? maxOriginalBytes * 3 : 80 * 1024 * 1024 }

/// Refuses a picked file larger than what is read of one.
public func checkPicked(bytes: Int64, original: Bool) throws {
    let limit = pickedReadLimit(original: original)
    if bytes > limit { throw ImageTooLarge("图片超过 \(limit / 1024 / 1024) MB") }
}

/// What to try next when a compressed image came out at `bytes`.
public enum CompressStep: Equatable, Sendable {
    /// Encode again at this JPEG quality.
    case quality(Int)
    /// Shrink both sides to 80% and encode again.
    case shrink
}

/// The next step towards `compressedTargetBytes`, or nil when the image is
/// small enough or nothing more is to be tried: the quality goes down from
/// 85 in tens while it is above 60, then the picture shrinks until its long
/// side is 800. A PNG, which has no quality, only shrinks.
public func nextCompressStep(bytes: Int, quality: Int, longSide: Int, png: Bool) -> CompressStep? {
    guard bytes > compressedTargetBytes, quality > 60 || longSide > 800 else { return nil }
    return !png && quality > 60 ? .quality(quality - 10) : .shrink
}

/// One side after shrinking to 80%, never under a pixel.
public func shrunkSide(_ n: Int) -> Int { max(1, Int(Double(n) * 0.8)) }

/// What 原图 says under the strip when the file could not go as it was.
public func originalNote(turned: Bool, png: Bool, sample: Int) -> String {
    let shrunk = sample > 1 ? "；像素超过手机能处理的上限，缩小到 1/\(sample)" : ""
    return (turned ? "按拍摄方向转正后以最高画质重新编码" : "转成了 \(png ? "PNG" : "JPEG")，保留原始尺寸") + shrunk
}

/// Pixels this process may decode at once: the fixed cap, and a share of
/// the memory that leaves room for a second copy and the encoded bytes.
func pixelBudget(physicalMemory: UInt64 = ProcessInfo.processInfo.physicalMemory) -> Int64 {
    min(maxDecodePixels, Int64(clamping: physicalMemory / 64))
}

private let undecodable = UserError("解不出这张图片")

private func hasAlpha(_ image: CGImage) -> Bool {
    switch image.alphaInfo {
    case .none, .noneSkipFirst, .noneSkipLast: return false
    default: return true
    }
}

/// A bitmap to draw into: the image's own colors when they are RGB, sRGB otherwise.
private func canvas(width: Int, height: Int, like image: CGImage) -> CGContext? {
    var space = CGColorSpace(name: CGColorSpace.sRGB)!
    if let own = image.colorSpace, own.model == .rgb, own.supportsOutput { space = own }
    let info = hasAlpha(image) ? CGImageAlphaInfo.premultipliedLast : .noneSkipLast
    return CGContext(
        data: nil, width: width, height: height, bitsPerComponent: 8, bytesPerRow: 0, space: space, bitmapInfo: info.rawValue)
}

/// The picture at another size.
func scaled(_ image: CGImage, width: Int, height: Int) throws -> CGImage {
    guard let ctx = canvas(width: width, height: height, like: image) else { throw undecodable }
    ctx.interpolationQuality = .high
    ctx.draw(image, in: CGRect(x: 0, y: 0, width: width, height: height))
    guard let out = ctx.makeImage() else { throw undecodable }
    return out
}

/// The stored picture turned upright by orientation `o` (1 to 8), pixel by
/// pixel. Only for the tags ImageIO does not act on itself (a PNG's eXIf, a
/// WebP's EXIF chunk); it turns the others while decoding.
func oriented(_ image: CGImage, orientation o: Int) throws -> CGImage {
    guard (2...8).contains(o) else { return image }
    let (w, h) = (image.width, image.height)
    let (dw, dh) = swapsSides(orientation: o) ? (h, w) : (w, h)
    guard let src = canvas(width: w, height: h, like: image), let dst = canvas(width: dw, height: dh, like: image) else { throw undecodable }
    src.draw(image, in: CGRect(x: 0, y: 0, width: w, height: h))
    guard let from = src.data, let to = dst.data else { throw undecodable }
    let (srcRow, dstRow) = (src.bytesPerRow, dst.bytesPerRow)
    let a = from.bindMemory(to: UInt32.self, capacity: srcRow * h / 4)
    let b = to.bindMemory(to: UInt32.self, capacity: dstRow * dh / 4)
    for dy in 0..<dh {
        for dx in 0..<dw {
            // Where the stored picture has this upright pixel: the tag says
            // which sides of the scene its first row and column are.
            let (sx, sy): (Int, Int)
            switch o {
            case 2: (sx, sy) = (w - 1 - dx, dy)
            case 3: (sx, sy) = (w - 1 - dx, h - 1 - dy)
            case 4: (sx, sy) = (dx, h - 1 - dy)
            case 5: (sx, sy) = (dy, dx)
            case 6: (sx, sy) = (dy, dw - 1 - dx)
            case 7: (sx, sy) = (dh - 1 - dy, dw - 1 - dx)
            default: (sx, sy) = (dh - 1 - dy, dx)
            }
            b[dy * dstRow / 4 + dx] = a[sy * srcRow / 4 + sx]
        }
    }
    guard let out = dst.makeImage() else { throw undecodable }
    return out
}

/// A decoded picture, the sample size it was decoded at (1: full size), and
/// whether the file has transparency. That is the file's word: a picture
/// ImageIO sampled down may carry an alpha channel its source never had (a
/// HEIC photo does), and would be taken for a PNG.
struct DecodedImage {
    var image: CGImage
    var sample: Int
    var alpha: Bool
}

private func source(_ data: Data) -> (CGImageSource, w: Int, h: Int, orientation: Int, alpha: Bool)? {
    guard let src = CGImageSourceCreateWithData(data as CFData, [kCGImageSourceShouldCache: false] as CFDictionary),
        CGImageSourceGetCount(src) > 0,
        let props = CGImageSourceCopyPropertiesAtIndex(src, 0, nil) as? [CFString: Any],
        let w = props[kCGImagePropertyPixelWidth] as? Int, let h = props[kCGImagePropertyPixelHeight] as? Int, w > 0, h > 0
    else { return nil }
    return (src, w, h, props[kCGImagePropertyOrientation] as? Int ?? 1, props[kCGImagePropertyHasAlpha] as? Bool ?? false)
}

/// The size of encoded image bytes as stored, before any orientation; nil when they do not read.
public func imagePixelSize(_ data: Data) -> (w: Int, h: Int)? {
    source(data).map { ($0.w, $0.h) }
}

/// Decodes and turns the picture upright, at most `maxLong` on its long
/// side (0: full size), never more than `budget` pixels: the size is read
/// first and ImageIO samples the image down while it decodes, so the full
/// picture is never in memory unless it is what was asked for. An animated
/// image gives its first frame.
func decodeUpright(_ data: Data, maxLong: Int, budget: Int64? = nil) throws -> DecodedImage {
    guard let (src, w, h, tagged, alpha) = source(data) else { throw undecodable }
    let sample = try decodeSample(w: w, h: h, minLong: 0, pixelBudget: budget ?? pixelBudget())
    var long = max(w, h) / sample
    if maxLong > 0 { long = min(long, maxLong) }
    let options: [CFString: Any] = [
        kCGImageSourceCreateThumbnailFromImageAlways: true,
        kCGImageSourceCreateThumbnailWithTransform: true,
        kCGImageSourceThumbnailMaxPixelSize: max(1, long),
        kCGImageSourceShouldCacheImmediately: true,
    ]
    guard var image = CGImageSourceCreateThumbnailAtIndex(src, 0, options as CFDictionary) else { throw undecodable }
    // A tag this app reads and ImageIO did not act on.
    if tagged == 1, detectFormat(data) != nil {
        let own = orientationOf(data)
        if own != 1 { image = try oriented(image, orientation: own) }
    }
    return DecodedImage(image: image, sample: sample, alpha: alpha)
}

/// Encodes as PNG or as JPEG at `quality` (1 to 100), with nothing but the pixels and their color profile.
func encodeImage(_ image: CGImage, png: Bool, quality: Int) throws -> Data {
    let failed = UserError("没能编码这张图片")
    let out = NSMutableData()
    let type = (png ? UTType.png : UTType.jpeg).identifier as CFString
    guard let dest = CGImageDestinationCreateWithData(out, type, 1, nil) else { throw failed }
    let options: [CFString: Any] = png ? [:] : [kCGImageDestinationLossyCompressionQuality: Double(quality) / 100]
    CGImageDestinationAddImage(dest, image, options as CFDictionary)
    guard CGImageDestinationFinalize(dest) else { throw failed }
    // ImageIO writes a little Exif of its own (the size, the color space): out it goes with the rest.
    return png ? try stripPng(out as Data) : try stripJpeg(out as Data)
}

/// Prepares one picked image, here and now. Compressed: upright, long side
/// at most 2048, JPEG, or PNG when it has transparency; re-encoding leaves
/// every bit of metadata behind. Original: the file as it is, its metadata
/// taken out; a photo turned by its orientation tag, or a format the CLIs
/// do not read (HEIC), is re-encoded at the highest quality instead, and
/// `Processed.note` says so. `budget` is the most pixels to decode, the
/// phone's own when nil.
public func prepareImageNow(_ data: Data, original: Bool, budget: Int64? = nil) throws -> Processed {
    try checkPicked(bytes: Int64(data.count), original: original)
    let tooLarge = ImageTooLarge("原图超过 \(maxOriginalBytes / 1024 / 1024) MB")
    if original {
        if let clean = try stripOriginal(data), let format = detectFormat(clean) {
            if Int64(clean.count) > maxOriginalBytes { throw tooLarge }
            let size = imagePixelSize(clean)
            return Processed(bytes: clean, format: format, width: size?.w ?? 0, height: size?.h ?? 0)
        }
        let turned = detectFormat(data) != nil
        let d = try decodeUpright(data, maxLong: 0, budget: budget)
        let png = d.alpha
        let out = try encodeImage(d.image, png: png, quality: turned ? 100 : 95)
        if Int64(out.count) > maxOriginalBytes { throw tooLarge }
        return Processed(
            bytes: out, format: png ? .png : .jpeg, width: d.image.width, height: d.image.height,
            note: originalNote(turned: turned, png: png, sample: d.sample))
    }
    let d = try decodeUpright(data, maxLong: compressedLongSide, budget: budget)
    var image = d.image
    let png = d.alpha
    var quality = 85
    var out = try encodeImage(image, png: png, quality: quality)
    while let step = nextCompressStep(bytes: out.count, quality: quality, longSide: max(image.width, image.height), png: png) {
        switch step {
        case .quality(let q): quality = q
        case .shrink: image = try scaled(image, width: shrunkSide(image.width), height: shrunkSide(image.height))
        }
        out = try encodeImage(image, png: png, quality: quality)
    }
    return Processed(bytes: out, format: png ? .png : .jpeg, width: image.width, height: image.height)
}

/// One image is prepared at a time, off whatever thread asked.
private let preparing = DispatchQueue(label: "remote-agent.image-prepare", qos: .userInitiated)

/// Prepares one picked image (`prepareImageNow`) after the ones asked for before it.
public func prepareImage(_ data: Data, original: Bool) async throws -> Processed {
    try await withCheckedThrowingContinuation { done in
        preparing.async {
            done.resume(with: Result { try autoreleasepool { try prepareImageNow(data, original: original) } })
        }
    }
}

/// A small picture of encoded image bytes, upright, for a thumbnail: decoded at that size, not from the whole picture.
public func thumbnailImage(_ data: Data, maxLong: Int = 256) -> CGImage? {
    guard let (src, _, _, _, _) = source(data) else { return nil }
    let options: [CFString: Any] = [
        kCGImageSourceCreateThumbnailFromImageAlways: true,
        kCGImageSourceCreateThumbnailWithTransform: true,
        kCGImageSourceThumbnailMaxPixelSize: maxLong,
        kCGImageSourceShouldCacheImmediately: true,
    ]
    return CGImageSourceCreateThumbnailAtIndex(src, 0, options as CFDictionary)
}
