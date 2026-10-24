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
        latest.conn == .online && latest.machines[id]?.online == true && latest.machines[id]?.meta?.capabilities.scheduledTasks == true && protocolMismatch(latest.machines[id]) == nil
    }
    func reconcile(_ state: RemoteState) {
        let reconnected = latest.conn != .online && state.conn == .online
        if reconnected { revisions.removeAll() }
        for (id, m) in state.machines where m.online && latest.machines[id]?.online != true { revisions[id] = nil }
        latest = state
        let supported = Set(state.machines.values.filter { $0.meta?.capabilities.scheduledTasks == true && protocolMismatch($0) == nil }.map(\.id))
        for id in revisions.keys where !supported.contains(id) { revisions[id] = nil; restored.remove(id) }
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
                if let cached { replace(id, cached) }
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
    private func replace(_ id: String, _ list: [ScheduleTask]) {
        guard latest.machines[id]?.meta?.capabilities.scheduledTasks == true else { return }
        tasks.removeAll { $0.machineID == id }
        tasks += list.filter(\.visible).map { MachineSchedule(machineID: id, task: $0) }
    }
    func task(machine: String, id: String) -> ScheduleTask? { tasks.first { $0.machineID == machine && $0.task.id == id }?.task }
    #if DEBUG
    func show(_ tasks: [MachineSchedule], state: RemoteState) { latest = state; self.tasks = tasks }
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
        guard !running, !expired else { throw UserError("请先确认上一条修改的结果") }
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
        guard (error as? CommandError)?.code == "aborted" else { return nil }
        attempt.abandon(); frozen = false; expired = false
        conflicts += 1
        return UserError("任务刚被修改，已更新为最新内容")
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
    var schedules: ScheduleStore { memory.value { ScheduleStore() } }
    var scheduleMutations: ScheduleMutations { memory.value { ScheduleMutations() } }
}
