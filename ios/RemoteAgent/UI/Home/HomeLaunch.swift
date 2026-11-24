// What the home and project screens do with their launch arguments, for
// looking at them with sample data (`-demo`).
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
        let host = context.coreHost
        // The sample data is shown from a task of its own; what is changed here must come after it.
        for _ in 0..<100 where !(await host.isDemoSeeded()) { try? await Task.sleep(for: .milliseconds(20)) }
        let tweaks = (value("-demoTweak") ?? "").split(separator: ",")
        if !tweaks.isEmpty {
            var w = await host.currentDemoWorld()
            for tweak in tweaks {
                switch tweak {
                case "mismatch":
                    w.machines["mac"]?.meta.protocolVersion = peerProtocolVersion - 1
                case "machineOffline":
                    w.machines["mac"]?.online = false
                case "fullAccess":
                    setPrefs(&w) {
                        $0.settings.append(.with {
                            $0.path = Demo.foo
                            $0.machineID = "mac"
                            $0.agent = defaultAgent
                            $0.agents = [.with {
                                $0.agent = defaultAgent
                                $0.mode = .fullAccess
                            }]
                        })
                    }
                case "added":
                    setPrefs(&w) {
                        $0.settings.append(.with {
                            $0.path = Demo.home + "/Code/ai-bot"
                            $0.machineID = "mac"
                            $0.updatedMs = nowMs()
                        })
                    }
                case "loading":
                    w.sessions = [:]
                    w.machines = [:]
                case "noneOnline":
                    w.machines["mac"] = machine(online: false)
                case "firstRun":
                    w.machines["mac"] = machine(online: true)
                case "diskLow":
                    w.machines["mac"]?.meta.diskStatus = .with { $0.totalBytes = 460_000_000_000; $0.availableBytes = 8_200_000_000; $0.pressure = .warning }
                case "diskCritical":
                    w.machines["mac"]?.meta.diskStatus = .with { $0.totalBytes = 460_000_000_000; $0.availableBytes = 1_200_000_000; $0.pressure = .critical }
                case "diskTwo":
                    w.machines["mac"]?.meta.diskStatus = .with { $0.totalBytes = 460_000_000_000; $0.availableBytes = 8_200_000_000; $0.pressure = .warning }
                    w.machines["vps"]?.meta.diskStatus = .with { $0.totalBytes = 200_000_000_000; $0.availableBytes = 1_200_000_000; $0.pressure = .critical }
                default:
                    break
                }
            }
            await host.appendDemoSessionData(world: w)
        }
        if let scope = value("-homeScope") {
            context.dispatch { $0.homeSetScope.scopeKey = scope == "all" ? "" : scope }
        }
        done = true
    }

    private static func setPrefs(_ w: inout DemoWorld, _ change: (inout PBProjectPrefs) -> Void) {
        if w.prefs.prefs.schema == 0 { w.prefs.prefs.schema = 1 }
        change(&w.prefs.prefs)
        w.prefs.revision += 1
    }

    private static func machine(online: Bool) -> PBCorePlainMachine {
        .with {
            $0.machineID = "mac"
            $0.online = online
            $0.lastSeenMs = nowMs()
            $0.meta = .with { meta in
                meta.hostname = "MacBook"
                meta.home = Demo.home
                meta.protocolVersion = peerProtocolVersion
                meta.agents = AGENTS.map { entry in
                    .with {
                        $0.agent = entry.agent
                        $0.defaultMode = .ask
                        $0.defaultModeSource = .cliConfig
                    }
                }
            }
            $0.metaRev = 1
        }
    }
    #else
    static func value(_ name: String) -> String? { nil }
    static func flag(_ name: String) -> Bool { false }
    static func apply(_ model: AppModel, _ context: AccountContext) async {}
    static func settled(_ model: AppModel) async {}
    #endif
}

extension View {
    func homeLaunchTweaks() -> some View {
        modifier(HomeLaunchModifier())
    }
}

private struct HomeLaunchModifier: ViewModifier {
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context

    func body(content: Content) -> some View {
        content.task { await HomeLaunch.apply(model, context) }
    }
}
