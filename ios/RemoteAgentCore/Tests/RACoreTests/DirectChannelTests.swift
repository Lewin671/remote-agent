import Foundation
import RAProto
import Testing

@testable import RACore

@Suite struct DirectChannelTests {
    @Test(.timeLimit(.minutes(1))) func validateMinimalSDPRules() throws {
        let valid = "v=0\r\nm=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\na=fingerprint:sha-256 00:11\r\n"
        #expect(try validateMinimalSDP(Data(valid.utf8)) == valid)

        #expect(throws: PathChanged.self) { _ = try validateMinimalSDP(Data()) }
        #expect(throws: PathChanged.self) {
            _ = try validateMinimalSDP(Data([0xEF, 0xBB, 0xBF]) + Data(valid.utf8))
        }
        #expect(throws: PathChanged.self) {
            _ = try validateMinimalSDP(Data("v=0\nm=application 9\na=fingerprint:sha-256 00\r\n".utf8))
        }
        #expect(throws: PathChanged.self) {
            _ = try validateMinimalSDP(Data("v=0\r\nm=application 9 UDP/DTLS/SCTP\r\n".utf8))
        }
    }

    @Test(.timeLimit(.minutes(1))) func fragmentRoundTripAndUnknownFieldRejection() async throws {
        let net = FakeRtcNet()
        let factory = FakeRtcFactory(net: net)
        let budgetA = DirectBudget()
        let budgetB = DirectBudget()
        let deliveredB = Box<[Data]>([])
        let deliveredSignal = AsyncSignal()
        let closedA = Box(false)
        let closedB = Box(false)
        let closedBSignal = AsyncSignal()
        let virtualNow = Box<Double>(1_000)
        let pendingCloses = Box<[@Sendable () -> Void]>([])

        let chA = try DirectChannel(DirectChannelOptions(
            peer: "m1",
            roundId: 1,
            use: 0,
            generation: 7,
            budget: budgetA,
            valid: { true },
            stun: ["stun:127.0.0.1:3478"],
            factory: factory,
            candidate: { _ in },
            state: { _, _ in },
            frame: { _ in },
            closed: { closedA.withValue { $0 = true } },
            monotonic: { virtualNow.get() },
            autoTick: false,
            closeDispatch: { block in pendingCloses.withValue { $0.append(block) } }
        ))
        let chB = try DirectChannel(DirectChannelOptions(
            peer: "m1",
            roundId: 2,
            use: 0,
            generation: 7,
            budget: budgetB,
            valid: { true },
            stun: ["stun:127.0.0.1:3478"],
            factory: factory,
            candidate: { _ in },
            state: { _, _ in },
            frame: { data in
                deliveredB.withValue { $0.append(data) }
                deliveredSignal.signal()
            },
            closed: {
                closedB.withValue { $0 = true }
                closedBSignal.signal()
            },
            monotonic: { virtualNow.get() },
            autoTick: false,
            closeDispatch: { block in pendingCloses.withValue { $0.append(block) } }
        ))
        defer {
            chA.close()
            chB.close()
            let blocks = pendingCloses.withValue { list -> [@Sendable () -> Void] in
                defer { list.removeAll() }
                return list
            }
            for block in blocks { block() }
        }

        let offer = try await chA.description(offer: true)
        try await chB.remote(offer, offer: true)
        let answer = try await chB.description(offer: false)
        try await chA.remote(answer, offer: false)
        chA.confirm()
        chB.confirm()

        #expect(chA.ready && chB.ready)

        // Multi-fragment write (40 KiB > 16 KiB frame limit).
        let payload = Data((0..<40_000).map { UInt8(truncatingIfNeeded: $0 &* 13 &+ 5) })
        let handoffCount = Box(0)
        try await chA.write(payload) { handoffCount.withValue { $0 += 1 } }
        #expect(handoffCount.get() == 1)

        while deliveredB.get().isEmpty {
            try Task.checkCancellation()
            await deliveredSignal.wait(ms: 1_000)
        }
        #expect(deliveredB.get() == [payload])

        // Injecting an unknown protobuf field into the fragment stream must close the receiving channel.
        let pcA = try #require(net.all.first)
        var bogus = Data()
        bogus.append(contentsOf: [0xF8, 0x01, 0x01]) // field 31, varint = 1
        _ = try #require(pcA.channels[0]).send(bogus)

        while !closedB.get() {
            try Task.checkCancellation()
            await closedBSignal.wait(ms: 1_000)
        }
        #expect(closedB.get())

        chA.close()
        chB.close()
        #expect(closedA.get())
        #expect(budgetA.isEmpty && budgetB.isEmpty)
    }

    @Test(.timeLimit(.minutes(1))) func silentDataChannelCloseDetectedOnTick() async throws {
        let net = FakeRtcNet()
        let factory = FakeRtcFactory(net: net)
        let budgetA = DirectBudget()
        let budgetB = DirectBudget()
        let closedA = Box(false)
        let virtualNow = Box<Double>(1_000)

        let chA = try DirectChannel(DirectChannelOptions(
            peer: "m1",
            roundId: 3,
            use: 0,
            generation: 1,
            budget: budgetA,
            valid: { true },
            stun: ["stun:127.0.0.1:3478"],
            factory: factory,
            candidate: { _ in },
            state: { _, _ in },
            frame: { _ in },
            closed: { closedA.withValue { $0 = true } },
            monotonic: { virtualNow.get() },
            autoTick: false
        ))
        let chB = try DirectChannel(DirectChannelOptions(
            peer: "m1",
            roundId: 4,
            use: 0,
            generation: 1,
            budget: budgetB,
            valid: { true },
            stun: ["stun:127.0.0.1:3478"],
            factory: factory,
            candidate: { _ in },
            state: { _, _ in },
            frame: { _ in },
            closed: {},
            monotonic: { virtualNow.get() },
            autoTick: false
        ))
        defer { chA.close(); chB.close() }

        let offer = try await chA.description(offer: true)
        try await chB.remote(offer, offer: true)
        let answer = try await chB.description(offer: false)
        try await chA.remote(answer, offer: false)
        chA.confirm()
        chB.confirm()

        #expect(chA.ready && chB.ready)

        // SDK transitions DataChannel state to .closed without emitting onStateChange().
        let pcA = try #require(net.all.first)
        try #require(pcA.channels[0]).setSilent(.closed)
        #expect(!closedA.get())

        chA.tickOnce()
        #expect(closedA.get())
    }
}

private final class Box<T>: @unchecked Sendable {
    private let lock = NSLock()
    private var value: T
    init(_ value: T) { self.value = value }
    func get() -> T { lock.withLock { value } }
    func withValue<R>(_ body: (inout T) -> R) -> R { lock.withLock { body(&value) } }
}
