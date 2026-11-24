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
        let (hw, hh) = (CGFloat(w / 2), CGFloat(h / 2))
        let parts: [([UInt8], CGRect)] = [
            (red, CGRect(x: 0, y: hh, width: hw, height: CGFloat(h) - hh)),
            (green, CGRect(x: hw, y: hh, width: CGFloat(w) - hw, height: CGFloat(h) - hh)),
            (white, CGRect(x: 0, y: 0, width: hw, height: hh)),
            (blue, CGRect(x: hw, y: 0, width: CGFloat(w) - hw, height: hh)),
        ]
        for (c, rect) in parts {
            ctx.setFillColor(red: CGFloat(c[0]) / 255, green: CGFloat(c[1]) / 255, blue: CGFloat(c[2]) / 255, alpha: 1)
            ctx.fill(rect)
        }
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

/// Platform image decoding, orientation correction, EXIF stripping, and Core-parameterized compression.
@Suite(.timeLimit(.minutes(2))) struct ImagePrepareTests {
    private typealias P = TestPictures
    private static let testOriginalMaxBytes: Int64 = 20 * 1024 * 1024
    private static let testMaxDimensionPx = 2048
    private static let testMaxCompressedBytes = 900 * 1024

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
        let target = Self.testMaxCompressedBytes
        let over = target + 1
        #expect(nextCompressStep(bytes: target, quality: 85, longSide: 2048, png: false, targetBytes: target) == nil)
        #expect(nextCompressStep(bytes: over, quality: 85, longSide: 2048, png: false, targetBytes: target) == .quality(75))
        #expect(nextCompressStep(bytes: over, quality: 75, longSide: 2048, png: false, targetBytes: target) == .quality(65))
        #expect(nextCompressStep(bytes: over, quality: 65, longSide: 2048, png: false, targetBytes: target) == .quality(55))
        #expect(nextCompressStep(bytes: over, quality: 55, longSide: 2048, png: false, targetBytes: target) == .shrink)
        #expect(nextCompressStep(bytes: over, quality: 55, longSide: 800, png: false, targetBytes: target) == nil)
        #expect(nextCompressStep(bytes: over, quality: 85, longSide: 2048, png: true, targetBytes: target) == .shrink)
        #expect(nextCompressStep(bytes: over, quality: 85, longSide: 10, png: true, targetBytes: target) == .shrink)
        #expect(shrunkSide(2048) == 1638 && shrunkSide(1) == 1)
    }

    @Test func aFileTooLargeToReadIsRefusedWithDimensionsKind() throws {
        try checkPicked(bytes: 80 * 1024 * 1024, original: false, originalMaxBytes: Self.testOriginalMaxBytes)
        #expect(throws: ImageTooLarge(L10nMessage.imageIosImageExceedsMb(count: 80), ioKind: .dimensions)) {
            try checkPicked(bytes: 80 * 1024 * 1024 + 1, original: false, originalMaxBytes: Self.testOriginalMaxBytes)
        }
        try checkPicked(bytes: 60 * 1024 * 1024, original: true, originalMaxBytes: Self.testOriginalMaxBytes)
        #expect(throws: ImageTooLarge(L10nMessage.imageIosImageExceedsMb(count: 60), ioKind: .dimensions)) {
            try checkPicked(bytes: 60 * 1024 * 1024 + 1, original: true, originalMaxBytes: Self.testOriginalMaxBytes)
        }
    }

    @Test func aPickedFileTooLargeIsRefusedBeforeItIsRead() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("ra-picked-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        let small = dir.appendingPathComponent("small.png")
        try Data("picked".utf8).write(to: small)
        #expect(try readPicked(small) == Data("picked".utf8))
        let large = dir.appendingPathComponent("large.tiff")
        #expect(FileManager.default.createFile(atPath: large.path, contents: nil))
        let handle = try FileHandle(forWritingTo: large)
        try handle.truncate(atOffset: UInt64(pickedReadLimit(original: false, originalMaxBytes: Self.testOriginalMaxBytes)) + 1)
        try handle.close()
        #expect(throws: ImageTooLarge(L10nMessage.imageIosImageExceedsMb(count: 80), ioKind: .dimensions)) { try readPicked(large) }
        try FileHandle(forWritingTo: large).truncate(atOffset: UInt64(pickedReadLimit(original: false, originalMaxBytes: Self.testOriginalMaxBytes)))
        #expect(try readPicked(large).count == 80 * 1024 * 1024)
    }

    @Test func thePixelBudgetHasACapAndFollowsMemory() {
        #expect(pixelBudget(physicalMemory: 8 << 30) == maxDecodePixels)
        #expect(pixelBudget(physicalMemory: 1 << 30) == Int64(1 << 30) / 64)
    }

    @Test func aPhotoIsShrunkToCoreDimensionTurnedUprightAndLosesItsLocation() throws {
        let photo = P.encode(P.quarters(w: 4000, h: 3000), .jpeg, orientation: 6, gps: true)
        #expect(P.properties(photo)[kCGImagePropertyGPSDictionary] != nil)
        #expect(jpegOrientation(photo) == 6)

        let p = try prepareImageNow(
            photo, original: false, originalMaxBytes: Self.testOriginalMaxBytes,
            maxDimensionPx: Self.testMaxDimensionPx, maxCompressedBytes: Self.testMaxCompressedBytes
        )
        #expect(p.format == .jpeg && detectFormat(p.bytes) == .jpeg)
        #expect(p.width == 1536 && p.height == 2048)
        #expect(p.bytes.count <= Self.testMaxCompressedBytes)
        #expect(P.corners(P.decoded(p.bytes)) == Self.upright[6])
        #expect(jpegOrientation(p.bytes) == 1)
        let props = P.properties(p.bytes)
        #expect(props[kCGImagePropertyGPSDictionary] == nil)
        #expect((props[kCGImagePropertyExifDictionary] as? [CFString: Any])?[kCGImagePropertyExifUserComment] == nil)
        #expect(try stripJpeg(p.bytes) == p.bytes)
        #expect(p.bytes.range(of: Data("Exif".utf8)) == nil && p.bytes.range(of: Data("taken at home".utf8)) == nil)

        // Custom Core maxDimensionPx is obeyed instead of any hardcoded shell constant.
        let custom = try prepareImageNow(
            photo, original: false, originalMaxBytes: Self.testOriginalMaxBytes,
            maxDimensionPx: 1024, maxCompressedBytes: Self.testMaxCompressedBytes
        )
        #expect(custom.width == 768 && custom.height == 1024)
    }

    @Test func everyOrientationComesOutUpright() throws {
        for o in 1...8 {
            let p = try prepareImageNow(
                P.encode(P.quarters(w: 60, h: 40), .jpeg, orientation: o, quality: 1),
                original: false, originalMaxBytes: Self.testOriginalMaxBytes,
                maxDimensionPx: Self.testMaxDimensionPx, maxCompressedBytes: Self.testMaxCompressedBytes
            )
            #expect(P.corners(P.decoded(p.bytes)) == Self.upright[o], "orientation \(o)")
            #expect((p.width, p.height) == (o >= 5 ? (40, 60) : (60, 40)), "orientation \(o)")
        }
    }

    @Test func turningByHandAgreesWithImageIO() throws {
        let stored = P.quarters(w: 6, h: 4)
        for o in 1...8 {
            let mine = try oriented(stored, orientation: o)
            #expect(P.corners(mine) == Self.upright[o], "orientation \(o)")
            let theirs = try decodeUpright(P.encode(stored, .tiff, orientation: o), maxLong: 0).image
            #expect((mine.width, mine.height) == (theirs.width, theirs.height), "orientation \(o)")
            #expect(P.corners(mine) == P.corners(theirs), "orientation \(o)")
        }
    }

    @Test func aTagImageIODoesNotActOnStillTurnsThePicture() throws {
        let plain = try stripPng(P.encode(P.quarters(w: 60, h: 40), .png))
        #expect(pngOrientation(plain) == 1)
        let tiff = Data("MM".utf8) + Data([0, 42, 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, 6, 0, 0, 0, 0, 0, 0])
        let body = Data("eXIf".utf8) + tiff
        let crc = body.withUnsafeBytes { UInt32(crc32(0, $0.bindMemory(to: UInt8.self).baseAddress, UInt32($0.count))) }
        let chunk = Data([0, 0, 0, UInt8(tiff.count)]) + body + withUnsafeBytes(of: crc.bigEndian) { Data($0) }
        let idat = try #require(plain.range(of: Data("IDAT".utf8))).lowerBound - 4
        let tagged = plain.prefix(idat) + chunk + plain.suffix(from: idat)
        #expect(pngOrientation(tagged) == 6)

        let p = try prepareImageNow(
            tagged, original: false, originalMaxBytes: Self.testOriginalMaxBytes,
            maxDimensionPx: Self.testMaxDimensionPx, maxCompressedBytes: Self.testMaxCompressedBytes
        )
        #expect(P.corners(P.decoded(p.bytes)) == Self.upright[6])
        #expect((p.width, p.height) == (40, 60))
        #expect(orientationOf(p.bytes) == 1)
    }

    @Test func transparencyKeepsAPngAndEverythingElseBecomesAJpeg() throws {
        let clear = try prepareImageNow(
            P.encode(P.quarters(w: 64, h: 48, alpha: true), .png),
            original: false, originalMaxBytes: Self.testOriginalMaxBytes,
            maxDimensionPx: Self.testMaxDimensionPx, maxCompressedBytes: Self.testMaxCompressedBytes
        )
        #expect(clear.format == .png && detectFormat(clear.bytes) == .png)
        #expect(try stripPng(clear.bytes) == clear.bytes)
        let opaque = try prepareImageNow(
            P.encode(P.quarters(w: 64, h: 48), .png),
            original: false, originalMaxBytes: Self.testOriginalMaxBytes,
            maxDimensionPx: Self.testMaxDimensionPx, maxCompressedBytes: Self.testMaxCompressedBytes
        )
        #expect(opaque.format == .jpeg)
        let tiff = try prepareImageNow(
            P.encode(P.quarters(w: 64, h: 48), .tiff),
            original: false, originalMaxBytes: Self.testOriginalMaxBytes,
            maxDimensionPx: Self.testMaxDimensionPx, maxCompressedBytes: Self.testMaxCompressedBytes
        )
        #expect(tiff.format == .jpeg && (tiff.width, tiff.height) == (64, 48))
    }

    @Test func anOriginalGoesAsItIsWithoutItsMetadataAndEnforcesCoreLimit() throws {
        let photo = P.encode(P.quarters(w: 400, h: 300), .jpeg, gps: true)
        let p = try prepareImageNow(
            photo, original: true, originalMaxBytes: Self.testOriginalMaxBytes,
            maxDimensionPx: Self.testMaxDimensionPx, maxCompressedBytes: Self.testMaxCompressedBytes
        )
        #expect(p.bytes == (try stripJpeg(photo)))
        #expect(p.format == .jpeg && (p.width, p.height) == (400, 300))
        #expect(P.properties(p.bytes)[kCGImagePropertyGPSDictionary] == nil)
        #expect(throws: ImageTooLarge.self) {
            try prepareImageNow(
                photo, original: true, originalMaxBytes: Int64(p.bytes.count - 1),
                maxDimensionPx: Self.testMaxDimensionPx, maxCompressedBytes: Self.testMaxCompressedBytes
            )
        }
    }

    @Test func anOriginalTurnedByItsTagIsReencodedAtItsOwnSize() throws {
        let photo = P.encode(P.quarters(w: 400, h: 300), .jpeg, orientation: 8, gps: true)
        let p = try prepareImageNow(
            photo, original: true, originalMaxBytes: Self.testOriginalMaxBytes,
            maxDimensionPx: Self.testMaxDimensionPx, maxCompressedBytes: Self.testMaxCompressedBytes
        )
        #expect((p.width, p.height) == (300, 400))
        #expect(P.corners(P.decoded(p.bytes)) == Self.upright[8])
        #expect(jpegOrientation(p.bytes) == 1)
        #expect(P.properties(p.bytes)[kCGImagePropertyGPSDictionary] == nil)
    }

    @Test func aHeicPhotoIsConverted() throws {
        let out = NSMutableData()
        guard let dest = CGImageDestinationCreateWithData(out, UTType.heic.identifier as CFString, 1, nil) else { return }
        CGImageDestinationAddImage(dest, P.quarters(w: 640, h: 480), [kCGImagePropertyOrientation: 6] as CFDictionary)
        guard CGImageDestinationFinalize(dest) else { return }
        let heic = out as Data
        #expect(detectFormat(heic) == nil)
        let p = try prepareImageNow(
            heic, original: false, originalMaxBytes: Self.testOriginalMaxBytes,
            maxDimensionPx: Self.testMaxDimensionPx, maxCompressedBytes: Self.testMaxCompressedBytes
        )
        #expect(p.format == .jpeg && (p.width, p.height) == (480, 640))
        #expect(P.corners(P.decoded(p.bytes)) == Self.upright[6])
    }

    @Test func aPictureOverThePixelBudgetIsSampledDownNotDecodedWhole() throws {
        let big = P.encode(P.quarters(w: 4000, h: 3000), .tiff)
        let p = try prepareImageNow(
            big, original: true, originalMaxBytes: Self.testOriginalMaxBytes,
            maxDimensionPx: Self.testMaxDimensionPx, maxCompressedBytes: Self.testMaxCompressedBytes,
            budget: 4_000_000
        )
        #expect((p.width, p.height) == (2000, 1500))
        let d = try decodeUpright(big, maxLong: Self.testMaxDimensionPx, budget: 1_000_000)
        #expect(d.sample == 4 && (d.image.width, d.image.height) == (1000, 750))
    }

    @Test func whatIsNotAnImageIsRefusedAndMappedToDecodeKind() {
        #expect(throws: UserError(L10nMessage.imageIosCannotDecodeThisImage())) {
            try prepareImageNow(
                Data("hello, not a picture".utf8), original: false, originalMaxBytes: Self.testOriginalMaxBytes,
                maxDimensionPx: Self.testMaxDimensionPx, maxCompressedBytes: Self.testMaxCompressedBytes
            )
        }
        #expect(attachmentIOErrorKind(for: UserError("bad", problemKind: .decode)) == .decode)
        #expect(attachmentIOErrorKind(for: NSError(domain: NSCocoaErrorDomain, code: NSFileWriteOutOfSpaceError)) == .storageFull)
        #expect(attachmentIOErrorKind(for: NSError(domain: NSPOSIXErrorDomain, code: Int(ENOSPC))) == .storageFull)
        #expect(attachmentIOErrorKind(for: NSError(domain: NSCocoaErrorDomain, code: NSFileReadNoSuchFileError)) == .sourceMissing)
    }

    @Test func aThumbnailIsSmallAndUpright() throws {
        let photo = P.encode(P.quarters(w: 1200, h: 800), .jpeg, orientation: 6)
        let t = try #require(thumbnailImage(photo, maxLong: 256))
        #expect(t.height == 256 && (170...171).contains(t.width))
        #expect(P.corners(t) == Self.upright[6])
        let thumbBytes = try #require(thumbnailData(photo, maxLong: 256))
        #expect(detectFormat(thumbBytes) == .jpeg)
        #expect(thumbnailImage(Data("nope".utf8)) == nil)
        #expect(imagePixelSize(photo).map { [$0.w, $0.h] } == [1200, 800])
    }
}
