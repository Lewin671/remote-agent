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
}
