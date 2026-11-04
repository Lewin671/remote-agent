import Observation
import Foundation
import RACore
import RAProto
import SwiftUI
import Testing
import UIKit
@testable import RemoteAgent

@MainActor @Suite struct ScheduleStoreTests {
    @Test func creationUsesTheDisplayedFallbackAndRejectsAnOversizeDescription() throws {
        let draft = ScheduleDraft()
        draft.config = NewConfig(machineId: "m", cwd: "/example", agent: .claude, mode: .auto)
        draft.message = "\n每日检查：\n1. 检查构建。\n2. 汇报结果。"
        let spec = try #require(draft.spec(session: nil, state: state()))
        #expect(spec.title == "每日检查" && spec.message == draft.message && spec.notifyOnFinish)
        draft.notifyFinish = false
        #expect(draft.spec(session: nil, state: state())?.notify == "agent_only")
        draft.message = String(repeating: "中", count: 10923)
        #expect(draft.spec(session: nil, state: state()) == nil)
    }
    @Test func scheduledQueueCaptionDoesNotChangeOrdinaryPendingMessages() {
        var p = PendingInput(id: "q", text: "检查", mode: .queue, paused: false, held: false, origin: nil, time: 1800000000000)
        #expect(scheduledPendingLabel(p, label: "这一轮结束后发") == nil)
        p.origin = .with { $0.scheduledRunID = "run" }
        #expect(scheduledPendingLabel(p, label: "这一轮结束后发") == "定时任务 · " + scheduleTime(Date(timeIntervalSince1970: 1800000000)) + " · 这一轮结束后发")
        p.paused = true
        #expect(scheduledPendingLabel(p, label: "已暂停")?.hasSuffix(" · 已暂停") == true)
    }
    @Test func attentionIsCountedAndOnlyTheViewedTaskOrSessionIsRead() async throws {
        let store = ScheduleStore()
        var current = task()
        current.runs = [ScheduleRun(id: "old", startedAt: "", status: "completed", notified: true)]
        var other = task("other"); other.status = "uncertain"
        store.configure(.init(cached: { _ in nil }, list: { _ in [current, other] }))
        store.reconcile(state()); await eventually("initial list loaded") { store.loading.isEmpty && !store.tasks.isEmpty }
        #expect(store.badgeCount == 1) // Existing history is the baseline; attention still counts.
        current.runs! += [
            ScheduleRun(id: "a", startedAt: "", status: "completed", notified: true, sessionID: "shared"),
            ScheduleRun(id: "b", startedAt: "", status: "completed", notified: true, sessionID: "shared"),
            ScheduleRun(id: "silent", startedAt: "", status: "completed", sessionID: "shared")
        ]
        current.active = ScheduleRun(id: "c", startedAt: "", status: "running", notified: true, sessionID: "different")
        current.status = "failed"
        other.runs = [ScheduleRun(id: "same-session", startedAt: "", status: "completed", notified: true, sessionID: "shared")]
        await store.refresh("m")
        #expect(store.badgeCount == 2 && store.hasNewResults) // Only attention tasks count.
        store.setViewing(.schedules, foreground: true)
        #expect(store.badgeCount == 2)
        current.runs?.append(ScheduleRun(id: "tab-open", startedAt: "", status: "completed", notified: true))
        await store.refresh("m"); #expect(store.badgeCount == 2)
        store.setViewing(.session(id: "shared"), foreground: true)
        #expect(store.unreadRunIDs(machine: "m", task: current) == ["c", "tab-open"])
        #expect(store.unreadRunIDs(machine: "m", task: other).isEmpty)
        #expect(store.badgeCount == 2)
        store.setViewing(.schedule(machine: "m", id: "t"), foreground: true)
        #expect(store.badgeCount == 2) // Both attention tasks remain.
        #expect(store.detailUnread == ["c", "tab-open"])
        #expect(store.wasUnreadOnEntry(machine: "m", task: "t", run: "c"))
        #expect(!store.wasUnreadOnEntry(machine: "other", task: "t", run: "c"))
        current.runs?.append(ScheduleRun(id: "detail-open", startedAt: "", status: "completed", notified: true))
        await store.refresh("m")
        #expect(store.unreadRunIDs(machine: "m", task: current).isEmpty)
        #expect(store.detailUnread == ["c", "tab-open"]) // Arrival during the visit is read, without an entry dot.
        store.setViewing(.schedules, foreground: true)
        #expect(store.detailUnread.isEmpty)
        store.setViewing(.schedule(machine: "m", id: "t"), foreground: true)
        #expect(store.detailUnread.isEmpty)
        current.status = "scheduled"; other.status = "scheduled"
        store.setViewing(nil, foreground: true)
        await store.refresh("m"); #expect(store.badgeCount == 0)
        current.runs?.append(ScheduleRun(id: "later", startedAt: "", status: "completed", notified: true))
        await store.refresh("m"); #expect(store.badgeCount == 0 && store.hasNewResults)
        current.status = "cancelled"; current.reason = "user_cancelled"
        await store.refresh("m"); #expect(store.badgeCount == 0)
        store.configure(.init(cached: { _ in nil }, list: { _ in [] }))
        await store.refresh("m"); #expect(store.badgeCount == 0)
    }
    @Test func viewingSessionConsumesNewRunsButBackgroundDoesNot() async {
        let store = ScheduleStore()
        var current = task(); current.spec.target.type = "resume_session"; current.spec.target.session = "shared"
        store.configure(.init(cached: { _ in nil }, list: { _ in [current] }))
        store.reconcile(state()); await eventually("loaded") { store.loading.isEmpty }
        store.setViewing(.session(id: "shared"), foreground: true)
        current.runs = [ScheduleRun(id: "a", startedAt: "", status: "completed", notified: true, sessionID: "shared")]
        await store.refresh("m"); #expect(store.badgeCount == 0)
        store.setViewing(.session(id: "shared"), foreground: false)
        current.runs?.append(ScheduleRun(id: "b", startedAt: "", status: "completed", notified: true, sessionID: "shared"))
        await store.refresh("m"); #expect(store.badgeCount == 0 && store.hasNewResults)
        store.setViewing(.session(id: "shared"), foreground: true)
        #expect(store.badgeCount == 0)
        current.active = ScheduleRun(id: "c", startedAt: "", status: "running", notified: true, sessionID: "shared")
        await store.refresh("m"); #expect(store.badgeCount == 0)
    }
    @Test func anUnloadedDetailCapturesUnreadAfterCachedHistoryArrives() async {
        let store = ScheduleStore()
        var current = task()
        store.setViewing(.schedule(machine: "m", id: "t"), foreground: true)
        store.configure(.init(cached: { _ in [] }, list: { _ in
            current.runs = [ScheduleRun(id: "new", startedAt: "", status: "completed", notified: true)]
            return [current]
        }))
        store.reconcile(state()); await eventually("detail loaded") { store.loading.isEmpty }
        #expect(store.badgeCount == 0 && store.detailUnread == ["new"])
    }
    @Test func newResultsAreBooleanAndDetailReadsAreMachineScoped() async {
        let store = ScheduleStore()
        var current = task()
        var state = state(); state.currentMachines.insert("second")
        state.machines["second"] = MachineView(id: "second", online: true, lastSeenMs: 0, meta: state.machines["m"]?.meta)
        store.configure(.init(cached: { _ in [] }, list: { _ in [current] }))
        store.reconcile(state); await eventually("both computers loaded") { store.loading.isEmpty }
        current.runs = (0..<100).map { ScheduleRun(id: "r-\($0)", startedAt: "", status: "completed", notified: true) }
        await store.refresh("m"); await store.refresh("second")
        #expect(store.badgeCount == 0 && store.hasNewResults)
        store.setViewing(.schedule(machine: "m", id: "t"), foreground: true)
        #expect(store.badgeCount == 0 && store.hasNewResults && store.detailUnread.count == 100)
        #expect(store.unreadRunIDs(machine: "second", task: current).count == 100)
        var offline = state; offline.conn = .offline
        store.reconcile(offline)
        #expect(store.badgeCount == 0 && store.hasNewResults)
        offline.currentMachines.remove("second"); store.reconcile(offline)
        #expect(store.badgeCount == 0)
    }
    @Test func unreadBadgePersistsAcrossLaunchAndIsolatesRelayAccountAndMachine() async throws {
        let suite = "schedule-badge-test-" + UUID().uuidString
        let defaults = try #require(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        func make(_ scope: [String], _ list: [ScheduleTask]) async -> ScheduleStore {
            let store = ScheduleStore(defaults: defaults, scope: scope)
            store.configure(.init(cached: { _ in nil }, list: { _ in list }))
            store.reconcile(state())
            await eventually("read settled") { store.loading.isEmpty }
            return store
        }
        let scope = ["relay-a", "client-a"]
        _ = await make(scope, []) // A real empty list establishes a baseline.
        var current = task()
        current.runs = [ScheduleRun(id: "r", startedAt: "", status: "completed", notified: true)]
        let reload = await make(scope, [current])
        #expect(reload.badgeCount == 0 && reload.hasNewResults)
        for scope in [["relay-b", "client-a"], ["relay-a", "client-b"]] {
            let other = await make(scope, [current]); #expect(other.badgeCount == 0)
        }
        var otherState = state()
        otherState.currentMachines = ["other-machine"]
        let removed = otherState.machines.removeValue(forKey: "m")
        let machine = try #require(removed)
        otherState.machines["other-machine"] = MachineView(id: "other-machine", online: true, lastSeenMs: 0, meta: machine.meta)
        let other = ScheduleStore(defaults: defaults, scope: scope)
        other.configure(.init(cached: { _ in [current] }, list: { _ in [current] }))
        other.reconcile(otherState); await eventually("other machine loaded") { other.loading.isEmpty }
        #expect(other.badgeCount == 0)
        reload.setViewing(.schedule(machine: "m", id: "t"), foreground: true)
        let seen = await make(scope, [current]); #expect(seen.badgeCount == 0)
    }

    @Test func detailFreezesResultAndAddsBackgroundDotsWhenReturning() async {
        let store = ScheduleStore()
        var current = task()
        store.configure(.init(cached: { _ in [] }, list: { _ in [current] }))
        store.reconcile(state()); await eventually("loaded") { store.loading.isEmpty }
        current.runs = [ScheduleRun(id: "first", startedAt: "2026-10-30T08:00:00Z", status: "completed", notified: true)]
        await store.refresh("m")
        store.setViewing(.schedule(machine: "m", id: "t"), foreground: true)
        #expect(store.detailResult?.id == "first" && store.detailUnread == ["first"] && !store.hasNewResults)
        current.runs?.append(ScheduleRun(id: "second", startedAt: "2026-10-31T08:00:00Z", status: "completed", notified: true))
        await store.refresh("m")
        #expect(store.detailResult?.id == "first" && store.detailUnread == ["first"] && !store.hasNewResults)
        store.setViewing(.schedule(machine: "m", id: "t"), foreground: false)
        current.runs?.append(ScheduleRun(id: "third", startedAt: "2026-11-01T08:00:00Z", status: "completed", notified: true))
        await store.refresh("m"); #expect(store.hasNewResults)
        store.setViewing(.schedule(machine: "m", id: "t"), foreground: true)
        #expect(store.detailUnread == ["first", "third"] && !store.hasNewResults)
        #expect(store.detailResult?.id == "first")
        store.refreshDetailResult(machine: "m", task: "t")
        #expect(store.detailResult?.id == "third")
    }
    @Test func foregroundCatchupAddsDotsEvenWhenTheListWasNotReadInBackground() async {
        let store = ScheduleStore(); var current = task()
        store.configure(.init(cached: { _ in [] }, list: { _ in [current] }))
        store.reconcile(state()); await eventually("loaded") { store.loading.isEmpty }
        store.setViewing(.schedule(machine: "m", id: "t"), foreground: true)
        store.setViewing(.schedule(machine: "m", id: "t"), foreground: false)
        current.runs = [ScheduleRun(id: "while-away", startedAt: scheduleISO(Date().addingTimeInterval(-30)), status: "completed", notified: true)]
        store.setViewing(.schedule(machine: "m", id: "t"), foreground: true)
        await store.refresh("m")
        #expect(store.detailUnread == ["while-away"] && !store.hasNewResults && store.detailResult == nil)
        store.refreshDetailResult(machine: "m", task: "t")
        #expect(store.detailResult?.id == "while-away")
    }
    @Test func cachedDetailWaitsForFirstCompleteListAndResolvesSelectedRunFromLiveData() async {
        let store = ScheduleStore()
        var cached = task()
        cached.runs = [ScheduleRun(id: "old", startedAt: "2026-10-30T08:00:00Z", status: "completed", notified: true)]
        var complete: CheckedContinuation<[ScheduleTask], Never>?
        store.configure(.init(cached: { _ in [cached] }, list: { _ in await withCheckedContinuation { complete = $0 } }))
        store.setViewing(.schedule(machine: "m", id: "t"), foreground: true)
        store.reconcile(state())
        await eventually("cache loaded; complete list pending") { complete != nil && !store.tasks.isEmpty }
        #expect(store.detailResultID == nil && store.detailResult?.id == "old" && store.detailUnread.isEmpty && !store.hasNewDetailResult)
        store.setViewing(nil, foreground: true)
        store.setViewing(.schedule(machine: "m", id: "t"), foreground: true)
        #expect(store.detailResultID == nil && !store.hasNewDetailResult)
        var current = cached
        current.runs?.append(ScheduleRun(id: "latest", startedAt: "2026-10-31T08:00:00Z", status: "completed", notified: true))
        complete?.resume(returning: [current])
        await eventually("complete list captured") { store.loading.isEmpty }
        #expect(store.detailResultID == "latest" && store.detailResult?.id == "latest")
        #expect(store.detailUnread == ["latest"] && !store.hasNewDetailResult && !store.hasNewResults)
        current.runs?.append(ScheduleRun(id: "newer", startedAt: "2026-11-01T08:00:00Z", status: "completed", notified: true))
        store.configure(.init(cached: { _ in nil }, list: { _ in [current] }))
        await store.refresh("m")
        #expect(store.hasNewDetailResult && store.detailResultID == "latest" && store.detailUnread == ["latest"])
        current.runs?[1].finalSummary = "后补的摘要"
        current.runs?[1].endedAt = "2026-10-31T08:03:00Z"
        await store.refresh("m")
        #expect(store.detailResultID == "latest" && store.detailResult?.finalSummary == "后补的摘要")
        #expect(store.detailResult?.notificationTime == scheduleDate("2026-10-31T08:03:00Z"))
        store.refreshDetailResult(machine: "m", task: "t")
        #expect(store.detailResultID == "newer" && !store.hasNewDetailResult)
    }
    @Test func offlineCacheDoesNotConsumeUnreadOrCaptureUntilOnlineListArrives() async throws {
        let suite = "schedule-detail-cache-test-" + UUID().uuidString
        let defaults = try #require(UserDefaults(suiteName: suite)); defer { defaults.removePersistentDomain(forName: suite) }
        let baseline = ScheduleStore(defaults: defaults)
        baseline.configure(.init(cached: { _ in nil }, list: { _ in [] }))
        baseline.reconcile(state()); await eventually("baseline") { baseline.loading.isEmpty }
        var cached = task(); cached.runs = [ScheduleRun(id: "unread", startedAt: "2026-10-31T08:00:00Z", status: "completed", notified: true)]
        let store = ScheduleStore(defaults: defaults)
        store.configure(.init(cached: { _ in [cached] }, list: { _ in [cached] }))
        store.reconcile(state(online: false)); await eventually("offline cache") { store.loading.isEmpty }
        store.setViewing(.schedule(machine: "m", id: "t"), foreground: true)
        #expect(store.hasNewResults && store.detailUnread.isEmpty && store.detailResultID == nil && !store.hasNewDetailResult)
        store.reconcile(state()); await eventually("online list") { store.loading.isEmpty }
        #expect(store.detailUnread == ["unread"] && store.detailResultID == "unread" && !store.hasNewResults && !store.hasNewDetailResult)
    }
    @Test(arguments: [false, true])
    func cachedResultsStayVisibleAndUnreadWhileOfflineOrCompleteReadFails(online: Bool) async throws {
        let suite = "schedule-cached-result-test-" + UUID().uuidString
        let defaults = try #require(UserDefaults(suiteName: suite)); defer { defaults.removePersistentDomain(forName: suite) }
        let baseline = ScheduleStore(defaults: defaults)
        baseline.configure(.init(cached: { _ in nil }, list: { _ in [] }))
        baseline.reconcile(state()); await eventually("seen baseline established") { baseline.loading.isEmpty }
        var cached = task()
        cached.runs = [ScheduleRun(id: "older", startedAt: "2026-10-30T08:00:00Z", status: "completed", notified: true),
                       ScheduleRun(id: "latest", startedAt: "2026-10-31T08:00:00Z", status: "completed", notified: true, sessionID: "cached-session")]
        cached.runs?[1].finalSummary = "缓存里的最终摘要"
        let store = ScheduleStore(defaults: defaults)
        store.configure(.init(cached: { _ in [cached] }, list: { _ in throw UserError("测试读取失败") }))
        store.setViewing(.schedule(machine: "m", id: "t"), foreground: true)
        store.reconcile(state(online: online)); await eventually("cached results loaded") { store.loading.isEmpty }
        #expect((store.errors["m"] != nil) == online)
        #expect(store.detailResult?.id == "latest" && store.detailResult?.sessionID == "cached-session")
        #expect(store.detailResult?.finalSummary == "缓存里的最终摘要" && store.detailResultID == nil)
        #expect(store.detailUnread.isEmpty && !store.wasUnreadOnEntry(machine: "m", task: "t", run: "latest"))
        #expect(store.unreadRunIDs(machine: "m", task: cached) == ["older", "latest"] && !store.hasNewDetailResult)
        #expect(cached.earlierRuns(resultID: store.detailResult?.id).map(\.id) == ["older"])
        store.setViewing(.schedule(machine: "m", id: "t"), foreground: true)
        #expect(store.unreadRunIDs(machine: "m", task: cached) == ["older", "latest"])
        store.configure(.init(cached: { _ in nil }, list: { _ in [cached] }))
        store.reconcile(state()); await store.refresh("m")
        #expect(store.detailResultID == "latest" && store.detailUnread == ["older", "latest"])
        #expect(!store.hasNewDetailResult && !store.hasNewResults)
    }
    @Test func manualUnreadSurvivesReturnToItsSessionAndRefreshesUntilLeaving() async {
        let store = ScheduleStore(); var current = task()
        current.spec.target.type = "resume_session"; current.spec.target.session = "shared"
        current.runs = [ScheduleRun(id: "result", startedAt: "2026-10-31T08:00:00Z", status: "completed", notified: true, sessionID: "shared")]
        store.configure(.init(cached: { _ in nil }, list: { _ in [current] }))
        store.reconcile(state()); await eventually("loaded") { store.loading.isEmpty }
        let session = Route.session(id: "shared"), detail = Route.schedule(machine: "m", id: "t")
        let nav = Navigator(); nav.path = [session]
        store.setViewing(session, foreground: true)
        nav.open(detail); store.setViewing(detail, foreground: true)
        markScheduleUnreadAndBack(machine: "m", taskID: "t", store: store, nav: nav, page: detail)
        #expect(nav.path == [session])
        store.setViewing(detail, foreground: true) // A repeated visibility callback during dismissal must not clear protection.
        store.setViewing(nav.path.last, foreground: true)
        await store.refresh("m"); store.setViewing(session, foreground: true)
        #expect(store.unreadRunIDs(machine: "m", task: current) == ["result"])
        current.runs?.append(ScheduleRun(id: "new-result", startedAt: "2026-11-01T08:00:00Z", status: "completed", notified: true, sessionID: "shared"))
        await store.refresh("m")
        #expect(store.unreadRunIDs(machine: "m", task: current) == ["result"]) // Only the explicitly unread result is protected.
        store.setViewing(.schedules, foreground: true)
        #expect(store.hasNewResults)
        store.setViewing(session, foreground: true)
        #expect(!store.hasNewResults)
    }
    @Test func manualUnreadInDetailSurvivesRefreshButEndsOnBackgroundOrAnotherPage() async {
        let store = ScheduleStore(); var current = task()
        current.runs = [ScheduleRun(id: "result", startedAt: "2026-10-31T08:00:00Z", status: "completed", notified: true)]
        store.configure(.init(cached: { _ in nil }, list: { _ in [current] }))
        store.reconcile(state()); await eventually("loaded") { store.loading.isEmpty }
        let detail = Route.schedule(machine: "m", id: "t")
        store.setViewing(detail, foreground: true)
        let entry = MachineSchedule(machineID: "m", task: current)
        store.markUnread(entry)
        store.setViewing(detail, foreground: true); await store.refresh("m")
        #expect(store.hasNewResults)
        store.markRead([entry]); await store.refresh("m")
        #expect(!store.hasNewResults) // Explicit read still wins while staying here.
        store.markUnread(entry)
        store.setViewing(detail, foreground: false); await store.refresh("m")
        #expect(store.hasNewResults)
        store.setViewing(detail, foreground: true)
        #expect(!store.hasNewResults)
        store.markUnread(entry); await store.refresh("m")
        #expect(store.hasNewResults)
        store.setViewing(.session(id: "other"), foreground: true)
        store.setViewing(detail, foreground: true)
        #expect(!store.hasNewResults)
    }
    @Test func delayedUnreadActionResolvesTheNewestResultAtExecutionTime() async {
        let store = ScheduleStore(); var current = task()
        current.runs = [ScheduleRun(id: "old", startedAt: "2026-10-31T08:00:00Z", status: "completed", notified: true, sessionID: "shared")]
        store.configure(.init(cached: { _ in nil }, list: { _ in [current] }))
        store.reconcile(state()); await eventually("loaded") { store.loading.isEmpty }
        let detail = Route.schedule(machine: "m", id: "t"), session = Route.session(id: "shared")
        let nav = Navigator(); nav.path = [session, detail]
        store.setViewing(detail, foreground: true)
        let menuTaskID = current.id // Captured when the menu was rendered, before the new result.
        current.runs?.append(ScheduleRun(id: "newest", startedAt: "2026-11-01T08:00:00Z", status: "completed", notified: true, sessionID: "shared"))
        await store.refresh("m"); #expect(!store.hasNewResults)
        markScheduleUnreadAndBack(machine: "m", taskID: menuTaskID, store: store, nav: nav, page: detail)
        store.setViewing(nav.path.last, foreground: true); await store.refresh("m")
        #expect(store.unreadRunIDs(machine: "m", task: current) == ["newest"])
        store.setViewing(.schedules, foreground: true); store.setViewing(session, foreground: true)
        #expect(!store.hasNewResults)
    }
    @Test func recoveryPlacementKeepsMissingTargetsInMenuAndOmitsDuplicateOneShotRecovery() {
        var once = task(); once.status = "failed"; once.spec.trigger.type = "at"
        func access(_ t: ScheduleTask) -> ScheduleEditAccess { ScheduleEditAccess(task: t, online: true, saving: false, unknown: false) }
        #expect(access(once).primaryRecovery == "立即运行一次" && access(once).menuToggle == nil)
        var recurring = once; recurring.spec.trigger.type = "interval"
        #expect(access(recurring).primaryRecovery == "恢复" && access(recurring).menuToggle == nil)
        recurring.spec.target.type = "resume_session"; recurring.reason = "execution_session_missing"
        #expect(access(recurring).primaryRecovery == nil && access(recurring).menuToggle == "恢复")
        #expect(scheduleStopBanner(recurring).contains("恢复后还会失败"))
        var paused = task(); paused.status = "paused"; paused.reason = "user_paused"
        #expect(access(paused).primaryRecovery == nil && access(paused).menuToggle == "恢复")
        #expect(access(task()).menuToggle == "暂停")
        var ended = task(); ended.status = "completed"; #expect(access(ended).menuToggle == nil)
    }
    @Test func markingUnreadReturnsToTheCallingPageAndPreservesItsStack() async {
        let store = ScheduleStore(); var current = task()
        current.runs = [ScheduleRun(id: "result", startedAt: "2026-10-31T08:00:00Z", status: "completed", notified: true)]
        store.configure(.init(cached: { _ in nil }, list: { _ in [current] }))
        store.reconcile(state()); await eventually("loaded") { store.loading.isEmpty }
        let entry = MachineSchedule(machineID: "m", task: current), detail = Route.schedule(machine: "m", id: "t")
        let prefixes: [[Route]] = [[], [.session(id: "calling-session")], [.devices, .session(id: "calling-session")]]
        for prefix in prefixes {
            let nav = Navigator(); nav.path = prefix + [detail]
            store.markRead([entry])
            markScheduleUnreadAndBack(machine: entry.machineID, taskID: entry.task.id, store: store, nav: nav, page: detail)
            #expect(nav.path == prefix && store.hasNewResult(entry))
        }
        let nav = Navigator(); nav.path = [detail, .session(id: "other")]
        store.markRead([entry])
        markScheduleUnreadAndBack(machine: entry.machineID, taskID: entry.task.id, store: store, nav: nav, page: detail)
        #expect(nav.path == [detail, .session(id: "other")] && !store.hasNewResult(entry))
    }
    @Test func staleCacheCannotPruneThePersistedSeenSet() async throws {
        let suite = "schedule-cache-test-" + UUID().uuidString
        let defaults = try #require(UserDefaults(suiteName: suite)); defer { defaults.removePersistentDomain(forName: suite) }
        var current = task(); current.runs = [ScheduleRun(id: "known", startedAt: "", status: "completed", notified: true)]
        let first = ScheduleStore(defaults: defaults, scope: ["test"])
        first.configure(.init(cached: { _ in nil }, list: { _ in [current] }))
        first.reconcile(state()); await eventually("first loaded") { first.loading.isEmpty }
        let reloaded = ScheduleStore(defaults: defaults, scope: ["test"])
        reloaded.configure(.init(cached: { _ in [] }, list: { _ in [current] }))
        reloaded.reconcile(state()); await eventually("reloaded") { reloaded.loading.isEmpty }
        #expect(!reloaded.hasNewResults)
    }
    @Test func readActionsOnlyAffectTheirGroupAndUnreadOnlyTheLatestResult() async {
        let store = ScheduleStore(); let recent = task("recent"); var stopped = task("stopped"), ended = task("ended")
        stopped.status = "failed"; ended.status = "completed"
        var current = [recent, stopped, ended]
        store.configure(.init(cached: { _ in [] }, list: { _ in current }))
        store.reconcile(state()); await eventually("loaded") { store.loading.isEmpty }
        for index in current.indices {
            current[index].runs = [ScheduleRun(id: "old", startedAt: "2026-10-30T08:00:00Z", status: "completed", notified: true),
                                   ScheduleRun(id: "latest", startedAt: "2026-10-31T08:00:00Z", status: "completed", notified: true)]
        }
        await store.refresh("m")
        #expect(store.rows(.recent).count == 2 && store.rows(.ended).isEmpty && store.badgeCount == 1)
        store.markRead(store.rows(.recent))
        #expect(store.rows(.ended).count == 1 && store.hasNewResult(store.rows(.attention)[0]))
        let entry = store.rows(.recent)[0]; store.markUnread(entry)
        #expect(store.unreadRunIDs(machine: "m", task: entry.task) == ["latest"])
    }
    @Test func completeListPrunesSeenIncludingHiddenTasksAndPersistedHistory() async throws {
        let suite = "schedule-prune-test-" + UUID().uuidString
        let defaults = try #require(UserDefaults(suiteName: suite)); defer { defaults.removePersistentDomain(forName: suite) }
        let store = ScheduleStore(defaults: defaults, scope: ["test"])
        var current = task(); current.runs = [ScheduleRun(id: "old", startedAt: "", status: "completed", notified: true)]
        var list = [current]
        store.configure(.init(cached: { _ in nil }, list: { _ in list }))
        store.reconcile(state()); await eventually("loaded") { store.loading.isEmpty }
        current.status = "cancelled"; current.reason = "user_cancelled"; list = [current]
        await store.refresh("m")
        #expect((defaults.persistentDomain(forName: suite) ?? [:]).values.compactMap { $0 as? [String] }.flatMap { $0 }.count == 1)
        list = []; await store.refresh("m")
        #expect((defaults.persistentDomain(forName: suite) ?? [:]).values.compactMap { $0 as? [String] }.flatMap { $0 }.isEmpty)
        list = [task()]; list[0].runs = current.runs; await store.refresh("m")
        #expect(store.hasNewResults) // Deleted seen token did not survive removal.
    }

    func task(_ id: String = "t") -> ScheduleTask {
        ScheduleTask(id: id, spec: ScheduleSpec(title: "检查 PR", message: "检查新回复，有变化时通知我", trigger: ScheduleTrigger(type: "interval"), target: ScheduleTarget(type: "new_session")))
    }
    func state(online: Bool = true, revision: UInt64 = 1) -> RemoteState {
        var state = RemoteState(); state.conn = online ? .online : .offline; state.link = online ? .online : .noServer
        var meta = PBMachineMeta(); meta.capabilities.scheduledTasks = true; meta.protocolVersion = protocolVersion; meta.scheduleRevision = revision
        state.currentMachines = ["m"]
        state.machines["m"] = MachineView(id: "m", online: online, lastSeenMs: 0, meta: meta)
        return state
    }
    func settle(_ done: () -> Bool) async { for _ in 0..<100 where !done() { try? await Task.sleep(for: .milliseconds(10)) } }
    @Test func revokedArchiveDropsTasksBadgeAndPendingRead() async {
        let store=ScheduleStore();var attention=task();attention.status="paused";attention.reason="agent_schedule_permissions_changed"
        var release:CheckedContinuation<Void,Never>?;var returned=false
        store.configure(.init(cached:{_ in [attention]},list:{_ in await withCheckedContinuation { release=$0 };returned=true;return [attention]}))
        store.reconcile(state());await eventually("list is suspended") { release != nil }
        #expect(store.tasks.count == 1)
        var removed=state(online:false);removed.currentMachines=[] // archived metadata is deliberately retained
        store.reconcile(removed)
        #expect(store.tasks.isEmpty && store.loading.isEmpty && !store.online("m"))
        release?.resume();await eventually("revoked read returned") { returned }
        #expect(store.tasks.isEmpty)
        let cold=ScheduleStore();var restores=0
        cold.configure(.init(cached:{_ in restores += 1;return [attention]},list:{_ in []}))
        cold.reconcile(removed);await eventually("revoked cold store settled") { cold.loading.isEmpty }
        #expect(cold.tasks.isEmpty && restores == 0)
    }
    @Test(arguments: [[PBAgent.codex], [.codex,.claude]])
    func switchingCreationComputerUsesOnlyItsDisplayedDefaults(agents: [PBAgent]) throws {
        var state=self.state();state.currentMachines.insert("new");let path="/example/project"
        state.machines["new"]=MachineView(id:"new",online:true,lastSeenMs:0,meta:PBMachineMeta.with { $0.capabilities.scheduledTasks=true;$0.protocolVersion=protocolVersion;$0.agents=agents.map { agent in .with { $0.agent=agent;$0.defaultMode = .ask;$0.defaultModeSource = .cliConfig } } })
        state.prefs=PrefsObject(revision:1,prefs:PBProjectPrefs.with { $0.settings=[PBProjectSettings.with { $0.path=path;$0.machineID="m";$0.agents=[PBAgent.codex,.claude].map { agent in .with { $0.agent=agent;$0.model="old-model";$0.effort="high";$0.mode = .fullAccess } } }] })
        let draft=ScheduleDraft();draft.message="检查回复";draft.config=NewConfig(machineId:"m",cwd:path,agent:.claude,model:"old-model",effort:"high",mode:.full)
        let binding=Binding<NewConfig?>(get:{draft.config},set:{draft.config=$0})
        let sheet=ScheduleConfigSheet(page:.machine,config:binding,onClose:{})
        sheet.selectMachine("new",state:state)
        let selected=try #require(draft.config), agent:PBAgent=agents.contains(.claude) ? .claude:.codex
        #expect(selected == NewConfig(machineId:"new",cwd:path,agent:agent))
        #expect(effectiveModeText(chosen:selected.mode,default:defaultModeOf(state.machines["new"],agent:agent)) == "询问")
        let target=try #require(draft.spec(session:nil,state:state)).target
        #expect(target.agent == (agent == .claude ? "claude":"codex") && target.model == nil && target.effort == nil && target.mode == "ask")
    }
    @Test func creationResolvesKnownDefaultsAndRequiresAnExplicitChoiceWhenUnknown() {
        let draft=ScheduleDraft();draft.message="检查";draft.config=NewConfig(machineId:"m",cwd:"/example",agent:.claude)
        var known=state();known.machines["m"]?.meta?.agents=[PBAgentInfo.with { $0.agent = .claude;$0.defaultMode = .auto;$0.defaultModeSource = .cliConfig }]
        #expect(draft.spec(session:nil,state:known)?.target.mode == "auto")
        #expect(draft.spec(session:nil,state:state()) == nil)
        #expect(draft.creationIssue(session:nil,state:state()) == "请选择明确的权限，再创建定时任务。")
    }
    @Test func targetResolutionDoesNotHoldTheListOrRefreshSlot() async {
        let store=ScheduleStore();var reads=0;var hold:CheckedContinuation<Void,Never>?;var finished=false
        var task=task();task.spec.target=ScheduleTarget(type:"resume_session");task.spec.target.session="unloaded"
        store.configure(.init(cached:{_ in nil},list:{_ in reads += 1;return [task]},resolve:{_ in if hold == nil { await withCheckedContinuation { hold=$0 } }}))
        store.reconcile(state());await eventually("target resolution entered") { hold != nil }
        let refresh=Task { await store.refresh("m");finished=true }
        await eventually("target does not block refresh") { finished }
        #expect(finished && reads == 2 && store.loading.isEmpty)
        hold?.resume();await refresh.value
    }
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
    @Test func planDefaultsRemainPlanAndCannotCreateAnExecutionTask() {
        let draft=ScheduleDraft();draft.message="检查";draft.config=NewConfig(machineId:"m",cwd:"/example",mode:.plan)
        #expect(draft.spec(session:nil,state:state())==nil)
        #expect(draft.creationIssue(session:nil,state:state())=="计划模式不能创建执行任务，请先选择其他权限")
        #expect(draft.spec(session:SessionView(id:"s",machineId:"m"),state:state())?.target.type=="resume_session")
    }
    @Test func anEditorKeepsItsOpeningRevisionAndCompleteSpecAcrossARefresh() throws {
        let original=task();let editor=ScheduleEditDraft();editor.open(original)
        var remote=original;remote.revision=9;remote.spec.title="另一端改名"
        var edited=try #require(editor.task?.spec);edited.message="本端消息"
        let command=try editor.command(spec:edited)
        #expect(command.schedule.expectedRevision==original.revision)
        #expect(try JSONDecoder().decode(ScheduleSpec.self,from:command.schedule.specJson).title==original.spec.title)
        #expect(editor.task?.revision != remote.revision)
    }
    @Test func concurrentManualAndRevisionReadsShareOneMachineSlot() async {
        let store = ScheduleStore(); var started = 0, active = 0, peak = 0, entered = 0
        var holds = [Int: CheckedContinuation<Void, Never>]()
        store.configure(.init(cached: { _ in nil }, list: { _ in
            started += 1; let request = started
            active += 1; peak = max(peak, active)
            defer { active -= 1 }
            if request <= 2 { await withCheckedContinuation { holds[request] = $0 } }
            return [self.task("r\(request)")]
        }))
        store.reconcile(state())
        await eventually("first read is suspended") { holds[1] != nil }
        let first = Task { entered += 1; await store.refresh("m") }
        let second = Task { entered += 1; await store.refresh("m") }
        store.reconcile(state(revision: 2))
        await eventually("both manual refreshes entered") { entered == 2 }
        #expect(started == 1 && peak == 1)
        holds.removeValue(forKey: 1)?.resume()
        await eventually("revision catch-up is suspended") { holds[2] != nil }
        holds.removeValue(forKey: 2)?.resume()
        await first.value; await second.value
        #expect(peak == 1 && started >= 3 && store.tasks.first?.task.id == "r\(started)")
    }
    @Test func creationKeyStaysStableForRetriesAndChangesWithTheDefinition() throws {
        let draft = ScheduleDraft(); draft.message = "检查"
        draft.config = NewConfig(machineId: "m", cwd: "/example", mode: .ask)
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
        let context = AccountContext(account: Demo.account, remote: Demo.remote())
        let settings = SettingsStore(defaults: UserDefaults(suiteName: "schedule-layout-" + UUID().uuidString)!)
        let model = AppModel(settings: settings)
        var task = task(); task.spec.title = String(repeating: "很长的任务名", count: 12)
        task.spec.target.cwd = "/example/project"
        task.runs = [ScheduleRun(id: "new", startedAt: "2026-10-31T08:00:00Z", status: "completed", notified: true)]
        task.runs?[0].notificationText = String(repeating: "很长的结果摘要", count: 20)
        let entry = MachineSchedule(machineID: "m", task: task)
        context.schedules.show([], state: state()); context.schedules.show([entry], state: state())
        for width in [320.0, 760] {
            for scheme in [ColorScheme.light, .dark] {
                let row = ScheduleListRow(entry: entry, now: 1793520000000, last: false)
                    .environment(context).environment(model).environment(Navigator())
                    .environment(\.dynamicTypeSize, .accessibility3).environment(\.colorScheme, scheme)
                let host = UIHostingController(rootView: row)
                let size = host.sizeThatFits(in: CGSize(width: width, height: 1000))
                #expect(size.width <= width + 0.5 && size.height >= 44 && size.height < 500)
            }
        }
    }
    @Test func firstRunMetadataAndPreviewDoNotRepeatRunningText() {
        var current = task()
        current.active = ScheduleRun(id: "first", startedAt: "2026-10-31T08:00:00Z", status: "running")
        let entry = MachineSchedule(machineID: "m", task: current)
        #expect(current.listPreview() == "还没有结果")
        #expect(scheduleListMetadata(entry, entries: [entry], state: state(), online: true).hasPrefix("正在运行 · "))
        #expect(current.latestNotification == nil)
    }
    @Test func recoveryNoticeFitsTheSameNarrowAndWideContentColumn() {
        let minimumHeight: CGFloat = Theme.minHit + 32 + 12 + 28
        for offered in [320.0, 760, 1200] {
            for scheme in [ColorScheme.light, .dark] {
                let notice = ScheduleRecoveryNotice(text: String(repeating: "创建任务时的权限已改变，任务已停下。", count: 4), recovery: "恢复", enabled: true, action: {})
                    .environment(\.onSoft, true).environment(\.colorScheme, scheme)
                    .environment(\.dynamicTypeSize, .accessibility3)
                    .frame(maxWidth: Theme.readableWidth)
                let host = UIHostingController(rootView: notice)
                let size = host.sizeThatFits(in: CGSize(width: offered, height: 3000))
                #expect(abs(size.width - min(offered, Theme.readableWidth)) <= 0.5)
                #expect(size.height >= minimumHeight)
            }
        }
    }
    @Test func navigationObserverKeepsListUnreadAndReadsTheSelectedDetail() async throws {
        await WindowTestGate.shared.acquire(); defer { WindowTestGate.shared.release() }
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let nav = Navigator(), store = ScheduleStore()
        var current = task()
        store.configure(.init(cached: { _ in [] }, list: { _ in [current] }))
        store.reconcile(state()); await eventually("baseline loaded") { store.loading.isEmpty }
        current.runs = [ScheduleRun(id: "new", startedAt: "", status: "completed", notified: true)]
        await store.refresh("m")
        struct Host: View {
            let nav: Navigator
            let store: ScheduleStore
            var body: some View { Text("navigation").modifier(ScheduleViewingObserver(store: store, page: nav.path.last, foreground: true)) }
        }
        let host = UIHostingController(rootView: Host(nav: nav, store: store))
        let window = UIWindow(windowScene: scene); window.rootViewController = host; window.isHidden = false
        defer { window.isHidden = true }
        nav.open(.schedules)
        host.view.layoutIfNeeded()
        for _ in 0..<5 { await Task.yield() }
        #expect(store.hasNewResults)
        nav.open(.schedule(machine: "m", id: "t"))
        await eventually("navigation reads detail") { !store.hasNewResults }
        #expect(store.detailUnread == ["new"])
        nav.open(.session(id: "s"))
        await eventually("leaving detail removes entry dots") { store.detailUnread.isEmpty }
    }
    @Test func tabBadgeCapsCountsAndWholeBlocksFitWithoutClipping() {
        let conversation = TabBarItem(id: "home", label: "会话", icon: Lucide.messageSquare, badge: 100)
        let schedules = TabBarItem(id: "schedules", label: "定时", icon: Lucide.clock, badge: 100, scheduleBadge: true)
        let newResult = TabBarItem(id: "schedules", label: "定时", icon: Lucide.clock, dot: true, scheduleBadge: true)
        #expect(conversation.badgeText == "100" && conversation.badgeAccessibilityValue == "100 个需要处理")
        #expect(schedules.badgeText == "99+" && schedules.badgeAccessibilityValue == "100 件需要处理")
        #expect(newResult.badgeAccessibilityValue == "有新结果")
        #expect(tabBadgeText(99) == "99")
        #expect(scheduleResultBlockCount([24, 48, 96], budget: 192) == 2)
        #expect(scheduleResultBlockCount([240], budget: 192) == 1)
        #expect(scheduleResultBlockCount([193, 193], budget: 192) == 1) // Still show the first block before measurement.
        #expect(scheduleResultBlockCount([24, 48], budget: 192) == 2)
        #expect(scheduleResultBlockCount([], budget: 192) == 0)
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

@MainActor @Suite struct ScheduleAlignmentTests {
    @Test func stopBannerMatchesTheSharedContinuationWording() {
        var task = ScheduleStoreTests().task(); task.status = "failed"; task.reason = "execution_failed"
        #expect(scheduleStopBanner(task) == "运行失败，任务已停下。恢复或重新安排后从下一次自动时间开始，不重发上次消息。")
        task.spec.trigger.type = "at"
        #expect(scheduleStopBanner(task) == "运行失败，任务已停下。可立即额外执行一次，或改时间重排。")
        task.status = "uncertain"; task.reason = "restart_dispatching"
        #expect(scheduleStopBanner(task) == "电脑重启前的发送结果无法确认。继续可能重复执行上次的消息，请先打开运行记录核实。")
        task.status = "paused"; task.reason = "agent_schedule_permissions_changed"
        #expect(scheduleStopBanner(task) == "创建任务时的权限已改变，任务已停下。恢复或重新安排后从下一次自动时间开始，不重发上次消息。继续会由你接管任务，按任务中显示的权限运行。")
        task.status = "uncertain"
        #expect(scheduleStopBanner(task) == "创建任务时的权限已改变，任务已停下。继续可能重复执行上次的消息，请先打开运行记录核实。继续会由你接管任务，按任务中显示的权限运行。")
    }
    @Test func missingConversationBannerUsesOnlyTheMatchingFailedRunAndLocalDateRules() {
        var task = ScheduleStoreTests().task(); task.status = "failed"; task.reason = "execution_session_missing"
        task.spec.target.type = "resume_session"
        let now = scheduleDate("2026-10-30T20:00:00Z")!, zone = TimeZone(identifier: "America/Los_Angeles")!
        let suffix = "任务已停下：找不到运行的对话。这个任务只发到原来的对话，恢复后还会失败。"
        #expect(scheduleStopBanner(task, now: now, zone: zone) == suffix)
        var failed = ScheduleRun(id: "missing", startedAt: "2026-10-30T15:00:00Z", status: "failed")
        failed.reason = task.reason
        var other = ScheduleRun(id: "other", startedAt: "2026-10-30T16:00:00Z", status: "failed"); other.reason = "execution_failed"
        task.runs = [failed, other]
        #expect(scheduleStopBanner(task, now: now, zone: zone) == "今天 08:00 的运行失败，" + suffix)
        failed.startedAt = "2026-10-29T15:00:00Z"; task.runs = [failed]
        #expect(scheduleStopBanner(task, now: now, zone: zone) == "昨天 08:00 的运行失败，" + suffix)
        task.runs = [other]
        #expect(scheduleStopBanner(task, now: now, zone: zone) == suffix)
    }
    @Test func offlineListNoticeNamesEachOfflineComputerOnce() {
        var state = ScheduleStoreTests().state()
        state.machines["a"] = MachineView(id: "a", online: false, lastSeenMs: 0, meta: .with { $0.hostname = "iMac" })
        state.machines["b"] = MachineView(id: "b", online: false, lastSeenMs: 0, meta: .with { $0.hostname = "构建电脑" })
        let task = ScheduleStoreTests().task()
        let rows = ["b", "a", "a", "m"].map { MachineSchedule(machineID: $0, task: task) }
        #expect(scheduleOfflineNotice(rows, state: state, online: { $0 == "m" }) == "iMac、构建电脑 未连接，显示的是上次的状态")
        #expect(scheduleOfflineNotice(rows, state: state, online: { $0 != "a" }) == "iMac 未连接，显示的是上次的状态")
        #expect(scheduleOfflineNotice(rows, state: state, online: { _ in true }) == nil)
    }
    @Test(arguments: ["at", "calendar"])
    func nativePickersDoNotSilentlyReplaceRetainedInstants(type: String) async throws {
        await WindowTestGate.shared.acquire(); defer { WindowTestGate.shared.release() }
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let stored = "2020-01-02T08:15:00Z"
        var trigger = ScheduleTrigger(type: type); trigger.timezone = "UTC"
        if type == "at" { trigger.at = stored }
        else { trigger.time = "08:00"; trigger.until = stored }
        let host = UIHostingController(rootView: ScheduleTimingSheet(trigger: trigger, onSave: { _ in }, onClose: {}))
        host.safeAreaRegions = []
        let window = UIWindow(windowScene: scene); window.rootViewController = host; window.isHidden = false
        defer { window.isHidden = true }
        func pickers(_ v: UIView) -> [UIDatePicker] { (v as? UIDatePicker).map { [$0] } ?? v.subviews.flatMap(pickers) }
        await eventually("retained date picker mounted") { pickers(host.view).contains { $0.datePickerMode == .dateAndTime } }
        let picker = try #require(pickers(host.view).first { $0.datePickerMode == .dateAndTime })
        #expect(picker.date == scheduleDate(stored))
        #expect(picker.minimumDate == nil)
    }
    @Test func aLongDescriptionKeepsHistoryWithinTheFirstScreen() async throws {
        await WindowTestGate.shared.acquire(); defer { WindowTestGate.shared.release() }
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let text = (1...40).map { "\($0). 检查构建并保留原文换行。" }.joined(separator: "\n")
        let host = UIHostingController(rootView: ScheduleMessageText(text: text).environment(\.dynamicTypeSize, .large))
        host.safeAreaRegions = []
        let window = UIWindow(windowScene: scene); window.frame = CGRect(x: 0, y: 0, width: 320, height: 600)
        window.rootViewController = host; window.isHidden = false; defer { window.isHidden = true }
        for _ in 0..<30 { host.view.layoutIfNeeded(); try await Task.sleep(for: .milliseconds(10)) }
        let size = host.sizeThatFits(in: CGSize(width: 288, height: 4000))
        #expect(size.height < 230, "six lines and the disclosure must not lay out all 40 lines")
        #expect(size.height > 100, "the preview must keep the text visible")
    }
    @Test func conflictDismissalObservesTheMutationSignal() async throws {
        await WindowTestGate.shared.acquire();defer { WindowTestGate.shared.release() }
        let scene=try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let mutation=ScheduleMutation();var dismissed=false;var appeared=false
        let host=UIHostingController(rootView:ScheduleConflictObserver(mutation:mutation) { dismissed=true }.onAppear { appeared=true })
        let window=UIWindow(windowScene:scene);window.rootViewController=host;window.isHidden=false
        host.view.layoutIfNeeded();defer { window.isHidden=true }
        await eventually("conflict observer visible") { appeared }
        #expect(mutation.recordConflict(CommandError("unavailable","offline")) == nil)
        #expect(mutation.recordConflict(CommandError("aborted","relay outcome unknown")) == nil)
        let error=try #require(mutation.recordConflict(CommandError("aborted","revision",fromDaemon:true)))
        #expect(errorText(error)=="任务刚被修改，已更新为最新内容")
        await eventually("conflict dismisses stale editor") { dismissed }
        #expect(dismissed && !mutation.frozen)
    }
    @Test func destinationUsesSharedPlaceholderUntilMetadataLoads() {
        var state=ScheduleStoreTests().state()
        var task=ScheduleStoreTests().task();task.spec.target=ScheduleTarget(type:"resume_session");task.spec.target.session="s"
        state.sessions["s"]=SessionView(id:"s",machineId:"m")
        #expect(scheduleDestination(task,state:state)=="对话")
        state.sessions["s"]?.info=PBSession.with { $0.title="修复登录超时" }
        #expect(scheduleDestination(task,state:state)=="修复登录超时")
    }
    @Test func taskEditorIgnoresOtherComputerDefaults() throws {
        var state=ScheduleStoreTests().state()
        state.machines["other"]=state.machines["m"]
        state.prefs=PrefsObject(revision:1,prefs:PBProjectPrefs.with { $0.settings=[PBProjectSettings.with { $0.path="/example";$0.machineID="other";$0.agents=[PBAgentSettings.with { $0.agent = .claude;$0.model="other-model" }] }] })
        state.sessions["other"]=SessionView(id:"other",machineId:"other",info:PBSession.with { $0.cwd="/example";$0.agent = .claude;$0.model="other-session-model" })
        let scoped=scheduleEditingState(state,machine:"m")
        #expect(scoped.sessions.isEmpty && scoped.prefs?.prefs.settings.isEmpty == true && scoped.machines.count == 1)
        #expect(projectStart(scoped,key:"/example",pick:.claude,machine:"m")?.config.model == "")
    }
    @Test func unloadedConversationMetadataUsesTheSharedPlaceholder() {
        var task = ScheduleStoreTests().task(); task.spec.target = ScheduleTarget(type:"resume_session"); task.spec.target.session = "s"
        var state = ScheduleStoreTests().state(); state.sessions["s"] = SessionView(id:"s",machineId:"m")
        #expect(scheduleLocation(task,state:state,machine:"m") == "在对话里")
        state.sessions["s"]?.info = PBSession.with { $0.title = "修复登录超时" }
        #expect(scheduleLocation(task,state:state,machine:"m") == "在「修复登录超时」里")
        state.sessions["s"]?.info?.agent = .claude
        #expect(scheduleLocation(task,state:state,machine:"m",frequency:true).contains("在「修复登录超时」里 · Claude Code"))
    }
    @Test("Only system stops show a reason banner; an expired manual pause stays quiet")
    func onlySystemStopsAndExpiredPausesShowAReasonBanner() {
        var task = ScheduleStoreTests().task(); task.status = "paused"
        func visible() -> Bool { ScheduleEditAccess(task:task,online:true,saving:false,unknown:false).showsReason }
        task.reason = "user_paused"; #expect(!visible())
        task.reason = "completed_while_paused"; #expect(!visible())
        task.reason = "agent_schedule_permissions_changed"; #expect(visible())
    }
    @Test func renamingIsIndependentOfTaskStatusExecutionAndPendingRun() {
        let base = ScheduleStoreTests().task()
        for status in ["scheduled", "paused", "failed", "uncertain", "completed", "cancelled", "running", "dispatching", "queued"] {
            var task = base; task.status = status
            for active in [false, true] {
                if active { task.active = ScheduleRun(id: "run", startedAt: "2026-10-24T17:00:00Z", status: "running") }
                let access = ScheduleEditAccess(task: task, online: true, saving: false, unknown: false)
                #expect(access.rename)
                #expect(access.definition == !active)
                #expect(!ScheduleEditAccess(task: task, online: false, saving: false, unknown: false).rename)
                #expect(!ScheduleEditAccess(task: task, online: true, saving: true, unknown: false).rename)
                #expect(!ScheduleEditAccess(task: task, online: true, saving: false, unknown: true).rename)
            }
        }
        let json = "{\"request_id\":\"manual\"}".data(using: .utf8)!
        var pending = base; pending.manualRequest = try? JSONDecoder().decode(ScheduleTask.Manual.self, from: json)
        let access = ScheduleEditAccess(task: pending, online: true, saving: false, unknown: false)
        #expect(access.rename && access.definition && !access.run)
    }

    @Test func cliIsANativeInlineSegmentAndDisappearsWithOnlyOneInstalledAgent() async throws {
        await WindowTestGate.shared.acquire()
        defer { WindowTestGate.shared.release() }
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        for agents in [[PBAgent.codex, .claude, .antigravity], [PBAgent.codex, .claude], [.claude]] {
            var state = ScheduleStoreTests().state()
            state.machines["m"]?.meta?.agents = agents.map { agent in .with { $0.agent = agent } }
            let draft = ScheduleDraft(); draft.config = NewConfig(machineId: "m", cwd: "/example", agent: .claude, model: "haiku")
            let row = ScheduleAgentRow(state: state, config: Bindable(draft).config)
            let host = UIHostingController(rootView: row)
            let window = UIWindow(windowScene: scene); window.frame = CGRect(x: 0, y: 0, width: 320, height: 200)
            window.rootViewController = host; window.isHidden = false
            func segments(_ view: UIView) -> [UISegmentedControl] { (view as? UISegmentedControl).map { [$0] } ?? view.subviews.flatMap(segments) }
            for _ in 0..<30 where agents.count > 1 && segments(host.view).isEmpty { try await Task.sleep(for: .milliseconds(10)) }
            host.view.layoutIfNeeded()
            let controls = segments(host.view)
            #expect(controls.count == (agents.count > 1 ? 1 : 0))
            if let control = controls.first {
                #expect(control.numberOfSegments == agents.count)
                #expect(control.titleForSegment(at: 0) == "Codex" && control.titleForSegment(at: 1) == "Claude Code")
                if agents.count > 2 { #expect(control.titleForSegment(at: 2) == "Antigravity") }
                #expect(control.selectedSegmentIndex == 1)
                #expect(control.selectedSegmentTintColor?.resolvedColor(with: control.traitCollection) == UIColor(Theme.card).resolvedColor(with: control.traitCollection))
                let normal = control.titleTextAttributes(for: .normal)?[.foregroundColor] as? UIColor
                let selected = control.titleTextAttributes(for: .selected)?[.foregroundColor] as? UIColor
                #expect(normal?.resolvedColor(with: control.traitCollection) == UIColor(Theme.muted).resolvedColor(with: control.traitCollection))
                #expect(selected?.resolvedColor(with: control.traitCollection) == UIColor(Theme.fg).resolvedColor(with: control.traitCollection))
                control.selectedSegmentIndex = 0; control.sendActions(for: .valueChanged)
                for _ in 0..<20 where draft.config?.agent != .codex { try await Task.sleep(for: .milliseconds(10)) }
                #expect(draft.config?.agent == .codex && draft.config?.model == "")
                if agents.count == 2 {
                    // Changing projects can replace one installed CLI without changing the segment count.
                    state.machines["m"]?.meta?.agents = [PBAgent.codex, .antigravity].map { agent in .with { $0.agent = agent } }
                    host.rootView = ScheduleAgentRow(state: state, config: Bindable(draft).config)
                    await eventually("updated installed CLI names") { control.titleForSegment(at: 1) == "Antigravity" }
                    control.selectedSegmentIndex = 1; control.sendActions(for: .valueChanged)
                    await eventually("selects the replacement CLI") { draft.config?.agent == .antigravity }
                }
            }
            window.isHidden = true
        }
    }
}

@MainActor @Observable private final class ScheduledJumpProbe {
    var loaded = false
    var jump = SessionStore.ScheduledJump(runID:"run",seq:5)
    var attempts = 0
}
@MainActor @Suite struct ScheduleJumpObserverTests {
    @Test func retriesWhenLoadingFinishesWithoutChangingAnyRowID() async throws {
        await WindowTestGate.shared.acquire();defer { WindowTestGate.shared.release() }
        let scene=try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let probe=ScheduledJumpProbe()
        // Keep the observable values in a real SwiftUI body, not a captured one-time view value.
        struct Host:View {
            let probe:ScheduledJumpProbe
            var body:some View { Text("history").modifier(ScheduledJumpObserver(rowIDs:["e5"],loaded:probe.loaded,jump:probe.jump) { probe.attempts += 1 }) }
        }
        let host=UIHostingController(rootView:Host(probe:probe))
        let window=UIWindow(windowScene:scene);window.frame=CGRect(x:0,y:0,width:320,height:200);window.rootViewController=host;window.isHidden=false
        defer { window.isHidden=true }
        await eventually("initial jump observation") { probe.attempts==1 }
        probe.loaded=true
        await eventually("loaded history retries jump") { probe.attempts==2 }
        probe.jump = .init(runID:"another",seq:8)
        await eventually("another requested run") { probe.attempts==3 }
        #expect(probe.attempts==3)
    }
}
