import Foundation
import Testing
@testable import RACore

@Suite struct MessageLocaleTests {
    @Test func sharedLanguageContract() throws {
        struct Vector: Decodable { let preference: String; let system: [String]; let expected: String }
        let vectors = try JSONDecoder().decode([Vector].self, from: Data(contentsOf: repoRoot.appendingPathComponent("i18n/locale-vectors.json")))
        for v in vectors { #expect(MessageLocale.resolve(v.preference, system: v.system) == v.expected) }
    }
    @Test func nativeResourcesAndPluralRules() {
        #expect(L10n.commonCancel() == "取消")
        #expect(L10n.commonCopy(locale: "en") == "Copy")
        #expect(L10n.commonCopy(locale: "unsupported") == "复制")
        #expect(L10n.imageAttachFailed(count: 1, locale: "en") == "1 image could not be attached. Tap it for details.")
        for count in [0, 2, 1000] {
            #expect(L10n.imageAttachFailed(count: count) == "\(count) 张图没加上，点图片看原因")
            #expect(L10n.imageAttachFailed(count: count, locale: "en") == "\(count) images could not be attached. Tap an image for details.")
        }
        #expect(imageFailureLine(["pixels", "decode"], locale: "en") == "2 images could not be attached. Tap an image for details.")
    }
    @Test func preferenceIsLocalAndDoesNotEnableEnglishInStageZero() {
        let defaults = UserDefaults(suiteName: "i18n-test-\(UUID())")!
        let preferences = MessagePreferences(defaults: defaults)
        #expect(preferences.preference == "auto")
        preferences.preference = "en"
        #expect(MessagePreferences(defaults: defaults).preference == "en")
        #expect(preferences.current == "zh-CN")
        defaults.removeObject(forKey: "message-language")
    }
}
