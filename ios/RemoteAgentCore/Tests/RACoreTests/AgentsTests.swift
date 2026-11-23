import Foundation
import RAProto
import Testing

@testable import RACore

@Suite(.timeLimit(.minutes(2))) struct AgentsTests {
    @Test func listsKnownClisInStableOrder() {
        #expect(AGENTS.map(\.id) == ["codex", "claude", "agy"])
        #expect(AGENTS.map(\.agent) == [.codex, .claude, .antigravity])
        #expect(defaultAgent == .codex)
    }

    @Test func mapsAgentEnumsToDisplayNames() {
        #expect(agentName(.codex) == "Codex")
        #expect(agentName(.claude) == "Claude Code")
        #expect(agentName(.antigravity) == "Antigravity")
        #expect(agentName(.unspecified) == "Codex")
        #expect(agentName(nil) == "Codex")
        #expect(agentName() == "Codex")
        #expect(agentName(.UNRECOGNIZED(99)) == L10n.modelsUnknownCli())
    }
}
