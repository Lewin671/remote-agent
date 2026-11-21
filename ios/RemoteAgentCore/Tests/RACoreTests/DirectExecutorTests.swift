import CryptoKit
import Foundation
import RAProto
import SwiftProtobuf
import Testing

@testable import RACore

@Suite struct DirectExecutorTests {
    private final class FakeFrameStream: FrameStream, @unchecked Sendable {
        private let lock = NSLock()
        private var _sent: [Data] = []
        private var _canceled = false

        var sent: [Data] { lock.withLock { _sent } }
        var canceled: Bool { lock.withLock { _canceled } }

        func send(_ data: Data, done: @escaping @Sendable (Error?) -> Void) {
            lock.withLock { _sent.append(data) }
            done(nil)
        }
        func opened() async -> Bool { true }
        func receive() async throws -> Data { throw PathChanged() }
        func ended() async -> PathEnd { PathEnd(closeCode: nil, error: nil) }
        func cancel() { lock.withLock { _canceled = true } }
        func close() { cancel() }
    }

    private final class Recorder<T>: @unchecked Sendable {
        private let lock = NSLock()
        private var list: [T] = []
        func append(_ item: T) { lock.withLock { list.append(item) } }
        var items: [T] { lock.withLock { list } }
    }

    @Test(.timeLimit(.minutes(1))) func signalFraming() throws {
        let emitted = Recorder<PBCoreEvent>()

        let exec = DirectExecutor(
            emit: { ev in emitted.append(ev) },
            onPushedEvent: { _, _, _, _, _ in }
        )

        // Signal send over attached leg
        let signalEff = PBCoreDirectSignalSendEffect.with {
            $0.toDevice = Data("dev-m1".utf8)
            $0.frameClass = UInt32(PBFrameClass.signal.rawValue)
            $0.frameID = Data([0xAA, 0xBB])
            $0.sealed = Data("opaque-sealed".utf8)
        }
        #expect(!exec.signalSend(signalEff))
        let stream = FakeFrameStream()
        exec.attachLeg(stream)
        #expect(exec.signalSend(signalEff))
        let frame = try PBPeerFrame(serializedBytes: try #require(stream.sent.first))
        #expect(frame.directedPeerFrame.toDevice == Data("dev-m1".utf8))
        #expect(frame.directedPeerFrame.frameID == Data([0xAA, 0xBB]))
        exec.detachLeg(stream)
        #expect(!exec.signalSend(signalEff))
    }

    @Test(.timeLimit(.minutes(1))) func peerConnectionLifecycleRpcAndStatsEvents() async throws {
        let net = FakeRtcNet()
        let factory = FakeRtcFactory(net: net)
        let emitted = Recorder<PBCoreEvent>()
        let pushed = Recorder<(String, UInt64, UInt64)>()

        let exec = DirectExecutor(
            emit: { ev in emitted.append(ev) },
            onPushedEvent: { mid, pcId, _, hdr, _ in
                pushed.append((mid, pcId, hdr.seq))
            },
            factory: factory
        )
        defer { exec.close() }

        exec.pcCreate(.with {
            $0.roundID = 10; $0.machineID = "m1"; $0.pcID = 101; $0.use = 0; $0.generation = 1; $0.stunServers = ["stun:127.0.0.1:3478"]
        })
        exec.pcCreate(.with {
            $0.roundID = 10; $0.machineID = "m1"; $0.pcID = 102; $0.use = 1; $0.generation = 1; $0.stunServers = ["stun:127.0.0.1:3478"]
        })
        let createdIds = emitted.items.compactMap { e -> UInt64? in
            if case .directPcCreated(let c)? = e.event, c.ok { return c.pcID }
            return nil
        }
        #expect(createdIds == [101, 102])

        await exec.pcDescribe(.with { $0.pcID = 101; $0.offer = true })
        let offerOpt = emitted.items.compactMap { e -> Data? in
            if case .directPcDescription(let d)? = e.event, d.pcID == 101, d.ok { return d.sdp }
            return nil
        }.last
        let offer = try #require(offerOpt)
        await exec.pcSetRemote(.with { $0.pcID = 102; $0.offer = true; $0.sdp = offer })
        await exec.pcDescribe(.with { $0.pcID = 102; $0.offer = false })
        let answerOpt = emitted.items.compactMap { e -> Data? in
            if case .directPcDescription(let d)? = e.event, d.pcID == 102, d.ok { return d.sdp }
            return nil
        }.last
        let answer = try #require(answerOpt)
        await exec.pcSetRemote(.with { $0.pcID = 101; $0.offer = false; $0.sdp = answer })

        exec.pcConfirm(.with { $0.pcID = 101; $0.machineID = "m1"; $0.use = 0 })
        exec.pcConfirm(.with { $0.pcID = 102; $0.machineID = "m1"; $0.use = 1 })

        while emitted.items.filter({ if case .directDcState(let s)? = $0.event { return s.open }; return false }).count < 2 {
            try await Task.sleep(nanoseconds: 10_000_000)
        }

        let pc102 = try #require(net.all.last)
        let reqID = "req-1"
        async let rpcReply = exec.sendRpc(pcId: 101, requestId: reqID, sealedBytes: Data("req".utf8), timeoutMs: 2_000) {}

        let replyEnv = try PBEnvelope.with { env in
            env.header = try PBHeader.with {
                $0.kind = .commandResult
                $0.machineID = "m1"
                $0.sender = "m1"
                $0.requestID = reqID
            }.serializedData()
            env.ciphertext = Data("ok".utf8)
        }
        var replyFrag = PBFragment()
        let replyBytes = try replyEnv.serializedData()
        replyFrag.messageID = Data(repeating: 7, count: 16)
        replyFrag.index = 0
        replyFrag.count = 1
        replyFrag.ciphertextLength = UInt32(replyBytes.count)
        replyFrag.ciphertextSha256 = Data(SHA256.hash(data: replyBytes))
        replyFrag.pathGeneration = 1
        replyFrag.fragment = replyBytes
        _ = try #require(pc102.channels[0]).send(try replyFrag.serializedData())

        let resolved = try await rpcReply
        #expect(resolved.ciphertext == Data("ok".utf8))

        // dcReadStats
        await exec.dcReadStats(.with { $0.pcID = 101 })
        #expect(emitted.items.contains { if case .directDcStats(let s)? = $0.event { return s.pcID == 101 }; return false })

        // Closing 101 by Core must NOT emit directPcClosed for 101 (while peer 102 disconnecting does emit 102).
        exec.pcClose(.with { $0.pcID = 101; $0.reason = "done" })
        while !emitted.items.contains(where: { if case .directPcClosed(let c)? = $0.event { return c.pcID == 102 }; return false }) {
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        let closedFor101 = emitted.items.filter { if case .directPcClosed(let c)? = $0.event { return c.pcID == 101 }; return false }.count
        let closedFor102 = emitted.items.filter { if case .directPcClosed(let c)? = $0.event { return c.pcID == 102 }; return false }.count
        #expect(closedFor101 == 0)
        #expect(closedFor102 == 1)
    }

    @Test(.timeLimit(.minutes(1))) func blockingPcCloseDoesNotBlockExecutor() async throws {
        let net = FakeRtcNet()
        let factory = FakeRtcFactory(net: net)
        let emitted = Recorder<PBCoreEvent>()
        let exec = DirectExecutor(
            emit: { ev in emitted.append(ev) },
            onPushedEvent: { _, _, _, _, _ in },
            factory: factory
        )
        defer { net.releaseClose(); exec.close() }

        exec.pcCreate(.with {
            $0.roundID = 20; $0.machineID = "m1"; $0.pcID = 201; $0.use = 0; $0.generation = 1; $0.stunServers = ["stun:127.0.0.1:3478"]
        })
        net.holdClose()
        exec.pcClose(.with { $0.pcID = 201; $0.reason = "done" })
        // pcClose must return immediately without waiting on pc.close().
        #expect(net.closeFinished == 0)

        // The close runs on a utility queue; wait for it to start rather than
        // for a wall-clock deadline, which a loaded CI runner can miss.
        while net.closeEntered == 0 {
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        #expect(net.closeFinished == 0)
        net.releaseClose()
        while net.closeFinished == 0 {
            try await Task.sleep(nanoseconds: 10_000_000)
        }
    }

    @Test(.timeLimit(.minutes(1))) func replacedPcLateCallbacksDoNotTouchSuccessor() async throws {
        let net = FakeRtcNet()
        let factory = FakeRtcFactory(net: net)
        let emitted = Recorder<PBCoreEvent>()
        let exec = DirectExecutor(
            emit: { ev in emitted.append(ev) },
            onPushedEvent: { _, _, _, _, _ in },
            factory: factory
        )
        defer { exec.close() }

        exec.pcCreate(.with {
            $0.roundID = 30; $0.machineID = "m1"; $0.pcID = 301; $0.use = 0; $0.generation = 1; $0.stunServers = ["stun:127.0.0.1:3478"]
        })
        let pc1 = try #require(net.all.first)

        // Replace pcID 301 with a fresh PC; pc1 becomes stale.
        exec.pcCreate(.with {
            $0.roundID = 31; $0.machineID = "m1"; $0.pcID = 301; $0.use = 0; $0.generation = 2; $0.stunServers = ["stun:127.0.0.1:3478"]
        })
        let pc2 = try #require(net.all.last)
        #expect(pc1 !== pc2)
        let beforeLate = emitted.items.count

        // Late SDK callbacks from pc1 must be suppressed and must not close pc2.
        pc1.listener.onIceCandidate(RtcIceCandidate(candidate: "candidate:9 1 udp 1 192.0.2.9 9999 typ host", sdpMid: "0", sdpMLineIndex: 0, isSrflx: false))
        pc1.listener.onConnectionStateChange(.failed)
        pc1.channels[0]?.set(.closed)
        try await Task.sleep(nanoseconds: 50_000_000)

        let lateEvents = Array(emitted.items.dropFirst(beforeLate))
        #expect(lateEvents.isEmpty)

        // Successor pc2 still describes and reads stats normally.
        await exec.pcDescribe(.with { $0.pcID = 301; $0.offer = true })
        #expect(emitted.items.contains { if case .directPcDescription(let d)? = $0.event { return d.pcID == 301 && d.ok }; return false })
    }
}
