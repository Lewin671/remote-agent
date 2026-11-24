import Foundation
import RAProto
import Testing

@testable import RACore

/// Shell-side viewer fetch status & saved filename formatting.
/// Image collection, indexing, source captions, dimensions, and display-copy rules live in `clientcore/domain/images`.
@Suite(.timeLimit(.minutes(2))) struct ImageViewingTests {
    @Test func eachStateSaysWhatIsThereToSee() {
        #expect(viewerNote(.loading, sync: .synced) == nil)
        #expect(viewerNote(.ready, sync: .synced) == nil)
        #expect(viewerNote(.notSynced, sync: .previewOnly) == L10n.imageOriginalOnComputer())
        #expect(viewerNote(.notSynced, sync: .nameOnly) == L10n.imageIosNameOnly())
        #expect(viewerNote(.expired, sync: .synced) == L10n.imageOriginalExpired())
        #expect(viewerNote(.offline, sync: .synced) == L10n.imageLoadConnected())
        #expect(viewerNote(.failed(why: "超时"), sync: .synced) == "没能下载：超时 · 点按重试")
        #expect(viewerNote(.corrupt(why: L10n.errorsIosImageContentDoesNotMatchItsFormat()), sync: .synced) == "图片校验失败：图片内容和格式对不上")
    }

    @Test func onlyAFetchThatMayWorkNextTimeIsRetried() {
        #expect(viewerCanRetry(.failed(why: "x")) && viewerCanRetry(.offline))
        for b in [BigImage.loading, .ready, .notSynced, .expired, .corrupt(why: "x")] {
            #expect(!viewerCanRetry(b))
        }
    }

    @Test func howAFetchEndedBecomesAState() {
        #expect(viewerNote(.retiredHistory, sync: .synced) == retiredHistoryError)
        let payload = PBImagePayload.with {
            $0.format = .png
            $0.data = Data([1, 2, 3])
        }
        let loaded = viewerOutcome(.success(.loaded(payload)), online: true)
        #expect(loaded.state == .ready && loaded.payload == payload)
        #expect(viewerOutcome(.success(.expired), online: true).state == .expired)
        #expect(viewerOutcome(.success(.missing), online: true).state == .notSynced)
        #expect(
            viewerOutcome(.success(.corrupt(why: "坏了")), online: true).state == .corrupt(why: "坏了"))
        // Without a connection a failure is no failure of the image's: it loads once there is one.
        #expect(viewerOutcome(.failure(UserError("断了")), online: false).state == .offline)
        #expect(viewerOutcome(.failure(UserError("断了")), online: true).state == .failed(why: "断了"))
        #expect(viewerOutcome(.failure(UserError("断了")), online: true).payload == nil)
    }
}
