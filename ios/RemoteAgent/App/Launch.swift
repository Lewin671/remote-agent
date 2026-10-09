// Launch arguments for working on the app without typing into it, and
// signals for driving it from a script. Debug builds only: in a release
// build `LaunchDriver` and `debugSignal` do nothing and none of the
// arguments is read.
//
//   -demo [everyday|crowded|empty]   sample data, no server, nothing stored
//   -demoRoute <route>               with -demo: open on a destination (`Route.path`, e.g. devices, session/run)
//   -demoRemoved                     with -demo: the 已被移出账户 notice
//   -demoLink <waiting|noNetwork|noServer>   with -demo: a connection that is down
//   -joinStep <step>                 the join flow in a fixed state: form, filled, error, cancelled,
//                                    working, emoji, registering, secret, secretError, scan, scanDenied, scanMissing
//   -addDeviceStep <step>            with -demo -demoRoute add-device: waiting, compare, compareMachine,
//                                    sending, done, failed, uncertain
//
//   -joinSecret <ra1…>               join with an account secret (an account already on the device is
//                                    kept when it is the same one, and replaced otherwise)
//   -joinServer <url> -joinCode <8 digits>   join with a pairing code
//   -resetAccount                    forget the account on this device first
//   -route <route>                   open on a destination once the account is in use
//
// Signals, sent with `xcrun simctl spawn <udid> notifyutil -p
// io.github.lewin671.remoteagent.debug.<name>`: join.cancel, pair.approve,
// pair.reject, removed.retry, name.skip, app.logout.
import Foundation
import RACore
import SwiftUI

#if DEBUG
import notify

@MainActor
enum LaunchDriver {
    private static let args = ProcessInfo.processInfo.arguments

    private static func value(_ name: String) -> String? {
        guard let i = args.firstIndex(of: name), i + 1 < args.count, !args[i + 1].hasPrefix("-") else { return nil }
        return args[i + 1]
    }

    /// The tests host themselves in the app: it must not read the real Keychain or connect meanwhile.
    static let isTesting = ["XCTestConfigurationFilePath", "XCTestBundlePath", "XCTestSessionIdentifier"]
        .contains { ProcessInfo.processInfo.environment[$0] != nil }

    static let demo: DemoSet? = args.contains("-demo") ? DemoSet(rawValue: value("-demo") ?? "") ?? .everyday : nil
    static let joinStep = value("-joinStep")
    static let startRoute: Route? = (value("-demoRoute") ?? value("-route")).flatMap(Route.init(path:))
    private static var secret = value("-joinSecret")
    private static var code = value("-joinCode")
    private static var reset = args.contains("-resetAccount")

    /// The scanner's states that the simulator cannot be put in.
    static var cameraAccess: CameraAccess? {
        switch joinStep {
        case "scanDenied": .denied
        case "scanMissing": .missing
        default: nil
        }
    }

    /// Called once, before the first frame: puts the model in sample mode when asked to.
    static func prepare(_ model: AppModel) {
        if joinStep != nil {
            model.showJoinForDemo(lastServer: "https://ra.example.com")
        } else if let demo {
            let remote = Demo.remote()
            var state = Demo.state(demo)
            if args.contains("-demoRemoved") { state.removed = true }
            switch value("-demoLink") {
            case "waiting": (state.conn, state.link) = (.connecting, .waiting)
            case "noNetwork": (state.conn, state.link) = (.offline, .noNetwork)
            case "noServer":
                (state.conn, state.link, state.error) = (.offline, .noServer, "连不上服务器")
                state.errorDetail = "NSURLErrorDomain -1004: Could not connect to the server."
            default: break
            }
            if state.link != .online {
                state.synced = true
                state.offlineSinceMs = nowMs() - 3 * 60_000
            }
            let shown = state
            let events = Demo.events(demo)
            // The first frame has the data; the feed is followed once the Remote shows it too.
            model.showDemo(account: Demo.account, remote: remote, initial: shown) {
                await remote.showForTest(state: shown, events: events, modelLists: Demo.models)
            }
        }
        DebugSignals.on("app.logout") { Task { await model.logout() } }
    }

    /// What the stage asks of the launch arguments: an account that is not
    /// the one asked for is forgotten, so that the join flow comes up.
    static func stageChanged(_ model: AppModel) async {
        guard model.stage == .ready, !model.isDemo else { return }
        if reset {
            reset = false
            await model.logout()
        } else if let text = secret, let wanted = try? Secret.parse(text) {
            if model.account?.root == wanted.root && model.account?.server == wanted.server {
                secret = nil
            } else {
                await model.logout()
            }
        }
    }

    /// Starts the join the launch arguments ask for, or shows the state they name.
    static func drive(_ join: JoinModel) {
        if let step = joinStep {
            preview(join, step)
        } else if let text = secret {
            secret = nil
            join.showSecret()
            join.secret = text
            join.joinWithSecret()
        } else if let digits = code, let server = value("-joinServer") {
            code = nil
            join.server = server
            join.typeCode(digits)
            join.joinWithCode()
        }
    }

    private static func preview(_ join: JoinModel, _ step: String) {
        let emoji = ["🦊", "🌵", "🍉", "🌙"]
        switch step {
        case "filled": join.show(step: .form, code: "48291756")
        case "error": join.show(step: .form, code: "48291756", notice: "配对码不对，或者已经用过了", error: true)
        case "cancelled": join.show(step: .form, notice: "已取消本次加入。旧请求可能还占着配对码：请在已有设备上拒绝它，再生成新码")
        case "working": join.show(step: .working, code: "48291756")
        case "emoji": join.show(step: .emoji(emoji), code: "48291756")
        case "registering": join.show(step: .emoji(emoji), code: "48291756", notice: "已经在登记\(thisDeviceWord)，稍等结果")
        case "secret": join.show(step: .secret)
        case "secretError":
            join.show(step: .secret, secret: "ra1.not-a-secret", notice: "这不是 remote-agent 的账户密钥", error: true)
        case "scan", "scanDenied", "scanMissing": join.show(step: .scan)
        default: join.show(step: .form, server: "https://")
        }
    }

    /// Shows the step `-addDeviceStep` names; false when there is none and the pairing should start.
    static func preview(_ model: AddDeviceModel) -> Bool {
        guard let step = value("-addDeviceStep") else { return false }
        let emoji = ["🦊", "🌵", "🍉", "🌙"]
        let link = PairLink(code: "48291756", server: "https://ra.example.com", commit: Data(repeating: 7, count: 32))
        switch step {
        case "compare": model.show(.compare(emoji: emoji, machine: false))
        case "compareMachine": model.show(.compare(emoji: emoji, machine: true))
        case "sending": model.show(.sending(emoji: emoji, machine: false))
        case "done": model.show(.done)
        case "failed": model.show(.failed("配对已过期或被取消", uncertain: false))
        case "uncertain":
            model.show(.failed("新设备没有来取账户密钥，它可能没有加入成功。请在新设备上确认，必要时重新配对。", uncertain: true))
        case "starting": model.show(.starting)
        default: model.show(.waiting(code: link.code, link: link))
        }
        return true
    }
}

/// Darwin notifications as buttons a script can press. Each name has one
/// handler, the last view that asked for it.
@MainActor
enum DebugSignals {
    private static var handlers: [String: () -> Void] = [:]
    private static var tokens: [String: Int32] = [:]

    static func on(_ name: String, _ handler: @escaping () -> Void) {
        handlers[name] = handler
        guard tokens[name] == nil else { return }
        var token: Int32 = 0
        let status = notify_register_dispatch("io.github.lewin671.remoteagent.debug." + name, &token, .main) { _ in
            MainActor.assumeIsolated { handlers[name]?() }
        }
        if status == 0 { tokens[name] = token }
    }

    static func off(_ name: String) {
        handlers[name] = nil
    }
}

private struct DebugSignal: ViewModifier {
    let name: String
    let action: () -> Void

    func body(content: Content) -> some View {
        content
            .onAppear { DebugSignals.on(name, action) }
            .onDisappear { DebugSignals.off(name) }
    }
}

extension View {
    /// Lets a script do what a tap here would (debug builds).
    func debugSignal(_ name: String, _ action: @escaping () -> Void) -> some View {
        modifier(DebugSignal(name: name, action: action))
    }
}
#else
@MainActor
enum LaunchDriver {
    static let isTesting = false
    static let startRoute: Route? = nil
    static func prepare(_ model: AppModel) {}
    static func stageChanged(_ model: AppModel) async {}
    static func drive(_ join: JoinModel) {}
    static func preview(_ model: AddDeviceModel) -> Bool { false }
}

extension View {
    func debugSignal(_ name: String, _ action: @escaping () -> Void) -> some View { self }
}
#endif
