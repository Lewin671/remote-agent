// What the home and project screens do with their launch arguments, for
// looking at them with sample data (`-demo`) in states that take a hand to
// reach: the scope and its sheets, a title filter, 项目设置 on a page, a
// draft and how a start stands, 项目管理's menu and dialogs, a change to
// the preferences on its way, and `-demoTweak`'s changes to the sample
// account. Every argument and signal is listed in one place, the head of
// App/Launch.swift. Debug builds only: in a release build nothing is read
// and nothing changes.
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
        await remote.showForTest(state: s, modelLists: Demo.models)
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
#endif
