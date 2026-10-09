import Foundation
import Testing

@testable import RemoteAgent

@MainActor
@Suite struct NavigationTests {
    nonisolated static let routes: [Route] = [
        .home, .settings, .session(id: "abc"), .session(id: "abc", terminal: true),
        .project(key: "/Users/me/Code/foo"), .project(key: "/Users/me/a b/项目?x=1&y", focusNew: true), .newProject,
        .newProjectAt(machine: "m1", path: "/srv/www/site"), .pickProject, .manageProjects,
        .approval(session: "s", approval: "A1"), .plan(session: "s"), .question(session: "s", question: "toolu_01/x"),
        .image(session: "s", seq: 18_446_744_073_709_551_615, index: 2), .devices, .machine(id: "m1"), .client(id: "c1"),
        .addDevice, .quota(machine: "m1"), .notifications, .connectionCheck, .imageStorage, .licenses,
    ]

    @Test(arguments: routes)
    func aRouteReadsBackFromItsText(route: Route) {
        #expect(Route(path: route.path) == route)
    }

    @Test func textThatIsNoRouteIsRefused() {
        for text in ["", "nowhere", "session", "session/", "approval/s", "image/s/x/1", "image/s/1", "home/x", "new-project/m"] {
            #expect(Route(path: text) == nil, "\(text)")
        }
    }

    @Test func firstLevelPagesReplaceTheStackAndOthersArePushed() {
        let nav = Navigator()
        nav.open(.devices)
        nav.open(.machine(id: "m"))
        #expect(nav.path == [.devices, .machine(id: "m")])
        nav.back()
        #expect(nav.path == [.devices])
        nav.open(.settings)
        #expect(nav.path.isEmpty && nav.tab == .settings)
        nav.open(.session(id: "s"))
        nav.open(.home)
        #expect(nav.path.isEmpty && nav.tab == .home)
        nav.back()
        #expect(nav.path.isEmpty)
    }

    @Test func aSessionOnTopIsNotPushedOverItself() {
        let nav = Navigator()
        nav.open(.session(id: "s"))
        nav.open(.session(id: "s", terminal: true))
        #expect(nav.path == [.session(id: "s", terminal: true)])
        nav.open(.session(id: "t"))
        #expect(nav.path.count == 2)
        nav.backToRoot()
        #expect(nav.path.isEmpty)
    }

    // MARK: This client's name

    @Test func theClientsNameIsAskedOnlyOnAFirstLevelPage() {
        func wanted(asked: Bool = false, demo: Bool = false, online: Bool = true, name: String? = "", pushed: Int = 0) -> Bool {
            ClientNameAsk.wanted(asked: asked, demo: demo, online: online, name: name, pushed: pushed)
        }
        #expect(wanted())
        // Never over a pushed page: a session being read, the connection check, a page opened by a route.
        let nav = Navigator()
        nav.open(.session(id: "s"))
        #expect(!wanted(pushed: nav.path.count))
        nav.open(.connectionCheck)
        #expect(!wanted(pushed: nav.path.count))
        // Back on 会话 or 设置 it is asked after all.
        nav.open(.settings)
        #expect(wanted(pushed: nav.path.count))
        // Once only, with a name already there never, and not before the device list has this client.
        #expect(!wanted(asked: true))
        #expect(!wanted(name: "我的手机"))
        #expect(!wanted(name: nil))
        #expect(!wanted(online: false))
        #expect(!wanted(demo: true))
    }

    @Test func whetherTheNameWasAskedIsKeptPerClientAndGoesWithTheAccount() {
        let d = UserDefaults(suiteName: "client-name-\(UUID().uuidString)")!
        d.set(true, forKey: ClientNameAsk.key("c1"))
        #expect(d.bool(forKey: ClientNameAsk.key("c1")) && !d.bool(forKey: ClientNameAsk.key("c2")))
        AccountDefaults.clear(d)
        #expect(!d.bool(forKey: ClientNameAsk.key("c1")))
    }
}
