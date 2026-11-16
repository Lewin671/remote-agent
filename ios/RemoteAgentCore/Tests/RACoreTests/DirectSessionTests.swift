import Foundation
import Testing
@testable import RACore

func eventually(_ seconds: Double = 5, _ condition: @Sendable () -> Bool) async -> Bool {
    let end = Date().addingTimeInterval(seconds)
    while Date() < end { if condition() { return true }; try? await Task.sleep(nanoseconds: 10_000_000) }
    return condition()
}

/// A client and a computer session wired through memory: the directed frames cross as the relay
/// would carry them, the PeerConnections are fakes.
final class SessionPair: @unchecked Sendable {
    typealias W = PeerWire
    let net = FakeRtcNet(), clientBudget = DirectBudget(), machineBudget = DirectBudget()
    let client: W.Identity, machine: W.Identity
    var clientSession: DirectSession!, machineSession: DirectSession!
    private let lock = NSLock()
    private var _clientPaths: [Int: DirectChannel] = [:], _machinePaths: [Int: DirectChannel] = [:]
    private var _machineFrames: [(Int, W.Bytes)] = [], _clientFrames: [(Int, W.Bytes)] = [], _clientClosed: [Int?] = []
    var dropFrames = false
    var clientPaths: [Int: DirectChannel] { lock.withLock { _clientPaths } }
    var machinePaths: [Int: DirectChannel] { lock.withLock { _machinePaths } }
    var machineFrames: [(Int, W.Bytes)] { lock.withLock { _machineFrames } }
    var clientFrames: [(Int, W.Bytes)] { lock.withLock { _clientFrames } }
    var clientClosed: [Int?] { lock.withLock { _clientClosed } }

    init(deadlineMs: UInt64 = 10_000) throws {
        client = try PeerSignalVectorsTests.identity("client", 1); machine = try PeerSignalVectorsTests.identity("machine", 2)
        let codec = PeerCodec(scope: PeerSignalVectorsTests.scope), factory = FakeRtcFactory(net: net)
        let now = UInt64(Date().timeIntervalSince1970 * 1000), connection = W.random(16)
        func options(_ me: W.Identity, _ other: W.Public, _ budget: DirectBudget, isClient: Bool) -> DirectSessionOptions {
            DirectSessionOptions(
                selfID: me, peer: other, codec: codec, connection: connection, deadline: now + deadlineMs, budget: budget, stun: ["stun:127.0.0.1:3478"], factory: factory,
                valid: { true }, authorize: {},
                send: { [unowned self] out in
                    if self.dropFrames { return true }
                    (isClient ? self.machineSession : self.clientSession)?.receive(out.raw); return true
                },
                chosen: {}, beforeOffer: {},
                ready: { [unowned self] use, path in self.lock.withLock { if isClient { self._clientPaths[use] = path } else { self._machinePaths[use] = path } } },
                closed: { [unowned self] use in if isClient { self.lock.withLock { self._clientClosed.append(use) } } },
                frame: { [unowned self] raw, use in self.lock.withLock { if isClient { self._clientFrames.append((use, raw)) } else { self._machineFrames.append((use, raw)) } } },
                capability: { _ in })
        }
        clientSession = try DirectSession(options(client, W.member(machine.peer), clientBudget, isClient: true))
        machineSession = try DirectSession(options(machine, W.member(client.peer), machineBudget, isClient: false))
    }
    func close() { clientSession.close(); machineSession.close() }
}

@Suite struct DirectSessionTests {
    @Test func handshakeOpensBothUsesAndCarriesAFragmentedMessage() async throws {
        let pair = try SessionPair()
        await pair.clientSession.start()
        #expect(await eventually { pair.clientPaths.count == 2 && pair.machinePaths.count == 2 }, "both PeerConnections ready on both sides")
        #expect(pair.clientSession.established)
        var bigData = PeerWire.random(1 << 20); bigData[0] = 7; let big = bigData
        try await pair.clientPaths[2]!.write(big, handoff: {})
        #expect(await eventually { pair.machineFrames.contains { $0.0 == 2 && $0.1 == big } }, "1 MiB reassembled and hash-checked")
        let small = PeerWire.random(300)
        try await pair.machinePaths[1]!.write(small, handoff: {})
        #expect(await eventually { pair.clientFrames.contains { $0.0 == 1 && $0.1 == small } })
        pair.close()
        #expect(await eventually { pair.clientBudget.isEmpty && pair.machineBudget.isEmpty }, "nothing stays reserved after close")
    }

    @Test func stunUnreachableNeverEstablishes() async throws {
        let pair = try SessionPair(deadlineMs: 3_000)
        pair.net.stunWorks = false
        await pair.clientSession.start()
        #expect(await eventually(5) { pair.clientClosed.contains(where: { $0 == nil }) }, "the round ends at its deadline")
        #expect(pair.clientPaths.isEmpty && !pair.clientSession.established)
        #expect(pair.clientSession.failure == .noTraversal)
        pair.close()
        #expect(await eventually { pair.clientBudget.isEmpty })
    }

    @Test func lostSignallingNeverEstablishesAndLeavesNothingBehind() async throws {
        let pair = try SessionPair(deadlineMs: 2_500)
        pair.dropFrames = true
        await pair.clientSession.start()
        #expect(await eventually(5) { pair.clientClosed.contains(where: { $0 == nil }) })
        #expect(pair.clientPaths.isEmpty)
        #expect(pair.clientSession.failure == .noResponse)
        pair.close()
        #expect(pair.net.all.isEmpty, "no PeerConnection was ever made without the computer's proof")
    }
}
