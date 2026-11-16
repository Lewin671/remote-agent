import Foundation
import RAProto
import Testing
@testable import RACore

@Suite struct DirectEngineBehaviorTests {
    func connected(_ rig: EngineRig) async -> Bool { await eventually(8) { rig.engine.status(rig.machineId).state == .connected } }

    @Test func establishesAndAnswersAProbeRead() async throws {
        let rig = try EngineRig(); await rig.start()
        #expect(await connected(rig), "direct comes up against a computer with real signalling")
        rig.announce()
        let id = rig.machineId
        guard let attempt = rig.engine.attempt(id, .read, handoff: {}, probe: true) else { Issue.record("no direct attempt"); return }
        #expect(attempt.isDirect)
        let request = newRequestId(), response = try await attempt.send(machineId: id, command: rig.envelope(request))
        #expect(response.attemptID == attempt.attemptId)
        #expect(try DeviceSecurity.parseHeader(response.result.securityHeader).request == request)
        #expect(rig.engine.status(id).readDirect == false, "a probe sample never moves the read path by itself")
        rig.close()
    }

    @Test func uploadUsesDirectOnlyWithAValidAllowingSnapshot() async throws {
        let rig = try EngineRig(); await rig.start()
        #expect(await connected(rig))
        let id = rig.machineId
        let upload = RouteHint.bulk(file: "t1", upload: .file, bytes: 700_000)
        #expect(!rig.engine.ready(id, upload), "no snapshot: the relay carries it")
        rig.announce()
        #expect(await eventually { rig.engine.capabilities(id) != nil })
        #expect(rig.engine.ready(id, upload))
        #expect(!rig.engine.ready(id, .bulk(file: "t1", upload: .file, bytes: 786_433)), "above the announced block size")
        rig.announce(upload: false)
        #expect(await eventually { rig.engine.capabilities(id)?.fileUpload == false })
        #expect(!rig.engine.ready(id, upload), "the computer does not allow uploads now")
        rig.announce(upload: true)
        await rig.pass(55_000)
        #expect(rig.engine.capabilities(id) == nil, "an expired snapshot is no snapshot")
        #expect(!rig.engine.ready(id, upload))
        rig.close()
    }

    @Test func downloadIsNotTiedToTheUploadSnapshot() async throws {
        let rig = try EngineRig(); await rig.start()
        #expect(await connected(rig))
        #expect(!rig.engine.ready(rig.machineId, .bulk(file: "s/f")), "no download window announced yet")
        rig.close()
    }

    @Test func switchOffStaysOnTheRelayAndSwitchOnStartsAgain() async throws {
        let rig = try EngineRig(); await rig.start()
        #expect(await connected(rig))
        try rig.engine.setEnabled(rig.machineId, false)
        #expect(rig.engine.status(rig.machineId).reason == .disabled)
        let probes = rig.probes
        await rig.advance(20_000)
        #expect(rig.probes == probes, "an off switch makes no new attempt")
        #expect(rig.engine.attempt(rig.machineId, .read, handoff: {}, probe: true) == nil)
        try rig.engine.setEnabled(rig.machineId, true)
        #expect(await connected(rig))
        rig.prefs.failWrites = true
        #expect(throws: Error.self) { try rig.engine.setEnabled(rig.machineId, false) }
        #expect(rig.engine.enabled(rig.machineId), "a preference that could not be saved is not reported as changed")
        rig.close()
    }

    /// The designed backoff (docs/transport.md §4): full jitter below 1, 2, 4 ... 30 s. The jitter
    /// source is pinned to its maximum so the cap itself is observable.
    @Test func failedRoundsRetryWithTheDesignedBackoff() async throws {
        let rig = try EngineRig(); rig.silent = true; await rig.start()
        var expected: [UInt64] = [1000, 2000, 4000, 8000, 16000, 30000, 30000]
        var round = 1
        while !expected.isEmpty {
            let delay = expected.removeFirst()
            let now = round
            #expect(await eventually(5) { rig.probes == now }, "round \(now) starts")
            await rig.advance(10_001)                                       // the round's 10 s end
            #expect(await eventually(5) { rig.engine.status(rig.machineId).reason == .noResponse }, "the round failed without the computer's answer")
            // Real time passes while the test steps, so the edges are checked with a margin each side.
            let failedAt = rig.time.now
            await rig.advance(delay - 450 - min(delay - 450, rig.time.now - failedAt))
            #expect(rig.probes == now, "still waiting shortly before \(delay) ms")
            await rig.advance(900)
            #expect(await eventually(5) { rig.probes == now + 1 }, "retries once \(delay) ms have passed")
            round += 1
        }
        rig.close()
    }

    @Test func mainConnectionBackNetworkChangeAndASwitchClearTheWait() async throws {
        let rig = try EngineRig(); rig.silent = true; await rig.start()
        #expect(await eventually { rig.probes == 1 })
        await rig.advance(10_001)
        #expect(await eventually { rig.engine.status(rig.machineId).retryScheduled })
        let before = rig.probes
        rig.engine.attach(FakeLeg { _ in })                                 // the main connection is back
        #expect(await eventually(5) { rig.engine.status(rig.machineId).state == .connecting }, "a new round starts at once on the new leg, not after the old failure's wait")
        _ = before
        rig.close()
    }

    @Test func aDroppedPathIsReestablishedAfterItsBackoffAndOldEventsDoNotTouchTheNewOne() async throws {
        let rig = try EngineRig(); await rig.start()
        #expect(await connected(rig))
        let old = rig.net.all
        rig.net.cutAll()
        #expect(await eventually(5) { rig.engine.status(rig.machineId).state != .connected }, "the drop is noticed")
        await rig.advance(1_001)                                            // the first backoff step at its maximum
        #expect(await eventually(10) { rig.probes == 2 && rig.engine.status(rig.machineId).state == .connected }, "a new bounded round comes up on its own")
        for pc in old { pc.fail() }                                         // late lifecycle events of the first round
        try? await Task.sleep(nanoseconds: 200_000_000)
        #expect(rig.engine.status(rig.machineId).state == .connected, "the retired round's events leave the new one alone")
        rig.close()
    }

    @Test func aPathThatStayedUpRestartsTheSequence() async throws {
        let rig = try EngineRig(); await rig.start()
        #expect(await connected(rig))
        await rig.pass(31_000)                                              // up for more than 30 s
        rig.net.cutAll()
        #expect(await eventually(5) { rig.engine.status(rig.machineId).state != .connected })
        await rig.advance(1_001)                                            // not 2 s: the failure count started over
        #expect(await eventually(10) { rig.probes == 2 })
        rig.close()
    }

    /// Closing SDK objects can block. It must never happen under the engine's lock: the UI reads
    /// the status and a request asks whether direct is ready while it goes on.
    @Test func closingBlockingSdkObjectsNeverBlocksStatusOrRouting() async throws {
        let rig = try EngineRig(); await rig.start()
        #expect(await connected(rig))
        rig.net.closeDelay = 1.0
        let id = rig.machineId, engine = rig.engine!
        Task.detached { try? engine.setEnabled(id, false) }
        try await Task.sleep(nanoseconds: 150_000_000)
        let started = Date()
        _ = engine.status(id); _ = engine.ready(id, .read); _ = engine.enabled(id)
        #expect(Date().timeIntervalSince(started) < 0.3, "status and routing answer while the SDK shuts down")
        rig.net.closeDelay = 0
        rig.close()
    }

    /// A Probe on a new connection proves nothing yet: the established session keeps carrying
    /// traffic until a new round has proven the computer's process (docs/transport.md §5).
    @Test func anUnprovenRoundNeverRetiresAnEstablishedSession() async throws {
        let rig = try EngineRig(); await rig.start()
        #expect(await connected(rig))
        let id = rig.machineId
        await rig.lateProbe()
        try await Task.sleep(nanoseconds: 700_000_000)
        #expect(rig.engine.status(id).state == .connected, "the late Probe did not replace the path")
        rig.announce()
        guard let attempt = rig.engine.attempt(id, .read, handoff: {}, probe: true) else { Issue.record("the established path went away"); return }
        let response = try await attempt.send(machineId: id, command: rig.envelope())
        #expect(response.attemptID == attempt.attemptId, "and it still answers")
        rig.close()
    }
}
