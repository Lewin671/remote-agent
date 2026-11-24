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
}
