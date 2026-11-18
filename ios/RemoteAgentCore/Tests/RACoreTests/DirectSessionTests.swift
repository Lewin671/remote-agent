import Foundation
import RAProto
import Testing
@testable import RACore

/// A client and a computer session wired through memory: the directed frames cross as the relay
/// would carry them, the PeerConnections are fakes.
final class SessionPair: @unchecked Sendable {
    typealias W = PeerWire
    let net = FakeRtcNet(), clientBudget = DirectBudget(), machineBudget = DirectBudget()
    let time: FakeClock
    let client: W.Identity, machine: W.Identity
    var clientSession: DirectSession!, machineSession: DirectSession!
    private let lock = NSLock()
    private let waitersLock = NSLock()
    private var _clientPaths: [Int: DirectChannel] = [:], _machinePaths: [Int: DirectChannel] = [:]
    private var _machineFrames: [(Int, W.Bytes)] = [], _clientFrames: [(Int, W.Bytes)] = [], _clientClosed: [Int?] = []
    private var _clientControl15Count = 0, _machineUnsupportedCount = 0
    private var stateWaiters: [(id: UUID, predicate: @Sendable () -> Bool, cont: CheckedContinuation<Void, Never>)] = []
    var dropFrames = false
    var clientPaths: [Int: DirectChannel] { lock.withLock { _clientPaths } }
    var machinePaths: [Int: DirectChannel] { lock.withLock { _machinePaths } }
    var machineFrames: [(Int, W.Bytes)] { lock.withLock { _machineFrames } }
    var clientFrames: [(Int, W.Bytes)] { lock.withLock { _clientFrames } }
    var clientClosed: [Int?] { lock.withLock { _clientClosed } }
    var clientControl15Count: Int { lock.withLock { _clientControl15Count } }
    var machineUnsupportedCount: Int { lock.withLock { _machineUnsupportedCount } }

    init(deadlineMs: UInt64 = 10_000) throws {
        client = try PeerSignalVectorsTests.identity("client", 1); machine = try PeerSignalVectorsTests.identity("machine", 2)
        let codec = PeerCodec(scope: PeerSignalVectorsTests.scope), factory = FakeRtcFactory(net: net)
        let now = UInt64(Date().timeIntervalSince1970 * 1000), connection = W.random(16)
        time = FakeClock(now)
        func options(_ me: W.Identity, _ other: W.Public, _ budget: DirectBudget, isClient: Bool) -> DirectSessionOptions {
            DirectSessionOptions(
                selfID: me, peer: other, codec: codec, connection: connection, deadline: now + deadlineMs, budget: budget, stun: ["stun:127.0.0.1:3478"], factory: factory,
                valid: { true }, authorize: {},
                send: { [unowned self] out in
                    if let recipient = isClient ? self.clientSession?.peer : self.machineSession?.peer,
                       let o = try? codec.openSent(out.raw, me, recipient, self.time.clock.now()), o.ctx.cls == 2 {
                        if isClient && o.type == 15 {
                            self.lock.withLock { self._clientControl15Count += 1 }
                            self.signalStateChange()
                        } else if !isClient && o.type == 10 {
                            self.lock.withLock { self._machineUnsupportedCount += 1 }
                            self.signalStateChange()
                        }
                    }
                    if self.dropFrames { return true }
                    (isClient ? self.machineSession : self.clientSession)?.receive(out.raw); return true
                },
                chosen: {}, beforeOffer: {},
                ready: { [unowned self] use, path in
                    self.lock.withLock { if isClient { self._clientPaths[use] = path } else { self._machinePaths[use] = path } }
                    self.signalStateChange()
                },
                closed: { [unowned self] use in
                    if isClient {
                        self.lock.withLock { self._clientClosed.append(use) }
                        self.signalStateChange()
                    }
                },
                frame: { [unowned self] raw, use in
                    self.lock.withLock { if isClient { self._clientFrames.append((use, raw)) } else { self._machineFrames.append((use, raw)) } }
                    self.signalStateChange()
                },
                capability: { _ in },
                clock: time.clock)
        }
        clientSession = try DirectSession(options(client, W.member(machine.peer), clientBudget, isClient: true))
        machineSession = try DirectSession(options(machine, W.member(client.peer), machineBudget, isClient: false))
        net.onEvent = { [weak self] in self?.signalStateChange() }
    }
    private func signalStateChange() {
        let ready = waitersLock.withLock { () -> [CheckedContinuation<Void, Never>] in
            var fulfilled: [CheckedContinuation<Void, Never>] = []
            var remaining: [(id: UUID, predicate: @Sendable () -> Bool, cont: CheckedContinuation<Void, Never>)] = []
            for w in stateWaiters {
                if w.predicate() { fulfilled.append(w.cont) } else { remaining.append(w) }
            }
            stateWaiters = remaining
            return fulfilled
        }
        for c in ready { c.resume() }
    }
    func waitForState(_ predicate: @escaping @Sendable () -> Bool) async {
        let waiterID = UUID()
        await withTaskCancellationHandler {
            await withCheckedContinuation { (cont: CheckedContinuation<Void, Never>) in
                let resumeNow = waitersLock.withLock { () -> Bool in
                    if predicate() || Task.isCancelled { return true }
                    stateWaiters.append((waiterID, predicate, cont))
                    return false
                }
                if resumeNow { cont.resume() }
            }
        } onCancel: {
            let cont = waitersLock.withLock { () -> CheckedContinuation<Void, Never>? in
                guard let idx = stateWaiters.firstIndex(where: { $0.id == waiterID }) else { return nil }
                return stateWaiters.remove(at: idx).cont
            }
            cont?.resume()
        }
    }
    func waitForBudgetEmpty() async {
        await clientBudget.waitUntilEmptyForTest()
        await machineBudget.waitUntilEmptyForTest()
    }
    func close() {
        clientSession.close(); machineSession.close()
        let leftover = waitersLock.withLock { () -> [CheckedContinuation<Void, Never>] in
            let out = stateWaiters.map(\.cont)
            stateWaiters.removeAll()
            return out
        }
        for c in leftover { c.resume() }
    }
}

@Suite(.timeLimit(.minutes(2))) struct DirectSessionTests {
    @Test func handshakeOpensBothUsesAndCarriesAFragmentedMessage() async throws {
        let pair = try SessionPair()
        defer { pair.close() }
        await pair.clientSession.start()
        await pair.clientSession.tickForTest()
        await pair.waitForState { pair.clientPaths.count == 2 && pair.machinePaths.count == 2 }
        #expect(pair.clientPaths.count == 2 && pair.machinePaths.count == 2, "both PeerConnections ready on both sides")
        #expect(pair.clientSession.established)
        var bigData = PeerWire.random(1 << 20); bigData[0] = 7; let big = bigData
        try await pair.clientPaths[2]!.write(big, handoff: {})
        await pair.waitForState { pair.machineFrames.contains { $0.0 == 2 && $0.1 == big } }
        #expect(pair.machineFrames.contains { $0.0 == 2 && $0.1 == big }, "1 MiB reassembled and hash-checked")
        let small = PeerWire.random(300)
        try await pair.machinePaths[1]!.write(small, handoff: {})
        await pair.waitForState { pair.clientFrames.contains { $0.0 == 1 && $0.1 == small } }
        #expect(pair.clientFrames.contains { $0.0 == 1 && $0.1 == small })
        pair.close()
        await pair.waitForBudgetEmpty()
        #expect(pair.clientBudget.isEmpty && pair.machineBudget.isEmpty, "nothing stays reserved after close")
    }

    @Test func stunUnreachableStillEstablishesOverHostAndBlockedFailsNoTraversal() async throws {
        let lan = try SessionPair(deadlineMs: 3_000)
        defer { lan.close() }
        lan.net.stunWorks = false
        await lan.clientSession.start()
        await lan.clientSession.tickForTest()
        await lan.waitForState { lan.clientPaths.count == 2 && lan.machinePaths.count == 2 && lan.clientSession.established }
        #expect(lan.clientPaths.count == 2 && lan.machinePaths.count == 2 && lan.clientSession.established, "host/prflx establishes without srflx")
        lan.close()
        await lan.waitForBudgetEmpty()
        #expect(lan.clientBudget.isEmpty)

        let blocked = try SessionPair(deadlineMs: 3_000)
        defer { blocked.close() }
        blocked.net.stunWorks = false
        blocked.net.blackhole = true
        await blocked.clientSession.start()
        await blocked.clientSession.tickForTest()
        await blocked.waitForState { blocked.net.all.count == 4 && blocked.net.all.allSatisfy { $0.hasExchangedSDP } }
        #expect(blocked.net.all.count == 4 && blocked.net.all.allSatisfy { $0.hasExchangedSDP }, "both uses negotiated SDP before deadline")
        blocked.time.advance(3_001)
        await blocked.clientSession.tickForTest()
        await blocked.machineSession.tickForTest()
        await blocked.waitForState { blocked.clientClosed.contains(where: { $0 == nil }) }
        #expect(blocked.clientClosed.contains(where: { $0 == nil }), "the round ends at its deadline")
        #expect(blocked.clientPaths.isEmpty && !blocked.clientSession.established)
        #expect(blocked.clientSession.failure == .noTraversal)
        blocked.close()
        await blocked.waitForBudgetEmpty()
        #expect(blocked.clientBudget.isEmpty)
    }

    @Test func lostSignallingNeverEstablishesAndLeavesNothingBehind() async throws {
        let pair = try SessionPair(deadlineMs: 2_500)
        defer { pair.close() }
        pair.dropFrames = true
        await pair.clientSession.start()
        await pair.clientSession.tickForTest()
        pair.time.advance(2_501)
        await pair.clientSession.tickForTest()
        await pair.waitForState { pair.clientClosed.contains(where: { $0 == nil }) }
        #expect(pair.clientClosed.contains(where: { $0 == nil }))
        #expect(pair.clientPaths.isEmpty)
        #expect(pair.clientSession.failure == .noResponse)
        pair.close()
        #expect(pair.net.all.isEmpty, "no PeerConnection was ever made without the computer's proof")
    }

    @Test func control10UnsupportedForControl15DoesNotClosePCsOrFailSession() async throws {
        let pair = try SessionPair(deadlineMs: 5_000)
        defer { pair.close() }
        await pair.clientSession.start()
        await pair.clientSession.tickForTest()
        await pair.waitForState { pair.clientPaths.count == 2 && pair.machinePaths.count == 2 }
        #expect(pair.clientPaths.count == 2 && pair.machinePaths.count == 2)

        let sub = try PBDirectEventSubscription.with { $0.sessionIds = ["sess-1"] }.serializedData()
        pair.clientSession.sendControl(15, Array(sub))

        // Wait deterministically until Control 10 (Unsupported) returns, then drive a tick pass
        // after advancing the injected clock past retry intervals and verify one-shot sendControl(15).
        await pair.waitForState { pair.machineUnsupportedCount >= 1 }
        #expect(pair.machineUnsupportedCount >= 1)
        let ticksBefore = pair.clientSession.tickCount
        pair.time.advance(1_600)
        await pair.clientSession.tickForTest()
        #expect(pair.clientSession.tickCount > ticksBefore)

        #expect(pair.clientControl15Count == 1)
        #expect(pair.clientSession.active)
        #expect(pair.clientSession.failure == nil)
        #expect(pair.clientClosed.isEmpty)
    }
}
