import Foundation
import Observation
import RACore
import RAProto
import Testing
import UIKit

@testable import RemoteAgent

/// What the viewer holds of decrypted pictures, who hears of it, and Core viewer open/navigate/close actions.
@MainActor
@Suite struct ImageViewerTests {
    private let remote = try! Remote(
        account: Account(server: "http://127.0.0.1:1", deviceId: "00112233445566778899aabbccddeeff"),
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
        let viewerImg = PBSessionViewerImageView.with {
            $0.ref = ref
            $0.displayName = "shot.png"
            $0.fileExtension = "png"
            $0.hasDisplayCopy_p = true
        }
        final class Heard: @unchecked Sendable { var yes = false }
        let heard = Heard()
        withObservationTracking {
            #expect(ImageLeaving.picture(viewerImg, loader: loader) == nil)
        } onChange: {
            heard.yes = true
        }
        let bytes = png(.red)
        loader.keep(ref, ViewedImage(image: UIImage(data: bytes)!, bytes: bytes, format: .png))
        #expect(heard.yes)
        let picture = ImageLeaving.picture(viewerImg, loader: loader)
        #expect(picture?.bytes == bytes && picture?.format == .png)
        #expect(picture?.name.hasPrefix("shot-") == true && picture?.name.hasSuffix(".png") == true)
    }

    /// A preview-only image can still leave via its embedded timeline preview ref when Core strips preview bytes from `viewer_images`.
    @Test func strippedViewerImageFallsBackToTimelinePreviewRef() {
        let loader = ViewerLoader(center: center(), session: "s1")
        let previewBytes = png(.blue)
        let viewerImg = PBSessionViewerImageView.with {
            $0.ref = .with { $0.imageID = "i2"; $0.sync = .previewOnly }
            $0.displayName = "clip.png"
            $0.fileExtension = "png"
            $0.hasDisplayCopy_p = false
        }
        let previewRef = PBImageRef.with {
            $0.imageID = "i2"
            $0.sync = .previewOnly
            $0.preview = previewBytes
            $0.previewFormat = .png
        }
        #expect(ImageLeaving.picture(viewerImg, loader: loader) == nil)
        let picture = ImageLeaving.picture(viewerImg, previewRef: previewRef, loader: loader)
        #expect(picture?.bytes == previewBytes && picture?.format == .png)
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

    #if DEBUG
    @Test func coreHostOpenNavigateAndCloseSessionImageViewer() async throws {
        let settings = SettingsStore(defaults: UserDefaults(suiteName: "ra-viewer-\(UUID())")!)
        let model = AppModel(settings: settings)
        let demoRemote = Demo.remote()
        let initial = Demo.state(.everyday)
        let events: [String: [PBEvent]] = [
            "s-view": [
                .with {
                    $0.seq = 1
                    $0.userMessage = .with {
                        $0.text = "[Image #1] [Image #2] compare"
                        $0.images = [
                            .with { $0.imageID = "img-1"; $0.cliIndex = 1; $0.sync = .synced; $0.width = 400; $0.height = 300 },
                            .with { $0.imageID = "img-2"; $0.cliIndex = 2; $0.sync = .synced; $0.width = 800; $0.height = 600 },
                        ]
                    }
                }
            ]
        ]
        var stateWithSession = initial
        stateWithSession.sessions["s-view"] = SessionView(id: "s-view", machineId: "mac", info: .with { $0.title = "Viewer" })
        let snapshot = stateWithSession
        model.setForeground(true)
        model.showDemo(account: Demo.account, remote: demoRemote, initial: snapshot, events: events) {
            await demoRemote.showForTest(state: snapshot)
        }
        defer { model.closeSyncForTest() }
        let host = try #require(model.context?.coreHost)
        let token = await host.acquireSessionWatch("s-view")
        defer { Task { await host.releaseSessionWatch(token) } }
        await eventually("viewer images seeded") {
            (model.context?.core.sessions["s-view"]?.timeline.viewerImages.count ?? 0) == 2
        }

        await host.openSessionImageViewer(sessionID: "s-view", seq: 1, imageIndex: 0)
        await eventually("viewer opened at 0") {
            let v = model.context?.core.sessions["s-view"]?.imageViewer
            return v?.open == true && v?.currentIndex == 0 && v?.totalCount == 2 && v?.hasNext_p == true
        }

        await host.navigateSessionImageViewer(sessionID: "s-view", delta: 1)
        await eventually("viewer navigated to 1") {
            let v = model.context?.core.sessions["s-view"]?.imageViewer
            return v?.open == true && v?.currentIndex == 1 && v?.hasPrev_p == true && v?.hasNext_p == false
        }

        await host.closeSessionImageViewer(sessionID: "s-view")
        await eventually("viewer closed") {
            model.context?.core.sessions["s-view"]?.imageViewer.open == false
        }
    }
    #endif
}
