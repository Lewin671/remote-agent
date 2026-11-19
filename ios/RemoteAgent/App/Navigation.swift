// Moving between screens: every destination the app has as a value
// (`Route`), one stack of them (`Navigator`), and the view that draws the
// stack (`MainView`). The destinations are Android's (MainActivity.kt's
// NavHost and ui/Common.kt's `Nav`), plus the pages Android opens as sheets
// or inner pages of 设置.
//
// On a phone one stack: the two first-level pages, 会话 and 定时, are its
// root with the bottom bar over them (UI/Shell); anything pushed covers the
// bar. 设置 is pushed, from the gear on the home page (client-v58). A wide
// window (an iPad, design-system §6 「宽屏」) splits: the first-level page
// is a sidebar (UI/Shell/Sidebar.swift) and the stack is the main area
// beside it, which what the sidebar chooses replaces; settings-like pages
// keep a centred column there, which `Page(gray:)` does.
import Observation
import RACore
import RAProto
import SwiftUI
import UIKit

/// A destination and what it needs to be drawn.
enum Route: Hashable, Sendable {
    /// 会话: the timeline of sessions. A first-level page.
    case home
    case schedules
    case schedule(machine: String, id: String)
    case newSchedule(session: String = "")
    /// 设置, from the home page's gear.
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
    /// 管理项目 filtered to a specific computer.
    case machineProjects(id: String)
    /// An approval on a page of its own, as opened from a notification.
    case approval(session: String, approval: String)
    /// A proposed plan on a page of its own.
    case plan(session: String)
    /// A question on a page of its own.
    case question(session: String, question: String)
    /// The image viewer at an event's image.
    case image(session: String, seq: UInt64, index: Int)
    /// 客户端: paired clients and unfinished pairings.
    case devices
    case addDevice(replacing: String = "")
    case accountSecurity, recoveryCode, handoff, rePair
    /// A computer's details and settings.
    case machine(id: String, initialDirect: Bool = false, initialCliAgent: String? = nil)
    /// A computer's attachment storage management screen.
    case machineStorage(id: String)
    /// A computer's usage & speed screen.
    case usage(machine: String, agent: PBAgent = .unspecified, model: String = "")
    /// 电脑状态: a computer's CPU, memory, disk and what uses them.
    case machineStatus(id: String)
    /// A client's details.
    case client(id: String)
    /// A computer's account quotas.
    case quota(machine: String)
    /// 本客户端的通知.
    case notifications
    /// 连接检测.
    case connectionCheck
    /// 附件存储 · 这台手机.
    case imageStorage
    /// 开源许可.
    case licenses

    /// The route as text, for `-demoRoute` and links: `session/<id>`,
    /// `project/<path>`, `machine/<id>`, `devices`. A project's key is a
    /// path and comes last, slashes and all.
    var path: String {
        switch self {
        case .schedules: "schedules"
        case .schedule(let machine, let id): "schedule/\(machine)/\(id)"
        case .newSchedule(let session): session.isEmpty ? "new-schedule" : "new-schedule/" + session
        case .home: "home"
        case .settings: "settings"
        case .session(let id, let terminal): (terminal ? "terminal/" : "session/") + id
        case .project(let key, let focus): (focus ? "project-new/" : "project/") + key
        case .newProject: "new-project"
        case .newProjectAt(let machine, let path): "new-project/\(machine)/\(path)"
        case .pickProject: "pick-project"
        case .manageProjects: "projects"
        case .machineProjects(let id): "projects/\(id)"
        case .approval(let sid, let aid): "approval/\(sid)/\(aid)"
        case .plan(let sid): "plan/\(sid)"
        case .question(let sid, let qid): "question/\(sid)/\(qid)"
        case .image(let sid, let seq, let index): "image/\(sid)/\(seq)/\(index)"
        case .accountSecurity: "account-security"
        case .recoveryCode: "recovery-code"
        case .handoff: "handoff"
        case .rePair: "re-pair"
        case .devices: "devices"
        case .addDevice(let id): id.isEmpty ? "add-device" : "add-device/" + id
        case .machine(let id, let initialDirect, let initialCliAgent):
            if initialDirect {
                "machine-direct/\(id)"
            } else if let agent = initialCliAgent, !agent.isEmpty {
                "machine-cli/\(id)/\(agent)"
            } else {
                "machine/\(id)"
            }
        case .machineStorage(let id): "machine-storage/\(id)"
        case .usage(let machine, let agent, let model):
            if agent == .unspecified && model.isEmpty {
                "usage/\(machine)"
            } else {
                "usage/\(machine)/\(agent.rawValue)/\(model)"
            }
        case .machineStatus(let id): "machine-status/\(id)"
        case .client(let id): "client/\(id)"
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
        case ("schedules", true): self = .schedules
        case ("schedule", false):
            guard let (machine, id) = two() else { return nil }
            self = .schedule(machine: machine, id: id)
        case ("new-schedule", _): self = .newSchedule(session: rest)
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
        case ("projects", false): self = .machineProjects(id: rest)
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
        case ("account-security", true): self = .accountSecurity
        case ("recovery-code", true): self = .recoveryCode
        case ("handoff", true): self = .handoff
        case ("re-pair", true): self = .rePair
        case ("devices", true): self = .devices
        case ("add-device", _): self = .addDevice(replacing: rest)
        case ("machine", false): self = .machine(id: rest)
        case ("machine-direct", false): self = .machine(id: rest, initialDirect: true)
        case ("machine-cli", false):
            guard let (id, agent) = two() else { return nil }
            self = .machine(id: id, initialCliAgent: agent)
        case ("machine-storage", false): self = .machineStorage(id: rest)
        case ("usage", false):
            let segs = rest.split(separator: "/", maxSplits: 2, omittingEmptySubsequences: false).map(String.init)
            guard let machine = segs.first, !machine.isEmpty else { return nil }
            if segs.count == 1 {
                self = .usage(machine: machine)
            } else if segs.count >= 2, let raw = Int(segs[1]), let agent = PBAgent(rawValue: raw) {
                let model = segs.count >= 3 ? segs[2] : ""
                self = .usage(machine: machine, agent: agent, model: model)
            } else {
                return nil
            }
        case ("machine-status", false): self = .machineStatus(id: rest)
        case ("client", false): self = .client(id: rest)
        case ("quota", false): self = .quota(machine: rest)
        case ("notifications", true): self = .notifications
        case ("connection", true): self = .connectionCheck
        case ("image-storage", true): self = .imageStorage
        case ("licenses", true): self = .licenses
        default: return nil
        }
    }
}

/// The two first-level pages, the tabs of the bottom bar.
enum MainTab: String, Sendable {
    case home, schedules
}

/// Where the app is: a first-level page and what is pushed over it. Screens
/// take it from the environment and call `open`; nothing else changes the stack.
@MainActor
@Observable
final class Navigator {
    var tab = MainTab.home
    var path: [Route] = []
    /// The window is wide: the first-level page is a sidebar, and what it opens replaces the main area.
    var wide = false

    /// Goes to a destination from anywhere: a row, a notification, a link.
    /// The first-level pages replace the stack; a session already on top is
    /// not pushed over itself.
    func open(_ route: Route) {
        switch route {
        case .schedules:
            path.removeAll()
            tab = .schedules
        case .home:
            path.removeAll()
            tab = .home
        case .schedule, .newSchedule:
            if path.isEmpty { tab = .schedules }
            if wide { path = [route] } else if path.last != route { path.append(route) }
        case .session where wide, .settings where wide:
            // Chosen in the sidebar: the main area shows it, in place of what it showed.
            path = [route]
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

    /// Whether `page` is what the user is looking at: the destination on
    /// top of the stack, or for nil a first-level page.
    func showing(_ page: Route?) -> Bool {
        path.last == page
    }

    /// One step back for `page`, when a command it sent has ended. The
    /// command outlives its page, so the user may have left it or opened
    /// another over it meanwhile: the stack is theirs then, and stays as it is.
    func back(from page: Route?) {
        if showing(page) { back() }
    }

    /// Goes on to `route` from `page` when a command it sent has ended; nothing once the user has moved on.
    func open(_ route: Route, from page: Route?) {
        if showing(page) { open(route) }
    }
}

extension EnvironmentValues {
    /// The destination a view is drawn for (`Navigator.back(from:)`); nil on a first-level page.
    @Entry var page: Route?
}

extension Route {
    /// The screen of a pushed destination. Each is in a file of its own under UI/<Area>/.
    @MainActor @ViewBuilder var destination: some View {
        switch self {
        case .schedules: SchedulesScreen()
        case .schedule(let machine, let id): ScheduleDetailScreen(machine: machine, id: id)
        case .newSchedule(let session): NewScheduleScreen(sessionID: session)
        case .home: HomeScreen()
        case .settings: SettingsScreen()
        case .session(let id, let terminal): SessionScreen(id: id, terminal: terminal)
        case .project(let key, let focusNew): ProjectScreen(key: key, focusNew: focusNew)
        case .newProject: NewProjectScreen()
        case .newProjectAt(let machine, let path): NewProjectAtScreen(machine: machine, path: path)
        case .pickProject: PickProjectScreen()
        case .manageProjects: ManageProjectsScreen()
        case .machineProjects(let id): ManageProjectsScreen(initialMachineId: id)
        case .approval(let sid, let aid): ApprovalScreen(session: sid, approval: aid)
        case .plan(let sid): PlanScreen(session: sid)
        case .question(let sid, let qid): QuestionScreen(session: sid, question: qid)
        case .image(let sid, let seq, let index): ImageViewerScreen(session: sid, seq: seq, index: index)
        case .accountSecurity: AccountSecurityScreen()
        case .recoveryCode: RecoveryCodeScreen()
        case .handoff: HandoffScreen()
        case .rePair: RePairScreen()
        case .devices: DevicesScreen()
        case .addDevice(let id): AddDeviceScreen(replacing: id)
        case .machine(let id, let initialDirect, let initialCliAgent):
            MachineScreen(id: id, initialDirect: initialDirect, initialCliAgent: initialCliAgent)
        case .machineStorage(let id): MachineStorageScreen(id: id)
        case .usage(let machine, let agent, let model): UsageScreen(machineID: machine, focusAgent: agent, focusModel: model)
        case .machineStatus(let id): MachineStatusScreen(id: id)
        case .client(let id): ClientScreen(id: id)
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
    @Environment(AppModel.self) private var model
    @State private var nav: Navigator

    init(context: AccountContext, start: Route? = nil) {
        self.context = context
        let nav = Navigator()
        if let start { nav.open(start) }
        _nav = State(initialValue: nav)
    }

    @Environment(\.horizontalSizeClass) private var width

    var body: some View {
        @Bindable var nav = nav
        Group {
            if width == .regular {
                // Wide (design-system §6 「宽屏」): the first-level page as a sidebar on the grey window, what it opens beside it.
                NavigationSplitView {
                    FirstLevel(sidebar: true)
                        .navigationSplitViewColumnWidth(min: 320, ideal: 360, max: 420)
                } detail: {
                    NavigationStack(path: $nav.path) {
                        WideEmpty(tab: nav.tab)
                            .navigationDestination(for: Route.self) { route in
                                // What the sidebar chose has no way back: the sidebar is beside it.
                                route.destination.environment(\.page, route)
                                    .navigationBarBackButtonHidden(nav.path.first == route)
                            }
                    }
                }
                .navigationSplitViewStyle(.balanced)
                .background(DS.gbg.ignoresSafeArea())
            } else {
                NavigationStack(path: $nav.path) {
                    FirstLevel()
                        .navigationDestination(for: Route.self) { $0.destination.environment(\.page, $0) }
                }
            }
        }
        .onChange(of: width, initial: true) { _, w in nav.wide = w == .regular }
        .tint(DS.blue)
        // Goes where a tap would, for a script: to the route written to `debug.route` (debug builds).
        .debugSignal("app.open") {
            if let route = Route(path: UserDefaults.standard.string(forKey: "debug.route") ?? "") { nav.open(route) }
        }
        .debugSignal("app.back") { nav.back() }
        .task {
            await context.coreHost.setNavigateHandler { effect in
                switch effect.route {
                case .session:
                    context.dismissNewTask?()
                    nav.open(.session(id: effect.sessionID))
                case .project:
                    nav.open(.project(key: effect.projectKey))
                case .machineSettings:
                    nav.open(.machine(id: effect.machineID))
                case .machineStatus:
                    nav.open(.machineStatus(id: effect.machineID))
                case .unspecified, .UNRECOGNIZED:
                    break
                }
            }
            await context.coreHost.setScheduleNavigateHandler { effect in
                switch effect.route {
                case .list:
                    context.dismissNewSchedule?()
                    if case .schedule? = nav.path.last {
                        nav.back()
                    } else {
                        nav.open(.schedules)
                    }
                case .detail:
                    context.dismissNewSchedule?()
                    if case .newSchedule? = nav.path.last {
                        nav.back()
                    }
                    nav.open(.schedule(machine: effect.machineID, id: effect.taskID))
                case .session:
                    if effect.firstSeq > 0 {
                        context.sessionStore.jumpToScheduledRun(effect.sessionID, runID: effect.runID, seq: effect.firstSeq)
                    }
                    nav.open(.session(id: effect.sessionID))
                case .closeForm:
                    context.dismissNewSchedule?()
                    if case .newSchedule? = nav.path.last {
                        nav.back()
                    }
                case .formTiming, .unspecified, .UNRECOGNIZED:
                    break
                }
            }
        }
        .onChange(of: nav.path.last, initial: true) { _, page in
            let viewingOther = switch page {
            case .session, .schedule: false
            default: true
            }
            if viewingOther {
                Task { await context.coreHost.openScheduleList(true) }
            }
        }
        .environment(nav)
        .environment(context)
    }
}

/// The project 新任务 opens in; `machine` when it is a folder just chosen there.
struct NewTaskTarget: Identifiable {
    let key: String
    var machine: String? = nil
    var id: String { key }
}

/// 会话 or 定时, under the bottom bar. Their titles are the pages' own
/// sentences, and their buttons glass floating over them, so the system's
/// bar is hidden here; a pushed page has it back.
private struct FirstLevel: View {
    /// Drawn as the wide window's sidebar: 新任务 at its top, 会话 / 定时 and the gear at its bottom.
    var sidebar = false
    @Environment(AppModel.self) private var model
    @Environment(Navigator.self) private var nav
    @Environment(AccountContext.self) private var context
    @State private var pick = ["pick", "pickBrowse"].contains(HomeLaunch.value("-homeSheet"))
    @State private var pickBrowsing = HomeLaunch.value("-homeSheet") == "pickBrowse"
    @State private var newSchedule = HomeLaunch.value("-homeSheet") == "newSchedule"
    /// The project 新任务 is open in, and the one chosen in the list of projects, opened once that list has gone.
    @State private var newTask: NewTaskTarget? = HomeLaunch.value("-newTask").map { NewTaskTarget(key: $0) }
    @State private var chosen: NewTaskTarget?

    var body: some View {
        @Bindable var nav = nav
        let state = model.state
        let home = context.core.home
        // What waits for the user, on the 会话 tab; it stays while the server is out of reach.
        let waiting = Int(home.waitingTotal)
        let schedules = context.core.schedules
        let canCreate = nav.tab == .home ? home.canStart : schedules.canCreate
        Group {
            switch nav.tab {
            case .home: HomeScreen()
            case .schedules: SchedulesScreen()
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        // Nothing can be started out of reach of the server, nor without a computer to run it.
        .environment(\.sidebarCreate, sidebar ? SidebarCreate(enabled: canCreate) { create(home) } : nil)
        .floatingBar(.bottom) {
            if sidebar {
                SidebarFooter(tab: $nav.tab)
            } else {
                BottomBar(
                    tab: $nav.tab,
                    badges: [
                        .home: TabBadge(count: waiting),
                        .schedules: TabBadge(count: Int(schedules.attentionCount), dot: schedules.hasNewResults_p, unit: .tasks),
                    ],
                    canCreate: canCreate,
                    onCreate: { create(home) })
            }
        }
        // The bar stays at the bottom when a dialog over the page brings the keyboard up.
        .ignoresSafeArea(.keyboard, edges: .bottom)
        .toolbar(.hidden, for: .navigationBar)
        .kitSheet(isPresented: $pick, size: pickBrowsing || home.sidebarProjects.canSearch ? .large : .fit, onDismiss: {
            pickBrowsing = false
            if let chosen { newTask = chosen }
            chosen = nil
        }) {
            PickProjectSheet(state: state, browsing: $pickBrowsing, onClose: { pick = false }) { target in
                chosen = target
                pick = false
            }
        }
        .sheet(item: $newTask) { target in
            NavigationStack {
                NewTaskScreen(key: target.key, machine: target.machine, onClose: { newTask = nil }) { sid in
                    newTask = nil
                    nav.open(.session(id: sid))
                }
            }
            .onAppear { context.dismissNewTask = { newTask = nil } }
            .onDisappear { context.dismissNewTask = nil }
            .sheetSurface()
            .presentationDetents([.large])
        }
        .sheet(isPresented: $newSchedule) {
            NavigationStack {
                NewScheduleScreen(sessionID: "", onClose: { newSchedule = false })
            }
            .onAppear { context.dismissNewSchedule = { newSchedule = false } }
            .onDisappear { context.dismissNewSchedule = nil }
            .sheetSurface()
            .presentationDetents([.large])
        }
    }

    /// 新定时 opens its page. 新任务 starts in the scope's project; under every project it asks which first.
    private func create(_ home: PBCoreHomeViewModel) {
        switch nav.tab {
        case .schedules:
            newSchedule = true
        case .home:
            if !home.scopeKey.isEmpty {
                newTask = NewTaskTarget(key: home.scopeKey)
            } else {
                pick = true
            }
        }
    }
}
