// Moving between screens: every destination the app has as a value
// (`Route`), one stack of them (`Navigator`), and the view that draws the
// stack (`MainView`). The destinations are Android's (MainActivity.kt's
// NavHost and ui/Common.kt's `Nav`), plus the pages Android opens as sheets
// or inner pages of 设置.
//
// One stack on every size of screen. The two first-level pages, 会话 and
// 设置, are the stack's root with the tab bar under them; anything pushed
// covers the bar, as on Android. An iPad gets the same stack: the mockup's
// wide layout (a sidebar of sessions beside the session) is a different
// set of screens, which Android has not taken on either (docs/plan.md
// 「待确认」), and until it is decided the design asks only that settings-like
// pages keep a centred column (design-system §6), which `Page(gray:)` does.
import Observation
import RACore
import SwiftUI
import UIKit

/// A destination and what it needs to be drawn.
enum Route: Hashable, Sendable {
    /// 会话: the timeline of sessions. A first-level page.
    case home
    /// 设置. A first-level page.
    case settings
    /// A session; `terminal` opens it on its terminal.
    case session(id: String, terminal: Bool = false)
    /// A project's page, which is where a session is started in it; `focusNew` brings the keyboard up in the new-session box.
    case project(key: String, focusNew: Bool = false)
    /// Choosing a folder on a computer for a new project.
    case newProject
    /// A folder chosen for a new project, to start its first session in.
    case newProjectAt(machine: String, path: String)
    /// Choosing which project a new session goes to.
    case pickProject
    /// 管理项目: pinned and hidden projects.
    case manageProjects
    /// An approval on a page of its own, as opened from a notification.
    case approval(session: String, approval: String)
    /// A proposed plan on a page of its own.
    case plan(session: String)
    /// A question on a page of its own.
    case question(session: String, question: String)
    /// The image viewer at an event's image.
    case image(session: String, seq: UInt64, index: Int)
    /// 设备: computers and clients.
    case devices
    /// A computer's details and settings.
    case machine(id: String)
    /// A client's details.
    case client(id: String)
    /// 添加设备: showing a code for a new device to join with.
    case addDevice
    /// A computer's account quotas.
    case quota(machine: String)
    /// 本客户端的通知.
    case notifications
    /// 连接检测.
    case connectionCheck
    /// 图片存储 · 这台手机.
    case imageStorage
    /// 开源许可.
    case licenses

    /// The route as text, for `-demoRoute` and links: `session/<id>`,
    /// `project/<path>`, `machine/<id>`, `devices`. A project's key is a
    /// path and comes last, slashes and all.
    var path: String {
        switch self {
        case .home: "home"
        case .settings: "settings"
        case .session(let id, let terminal): (terminal ? "terminal/" : "session/") + id
        case .project(let key, let focus): (focus ? "project-new/" : "project/") + key
        case .newProject: "new-project"
        case .newProjectAt(let machine, let path): "new-project/\(machine)/\(path)"
        case .pickProject: "pick-project"
        case .manageProjects: "projects"
        case .approval(let sid, let aid): "approval/\(sid)/\(aid)"
        case .plan(let sid): "plan/\(sid)"
        case .question(let sid, let qid): "question/\(sid)/\(qid)"
        case .image(let sid, let seq, let index): "image/\(sid)/\(seq)/\(index)"
        case .devices: "devices"
        case .machine(let id): "machine/\(id)"
        case .client(let id): "client/\(id)"
        case .addDevice: "add-device"
        case .quota(let machine): "quota/\(machine)"
        case .notifications: "notifications"
        case .connectionCheck: "connection"
        case .imageStorage: "image-storage"
        case .licenses: "licenses"
        }
    }

    /// Reads what `path` writes; nil for anything else.
    init?(path: String) {
        let parts = path.split(separator: "/", maxSplits: 1, omittingEmptySubsequences: false).map(String.init)
        let rest = parts.count > 1 ? parts[1] : ""
        /// The first segment of the rest and what follows it.
        func two() -> (String, String)? {
            let p = rest.split(separator: "/", maxSplits: 1, omittingEmptySubsequences: false).map(String.init)
            return p.count == 2 && !p[0].isEmpty && !p[1].isEmpty ? (p[0], p[1]) : nil
        }
        switch (parts[0], rest.isEmpty) {
        case ("home", true): self = .home
        case ("settings", true): self = .settings
        case ("session", false): self = .session(id: rest)
        case ("terminal", false): self = .session(id: rest, terminal: true)
        case ("project", false): self = .project(key: rest)
        case ("project-new", false): self = .project(key: rest, focusNew: true)
        case ("new-project", true): self = .newProject
        case ("new-project", false):
            guard let (machine, path) = two() else { return nil }
            self = .newProjectAt(machine: machine, path: path)
        case ("pick-project", true): self = .pickProject
        case ("projects", true): self = .manageProjects
        case ("approval", false):
            guard let (sid, aid) = two() else { return nil }
            self = .approval(session: sid, approval: aid)
        case ("plan", false): self = .plan(session: rest)
        case ("question", false):
            guard let (sid, qid) = two() else { return nil }
            self = .question(session: sid, question: qid)
        case ("image", false):
            let p = rest.split(separator: "/").map(String.init)
            guard p.count == 3, let seq = UInt64(p[1]), let index = Int(p[2]) else { return nil }
            self = .image(session: p[0], seq: seq, index: index)
        case ("devices", true): self = .devices
        case ("machine", false): self = .machine(id: rest)
        case ("client", false): self = .client(id: rest)
        case ("add-device", true): self = .addDevice
        case ("quota", false): self = .quota(machine: rest)
        case ("notifications", true): self = .notifications
        case ("connection", true): self = .connectionCheck
        case ("image-storage", true): self = .imageStorage
        case ("licenses", true): self = .licenses
        default: return nil
        }
    }
}

/// The two first-level pages.
enum MainTab: String, Sendable {
    case home, settings
}

/// Where the app is: a first-level page and what is pushed over it. Screens
/// take it from the environment and call `open`; nothing else changes the stack.
@MainActor
@Observable
final class Navigator {
    var tab = MainTab.home
    var path: [Route] = []

    /// Goes to a destination from anywhere: a row, a notification, a link.
    /// The first-level pages replace the stack; a session already on top is
    /// not pushed over itself.
    func open(_ route: Route) {
        switch route {
        case .home:
            path.removeAll()
            tab = .home
        case .settings:
            path.removeAll()
            tab = .settings
        case .session(let id, _):
            // The same session again, as from a second notification: the page on top is replaced, not stacked.
            if case .session(let top, _)? = path.last, top == id {
                path[path.count - 1] = route
            } else {
                path.append(route)
            }
        default:
            path.append(route)
        }
    }

    /// One step back; at a first-level page nothing.
    func back() {
        if !path.isEmpty { path.removeLast() }
    }

    /// Back to the first-level page under the stack.
    func backToRoot() {
        path.removeAll()
    }
}

extension Route {
    /// The screen of a pushed destination. Each is in a file of its own under UI/<Area>/.
    @MainActor @ViewBuilder var destination: some View {
        switch self {
        case .home: HomeScreen()
        case .settings: SettingsScreen()
        case .session(let id, let terminal): SessionScreen(id: id, terminal: terminal)
        case .project(let key, let focusNew): ProjectScreen(key: key, focusNew: focusNew)
        case .newProject: NewProjectScreen()
        case .newProjectAt(let machine, let path): NewProjectAtScreen(machine: machine, path: path)
        case .pickProject: PickProjectScreen()
        case .manageProjects: ManageProjectsScreen()
        case .approval(let sid, let aid): ApprovalScreen(session: sid, approval: aid)
        case .plan(let sid): PlanScreen(session: sid)
        case .question(let sid, let qid): QuestionScreen(session: sid, question: qid)
        case .image(let sid, let seq, let index): ImageViewerScreen(session: sid, seq: seq, index: index)
        case .devices: DevicesScreen()
        case .machine(let id): MachineScreen(id: id)
        case .client(let id): ClientScreen(id: id)
        case .addDevice: AddDeviceScreen()
        case .quota(let machine): QuotaScreen(machine: machine)
        case .notifications: NotificationsScreen()
        case .connectionCheck: ConnectionCheckScreen()
        case .imageStorage: ImageStorageScreen()
        case .licenses: LicensesScreen()
        }
    }
}

/// The app once an account is in use: the stack, with the account's context and the navigator in the environment.
struct MainView: View {
    let context: AccountContext
    @State private var nav: Navigator

    init(context: AccountContext, start: Route? = nil) {
        self.context = context
        let nav = Navigator()
        if let start { nav.open(start) }
        _nav = State(initialValue: nav)
    }

    var body: some View {
        @Bindable var nav = nav
        NavigationStack(path: $nav.path) {
            FirstLevel()
                .navigationDestination(for: Route.self) { $0.destination }
        }
        .tint(Theme.blue)
        .environment(nav)
        .environment(context)
    }
}

/// 会话 or 设置, over the tab bar. Their titles are the pages' own large titles, so the system's bar is hidden here.
private struct FirstLevel: View {
    @Environment(AppModel.self) private var model
    @Environment(Navigator.self) private var nav
    @Environment(AccountContext.self) private var context

    var body: some View {
        @Bindable var nav = nav
        let state = model.state
        // What the user is waiting to be asked about, on the 会话 tab.
        let waiting = state.sessions.values.reduce(0) { $0 + (needsAttention($1, machine: state.machines[$1.machineId]) ? 1 : 0) }
        Group {
            switch nav.tab {
            case .home: HomeScreen()
            case .settings: SettingsScreen()
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .safeAreaInset(edge: .bottom, spacing: 0) {
            TabBar(
                [
                    TabBarItem(id: MainTab.home.rawValue, label: "会话", icon: Lucide.messageSquare, badge: waiting),
                    TabBarItem(id: MainTab.settings.rawValue, label: "设置", icon: Lucide.settings),
                ],
                selected: Binding(get: { nav.tab.rawValue }, set: { nav.tab = MainTab(rawValue: $0) ?? .home }))
        }
        // The bar stays at the bottom when a dialog over the page brings the keyboard up.
        .ignoresSafeArea(.keyboard, edges: .bottom)
        .toolbar(.hidden, for: .navigationBar)
        .modifier(ClientNamePrompt())
    }
}

/// Asks once for this client's name, after joining and when it has none
/// yet, on the first-level pages rather than over a session being read
/// (MainActivity.kt). 跳过 counts as asked.
private struct ClientNamePrompt: ViewModifier {
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @State private var asked = false
    private let defaults = UserDefaults.standard

    private var key: String { AccountDefaults.key("client-name-asked." + context.me) }

    func body(content: Content) -> some View {
        let me = model.state.devices[context.me]
        let wanted = !asked && !model.isDemo && !defaults.bool(forKey: key) && model.state.conn == .online
            && me != nil && (me?.meta?.name ?? "").isEmpty
        content.inputAlert(
            isPresented: Binding(get: { wanted }, set: { shown in
                if !shown {
                    defaults.set(true, forKey: key)
                    asked = true
                }
            }),
            title: "给这个客户端起个名字", initial: String(UIDevice.current.name.prefix(40)), placeholder: "如：我的手机",
            confirm: "保存", dismiss: "跳过", text: "其他客户端的设备列表里靠它认出这一个，以后可以在「设置 › 设备」里改"
        ) { name in
            let name = name.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !name.isEmpty else { return }
            let remote = context.remote
            let id = context.me
            Task { try? await remote.renameDevice(id, name: name) }
        }
        .debugSignal("name.skip") {
            defaults.set(true, forKey: key)
            asked = true
        }
    }
}

/// What a destination shows until its screen is written: its name and what
/// it was opened with, on the surface the screen will have. Replace a
/// screen file's body; this stays for the ones not written yet.
struct ScreenPlaceholder: View {
    private let title: String
    private let detail: [String]
    private let gray: Bool

    init(_ title: String, detail: [String] = [], gray: Bool = false) {
        self.title = title
        self.detail = detail
        self.gray = gray
    }

    var body: some View {
        Page(gray: gray) {
            VStack(spacing: 0) {
                ConnectionBar()
                VStack(spacing: 0) {
                    Spacer(minLength: 24)
                    Text(title).type(T.h17, Theme.fg).multilineTextAlignment(.center).accessibilityAddTraits(.isHeader)
                    Text("这个页面还没有做").type(T.sm, Theme.muted).padding(.top, 8)
                    ForEach(detail, id: \.self) { line in
                        Text(line).type(T.mono, Theme.muted).multilineTextAlignment(.center).lineLimit(3).padding(.top, 6)
                    }
                    Spacer(minLength: 24)
                }
                .padding(.horizontal, 24)
            }
        }
    }
}
