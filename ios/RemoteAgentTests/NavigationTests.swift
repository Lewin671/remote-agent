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
        .quota(machine: "m1"), .notifications, .connectionCheck, .imageStorage, .licenses,
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

    @Test func aCommandThatEndsNavigatesOnlyFromThePageThatSentIt() {
        let nav = Navigator()
        nav.open(.devices)
        nav.open(.client(id: "c"))
        // Still on its page: one step back.
        nav.back(from: .client(id: "c"))
        #expect(nav.path == [.devices])
        // Left meanwhile, and another page opened: that one stays.
        nav.open(.machine(id: "m"))
        nav.back(from: .client(id: "c"))
        #expect(nav.path == [.devices, .machine(id: "m")])
        // Covered by a page opened from it: the page over it stays too.
        nav.open(.quota(machine: "m"))
        nav.back(from: .machine(id: "m"))
        #expect(nav.path == [.devices, .machine(id: "m"), .quota(machine: "m")])
        // Going on is held to the same: a session started from a page left is not opened over another.
        nav.open(.session(id: "new"), from: .machine(id: "m"))
        #expect(nav.path.count == 3)
        nav.open(.session(id: "new"), from: .quota(machine: "m"))
        #expect(nav.path.last == .session(id: "new"))
        // A first-level page is the one showing when nothing is pushed.
        nav.open(.settings)
        nav.open(.home, from: .project(key: "/p"))
        #expect(nav.tab == .settings)
        nav.open(.session(id: "s"), from: nil)
        #expect(nav.path == [.session(id: "s")])
        nav.back(from: nil)
        #expect(nav.path == [.session(id: "s")])
    }

    @Test func aRemovalThatEndsAfterItsPageWasLeftDoesNotTakeAnotherPageAway() async {
        let nav = Navigator()
        nav.open(.devices)
        nav.open(.client(id: "c"))
        let remove = ActionRunner()
        let (answers, answer) = AsyncStream.makeStream(of: Void.self)
        remove.run({ for await _ in answers { return } }, onDone: { nav.back(from: .client(id: "c")) })
        // The answer is slow; the user goes back and opens the computer's page.
        nav.back()
        nav.open(.machine(id: "m"))
        answer.yield()
        await eventually("ended") { !remove.running }
        #expect(nav.path == [.devices, .machine(id: "m")])
    }
}
