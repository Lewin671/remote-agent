import Foundation
import RAProto
import Testing

@testable import RACore

/// The viewer's words and choices, as Android's ui/Viewer.kt has them.
@Suite struct ImageViewingTests {
    private func ref(_ edit: (inout PBImageRef) -> Void = { _ in }) -> PBImageRef {
        var r = PBImageRef.with {
            $0.imageID = "i1"
            $0.sync = .synced
        }
        edit(&r)
        return r
    }

    private func image(_ seq: UInt64) -> SessionImage {
        SessionImage(ref: ref(), seq: seq, kind: .userMessage)
    }

    @Test func eachStateSaysWhatIsThereToSee() {
        #expect(viewerNote(.loading, sync: .synced) == nil)
        #expect(viewerNote(.ready, sync: .synced) == nil)
        #expect(viewerNote(.notSynced, sync: .previewOnly) == "只有预览，原图在电脑上")
        #expect(viewerNote(.notSynced, sync: .nameOnly) == "图片没有同步：只有文件名，原图在电脑上")
        #expect(viewerNote(.expired, sync: .synced) == "大图已过期，只剩预览")
        #expect(viewerNote(.offline, sync: .synced) == "连上后加载")
        #expect(viewerNote(.failed(why: "超时"), sync: .synced) == "没能下载：超时 · 点按重试")
        #expect(viewerNote(.corrupt(why: "图片内容和格式对不上"), sync: .synced) == "图片校验失败：图片内容和格式对不上")
    }

    @Test func onlyAFetchThatMayWorkNextTimeIsRetried() {
        #expect(viewerCanRetry(.failed(why: "x")) && viewerCanRetry(.offline))
        for b in [BigImage.loading, .ready, .notSynced, .expired, .corrupt(why: "x")] {
            #expect(!viewerCanRetry(b))
        }
    }

    @Test func aDisplayCopyIsOnlyAskedForWhenTheEventSaysThereIsOne() {
        #expect(hasDisplayCopy(ref()))
        #expect(!hasDisplayCopy(ref { $0.sync = .previewOnly }))
        #expect(!hasDisplayCopy(ref { $0.sync = .nameOnly }))
        #expect(!hasDisplayCopy(ref { $0.imageID = "" }))
    }

    @Test func howAFetchEndedBecomesAState() {
        let payload = PBImagePayload.with {
            $0.format = .png
            $0.data = Data([1, 2, 3])
        }
        let loaded = viewerOutcome(.success(.loaded(payload)), online: true)
        #expect(loaded.state == .ready && loaded.payload == payload)
        let old = viewerOutcome(.success(.legacy(payload)), online: true)
        #expect(old.state == .unverifiedLegacy && old.payload == payload)
        #expect(viewerNote(old.state, sync: .synced) == "旧图片的内容和来源未验证，仅供阅读")
        #expect(viewerOutcome(.success(.expired), online: true).state == .expired)
        #expect(viewerOutcome(.success(.missing), online: true).state == .notSynced)
        #expect(
            viewerOutcome(.success(.corrupt(why: "坏了")), online: true).state == .corrupt(why: "坏了"))
        // Without a connection a failure is no failure of the image's: it loads once there is one.
        #expect(viewerOutcome(.failure(UserError("断了")), online: false).state == .offline)
        #expect(viewerOutcome(.failure(UserError("断了")), online: true).state == .failed(why: "断了"))
        #expect(viewerOutcome(.failure(UserError("断了")), online: true).payload == nil)
    }

    @Test func itOpensOnTheImageThatWasTapped() {
        let all = [image(3), image(3), image(7), image(9), image(9), image(9)]
        #expect(viewerStart(all, seq: 3, index: 1) == 1)
        #expect(viewerStart(all, seq: 7, index: 0) == 2)
        #expect(viewerStart(all, seq: 9, index: 2) == 5)
        // An index past the end, or an event with no image, still opens on one that exists.
        #expect(viewerStart(all, seq: 9, index: 40) == 5)
        #expect(viewerStart(all, seq: 8, index: 0) == 0)
        #expect(viewerStart(all, seq: 3, index: -4) == 0)
        #expect(viewerStart([], seq: 3, index: 1) == 0)
    }

    @Test func theCaptionSaysWhoPutTheImageThere() {
        #expect(viewerSource(ref { $0.source = .tool }, agent: .codex) == "Codex 查看的图片")
        #expect(viewerSource(ref { $0.source = .generated }, agent: .codex) == "Codex 生成的图片")
        #expect(viewerSource(ref { $0.source = .tool }, agent: .claude) == "Claude Code 查看的图片")
        #expect(viewerSource(ref { $0.source = .user }, agent: .claude) == "用户粘贴")
        #expect(viewerSource(ref(), agent: .claude) == "用户粘贴")
    }

    @Test func theSizeIsTheOneOnTheComputer() {
        #expect(viewerSize(ref()) == nil)
        #expect(
            viewerSize(
                ref {
                    $0.width = 1024
                    $0.height = 768
                }) == "1024 × 768")
        #expect(
            viewerSize(
                ref {
                    $0.width = 1024
                    $0.height = 768
                    $0.originalWidth = 2880
                    $0.originalHeight = 1800
                }) == "2880 × 1800")
    }

    @Test func aPictureLeavesUnderItsOwnNameAndFormat() {
        #expect(
            imageExtension(.png) == "png" && imageExtension(.webp) == "webp"
                && imageExtension(.gif) == "gif")
        #expect(imageExtension(.jpeg) == "jpg" && imageExtension(.unspecified) == "jpg")
        #expect(imageMime(.jpeg) == "image/jpeg" && imageMime(.unspecified) == "image/jpeg")
        #expect(
            imageMime(.png) == "image/png" && imageMime(.webp) == "image/webp"
                && imageMime(.gif) == "image/gif")
        #expect(
            savedImageName(path: "/Users/me/shots/empty-after.png", format: .jpeg, nowMs: 1700)
                == "empty-after-1700.jpg")
        #expect(savedImageName(path: "shots/a.b.png", format: .png, nowMs: 5) == "a.b-5.png")
        // Without a path the name is the word the chat uses for it; a name that is only an extension has none.
        #expect(savedImageName(path: "", format: .png, nowMs: 5) == "图片-5.png")
        #expect(savedImageName(path: "/tmp/.png", format: .png, nowMs: 5) == "remote-agent-5.png")
    }
}
