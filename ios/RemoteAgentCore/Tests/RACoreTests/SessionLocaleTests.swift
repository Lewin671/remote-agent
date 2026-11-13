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
        MessageLocale.$overrideForTesting.withValue("zh-CN") {
            #expect(duration(12_000) == "12 秒")
            #expect(duration(252_000) == "4 分 12 秒")
            #expect(duration(3_780_000) == "1 小时 3 分")
        }
        MessageLocale.$overrideForTesting.withValue("en") {
            #expect(duration(12_000) == "12 sec")
            #expect(duration(252_000) == "4 min 12 sec")
            #expect(duration(3_780_000) == "1 hr 3 min")
        }
    }

    @Test func cachedRowsRebuildWithoutChangingTheEventsOrModeIdentity() {
        let events = [Mk.ev(1, .settingsChanged(.with { $0.mode = .fullAccess }))]
        let derived = derive(events)
        var projection = ChatProjection()
        let chinese = MessageLocale.$overrideForTesting.withValue("zh-CN") {
            projection.update(events, d: derived, machine: "Computer")
        }
        let english = MessageLocale.$overrideForTesting.withValue("en") {
            projection.update(events, d: derived, machine: "Computer")
        }
        #expect(chinese.map(\.key) == english.map(\.key))
        guard case .note(let zh) = chinese.first, case .note(let en) = english.first else {
            Issue.record("Expected mode-change rows"); return
        }
        #expect(zh.text == "模式切换到 完全放行")
        #expect(en.text == "Approval mode switched to Full access")
        #expect(projection.builds == 2)
    }

    @Test func terminalModeChangesAreIndependentOfRenderingLanguage() {
        let events = [
            Mk.ev(1, .turnContext(.with { $0.permissionMode = "default" })),
            Mk.ev(2, .turnContext(.with { $0.permissionMode = "plan" })),
            Mk.ev(3, .planProposed(.with { $0.text = "CLI plan" })),
            Mk.ev(4, .turnContext(.with { $0.permissionMode = "default" })),
        ]
        let zh = MessageLocale.$overrideForTesting.withValue("zh-CN") { derive(events, agent: .claude) }
        let en = MessageLocale.$overrideForTesting.withValue("en") { derive(events, agent: .claude) }
        #expect(zh == en)
        #expect(en.terminalChanges == [2: [.mode(.known(.plan))]])
        MessageLocale.$overrideForTesting.withValue("en") {
            #expect(en.terminalChanges[2]?.first?.text == "switched approval mode to Plan")
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
    @Test func sessionLookupResolvesOnlyAtTheDisplayBoundary() {
        let failure = SessionLookup.failed(reason: "raw detail")
        #expect(lookupNote(.reading) == nil)
        #expect(MessageLocale.$overrideForTesting.withValue("zh-CN") { lookupNote(.missing) } == "找不到这个会话")
        #expect(MessageLocale.$overrideForTesting.withValue("en") { lookupNote(.missing) } == "Session not found")
        #expect(MessageLocale.$overrideForTesting.withValue("zh-CN") { lookupNote(failure) } == "没能读取这个会话：raw detail")
        #expect(MessageLocale.$overrideForTesting.withValue("en") { lookupNote(failure) } == "Could not read this session: raw detail")
        #expect(failure == .failed(reason: "raw detail"))
    }

}
