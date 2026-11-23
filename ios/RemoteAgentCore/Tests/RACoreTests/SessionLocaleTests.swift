import Foundation
import RAProto
import Testing
@testable import RACore

@Suite(.timeLimit(.minutes(2))) struct SessionLocaleTests {
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
                #expect(agentName(.UNRECOGNIZED(99)) == L10n.modelsUnknownCli())
                let enter = terminalKeyRows.joined().first { $0.kind == .text("\r") }!
                #expect(enter.label == (locale == "en" ? "Enter" : "回车"))
                #expect(terminalKeySequence(enter, appCursor: false) == "\r")
                let left = terminalKeyRows.joined().first { $0.kind == .arrow("D") }!
                #expect(terminalKeySequence(left, appCursor: true) == "\u{1b}OD")
            }
        }
    }
}
