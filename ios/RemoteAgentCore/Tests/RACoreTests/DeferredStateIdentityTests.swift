import Foundation
import RAProto
import Testing
@testable import RACore

@Suite struct DeferredStateIdentityTests {
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

    @Test func retainedAgentCatalogDefaultsAndNotesResolveAtReadTime() {
        let codex = agentInfo(.codex), claude = agentInfo(.claude), agy = agentInfo(.antigravity), unknown = agentInfo(.UNRECOGNIZED(999))
        for locale in ["zh-CN", "en", "zh-CN"] {
            MessageLocale.$overrideForTesting.withValue(locale) {
                #expect(codex.planKeepLabel == L10n.dockIosKeepPlanning())
                #expect(codex.planApproveLabel == L10n.approvalApproveExecute())
                #expect(claude.planNote == L10n.dockClaudePlanNote())
                #expect(claude.trustFolderHint == L10n.dockTrustFolder())
                #expect(claude.usageHint == L10n.quotaIosViewUsageInLocalClaudeCode())
                #expect(claude.planKeepLabel == L10n.modesPlanEdit() && agy.planNote == L10n.dockAgyPlanNote())
                #expect(unknown.name == L10n.modelsUnknownCli())
                #expect(codex.name == "Codex" && claude.name == "Claude Code")
                #expect(agy.quotaWindow("3p-5h").group == .thirdParty)
                #expect(agy.quotaWindow("3p-5h").group?.title == L10n.quotaThirdPartyModelsClaudeGptOss())
            }
        }
        #expect(QuotaWindowGroup.thirdParty != .reported("第三方模型（Claude、GPT-OSS）"))
    }

    @Test func defaultNameCollisionIdentityDoesNotReadTranslatedFallbacks() {
        let missing = DeviceView(id: "fixture1111", kind: .client, createdMs: 0, meta: .with { $0.platform = "web" })
        let twin = DeviceView(id: "fixture2222", kind: .client, createdMs: 0, meta: .with { $0.platform = "web" })
        for raw in ["浏览器", "Browser"] {
            let described = DeviceView(id: "fixture3333", kind: .client, createdMs: 0, meta: .with { $0.platform = "web"; $0.description_p = raw })
            for locale in ["zh-CN", "en"] {
                MessageLocale.$overrideForTesting.withValue(locale) {
                    #expect(defaultClientName(missing, id: missing.id, among: [described.id: described]) == L10n.securityBrowser())
                    #expect(defaultClientName(missing, id: missing.id, among: [twin.id: twin]) == L10n.securityBrowser() + " · 1111")
                    #expect(defaultClientName(described, id: described.id, among: [missing.id: missing]) == raw)
                }
            }
        }
    }
}
