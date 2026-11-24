import Foundation
import RACore
import RAProto
import SwiftUI
import Testing
import UIKit
@testable import RemoteAgent

@Suite struct MessageLocaleSnapshotTests {
    /// Explicit-locale resource/component snapshots, not a paired-session test.
    @MainActor @Test func pilotInBothLanguages() throws {
        for locale in ["zh-CN", "en"] {
            let content = VStack(alignment: .leading, spacing: 18) {
                HStack {
                    Btn(L10n.commonCancel(locale: locale)) {}
                    Btn(L10n.commonSave(locale: locale), kind: .primary) {}
                }
                Text(L10n.commonCopy(locale: locale))
                Text(L10n.commonCopied(locale: locale))
                Text(L10n.commonRetry(locale: locale))
                Text(L10n.commonRemove(locale: locale))
                Text(L10n.commonDone(locale: locale))
                Text(L10n.imageAttachFailed(count: 2, locale: locale))
            }
            .font(.system(size: 22)).foregroundStyle(.black).padding(24)
            .frame(width: 375, alignment: .leading).background(.white)
            .environment(\.locale, Locale(identifier: locale))
            .environment(\.colorScheme, .light)
            let renderer = ImageRenderer(content: content)
            renderer.scale = 2
            let data = try #require(renderer.uiImage?.pngData())
            let directory = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            try data.write(to: directory.appendingPathComponent("i18n-\(locale).png"))
            #expect(L10n.commonCancel(locale: locale) == (locale == "en" ? "Cancel" : "取消"))
        }
    }
    /// File chips use the real components; the errors remain raw fixture details for batch C.
    @MainActor @Test func sessionFileChipsInBothLanguages() throws {
        for locale in ["zh-CN", "en"] {
            try MessageLocale.$overrideForTesting.withValue(locale) {
                let preparing = PBAttachmentDraftItemView.with {
                    $0.itemID = "1"
                    $0.kind = .file
                    $0.displayName = "report.pdf"
                    $0.sizeText = .with { $0.literal = "2.0 KB" }
                    $0.state = .preparing
                    $0.statusText = .with { $0.literal = L10n.imagePreparing(locale: locale) }
                    $0.canRemove = true
                }
                let uploading = PBAttachmentDraftItemView.with {
                    $0.itemID = "2"
                    $0.kind = .file
                    $0.displayName = "source.zip"
                    $0.sizeText = .with { $0.literal = "4.0 KB" }
                    $0.state = .uploading
                    $0.progress = 0.42
                    $0.statusText = .with { $0.literal = "42%" }
                    $0.canPause = true
                    $0.canRemove = true
                }
                let failed = PBAttachmentDraftItemView.with {
                    $0.itemID = "3"
                    $0.kind = .file
                    $0.displayName = "notes.txt"
                    $0.sizeText = .with { $0.literal = "256 B" }
                    $0.state = .failed
                    $0.fileErrorText = .with { $0.literal = "fixture detail" }
                    $0.canRetry = true
                    $0.canRemove = true
                }
                let content = VStack(alignment: .leading, spacing: 16) {
                    FileChip(item: preparing, remove: {})
                    FileChip(item: uploading, pause: {}, remove: {})
                    FileChip(item: failed, retry: {}, remove: {})
                }
                .padding(16).frame(width: 375).background(Color.white)
                .environment(\.locale, Locale(identifier: locale))
                .environment(\.colorScheme, .light)
                // ImageRenderer uses a placeholder for the UIKit-backed ProgressView.
                // A real hosting window captures the native control as displayed.
                let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
                let previous = scene.windows.first(where: \.isKeyWindow)
                let host = UIHostingController(rootView: content)
                host.safeAreaRegions = []
                let size = host.sizeThatFits(in: CGSize(width: 375, height: 1000))
                let window = UIWindow(windowScene: scene)
                window.frame = CGRect(origin: .zero, size: size)
                window.rootViewController = host
                window.makeKeyAndVisible()
                defer { window.isHidden = true; previous?.makeKeyAndVisible() }
                host.view.frame = window.bounds
                host.view.setNeedsLayout()
                host.view.layoutIfNeeded()
                let renderer = UIGraphicsImageRenderer(size: size)
                let picture = renderer.image { _ in
                    window.drawHierarchy(in: window.bounds, afterScreenUpdates: true)
                }
                let data = try #require(picture.pngData())
                let directory = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
                try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
                try data.write(to: directory.appendingPathComponent("session-files-\(locale).png"))
            }
        }
    }

}
