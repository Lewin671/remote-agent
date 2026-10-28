import CoreGraphics
import Foundation
import ImageIO
import RAProto
import Testing

@testable import RACore

/// Pictures for the tests, made and read back with the system's own codecs
/// where Android's tests use javax.imageio.
enum Pictures {
    static func encode(_ image: CGImage, _ type: String, progressive: Bool = false) -> Data {
        let out = NSMutableData()
        let dest = CGImageDestinationCreateWithData(out, type as CFString, 1, nil)!
        var props = [CFString: Any]()
        if progressive { props[kCGImagePropertyJFIFDictionary] = [kCGImagePropertyJFIFIsProgressive: true] }
        CGImageDestinationAddImage(dest, image, props as CFDictionary)
        precondition(CGImageDestinationFinalize(dest))
        return out as Data
    }

    static func draw(w: Int, h: Int, _ color: (Int, Int) -> (UInt8, UInt8, UInt8)) -> CGImage {
        var px = [UInt8](repeating: 255, count: w * h * 4)
        for y in 0..<h {
            for x in 0..<w {
                let (r, g, b) = color(x, y)
                px[(y * w + x) * 4] = r
                px[(y * w + x) * 4 + 1] = g
                px[(y * w + x) * 4 + 2] = b
            }
        }
        let ctx = CGContext(
            data: &px, width: w, height: h, bitsPerComponent: 8, bytesPerRow: w * 4, space: CGColorSpaceCreateDeviceRGB(),
            bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue)!
        return ctx.makeImage()!
    }

    /// Red on the left half, blue on the right, as Android's picture().
    static func halves(_ type: String, w: Int = 40, h: Int = 20) -> Data {
        encode(draw(w: w, h: h) { x, _ in x < w / 2 ? (255, 0, 0) : (0, 0, 255) }, type)
    }

    static func gradient(progressive: Bool) -> Data {
        encode(draw(w: 64, h: 48) { x, y in (UInt8(x * 4 & 0xff), UInt8(y * 5 & 0xff), 90) }, "public.jpeg", progressive: progressive)
    }

    static func decode(_ data: Data) -> CGImage? {
        guard let src = CGImageSourceCreateWithData(data as CFData, nil) else { return nil }
        return CGImageSourceCreateImageAtIndex(src, 0, nil)
    }

    /// The decoded pixels, to tell that two files are the same picture.
    static func pixels(_ data: Data) -> [UInt8]? {
        guard let img = decode(data) else { return nil }
        var px = [UInt8](repeating: 0, count: img.width * img.height * 4)
        let ctx = CGContext(
            data: &px, width: img.width, height: img.height, bitsPerComponent: 8, bytesPerRow: img.width * 4,
            space: CGColorSpaceCreateDeviceRGB(), bitmapInfo: CGImageAlphaInfo.noneSkipLast.rawValue)!
        ctx.draw(img, in: CGRect(x: 0, y: 0, width: img.width, height: img.height))
        return px
    }
}

private func contains(_ hay: Data, _ needle: String) -> Bool { hay.range(of: Data(needle.utf8)) != nil }

/// Where the first start-of-scan marker is, from `from` on.
private func sos(_ b: Data, from: Int = 0) -> Int? {
    let bytes = [UInt8](b)
    var i = from
    while i + 1 < bytes.count {
        if bytes[i] == 0xFF && bytes[i + 1] == 0xDA { return i }
        i += 1
    }
    return nil
}

private func scans(_ b: Data) -> Int {
    var n = 0
    var at = 0
    while let i = sos(b, from: at) {
        n += 1
        at = i + 2
    }
    return n
}

private func be16(_ n: Int) -> [UInt8] { [UInt8(n >> 8 & 0xff), UInt8(n & 0xff)] }
private func be32(_ n: Int) -> [UInt8] { [UInt8(n >> 24 & 0xff), UInt8(n >> 16 & 0xff), UInt8(n >> 8 & 0xff), UInt8(n & 0xff)] }
private func le32(_ n: Int) -> [UInt8] { be32(n).reversed() }

/// A JPEG segment: marker, length, payload.
private func seg(_ marker: UInt8, _ payload: Data) -> Data { Data([0xFF, marker] + be16(payload.count + 2)) + payload }

/// A big-endian TIFF block with the orientation and a GPS-looking tag, as a camera writes.
private func tiff(_ orientation: UInt8) -> Data {
    var o = Data([0x4D, 0x4D, 0, 42, 0, 0, 0, 8])
    o.append(contentsOf: [0, 2])
    o.append(contentsOf: [0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, orientation, 0, 0])
    // GPSInfo IFD pointer, pointing nowhere useful: only its presence matters here.
    o.append(contentsOf: [0x88, 0x25, 0, 4, 0, 0, 0, 1, 0, 0, 0, 0])
    o.append(contentsOf: [0, 0, 0, 0])
    o.append(Data("LOCATION 31.2304N 121.4737E".utf8))
    return o
}

private let exifHeader = Data("Exif\0\0".utf8)

/// Android's core ImagesTest.
@Suite struct ImagesTests {
    private func withSegment(_ jpeg: Data, _ marker: UInt8, _ payload: Data) -> Data {
        jpeg.prefix(2) + seg(marker, payload) + jpeg.dropFirst(2)
    }

    private func exifJpeg(_ orientation: UInt8) -> Data {
        let withExif = withSegment(Pictures.halves("public.jpeg"), 0xE1, exifHeader + tiff(orientation))
        return withSegment(withExif, 0xFE, Data("shot on a phone".utf8))
    }

    @Test func readsTheOrientationOfAJpeg() {
        #expect(jpegOrientation(exifJpeg(6)) == 6)
        #expect(jpegOrientation(Pictures.halves("public.jpeg")) == 1)
        #expect(orientationOf(exifJpeg(6)) == 6)
    }

    @Test func aTurnedPhotoIsNotSentAsIs() throws {
        // Stripping Exif from it would send it sideways: pixels must turn first.
        #expect(try stripOriginal(exifJpeg(6)) == nil)
        #expect(swapsSides(orientation: 6))
        #expect(!swapsSides(orientation: 3))
    }

    @Test func strippingAJpegKeepsItsPixelsAndDropsLocationAndComments() throws {
        let src = exifJpeg(1)
        #expect(contains(src, "LOCATION"))
        let out = try #require(try stripOriginal(src))
        #expect(!contains(out, "LOCATION"))
        #expect(!contains(out, "shot on a phone"))
        #expect(!contains(out, "Exif"))
        // The same image data, byte for byte, from the start of scan on.
        let a = try #require(sos(src))
        let b = try #require(sos(out))
        #expect(src.dropFirst(a) == out.dropFirst(b))
        #expect(Pictures.decode(out)?.width == 40)
        #expect(contains(out, "JFIF"))
    }

    private func pngChunk(_ type: String, _ data: Data) -> Data {
        let td = Data(type.utf8) + data
        return Data(be32(data.count)) + td + Data(be32(Int(crc32(td))))
    }

    private func crc32(_ data: Data) -> UInt32 {
        var crc: UInt32 = 0xffff_ffff
        for b in data {
            crc ^= UInt32(b)
            for _ in 0..<8 { crc = crc & 1 == 1 ? crc >> 1 ^ 0xedb8_8320 : crc >> 1 }
        }
        return ~crc
    }

    private func pngWith(_ chunks: Data...) -> Data {
        let base = Pictures.halves("public.png")
        // After the signature and IHDR (8 + 25 bytes).
        return base.prefix(33) + chunks.reduce(Data(), +) + base.dropFirst(33)
    }

    @Test func strippingAPngDropsTextAndExif() throws {
        let src = pngWith(pngChunk("tEXt", Data("Comment\0taken at home".utf8)), pngChunk("eXIf", tiff(1)))
        let out = try #require(try stripOriginal(src))
        #expect(!contains(out, "taken at home"))
        #expect(!contains(out, "LOCATION"))
        #expect(Pictures.decode(out)?.width == 40)
        #expect(pngOrientation(pngWith(pngChunk("eXIf", tiff(6)))) == 6)
        #expect(try stripOriginal(pngWith(pngChunk("eXIf", tiff(6)))) == nil)
    }

    private func riffChunk(_ type: String, _ data: Data) -> Data {
        Data(type.utf8) + Data(le32(data.count)) + data + (data.count % 2 == 1 ? Data([0]) : Data())
    }

    private func webp(_ chunks: Data...) -> Data {
        let body = chunks.reduce(Data(), +)
        return Data("RIFF".utf8) + Data(le32(body.count + 4)) + Data("WEBP".utf8) + body
    }

    @Test func strippingAWebpFixesItsFlagsAndSize() throws {
        var vp8x = Data(count: 10)
        vp8x[0] = 0x0C  // Exif and XMP flags
        let image = Data(repeating: 7, count: 15)
        let src = webp(riffChunk("VP8X", vp8x), riffChunk("VP8L", image), riffChunk("EXIF", tiff(1)), riffChunk("XMP ", Data("<x:xmpmeta/>".utf8)))
        #expect(detectFormat(src) == .webp)
        let out = [UInt8](try stripWebp(src))
        #expect(!contains(Data(out), "LOCATION"))
        #expect(!contains(Data(out), "xmpmeta"))
        #expect(out.count - 8 == Int(out[4]) | Int(out[5]) << 8)
        #expect(out[20] & 0x0C == 0)
        #expect(contains(Data(out), "VP8L"))
        #expect(webpOrientation(webp(riffChunk("VP8X", vp8x), riffChunk("EXIF", tiff(6)))) == 6)
    }

    @Test func strippingAGifDropsCommentsAndKeepsFrames() throws {
        let base = Pictures.halves("com.compuserve.gif")
        // What the system's encoder writes of its own is not the test's: start from a GIF that is already bare.
        let bare = try stripGif(base)
        // Insert a comment block right after the header and palette.
        let flags = Int(bare[10])
        let headerEnd = 13 + (flags & 0x80 != 0 ? 3 * (1 << ((flags & 7) + 1)) : 0)
        let comment = Data([0x21, 0xFE, 9]) + Data("home, 5pm".utf8) + Data([0])
        let src = bare.prefix(headerEnd) + comment + bare.dropFirst(headerEnd)
        let out = try #require(try stripOriginal(src))
        #expect(!contains(out, "home, 5pm"))
        #expect(Pictures.decode(out) != nil)
        #expect(out.count == bare.count)
        #expect(Pictures.pixels(out) == Pictures.pixels(base))
    }

    @Test func formatsAreToldBySignatureOnly() throws {
        #expect(detectFormat(Pictures.halves("public.jpeg")) == .jpeg)
        #expect(detectFormat(Pictures.halves("public.png")) == .png)
        #expect(detectFormat(Pictures.halves("com.compuserve.gif")) == .gif)
        // HEIC is not one the CLIs read: it gets new pixels.
        var heic = Data(count: 16)
        heic.replaceSubrange(4..<12, with: Data("ftypheic".utf8))
        #expect(detectFormat(heic) == nil)
        #expect(try stripOriginal(heic) == nil)
    }

    @Test func aMessageTakesSixAtMostInPickOrder() {
        let (taken, left) = takePicked(have: 4, picked: ["a", "b", "c", "d"])
        #expect(taken == ["a", "b"])
        #expect(left == 2)
        let none = takePicked(have: 6, picked: ["x"])
        #expect(none.taken == [] && none.left == 1)
    }

    @Test func compressionShrinksTheLongSideOnly() {
        #expect(fitLongSide(w: 4032, h: 3024) == (2048, 1536))
        #expect(fitLongSide(w: 3024, h: 4032) == (1536, 2048))
        #expect(fitLongSide(w: 800, h: 600) == (800, 600))
    }

    // --- not in Android's tests ---

    @Test func theSharedFixturesReadAsTheWebClientReadsThem() throws {
        func fixture(_ name: String) throws -> Data { try Data(contentsOf: repoRoot.appendingPathComponent("web/src/testdata/images/\(name)")) }
        // They carry no Exif (the -oN ones are jpegtran's turned pixels, the web's oracle for rotation).
        #expect(jpegOrientation(try fixture("s420.jpg")) == 1)
        #expect(jpegOrientation(try fixture("s420-o6.jpg")) == 1)
        // Every kind of JPEG the web client is tested with strips to the same picture.
        for name in ["s420.jpg", "s422.jpg", "s444.jpg", "gray.jpg", "prog.jpg", "restart.jpg", "odd420.jpg"] {
            let src = try fixture(name)
            let out = try stripJpeg(src)
            #expect(Pictures.pixels(out) == Pictures.pixels(src), "\(name)")
            #expect(scans(out) == scans(src), "\(name)")
            // Stripping what is already bare changes nothing.
            #expect(try stripJpeg(out) == out, "\(name)")
        }
        #expect(detectFormat(try fixture("tiny.webp")) == .webp)
        #expect(try stripWebp(try fixture("tiny.webp")).count <= (try fixture("tiny.webp")).count)
    }

    @Test func fillBytesBeforeARestartMarkerAreScanData() throws {
        let src = try Data(contentsOf: repoRoot.appendingPathComponent("web/src/testdata/images/restart.jpg"))
        let scan = try #require(sos(src, from: 0))
        // The file has no 0xFF before its scan's data but in markers; in the data one is followed by 0x00 or a restart marker.
        var filled = Data(src[..<scan])
        var restarts = 0
        for i in scan..<src.count {
            if src[i] == 0xFF, i + 1 < src.count, (0xD0...0xD7).contains(src[i + 1]) {
                filled.append(contentsOf: [0xFF, 0xFF])
                restarts += 1
            }
            filled.append(src[i])
        }
        #expect(restarts == 11)
        let out = try stripJpeg(filled)
        #expect(out == filled)
        #expect(Pictures.pixels(out) == Pictures.pixels(src))
        // Fill bytes that run to the end of the file are no marker.
        #expect(throws: UserError.self) { try stripJpeg(Data(filled[..<scan]) + Data(repeating: 0xFF, count: 16)) }
    }

    @Test func filesCutShortAreRefusedNotReadPastTheirEnd() throws {
        // Kotlin's version gets array bounds exceptions for some of these; here every one is an error or a plain answer.
        let jpeg = exifJpeg(6)
        for n in stride(from: 0, to: jpeg.count, by: 7) {
            let cut = jpeg.prefix(n)
            _ = jpegOrientation(cut)
            _ = orientationOf(cut)
            #expect(throws: UserError.self) { try stripJpeg(Data(cut)) }
        }
        let png = pngWith(pngChunk("eXIf", tiff(6).prefix(9)))
        #expect(pngOrientation(png) == 1)
        for n in stride(from: 0, to: png.count, by: 5) {
            _ = pngOrientation(png.prefix(n))
            _ = try? stripPng(Data(png.prefix(n)))
        }
        let gif = try stripGif(Pictures.halves("com.compuserve.gif"))
        for n in 0..<gif.count {
            #expect(throws: UserError.self) { try stripGif(Data(gif.prefix(n))) }
        }
        // A GIF whose header promises a palette it does not have.
        #expect(throws: UserError("GIF 被截断")) { try stripGif(Data("GIF89a".utf8) + Data([1, 0, 1, 0, 0x87, 0, 0])) }
        #expect(throws: UserError("GIF 里有认不出的块")) { try stripGif(Data("GIF89a".utf8) + Data([1, 0, 1, 0, 0, 0, 0, 0x99])) }
        let webpFile = webp(riffChunk("EXIF", exifHeader + tiff(8)))
        #expect(webpOrientation(webpFile) == 8)
        for n in 0..<webpFile.count {
            _ = webpOrientation(webpFile.prefix(n))
            _ = try? stripWebp(Data(webpFile.prefix(n)))
        }
        // A TIFF block whose directory points past its end, or back before its start.
        #expect(tiffOrientation(Data([0x49, 0x49, 42, 0, 0xff, 0xff, 0xff, 0xff])) == 1)
        #expect(tiffOrientation(Data([0x4D, 0x4D, 0, 42, 0, 0, 0, 8, 0, 9])) == 1)
        #expect(tiffOrientation(tiff(3)) == 3)
        // A slice of a larger buffer is read from its own start.
        let padded = Data([9, 9, 9]) + exifJpeg(6)
        #expect(jpegOrientation(padded.dropFirst(3)) == 6)
        #expect(detectFormat(padded.dropFirst(3)) == .jpeg)
    }

    @Test func damagedFilesAreAnswersOrErrorsNeverAStop() throws {
        // Bytes changed at random, the same ones every run: a parser that trusted a length would read past the end and trap.
        var seed: UInt64 = 0x9E37_79B9_7F4A_7C15
        func next() -> Int {
            seed = seed &* 6_364_136_223_846_793_005 &+ 1_442_695_040_888_963_407
            return Int(seed >> 33)
        }
        var vp8x = Data(count: 10)
        vp8x[0] = 0x0C
        let files = [
            exifJpeg(6), Pictures.gradient(progressive: true), pngWith(pngChunk("eXIf", tiff(6)), pngChunk("tEXt", Data("a\0b".utf8))),
            Pictures.halves("com.compuserve.gif"), webp(riffChunk("VP8X", vp8x), riffChunk("EXIF", exifHeader + tiff(5)), riffChunk("VP8L", Data(count: 9))),
        ]
        for file in files {
            for _ in 0..<400 {
                var b = [UInt8](file)
                // Past the signature, so the format is still told and its parser runs.
                for _ in 0..<(1 + next() % 4) { b[12 + next() % (b.count - 12)] = UInt8(next() & 0xff) }
                let d = Data(b.prefix(1 + next() % b.count))
                _ = orientationOf(d)
                _ = try? stripOriginal(d)
                _ = try? stripJpeg(d)
                _ = try? stripPng(d)
                _ = try? stripWebp(d)
                _ = try? stripGif(d)
                _ = tiffOrientation(d)
            }
        }
    }
}

/// Android's UploadTest (in ImagesTest.kt).
@Suite struct UploadTests {
    private struct Refused: UploadFailure {
        let code: String
        let message: String
    }

    /// The daemon's side, as internal/daemon keeps it: bytes on disk, overlap must match.
    private actor FakeDaemon: UploadChannel {
        func beginFile(transferId: String, name: String, total: Int64, sha256: Data) async throws -> PBImageUploadStatus {
            throw UserError("unexpected file upload in image test")
        }
        func abort(transferId: String) async throws {}

        let maxChunk: Int32
        var stored = Data()
        var total: Int64 = 0
        var sha = Data()
        var done = false
        var failNextPutAfterStoring = false
        var stopNextPutAfterStoring = false
        var puts = 0
        var refuseComplete = false

        init(maxChunk: Int32 = 10) {
            self.maxChunk = maxChunk
        }

        func lose() { failNextPutAfterStoring = true }
        func stop() { stopNextPutAfterStoring = true }
        func refuse() { refuseComplete = true }

        private func st() -> PBImageUploadStatus {
            .with {
                $0.transferID = "t"
                $0.persistedBytes = Int64(stored.count)
                $0.totalBytes = total
                $0.maxChunkBytes = maxChunk
                $0.state = done ? .complete : .receiving
                $0.expiresMs = Int64.max
                $0.imageID = done ? "img1" : ""
            }
        }

        func begin(transferId: String, format: PBImageFormat, total: Int64, sha256: Data) async throws -> PBImageUploadStatus {
            self.total = total
            sha = sha256
            return st()
        }

        func put(transferId: String, offset: Int64, data: Data) async throws -> PBImageUploadStatus {
            puts += 1
            if offset > Int64(stored.count) { throw Refused(code: "failed_precondition", message: "gap") }
            let overlap = min(stored.count - Int(offset), data.count)
            if stored.subdata(in: Int(offset)..<(Int(offset) + overlap)) != data.prefix(overlap) {
                throw Refused(code: "failed_precondition", message: "changed")
            }
            stored.append(data.dropFirst(overlap))
            if failNextPutAfterStoring {
                failNextPutAfterStoring = false
                throw Refused(code: "unknown_outcome", message: "reply lost")
            }
            if stopNextPutAfterStoring {
                stopNextPutAfterStoring = false
                throw CommandError("aborted", "the daemon stopped", fromDaemon: true)
            }
            return st()
        }

        func status(transferId: String) async throws -> PBImageUploadStatus { st() }

        func complete(transferId: String) async throws -> PBImageUploadStatus {
            if refuseComplete { throw Refused(code: "invalid_argument", message: "not an image") }
            if Int64(stored.count) != total || sha256(stored) != sha { throw Refused(code: "data_loss", message: "hash") }
            done = true
            return st()
        }
    }

    private let png = Pictures.encode(Pictures.draw(w: 8, h: 8) { _, _ in (0, 0, 0) }, "public.png")

    @Test func sendsEveryChunkAndCompletes() async throws {
        let d = FakeDaemon()
        #expect(try await uploadImage(d, transferId: "t", data: png) == UploadedImage(imageId: "img1", expiresMs: Int64.max))
        #expect(await d.stored == png)
        #expect(await d.puts == (png.count + 9) / 10)
    }

    @Test func aLostReplyGoesOnFromWhatTheDaemonStored() async throws {
        let d = FakeDaemon()
        await d.lose()
        #expect(try await uploadImage(d, transferId: "t", data: png) == UploadedImage(imageId: "img1", expiresMs: Int64.max))
        #expect(await d.stored == png)
    }

    /// A daemon that stopped while storing a chunk answers "aborted" for it: the chunk may be there.
    @Test func aDaemonThatStoppedHalfwayGoesOnFromWhatItStored() async throws {
        let d = FakeDaemon()
        await d.stop()
        #expect(try await uploadImage(d, transferId: "t", data: png) == UploadedImage(imageId: "img1", expiresMs: Int64.max))
        #expect(await d.stored == png)
    }

    @Test func aRefusalIsAnErrorNotASuccess() async throws {
        let d = FakeDaemon()
        await d.refuse()
        await #expect(throws: Refused.self) { try await uploadImage(d, transferId: "t", data: png) }
    }

    // --- not in Android's tests ---

    @Test func progressIsSaidPerChunkAndAnUnknownOutcomeIsNotRetriedForever() async throws {
        let d = FakeDaemon(maxChunk: 0)
        var seen = [Int64]()
        // The daemon names no chunk size: the caller's is used.
        #expect(try await uploadImage(d, transferId: "t", data: png, maxChunk: 40) { done, _ in seen.append(done) } == UploadedImage(imageId: "img1", expiresMs: Int64.max))
        #expect(seen == Array(stride(from: 0, to: Int64(png.count), by: 40)) + [Int64(png.count)])

        struct AlwaysLost: UploadChannel {
        func beginFile(transferId: String, name: String, total: Int64, sha256: Data) async throws -> PBImageUploadStatus {
            throw UserError("unexpected file upload in image test")
        }
        func abort(transferId: String) async throws {}

            func begin(transferId: String, format: PBImageFormat, total: Int64, sha256: Data) async throws -> PBImageUploadStatus {
                throw Refused(code: "network", message: "down")
            }
            func put(transferId: String, offset: Int64, data: Data) async throws -> PBImageUploadStatus { PBImageUploadStatus() }
            func status(transferId: String) async throws -> PBImageUploadStatus { PBImageUploadStatus() }
            func complete(transferId: String) async throws -> PBImageUploadStatus { PBImageUploadStatus() }
        }
        await #expect(throws: Refused.self) { try await uploadImage(AlwaysLost(), transferId: "t", data: png, retries: 2) }
        // Not an image the CLIs read: nothing is sent.
        await #expect(throws: UserError("不支持的图片格式")) { try await uploadImage(d, transferId: "t", data: Data("hello".utf8)) }
        // A daemon that completes without naming the image.
        struct NoId: UploadChannel {
        func beginFile(transferId: String, name: String, total: Int64, sha256: Data) async throws -> PBImageUploadStatus {
            throw UserError("unexpected file upload in image test")
        }
        func abort(transferId: String) async throws {}

            func begin(transferId: String, format: PBImageFormat, total: Int64, sha256: Data) async throws -> PBImageUploadStatus {
                .with { $0.state = .complete
            $0.expiresMs = Int64.max }
            }
            func put(transferId: String, offset: Int64, data: Data) async throws -> PBImageUploadStatus { PBImageUploadStatus() }
            func status(transferId: String) async throws -> PBImageUploadStatus { PBImageUploadStatus() }
            func complete(transferId: String) async throws -> PBImageUploadStatus { PBImageUploadStatus() }
        }
        await #expect(throws: UploadError(code: "internal", message: "电脑没有给出图片 ID")) { try await uploadImage(NoId(), transferId: "t", data: png) }
    }
}

/// Android's SessionImagesTest (in ImagesTest.kt).
@Suite struct SessionImagesTests {
    private func ref(_ n: Int32, _ id: String? = nil) -> PBImageRef { .with { $0.imageID = id ?? "i\(n)"; $0.cliIndex = n } }

    @Test func placeholdersBecomeNumbersAndLeadingOnesGo() {
        let refs = [ref(1), ref(2)]
        #expect(imageText("[Image #1] [Image #2]这两个页面底部的按钮都点不到", refs: refs) == "这两个页面底部的按钮都点不到")
        #expect(imageText("[Image #1] 电脑上，[Image #2] 手机上", refs: refs) == "图 1 电脑上，图 2 手机上")
        // A placeholder without its image is left as typed.
        #expect(imageText("see [Image #9]", refs: refs) == "see [Image #9]")
        #expect(imageText("[Image #1] text", refs: []) == "[Image #1] text")
    }

    @Test func theViewerPagesThroughEveryImageInOrder() {
        let events = [
            Mk.ev(1, .userMessage(.with { $0.images = [ref(1), ref(2)] })),
            Mk.ev(2, .toolCall(.with { $0.title = "Read a.png"; $0.images = [ref(0, "t")] })),
            Mk.ev(3, .imageGenerated(.with { $0.images = [ref(0, "g")] })),
        ]
        let got = sessionImages(events)
        #expect(got.map(\.ref.imageID) == ["i1", "i2", "t", "g"])
        #expect(got.map(\.kind) == [.userMessage, .userMessage, .toolCall, .imageGenerated])
        #expect(got[2].toolTitle == "Read a.png" && got[2].seq == 2)
    }

    // --- not in Android's tests ---

    @Test func aNumberNoImageCouldHaveIsLeftAsTyped() {
        // Kotlin's toInt throws on this one; here it is simply not an image of the message.
        #expect(imageText("[Image #99999999999999999999] x", refs: [ref(1)]) == "[Image #99999999999999999999] x")
        // \d is ASCII: a full-width digit is no placeholder.
        #expect(imageText("[Image #１] x", refs: [ref(1)]) == "[Image #１] x")
        // What follows a dropped placeholder is cut by UTF-16 units, emoji and all.
        #expect(imageText(" [Image #1]😀 [Image #2] 好", refs: [ref(1)]) == "😀 [Image #2] 好")
    }
}

/// Android's JpegAfterScanTest (in ImagesTest.kt).
@Suite struct JpegAfterScanTests {
    /// The bytes with `extra` put right before EOI.
    private func beforeEoi(_ j: Data, _ extra: Data) -> Data { j.dropLast(2) + extra + j.suffix(2) }

    @Test func aCommentBeforeTheEndIsDropped() throws {
        let src = beforeEoi(Pictures.gradient(progressive: false), seg(0xFE, Data("GPS 31.2304N 121.4737E".utf8)))
        let out = try stripJpeg(src)
        #expect(!contains(out, "GPS 31.23"))
        #expect(Pictures.pixels(src) == Pictures.pixels(out))
    }

    @Test func exifAfterTheScanIsDropped() throws {
        let src = beforeEoi(Pictures.gradient(progressive: false), seg(0xE1, exifHeader + Data("LOCATION-AFTER-SCAN".utf8)))
        let out = try stripJpeg(src)
        #expect(!contains(out, "LOCATION-AFTER-SCAN"))
        #expect(Pictures.pixels(src) == Pictures.pixels(out))
    }

    @Test func aProgressiveJpegKeepsEveryScanAndLosesItsComments() throws {
        let base = Pictures.gradient(progressive: true)
        // Put a comment between the first two scans.
        let first = try #require(sos(base))
        let second = try #require(sos(base, from: first + 2))
        let src = base.prefix(second) + seg(0xFE, Data("BETWEEN-SCANS".utf8)) + base.dropFirst(second)
        let out = try stripJpeg(src)
        #expect(!contains(out, "BETWEEN-SCANS"))
        #expect(Pictures.pixels(base) == Pictures.pixels(out))
        #expect(scans(out) == scans(base))
        #expect(scans(base) > 1)
    }

    @Test func whatFollowsTheEndIsDropped() throws {
        let src = Pictures.gradient(progressive: false) + Data("TRAILER-GPS".utf8)
        #expect(!contains(try stripJpeg(src), "TRAILER-GPS"))
    }
}

/// Android's DecodeBudgetTest (in ImagesTest.kt).
@Suite struct DecodeBudgetTests {
    @Test func aHugePictureIsDecodedWithinTheBudget() throws {
        // A 200 MP panorama as an original: never decoded whole.
        let s = try decodeSample(w: 20_000, h: 10_000, minLong: 0, pixelBudget: maxDecodePixels)
        #expect(Int64(20_000 / s) * Int64(10_000 / s) <= maxDecodePixels)
        #expect(s == 4)
        // A 12 MP photo fits as it is.
        #expect(try decodeSample(w: 4032, h: 3024, minLong: 0, pixelBudget: maxDecodePixels) == 1)
        // Compressed: sampled down toward the 2048 long side, then the budget.
        #expect(try decodeSample(w: 4032, h: 3024, minLong: 2048, pixelBudget: maxDecodePixels) == 1)
        #expect(try decodeSample(w: 8064, h: 6048, minLong: 2048, pixelBudget: maxDecodePixels) == 2)
        #expect(try decodeSample(w: 4032, h: 3024, minLong: 0, pixelBudget: 300_000) == 8)
        #expect(throws: UserError("图片没有尺寸")) { try decodeSample(w: 0, h: 10, minLong: 0, pixelBudget: 1) }
    }
}
