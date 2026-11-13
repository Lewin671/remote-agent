import Foundation
import Observation
import RACore
import RAProto

/// Account-scoped plaintext in memory; only signed encrypted list results are cached by Remote.
@MainActor @Observable
final class ScheduleStore {
    struct Calls {
        var cached: (String) async -> [ScheduleTask]?
        var list: (String) async throws -> [ScheduleTask]
        var resolve: (String) async -> Void = { _ in }
    }
    @ObservationIgnored private let defaults: UserDefaults?
    @ObservationIgnored private let scope: [String]
    // Observed: detailResult reads these, and a pushed detail may render before setViewing runs.
    private var viewing: Route?
    @ObservationIgnored private var foreground = false
    @ObservationIgnored private var detailReturnTime: Date?
    private var capturedDetail = false
    // Manual unread survives automatic reads on this page, including the page
    // returned to from a detail. The next different page/background ends it.
    @ObservationIgnored private var heldUnread: [String: Set<String>] = [:]
    @ObservationIgnored private var heldUnreadPage: Route?
    private(set) var detailResultID: String?
    @ObservationIgnored private var completeMachines: Set<String> = []
    var detailResult: ScheduleRun? {
        guard case .schedule(let machine, let id) = viewing, let task = task(machine: machine, id: id) else { return nil }
        if !capturedDetail { return task.latestNotification }
        return task.history.first { $0.id == detailResultID }
    }
    var hasNewDetailResult: Bool {
        guard capturedDetail, case .schedule(let machine, let id) = viewing else { return false }
        return task(machine: machine, id: id)?.latestNotification?.id != detailResultID
    }
    private(set) var detailUnread: Set<String> = []
    private var seen: [String: Set<String>] = [:]
    init(defaults: UserDefaults? = nil, scope: [String] = []) { self.defaults = defaults; self.scope = scope }
    private func key(_ machine: String) -> String { "ra-schedule-seen:" + String(data: try! JSONEncoder().encode(scope + [machine]), encoding: .utf8)! }
    private func token(_ task: String, _ run: String) -> String { String(data: try! JSONEncoder().encode([task, run]), encoding: .utf8)! }
    private func saveSeen(_ machine: String, _ ids: Set<String>) {
        seen[machine] = ids; defaults?.set(Array(ids), forKey: key(machine))
    }
    private func observe(_ machine: String, _ list: [ScheduleTask], complete: Bool = true) {
        if complete { completeMachines.insert(machine) } else { completeMachines.remove(machine) }
        let ids = Set(list.flatMap { t in t.history.filter(\.notified).map { token(t.id, $0.id) } })
        if seen[machine] == nil {
            if let stored = defaults?.stringArray(forKey: key(machine)) { seen[machine] = Set(stored) }
            else { saveSeen(machine, ids) }
        }
        if complete, let current = seen[machine], current != current.intersection(ids) { saveSeen(machine, current.intersection(ids)) }
        if complete, let held = heldUnread[machine] { heldUnread[machine] = held.intersection(ids) }
        if complete, foreground, let returned = detailReturnTime,
           case .schedule(let selectedMachine, let id) = viewing, selectedMachine == machine,
           let task = list.first(where: { $0.id == id }) {
            let unread = unreadRunIDs(machine: machine, task: task)
            detailUnread.formUnion(task.history.filter { unread.contains($0.id) && $0.notificationTime <= returned }.map(\.id))
            detailReturnTime = nil
        }
        captureDetail(machine, list)
        markViewing(machine, list)
    }
    func unreadRunIDs(machine: String, task: ScheduleTask) -> Set<String> {
        Set(task.history.filter { $0.notified && seen[machine]?.contains(token(task.id, $0.id)) == false }.map(\.id))
    }
    func hasNewResult(_ entry: MachineSchedule) -> Bool { !unreadRunIDs(machine: entry.machineID, task: entry.task).isEmpty }
    func rows(_ group: ScheduleInboxGroup) -> [MachineSchedule] { scheduleInbox(tasks, group: group, hasNew: hasNewResult) }
    func markRead(_ entries: [MachineSchedule]) {
        for entry in entries {
            var ids = seen[entry.machineID] ?? []
            for run in entry.task.history where run.notified {
                let id = token(entry.task.id, run.id)
                ids.insert(id); heldUnread[entry.machineID]?.remove(id)
            }
            saveSeen(entry.machineID, ids)
        }
    }
    func markUnread(_ entry: MachineSchedule) { markUnread(entry, keepingUnreadOn: viewing) }
    func markUnread(_ entry: MachineSchedule, keepingUnreadOn page: Route?) {
        guard let run = entry.task.latestNotification else { return }
        if heldUnreadPage != page { heldUnread.removeAll() }
        heldUnreadPage = page
        let id = token(entry.task.id, run.id)
        heldUnread[entry.machineID, default: []].insert(id)
        var ids = seen[entry.machineID] ?? []
        ids.remove(id); saveSeen(entry.machineID, ids)
    }
    func refreshDetailResult(machine: String, task id: String) {
        guard capturedDetail, completeMachines.contains(machine), viewing == .schedule(machine: machine, id: id) else { return }
        detailResultID = task(machine: machine, id: id)?.latestNotification?.id
    }

    /// The navigator's top page is the existing source of truth for what is visible.
    /// A backgrounded page keeps its visit snapshot but does not consume new notifications.
    func setViewing(_ page: Route?, foreground: Bool) {
        if !foreground || (viewing != page && page != heldUnreadPage) {
            heldUnread.removeAll(); heldUnreadPage = nil
        }
        if viewing != page {
            viewing = page; capturedDetail = false; detailUnread = []; detailResultID = nil; detailReturnTime = nil
        }
        let returning = !self.foreground && foreground
        if returning && capturedDetail { detailReturnTime = Date() }
        self.foreground = foreground
        for machine in Set(tasks.map(\.machineID)) {
            let list = tasks.filter { $0.machineID == machine }.map(\.task)
            if returning, capturedDetail, case .schedule(let selectedMachine, let id) = viewing,
               machine == selectedMachine, let task = list.first(where: { $0.id == id }) {
                detailUnread.formUnion(unreadRunIDs(machine: machine, task: task))
            }
            captureDetail(machine, list)
            markViewing(machine, list)
        }
    }
    private func captureDetail(_ machine: String, _ list: [ScheduleTask]) {
        // Entered with no result, there is nothing to hold: the first one that comes is shown as it is.
        if capturedDetail, detailResultID == nil, case .schedule(let selectedMachine, let id) = viewing, selectedMachine == machine,
           let first = list.first(where: { $0.id == id })?.latestNotification {
            detailResultID = first.id
        }
        guard foreground, completeMachines.contains(machine), !capturedDetail, case .schedule(let selectedMachine, let id) = viewing,
              selectedMachine == machine, let task = list.first(where: { $0.id == id }) else { return }
        detailUnread = unreadRunIDs(machine: machine, task: task)
        detailResultID = task.latestNotification?.id
        capturedDetail = true
        detailReturnTime = nil
    }
    private func markViewing(_ machine: String, _ list: [ScheduleTask]) {
        guard foreground else { return }
        var ids = seen[machine] ?? []
        for task in list {
            for run in task.history where run.notified {
                let read: Bool
                switch viewing {
                case .schedule(let selectedMachine, let id): read = capturedDetail && completeMachines.contains(machine) && selectedMachine == machine && id == task.id
                case .session(let id, _): read = run.sessionID == id
                default: read = false
                }
                let id = token(task.id, run.id)
                if read && heldUnread[machine]?.contains(id) != true { ids.insert(id) }
            }
        }
        if ids != seen[machine] { saveSeen(machine, ids) }
    }
    func wasUnreadOnEntry(machine: String, task: String, run: String) -> Bool {
        viewing == .schedule(machine: machine, id: task) && detailUnread.contains(run)
    }
    var badgeCount: Int { tasks.filter { $0.task.visible && $0.task.group == .attention }.count }
    var hasNewResults: Bool { tasks.contains { $0.task.visible && hasNewResult($0) } }
    private(set) var tasks: [MachineSchedule] = []
    private(set) var errors: [String: String] = [:]
    private(set) var loading: Set<String> = []
    @ObservationIgnored private var restored: Set<String> = []
    @ObservationIgnored private var revisions: [String: UInt64] = [:]
    @ObservationIgnored private var latest = RemoteState()
    @ObservationIgnored private var calls: Calls?
    @ObservationIgnored private var jobs: [String: Task<Void, Never>] = [:]

    @ObservationIgnored private var resolutions: [String: [String: Task<Void, Never>]] = [:]

    func follow(_ remote: Remote) async {
        if calls == nil { calls = Calls(cached: { await remote.cachedSchedules($0) }, list: { try await remote.listSchedules($0) }, resolve: { _ = try? await remote.fetchSession($0) }) }
        for await state in remote.state.updates() {
            reconcile(state)
            if Task.isCancelled { break }
        }
        for job in jobs.values { job.cancel() }
        jobs.removeAll()
        for machine in resolutions.values { for job in machine.values { job.cancel() } }; resolutions.removeAll()
    }
    func configure(_ calls: Calls) { self.calls = calls }
    func online(_ id: String) -> Bool {
        latest.currentMachines.contains(id) && latest.conn == .online && latest.machines[id]?.online == true && latest.machines[id]?.meta?.capabilities.scheduledTasks == true && protocolMismatch(latest.machines[id]) == nil
    }
    func reconcile(_ state: RemoteState) {
        let reconnected = latest.conn != .online && state.conn == .online
        if reconnected { revisions.removeAll() }
        for (id, m) in state.machines where m.online && latest.machines[id]?.online != true { revisions[id] = nil }
        latest = state
        let supported = Set(state.machines.values.filter { state.currentMachines.contains($0.id) && $0.meta?.capabilities.scheduledTasks == true && protocolMismatch($0) == nil }.map(\.id))
        restored.formIntersection(supported)
        completeMachines.formIntersection(supported)
        heldUnread = heldUnread.filter { supported.contains($0.key) }
        for id in revisions.keys where !supported.contains(id) { revisions[id] = nil }
        for id in resolutions.keys where !supported.contains(id) { for job in resolutions[id]!.values { job.cancel() }; resolutions[id] = nil }
        tasks.removeAll { !supported.contains($0.machineID) }
        for id in errors.keys where !supported.contains(id) { errors[id] = nil }
        for id in jobs.keys where !supported.contains(id) { jobs[id]?.cancel(); jobs[id] = nil; readIDs[id] = nil; loading.remove(id) }
        for id in supported where jobs[id] == nil {
            let revision = state.machines[id]?.meta?.scheduleRevision ?? 0
            if !restored.contains(id) || (online(id) && revisions[id] != revision) { _ = beginRead(id) }
        }
    }
    @ObservationIgnored private var readIDs: [String: UUID] = [:]
    /// Every read, including explicit refresh after a mutation, owns the same machine slot.
    private func beginRead(_ id: String) -> Task<Void, Never>? {
        guard jobs[id] == nil, let calls else { return jobs[id] }
        let token = UUID(), revision = latest.machines[id]?.meta?.scheduleRevision ?? 0
        let needsRestore = !restored.contains(id)
        readIDs[id] = token; loading.insert(id)
        let job = Task {
            defer {
                if readIDs[id] == token {
                    loading.remove(id); jobs[id] = nil; readIDs[id] = nil
                    reconcile(latest)
                }
            }
            if needsRestore {
                let cached = await calls.cached(id)
                guard !Task.isCancelled else { return }
                restored.insert(id)
                if let cached { replace(id, cached, complete: false) }
            }
            guard online(id) else { return }
            do {
                let list = try await calls.list(id)
                guard !Task.isCancelled else { return }
                replace(id, list); revisions[id] = revision; errors[id] = nil
                for sid in Set(list.compactMap { $0.spec.target.session }) where latest.sessions[sid]?.info == nil && resolutions[id]?[sid] == nil {
                    resolutions[id, default: [:]][sid] = Task {
                        await calls.resolve(sid)
                        guard !Task.isCancelled else { return }
                        resolutions[id]?[sid] = nil
                    }
                }
            } catch {
                guard !Task.isCancelled else { return }
                errors[id] = errorText(error); revisions[id] = revision
            }
        }
        jobs[id] = job; return job
    }
    func refresh(_ id: String) async {
        // Recheck after each suspension: the finished read may have queued a revision catch-up.
        while let job = jobs[id] { await job.value }
        guard online(id), let job = beginRead(id) else { return }
        await job.value
    }
    private func replace(_ id: String, _ list: [ScheduleTask], complete: Bool = true) {
        guard latest.currentMachines.contains(id), latest.machines[id]?.meta?.capabilities.scheduledTasks == true else { return }
        observe(id, list, complete: complete)
        tasks.removeAll { $0.machineID == id }
        tasks += list.filter(\.visible).map { MachineSchedule(machineID: id, task: $0) }
    }
    func task(machine: String, id: String) -> ScheduleTask? { tasks.first { $0.machineID == machine && $0.task.id == id }?.task }
    #if DEBUG
    func show(_ tasks: [MachineSchedule], state: RemoteState) {
        latest = state
        for machine in Set(tasks.map(\.machineID)) { observe(machine, tasks.filter { $0.machineID == machine }.map(\.task)) }
        self.tasks = tasks
    }
    #endif
}

/// Kept for the account, including after a screen is left. Retrying an unknown mutation
/// sends the very same prepared command. Other writes stay blocked until it is resolved.
@MainActor @Observable
final class ScheduleMutation {
    private(set) var running = false
    private(set) var error = ""
    private(set) var frozen = false
    private(set) var expired = false
    private(set) var conflicts = 0
    @ObservationIgnored private let attempt = Attempt()
    func abandon() {
        guard !running else { return }
        attempt.abandon(); frozen = false; expired = false; error = ""
    }
    @discardableResult
    func execute(remote: Remote, machine: String, command: () throws -> PBCommand, refresh: () async -> Void) async throws -> PBCommandResult {
        guard !running, !expired else { throw UserError(L10nMessage.scheduleIosConfirmThePreviousChangeSResultFirst()) }
        running = true; error = ""
        defer { running = false }
        do {
            let retry = attempt.frozen
            let p = try attempt.next { try remote.prepare(machineId: machine, sessionId: "", command()) }
            let result = try await remote.forward(p, retryOfUnknown: retry)
            attempt.settle(); frozen = false
            await refresh()
            return result
        } catch {
            var shown = attempt.settle(error) ?? error
            frozen = attempt.frozen; expired = attempt.expired
            await refresh()
            if let conflict = recordConflict(error) { shown = conflict }
            self.error = errorText(shown)
            throw shown
        }
    }
    func recordConflict(_ error: Error) -> Error? {
        guard let command = error as? CommandError, command.fromDaemon, command.code == "revision_conflict" else { return nil }
        attempt.abandon(); frozen = false; expired = false
        conflicts += 1
        return UserError(L10nMessage.errorsTheTaskWasJustChangedUpdatedToTheLatest())
    }
    func run(remote: Remote, machine: String, command: @escaping () throws -> PBCommand, refresh: @escaping () async -> Void, done: @escaping (PBCommandResult) -> Void = { _ in }) {
        guard !running, !expired else { return }
        Task { if let result = try? await execute(remote: remote, machine: machine, command: command, refresh: refresh) { done(result) } }
    }

}
@MainActor final class ScheduleMutations {
    private var entries: [String: ScheduleMutation] = [:]
    func of(_ id: String) -> ScheduleMutation {
        if let x = entries[id] { return x }
        let x = ScheduleMutation(); entries[id] = x; return x
    }
}
extension AccountContext {
    var schedules: ScheduleStore { memory.value { ScheduleStore(defaults: preferenceDefaults, scope: [account.server, account.deviceId]) } }
    var scheduleMutations: ScheduleMutations { memory.value { ScheduleMutations() } }
}
