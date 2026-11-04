// One module that knows the set of CLIs and what differs between them.
// Every other file asks the registry.
import Foundation
import RAProto

public func normalizePlanName(_ plan: String) -> String {
    switch plan.lowercased() {
    case "prolite": return "Pro Lite"
    case "claude_max": return "Max"
    case "claude_pro": return "Pro"
    case "claude_team": return "Team"
    case "claude_enterprise": return "Enterprise"
    default:
        return planWordBreak.split(claudePrefix.replace(plan, with: ""))
            .filter { !$0.isEmpty }
            .map(capitalized)
            .joined(separator: " ")
    }
}

public func gptSlugLabel(_ id: String) -> String {
    id.split(separator: "-").map { word in
        if word.caseInsensitiveCompare("gpt") == .orderedSame {
            return "GPT"
        }
        guard let first = word.first else { return "" }
        return first.uppercased() + word.dropFirst()
    }.joined(separator: "-")
}

private let claudePrefix = Rx("\\Aclaude_", ignoreCase: true)
private let planWordBreak = Rx("[_\(Jre.s)]+")

private func capitalized(_ word: String) -> String {
    guard let first = word.unicodeScalars.first, first.value <= 0xFFFF else { return word }
    return first.properties.uppercaseMapping + String(word.unicodeScalars.dropFirst())
}

public struct AgentEntry: Sendable, Equatable, Identifiable {
    public let agent: PBAgent
    public let id: String
    public let name: String
    public let quotaWindows: [String]
    public let toolLabelFromItemType: Bool
    public let planClearContext: Bool
    public let planApprovalReturnsToAsk: Bool
    public let standardContextOption: Bool
    public let trustFolderHint: String?
    public let usageHint: String?
    public let reconcilesUnclaimedInput: Bool
    public let sessionNeedsModelList: Bool

    private let _planName: @Sendable (String) -> String
    private let _modelLabel: @Sendable (String) -> String
    private let _defaults: @Sendable (PBDaemonSettings?) -> PBAgentDefaults
    private let _withDefaults: @Sendable (PBDaemonSettings, String?, String?, PBApprovalMode?) -> PBDaemonSettings
    private let _sheetTitle: @Sendable (String) -> String

    public init(
        agent: PBAgent,
        id: String,
        name: String,
        quotaWindows: [String],
        toolLabelFromItemType: Bool,
        planClearContext: Bool,
        planApprovalReturnsToAsk: Bool,
        standardContextOption: Bool,
        trustFolderHint: String?,
        usageHint: String?,
        reconcilesUnclaimedInput: Bool,
        sessionNeedsModelList: Bool,
        planName: @escaping @Sendable (String) -> String,
        modelLabel: @escaping @Sendable (String) -> String,
        defaults: @escaping @Sendable (PBDaemonSettings?) -> PBAgentDefaults,
        withDefaults: @escaping @Sendable (PBDaemonSettings, String?, String?, PBApprovalMode?) -> PBDaemonSettings,
        sheetTitle: @escaping @Sendable (String) -> String
    ) {
        self.agent = agent
        self.id = id
        self.name = name
        self.quotaWindows = quotaWindows
        self.toolLabelFromItemType = toolLabelFromItemType
        self.planClearContext = planClearContext
        self.planApprovalReturnsToAsk = planApprovalReturnsToAsk
        self.standardContextOption = standardContextOption
        self.trustFolderHint = trustFolderHint
        self.usageHint = usageHint
        self.reconcilesUnclaimedInput = reconcilesUnclaimedInput
        self.sessionNeedsModelList = sessionNeedsModelList
        self._planName = planName
        self._modelLabel = modelLabel
        self._defaults = defaults
        self._withDefaults = withDefaults
        self._sheetTitle = sheetTitle
    }

    public func planName(_ raw: String) -> String { _planName(raw) }
    public func modelLabel(_ id: String) -> String { _modelLabel(id) }
    public func defaults(_ settings: PBDaemonSettings?) -> PBAgentDefaults { _defaults(settings) }
    public func withDefaults(
        _ settings: PBDaemonSettings,
        model: String? = nil,
        effort: String? = nil,
        mode: PBApprovalMode? = nil
    ) -> PBDaemonSettings {
        _withDefaults(settings, model, effort, mode)
    }
    public func withDefaults(
        _ settings: PBDaemonSettings,
        model: String? = nil,
        effort: String? = nil,
        mode: Mode?
    ) -> PBDaemonSettings {
        _withDefaults(settings, model, effort, mode?.proto ?? .unspecified)
    }
    public func sheetTitle(_ what: String) -> String { _sheetTitle(what) }

    public static func == (lhs: AgentEntry, rhs: AgentEntry) -> Bool {
        lhs.agent == rhs.agent && lhs.id == rhs.id
    }
}

private let CODEX_ENTRY = AgentEntry(
    agent: .codex,
    id: "codex",
    name: "Codex",
    quotaWindows: ["primary", "secondary"],
    toolLabelFromItemType: false,
    planClearContext: true,
    planApprovalReturnsToAsk: false,
    standardContextOption: false,
    trustFolderHint: nil,
    usageHint: nil,
    reconcilesUnclaimedInput: false,
    sessionNeedsModelList: false,
    planName: { normalizePlanName($0) },
    modelLabel: { gptSlugLabel($0) },
    defaults: { settings in
        guard let s = settings else { return PBAgentDefaults() }
        return .with {
            $0.model = s.defaultModel
            $0.effort = s.defaultEffort
            $0.mode = s.defaultMode
        }
    },
    withDefaults: { settings, model, effort, mode in
        var s = settings
        if let model { s.defaultModel = model }
        if let effort { s.defaultEffort = effort }
        if let mode { s.defaultMode = mode }
        return s
    },
    sheetTitle: { $0 }
)

private let CLAUDE_ENTRY = AgentEntry(
    agent: .claude,
    id: "claude",
    name: "Claude Code",
    quotaWindows: ["five_hour", "seven_day"],
    toolLabelFromItemType: true,
    planClearContext: false,
    planApprovalReturnsToAsk: true,
    standardContextOption: true,
    trustFolderHint: "默认选中的是 No, exit。按 ↓ 选中 Yes, I trust this folder 再按 ⏎",
    usageHint: " 可在本机 Claude Code 中查看 /usage。",
    reconcilesUnclaimedInput: true,
    sessionNeedsModelList: true,
    planName: { normalizePlanName($0) },
    modelLabel: { $0 },
    defaults: { settings in
        settings?.claude ?? PBAgentDefaults()
    },
    withDefaults: { settings, model, effort, mode in
        var s = settings
        var c = s.claude
        if let model { c.model = model }
        if let effort { c.effort = effort }
        if let mode { c.mode = mode }
        s.claude = c
        return s
    },
    sheetTitle: { "Claude Code · \($0)" }
)

private func unknownEntry(agent: PBAgent) -> AgentEntry {
    AgentEntry(
        agent: agent,
        id: "",
        name: "未知 CLI",
        quotaWindows: [],
        toolLabelFromItemType: false,
        planClearContext: false,
        planApprovalReturnsToAsk: false,
        standardContextOption: false,
        trustFolderHint: nil,
        usageHint: nil,
        reconcilesUnclaimedInput: false,
        sessionNeedsModelList: false,
        planName: { $0 },
        modelLabel: { $0 },
        defaults: { _ in PBAgentDefaults() },
        withDefaults: { settings, _, _, _ in settings },
        sheetTitle: { $0 }
    )
}

public let AGENTS: [AgentEntry] = [CODEX_ENTRY, CLAUDE_ENTRY]

public let defaultAgent: PBAgent = CODEX_ENTRY.agent
public let DEFAULT_AGENT: PBAgent = defaultAgent

public func isKnownAgent(_ a: PBAgent?) -> Bool {
    guard let a else { return false }
    return AGENTS.contains { $0.agent == a }
}

public func agentInfo(_ a: PBAgent? = nil) -> AgentEntry {
    guard let a, a != .unspecified else { return CODEX_ENTRY }
    if let found = AGENTS.first(where: { $0.agent == a }) {
        return found
    }
    return unknownEntry(agent: a)
}

public func agentById(_ id: String?) -> AgentEntry {
    guard let id, !id.isEmpty else { return unknownEntry(agent: .UNRECOGNIZED(-1)) }
    if let found = AGENTS.first(where: { $0.id == id }) {
        return found
    }
    return unknownEntry(agent: .UNRECOGNIZED(-1))
}

public func agentName(_ a: PBAgent? = nil) -> String {
    agentInfo(a).name
}

public func defaultsFor(_ settings: PBDaemonSettings?, agent: PBAgent) -> PBAgentDefaults {
    agentInfo(agent).defaults(settings)
}

public func withDefaults(
    _ settings: PBDaemonSettings,
    agent: PBAgent,
    model: String? = nil,
    effort: String? = nil,
    mode: PBApprovalMode? = nil
) -> PBDaemonSettings {
    agentInfo(agent).withDefaults(settings, model: model, effort: effort, mode: mode)
}

public func withDefaults(
    _ settings: PBDaemonSettings,
    agent: PBAgent,
    model: String? = nil,
    effort: String? = nil,
    mode: Mode?
) -> PBDaemonSettings {
    agentInfo(agent).withDefaults(settings, model: model, effort: effort, mode: mode)
}

public func machineVersions(_ meta: PBMachineMeta?) -> [PBAgentInfo] {
    guard let meta else { return [] }
    if !meta.agents.isEmpty { return meta.agents }
    return [
        .with {
            $0.agent = defaultAgent
            $0.version = meta.codexVersion
            $0.verified = meta.codexVerified
        }
    ]
}

extension PBDaemonSettings {
    public func defaults(for agent: PBAgent) -> PBAgentDefaults {
        defaultsFor(self, agent: agent)
    }

    public func with(model: String? = nil, effort: String? = nil, for agent: PBAgent) -> PBDaemonSettings {
        withDefaults(self, agent: agent, model: model, effort: effort)
    }

    public func with(mode: Mode?, for agent: PBAgent) -> PBDaemonSettings {
        withDefaults(self, agent: agent, mode: mode)
    }
}
