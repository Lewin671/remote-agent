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

public struct QuotaWindowDescriptor: Sendable, Equatable {
    public let label: String
    public let group: String?

    public init(label: String, group: String?) {
        self.label = label
        self.group = group
    }
}

public struct AgentEntry: Sendable, Equatable, Identifiable {
    public let agent: PBAgent
    public let id: String
    public let name: String
    /// Nil means the CLI reports its own windows; otherwise order these names first.
    public let expectedQuotaWindows: [String]?
    private let _quotaWindow: @Sendable (String) -> QuotaWindowDescriptor
    public let toolLabelFromItemType: Bool
    public let planClearContext: Bool
    public let planNote: String?
    public let planKeepLabel: String
    public let planApproveLabel: String
    public let standardContextOption: Bool
    public let autoMayBeUnavailable: Bool
    public let trustFolderHint: String?
    public let usageHint: String?
    public let reconcilesUnclaimedInput: Bool
    public let sessionNeedsModelList: Bool
    public let switchesModelInSession: Bool
    public let forks: Bool

    private let _planName: @Sendable (String) -> String
    private let _modelLabel: @Sendable (String) -> String
    private let _defaults: @Sendable (PBDaemonSettings?) -> PBAgentDefaults
    private let _withDefaults: @Sendable (PBDaemonSettings, String?, String?, PBApprovalMode?) -> PBDaemonSettings

    public init(
        agent: PBAgent,
        id: String,
        name: String,
        expectedQuotaWindows: [String]?,
        quotaWindow: @escaping @Sendable (String) -> QuotaWindowDescriptor,
        toolLabelFromItemType: Bool,
        planClearContext: Bool,
        planNote: String?,
        standardContextOption: Bool,
        autoMayBeUnavailable: Bool,
        trustFolderHint: String?,
        usageHint: String?,
        reconcilesUnclaimedInput: Bool,
        sessionNeedsModelList: Bool,
        switchesModelInSession: Bool,
        forks: Bool,
        planKeepLabel: String = "继续规划…",
        planApproveLabel: String = "批准执行",
        planName: @escaping @Sendable (String) -> String,
        modelLabel: @escaping @Sendable (String) -> String,
        defaults: @escaping @Sendable (PBDaemonSettings?) -> PBAgentDefaults,
        withDefaults: @escaping @Sendable (PBDaemonSettings, String?, String?, PBApprovalMode?) -> PBDaemonSettings
    ) {
        self.agent = agent
        self.id = id
        self.name = name
        self.expectedQuotaWindows = expectedQuotaWindows
        self._quotaWindow = quotaWindow
        self.toolLabelFromItemType = toolLabelFromItemType
        self.planClearContext = planClearContext
        self.planNote = planNote
        self.planKeepLabel = planKeepLabel
        self.planApproveLabel = planApproveLabel
        self.standardContextOption = standardContextOption
        self.autoMayBeUnavailable = autoMayBeUnavailable
        self.trustFolderHint = trustFolderHint
        self.usageHint = usageHint
        self.reconcilesUnclaimedInput = reconcilesUnclaimedInput
        self.sessionNeedsModelList = sessionNeedsModelList
        self.switchesModelInSession = switchesModelInSession
        self.forks = forks
        self._planName = planName
        self._modelLabel = modelLabel
        self._defaults = defaults
        self._withDefaults = withDefaults
    }

    public func quotaWindow(_ name: String) -> QuotaWindowDescriptor { _quotaWindow(name) }
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

    public static func == (lhs: AgentEntry, rhs: AgentEntry) -> Bool {
        lhs.agent == rhs.agent && lhs.id == rhs.id
    }
}

private func fixedWindowLabel(_ name: String) -> QuotaWindowDescriptor {
    if name == "primary" || name == "five_hour" { return QuotaWindowDescriptor(label: "5 小时", group: nil) }
    if name == "secondary" || name == "seven_day" { return QuotaWindowDescriptor(label: "7 天", group: nil) }
    return QuotaWindowDescriptor(label: name, group: nil)
}

private let ANTIGRAVITY_QUOTA_WINDOWS: [String: QuotaWindowDescriptor] = [
    "gemini-5h": QuotaWindowDescriptor(label: "Gemini · 5 小时", group: "Gemini"),
    "gemini-weekly": QuotaWindowDescriptor(label: "Gemini · 7 天", group: "Gemini"),
    "gemini-argon-wtu-weekly": QuotaWindowDescriptor(label: "Gemini 4 Argon · 7 天", group: "Gemini"),
    "3p-5h": QuotaWindowDescriptor(label: "第三方模型 · 5 小时", group: "第三方模型（Claude、GPT-OSS）"),
    "3p-weekly": QuotaWindowDescriptor(label: "第三方模型 · 7 天", group: "第三方模型（Claude、GPT-OSS）"),
]

private var CODEX_ENTRY: AgentEntry { AgentEntry(
    agent: .codex,
    id: "codex",
    name: "Codex",
    expectedQuotaWindows: ["primary", "secondary"],
    quotaWindow: { fixedWindowLabel($0) },
    toolLabelFromItemType: false,
    planClearContext: true,
    planNote: nil,
    standardContextOption: false,
    autoMayBeUnavailable: false,
    trustFolderHint: nil,
    usageHint: nil,
    reconcilesUnclaimedInput: false,
    sessionNeedsModelList: false,
    switchesModelInSession: true,
    forks: true,
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
    }
) }

private var CLAUDE_ENTRY: AgentEntry { AgentEntry(
    agent: .claude,
    id: "claude",
    name: "Claude Code",
    expectedQuotaWindows: ["five_hour", "seven_day"],
    quotaWindow: { fixedWindowLabel($0) },
    toolLabelFromItemType: true,
    planClearContext: false,
    planNote: "批准后使用询问模式",
    standardContextOption: true,
    autoMayBeUnavailable: true,
    trustFolderHint: "默认选中的是 No, exit。按 ↓ 选中 Yes, I trust this folder 再按 ⏎",
    usageHint: " 可在本机 Claude Code 中查看 /usage。",
    reconcilesUnclaimedInput: true,
    sessionNeedsModelList: true,
    switchesModelInSession: true,
    forks: true,
    planKeepLabel: "修改计划…",
    planApproveLabel: "批准",
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
    }
) }

private var ANTIGRAVITY_ENTRY: AgentEntry { AgentEntry(
    agent: .antigravity,
    id: "agy",
    name: "Antigravity",
    expectedQuotaWindows: nil,
    quotaWindow: { name in
        ANTIGRAVITY_QUOTA_WINDOWS[name] ?? QuotaWindowDescriptor(label: name, group: name)
    },
    toolLabelFromItemType: false,
    planClearContext: false,
    planNote: "批准后回到默认模式（询问）",
    standardContextOption: false,
    autoMayBeUnavailable: true,
    trustFolderHint: nil,
    usageHint: nil,
    reconcilesUnclaimedInput: false,
    sessionNeedsModelList: true,
    switchesModelInSession: false,
    forks: false,
    planKeepLabel: "修改计划…",
    planApproveLabel: "批准",
    planName: { $0 },
    modelLabel: { $0 },
    defaults: { settings in
        settings?.antigravity ?? PBAgentDefaults()
    },
    withDefaults: { settings, model, effort, mode in
        var s = settings
        var a = s.antigravity
        if let model { a.model = model }
        if let effort { a.effort = effort }
        if let mode { a.mode = mode }
        s.antigravity = a
        return s
    }
) }

private func unknownEntry(agent: PBAgent) -> AgentEntry {
    AgentEntry(
        agent: agent,
        id: "",
        name: "未知 CLI",
        expectedQuotaWindows: nil,
        quotaWindow: { QuotaWindowDescriptor(label: $0, group: nil) },
        toolLabelFromItemType: false,
        planClearContext: false,
        planNote: nil,
        standardContextOption: false,
        autoMayBeUnavailable: false,
        trustFolderHint: nil,
        usageHint: nil,
        reconcilesUnclaimedInput: false,
        sessionNeedsModelList: false,
        switchesModelInSession: false,
        forks: false,
        planName: { $0 },
        modelLabel: { $0 },
        defaults: { _ in PBAgentDefaults() },
        withDefaults: { settings, _, _, _ in settings }
    )
}

public let AGENTS: [AgentEntry] = [CODEX_ENTRY, CLAUDE_ENTRY, ANTIGRAVITY_ENTRY]

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
