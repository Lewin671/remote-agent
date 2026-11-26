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
        let host = try! CoreHost.singleCore(
            store: MemorySecurityStoreV4(),
            http: HTTP(),
            allowPlainHttp: true,
            screens: CoreScreens(),
            defaults: UserDefaults(suiteName: "ra-viewer-center-\(UUID().uuidString)")!,
            demoMode: true
        )
        return ImageCenter.install(remote: remote, coreHost: host, pending: PendingStore.ephemeral().scoped())
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
        let viewerImg = PBSessionViewerImageView.with {
            $0.imageID = "i1"
            $0.displayName = "shot.png"
            $0.fileExtension = "png"
            $0.downloadName = "shot-1.png"
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
        loader.keep("i1", ViewedImage(image: UIImage(data: bytes)!, bytes: bytes, format: .png))
        #expect(heard.yes)
        let picture = ImageLeaving.picture(viewerImg, loader: loader)
        #expect(picture?.bytes == bytes && picture?.format == .png)
        #expect(picture?.name == "shot-1.png")
    }

    /// A preview-only image can still leave via its embedded timeline preview image when Core strips preview bytes from `viewer_images`.
    @Test func strippedViewerImageFallsBackToTimelinePreviewImage() {
        let loader = ViewerLoader(center: center(), session: "s1")
        let previewBytes = png(.blue)
        let viewerImg = PBSessionViewerImageView.with {
            $0.imageID = "i2"
            $0.syncState = .previewOnly
            $0.displayName = "clip.png"
            $0.fileExtension = "png"
            $0.hasDisplayCopy_p = false
        }
        let previewImage = PBSessionImageView.with {
            $0.imageID = "i2"
            $0.syncState = .previewOnly
            $0.preview = previewBytes
            $0.previewFormat = .png
        }
        #expect(ImageLeaving.picture(viewerImg, loader: loader) == nil)
        let picture = ImageLeaving.picture(viewerImg, previewImage: previewImage, loader: loader)
        #expect(picture?.bytes == previewBytes && picture?.format == .png)
    }

    /// docs/session-images.md 「缓存与草稿」: decrypted pictures belong to the account in use.
    @Test func decryptedPicturesGoWithTheAccount() {
        let first = center()
        let loader = ViewerLoader(center: first, session: "s1")
        let redPreview = png(.red)
        let shown = first.preview(imageID: "i1", displayName: "a.png", data: redPreview)
        #expect(shown != nil && first.preview(imageID: "i1", displayName: "a.png", data: redPreview) === shown)
        let bytes = png(.blue)
        loader.keep("i1", ViewedImage(image: UIImage(data: bytes)!, bytes: bytes, format: .png))
        #expect(loader.held("i1") != nil)

        ImageCenter.remove(remote)
        // Another account whose image has the same ID, name and preview length decodes its own.
        let greenPreview = png(.green)
        let next = center()
        let other = next.preview(imageID: "i1", displayName: "a.png", data: greenPreview)
        #expect(other != nil && other !== shown)
        #expect(other?.pngData() != shown?.pngData())
        #expect(ViewerLoader(center: next, session: "s1").held("i1") == nil)
        // What was still on its way for the account that was left is not held for it.
        loader.keep("i1", ViewedImage(image: UIImage(data: bytes)!, bytes: bytes, format: .png))
        #expect(loader.held("i1") == nil)
        #expect(first.preview(imageID: "i1", displayName: "a.png", data: redPreview) !== first.preview(imageID: "i1", displayName: "a.png", data: redPreview))
    }

    #if DEBUG
    @Test func coreHostOpenNavigateAndCloseSessionImageViewer() async throws {
        let settings = SettingsStore(defaults: UserDefaults(suiteName: "ra-viewer-\(UUID())")!)
        let model = AppModel(settings: settings)
        let demoRemote = Demo.remote()
        var world = Demo.world(.everyday)
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
        world.sessions["s-view"] = .with {
            $0.sessionID = "s-view"
            $0.machineID = "mac"
            $0.meta = .with { $0.id = "s-view"; $0.title = "Viewer" }
            $0.metaRev = 1
        }
        model.setForeground(true)
        model.showDemo(account: Demo.account, remote: demoRemote, world: world, events: events)
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

    @Test func staleRenderSequenceDoesNotOverwriteNewerSessionSnapshot() {
        let screens = CoreScreens()
        #expect(screens.acceptRender(.session("s-view"), seq: 2))
        #expect(!screens.acceptRender(.session("s-view"), seq: 1))
        #expect(screens.acceptRender(.session("s-view"), seq: 3))
        // Other slots track their own high-water marks independently.
        #expect(screens.acceptRender(.home, seq: 1))
    }
    #endif
}
