import Foundation
import RAProto
import Testing

@testable import RACore

@Suite struct AgentsTests {
    @Test func listsKnownClisInStableOrder() {
        #expect(AGENTS.map(\.id) == ["codex", "claude", "agy"])
        #expect(AGENTS.map(\.agent) == [.codex, .claude, .antigravity])
        #expect(DEFAULT_AGENT == .codex)
        #expect(defaultAgent == .codex)
    }

    @Test func looksUpAgentInfoByEnum() {
        let codex = agentInfo(.codex)
        #expect(codex.quotaWindow("primary") == QuotaWindowDescriptor(label: "5 小时", group: nil))
        #expect(codex.quotaWindow("secondary") == QuotaWindowDescriptor(label: "7 天", group: nil))
        #expect(codex.toolLabelFromItemType == false)
        #expect(codex.planClearContext == true)
        #expect(codex.planNote == nil)
        #expect(codex.planKeepLabel == L10n.dockIosKeepPlanning() && codex.planApproveLabel == L10n.approvalApproveExecute())
        #expect(codex.standardContextOption == false)
        #expect(codex.autoMayBeUnavailable == false)
        #expect(codex.reconcilesUnclaimedInput == false)
        #expect(codex.sessionNeedsModelList == false)
        #expect(codex.switchesModelInSession == true)
        #expect(codex.forks == true)

        let claude = agentInfo(.claude)
        #expect(claude.quotaWindow("five_hour") == QuotaWindowDescriptor(label: "5 小时", group: nil))
        #expect(claude.quotaWindow("seven_day") == QuotaWindowDescriptor(label: "7 天", group: nil))
        #expect(claude.toolLabelFromItemType == true)
        #expect(claude.planClearContext == false)
        #expect(claude.planNote == L10n.dockClaudePlanNote())
        #expect(claude.planKeepLabel == L10n.modesPlanEdit() && claude.planApproveLabel == L10n.approvalApprove())
        #expect(claude.standardContextOption == true)
        #expect(claude.autoMayBeUnavailable == true)
        #expect(claude.reconcilesUnclaimedInput == true)
        #expect(claude.sessionNeedsModelList == true)
        #expect(claude.switchesModelInSession == true)
        #expect(claude.forks == true)

        let agy = agentInfo(.antigravity)
        #expect(agy.quotaWindow("gemini-5h") == QuotaWindowDescriptor(label: "Gemini · 5 小时", group: .gemini))
        #expect(agy.quotaWindow("gemini-weekly") == QuotaWindowDescriptor(label: "Gemini · 7 天", group: .gemini))
        #expect(agy.quotaWindow("gemini-argon-wtu-weekly") == QuotaWindowDescriptor(label: "Gemini 4 Argon · 7 天", group: .gemini))
        #expect(agy.quotaWindow("3p-5h") == QuotaWindowDescriptor(label: "第三方模型 · 5 小时", group: .thirdParty))
        #expect(agy.quotaWindow("3p-weekly") == QuotaWindowDescriptor(label: "第三方模型 · 7 天", group: .thirdParty))
        #expect(agy.quotaWindow("custom") == QuotaWindowDescriptor(label: "custom", group: .reported("custom")))
        #expect(agy.toolLabelFromItemType == false)
        #expect(agy.planClearContext == false)
        #expect(agy.planNote == L10n.dockAgyPlanNote())
        #expect(agy.planKeepLabel == L10n.modesPlanEdit() && agy.planApproveLabel == L10n.approvalApprove())
        #expect(agy.standardContextOption == false)
        #expect(agy.autoMayBeUnavailable == true)
        #expect(agy.reconcilesUnclaimedInput == false)
        #expect(agy.sessionNeedsModelList == true)
        #expect(agy.switchesModelInSession == false)
        #expect(agy.forks == false)
    }

    @Test func mapsUnspecifiedAndNilToCodex() {
        #expect(agentInfo(.unspecified).id == "codex")
        #expect(agentInfo(nil).id == "codex")
        #expect(agentName(.unspecified) == "Codex")
        #expect(agentName(nil) == "Codex")
        #expect(agentName() == "Codex")
    }

    @Test func mapsUnrecognizedAgentValuesToUnknownEntryAndNeverCodex() {
        let unknown = agentInfo(.UNRECOGNIZED(99))
        #expect(unknown.id == "")
        #expect(unknown.name == L10n.modelsUnknownCli())
        #expect(unknown.expectedQuotaWindows == nil)
        #expect(unknown.quotaWindow("primary") == QuotaWindowDescriptor(label: "primary", group: nil))
        #expect(unknown.toolLabelFromItemType == false)
        #expect(unknown.planClearContext == false)
        #expect(unknown.planNote == nil)
        #expect(unknown.standardContextOption == false)
        #expect(unknown.autoMayBeUnavailable == false)
        #expect(unknown.trustFolderHint == nil)
        #expect(unknown.usageHint == nil)
        #expect(unknown.reconcilesUnclaimedInput == false)
        #expect(unknown.sessionNeedsModelList == false)
        #expect(unknown.switchesModelInSession == false)
        #expect(unknown.forks == false)
        #expect(unknown.modelLabel("any-slug") == "any-slug")
        #expect(unknown.planName("any-plan") == "any-plan")
        #expect(agentName(.UNRECOGNIZED(99)) == L10n.modelsUnknownCli())
    }

    @Test func unknownEntryRoundTripPreservesUnknownEntryAndNeverCodex() {
        let unrecognised = PBAgent.UNRECOGNIZED(99)
        let unknown = agentInfo(unrecognised)
        #expect(unknown.name == L10n.modelsUnknownCli())
        #expect(unknown.agent == unrecognised)

        let roundTrip = agentInfo(unknown.agent)
        #expect(roundTrip.name == L10n.modelsUnknownCli())
        #expect(roundTrip.agent == unrecognised)
        #expect(roundTrip.id == "")

        // Also test effectiveAgent preserves unrecognized
        #expect(effectiveAgent(unrecognised) == unrecognised)
        #expect(agentInfo(effectiveAgent(unrecognised)).name == L10n.modelsUnknownCli())
    }

    @Test func looksUpAgentsByTargetId() {
        #expect(agentById("codex").agent == .codex)
        #expect(agentById("claude").agent == .claude)
        #expect(agentById("agy").agent == .antigravity)
        #expect(agentById("").id == "")
        #expect(agentById("unknown").id == "")
        #expect(agentById(nil).id == "")
        #expect(agentInfo(agentById("unknown").agent).name == L10n.modelsUnknownCli())
    }

    @Test func checksWhetherAnAgentEnumIsRecognized() {
        #expect(isKnownAgent(.codex) == true)
        #expect(isKnownAgent(.claude) == true)
        #expect(isKnownAgent(.antigravity) == true)
        #expect(isKnownAgent(.unspecified) == false)
        #expect(isKnownAgent(.UNRECOGNIZED(99)) == false)
        #expect(isKnownAgent(nil) == false)
    }

    @Test func roundTripsDefaultsAndWithDefaultsForCodex() {
        var base = PBDaemonSettings()
        base.defaultModel = "gpt-6-sol"
        base.defaultEffort = "medium"
        base.defaultMode = .auto

        let defaults = defaultsFor(base, agent: .codex)
        #expect(defaults.model == "gpt-6-sol")
        #expect(defaults.effort == "medium")
        #expect(defaults.mode == .auto)

        let updated = withDefaults(base, agent: .codex, model: "gpt-6.1", effort: "high")
        #expect(updated.defaultModel == "gpt-6.1")
        #expect(updated.defaultEffort == "high")
        #expect(updated.defaultMode == .auto)

        let updatedDefaults = defaultsFor(updated, agent: .codex)
        #expect(updatedDefaults.model == "gpt-6.1")
        #expect(updatedDefaults.effort == "high")
        #expect(updatedDefaults.mode == .auto)
    }

    @Test func roundTripsDefaultsAndWithDefaultsForClaude() {
        var base = PBDaemonSettings()
        base.claude = .with {
            $0.model = "claude-sonnet-5-5"
            $0.effort = "low"
            $0.mode = .ask
        }

        let defaults = defaultsFor(base, agent: .claude)
        #expect(defaults.model == "claude-sonnet-5-5")
        #expect(defaults.effort == "low")
        #expect(defaults.mode == .ask)

        let updated = withDefaults(base, agent: .claude, model: "claude-opus-9")
        #expect(updated.claude.model == "claude-opus-9")
        #expect(updated.claude.effort == "low")
        #expect(updated.claude.mode == .ask)

        let updatedDefaults = defaultsFor(updated, agent: .claude)
        #expect(updatedDefaults.model == "claude-opus-9")
        #expect(updatedDefaults.effort == "low")
        #expect(updatedDefaults.mode == .ask)
    }

    @Test func roundTripsDefaultsAndWithDefaultsForAntigravity() {
        var base = PBDaemonSettings()
        base.antigravity = .with {
            $0.model = "gemini-3-flash"
            $0.effort = "high"
            $0.mode = .ask
        }

        let defaults = defaultsFor(base, agent: .antigravity)
        #expect(defaults.model == "gemini-3-flash")
        #expect(defaults.effort == "high")
        #expect(defaults.mode == .ask)

        let updated = withDefaults(base, agent: .antigravity, model: "gemini-3.1-pro-high")
        #expect(updated.antigravity.model == "gemini-3.1-pro-high")
        #expect(updated.antigravity.effort == "high")
        #expect(updated.antigravity.mode == .ask)

        let updatedDefaults = defaultsFor(updated, agent: .antigravity)
        #expect(updatedDefaults.model == "gemini-3.1-pro-high")
        #expect(updatedDefaults.effort == "high")
        #expect(updatedDefaults.mode == .ask)
    }

    @Test func preservesSettingsWhenUpdatingUnknownAgentDefaults() {
        var base = PBDaemonSettings()
        base.defaultModel = "m"
        #expect(withDefaults(base, agent: .UNRECOGNIZED(99), model: "x") == base)
        let defaults = defaultsFor(base, agent: .UNRECOGNIZED(99))
        #expect(defaults.model == "")
        #expect(defaults.effort == "")
        #expect(defaults.mode == .unspecified)
    }

    @Test func synthesizesMachineVersionsFromLegacyCodexVersionAndModernAgentsList() {
        let legacy = PBMachineMeta.with {
            $0.codexVersion = "1.2.3"
            $0.codexVerified = true
        }
        let legacyVersions = machineVersions(legacy)
        #expect(legacyVersions.count == 1)
        #expect(legacyVersions[0].agent == .codex)
        #expect(legacyVersions[0].version == "1.2.3")
        #expect(legacyVersions[0].verified == true)

        let modern = PBMachineMeta.with {
            $0.agents = [
                .with { $0.agent = .codex; $0.version = "2.0.0" },
                .with { $0.agent = .claude; $0.version = "3.0.0" }
            ]
        }
        let modernVersions = machineVersions(modern)
        #expect(modernVersions.count == 2)
        #expect(modernVersions[0].agent == .codex)
        #expect(modernVersions[0].version == "2.0.0")
        #expect(modernVersions[1].agent == .claude)
        #expect(modernVersions[1].version == "3.0.0")
    }
}
