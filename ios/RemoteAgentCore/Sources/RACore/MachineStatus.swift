import Foundation
import RAProto

extension Remote {
    public func getMachineStatus(_ machineID: String) async throws -> PBMachineStatus {
        var cmd = PBCommand(); cmd.getMachineStatus = PBGetMachineStatusRequest()
        let result = try await machineCommand(machineID, cmd)
        guard case .machineStatus(let status)? = result.kind else {
            throw CommandError("unavailable", L10nMessage.machineStatusUnavailable())
        }
        return status
    }
}
public func isDiskFull(_ error: Error) -> Bool { (error as? CommandError)?.code == "disk_full" }
public func recentMachineProject(_ meta: PBMachineMeta) -> String? {
    meta.projects.max { max($0.latestCreatedMs, $0.latestEndedMs) < max($1.latestCreatedMs, $1.latestEndedMs) }?.path
}
public func machineDiskReminder(online: Bool, meta: PBMachineMeta) -> PBDiskStatus? {
    online && meta.capabilities.machineStatus && meta.hasDiskStatus && meta.diskStatus.availableBytes < 10_000_000_000 ? meta.diskStatus : nil
}
public func stableUsage(previous: [String], rows: [PBResourceUsage], memory: Bool = false) -> [PBResourceUsage] {
    var byID = Dictionary(rows.map { ($0.id, $0) }, uniquingKeysWith: { _, last in last })
    var ordered = previous.compactMap { byID.removeValue(forKey: $0) }
    func metric(_ r: PBResourceUsage) -> Double { memory ? Double(r.memoryBytes) : r.cpuPercent }
    ordered += byID.values.sorted { metric($0) == metric($1) ? $0.id < $1.id : metric($0) > metric($1) }
    let threshold = memory ? 50_000_000.0 : 2.0
    for i in ordered.indices.dropFirst() {
        var j = i
        while j > 0 && metric(ordered[j]) - metric(ordered[j-1]) > threshold {
            ordered.swapAt(j, j-1); j -= 1
        }
    }
    return ordered
}
public enum MachinePollState: Sendable { case idle, loading, live, stale, unavailable }
/// Pure logic owned by a screen coordinator, with no view dependency. Feed all
/// visibility/presence/capability changes to setActive. One fetch at a time;
/// late replies cannot revive a hidden page. Stale values remain available.
@MainActor public final class MachineStatusController {
    public private(set) var state: MachinePollState = .idle
    public private(set) var last: PBMachineStatus?
    public private(set) var failures = 0
    public private(set) var curveSinceMs: Int64 = 0
    private let fetch: @Sendable () async throws -> PBMachineStatus
    private let changed: @MainActor () -> Void
    public static let requestTimeout: Duration = .seconds(15)
    private let now: @Sendable () -> Duration
    private let sleep: @Sendable (Duration) async throws -> Void
    private let interval: Duration
    private var active = false
    private var generation = 0
    private var busy = false
    private var timer: Task<Void, Never>?
    public init(interval: Duration = .seconds(2), fetch: @escaping @Sendable () async throws -> PBMachineStatus, changed: @escaping @MainActor () -> Void = {}, now: @escaping @Sendable () -> Duration = { .nanoseconds(Int64(DispatchTime.now().uptimeNanoseconds)) }, sleep: @escaping @Sendable (Duration) async throws -> Void = { try await Task.sleep(for:$0) }) {
        self.interval = interval; self.fetch = fetch; self.changed = changed; self.now=now; self.sleep=sleep
    }
    public func setActive(visible: Bool, online: Bool, supported: Bool) {
        let next = visible && online && supported
        if visible && supported && !online { state = last == nil ? .unavailable : .stale; changed() }
        guard next != active else { return }
        active = next; generation += 1; timer?.cancel(); timer = nil
        if next { poll() } else { changed() }
    }
    public func retry() { timer?.cancel(); timer = nil; poll() }
    public func dispose() { setActive(visible: false, online: false, supported: false) }
    private func poll() {
        guard active && !busy else { return }
        busy = true
        let current = generation
        let started = now()
        if last == nil && state == .idle { state = .loading }; changed()
        Task { [weak self, fetch, sleep] in
            let result: Result<PBMachineStatus, Error>
            do { result = .success(try await statusRequest(fetch:fetch,sleep:sleep)) } catch { result = .failure(error) }
            guard let self else { return }
            self.busy = false
            if self.active && current == self.generation {
                switch result {
                case .success(let value):
                    if self.state == .stale || self.state == .unavailable { self.curveSinceMs = value.observedMs }
                    self.last = value; self.failures = 0; self.state = .live
                case .failure:
                    self.failures = min(3, self.failures + 1)
                    if self.last == nil { self.state = .unavailable }
                    if self.failures == 3 { self.state = self.last == nil ? .unavailable : .stale }
                }
            }
            self.changed()
            if self.active {
                let wait = current == self.generation ? max(.zero,self.interval-(self.now()-started)) : .zero
                self.timer = Task { [weak self] in
                    do { try await sleep(wait) } catch { return }
                    self?.poll()
                }
            }
        }
    }
}

private struct MachineStatusTimeout: Error {}
private func statusRequest(fetch: @escaping @Sendable () async throws -> PBMachineStatus, sleep: @escaping @Sendable (Duration) async throws -> Void) async throws -> PBMachineStatus {
    let (stream,continuation)=AsyncStream<Result<PBMachineStatus,Error>>.makeStream()
    let request=Task { do { continuation.yield(.success(try await fetch())) } catch { continuation.yield(.failure(error)) } }
    let timeout=Task { do { try await sleep(.seconds(15));continuation.yield(.failure(MachineStatusTimeout())) } catch {} }
    defer { request.cancel();timeout.cancel();continuation.finish() }
    var iterator=stream.makeAsyncIterator()
    guard let result=await iterator.next() else { throw CancellationError() }
    return try result.get()
}
