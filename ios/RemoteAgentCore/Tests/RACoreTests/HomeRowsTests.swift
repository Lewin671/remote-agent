import Foundation
import RAProto
import SwiftProtobuf
import Testing

@testable import RACore

/// The home page as a list of lines: what Android's HomeScreen decides inside its composable.
@Suite struct HomeRowsTests {
    private let zone = Mk.shanghai
    private func at(_ y: Int, _ mo: Int, _ d: Int, _ h: Int = 12, _ mi: Int = 0) -> Int64 { Mk.at(y, mo, d, h, mi, zone: Mk.shanghai) }
    // A Sunday.
    private var now: Int64 { at(2026, 10, 4, 9, 41) }
    private func ts(_ ms: Int64) -> Google_Protobuf_Timestamp { .with { $0.seconds = ms / 1000 } }

    private func session(
        _ id: String, _ cwd: String = "/p/foo", title: String? = nil, created: Int64? = nil, ended: Int64? = nil,
        machine: String = "m", agent: PBAgent = .codex, approvals: Int32 = 0, questions: Int32 = 0, plan: Bool = false,
        status: PBSessionStatus? = nil, preview: String = ""
    ) -> SessionView {
        let created = created ?? now - 3_600_000
        return Mk.session(id, machine: machine, updatedMs: ended ?? created, listedEndedMs: ended ?? 0) {
            $0.cwd = cwd
            $0.title = title ?? id
            $0.agent = agent
            $0.createdAt = ts(created)
            $0.pendingApprovals = approvals
            $0.pendingQuestions = questions
            $0.planOpen = plan
            $0.preview = preview
            $0.status = status ?? (ended != nil ? .exited : .idle)
            if let ended { $0.endedAt = ts(ended) }
        }
    }

    private func state(_ sessions: [SessionView], machines: [String] = ["m"], _ more: (inout RemoteState) -> Void = { _ in }) -> RemoteState {
        Mk.state(sessions: sessions, machines: machines.map { MachineView(id: $0, online: true, lastSeenMs: 0) }, prefs: PBProjectPrefs()) {
            $0.loaded = true
            $0.conn = .online
            $0.link = .online
            $0.synced = true
            more(&$0)
        }
    }

    private func list(_ st: RemoteState, scope: HomeScope = .all, query: String = "", shown: HomeShown = HomeShown()) -> HomeList {
        homeList(st, scope: scope, query: query, shown: shown, now: now, zone: zone)
    }

    @Test func theListIsWhatWaitsThenWhatRunsThenTheEndedByDay() {
        let st = state([
            session("wait", approvals: 1), session("run", created: now - 60_000, status: .running),
            session("idle", created: now - 120_000),
            session("today", ended: at(2026, 10, 4, 7, 10)), session("yesterday", ended: at(2026, 10, 3, 18, 44)),
            session("week", ended: at(2026, 9, 29, 10, 0)), session("month", ended: at(2026, 9, 21, 10, 0)),
            session("lastYear", ended: at(2025, 12, 3, 10, 0)),
        ])
        let l = list(st)
        #expect(l.items.map(\.id) == [
            "h:attention", "s:wait", "h:running", "s:run", "s:idle", "h:today", "s:today", "h:yesterday", "s:yesterday",
            "h:week", "s:week", "h:month:2026-9", "s:month", "h:month:2025-12", "s:lastYear",
        ])
        let titles = l.items.compactMap { item -> String? in
            if case .label(_, let title, let count) = item { return title + (count.map { " \($0)" } ?? "") }
            return nil
        }
        #expect(titles == ["需要处理 1", "进行中 2", "今天", "昨天", "近 7 天", "9 月", "2025 年 12 月"])
        #expect(l.empty == nil && !l.showFilter && !l.wantAll && !l.wantPage && !l.grows)
        // The last running row draws no line under it.
        #expect(l.items[3] == .running(st.sessions["run"]!, last: false))
        #expect(l.items[4] == .running(st.sessions["idle"]!, last: true))
    }

    @Test func aSessionKeepsItsLineWhenItMovesBetweenParts() {
        let waiting = list(state([session("s", approvals: 1)])).items.last
        let running = list(state([session("s")])).items.last
        let ended = list(state([session("s", ended: now - 1000)])).items.last
        #expect(waiting?.id == "s:s" && running?.id == "s:s" && ended?.id == "s:s")
        #expect(Set(list(state((0..<40).map { session("e\($0)", ended: now - Int64($0) * 86_400_000) })).items.map(\.id)).count == 30 + 4 + 1)
    }

    @Test func onlyTheFirstFewWaitAndRunUntilTheRestIsAskedFor() {
        let st = state((0..<5).map { session("a\($0)", created: now - Int64($0) * 1000, approvals: 1) } + (0..<8).map { session("r\($0)", created: now - Int64($0) * 1000) })
        let l = list(st)
        #expect(l.items.filter { if case .attention = $0 { true } else { false } }.count == attentionMax)
        #expect(l.items.contains(.moreAttention(2)))
        #expect(l.items.filter { if case .running = $0 { true } else { false } }.count == runningMax)
        #expect(l.items.contains(.moreRunning(3)))
        let all = list(st, shown: HomeShown(allAttention: true, allRunning: true))
        #expect(all.items.count == 2 + 5 + 8)
        // A filter shows every match.
        #expect(list(st, query: "r").items.filter { if case .running = $0 { true } else { false } }.count == 8)
    }

    @Test func endedSessionsComeThirtyAtATimeAndTheLastLineCountsThem() {
        let st = state((0..<71).map { session("e\($0)", ended: now - Int64($0 + 1) * 60_000) })
        let first = list(st)
        #expect(first.items.filter { if case .ended = $0 { true } else { false } }.count == 30)
        #expect(first.items.last == .tail(.text("共 71 个，滚到底加载更早的")))
        #expect(first.grows && first.endedHeld == 71 && !first.wantPage)
        let all = list(st, shown: HomeShown(ended: 90))
        #expect(all.items.last == .tail(.text("共 71 个")) && !all.grows)
        #expect(first.showFilter)
        // Thirty or fewer need no last line.
        #expect(!list(state((0..<30).map { session("e\($0)", ended: now - 1000) })).items.contains { if case .tail = $0 { true } else { false } })
    }

    @Test func pagesStillOnTheServerAreAskedForAsTheEndIsReached() {
        var summary = PBProjectSummary()
        summary.path = "/p/foo"
        summary.ended = 80
        let machine = MachineView(id: "m", online: true, lastSeenMs: 0, meta: .with { $0.projects = [summary] })
        var st = state((0..<50).map { session("e\($0)", ended: now - Int64($0 + 1) * 60_000) }) {
            $0.sessionsNext = SessionPlace(endedMs: 1, sessionId: "e49")
        }
        st.machines["m"] = machine
        // Thirty of fifty held: nothing to fetch yet, the end shows the next thirty.
        let first = list(st)
        #expect(!first.wantPage && first.grows && first.items.last == .tail(.text("共 80 个，滚到底加载更早的")))
        // Sixty wanted, fifty held: the next page, and the list waits for it.
        let second = list(st, shown: HomeShown(ended: 60))
        #expect(second.wantPage && !second.grows && second.items.last == .tail(.text("共 80 个，滚到底加载更早的")))
        // A page that failed says so, with its retry.
        st.sessionsError = "超时"
        #expect(list(st, shown: HomeShown(ended: 60)).items.last == .tail(.earlier("没能加载更早的会话：超时 · 点这里重试", retry: true)))
        st.conn = .offline
        #expect(list(st, shown: HomeShown(ended: 60)).items.last == .tail(.earlier(earlierOffline, retry: false)))
    }

    @Test func aFilterWantsEveryPageAndCountsWhatItMatched() {
        var st = state([session("a", title: "补充接口文档"), session("b", title: "接口限流", ended: now - 1000), session("c", title: "别的")])
        #expect(list(st, query: "接口").items.last == .tail(.text("2 个会话的标题里有「接口」")))
        #expect(list(st, query: "没有").items == [.tail(.text("没有标题里有「没有」的会话"))])
        #expect(list(st, query: "接口").showFilter)
        // While pages are left the count is not known yet.
        st.sessionsNext = SessionPlace(endedMs: 1, sessionId: "b")
        let loading = list(st, query: "接口")
        #expect(loading.wantAll && !loading.wantPage && loading.items.last == .tail(.earlier(loadingEarlier, retry: false)))
        // Blank is not a filter.
        #expect(!list(st, query: "  ").wantAll)
    }

    @Test func anEmptyPageSaysWhy() {
        var nothing = RemoteState()
        #expect(list(nothing).empty == .loading)
        nothing.loaded = true
        #expect(list(nothing).empty == .account(.noMachines))
        var offline = state([], machines: [])
        offline.machines["m"] = MachineView(id: "m", online: false, lastSeenMs: 0)
        #expect(list(offline).empty == .account(.noneOnline))
        offline.machines["m"] = MachineView(id: "m", online: true, lastSeenMs: 0, meta: .with { $0.hostname = "MacBook.local" })
        #expect(list(offline).empty == .account(.ready(machine: "MacBook")))
        // Out of reach of the server nothing is said of the computers.
        offline.link = .noNetwork
        #expect(list(offline).empty == .account(.unlinked))
        // A project added and not used yet, chosen as the scope.
        let added = Mk.state(machines: [MachineView(id: "m", online: true, lastSeenMs: 0)], prefs: .with {
            $0.settings = [.with { $0.path = "/p/new"; $0.machineID = "m" }]
        }) { $0.loaded = true }
        #expect(list(added, scope: .of(key: "/p/new")).empty == .scope)
        #expect(list(added, scope: .all).empty == nil)
    }

    @Test func aRowWritesItsStateItsPreviewAndOnlyWhatDiffers() {
        let run = session("run", title: "修复登录超时", created: now - 20_000, status: .running, preview: "正在执行 `go test`")
        let st = state([run], machines: ["m", "n"])
        let row = sessionRowText(st, run, meta: RowMeta(project: true, agent: true, machine: true), now: now, zone: zone)
        #expect(row == SessionRowText(title: "修复登录超时", time: "刚刚", status: .running, preview: "正在执行 go test", meta: "foo · Codex · m"))
        #expect(row.spoken == "修复登录超时，运行中，正在执行 go test，foo，Codex，m，刚刚")
        // The status word already says a plan waits.
        let plan = session("plan", plan: true)
        #expect(sessionRowText(st, plan, meta: RowMeta(), now: now, zone: zone).preview.isEmpty)
        // Out of reach of the server the last state heard is not shown as if it held.
        var cut = st
        cut.link = .noServer
        #expect(sessionRowText(cut, run, meta: RowMeta(), now: now, zone: zone).status == .unlinked)
    }

    @Test func anEndedRowWritesWhenItEndedAfterTheProjectWhereTheListSpansProjects() {
        let s = session("e", "/p/blog", title: "发布秋季文章", ended: at(2026, 10, 4, 7, 50))
        #expect(endedRowText(s, project: true, now: now, zone: zone).right == "blog · 7:50")
        #expect(endedRowText(s, project: false, now: now, zone: zone).right == "7:50")
        #expect(endedRowText(s, project: true, now: now, zone: zone).spoken == "发布秋季文章，已结束，blog，7:50")
        #expect(endedRowText(session("w", ended: at(2026, 9, 29, 10, 0)), project: false, now: now, zone: zone).right == "周二")
    }

    @Test func aCardSaysWhatWaitsAndWhereItIsFrom() {
        let st = state([], machines: ["m", "n"])
        let api = session("api", "/p/foo", title: "重构 API 层", approvals: 1)
        let request = PBApprovalRequested.with {
            $0.approvalID = "A1"
            $0.command = "go test ./..."
            $0.deadline = ts(now + 8 * 60_000 - 20_000)
        }
        let card = attentionCardText(st, api, scope: .all, approval: request, question: nil, now: now, zone: zone)
        #expect(card.kind == "请求执行" && card.right == "剩 8 分钟，超时拒绝" && card.action == "查看会话" && card.meta == "foo · m")
        #expect(attentionCardText(st, api, scope: .all, approval: .with { $0.approvalID = "A2" }, question: nil, now: now, zone: zone).kind == "请求权限")
        // A tool that is not a shell command is named, as in the session's dock: the card offers 允许 too.
        let write = PBApprovalRequested.with {
            $0.approvalID = "A3"
            $0.toolName = "Write"
            $0.toolInputJson = "{\"file_path\":\"/p/foo/a.go\"}"
        }
        #expect(attentionCardText(st, api, scope: .all, approval: write, question: nil, now: now, zone: zone).kind == "请求权限 · Write")
        #expect(approvalBody(write) == "{\"file_path\":\"/p/foo/a.go\"}")
        #expect(approvalBody(request) == "go test ./...")
        // In its own project's scope the card does not repeat the project; from another one it does.
        #expect(attentionCardText(st, api, scope: .of(key: "/p/foo"), approval: nil, question: nil, now: now, zone: zone).meta == "m")
        #expect(attentionCardText(st, api, scope: .of(key: "/p/bar"), approval: nil, question: nil, now: now, zone: zone).meta == "foo · m")
        let dep = session("dep", "/p/bar", title: "升级依赖", agent: .claude, questions: 1)
        let asked = attentionCardText(st, dep, scope: .all, approval: nil, question: "大版本也一起升吗？", now: now, zone: zone)
        #expect(asked.kind == "Claude Code 在问你" && asked.action == "去回答" && asked.meta == "bar · m · 大版本也一起升吗？")
        let plan = attentionCardText(st, session("p", plan: true), scope: .all, approval: nil, question: nil, now: now, zone: zone)
        #expect(plan.kind == "计划待批准" && plan.action == "去处理")
    }

    @Test func anAnswerToAnApprovalNamesItAndTheDecision() {
        let cmd = resolveApprovalCommand(sessionId: "s", approvalId: "A1", allow: false, message: "先别动", always: false)
        #expect(cmd.resolveApproval.sessionID == "s" && cmd.resolveApproval.approvalID == "A1")
        #expect(!cmd.resolveApproval.allow && cmd.resolveApproval.message == "先别动" && !cmd.resolveApproval.always)
    }
}
