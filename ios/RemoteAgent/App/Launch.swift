// Launch arguments for working on the app without typing into it, and
// signals for driving it from a script. Debug builds only: in a release
// build `LaunchDriver` and `debugSignal` do nothing and none of the
// arguments is read.
//
// This is the one list of them all. An argument or a signal added anywhere
// in the app is added here; the file named with each group says more.
//
// Sample data (here, Demo/DemoData.swift)
//   -demo [everyday|crowded|empty]   sample data, no server, nothing stored
//   -demoRoute <route>               with -demo: open on a destination (`Route.path`, e.g. devices, session/run)
//   -demoRemoved                     with -demo: the 已被移出账户 notice
//   -demoLink <waiting|noNetwork|noServer>   with -demo: a connection that is down
//   -demoTweak <a,b,…>               with -demo: changes to the sample account: mismatch, fullAccess, loading,
//                                    noneOnline, firstRun, listFailed, added, machineOffline (UI/Home/HomeLaunch.swift)
//
// A real account (here)
//   -joinSecret <ra1…>               join with an account secret (an account already on the device is
//                                    kept when it is the same one, and replaced otherwise)
//   -joinServer <url> -joinCode <8 digits>   join with a pairing code
//   -resetAccount                    forget the account on this device first
//   -route <route>                   open on a destination once the account is in use
//
// The window (here)
//   -landscape                       the window on its side, which a script cannot do to a simulator (an iPhone's;
//                                    an iPad's window does not take the request)
//
// Joining and adding a device (here)
//   -joinStep <step>                 the join flow in a fixed state: form, filled, error, cancelled,
//                                    working, emoji, registering, secret, secretError, scan, scanDenied, scanMissing
//
// Home and projects, with -demo (UI/Home/HomeLaunch.swift)
//   -homeScope <all|path>            the scope the home page opens on
//   -homeScopeTarget <all|path>      the scope the signal home.scope chooses
//   -homeSheet <scope|browse|pick|pickBrowse>   看哪个项目, 新会话 · 选项目, or either on 新项目 · 选目录
//   -homeFilter <text>               something typed in the title filter
//   -homeExpand                      需要处理 and 进行中 opened in full
//   -projectSheet <main|machine|model|context|mode>   项目设置 on a page
//   -projectDraft <text>             something written in the new-session box
//   -projectStart <running|failed|unknown|expired>   how a start stands
//   -projectEnded                    a project's 已结束 opened
//   -projectsMenu <path> / -projectsRemove <path> / -projectsAdd / -projectsElsewhere
//                                    项目管理's menu, dialog, sheet, notice
//   -projectsTarget <path>           the project the signals projects.* act on
//   -prefsSaving <path> / -prefsFailed <path>   a change to a project's preferences on its way, or failed
//   -dirPath <path> / -dirError      the folder 新项目 · 选目录 opens on, or a listing that fails
//   -settingsEdit <a,b,…>            what the signal settings.apply chooses in 项目设置: full, auto, ask, standard,
//                                    effort=<e>, model=<slug>
//
// Sessions (UI/Session/SessionSamples.swift)
//   -demo -demoRoute session/<id>    the sample sessions: sample-rows, sample-queue, sample-send, sample-start,
//                                    sample-claude, sample-old, sample-long, sample-wait, sample-many, sample-ask,
//                                    sample-ended; and the sets' own (run, ask, …)
//   -sessionShow <what>              a session with something showing: menu, mode, confirmFull, model, rename,
//                                    project, stop, dock, reason
//   -sessionPerf                     print what each pass over the events cost, and the frames while session.stream plays
//   defaults write <bundle> debug.sessionText <text>   the text the signals that type use
//
// Settings, with -demo (UI/Settings/SettingsShared.swift)
//   -settingsState <name>            a page's fixed state. machine/<id>: loading, failed, saveFailed, saveUnknown,
//                                    mismatch; connection: checking, serverDown, machineDown; image-storage: empty,
//                                    clearing; licenses: <a licence's id>; client/<id>, devices: renaming, removing
//   -settingsEnd                     a settings page scrolled to its end
//
// The terminal (UI/Terminal/TerminalDebug.swift)
//   -demo -demoRoute terminal/<session>   the terminal on a sample screen
//   -termState <live|connecting|closed|offline|ended>   the pane in that state
//   -termSize <cols>x<rows>          the sample screen as a window of that size, not fitted
//   -termRead                        the reader over the sample, with sample history
//   -termOpening <reading|failed>    the live sample under the opening line
//
// Images (UI/Images/ImageDebug.swift)
//   -imageAdd <path[,path…]>         once a message box is up: these files through what the picker's results go through
//   -imageDraft <state>              with -demo: the strip in a fixed state: ready, crowded, uploading, failed,
//                                    original, full, notice
//   images.expire                    force the on-screen draft upload deadline (debug signal)
//   -imageOutgoing                   with -demo: a message with images on its way, in the session shown
//   -viewerState <state>             with -demo: the viewer's display copy: loading, ready, notSynced, expired,
//                                    offline, failed, corrupt
//
// The component gallery (UI/Gallery/Gallery.swift)
//   -gallery [page]                  the list of pages, or one (tokens, icons, buttons, notices, lists, choices,
//                                    fields, states, nav, sheet, dialog, input, saving, menu, cover)
//   -galleryEnd                      a page scrolled to its end
//
// Signals, sent with `xcrun simctl spawn <udid> notifyutil -p
// io.github.lewin671.remoteagent.debug.<name>`. Each does what a tap on
// the control it names would, through the same code, and only while its
// screen is showing:
//
//   app.logout          退出账户, from anywhere
//   app.open            goes to the route of `defaults write <bundle> debug.route <route>`, as a tap on a row would
//   name.skip           跳过 of 给这个客户端起个名字, from any page
//   join.cancel         the join flow
//   removed.retry       再试一次 on the 已被移出账户 notice
//   home.scope          chooses the scope -homeScopeTarget names
//   project.start, project.agent                ↑ of the new-session box; the other CLI
//   settings.apply      chooses what -settingsEdit names in 项目设置 and saves
//   projects.pin, projects.remove, projects.remove.confirm   项目管理, on the project -projectsTarget names
//   dir.first, dir.up, dir.choose               选目录: into the first folder, up, 选这个目录
//   session.draft, session.send, session.queue, session.interrupt   the message box (draft types debug.sessionText)
//   session.allow, session.deny                 the docked approval (deny with debug.sessionText as its reason)
//   session.cancel, session.resume              the last waiting message
//   session.menu, session.mode, session.model, session.rename, session.project, session.close
//                       what the ⋯ menu opens; close takes them all down
//   session.mode.pick   chooses the mode debug.sessionText names (ask, auto, plan, full)
//   session.rename.save saves debug.sessionText as the title
//   session.top, session.back, session.end      the chat: to its start, away from its end, back to it
//   session.focus, session.blur                 the keyboard up and down
//   session.select                             选择文字 of the last agent reply
//   session.stream, session.resolve             sample data: a turn plays into the session; its first approval
//                       is answered as by another client
//   client.rename, client.reset  a client's page: a fixed new name, 恢复默认名称
//   machine.timeout             a computer's page: the next approval timeout
//   check.again         再测一次 of 连接检测
//   images.clear        清理 of 图片存储
//   images.add, images.pick, images.original, images.retry, images.remove
//                       a message box's images: the paths of debug.imagePaths, what ＋ does, 原图, 重试, 移除
//   viewer.close, viewer.next, viewer.zoom, viewer.retry, viewer.save, viewer.share   the image viewer
//   term.type, term.ime, term.paste             the terminal: the text of the tmp directory's `term-input`
//   term.enter, term.esc, term.tab, term.ctrlc, term.left, term.up, term.down, term.right, term.keyboard
//                       the key bar's keys
//   term.fit, term.fontUp, term.fontDown, term.reconnect, term.details, term.dump
//                       the toolbar and the notice; dump writes what the pane shows to `term-dump`
//   gallery.save        保存 of the gallery's `saving` dialog
import Foundation
import RACore
import SwiftUI

#if DEBUG
import UIKit
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

    /// Turns the window on its side when `-landscape` asks for it.
    static func orient() {
        guard args.contains("-landscape") else { return }
        for scene in UIApplication.shared.connectedScenes.compactMap({ $0 as? UIWindowScene }) {
            scene.requestGeometryUpdate(.iOS(interfaceOrientations: .landscapeRight))
        }
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
    static func orient() {}
    static func stageChanged(_ model: AppModel) async {}
    static func drive(_ join: JoinModel) {}
}

extension View {
    func debugSignal(_ name: String, _ action: @escaping () -> Void) -> some View { self }
}
#endif
