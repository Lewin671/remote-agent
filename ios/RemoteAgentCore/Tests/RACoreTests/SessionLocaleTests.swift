import Foundation
import RAProto
import Testing
@testable import RACore

@Suite struct SessionLocaleTests {
    @Test func quantitiesAndDurationsKeepChineseAndUseEnglishPlurals() {
        for count in [0, 1, 2, 1000] {
            #expect(L10n.toolsCompletedCommands(count: count, locale: "zh-CN") == "运行了 \(count) 条命令")
            #expect(L10n.imagePosition(count: count, index: 1, locale: "zh-CN") == "第 1 张，共 \(count) 张")
            #expect(L10n.toolsCompletedCommands(count: count, locale: "en") == (count == 1 ? "Ran 1 command" : "Ran \(count) commands"))
        }
    }

    @Test func agentLabelsAndTerminalKeysResolveOnEveryRead() {
        for locale in ["zh-CN", "en", "zh-CN"] {
            MessageLocale.$overrideForTesting.withValue(locale) {
                #expect(agentInfo(.claude).planApproveLabel == L10n.approvalApprove())
                #expect(agentInfo(.claude).planNote == L10n.dockClaudePlanNote())
                let enter = terminalKeyRows.joined().first { $0.kind == .text("\r") }!
                #expect(enter.label == (locale == "en" ? "Enter" : "回车"))
                #expect(terminalKeySequence(enter, appCursor: false) == "\r")
                let left = terminalKeyRows.joined().first { $0.kind == .arrow("D") }!
                #expect(terminalKeySequence(left, appCursor: true) == "\u{1b}OD")
            }
        }
    }

    @Test func attachmentNoticesRetainArgumentsAndImageErrorsResolveInTheRequestedLanguage() {
        let notice = AttachmentNotice.restoreImages(have: 3, count: 4, limit: 6)
        #expect(MessageLocale.$overrideForTesting.withValue("zh-CN") { notice.text } == "输入框里已有 3 张图，放回这 4 张会超过 6 张。先移除一些再放回")
        #expect(MessageLocale.$overrideForTesting.withValue("en") { notice.text }.contains("4 images"))
        #expect(rawImageProblem("decode failed").kind == .unreadable)
        #expect(imageFailureLine(["decode failed", "invalid png"].map(rawImageProblem), locale: "en") == L10n.imageCannotReadThisImageTryJpegOrPngInstead(locale: "en"))
        #expect(imageFailureLine(["pixels", "decode failed"].map(rawImageProblem), locale: "en") == L10n.imageAttachFailed(count: 2, locale: "en"))
    }
}
