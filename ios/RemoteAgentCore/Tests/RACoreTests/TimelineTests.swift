import Foundation
import RAProto
import SwiftProtobuf
import Testing

@testable import RACore

/// Android's core TimelineTest: the home page's scope and timeline (client-v27, architecture §8 「主页的范围」).
@Suite struct TimelineTests {
    private let zone = Mk.shanghai
    private func at(_ y: Int, _ mo: Int, _ d: Int, _ h: Int = 12, _ mi: Int = 0) -> Int64 { Mk.at(y, mo, d, h, mi, zone: Mk.shanghai) }
    // A Sunday.
    private var now: Int64 { at(2026, 10, 4, 9, 41) }

    private func ts(_ ms: Int64) -> Google_Protobuf_Timestamp { .with { $0.seconds = ms / 1000 } }

    private func session(
        _ id: String, _ cwd: String, title: String? = nil, created: Int64? = nil, ended: Int64? = nil, machine: String = "m",
        agent: PBAgent = .codex, approvals: Int32 = 0, forkedFrom: String = "", listed: Bool = false
    ) -> SessionView {
        let created = created ?? now - 3600_000
        return Mk.session(id, machine: machine, updatedMs: ended ?? created, listedEndedMs: listed ? ended ?? 0 : 0) {
            $0.cwd = cwd
            $0.title = title ?? id
            $0.agent = agent
            $0.createdAt = ts(created)
            $0.pendingApprovals = approvals
            $0.forkedFrom = forkedFrom
            $0.status = ended != nil ? .exited : .idle
            if let ended { $0.endedAt = ts(ended) }
        }
    }

    private func state(_ sessions: SessionView..., hidden: [String] = [], pinned: [String] = [], machines: [String] = ["m"]) -> RemoteState {
        Mk.state(
            sessions: sessions, machines: machines.map { MachineView(id: $0, online: true, lastSeenMs: 0) },
            prefs: .with {
                $0.pinned = pinned.map { p in .with { $0.path = p } }
                $0.hidden = hidden.map { p in .with { $0.path = p } }
            })
    }

    @Test func taskOwnedApprovalsStillNeedAttention() {
        let owned = Mk.session("task") { $0.cwd = "/scheduled"; $0.taskOwned = true; $0.pendingApprovals = 1; $0.status = .idle }
        let timeline = homeTimeline(state(owned), scope: .all)
        #expect(timeline.attention.map(\.id) == ["task"])
        #expect(projects(state(owned)).first?.sessions.map(\.id) == ["task"])
        #expect(timeline.running.isEmpty && timeline.ended.isEmpty)
        #expect(timeline.meta.project)
    }

    @Test func allProjectCountsExcludeQuietTasksButIncludeApprovals() {
        let quiet = Mk.session("quiet") { $0.cwd = "/task"; $0.taskOwned = true }
        let approval = Mk.session("approval") { $0.cwd = "/task"; $0.taskOwned = true; $0.pendingApprovals = 1; $0.status = .idle }
        let snapshot = state(session("a", "/a"), session("b", "/b"), quiet, approval)
        #expect(sessionCounts(snapshot, snapshot.sessions.values) == "1 个待处理 · 2 个进行中")
        #expect(scopeRows(snapshot, current: .all).first?.detail == "1 个待处理 · 2 个进行中")
        #expect(homeTimeline(snapshot, scope: .all).attention.map(\.id) == ["approval"])
        #expect(Set(homeTimeline(snapshot, scope: .all).running.map(\.id)) == ["a", "b"])
    }

    @Test func taskOwnedSessionsRemainAddressableButDoNotEnterLists() {
        let owned = Mk.session("task") { $0.cwd = "/scheduled"; $0.taskOwned = true; $0.scheduledTaskID = "task-id" }
        let snapshot = state(owned)
        #expect(snapshot.sessions["task"] != nil)
        #expect(homeTimeline(snapshot, scope: .all).size == 0)
        #expect(projects(snapshot).isEmpty)
    }

    @Test func theOnlyListedProjectIsTheDefaultScopeElseEveryProject() {
        #expect(defaultScope(state()) == .all)
        #expect(defaultScope(state(session("1", "/a"))) == .of(key: "/a"))
        #expect(defaultScope(state(session("1", "/a"), session("2", "/b"))) == .all)
        // A removed project does not count.
        #expect(defaultScope(state(session("1", "/a"), session("2", "/b"), hidden: ["/b"])) == .of(key: "/a"))
    }

    @Test func foldersThatAreOnlyCanonicallyEqualAreTwoProjects() {
        // é as one character, and as e with a combining accent: Kotlin and the daemon tell them apart.
        let composed = "/work/caf\u{e9}", decomposed = "/work/cafe\u{301}"
        func units(_ paths: [String]) -> Set<[UInt8]> { Set(paths.map { Array($0.utf8) }) }
        let st = state(session("1", composed), session("2", decomposed, ended: now - 1000), session("3", "/b"), pinned: [decomposed])
        let all = projects(st)
        #expect(units(all.map(\.key)) == units([composed, decomposed, "/b"]))
        #expect(project(st, key: composed)?.sessions.map(\.id) == ["1"])
        #expect(project(st, key: decomposed)?.sessions.map(\.id) == ["2"])
        #expect(Set(all.map(\.id)).count == 3)
        #expect(units(projectGroups(st).pinned.map(\.key)) == units([decomposed]))
        #expect(isPinned(st, key: decomposed) && !isPinned(st, key: composed))

        #expect(HomeScope.of(key: composed) != .of(key: decomposed))
        #expect(resolveScope(remembered: .of(key: decomposed), state: state(session("1", composed), session("3", "/b"))) == .all)
        let line = homeTimeline(st, scope: .of(key: composed), zone: zone)
        #expect(line.running.map(\.id) == ["1"])
        #expect(line.ended.map(\.id) == [])
        // Removing one of them leaves the other on the list.
        let hidden = state(session("1", composed), session("2", decomposed, ended: now - 1000), session("3", "/b"), hidden: [composed])
        #expect(units(listedProjects(hidden).map(\.key)) == units([decomposed, "/b"]))
        #expect(homeTimeline(hidden, scope: .all, zone: zone).ended.map(\.id) == ["2"])
        #expect(isHidden(hidden, key: composed) && !isHidden(hidden, key: decomposed))
    }

    @Test func theRememberedScopeHoldsWhileItsProjectIsListed() {
        let two = state(session("1", "/a"), session("2", "/b"), session("3", "/c"))
        #expect(resolveScope(remembered: .of(key: "/b"), state: two) == .of(key: "/b"))
        #expect(resolveScope(remembered: .all, state: two) == .all)
        #expect(resolveScope(remembered: nil, state: two) == .all)
        // Gone or removed: the default again.
        #expect(resolveScope(remembered: .of(key: "/gone"), state: two) == .all)
        #expect(resolveScope(remembered: .of(key: "/b"), state: state(session("1", "/a"), session("2", "/b"), session("3", "/c"), hidden: ["/b"])) == .all)
        #expect(resolveScope(remembered: .of(key: "/b"), state: state(session("1", "/a"), session("2", "/b"), hidden: ["/b"])) == .of(key: "/a"))
        // 全部项目 is not offered for a single project.
        #expect(resolveScope(remembered: .all, state: state(session("1", "/a"))) == .of(key: "/a"))
    }

    @Test func theScopeSurvivesTheDeviceSettingsWithoutItsPath() {
        let key = scopeKey(root: Data(repeating: 7, count: 32))
        let foo = "/Users/me/Code/foo"
        let st = state(session("1", foo), session("2", "/a:b/project:c"), session("3", "/work/caf\u{e9}"))
        let listed = listedProjects(st)
        for s in [HomeScope.all, .of(key: foo), .of(key: "/a:b/project:c")] { #expect(decodeScope(encodeScope(s, key: key), key: key, listed: listed) == s) }
        // What is stored is a mark made with the account's key, the same on Android: nothing of the path.
        #expect(encodeScope(.all, key: key) == "all")
        #expect(encodeScope(.of(key: foo), key: key) == "project:2ac624a27cda79e036f4a5327451f4852a8937737402d8fdc57fd5cd97e871b8")
        #expect(encodeScope(.of(key: "/work/caf\u{e9}"), key: key) == "project:6ab514031182a854b288122c47d43faaf9a13a45f49c8f1d97b52cd1190e5220")
        #expect(encodeScope(.of(key: "/work/cafe\u{301}"), key: key) != encodeScope(.of(key: "/work/caf\u{e9}"), key: key))
        // Another account's key reads nothing out of it, and a project that is not listed is not remembered.
        #expect(decodeScope(encodeScope(.of(key: foo), key: key), key: scopeKey(root: Data(repeating: 8, count: 32)), listed: listed) == nil)
        #expect(decodeScope(encodeScope(.of(key: "/gone"), key: key), key: key, listed: listed) == nil)
        #expect(decodeScope(nil, key: key, listed: listed) == nil)
        #expect(decodeScope("something else", key: key, listed: listed) == nil)
        #expect(decodeScope("project:" + foo, key: key, listed: listed) == nil)
    }

    @Test func endedSessionsGroupByLocalCalendarDay() {
        func g(_ ms: Int64) -> EndedGroup { endedGroup(ms, now: now, zone: zone) }
        #expect(g(at(2026, 10, 4, 0, 0)) == .today)
        #expect(g(now + 3600_000) == .today)
        // One minute apart across midnight is another day.
        #expect(g(at(2026, 10, 3, 23, 59)) == .yesterday)
        #expect(g(at(2026, 10, 3, 0, 0)) == .yesterday)
        #expect(g(at(2026, 10, 2, 23, 59)) == .week)
        #expect(g(at(2026, 9, 28, 0, 0)) == .week)
        #expect(g(at(2026, 9, 27, 23, 59)) == .month(year: 2026, month: 9))
        #expect(g(at(2025, 12, 31)) == .month(year: 2025, month: 12))

        #expect(groupTitle(.today, now: now, zone: zone) == "今天")
        #expect(groupTitle(.yesterday, now: now, zone: zone) == "昨天")
        #expect(groupTitle(.week, now: now, zone: zone) == "近 7 天")
        #expect(groupTitle(.month(year: 2026, month: 9), now: now, zone: zone) == "9 月")
        #expect(groupTitle(.month(year: 2025, month: 12), now: now, zone: zone) == "2025 年 12 月")
    }

    @Test func aMonthOfThisYearEarlierThanAWeekAgoCanBeTheCurrentMonth() {
        let late = at(2026, 10, 20)
        #expect(endedGroup(at(2026, 10, 2), now: late, zone: zone) == .month(year: 2026, month: 10))
        #expect(groupTitle(.month(year: 2026, month: 10), now: late, zone: zone) == "10 月")
    }

    @Test func anEndedRowWritesTheTimeItsGroupCalls() {
        #expect(endedTime(at(2026, 10, 4, 7, 10), now: now, zone: zone) == "7:10")
        #expect(endedTime(at(2026, 10, 3, 18, 44), now: now, zone: zone) == "18:44")
        #expect(endedTime(at(2026, 10, 2, 8, 0), now: now, zone: zone) == "周五")
        #expect(endedTime(at(2026, 9, 28, 8, 0), now: now, zone: zone) == "周一")
        #expect(endedTime(at(2026, 9, 27, 8, 0), now: now, zone: zone) == "9月27日")
        #expect(endedTime(at(2025, 12, 31), now: now, zone: zone) == "12月31日")
        #expect(endedTime(0, now: now, zone: zone) == "")
    }

    @Test func groupsKeepTheOrderOfTheList() {
        let list = [
            session("a", "/a", ended: at(2026, 10, 4, 7, 50)), session("b", "/a", ended: at(2026, 10, 4, 7, 10)),
            session("c", "/a", ended: at(2026, 10, 3, 18, 44)), session("d", "/a", ended: at(2026, 9, 30)),
            session("e", "/a", ended: at(2026, 9, 27)), session("f", "/a", ended: at(2026, 9, 1)), session("g", "/a", ended: at(2025, 9, 1)),
        ]
        let got = groupEnded(list, now: now, zone: zone)
        #expect(got.map(\.group) == [.today, .yesterday, .week, .month(year: 2026, month: 9), .month(year: 2025, month: 9)])
        #expect(got.map { $0.sessions.map(\.id) } == [["a", "b"], ["c"], ["d"], ["e", "f"], ["g"]])
    }

    @Test func aProjectScopeListsItsOwnSessionsButEveryPendingOne() throws {
        let st = state(
            session("run-a", "/a", created: now - 1000), session("old-a", "/a", ended: now - 5000),
            session("run-b", "/b"), session("old-b", "/b", ended: now - 4000),
            session("wait-b", "/b", approvals: 1), session("wait-a", "/a", created: now - 9000, approvals: 1))
        let a = homeTimeline(st, scope: .of(key: "/a"), zone: zone)
        #expect(Set(a.attention.map(\.id)) == ["wait-a", "wait-b"])
        #expect(a.running.map(\.id) == ["run-a"])
        #expect(a.ended.map(\.id) == ["old-a"])
        #expect(a.total == 3)
        #expect(!a.meta.project)
        let waitA = try #require(st.sessions["wait-a"])
        let waitB = try #require(st.sessions["wait-b"])
        #expect(outsideScope(waitB, scope: .of(key: "/a")))
        #expect(!outsideScope(waitA, scope: .of(key: "/a")))
        // The card from outside the scope names its project; the scope's own does not.
        #expect(cardMeta(st, waitB, scope: .of(key: "/a")) == ["b"])
        #expect(cardMeta(st, waitA, scope: .of(key: "/a")) == [])

        let all = homeTimeline(st, scope: .all, zone: zone)
        #expect(all.running.map(\.id) == ["run-a", "run-b"])
        #expect(all.ended.map(\.id) == ["old-b", "old-a"])
        #expect(all.total == 6)
        #expect(all.meta.project)
        #expect(cardMeta(st, waitA, scope: .all) == ["a"])
    }

    @Test func aRemovedProjectsSessionsOnlyShowWhileTheyGoOn() {
        let st = state(
            session("run-a", "/a"), session("old-a", "/a", ended: now - 5000),
            session("run-h", "/h"), session("wait-h", "/h", approvals: 1), session("old-h", "/h", ended: now - 1000),
            session("x", "/c"), hidden: ["/h"])
        let all = homeTimeline(st, scope: .all, zone: zone)
        #expect(all.attention.map(\.id) == ["wait-h"])
        #expect(Set(all.running.map(\.id)) == ["run-a", "run-h", "x"])
        #expect(all.ended.map(\.id) == ["old-a"])
    }

    @Test func theThirdLineOnlyWritesWhatDiffers() {
        let one = state(session("1", "/a"), session("2", "/a", forkedFrom: "1"))
        let m1 = homeTimeline(one, scope: .of(key: "/a"), zone: zone).meta
        #expect(m1 == RowMeta())
        #expect(metaLine(one, one.sessions["1"]!, meta: m1) == "")
        #expect(metaLine(one, one.sessions["2"]!, meta: m1) == "分叉")

        let many = state(
            session("1", "/Code/foo", machine: "m"), session("2", "/Code/foo", machine: "n", agent: .claude, forkedFrom: "1"),
            session("3", "/Code/bar"), machines: ["m", "n"])
        let all = homeTimeline(many, scope: .all, zone: zone).meta
        #expect(all == RowMeta(project: true, agent: true, machine: true))
        #expect(metaLine(many, many.sessions["2"]!, meta: all) == "分叉 · foo · Claude Code · n")
        // One CLI in the scope: not written, though the account has two.
        let bar = homeTimeline(many, scope: .of(key: "/Code/bar"), zone: zone).meta
        #expect(bar == RowMeta(machine: true))
        #expect(metaLine(many, many.sessions["3"]!, meta: bar) == "m")
        // An ended session's CLI counts: the scope used more than one.
        let used = state(session("1", "/a"), session("2", "/a", ended: now - 1000, agent: .claude))
        #expect(homeTimeline(used, scope: .of(key: "/a"), zone: zone).meta.agent)
        // A project page never writes the project.
        #expect(rowMeta(many, sessions: many.sessions.values.filter { projectKey($0) == "/Code/foo" }, project: false) == RowMeta(agent: true, machine: true))
    }

    @Test func theFilterMatchesTitlesOnlyAndKeepsTheOrder() {
        let st = state(
            session("1", "/a", title: "补充接口文档", created: now - 1000), session("2", "/a", title: "修复登录超时", created: now - 2000),
            session("3", "/a", title: "给登录接口加限流", ended: now - 3000), session("4", "/a", title: "Upgrade API layer", ended: now - 4000),
            session("5", "/a", title: "接口返回值统一成驼峰", ended: at(2026, 9, 21)))
        let f = homeTimeline(st, scope: .of(key: "/a"), query: " 接口 ", zone: zone)
        #expect(f.running.map(\.id) == ["1"])
        #expect(f.ended.map(\.id) == ["3", "5"])
        #expect(f.size == 3)
        // The count that decides whether the box shows does not shrink with the filter.
        #expect(f.total == 5)
        #expect(homeTimeline(st, scope: .of(key: "/a"), query: "api", zone: zone).ended.map(\.id) == ["4"])
        // The folder is not the title.
        #expect(homeTimeline(st, scope: .of(key: "/a"), query: "/a", zone: zone).size == 0)
        #expect(homeTimeline(st, scope: .of(key: "/a"), query: "  ", zone: zone).size == 5)

        #expect(filterTail(count: 3, query: " 接口 ") == "3 个会话的标题里有「接口」")
        #expect(filterTail(count: 0, query: "zzz") == "没有标题里有「zzz」的会话")
    }

    @Test func theTailCountsEndedSessionsOnceTheyTakeMoreThanAPage() {
        #expect(endedTail(total: 30, more: false) == nil)
        #expect(endedTail(total: 71, more: true) == "共 71 个，滚到底加载更早的")
        #expect(endedTail(total: 71, more: false) == "共 71 个")
    }

    private func summary(_ path: String, _ ended: UInt32) -> PBProjectSummary { .with { $0.path = path; $0.ended = ended } }

    private func withSummaries(_ st: RemoteState, _ byMachine: [String: [PBProjectSummary]]) -> RemoteState {
        var out = st
        for (id, list) in byMachine {
            out.machines[id] = MachineView(id: id, online: true, lastSeenMs: 0, meta: .with { $0.projects = list })
        }
        return out
    }

    @Test func theScopeCountsEndedSessionsByTheSummariesNotByThePagesFetched() {
        // Two machines have ended sessions in /a; only two of them are held here.
        let st = withSummaries(
            state(
                session("1", "/a"), session("2", "/a", ended: now - 1000), session("3", "/a", ended: now - 2000),
                session("4", "/b", ended: now - 3000), session("5", "/gone", ended: now - 4000),
                hidden: ["/gone"], machines: ["m", "n"]),
            ["m": [summary("/a", 40), summary("/b", 7), summary("/gone", 100)], "n": [summary("/a", 2), summary("/old", 5)]])
        let a = homeTimeline(st, scope: .of(key: "/a"), zone: zone)
        #expect(a.ended.map(\.id) == ["2", "3"])
        #expect(a.endedTotal == 42)
        // The filter is offered by what the scope has, not by what is held: one running and 42 ended.
        #expect(a.total == 43)
        // Every project leaves out the removed ones, and counts a project nothing of which is held.
        let all = homeTimeline(st, scope: .all, zone: zone)
        #expect(all.endedTotal == 40 + 2 + 7 + 5)
        #expect(all.total == 1 + all.endedTotal)
        // A filter changes what is listed, not how many the scope has.
        #expect(homeTimeline(st, scope: .of(key: "/a"), query: "zzz", zone: zone).endedTotal == 42)
        // Summaries behind the sessions held never count fewer than those.
        #expect(homeTimeline(withSummaries(st, ["m": [], "n": []]), scope: .of(key: "/a"), zone: zone).endedTotal == 2)
        // A project known from a summary alone can be the scope.
        #expect(resolveScope(remembered: .of(key: "/old"), state: st) == .of(key: "/old"))
        #expect(homeTimeline(st, scope: .of(key: "/old"), zone: zone).total == 5)
    }

    @Test func anotherPageIsDueWhileFewerAreHeldThanShownOrTheShownReachPastThePagesFetched() {
        func held(_ n: Int) -> [SessionView] { (1...max(n, 1)).prefix(n).map { session("e\($0)", "/a", ended: now - Int64($0) * 1000, listed: true) } }
        var base = RemoteState()
        for s in held(50) { base.sessions[s.id] = s }
        // Every page is here: nothing to ask for, whatever the summaries say.
        #expect(!needsEndedPage(base, held: held(50), want: 60, total: 200))
        var more = base
        more.sessionsNext = SessionPlace(endedMs: now - 50_000, sessionId: "e50")
        #expect(!needsEndedPage(more, held: held(50), want: 30, total: 200))
        #expect(needsEndedPage(more, held: held(50), want: 60, total: 200))
        // The scope has no more than are held: the pages left are other projects'.
        #expect(!needsEndedPage(more, held: held(50), want: 60, total: 50))
        // A page may hold none of the scope's sessions: it is due until enough are held.
        #expect(needsEndedPage(more, held: [], want: 30, total: 4))
        // Kept from before a reconnect, or fetched on its own: others not fetched yet come before it.
        let old = session("old", "/a", ended: now - 900_000, listed: true)
        #expect(needsEndedPage(more, held: held(20) + [old], want: 30, total: 21))
        #expect(!needsEndedPage(more, held: held(20) + [old], want: 20, total: 21))
        #expect(pastFetched(more, old))
        #expect(!pastFetched(more, held(1)[0]))
        #expect(!pastFetched(base, old))
        // Before the first page came (a cached page shown at start), that page is due while more are to be shown than are held.
        var unlisted = base
        unlisted.sessionsListed = false
        #expect(unlisted.moreSessions)
        #expect(needsEndedPage(unlisted, held: held(50), want: 60, total: 200))
        #expect(!needsEndedPage(unlisted, held: held(50), want: 30, total: 200))
        #expect(!pastFetched(unlisted, old))
    }

    @Test func theEndSaysWhatBecomesOfTheEarlierPages() {
        var online = RemoteState()
        online.conn = .online
        online.sessionsNext = SessionPlace(endedMs: 1, sessionId: "a")
        #expect(earlierTail(online) == "正在加载更早的会话…")
        var offline = online
        offline.conn = .offline
        #expect(earlierTail(offline) == "连上服务器后加载更早的会话")
        var failed = online
        failed.sessionsError = "超时"
        #expect(earlierTail(failed) == "没能加载更早的会话：超时 · 点这里重试")
        // The first page itself: there is no listing yet to have earlier pages.
        var first = RemoteState()
        first.conn = .online
        first.sessionsError = "超时"
        first.sessionsListed = false
        #expect(earlierTail(first) == "没能读取会话列表：超时 · 点这里重试")
    }

    @Test func theScopeMenuListsEveryProjectFirstThenPinnedThenTheOthers() {
        let st = state(
            session("1", "/Users/me/Code/foo"), session("2", "/Users/me/Code/foo", created: now - 100),
            session("3", "/Users/me/Code/billing", created: now - 50), session("4", "/Users/me/Code/blog", ended: now - 2 * 3600_000),
            session("5", "/Users/me/gone", ended: now - 1000), hidden: ["/Users/me/gone"], pinned: ["/Users/me/Code/blog"])
        let rows = scopeRows(st, current: .of(key: "/Users/me/Code/foo"))
        #expect(rows.map(\.name) == ["全部项目", "blog", "billing", "foo"])
        #expect(rows.map(\.detail) == ["3 个进行中", "~/Code/blog", "~/Code/billing · 1 个进行中", "~/Code/foo · 2 个进行中"])
        #expect(rows.map(\.current) == [false, false, false, true])
        #expect(scopeRows(st, current: .all, search: "BILL").map(\.name) == ["billing"])
        // One project: nothing to choose 全部项目 over.
        #expect(scopeRows(state(session("1", "/Users/me/Code/foo")), current: .of(key: "/Users/me/Code/foo")).map(\.name) == ["foo"])
    }

    // --- not in Android's tests ---

    @Test func projectsCountWaitingAndRunningSessionsAsTheHomeDoes() {
        let st = state(
            session("1", "/Users/me/Code/foo", approvals: 1), session("2", "/Users/me/Code/foo"),
            session("3", "/Users/me/Code/billing", approvals: 2), session("4", "/Users/me/Code/blog", ended: now - 1000))
        let rows = scopeRows(st, current: .all)
        #expect(rows.map(\.detail) == ["2 个待处理 · 1 个进行中", "~/Code/billing · 1 个待处理", "~/Code/foo · 1 个待处理 · 1 个进行中", "~/Code/blog"])
        #expect(sessionCounts(st, []) == "")
        // With nothing waiting or running, 全部项目 says how many projects there are.
        let quiet = state(session("1", "/a", ended: now - 1000), session("2", "/b", ended: now - 1000))
        #expect(scopeRows(quiet, current: .all).first?.detail == "2 个项目")
    }

    @Test func daysAreTheZonesNotTheDevices() {
        // 23:30 UTC on the 3rd is already the 4th in Shanghai, and still the 3rd in New York.
        let ms = Mk.instant("2026-10-03T23:30:00Z")
        let noon = Mk.instant("2026-10-04T04:00:00Z")
        #expect(endedGroup(ms, now: noon, zone: Mk.shanghai) == .today)
        #expect(endedGroup(ms, now: noon, zone: TimeZone(identifier: "America/New_York")!) == .yesterday)
        #expect(endedTime(ms, now: noon, zone: Mk.shanghai) == "7:30")
        // The weekdays, Monday first: 2026-09-28 is a Monday, 2026-10-04 a Sunday.
        #expect(weekdayNames[LocalDay(ms: at(2026, 9, 28), zone: zone).weekdayIndex] == "周一")
        #expect(weekdayNames[LocalDay(ms: at(2026, 10, 4), zone: zone).weekdayIndex] == "周日")
        #expect(weekdayNames[LocalDay(ms: at(1969, 12, 28), zone: zone).weekdayIndex] == "周日")
        // A leap day and a year's end count as one day apart.
        #expect(endedGroup(at(2024, 2, 29, 23, 59), now: at(2024, 3, 1, 0, 1), zone: zone) == .yesterday)
        #expect(endedGroup(at(2025, 12, 31, 23, 59), now: at(2026, 1, 1, 0, 1), zone: zone) == .yesterday)
    }

    @Test func theFilterIgnoresCaseOneCharacterAtATime() {
        let st = state(session("1", "/a", title: "Fix the Straße sign"), session("2", "/a", title: "ÉCOLE schedule"))
        func ids(_ q: String) -> [String] { homeTimeline(st, scope: .of(key: "/a"), query: q, zone: zone).running.map(\.id).sorted() }
        #expect(ids("STRA") == ["1"])
        #expect(ids("STRASSE") == [])
        #expect(ids("école") == ["2"])
        // An ideographic space is whitespace to trim(); the query between is what counts.
        #expect(ids("\u{3000}sign\u{3000}") == ["1"])
        #expect(scopeName(.all) == "全部项目")
        #expect(scopeName(.of(key: "/Users/me/Code/foo")) == "foo")
    }
}
