import Foundation
import Observation
import RACore
import RAProto
import Testing
import UIKit

@testable import RemoteAgent

/// What the viewer holds of decrypted pictures, and who hears of it (Android keeps its viewer's previews in the composition).
@MainActor
@Suite struct ImageViewerTests {
    private let remote = try! Remote(
        account: Account(server: "http://127.0.0.1:1", root: Data(count: 32), deviceSeed: Data(count: 32), deviceId: "00112233445566778899aabbccddeeff"),
        http: AppModel.http)

    private func center() -> ImageCenter {
        ImageCenter.install(remote: remote, files: VolatileFiles())
        return ImageCenter.of(remote)
    }

    private func png(_ color: UIColor) -> Data {
        UIGraphicsImageRenderer(size: CGSize(width: 4, height: 3)).image { ctx in
            color.setFill()
            ctx.fill(CGRect(x: 0, y: 0, width: 4, height: 3))
        }.pngData()!
    }

    /// An image whose event lost its preview can be saved only once its display copy is here: the bar
    /// with 保存 and 分享 asked before that, and is told.
    @Test func whoAskedForADisplayCopyHearsWhenItIsHere() {
        let loader = ViewerLoader(center: center(), session: "s1")
        let ref = PBImageRef.with { $0.imageID = "i1" }
        final class Heard: @unchecked Sendable { var yes = false }
        let heard = Heard()
        withObservationTracking {
            #expect(ImageLeaving.picture(ref, loader: loader) == nil)
        } onChange: {
            heard.yes = true
        }
        let bytes = png(.red)
        loader.keep(ref, ViewedImage(image: UIImage(data: bytes)!, bytes: bytes, format: .png))
        #expect(heard.yes)
        let picture = ImageLeaving.picture(ref, loader: loader)
        #expect(picture?.bytes == bytes && picture?.format == .png)
    }

    /// docs/session-images.md 「缓存与草稿」: decrypted pictures belong to the account in use.
    @Test func decryptedPicturesGoWithTheAccount() {
        let first = center()
        let loader = ViewerLoader(center: first, session: "s1")
        let ref = PBImageRef.with {
            $0.imageID = "i1"
            $0.path = "/tmp/a.png"
            $0.preview = png(.red)
        }
        let shown = first.preview(ref)
        #expect(shown != nil && first.preview(ref) === shown)
        let bytes = png(.blue)
        loader.keep(ref, ViewedImage(image: UIImage(data: bytes)!, bytes: bytes, format: .png))
        #expect(loader.held(ref) != nil)

        ImageCenter.remove(remote)
        // Another account whose image has the same ID, path and preview length decodes its own.
        var theirs = ref
        theirs.preview = png(.green)
        let next = center()
        let other = next.preview(theirs)
        #expect(other != nil && other !== shown)
        #expect(other?.pngData() != shown?.pngData())
        #expect(ViewerLoader(center: next, session: "s1").held(ref) == nil)
        // What was still on its way for the account that was left is not held for it.
        loader.keep(ref, ViewedImage(image: UIImage(data: bytes)!, bytes: bytes, format: .png))
        #expect(loader.held(ref) == nil)
        #expect(first.preview(ref) !== first.preview(ref))
    }
}
