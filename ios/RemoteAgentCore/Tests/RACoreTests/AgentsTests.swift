import Foundation
import RAProto
import Testing

@testable import RACore

@Suite struct AgentsTests {
    @Test func listsKnownClisInStableOrder() {
        #expect(AGENTS.map(\.id) == ["codex", "claude"])
        #expect(AGENTS.map(\.agent) == [.codex, .claude])
        #expect(DEFAULT_AGENT == .codex)
        #expect(defaultAgent == .codex)
    }

    @Test func looksUpAgentInfoByEnum() {
        let codex = agentInfo(.codex)
        #expect(codex.id == "codex")
        #expect(codex.name == "Codex")
        #expect(codex.quotaWindows == ["primary", "secondary"])
        #expect(codex.planClearContext == true)
        #expect(codex.planApprovalReturnsToAsk == false)
        #expect(codex.standardContextOption == false)
        #expect(codex.reconcilesUnclaimedInput == false)
        #expect(codex.sessionNeedsModelList == false)
        #expect(codex.modelLabel("gpt-6-sol") == "GPT-6-Sol")
        #expect(codex.planName("claude_max") == "Max")
        #expect(codex.planName("prolite") == "Pro Lite")
        #expect(codex.sheetTitle("设置") == "设置")

        let claude = agentInfo(.claude)
        #expect(claude.id == "claude")
        #expect(claude.name == "Claude Code")
        #expect(claude.quotaWindows == ["five_hour", "seven_day"])
        #expect(claude.planClearContext == false)
        #expect(claude.planApprovalReturnsToAsk == true)
        #expect(claude.standardContextOption == true)
        #expect(claude.reconcilesUnclaimedInput == true)
        #expect(claude.sessionNeedsModelList == true)
        #expect(claude.modelLabel("claude-sonnet-5-5") == "claude-sonnet-5-5")
        #expect(claude.planName("claude_max") == "Max")
        #expect(claude.planName("prolite") == "Pro Lite")
        #expect(claude.sheetTitle("设置") == "Claude Code · 设置")
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
        #expect(unknown.name == "未知 CLI")
        #expect(unknown.quotaWindows.isEmpty)
        #expect(unknown.planClearContext == false)
        #expect(unknown.planApprovalReturnsToAsk == false)
        #expect(unknown.standardContextOption == false)
        #expect(unknown.trustFolderHint == nil)
        #expect(unknown.usageHint == nil)
        #expect(unknown.reconcilesUnclaimedInput == false)
        #expect(unknown.sessionNeedsModelList == false)
        #expect(unknown.modelLabel("any-slug") == "any-slug")
        #expect(unknown.planName("any-plan") == "any-plan")
        #expect(unknown.sheetTitle("设置") == "设置")
        #expect(agentName(.UNRECOGNIZED(99)) == "未知 CLI")
    }

    @Test func unknownEntryRoundTripPreservesUnknownEntryAndNeverCodex() {
        let unrecognised = PBAgent.UNRECOGNIZED(99)
        let unknown = agentInfo(unrecognised)
        #expect(unknown.name == "未知 CLI")
        #expect(unknown.agent == unrecognised)

        let roundTrip = agentInfo(unknown.agent)
        #expect(roundTrip.name == "未知 CLI")
        #expect(roundTrip.agent == unrecognised)
        #expect(roundTrip.id == "")

        // Also test effectiveAgent preserves unrecognized
        #expect(effectiveAgent(unrecognised) == unrecognised)
        #expect(agentInfo(effectiveAgent(unrecognised)).name == "未知 CLI")
    }

    @Test func looksUpAgentsByTargetId() {
        #expect(agentById("codex").agent == .codex)
        #expect(agentById("claude").agent == .claude)
        #expect(agentById("").id == "")
        #expect(agentById("unknown").id == "")
        #expect(agentById(nil).id == "")
        #expect(agentInfo(agentById("unknown").agent).name == "未知 CLI")
    }

    @Test func checksWhetherAnAgentEnumIsRecognized() {
        #expect(isKnownAgent(.codex) == true)
        #expect(isKnownAgent(.claude) == true)
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
