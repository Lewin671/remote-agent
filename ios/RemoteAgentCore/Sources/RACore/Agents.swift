// Minimal CLI display-name registry retained for shell presentation fallbacks.
import Foundation
import RAProto

public struct AgentEntry: Sendable, Equatable, Identifiable {
    public let agent: PBAgent
    public let id: String
    private let _name: LocalizedMessage
    public var name: String { _name.resolve() }

    public init(agent: PBAgent, id: String, name: LocalizedMessage) {
        self.agent = agent
        self.id = id
        self._name = name
    }

    public static func == (lhs: AgentEntry, rhs: AgentEntry) -> Bool {
        lhs.agent == rhs.agent && lhs.id == rhs.id
    }
}

private let CODEX_ENTRY = AgentEntry(agent: .codex, id: "codex", name: LocalizedMessage(raw: "Codex"))
private let CLAUDE_ENTRY = AgentEntry(agent: .claude, id: "claude", name: LocalizedMessage(raw: "Claude Code"))
private let ANTIGRAVITY_ENTRY = AgentEntry(agent: .antigravity, id: "agy", name: LocalizedMessage(raw: "Antigravity"))

public let AGENTS: [AgentEntry] = [CODEX_ENTRY, CLAUDE_ENTRY, ANTIGRAVITY_ENTRY]

public let defaultAgent: PBAgent = CODEX_ENTRY.agent

public func agentName(_ a: PBAgent? = nil) -> String {
    guard let a, a != .unspecified else { return CODEX_ENTRY.name }
    return AGENTS.first(where: { $0.agent == a })?.name ?? L10n.modelsUnknownCli()
}
