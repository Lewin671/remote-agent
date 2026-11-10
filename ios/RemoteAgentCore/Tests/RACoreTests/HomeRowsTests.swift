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
            "s:wait", "h:running", "s:run", "s:idle", "h:today", "s:today", "h:yesterday", "s:yesterday",
            "h:week", "s:week", "h:month:2026-9", "s:month", "h:month:2025-12", "s:lastYear",
        ])
        let titles = l.items.compactMap { item -> String? in
            if case .label(_, let title, let count) = item { return title + (count.map { " \($0)" } ?? "") }
            return nil
        }
        #expect(titles == ["进行中", "今天", "昨天", "近 7 天", "9 月", "2025 年 12 月"])
        #expect(l.empty == nil && !l.showFilter && !l.wantAll && !l.wantPage && !l.grows)
        // The approval that waits is the dark card.
        #expect(l.items[0] == .attentionCard(st.sessions["wait"]!))
        // The last running row draws no line under it.
        #expect(l.items[2] == .running(st.sessions["run"]!, last: false))
        #expect(l.items[3] == .running(st.sessions["idle"]!, last: true))
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
        // One dark card, then three rows, then 还有 1 件 closes the grey list.
        #expect(l.items.filter { if case .attentionCard = $0 { true } else { false } }.count == 1)
        let rows = l.items.compactMap { item -> (Bool, Bool)? in if case .attention(_, let first, let last) = item { (first, last) } else { nil } }
        #expect(rows.count == attentionMax && rows.map(\.0) == [true, false, false] && rows.map(\.1) == [false, false, false])
        #expect(l.items.contains(.moreAttention(1)))
        #expect(l.items.filter { if case .running = $0 { true } else { false } }.count == runningMax)
        #expect(l.items.contains(.moreRunning(3)))
        let all = list(st, shown: HomeShown(allAttention: true, allRunning: true))
        #expect(all.items.count == 5 + 1 + 8)
        #expect(all.items.contains { if case .attention(_, _, true) = $0 { true } else { false } })
        // A filter shows every match.
        #expect(list(st, query: "r").items.filter { if case .running = $0 { true } else { false } }.count == 8)
    }

    @Test func onlyAnApprovalIsTheDarkCard() {
        // Questions, plans and popups are rows, the most urgent of them too.
        let st = state([session("q", questions: 1), session("p", plan: true)])
        let l = list(st)
        #expect(!l.items.contains { if case .attentionCard = $0 { true } else { false } })
        #expect(l.items.first == .attention(st.sessions["q"]!, first: true, last: false))
        #expect(l.items[1] == .attention(st.sessions["p"]!, first: false, last: true))
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

    @Test func aRowOfWhatWaitsSaysItsTypeAndWhatAbout() {
        let st = state([], machines: ["m", "n"])
        let api = session("api", "/p/foo", title: "重构 API 层", approvals: 1)
        let request = PBApprovalRequested.with {
            $0.approvalID = "A1"
            $0.command = "go test ./... \\\n  -count=1"
        }
        let row = pendingRowText(st, api, scope: .all, approval: request, question: nil, zone: zone)
        #expect(row == PendingRowText(title: "重构 API 层", kind: "请求执行", summary: "go test ./... \\ -count=1"))
        // Before the approval is read the status says what waits.
        #expect(pendingRowText(st, api, scope: .all, approval: nil, question: nil, zone: zone).kind == "等待审批")
        let dep = session("dep", "/p/bar", title: "升级依赖", agent: .claude, questions: 1)
        // In another project's scope the row names its project.
        let asked = pendingRowText(st, dep, scope: .of(key: "/p/foo"), approval: nil, question: "大版本也一起升吗？", zone: zone)
        #expect(asked.kind == "Claude Code 在问你" && asked.summary == "bar · 大版本也一起升吗？")
        #expect(asked.spoken == "升级依赖，Claude Code 在问你，bar，大版本也一起升吗？")
        #expect(pendingRowText(st, dep, scope: .of(key: "/p/bar"), approval: nil, question: "x", zone: zone).summary == "x")
        let plan = pendingRowText(st, session("p", plan: true), scope: .all, approval: nil, question: nil, zone: zone)
        #expect(plan.kind == "计划待批准" && plan.summary.isEmpty)
        var popup = session("t", status: .blocked)
        popup.info?.statusDetail = "Trust this folder?"
        #expect(pendingRowText(st, popup, scope: .all, approval: nil, question: nil, zone: zone) == PendingRowText(title: "t", kind: "终端里有弹窗", summary: "Trust this folder?"))
    }

    @Test func theDarkCardSaysWhatAndUntilWhenAndWhere() {
        var st = state([], machines: ["m", "n"])
        let api = session("api", "/p/foo", title: "重构 API 层", approvals: 1)
        var request = PBApprovalRequested.with {
            $0.approvalID = "A1"
            $0.command = "go test ./..."
            $0.deadline = ts(now + 8 * 60_000 - 20_000)
        }
        let card = approvalCardText(st, api, approval: request, timeoutNote: true, now: now, zone: zone)
        #expect(card == ApprovalCardText(kind: "请求执行", time: "剩 8 分钟，超时拒绝", hot: false, title: "重构 API 层", place: "foo · m"))
        #expect(approvalCardText(st, api, approval: request, timeoutNote: false, now: now, zone: zone).time == "剩 8 分钟")
        // The last minute is orange.
        request.deadline = ts(now + 30_000)
        #expect(approvalCardText(st, api, approval: request, timeoutNote: true, now: now, zone: zone).hot)
        // A tool that is not a shell command is named, and no deadline says nothing.
        let write = PBApprovalRequested.with {
            $0.approvalID = "A3"
            $0.toolName = "Write"
            $0.toolInputJson = "{\"file_path\":\"/p/foo/a.go\"}"
        }
        let w = approvalCardText(st, api, approval: write, timeoutNote: true, now: now, zone: zone)
        #expect(w.kind == "请求权限 · Write" && w.time.isEmpty && !w.hot)
        #expect(approvalBody(write) == "{\"file_path\":\"/p/foo/a.go\"}")
        // Out of reach of the server it cannot be decided: 未连接.
        st.link = .noNetwork
        let cut = approvalCardText(st, api, approval: request, timeoutNote: true, now: now, zone: zone)
        #expect(cut.time == "未连接" && !cut.hot)
    }

    @Test func theSentenceAtTheTopSaysHowThingsStand() {
        let wait = session("w", "/p/bar", approvals: 1)
        let run = session("r", status: .running)
        let idle = session("i")
        let idle2 = session("i2")
        func head(_ st: RemoteState, _ scope: HomeScope = .all) -> HomeHeadline? { homeHeadline(st, scope: scope) }
        #expect(head(state([wait, run, idle, idle2])) == HomeHeadline(kind: .waiting(count: 1), title: "1 件事等你", sub: "1 个会话在运行，2 个空闲"))
        #expect(head(state([wait, idle]))?.sub == "1 个会话空闲")
        // What waits is the account's; what runs is the scope's.
        #expect(head(state([wait, run]), .of(key: "/p/bar")) == HomeHeadline(kind: .waiting(count: 1), title: "1 件事等你"))
        #expect(head(state([run, idle, idle2])) == HomeHeadline(kind: .running, title: "1 个会话在运行", sub: "另有 2 个空闲"))
        #expect(head(state([idle])) == HomeHeadline(kind: .nothingWaits, title: "没有等你的事", sub: "1 个会话空闲"))
        #expect(head(state([session("e", ended: now - 1000)])) == HomeHeadline(kind: .nothingWaits, title: "没有等你的事"))
        #expect(head(state([])) == HomeHeadline(kind: .noSessions, title: "还没有会话"))
        #expect(head(state([]) { $0.sessionsListed = false; $0.sessionsError = "超时" })?.kind == .unreadable)
        #expect(head(state([idle]), .of(key: "/p/new")) == HomeHeadline(kind: .emptyProject, title: "这个项目还没有会话", sub: "/p/new", subPath: true))
        // Connected and nothing read yet: no sentence; not connected yet, 连接中….
        var fresh = RemoteState()
        #expect(head(fresh)?.kind == .connecting)
        fresh.link = .online
        #expect(head(fresh) == nil)
        // Out of reach the connection is the sentence, the nearest stretch that is down.
        let kept = state([wait, run])
        var cut = kept
        cut.link = .noNetwork
        #expect(head(cut) == HomeHeadline(kind: .noNetwork, title: "本机网络不通", sub: "下面是上次的内容"))
        cut.link = .waiting
        #expect(head(cut) == HomeHeadline(kind: .reconnecting, title: "正在重连…", sub: "下面是上次的内容"))
        cut.synced = false
        #expect(head(cut)?.title == "连接中…" && head(cut)?.unlinked == true)
        cut.link = .noServer
        #expect(head(cut) == HomeHeadline(kind: .noServer, title: "连不上服务器", sub: "正在重试，下面是上次的内容", details: true))
        var empty = RemoteState()
        empty.link = .noNetwork
        #expect(head(empty)?.sub == "联网后才能读取会话")
        empty.link = .noServer
        empty.error = "证书无效"
        #expect(head(empty) == HomeHeadline(kind: .noServer, title: "连不上服务器", sub: "证书无效", subError: true, details: true))
    }

    @Test func anAnswerToAnApprovalNamesItAndTheDecision() {
        let cmd = resolveApprovalCommand(sessionId: "s", approvalId: "A1", allow: false, message: "先别动", always: false)
        #expect(cmd.resolveApproval.sessionID == "s" && cmd.resolveApproval.approvalID == "A1")
        #expect(!cmd.resolveApproval.allow && cmd.resolveApproval.message == "先别动" && !cmd.resolveApproval.always)
    }
}
