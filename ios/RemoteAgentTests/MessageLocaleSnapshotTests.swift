import Foundation
import RACore
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
}
