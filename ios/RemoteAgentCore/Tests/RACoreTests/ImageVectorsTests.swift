import Corebind
import Foundation
import RAProto
import Testing

@testable import RACore

@Suite(.timeLimit(.minutes(2))) struct ImageVectorsTests {
    private func coreFormatName(_ code: Int32) -> String {
        switch code {
        case 1: return "jpeg"
        case 2: return "png"
        case 3: return "webp"
        case 4: return "gif"
        case 5: return "heic"
        case 6: return "avif"
        default: return "unknown"
        }
    }

    @Test func sharedImageParseAndStripVectors() throws {
        let json = try loadJSON("testdata/images/vectors.json")
        let cases = try #require(json["cases"] as? [[String: Any]])
        #expect(cases.count >= 30)

        for tc in cases {
            let name = try #require(tc["name"] as? String)
            let inputB64 = try #require(tc["inputBase64"] as? String)
            let input = try #require(Data(base64Encoded: inputB64))
            let wantFmt = try #require(tc["format"] as? String)
            let wantOrient = try #require(tc["orientation"] as? Int)
            let stripError = tc["stripError"] as? String
            let stripErrorCode = tc["stripErrorCode"] as? Int ?? 0
            let wantStrippedB64 = tc["expectedStrippedBase64"] as? String
            let forbidden = tc["forbiddenSubstrings"] as? [String] ?? []

            let head = Data(input.prefix(256))
            #expect(coreFormatName(CorebindSniffImageFormat(head)) == wantFmt, "\(name): sniff")

            let wantProtoFmt: PBImageFormat? = switch wantFmt {
            case "jpeg": .jpeg
            case "png": .png
            case "webp": .webp
            case "gif": .gif
            default: nil
            }
            #expect(detectFormat(input) == wantProtoFmt, "\(name): detectFormat")
            #expect(orientationOf(input) == wantOrient, "\(name): orientationOf")

            if stripError != nil {
                switch wantFmt {
                case "jpeg": #expect(throws: UserError.self, "\(name)") { _ = try stripJpeg(input) }
                case "png": #expect(throws: UserError.self, "\(name)") { _ = try stripPng(input) }
                case "webp": #expect(throws: UserError.self, "\(name)") { _ = try stripWebp(input) }
                case "gif":
                    switch stripErrorCode {
                    case 5:
                        #expect(throws: UserError(L10nMessage.imageIosGifContainsAnUnrecognizedBlock()), "\(name)") { _ = try stripGif(input) }
                    case 4:
                        #expect(throws: UserError(L10nMessage.imageIosGifIsTruncated()), "\(name)") { _ = try stripGif(input) }
                    case 3:
                        #expect(throws: UserError(L10nMessage.imageIosNotAReadableGif()), "\(name)") { _ = try stripGif(input) }
                    default:
                        #expect(throws: UserError.self, "\(name)") { _ = try stripGif(input) }
                    }
                default: #expect(try stripOriginal(input) == nil, "\(name): stripOriginal unsupported")
                }
            } else if let wantStrippedB64 {
                let wantStripped = try #require(Data(base64Encoded: wantStrippedB64))
                let stripped: Data = switch wantFmt {
                case "jpeg": try stripJpeg(input)
                case "png": try stripPng(input)
                case "webp": try stripWebp(input)
                case "gif": try stripGif(input)
                default: fatalError("\(name): unexpected format \(wantFmt)")
                }
                #expect(stripped == wantStripped, "\(name): stripped bytes")
                #expect(orientationOf(stripped) == 1, "\(name): stripped orientation")
                for bad in forbidden {
                    #expect(stripped.range(of: Data(bad.utf8)) == nil, "\(name): leaked \(bad)")
                }
                if wantOrient == 1 {
                    #expect(try stripOriginal(input) == wantStripped, "\(name): stripOriginal")
                } else {
                    #expect(try stripOriginal(input) == nil, "\(name): stripOriginal turned")
                }
            }
        }
    }

    private func makeTiffOrient(_ orient: UInt8, totalSize: Int) -> Data {
        var b = [UInt8](repeating: 0, count: max(32, totalSize))
        let hdr: [UInt8] = [0x4D, 0x4D, 0, 42, 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, orient, 0, 0]
        b.replaceSubrange(0..<hdr.count, with: hdr)
        return Data(b)
    }

    private func makePngChunk(_ type: String, _ payload: Data) -> Data {
        var out = Data()
        let n = UInt32(payload.count)
        out.append(contentsOf: [UInt8(n >> 24), UInt8((n >> 16) & 0xff), UInt8((n >> 8) & 0xff), UInt8(n & 0xff)])
        out.append(Data(type.utf8))
        out.append(payload)
        out.append(contentsOf: [0, 0, 0, 0])
        return out
    }

    private func makeRiffChunk(_ fourcc: String, _ payload: Data) -> Data {
        var out = Data(fourcc.utf8)
        let n = UInt32(payload.count)
        out.append(contentsOf: [UInt8(n & 0xff), UInt8((n >> 8) & 0xff), UInt8((n >> 16) & 0xff), UInt8(n >> 24)])
        out.append(payload)
        if payload.count & 1 != 0 { out.append(0) }
        return out
    }

    @Test(.timeLimit(.minutes(1))) func largeFilesCoverNeedReadAndWindowContinuationBranches() throws {
        // 1. JPEG: 70 KB 0xFF fill + full-length APP1 (65535) with fill + 128 KB scan + post-scan COM
        let body = Data([
            0xff, 0xc0, 0, 11, 8, 0, 12, 0, 16, 1, 1, 0x11, 0,
            0xff, 0xda, 0, 8, 1, 1, 0, 0, 63, 0,
        ])
        let scan = Data(repeating: 0x22, count: 128 * 1024)
        var postCom = Data([0xff, 0xfe, UInt8((60_002 >> 8) & 0xff), UInt8(60_002 & 0xff)])
        postCom.append(Data(count: 60_000))
        let eoi = Data([0xff, 0xd9])
        let expectedJpeg = Data([0xff, 0xd8]) + body + scan + eoi

        let fillJpeg = Data([0xff, 0xd8]) + Data(repeating: 0xff, count: 70_000) + body + scan + postCom + eoi
        #expect(orientationOf(fillJpeg) == 1)
        #expect(try stripJpeg(fillJpeg) == expectedJpeg)

        let app1Jpeg = Data([0xff, 0xd8, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xe1, 0xff, 0xff])
            + Data("Exif\0\0".utf8) + makeTiffOrient(3, totalSize: 65535 - 2 - 6) + body + scan + postCom + eoi
        #expect(orientationOf(app1Jpeg) == 3)
        #expect(try stripJpeg(app1Jpeg) == expectedJpeg)

        // 2. PNG: >64 KB eXIf + 24 x 8 KB IDAT chunks + post-IDAT tEXt
        let pngSig = Data([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])
        let ihdr = makePngChunk("IHDR", Data([0, 0, 0, 10, 0, 0, 0, 20, 8, 6, 0, 0, 0]))
        let idat = makePngChunk("IDAT", Data(count: 8 * 1024))
        let iend = makePngChunk("IEND", Data())
        var pngDirty = pngSig + ihdr + makePngChunk("eXIf", makeTiffOrient(6, totalSize: 70_000))
        var pngClean = pngSig + ihdr
        for _ in 0..<24 {
            pngDirty.append(idat)
            pngClean.append(idat)
        }
        pngDirty.append(makePngChunk("tEXt", Data("Comment\0SECRET".utf8)))
        pngDirty.append(iend)
        pngClean.append(iend)
        #expect(orientationOf(pngDirty) == 6)
        #expect(try stripPng(pngDirty) == pngClean)

        // 3. WebP: 128 KB VP8L + trailing 70 KB EXIF + XMP
        var vp8x = Data(count: 10)
        vp8x[0] = 0x1c; vp8x[4] = 9; vp8x[7] = 19
        var vp8l = Data(count: 128 * 1024)
        vp8l[0] = 0x2f
        let chunks = makeRiffChunk("VP8X", vp8x) + makeRiffChunk("VP8L", vp8l)
            + makeRiffChunk("EXIF", makeTiffOrient(8, totalSize: 70_000))
            + makeRiffChunk("XMP ", Data("<x:xmpmeta>SECRET</x:xmpmeta>".utf8))
        let riffLen = UInt32(4 + chunks.count)
        let webp = Data("RIFF".utf8)
            + Data([UInt8(riffLen & 0xff), UInt8((riffLen >> 8) & 0xff), UInt8((riffLen >> 16) & 0xff), UInt8(riffLen >> 24)])
            + Data("WEBP".utf8) + chunks
        #expect(orientationOf(webp) == 8)
        let webpStripped = try stripWebp(webp)
        #expect(orientationOf(webpStripped) == 1)
        #expect(webpStripped[20] == 0x10)
        #expect(webpStripped.range(of: Data("SECRET".utf8)) == nil)
    }
}
