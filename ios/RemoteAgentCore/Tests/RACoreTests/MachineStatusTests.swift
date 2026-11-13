import Foundation
import RAProto
import Testing
@testable import RACore

private enum StatusFailure: Error { case offline }
private actor StatusReads {
    var count = 0
    var block: CheckedContinuation<PBMachineStatus, Never>?
    let gated: Bool
    init(gated: Bool = false) { self.gated = gated }
    func read() async throws -> PBMachineStatus {
        count += 1
        if gated { return await withCheckedContinuation { block = $0 } }
        if (2...4).contains(count) { throw StatusFailure.offline }
        var status = PBMachineStatus(); status.observedMs = Int64(count); return status
    }
    func release() { block?.resume(returning: PBMachineStatus()); block = nil }
}
@MainActor private func statusEventually(_ test: () -> Bool) async -> Bool {
    for _ in 0..<10_000 { if test() { return true }; await Task.yield() }
    return false
}
@Suite struct MachineStatusTests {
    @MainActor @Test func lifecycle() async throws {
        let reads = StatusReads()
        let p = MachineStatusController(interval: .seconds(3600), fetch: { try await reads.read() })
        p.setActive(visible: true, online: true, supported: false)
        #expect(await reads.count == 0)
        p.setActive(visible: true, online: true, supported: true)
        #expect(await statusEventually { p.state == .live })
        for n in 1...3 { p.retry(); #expect(await statusEventually { p.failures == n }) }
        #expect(p.state == .stale); #expect(p.last?.observedMs == 1)
        p.retry(); #expect(await statusEventually { p.last?.observedMs == 5 })
        #expect(p.state == .live); #expect(p.curveSinceMs == 5)
        p.setActive(visible: false, online: true, supported: true)
        p.retry(); await Task.yield(); #expect(await reads.count == 5)
        p.setActive(visible: true, online: true, supported: true)
        #expect(await statusEventually { p.last?.observedMs == 6 }); #expect(p.curveSinceMs == 5)
        p.dispose()
    }
    @MainActor @Test func noOverlapOrHiddenPublication() async {
        let reads = StatusReads(gated: true)
        let p = MachineStatusController(fetch: { try await reads.read() })
        p.setActive(visible: true, online: true, supported: true)
        for _ in 0..<100 { await Task.yield() }
        p.retry(); p.retry(); #expect(await reads.count == 1)
        p.setActive(visible: false, online: true, supported: true)
        await reads.release()
        for _ in 0..<100 { await Task.yield() }
        #expect(p.last == nil); p.dispose()
    }
    @MainActor @Test func firstFailure() async {
        let p = MachineStatusController(interval: .seconds(3600), fetch: { throw StatusFailure.offline })
        p.setActive(visible: true, online: true, supported: true)
        #expect(await statusEventually { p.failures == 1 })
        for n in 2...3 { p.retry(); #expect(await statusEventually { p.failures == n }) }
        #expect(p.state == .unavailable); #expect(p.last == nil); p.dispose()
    }
    @Test func rankingsAndMachineHelpers() {
        var a = PBResourceUsage(); a.id = "a"; a.cpuPercent = 10; a.memoryBytes = 100_000_000
        var b = PBResourceUsage(); b.id = "b"; b.cpuPercent = 12; b.memoryBytes = 150_000_000
        for memory in [false, true] { #expect(stableUsage(previous: ["a", "b"], rows: [b,a], memory: memory)[0].id == "a") }
        b.cpuPercent += 0.01; b.memoryBytes += 1
        for memory in [false, true] { #expect(stableUsage(previous: ["a", "b"], rows: [a,b], memory: memory)[0].id == "b") }
        var meta = PBMachineMeta(); meta.capabilities.machineStatus = true; meta.diskStatus.availableBytes = 9_000_000_000
        var old = PBProjectSummary(); old.path = "old"; old.latestCreatedMs = 1
        var new = PBProjectSummary(); new.path = "new"; new.latestEndedMs = 2
        meta.projects = [old,new]
        #expect(machineDiskReminder(online: false, meta: meta) == nil)
        #expect(machineDiskReminder(online: true, meta: meta) != nil)
        #expect(recentMachineProject(meta) == "new")
        #expect(isDiskFull(CommandError("disk_full", "fixture")))
        #expect(!isDiskFull(CommandError("internal", "no space left on device")))
        var cmd = PBCommand(); cmd.getMachineStatus = PBGetMachineStatusRequest()
        #expect(cmd.isReadOnlyCommand)
    }
}

private final class StatusTestClock: @unchecked Sendable {
    private let lock=NSLock()
    private var value: Duration = .zero
    private var waits: [UUID:(Duration,CheckedContinuation<Void,Error>)] = [:]
    var now: Duration { lock.lock();defer {lock.unlock()};return value }
    func advance(_ duration: Duration) { lock.lock();value += duration;let due=waits.filter { $0.value.0 <= value };for (id,_) in due { waits.removeValue(forKey:id) };lock.unlock();for (_,item) in due { item.1.resume() } }
    func sleep(_ duration: Duration) async throws {
        if duration <= .zero { return }
        let id=UUID()
        try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { (c:CheckedContinuation<Void,Error>) in
                lock.lock();if Task.isCancelled {lock.unlock();c.resume(throwing:CancellationError())} else { waits[id]=(value+duration,c);lock.unlock() }
            }
        } onCancel: { self.cancel(id) }
    }
    private func cancel(_ id:UUID){lock.lock();let c=waits.removeValue(forKey:id)?.1;lock.unlock();c?.resume(throwing:CancellationError())}
    var pending: Int { lock.lock();defer{lock.unlock()};return waits.count }
}
private actor ReviewStatusSource {
 var count=0;var hung=false
 let clock:StatusTestClock
 init(_ clock:StatusTestClock){self.clock=clock}
 func hang(){hung=true}
 func read()async throws -> PBMachineStatus {count+=1;if hung{try await clock.sleep(.seconds(100))};var s=PBMachineStatus();s.observedMs=Int64(count);return s}
}
@Suite struct MachineStatusReviewTests {
 @MainActor @Test func actualCadenceOfflineAndTimeout() async {
  let clock=StatusTestClock()
  // Use the same injected clock for request deadlines and repeat ticks.
  let live=ReviewStatusSource(clock)
  let p=MachineStatusController(fetch:{try await live.read()},now:{clock.now},sleep:{try await clock.sleep($0)})
  p.setActive(visible:true,online:true,supported:true)
  #expect(await statusEventually {p.last?.observedMs == 1 && clock.pending == 1})
  clock.advance(.milliseconds(1999));await Task.yield();#expect(await live.count == 1)
  clock.advance(.milliseconds(1));#expect(await statusEventually {p.last?.observedMs == 2 && clock.pending == 1})
  p.setActive(visible:true,online:false,supported:true);#expect(p.state == .stale && p.last?.observedMs == 2)
  clock.advance(.seconds(20));await Task.yield();#expect(await live.count == 2)
  await live.hang();p.setActive(visible:true,online:true,supported:true)
  #expect(await statusEventually {clock.pending >= 2})
  for failure in 1...3 {clock.advance(.seconds(15));#expect(await statusEventually {p.failures == failure && clock.pending >= 2})}
  #expect(p.state == .stale);p.dispose()
 }
 @Test func rankingMembership() {
  let rows=zip(["a","b","new","c"],[5.0,10,30,25]).map { id,cpu in var r=PBResourceUsage();r.id=id;r.cpuPercent=cpu;return r }
  #expect(stableUsage(previous:["gone","a","b","c"],rows:rows).map(\.id) == ["new","c","b","a"])
 }
}
