import Observation
import RACore
import RAProto
import SwiftUI

/// One immutable opening snapshot owns the whole edit, including its compare-and-set revision.
@MainActor @Observable final class ScheduleEditDraft {
    private(set) var task: ScheduleTask?
    func open(_ task: ScheduleTask) { self.task = task }
    func command(spec: ScheduleSpec) throws -> PBCommand {
        guard let task else { throw UserError(L10nMessage.scheduleIosOpenTheTaskEditorFirst()) }
        return try scheduleCommand(operation: "update", taskID: task.id, spec: spec, revision: task.revision)
    }
}

func scheduleStopBanner(_ task: ScheduleTask, now: Date = Date(), zone: TimeZone = .current) -> String {
    if task.missingTarget {
        let failedAt = task.history.first { $0.status == "failed" && $0.reason == task.reason }
            .flatMap { scheduleDate($0.startedAt) }
        let reason = scheduleReason("execution_session_missing")
        return failedAt.map { L10n.scheduleMissingTargetRun(reason: reason, time: scheduleMoment($0, now: now, zone: zone)) } ?? L10n.scheduleMissingTarget(reason: reason)
    }
    let reason = scheduleReason(task.reason)
    let notice: String
    if task.status == "uncertain" {
        notice = L10n.scheduleAttentionUncertain(reason: reason)
    } else if task.status == "failed" && task.oneShot {
        notice = L10n.scheduleIosAttentionOnce(reason: reason)
    } else {
        notice = L10n.scheduleAttentionResume(reason: reason)
    }
    return task.reason == "agent_schedule_permissions_changed" ? L10n.scheduleIosTakeoverNotice(notice: notice) : notice
}

struct ScheduleDetailScreen: View {
    let machine: String
    let id: String
    @Environment(AppModel.self) private var model
    @Environment(AccountContext.self) private var context
    @Environment(Navigator.self) private var nav
    @Environment(\.page) private var page
    @State private var editor = ScheduleEditDraft()
    @State private var timing = false
    @State private var rename = false
    @State private var message = false
    @State private var permissions = false
    @State private var targetEdit = false
    @State private var delete = false
    @State private var expanded: Set<String> = []
    @State private var allHistory = false
    @State private var menu = false
    var body: some View {
        let store = context.schedules
        let task = store.task(machine: machine, id: id)
        let mutation = context.scheduleMutations.of(machine + "/" + id)
        Page(gray: true) {
            VStack(spacing: 0) {
                ConnectionBar()
                ScrollViewReader { proxy in
                    ScrollView {
                        if let task {
                            content(task, mutation: mutation)
                        } else {
                            Text(store.loading.contains(machine) ? L10n.scheduleIosLoadingTasks() : L10n.scheduleIosTaskNotFound()).font(.body).ink(DS.ink2).padding(32)
                        }
                        Color.clear.frame(height: 1).id("schedule-detail-end")
                    }
                    .debugSignal("schedule.detail.end") { proxy.scrollTo("schedule-detail-end", anchor: .bottom) }
                }
            }
        }
        .background(ScheduleConflictObserver(mutation: mutation) {
            timing = false; targetEdit = false; permissions = false; rename = false; message = false
        })
        .navBar(task?.spec.title ?? L10n.scheduleIosTaskDetails(), subtitle: task.map { detailSubtitle($0) }, onMore: { menu = true })
        .actionMenu(isPresented: $menu, actions: task.map { menuActions($0, mutation: mutation) } ?? []) {
            if let task {
                VStack(alignment: .leading, spacing: 4) {
                    Text(task.spec.title).font(.body.weight(.semibold)).ink(DS.ink)
                    Text(scheduleLocation(task, state: model.state, machine: machine) + " · " + task.creator +
                         (scheduleDate(task.createdAt).map { " · " + scheduleDay($0) } ?? "")).font(.footnote).ink(DS.ink2)
                }
            }
        }
        .kitSheet(isPresented: $timing, size: .large) {
            if let task = editor.task {
                ScheduleTimingSheet(trigger: task.spec.trigger, onSave: { t in var spec = task.spec; spec.trigger = t; update(task, spec: spec) }, onClose: { timing = false })
            }
        }
        .kitSheet(isPresented: $targetEdit, size: .large) {
            if let task = editor.task { ScheduleTargetSheet(machine: machine, target: task.spec.target, onSave: { target in var spec = task.spec; spec.target = target; update(task, spec: spec) }, onClose: { targetEdit = false }) }
        }
        .kitSheet(isPresented: $permissions) {
            if let task = editor.task {
                ScheduleConfigSheet(page: .mode, config: Binding(get: {
                    NewConfig(machineId: machine, cwd: task.spec.target.cwd ?? "", agent: agentById(task.spec.target.agent).agent,
                              model: task.spec.target.model ?? "", effort: task.spec.target.effort ?? "", mode: ["ask": Mode.ask, "auto": .auto, "full": .full][task.spec.target.mode ?? ""])
                }, set: { selected in
                    var spec = task.spec
                    spec.target.mode = selected?.mode.map { $0 == .full ? "full" : $0 == .auto ? "auto" : "ask" }
                    update(task, spec: spec)
                }), fixedMachine: machine) { permissions = false }
            }
        }
        .inputAlert(isPresented: $rename, title: L10n.scheduleRenameTask(), initial: editor.task?.spec.title ?? "", placeholder: L10n.scheduleLeaveBlankToUseTheMessageSFirstSentence(), confirm: L10n.commonSave(), text: L10n.scheduleSavingBlankUsesTheMessageSFirstSentence(), limit: 60, lines: 1...1, save: { typed in
            guard let task = editor.task else { return }
            var spec = task.spec; spec.title = scheduleTitle(typed, message: task.spec.message)
            guard spec.title != task.spec.title else { return }
            try await save(task, spec: spec)
        })
        .fullScreenCover(isPresented: $message) {
            if let snapshot = editor.task {
                ScheduleMessageEditor(initial: snapshot.spec.message,
                    enabled: ScheduleEditAccess(task: task ?? snapshot, online: store.online(machine), saving: mutation.running, unknown: mutation.frozen).definition,
                    onSave: { typed in var spec = snapshot.spec; spec.message = typed; try await save(snapshot, spec: spec) },
                    onClose: { message = false })
            }
        }
        .confirm(isPresented: $delete, title: L10n.scheduleIosDeleteThisTask(), text: L10n.scheduleIosStopsFutureAutomaticRunsMessagesAlreadySentAndConversations(), confirm: L10n.scheduleDelete(), danger: true) {
            if let task { perform(task, operation: "cancel", done: { nav.back(from: page) }) }
        }
        .debugSignal("schedule.more") { menu = true }
        .debugSignal("schedule.result.latest") { store.refreshDetailResult(machine: machine, task: id) }
        .debugSignal("schedule.mark-unread") {
            markScheduleUnreadAndBack(machine: machine, taskID: id, store: store, nav: nav, page: page)
        }
        .debugSignal("schedule.history.all") { allHistory = true }
        .debugSignal("schedule.edit-message") {
            if let current = store.task(machine: machine, id: id) { editor.open(current); message = true }
        }
        .debugSignal("schedule.edit-target") {
            if let current = store.task(machine: machine, id: id) { editor.open(current); targetEdit = true }
        }
        .debugSignal("schedule.open-run") {
            if let run = store.task(machine: machine, id: id)?.history.first(where: { !($0.sessionID ?? "").isEmpty }) { open(run) }
        }
        .debugSignal("schedule.pause") { if let task = store.task(machine: machine, id: id) { perform(task, operation: "pause") } }
        .debugSignal("schedule.run") { if let task = store.task(machine: machine, id: id) { perform(task, operation: "run") } }
        .task { await store.refresh(machine) }
        .onChange(of: task?.id, initial: true) { _, _ in
            #if DEBUG
            if model.isDemo, let task {
                editor.open(task)
                let panel = HomeLaunch.value("-schedulePanel")
                message = panel == "editor"; timing = panel?.hasPrefix("timing-") == true
            }
            #endif
        }
        .task(id: task?.spec.target.session) {
            if let sid = task?.spec.target.session, model.state.sessions[sid] == nil { _ = try? await context.remote.fetchSession(sid) }
        }
    }
    @ViewBuilder private func content(_ task: ScheduleTask, mutation: ScheduleMutation) -> some View {
        let online = context.schedules.online(machine)
        let access = ScheduleEditAccess(task: task, online: online, saving: mutation.running, unknown: mutation.frozen)
        let mutable = access.definition
        let displayedResult = context.schedules.detailResult
        VStack(alignment: .leading, spacing: 0) {
            if access.showsReason {
                ScheduleRecoveryNotice(text: scheduleStopBanner(task), recovery: access.primaryRecovery,
                    enabled: online && !mutation.running && !mutation.frozen && (task.active == nil || task.active?.status == "uncertain") && task.manualRequest == nil) { recover(task) }
            }
            if !mutation.error.isEmpty { Banner(mutation.error, tone: .error).padding(16) }
            if mutation.frozen {
                TextAction(L10n.scheduleRetrySameRequest(), enabled: online && !mutation.running && !mutation.expired) { perform(task, operation: "get") }.padding(.horizontal, 16)
                Hint(L10n.scheduleIosTheComputerMayHaveAlreadyExecutedItCheckThe()).padding(.horizontal, 16)
                TextAction(L10n.scheduleIosAbandonOriginalRequest(), enabled: online && !mutation.running) { mutation.abandon() }.padding(.horizontal, 16)
            }
            if context.schedules.hasNewDetailResult {
                TextAction(L10n.homeIosNewResults()) { context.schedules.refreshDetailResult(machine: machine, task: id) }
                    .padding(.horizontal, 16)
            }
            if let running = task.running {
                HStack(spacing: 6) {
                    SymbolImage(Mark.progress, style: .subheadline, color: DS.blue)
                    Text(L10n.scheduleRunningSince(time: String(describing: scheduleTime(running.date)))).font(.subheadline).ink(DS.blue)
                    Spacer(minLength: 0)
                    if !(running.sessionID ?? "").isEmpty { TextAction(L10n.scheduleConversation(), arrow: true) { open(running) } }
                }.padding(16).background(DS.gbg, in: RoundedRectangle(cornerRadius: DS.Radius.group))
                    .padding(.horizontal, 16).padding(.top, 16)
            }
            if let result = displayedResult {
                ScheduleResultCard(run: result,
                    unread: context.schedules.wasUnreadOnEntry(machine: machine, task: id, run: result.id),
                    showConversation: task.spec.target.type != "resume_session" || (task.running?.sessionID ?? "").isEmpty) { open(result) }
                    .id(result.id)
            }
            let earlier = task.earlierRuns(resultID: displayedResult?.id)
            if !earlier.isEmpty {
                HStack {
                    Text(L10n.scheduleEarlier()).font(.subheadline).ink(DS.ink2)
                    Spacer()
                    Text(L10n.scheduleRunsCount(count: Int(earlier.count))).font(.subheadline).ink(DS.ink2)
                }.padding(.horizontal, 30).padding(.top, 20).padding(.bottom, 6)
                history(task, runs: earlier)
            }
            GroupCard {
                VStack(alignment: .leading, spacing: 0) {
                HStack {
                    Text(L10n.scheduleWhatShouldTheAgentDo()).font(.subheadline.weight(.semibold)).ink(DS.ink)
                    Spacer()
                    TextAction(L10n.toolsIosEdit(), enabled: mutable) { editor.open(task); message = true }
                }.padding(.horizontal, 14)
                Text(task.spec.message).font(.body).ink(DS.ink).lineLimit(3)
                    .frame(maxWidth: .infinity, alignment: .leading).padding(14)
                }
            }.padding(.top, 16).padding(.bottom, 16)
            GroupLabel(L10n.commonSettings())
            GroupCard {
                GRow(L10n.scheduleWhen(), value: scheduleFrequency(task.spec.trigger, includeZone: true), chevron: mutable, onTap: mutable ? { editor.open(task); timing = true } : nil)
                ToggleRow(L10n.scheduleNotifyOnCompletion(), isOn: Binding(get: { task.spec.notifyOnFinish }, set: { on in var spec = task.spec; spec.notifyOnFinish = on; update(task, spec: spec) })).disabled(!access.rename)
                let limits = scheduleLimits(task.spec.trigger)
                if !limits.isEmpty { GRow(L10n.sessionEnd(), value: limits, chevron: mutable, onTap: mutable ? { editor.open(task); timing = true } : nil) }
                if task.spec.target.type == "new_session" {
                    GRow(L10n.scheduleRunIn(), value: scheduleLocation(task, state: model.state, machine: machine), chevron: mutable, onTap: mutable ? { editor.open(task); targetEdit = true } : nil)
                    GRow(L10n.newSessionPermissions(), value: permission(task), chevron: mutable, valueColor: task.spec.target.mode == "full" ? DS.red : nil, onTap: mutable ? { editor.open(task); permissions = true } : nil)
                } else {
                    GRow(L10n.scheduleSendToSchedules(), value: scheduleDestination(task, state: model.state), chevron: online && !task.missingTarget, onTap: online && !task.missingTarget ? {
                        if let sid = task.spec.target.session { nav.open(.session(id: sid)) }
                    } : nil)
                    GRow(L10n.newSessionPermissions(), value: permission(task), valueColor: targetSession(task).map { modeOf($0) == .full } == true ? DS.red : nil)
                }
                GRow(L10n.scheduleTaskName(), value: task.spec.title, chevron: access.rename, valueLines: 1, onTap: access.rename ? { editor.open(task); rename = true } : nil)
            }
            Hint((task.spec.target.type == "new_session" ? L10n.scheduleEachRunOpensANewConversationGroupedUnderThis() : L10n.scheduleMessagesGoToTheCurrentConversationAndWaitFor()) + " " + task.creator + (task.createdAt.isEmpty ? "" : " · " + (scheduleDate(task.createdAt).map { scheduleMoment($0) } ?? ""))).padding(.horizontal, 16).padding(.bottom, 16)
            if !online { Hint(L10n.scheduleIosConnectToEditRunOrDelete(value: String(describing: machineLabel(model.state, id: machine)))).padding(.horizontal, 16).padding(.bottom, 16) }
        }
    }
    private func recover(_ task: ScheduleTask) {
        if task.status == "cancelled" && task.visible {
            if task.oneShot { editor.open(task); timing = true }
            else { perform(task, operation: "update", spec: task.spec) }
        } else { perform(task, operation: task.status == "failed" && task.oneShot ? "run" : "resume") }
    }
    private func targetSession(_ task: ScheduleTask) -> SessionView? { task.spec.target.session.flatMap { model.state.sessions[$0] } }
    private func permission(_ task: ScheduleTask) -> String {
        if task.spec.target.type == "resume_session" { return targetSession(task).flatMap { modeLabel($0) }?.text ?? L10n.scheduleVerifyCurrentPermissionsInTheConversation() }
        return ["ask": L10n.modesAsk(), "auto": L10n.modesAuto(), "full": L10n.modesFullAccess()][task.spec.target.mode ?? ""] ?? L10n.newSessionFollowCli()
    }
    @ViewBuilder private func history(_ task: ScheduleTask, runs: [ScheduleRun]) -> some View {
        let groups = scheduleHistoryGroups(runs)
        GroupCard {
            if groups.isEmpty { GRow(L10n.scheduleHasNotRunYet()) }
            if allHistory {
                ForEach(runs) { run in record(run, task: task) }
            } else {
                ForEach(Array(groups.prefix(5))) { group in
                    let run = group.runs[0]
                    if group.folded {
                        GRow(L10n.scheduleQuietRuns(count: Int(group.runs.count), day: String(describing: scheduleDay(run.notificationTime))), sub: L10n.scheduleLatestSummary(summary: String(describing: run.foldedSummary), time: String(describing: scheduleTime(run.notificationTime))), chevron: false) {
                            if expanded.contains(group.id) { expanded.remove(group.id) } else { expanded.insert(group.id) }
                        }.trailing { Arrow(DS.ink2, direction: expanded.contains(group.id) ? .up : .down) }
                        if expanded.contains(group.id) { ForEach(group.runs) { record($0, task: task) } }
                    } else { record(run, task: task) }
                }
            }
            if groups.count > 5 && !allHistory {
                GRow(L10n.scheduleViewAllRuns(count: Int(runs.count)), action: true) { allHistory = true }
            }
        }
    }
    private func record(_ run: ScheduleRun, task: ScheduleTask) -> some View {
        let sid = run.sessionID ?? ""
        return Button { open(run) } label: {
            HStack(alignment: .top, spacing: 0) {
                ZStack {
                    if context.schedules.wasUnreadOnEntry(machine: machine, task: id, run: run.id) {
                        Circle().fill(DS.blue).frame(width: 8, height: 8).accessibilityLabel(L10n.scheduleUnreadOnEntry())
                    }
                }.frame(width: 22, height: 20)
                VStack(alignment: .leading, spacing: 4) {
                    Text(scheduleMoment(run.notificationTime)).font(.subheadline).ink(DS.ink2)
                    Text(scheduleRunSummary(run)).font(.body).ink(DS.ink).lineLimit(2)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                if !sid.isEmpty { Arrow(DS.ink2).padding(.leading, 8).padding(.top, 12) }
            }.padding(.horizontal, 14).padding(.vertical, 12).contentShape(Rectangle())
        }.buttonStyle(RowPressStyle()).disabled(sid.isEmpty).accessibilityElement(children: .combine)
    }
    private func detailSubtitle(_ task: ScheduleTask) -> String {
        if task.group == .attention { return L10n.scheduleStopped() }
        let frequency = scheduleListFrequency(task.spec.trigger)
        if task.spec.target.type == "resume_session" { return L10n.scheduleSendTo(detail: String(describing: scheduleDestination(task, state: model.state)), value: String(describing: frequency)) }
        return frequency + (task.nextRunAt.flatMap(scheduleDate).map { L10n.scheduleIosNext(value: String(describing: scheduleMoment($0))) } ?? "")
    }
    private func menuActions(_ task: ScheduleTask, mutation: ScheduleMutation) -> [SheetAction] {
        let online = context.schedules.online(machine)
        let access = ScheduleEditAccess(task: task, online: online, saving: mutation.running, unknown: mutation.frozen)
        var actions = [SheetAction(L10n.scheduleRunOnceNow(), enabled: access.run || (access.rename && task.oneShot && task.status == "failed" && task.active == nil && task.manualRequest == nil)) {
            perform(task, operation: "run")
        }]
        if task.latestNotification != nil {
            actions.append(SheetAction(L10n.scheduleMarkAsUnread()) {
                afterMenu {
                    markScheduleUnreadAndBack(machine: machine, taskID: id, store: context.schedules, nav: nav, page: page)
                }
            })
        }
        if let toggle = access.menuToggle {
            actions.append(SheetAction(toggle, enabled: online && !mutation.running && !mutation.frozen) {
                if task.group == .paused || task.group == .attention { afterMenu { recover(task) } }
                else { perform(task, operation: "pause") }
            })
        }
        actions.append(SheetAction(L10n.scheduleDeleteTask(), danger: true, enabled: online && !mutation.running && !mutation.frozen) { afterMenu { delete = true } })
        return actions
    }
    private func afterMenu(_ action: @escaping @MainActor () -> Void) {
        Task { try? await Task.sleep(for: .milliseconds(220)); action() }
    }
    private func open(_ run: ScheduleRun) {
        guard let sid = run.sessionID, !sid.isEmpty else { return }
        context.sessionStore.jumpToScheduledRun(sid, runID: run.id, seq: run.firstSeq)
        nav.open(.session(id: sid))
    }
    private func save(_ task: ScheduleTask, spec: ScheduleSpec) async throws {
        guard context.schedules.online(machine) else { throw UserError(L10nMessage.scheduleIosTheComputerIsOfflineConnectToMakeChanges()) }
        try await context.scheduleMutations.of(machine + "/" + id).execute(remote: context.remote, machine: machine, command: {
            try editor.command(spec: spec)
        }, refresh: { await context.schedules.refresh(machine) })
    }
    private func update(_ task: ScheduleTask, spec: ScheduleSpec) { perform(task, operation: "update", spec: spec) }
    private func perform(_ task: ScheduleTask, operation: String, spec: ScheduleSpec? = nil, done: @escaping () -> Void = {}) {
        guard context.schedules.online(machine) else { return }
        context.scheduleMutations.of(machine + "/" + id).run(remote: context.remote, machine: machine, command: {
            try scheduleCommand(operation: operation, taskID: id, spec: spec, revision: task.revision)
        }, refresh: { await context.schedules.refresh(machine) }, done: { _ in done() })
    }
}

struct ScheduleConflictObserver: View {
    let mutation: ScheduleMutation
    let dismiss: () -> Void
    var body: some View { Color.clear.frame(width: 0, height: 0).onChange(of: mutation.conflicts) { _, _ in dismiss() } }
}

@MainActor func markScheduleUnreadAndBack(machine: String, taskID: String, store: ScheduleStore, nav: Navigator, page: Route?) {
    guard nav.showing(page), let task = store.task(machine: machine, id: taskID) else { return }
    store.markUnread(.init(machineID: machine, task: task), keepingUnreadOn: nav.path.dropLast().last)
    nav.back(from: page)
}

/// The notice and its primary action share the result cards' 16 pt edges.
/// Its surface is explicitly soft, including inside a grey detail page.
struct ScheduleRecoveryNotice: View {
    let text: String
    let recovery: String?
    let enabled: Bool
    let action: () -> Void
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(text).font(.subheadline).ink(DS.orangeInk)
                .frame(maxWidth: .infinity, alignment: .leading).padding(16)
                .background(DS.gbg, in: RoundedRectangle(cornerRadius: DS.Radius.small))
            if let recovery { Btn(recovery, kind: .primary, enabled: enabled, action: action) }
        }.padding(.horizontal, 16).padding(.top, 16).padding(.bottom, 12)
    }
}
