import CoreGraphics
import Foundation
import ImageIO
import RAProto
import Testing
import UniformTypeIdentifiers
import zlib

@testable import RACore

/// Pictures made here, so the tests know every pixel and every tag of what goes in.
enum TestPictures {
    static let red: [UInt8] = [255, 0, 0, 255]
    static let green: [UInt8] = [0, 255, 0, 255]
    static let blue: [UInt8] = [0, 0, 255, 255]
    static let white: [UInt8] = [255, 255, 255, 255]

    /// A picture whose four quarters are red, green, blue and white, from the top left clockwise: no turn of it looks like another.
    static func quarters(w: Int, h: Int, alpha: Bool = false) -> CGImage {
        let info = alpha ? CGImageAlphaInfo.premultipliedLast : .noneSkipLast
        let ctx = CGContext(
            data: nil, width: w, height: h, bitsPerComponent: 8, bytesPerRow: w * 4, space: CGColorSpace(name: CGColorSpace.sRGB)!,
            bitmapInfo: info.rawValue)!
        // CoreGraphics counts rows from the bottom.
        let (hw, hh) = (CGFloat(w / 2), CGFloat(h / 2))
        let parts: [([UInt8], CGRect)] = [
            (red, CGRect(x: 0, y: hh, width: hw, height: CGFloat(h) - hh)), (green, CGRect(x: hw, y: hh, width: CGFloat(w) - hw, height: CGFloat(h) - hh)),
            (white, CGRect(x: 0, y: 0, width: hw, height: hh)), (blue, CGRect(x: hw, y: 0, width: CGFloat(w) - hw, height: hh)),
        ]
        for (c, rect) in parts {
            ctx.setFillColor(red: CGFloat(c[0]) / 255, green: CGFloat(c[1]) / 255, blue: CGFloat(c[2]) / 255, alpha: 1)
            ctx.fill(rect)
        }
        // One clear pixel is enough to make it a picture with transparency.
        if alpha { ctx.clear(CGRect(x: 0, y: h - 1, width: 1, height: 1)) }
        return ctx.makeImage()!
    }

    /// Encodes with the tags a camera would write: an orientation, and where the photo was taken.
    static func encode(_ image: CGImage, _ type: UTType, orientation: Int = 1, gps: Bool = false, quality: Double = 0.9) -> Data {
        let out = NSMutableData()
        let dest = CGImageDestinationCreateWithData(out, type.identifier as CFString, 1, nil)!
        var props: [CFString: Any] = [kCGImagePropertyOrientation: orientation, kCGImageDestinationLossyCompressionQuality: quality]
        if gps {
            props[kCGImagePropertyGPSDictionary] = [
                kCGImagePropertyGPSLatitude: 31.2304, kCGImagePropertyGPSLatitudeRef: "N",
                kCGImagePropertyGPSLongitude: 121.4737, kCGImagePropertyGPSLongitudeRef: "E",
            ]
            props[kCGImagePropertyExifDictionary] = [kCGImagePropertyExifUserComment: "taken at home"]
        }
        CGImageDestinationAddImage(dest, image, props as CFDictionary)
        #expect(CGImageDestinationFinalize(dest))
        return out as Data
    }

    /// The color at a place of a picture given as fractions of its sides, rounded to pure channels.
    static func color(_ image: CGImage, _ fx: Double, _ fy: Double) -> [UInt8] {
        let (w, h) = (image.width, image.height)
        let ctx = CGContext(
            data: nil, width: w, height: h, bitsPerComponent: 8, bytesPerRow: w * 4, space: CGColorSpace(name: CGColorSpace.sRGB)!,
            bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)!
        ctx.draw(image, in: CGRect(x: 0, y: 0, width: w, height: h))
        let px = ctx.data!.bindMemory(to: UInt8.self, capacity: w * h * 4)
        let at = (Int(Double(h) * fy) * w + Int(Double(w) * fx)) * 4
        return (0..<4).map { px[at + $0] > 127 ? 255 : 0 }
    }

    /// The four corners, top left first, clockwise.
    static func corners(_ image: CGImage) -> [[UInt8]] {
        [color(image, 0.2, 0.2), color(image, 0.8, 0.2), color(image, 0.8, 0.8), color(image, 0.2, 0.8)]
    }

    static func decoded(_ data: Data) -> CGImage {
        let src = CGImageSourceCreateWithData(data as CFData, nil)!
        return CGImageSourceCreateImageAtIndex(src, 0, nil)!
    }

    static func properties(_ data: Data) -> [CFString: Any] {
        let src = CGImageSourceCreateWithData(data as CFData, nil)!
        return CGImageSourceCopyPropertiesAtIndex(src, 0, nil) as? [CFString: Any] ?? [:]
    }
}

/// What Android's ui/ImageProcess.kt does to a picked image, held to docs/session-images.md.
@Suite struct ImagePrepareTests {
    private typealias P = TestPictures

    /// How a picture stored with each orientation looks once upright: the corners, top left first, clockwise.
    private static let upright: [Int: [[UInt8]]] = [
        1: [P.red, P.green, P.blue, P.white],
        2: [P.green, P.red, P.white, P.blue],
        3: [P.blue, P.white, P.red, P.green],
        4: [P.white, P.blue, P.green, P.red],
        5: [P.red, P.white, P.blue, P.green],
        6: [P.white, P.red, P.green, P.blue],
        7: [P.blue, P.green, P.red, P.white],
        8: [P.green, P.blue, P.white, P.red],
    ]

    @Test func theQualityGoesDownBeforeThePictureShrinks() {
        let over = compressedTargetBytes + 1
        #expect(nextCompressStep(bytes: compressedTargetBytes, quality: 85, longSide: 2048, png: false) == nil)
        #expect(nextCompressStep(bytes: over, quality: 85, longSide: 2048, png: false) == .quality(75))
        #expect(nextCompressStep(bytes: over, quality: 75, longSide: 2048, png: false) == .quality(65))
        #expect(nextCompressStep(bytes: over, quality: 65, longSide: 2048, png: false) == .quality(55))
        #expect(nextCompressStep(bytes: over, quality: 55, longSide: 2048, png: false) == .shrink)
        // Nothing more is tried once it is both at the lowest quality and small.
        #expect(nextCompressStep(bytes: over, quality: 55, longSide: 800, png: false) == nil)
        // A PNG has no quality to lower.
        #expect(nextCompressStep(bytes: over, quality: 85, longSide: 2048, png: true) == .shrink)
        #expect(nextCompressStep(bytes: over, quality: 85, longSide: 10, png: true) == .shrink)
        #expect(shrunkSide(2048) == 1638 && shrunkSide(1) == 1)
    }

    @Test func aFileTooLargeToReadIsRefusedInWords() throws {
        try checkPicked(bytes: 80 * 1024 * 1024, original: false)
        #expect(throws: ImageTooLarge("图片超过 80 MB")) { try checkPicked(bytes: 80 * 1024 * 1024 + 1, original: false) }
        try checkPicked(bytes: 60 * 1024 * 1024, original: true)
        #expect(throws: ImageTooLarge("图片超过 60 MB")) { try checkPicked(bytes: 60 * 1024 * 1024 + 1, original: true) }
    }

    @Test func whatAnOriginalThatCouldNotGoAsItWasSays() {
        #expect(originalNote(turned: true, png: false, sample: 1) == "按拍摄方向转正后以最高画质重新编码")
        #expect(originalNote(turned: false, png: false, sample: 1) == "转成了 JPEG，保留原始尺寸")
        #expect(originalNote(turned: false, png: true, sample: 2) == "转成了 PNG，保留原始尺寸；像素超过手机能处理的上限，缩小到 1/2")
    }

    @Test func thePixelBudgetHasACapAndFollowsMemory() {
        #expect(pixelBudget(physicalMemory: 8 << 30) == maxDecodePixels)
        #expect(pixelBudget(physicalMemory: 1 << 30) == Int64(1 << 30) / 64)
    }

    @Test func aPhotoIsShrunkTurnedUprightAndLosesItsLocation() throws {
        // A phone's photo: stored on its side, tagged with where it was taken.
        let photo = P.encode(P.quarters(w: 4000, h: 3000), .jpeg, orientation: 6, gps: true)
        #expect(P.properties(photo)[kCGImagePropertyGPSDictionary] != nil)
        #expect(jpegOrientation(photo) == 6)

        let p = try prepareImageNow(photo, original: false)
        #expect(p.format == .jpeg && detectFormat(p.bytes) == .jpeg)
        #expect(p.width == 1536 && p.height == 2048)
        #expect(p.note.isEmpty)
        #expect(p.bytes.count <= compressedTargetBytes)
        // Upright in its pixels, with no tag left to say otherwise.
        #expect(P.corners(P.decoded(p.bytes)) == Self.upright[6])
        #expect(jpegOrientation(p.bytes) == 1)
        let props = P.properties(p.bytes)
        #expect(props[kCGImagePropertyGPSDictionary] == nil)
        #expect((props[kCGImagePropertyExifDictionary] as? [CFString: Any])?[kCGImagePropertyExifUserComment] == nil)
        // No Exif or XMP segment at all: what is left is what `stripJpeg` leaves.
        #expect(try stripJpeg(p.bytes) == p.bytes)
        #expect(p.bytes.range(of: Data("Exif".utf8)) == nil && p.bytes.range(of: Data("taken at home".utf8)) == nil)
    }

    @Test func everyOrientationComesOutUpright() throws {
        for o in 1...8 {
            let p = try prepareImageNow(P.encode(P.quarters(w: 60, h: 40), .jpeg, orientation: o, quality: 1), original: false)
            #expect(P.corners(P.decoded(p.bytes)) == Self.upright[o], "orientation \(o)")
            #expect((p.width, p.height) == (o >= 5 ? (40, 60) : (60, 40)), "orientation \(o)")
        }
    }

    @Test func turningByHandAgreesWithImageIO() throws {
        let stored = P.quarters(w: 6, h: 4)
        for o in 1...8 {
            let mine = try oriented(stored, orientation: o)
            #expect(P.corners(mine) == Self.upright[o], "orientation \(o)")
            // The same file through ImageIO's own transform.
            let theirs = try decodeUpright(P.encode(stored, .tiff, orientation: o), maxLong: 0).image
            #expect((mine.width, mine.height) == (theirs.width, theirs.height), "orientation \(o)")
            #expect(P.corners(mine) == P.corners(theirs), "orientation \(o)")
        }
    }

    @Test func aTagImageIODoesNotActOnStillTurnsThePicture() throws {
        // A PNG with an eXIf chunk that says "rotate": spliced in by hand, before the image data.
        let plain = try stripPng(P.encode(P.quarters(w: 60, h: 40), .png))
        #expect(pngOrientation(plain) == 1)
        let tiff = Data("MM".utf8) + Data([0, 42, 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, 6, 0, 0, 0, 0, 0, 0])
        let body = Data("eXIf".utf8) + tiff
        let crc = body.withUnsafeBytes { UInt32(crc32(0, $0.bindMemory(to: UInt8.self).baseAddress, UInt32($0.count))) }
        let chunk = Data([0, 0, 0, UInt8(tiff.count)]) + body + withUnsafeBytes(of: crc.bigEndian) { Data($0) }
        let idat = try #require(plain.range(of: Data("IDAT".utf8))).lowerBound - 4
        let tagged = plain.prefix(idat) + chunk + plain.suffix(from: idat)
        #expect(pngOrientation(tagged) == 6)

        let p = try prepareImageNow(tagged, original: false)
        #expect(P.corners(P.decoded(p.bytes)) == Self.upright[6])
        #expect((p.width, p.height) == (40, 60))
        #expect(orientationOf(p.bytes) == 1)
    }

    @Test func transparencyKeepsAPngAndEverythingElseBecomesAJpeg() throws {
        let clear = try prepareImageNow(P.encode(P.quarters(w: 64, h: 48, alpha: true), .png), original: false)
        #expect(clear.format == .png && detectFormat(clear.bytes) == .png)
        #expect(try stripPng(clear.bytes) == clear.bytes)
        let opaque = try prepareImageNow(P.encode(P.quarters(w: 64, h: 48), .png), original: false)
        #expect(opaque.format == .jpeg)
        // A format the CLIs do not read is converted in the same step.
        let tiff = try prepareImageNow(P.encode(P.quarters(w: 64, h: 48), .tiff), original: false)
        #expect(tiff.format == .jpeg && (tiff.width, tiff.height) == (64, 48))
    }

    @Test func aSmallPictureIsNotEnlarged() throws {
        let p = try prepareImageNow(P.encode(P.quarters(w: 300, h: 200), .jpeg), original: false)
        #expect((p.width, p.height) == (300, 200))
    }

    @Test func anOriginalGoesAsItIsWithoutItsMetadata() throws {
        let photo = P.encode(P.quarters(w: 400, h: 300), .jpeg, gps: true)
        let p = try prepareImageNow(photo, original: true)
        // The file's own bytes, less the metadata: not re-encoded.
        #expect(p.bytes == (try stripJpeg(photo)))
        #expect(p.format == .jpeg && (p.width, p.height) == (400, 300) && p.note.isEmpty)
        #expect(P.properties(p.bytes)[kCGImagePropertyGPSDictionary] == nil)
    }

    @Test func anOriginalTurnedByItsTagIsReencodedAtItsOwnSize() throws {
        let photo = P.encode(P.quarters(w: 400, h: 300), .jpeg, orientation: 8, gps: true)
        let p = try prepareImageNow(photo, original: true)
        #expect((p.width, p.height) == (300, 400))
        #expect(p.note == "按拍摄方向转正后以最高画质重新编码")
        #expect(P.corners(P.decoded(p.bytes)) == Self.upright[8])
        #expect(jpegOrientation(p.bytes) == 1)
        #expect(P.properties(p.bytes)[kCGImagePropertyGPSDictionary] == nil)
    }

    @Test func anOriginalInAFormatTheCLIsDoNotReadIsConverted() throws {
        let p = try prepareImageNow(P.encode(P.quarters(w: 400, h: 300), .tiff, gps: true), original: true)
        #expect(p.format == .jpeg && (p.width, p.height) == (400, 300))
        #expect(p.note == "转成了 JPEG，保留原始尺寸")
        #expect(P.properties(p.bytes)[kCGImagePropertyGPSDictionary] == nil)
    }

    @Test func aHeicPhotoIsConverted() throws {
        // Not every Mac can write HEIC; one that cannot has nothing to check here.
        let out = NSMutableData()
        guard let dest = CGImageDestinationCreateWithData(out, UTType.heic.identifier as CFString, 1, nil) else { return }
        CGImageDestinationAddImage(dest, P.quarters(w: 640, h: 480), [kCGImagePropertyOrientation: 6] as CFDictionary)
        guard CGImageDestinationFinalize(dest) else { return }
        let heic = out as Data
        #expect(detectFormat(heic) == nil)
        let p = try prepareImageNow(heic, original: false)
        #expect(p.format == .jpeg && (p.width, p.height) == (480, 640))
        #expect(P.corners(P.decoded(p.bytes)) == Self.upright[6])
    }

    @Test func aPictureOverThePixelBudgetIsSampledDownNotDecodedWhole() throws {
        let big = P.encode(P.quarters(w: 4000, h: 3000), .tiff)
        // A budget this picture does not fit: an original is sent at a fraction of its size, and says so.
        let p = try prepareImageNow(big, original: true, budget: 4_000_000)
        #expect((p.width, p.height) == (2000, 1500))
        #expect(p.note == "转成了 JPEG，保留原始尺寸；像素超过手机能处理的上限，缩小到 1/2")
        let d = try decodeUpright(big, maxLong: compressedLongSide, budget: 1_000_000)
        #expect(d.sample == 4 && (d.image.width, d.image.height) == (1000, 750))
    }

    @Test func whatIsNotAnImageIsRefused() {
        #expect(throws: UserError("解不出这张图片")) { try prepareImageNow(Data("hello, not a picture".utf8), original: false) }
        #expect(throws: UserError("解不出这张图片")) { try prepareImageNow(Data(), original: true) }
        // A JPEG cut short is a JPEG that cannot be read.
        let cut = P.encode(P.quarters(w: 40, h: 30), .jpeg).prefix(40)
        #expect(throws: (any Error).self) { try prepareImageNow(Data(cut), original: true) }
    }

    @Test func imagesArePreparedOneAfterAnother() async throws {
        let photo = P.encode(P.quarters(w: 800, h: 600), .jpeg, orientation: 3)
        async let a = prepareImage(photo, original: false)
        async let b = prepareImage(photo, original: true)
        let (compressed, original) = try await (a, b)
        #expect((compressed.width, compressed.height) == (800, 600))
        #expect(original.note == "按拍摄方向转正后以最高画质重新编码")
    }

    @Test func aThumbnailIsSmallAndUpright() throws {
        let photo = P.encode(P.quarters(w: 1200, h: 800), .jpeg, orientation: 6)
        let t = try #require(thumbnailImage(photo, maxLong: 256))
        #expect(t.height == 256 && (170...171).contains(t.width))
        #expect(P.corners(t) == Self.upright[6])
        #expect(thumbnailImage(Data("nope".utf8)) == nil)
        #expect(imagePixelSize(photo).map { [$0.w, $0.h] } == [1200, 800])
    }
}
