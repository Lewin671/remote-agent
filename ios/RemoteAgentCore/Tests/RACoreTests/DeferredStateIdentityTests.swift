import Foundation
import RAProto
import Testing
@testable import RACore

@Suite struct DeferredStateIdentityTests {
    @Test func anExpiredUnknownAttemptKeepsItsKeyUntilBothErrorDisplays() throws {
        let attempt = Attempt()
        _ = try attempt.next { PreparedCommand(machineId: "fixture", sessionId: "", requestId: "same-request", deadlineMs: 1, command: PBCommand()) }
        attempt.settle(CommandError(CommandError.unknownOutcome, "raw cause"))
        let settled = try #require(attempt.settle(CommandError(CommandError.expired, "raw expiry")) as? CommandError)
        var thrown: CommandError?
        do {
            _ = try attempt.next { Issue.record("expired command was recreated"); throw CancellationError() }
            Issue.record("expired command was retried")
        } catch { thrown = error as? CommandError }
        let next = try #require(thrown)
        for locale in ["zh-CN", "en", "zh-CN"] {
            MessageLocale.$overrideForTesting.withValue(locale) {
                let expected = L10n.errorsIosThePreviousOperationIsUnconfirmedAndHasExpiredIt()
                #expect(settled.message == expected && next.message == expected)
                #expect(settled.localized == Attempt.expiredUnknown && next.localized == Attempt.expiredUnknown)
                #expect(settled.unknown && next.unknown && attempt.frozen && attempt.expired)
                #expect(attempt.pending?.requestId == "same-request")
            }
        }
    }

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
        let buffer = Remote.Buffer(); buffer.historyFailure = .retired
        let raw = retiredHistoryError
        for locale in ["zh-CN", "en", "zh-CN"] {
            MessageLocale.$overrideForTesting.withValue(locale) {
                #expect(view == .retiredHistory && !viewerCanRetry(view))
                #expect(viewerNote(view, sync: .synced) == retiredHistoryError)
                #expect(buffer.historyFailure == .retired && buffer.error == retiredHistoryError)
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
