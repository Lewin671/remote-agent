// Launch arguments for looking at the home and project screens in states
// that take a hand to reach, with sample data (`-demo`). Debug builds
// only: in a release build nothing is read and nothing changes.
//
//   -homeScope <all|path>        the scope the home page opens on
//   -homeSheet <scope|browse|pick|pickBrowse>   看哪个项目, 新会话 · 选项目, or either on 新项目 · 选目录
//   -homeFilter <text>           something typed in the title filter
//   -homeExpand                  需要处理 and 进行中 opened in full
//   -projectSheet <settings|machine|model|context|mode>   项目设置 on a page; -projectAgent <codex|claude> picks its tab
//   -projectDraft <text>         something written in the new-session box
//   -projectStart <running|failed|unknown|expired>   how a start stands
//   -projectEnded                a project's 已结束 opened
//   -projectsMenu <path> / -projectsRemove <path> / -projectsAdd / -projectsElsewhere   项目管理's menu, dialog, sheet, notice
//   -prefsSaving <path> / -prefsFailed <path>   a change to a project's preferences on its way, or failed
//   -dirPath <path> / -dirError  the folder 新项目 · 选目录 opens on, or a listing that fails
//   -demoTweak <a,b,…>           changes to the sample account: mismatch, fullAccess, loading, noneOnline,
//                                firstRun, listFailed, added, machineOffline
import Foundation
import RACore
import RAProto
import SwiftUI

@MainActor
enum HomeLaunch {
    #if DEBUG
    private static let args = ProcessInfo.processInfo.arguments

    static func value(_ name: String) -> String? {
        guard let i = args.firstIndex(of: name), i + 1 < args.count, !args[i + 1].hasPrefix("-") else { return nil }
        return args[i + 1]
    }

    static func flag(_ name: String) -> Bool { args.contains(name) }

    private static var applied = false
    private static var done = false

    /// Returns once the sample account is as the arguments ask; at once with a real account.
    static func settled(_ model: AppModel) async {
        guard model.isDemo else { return }
        for _ in 0..<150 where !done { try? await Task.sleep(for: .milliseconds(20)) }
    }

    /// Puts the sample account in the state the arguments ask for, once, after the sample data is in place.
    static func apply(_ model: AppModel, _ context: AccountContext) async {
        guard model.isDemo, !applied else { return }
        applied = true
        let remote = context.remote
        // The sample data is shown from a task of its own; what is changed here must come after it.
        for _ in 0..<100 where remote.current.devices.isEmpty { try? await Task.sleep(for: .milliseconds(20)) }
        var s = remote.current
        for tweak in (value("-demoTweak") ?? "").split(separator: ",") {
            switch tweak {
            case "mismatch":
                s.machines["mac"]?.meta?.protocolVersion = protocolVersion - 1
            case "machineOffline":
                s.machines["mac"]?.online = false
            case "fullAccess":
                setPrefs(&s) {
                    $0.settings.append(.with {
                        $0.path = Demo.foo
                        $0.machineID = "mac"
                        $0.agent = .codex
                        $0.agents = [.with {
                            $0.agent = .codex
                            $0.mode = .fullAccess
                        }]
                    })
                }
            case "added":
                setPrefs(&s) {
                    $0.settings.append(.with {
                        $0.path = Demo.home + "/Code/ai-bot"
                        $0.machineID = "mac"
                        $0.updatedMs = nowMs()
                    })
                }
            case "loading":
                s.loaded = false
                s.sessions = [:]
                s.machines = [:]
            case "noneOnline":
                s.machines["mac"] = machine(online: false)
            case "firstRun":
                s.machines["mac"] = machine(online: true)
            case "listFailed":
                s.sessionsListed = false
                s.sessionsError = "连接超时"
            default:
                break
            }
        }
        await remote.showForTest(state: s, modelLists: ProjectDemo.models)
        if let scope = value("-homeScope") { context.homeScopes.show(scope == "all" ? .all : .of(key: scope)) }
        if let path = value("-prefsSaving") { context.prefsChanges.show(.saving(.pin(path: path)), for: path) }
        if let path = value("-prefsFailed") { context.prefsChanges.show(.failed(.pin(path: path), "连接超时"), for: path) }
        done = true
    }

    private static func setPrefs(_ s: inout RemoteState, _ change: (inout PBProjectPrefs) -> Void) {
        var prefs = s.prefs ?? .empty
        change(&prefs.prefs)
        prefs.revision += 1
        s.prefs = prefs
    }

    private static func machine(online: Bool) -> MachineView {
        MachineView(id: "mac", online: online, lastSeenMs: nowMs(), meta: .with { meta in
            meta.hostname = "MacBook"
            meta.home = Demo.home
            meta.protocolVersion = protocolVersion
            meta.agents = [PBAgent.codex, .claude].map { agent in
                .with {
                    $0.agent = agent
                    $0.defaultMode = .ask
                    $0.defaultModeSource = .cliConfig
                }
            }
        }, metaRev: 1)
    }
    #else
    static func value(_ name: String) -> String? { nil }
    static func flag(_ name: String) -> Bool { false }
    static func settled(_ model: AppModel) async {}
    #endif
}

extension View {
    /// Applies the launch arguments above to the sample account (debug builds).
    func homeLaunchTweaks() -> some View {
        #if DEBUG
        modifier(HomeLaunchTweaks())
        #else
        self
        #endif
    }
}

#if DEBUG
private struct HomeLaunchTweaks: ViewModifier {
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context

    func body(content: Content) -> some View {
        content.task { await HomeLaunch.apply(model, context) }
    }
}

/// Sample data the project screens need and the account's sample data does not carry.
@MainActor
enum ProjectDemo {
    /// Models with efforts and context windows, for 项目设置 › 模型.
    static let models: [(machineId: String, agent: PBAgent, models: PBListModelsResponse)] = {
        func model(_ slug: String, _ name: String, efforts: [String] = [], window: Int64 = 0) -> PBModelInfo {
            .with {
                $0.slug = slug
                $0.displayName = name
                $0.efforts = efforts
                $0.defaultEffort = efforts.isEmpty ? "" : "medium"
                $0.contextWindow = window
            }
        }
        let efforts = ["low", "medium", "high", "xhigh", "max", "ultra"]
        let codex = PBListModelsResponse.with {
            $0.defaultModel = "gpt-6.1-sol"
            $0.models = [
                model("gpt-6.1-sol", "GPT-6.1-Sol", efforts: efforts), model("gpt-6-astra", "GPT-6-Astra", efforts: efforts),
                model("gpt-6-sol", "GPT-6-Sol", efforts: efforts), model("gpt-6-luna", "GPT-6-Luna", efforts: efforts),
                model("gpt-5.6-sol", "GPT-5.6-Sol", efforts: efforts), model("gpt-5.6-terra", "GPT-5.6-Terra", efforts: efforts),
                model("gpt-5.6-luna", "GPT-5.6-Luna", efforts: efforts), model("gpt-5.5", "GPT-5.5", efforts: efforts),
                model("a-model-with-a-very-long-slug-and-no-display-name-2026-10-09-preview", ""),
            ]
        }
        let claude = PBListModelsResponse.with {
            $0.defaultModel = "sonnet"
            $0.models = [
                model("opus", "Opus", efforts: ["low", "medium", "high"], window: 1_000_000),
                model("sonnet", "Sonnet", efforts: ["low", "medium", "high"], window: 1_000_000),
                model("haiku", "Haiku", window: 200_000),
            ]
        }
        let machines = ["mac", "vps", "imac", "studio", "ci", "nas"]
        return machines.map { (machineId: $0, agent: PBAgent.codex, models: codex) }
            + machines.map { (machineId: $0, agent: PBAgent.claude, models: claude) }
    }()

    private static let tree: [String: [String]] = [
        Demo.home: ["Code", "Documents", "Downloads", "work"],
        Demo.home + "/Code": [
            "ai-bot", "a-very-long-project-name-that-keeps-going-and-going", "bar", "billing", "blog", "demo", "design-system", "foo",
            "infra", "mobile", "remote-agent", "shop", "数据平台",
        ],
        Demo.home + "/Code/ai-bot": ["remote-agent", "happy", "notes-sync"],
        Demo.home + "/work": ["blog"],
    ]

    /// A made-up home folder, listed after a moment as a computer would.
    static let directories = DirectorySource { _, path in
        try await Task.sleep(for: .milliseconds(120))
        if HomeLaunch.flag("-dirError") { throw CommandError("not_found", "目录不存在") }
        let at = path.isEmpty ? Demo.home : path
        return .with {
            $0.path = at
            $0.home = Demo.home
            $0.parent = at == "/" ? "" : (at.split(separator: "/").dropLast().map { "/" + $0 }.joined().isEmpty ? "/" : at.split(separator: "/").dropLast().map { "/" + $0 }.joined())
            $0.dirs = tree[at] ?? []
        }
    }
}
#endif
