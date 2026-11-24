import Foundation
import RAProto
import Testing
@testable import RACore

@Suite(.timeLimit(.minutes(2))) struct DeferredStateIdentityTests {
    @Test func errorEqualityUsesIdentityNotRenderedText() {
        let a = UserError(L10nMessage.commonSave()), b = UserError(L10nMessage.commonSave())
        let raw = UserError("保存")
        for locale in ["zh-CN", "en"] {
            MessageLocale.$overrideForTesting.withValue(locale) {
                #expect(a == b)
                #expect(a != raw)
                #expect(CommandError("same", L10nMessage.commonSave()) == CommandError("same", L10nMessage.commonSave()))
                #expect(CommandError("same", L10nMessage.commonSave()) != CommandError("other", L10nMessage.commonSave()))
            }
        }
    }

    @Test func retiredHistoryIsAnExplicitViewerAndCacheState() {
        let view = viewerOutcome(.success(.retiredHistory), online: true).state
        let raw = retiredHistoryError
        for locale in ["zh-CN", "en", "zh-CN"] {
            MessageLocale.$overrideForTesting.withValue(locale) {
                #expect(view == .retiredHistory && !viewerCanRetry(view))
                #expect(viewerNote(view, sync: .synced) == retiredHistoryError)
                // A raw caption matching the old sentence must not choose a different state.
                #expect(viewerNote(.corrupt(why: raw), sync: .synced) == L10n.imageIosVerificationFailed(reason: raw))
            }
        }
    }

    @Test func retainedAgentCatalogNamesResolveAtReadTime() {
        for locale in ["zh-CN", "en", "zh-CN"] {
            MessageLocale.$overrideForTesting.withValue(locale) {
                #expect(agentName(.UNRECOGNIZED(999)) == L10n.modelsUnknownCli())
                #expect(agentName(.codex) == "Codex" && agentName(.claude) == "Claude Code")
            }
        }
    }
}
