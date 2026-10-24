import Observation
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
        for width in [320.0, 760] {
            let row = SessionListRowContent(title: "检查新回复", right: "下次 今天 15:00", third: "今天 4 次都没有通知", color: Theme.gray, last: false) { Text("每小时 · 在「修复登录超时」里").type(T.sm, Theme.muted) }
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

@MainActor @Suite struct ScheduleAlignmentTests {
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
    }
    @Test func onlySystemStopsAndExpiredPausesShowAReasonBanner() {
        var task = ScheduleStoreTests().task(); task.status = "paused"
        func visible() -> Bool { ScheduleEditAccess(task:task,online:true,saving:false,unknown:false).showsReason }
        task.reason = "user_paused"; #expect(!visible())
        task.reason = "completed_while_paused"; #expect(visible())
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
        for agents in [[PBAgent.codex, .claude], [.claude]] {
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
                #expect(control.numberOfSegments == 2)
                #expect(control.titleForSegment(at: 0) == "Codex" && control.titleForSegment(at: 1) == "Claude Code")
                #expect(control.selectedSegmentIndex == 1)
                let normal = control.titleTextAttributes(for: .normal)?[.foregroundColor] as? UIColor
                let selected = control.titleTextAttributes(for: .selected)?[.foregroundColor] as? UIColor
                #expect(normal?.resolvedColor(with: control.traitCollection) == UIColor(Theme.muted).resolvedColor(with: control.traitCollection))
                #expect(selected?.resolvedColor(with: control.traitCollection) == UIColor(Theme.fg).resolvedColor(with: control.traitCollection))
                control.selectedSegmentIndex = 0; control.sendActions(for: .valueChanged)
                for _ in 0..<20 where draft.config?.agent != .codex { try await Task.sleep(for: .milliseconds(10)) }
                #expect(draft.config?.agent == .codex && draft.config?.model == "")
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
