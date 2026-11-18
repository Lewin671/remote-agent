import Foundation
import RAProto
import Testing
@testable import RACore

final class Flag: @unchecked Sendable { private let l = NSLock(); private var v = false; func set() { l.withLock { v = true } }; var isSet: Bool { l.withLock { v } } }

@Suite struct DirectEngineBehaviorTests {
    func connected(_ rig: EngineRig) async -> Bool {
        await eventually(8) {
            rig.engine.status(rig.machineId).state == .connected &&
                rig.engine.ready(rig.machineId, .read) &&
                rig.net.all.filter { $0.isConnectedAndOpen }.count >= 4
        }
    }

    @Test func establishesAndAnswersAProbeRead() async throws {
        let rig = try EngineRig(); defer { rig.close() }
        await rig.start()
        #expect(await connected(rig), "direct comes up against a computer with real signalling")
        rig.announce()
        let id = rig.machineId
        guard let attempt = rig.engine.attempt(id, .read, handoff: {}, probe: true) else { Issue.record("no direct attempt"); return }
        #expect(attempt.isDirect)
        let request = newRequestId(), response = try await attempt.send(machineId: id, command: rig.envelope(request))
        #expect(response.attemptID == attempt.attemptId)
        #expect(try DeviceSecurity.parseHeader(response.result.securityHeader).request == request)
        #expect(rig.engine.status(id).readDirect == false, "a probe sample never moves the read path by itself")
    }

    @Test func uploadUsesDirectOnlyWithAValidAllowingSnapshot() async throws {
        let rig = try EngineRig(); defer { rig.close() }
        await rig.start()
        #expect(await connected(rig))
        let id = rig.machineId
        let upload = RouteHint.bulk(file: "t1", upload: .file, bytes: 700_000)
        #expect(!rig.engine.ready(id, upload), "no snapshot: the relay carries it")
        rig.announce()
        #expect(await eventually { rig.engine.capabilities(id) != nil && rig.engine.ready(id, upload) })
        #expect(!rig.engine.ready(id, .bulk(file: "t1", upload: .file, bytes: 786_433)), "above the announced block size")
        rig.announce(upload: false)
        #expect(await eventually { rig.engine.capabilities(id)?.fileUpload == false })
        #expect(!rig.engine.ready(id, upload), "the computer does not allow uploads now")
        rig.announce(upload: true)
        await rig.pass(55_000)
        #expect(rig.engine.capabilities(id) == nil, "an expired snapshot is no snapshot")
        #expect(!rig.engine.ready(id, upload))
    }

    @Test func downloadIsNotTiedToTheUploadSnapshot() async throws {
        let rig = try EngineRig(); defer { rig.close() }
        await rig.start()
        #expect(await connected(rig))
        #expect(!rig.engine.ready(rig.machineId, .bulk(file: "s/f")), "no download window announced yet")
    }

    @Test func switchOffStaysOnTheRelayAndSwitchOnStartsAgain() async throws {
        let rig = try EngineRig(); defer { rig.close() }
        await rig.start()
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
    }

    /// The designed backoff (docs/transport.md §4): full jitter below 1, 2, 4 ... 30 s. The jitter
    /// source is pinned to its maximum so the cap itself is observable.
    @Test func failedRoundsRetryWithTheDesignedBackoff() async throws {
        let rig = try EngineRig(); defer { rig.close() }
        rig.silent = true; await rig.start()
        var expected: [UInt64] = [1000, 2000, 4000, 8000, 16000, 30000, 30000]
        var round = 1
        while !expected.isEmpty {
            let delay = expected.removeFirst()
            let now = round
            #expect(await rig.settle { rig.probes == now }, "round \(now) starts")
            await rig.advance(10_001)                                       // the round's 10 s end
            #expect(await rig.settle { rig.engine.status(rig.machineId).reason == .noResponse }, "the round failed without the computer's answer")
            await rig.advance(delay - 200)
            #expect(rig.probes == now, "still waiting shortly before \(delay) ms")
            await rig.advance(400)
            #expect(await rig.settle { rig.probes == now + 1 }, "retries once \(delay) ms have passed")
            round += 1
        }
    }

    @Test func mainConnectionBackNetworkChangeAndASwitchClearTheWait() async throws {
        let rig = try EngineRig(); defer { rig.close() }
        rig.silent = true; await rig.start()
        #expect(await rig.settle { rig.probes == 1 })
        await rig.advance(10_001)
        #expect(await rig.settle { rig.engine.status(rig.machineId).retryScheduled })
        rig.engine.attach(FakeLeg { _ in })                                 // the main connection is back
        // Virtual clock is stationary: a round that starts without advancing the clock cleared the 1 s wait.
        #expect(await eventually(5) { rig.engine.status(rig.machineId).state == .connecting }, "a new round starts at once on the new leg, not after the old failure's wait")
    }

    @Test func aDroppedPathIsReestablishedAfterItsBackoffAndOldEventsDoNotTouchTheNewOne() async throws {
        let rig = try EngineRig(); defer { rig.close() }
        await rig.start()
        #expect(await connected(rig))
        let old = rig.net.all
        rig.net.cutAll()
        #expect(await eventually(5) { rig.engine.status(rig.machineId).retryScheduled }, "the drop is noticed and backoff is scheduled")
        await rig.advance(1_001)                                            // the first backoff step at its maximum
        #expect(await eventually(10) { rig.probes == 2 && rig.engine.status(rig.machineId).state == .connected }, "a new bounded round comes up on its own")
        for pc in old { pc.fail() }                                         // late lifecycle events of the first round
        _ = await rig.settle { true }
        #expect(rig.engine.status(rig.machineId).state == .connected, "the retired round's events leave the new one alone")
    }

    @Test func aPathThatStayedUpRestartsTheSequence() async throws {
        let rig = try EngineRig(); defer { rig.close() }
        await rig.start()
        #expect(await connected(rig))
        await rig.pass(31_000)                                              // up for more than 30 s
        rig.net.cutAll()
        #expect(await eventually(5) { rig.engine.status(rig.machineId).retryScheduled })
        await rig.advance(1_001)                                            // not 2 s: the failure count started over
        #expect(await eventually(10) { rig.probes == 2 })
    }

    /// Closing SDK objects can block. It must never happen under the engine's lock: the UI reads
    /// the status and a request asks whether direct is ready while it goes on.
    @Test func closingBlockingSdkObjectsNeverBlocksStatusOrRouting() async throws {
        let rig = try EngineRig(); defer { rig.net.releaseClose(); rig.close() }
        await rig.start()
        #expect(await connected(rig))
        rig.net.holdClose()
        let id = rig.machineId, engine = rig.engine!
        DispatchQueue.global().async { try? engine.setEnabled(id, false) }
        #expect(await eventually(5) { rig.net.closeEntered > 0 }, "SDK close is actively blocked")
        let answered = Flag()
        DispatchQueue.global().async { _ = engine.status(id); _ = engine.ready(id, .read); _ = engine.enabled(id); answered.set() }
        #expect(await eventually(5) { answered.isSet }, "status and routing answer while the SDK shuts down")
        rig.net.releaseClose()
    }

    /// A Probe on a new connection proves nothing yet: the established session keeps carrying
    /// traffic until a new round has proven the computer's process (docs/transport.md §5).
    @Test func anUnprovenRoundNeverRetiresAnEstablishedSession() async throws {
        let rig = try EngineRig(); defer { rig.close() }
        await rig.start()
        #expect(await connected(rig))
        let id = rig.machineId
        await rig.lateProbe()
        _ = await rig.settle { true }
        #expect(rig.engine.status(id).state == .connected, "the late Probe did not replace the path")
        rig.announce()
        guard let attempt = rig.engine.attempt(id, .read, handoff: {}, probe: true) else { Issue.record("the established path went away"); return }
        let response = try await attempt.send(machineId: id, command: rig.envelope())
        #expect(response.attemptID == attempt.attemptId, "and it still answers")
    }

    /// One use of an established round fails and is rebuilt inside the same session (a new PC
    /// generation). The old PC's channels close late, while the new ones are already up: that
    /// late lifecycle event belongs to a retired object and must change nothing, in particular it
    /// must not schedule a redial of the whole round.
    @Test func aRetiredPcsLateClosingNeverTouchesItsSuccessor() async throws {
        let rig = try EngineRig(); defer { rig.net.releaseClose(); rig.close() }
        await rig.start()
        #expect(await connected(rig))
        rig.announce()
        #expect(await eventually(5) {
            rig.engine.ready(rig.machineId, .read) &&
                rig.engine.ready(rig.machineId, .bulk(file: "t1", upload: .file, bytes: 1024))
        })
        let first = rig.net.all[0], second = first.peer!
        rig.net.holdClose()                                                 // hold old PC shutdown while replacement comes up
        DispatchQueue.global().async { first.fail() }
        DispatchQueue.global().async { second.fail() }
        #expect(await eventually(5) { rig.net.closeEntered >= 2 }, "old PCs entered shutdown")
        let beforeRelease = rig.net.closeFinished
        rig.net.releaseClose()                                              // the old PCs finish closing now
        #expect(await eventually(8) {
            rig.net.all.count >= 6 &&
                rig.net.all.dropFirst(4).allSatisfy { $0.isConnectedAndOpen } &&
                rig.engine.ready(rig.machineId, .read) &&
                rig.engine.ready(rig.machineId, .bulk(file: "t1", upload: .file, bytes: 1024)) &&
                rig.engine.status(rig.machineId).state == .connected &&
                rig.net.closeFinished >= beforeRelease + 2
        }, "the use is rebuilt in the same session after old PCs close")
        await rig.advance(2_001)                                            // past any wait a late closing could have scheduled
        _ = await rig.settle { true }
        #expect(rig.probes == 1, "no new round was started for a failure that had already been repaired")
        let id = rig.machineId
        #expect(rig.engine.status(id).state == .connected && rig.engine.status(id).reason == nil)
        guard let attempt = rig.engine.attempt(id, .read, handoff: {}, probe: true) else { Issue.record("the repaired path is gone"); return }
        let response = try await attempt.send(machineId: id, command: rig.envelope())
        #expect(response.attemptID == attempt.attemptId, "and the repaired path carries a request")
    }

    /// The engine asks for its warm-up samples through a hook the Remote installs; without it the
    /// read path never saw a direct sample and so never moved off the relay (found by the first
    /// run against a real daemon).
    @Test func theRemoteInstallsTheWarmUpProbeHook() throws {
        let rig = try EngineRig()
        let account = Account(server: "http://127.0.0.1:1", root: Data(repeating: 1, count: 32), deviceSeed: Data(repeating: 0x11, count: 32), deviceId: rig.clientId, accountId: rig.record.pin.account)
        let with = try Remote(account: account, http: HTTP(), securityStorage: MemorySecurityStore(rig.record), rtc: FakeRtcFactory(net: FakeRtcNet()))
        #expect(with.direct?.probe != nil, "the warm-up has a way to take its samples")
        #expect(with.direct?.eventHandler != nil, "the direct engine routes PayloadKind.event pushes to Remote")
        let without = try Remote(account: account, http: HTTP(), securityStorage: MemorySecurityStore(rig.record))
        #expect(without.direct == nil && !without.directSupported)
    }
}
