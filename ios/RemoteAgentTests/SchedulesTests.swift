import Foundation
import RACore
import RAProto
import SwiftUI
import Testing
import UIKit
@testable import RemoteAgent

@MainActor @Suite struct ScheduleStoreTests {
    func task(_ id: String = "t") -> ScheduleTask {
        ScheduleTask(id: id, spec: ScheduleSpec(title: "检查 PR", message: "检查新回复，有变化时通知我", trigger: ScheduleTrigger(type: "interval"), target: ScheduleTarget(type: "new_session")))
    }
    func state(online: Bool = true, revision: UInt64 = 1) -> RemoteState {
        var state = RemoteState(); state.conn = online ? .online : .offline; state.link = online ? .online : .noServer
        var meta = PBMachineMeta(); meta.capabilities.scheduledTasks = true; meta.protocolVersion = protocolVersion; meta.scheduleRevision = revision
        state.machines["m"] = MachineView(id: "m", online: online, lastSeenMs: 0, meta: meta)
        return state
    }
    func settle(_ done: () -> Bool) async { for _ in 0..<100 where !done() { try? await Task.sleep(for: .milliseconds(10)) } }
    @Test func revisionChangesAndReconnectionsReadButUnrelatedStateDoesNotPoll() async {
        let store = ScheduleStore(); var reads = 0
        store.configure(.init(cached: { _ in nil }, list: { _ in reads += 1; return [self.task()] }))
        store.reconcile(state()); await settle { reads == 1 && store.loading.isEmpty }
        #expect(store.tasks.count == 1)
        for _ in 0..<4 { store.reconcile(state()) }
        await Task.yield(); #expect(reads == 1)
        store.reconcile(state(revision: 2)); await settle { reads == 2 && store.loading.isEmpty }
        store.reconcile(state(online: false, revision: 2)); #expect(!store.online("m") && store.tasks.count == 1)
        store.reconcile(state(revision: 2)); await settle { reads == 3 && store.loading.isEmpty }
        #expect(reads == 3)
    }
    @Test func offlineStartsWithCacheAndHidesUnsupportedMachines() async {
        let store = ScheduleStore(); var reads = 0
        store.configure(.init(cached: { _ in [self.task()] }, list: { _ in reads += 1; return [] }))
        store.reconcile(state(online: false)); await settle { store.loading.isEmpty }
        #expect(store.tasks.count == 1 && reads == 0 && !store.online("m"))
        var unsupported = state(online: false); unsupported.machines["m"]?.meta?.capabilities.scheduledTasks = false
        store.reconcile(unsupported); #expect(store.tasks.isEmpty)
    }
    @Test func readFailureKeepsSnapshotAndOnlyExplicitRetryReadsAgain() async {
        let store = ScheduleStore(); var reads = 0
        store.configure(.init(cached: { _ in [self.task()] }, list: { _ in reads += 1; throw UserError("测试读取失败") }))
        store.reconcile(state()); await settle { store.loading.isEmpty }
        #expect(store.tasks.count == 1 && store.errors["m"] != nil)
        store.reconcile(state()); await Task.yield(); #expect(reads == 1)
        await store.refresh("m"); #expect(reads == 2)
    }
    @Test func aRevisionArrivingDuringReadIsNotLost() async {
        let store = ScheduleStore(); var reads = 0
        store.configure(.init(cached: { _ in nil }, list: { _ in reads += 1; try await Task.sleep(for: .milliseconds(30)); return [self.task("\(reads)")] }))
        store.reconcile(state()); await Task.yield(); store.reconcile(state(revision: 2))
        await settle { reads == 2 && store.loading.isEmpty }
        #expect(reads == 2 && store.tasks.first?.task.id == "2")
    }
    @Test func draftsAreAccountScopedAndCurrentConversationTargetsDoNotCarryNewSessionSettings() throws {
        let context = AccountContext(account: Demo.account, remote: Demo.remote())
        let draft = context.scheduleDrafts.of("s"); draft.name = "  检查 PR "; draft.message = "检查新回复"
        draft.config = NewConfig(machineId: "m", cwd: "/example", agent: .claude, model: "haiku", mode: .full)
        #expect(context.scheduleDrafts.of("s") === draft)
        let s = SessionView(id: "s", machineId: "m")
        let spec = try #require(draft.spec(session: s, state: state()))
        #expect(spec.title == "检查 PR" && spec.target.session == "s" && spec.target.type == "resume_session")
        #expect(spec.target.model == nil && spec.target.mode == nil && spec.target.cwd == nil)
        context.scheduleDrafts.clear("s"); #expect(context.scheduleDrafts.of("s") !== draft)
    }
    @Test func creationKeyStaysStableForRetriesAndChangesWithTheDefinition() throws {
        let draft = ScheduleDraft(); draft.message = "检查"
        draft.config = NewConfig(machineId: "m", cwd: "/example")
        var spec = try #require(draft.spec(session: nil, state: state()))
        let first = draft.key(for: spec)
        #expect(draft.key(for: spec) == first)
        spec.message = "另一个请求"
        #expect(draft.key(for: spec) != first)
    }
    @Test func routesRoundtripAndScheduleTabClearsTheStack() {
        for route in [Route.schedules, .schedule(machine: "m", id: "t"), .newSchedule(), .newSchedule(session: "s")] { #expect(Route(path: route.path) == route) }
        let nav = Navigator(); nav.open(.session(id: "s")); nav.open(.schedules)
        #expect(nav.tab == .schedules && nav.path.isEmpty)
        nav.open(.schedule(machine: "m", id: "t")); nav.open(.schedule(machine: "m", id: "t"))
        #expect(nav.path.count == 1 && nav.tab == .schedules)
    }
    @Test func taskRowsRemainWithinNarrowAndTabletColumnsAtLargeTextSizes() {
        for width in [320.0, 760] {
            let row = SessionListRowContent(title: "检查新回复", right: "下次 15:00", third: "今天 4 次都没有通知", color: Theme.gray, last: false) { Text("每小时 · 在「修复登录超时」里").type(T.sm, Theme.muted) }
                .environment(\.dynamicTypeSize, .accessibility3)
            let host = UIHostingController(rootView: row)
            let size = host.sizeThatFits(in: CGSize(width: width, height: 1000))
            #expect(size.width <= width + 0.5 && size.height >= 44)
        }
    }
    @Test func builderKeepsNotificationAndScheduledCaptionVisibleAndFoldsSilentRounds() async throws {
        var events = [PBEvent]()
        for i in 0..<2 {
            let seq = UInt64(i * 3 + 1), run = "run-\(i)"
            var user = PBEvent(); user.seq = seq; user.time.seconds = 1_790_000_000 + Int64(i * 3600); user.userMessage.text = "检查"; user.origin.scheduledRunID = run
            var agent = PBEvent(); agent.seq = seq + 1; agent.agentMessage.text = "没有变化"
            var end = PBEvent(); end.seq = seq + 2; end.turnEnded.outcome = .completed; end.turnEnded.scheduledRunID = run
            events += [user, agent, end]
        }
        let rows = await ChatBuilder().build(PagedList(events), ChatParams()).rows
        #expect(rows.count == 1 && rows[0].folded.count >= 4)
        guard case .user(let first) = rows[0].folded[0].item else { Issue.record("scheduled user message absent"); return }
        #expect(first.from?.hasPrefix("定时任务 · ") == true && first.scheduledRunID == "run-0")
        var notification = PBEvent(); notification.seq = 7; notification.scheduledNotification.text = "有新回复"
        let notified = await ChatBuilder().build(PagedList(events + [notification]), ChatParams()).rows
        guard case .note(let note) = notified.last?.item else { Issue.record("notification is hidden"); return }
        #expect(note.text == "通知你：有新回复")
    }
}
