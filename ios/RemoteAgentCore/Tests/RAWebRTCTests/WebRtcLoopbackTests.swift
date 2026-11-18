import Foundation
import RACore
import RAWebRTC
import Testing

/// The real SDK, in one process, no network beyond loopback: two peer connections with the two
/// negotiated channels (id 0 reliable, id 1 unreliable), no STUN, candidates handed across by hand.
/// This is the adapter's own check; every behavior rule is tested against the fake SDK in RACore.
@Suite(.serialized, .timeLimit(.minutes(2))) struct WebRtcLoopbackTests {
    final class Side: RtcPeerListener, RtcDataChannelListener, @unchecked Sendable {
        let lock = NSLock()
        var candidates: [RtcIceCandidate] = []
        var states: [RtcPeerState] = []
        var messages: [Data] = []
        var unexpected = 0
        var stateChanges = 0
        private let onEvent: @Sendable () -> Void
        init(onEvent: @escaping @Sendable () -> Void) { self.onEvent = onEvent }
        func onIceCandidate(_ c: RtcIceCandidate) { lock.withLock { candidates.append(c) }; onEvent() }
        func onIceGatheringComplete() { onEvent() }
        func onConnectionStateChange(_ s: RtcPeerState) { lock.withLock { states.append(s) }; onEvent() }
        func onUnexpectedDataChannel() { lock.withLock { unexpected += 1 }; onEvent() }
        func onStateChange() { lock.withLock { stateChanges += 1 }; onEvent() }
        func onBufferedAmountChange() {}
        func onMessage(_ d: Data) { lock.withLock { messages.append(d) }; onEvent() }
        var drained: [RtcIceCandidate] { lock.withLock { let c = candidates; candidates = []; return c } }
        var connected: Bool { lock.withLock { states.contains(.connected) } }
        var received: [Data] { lock.withLock { messages } }
    }

    private final class Waiter: @unchecked Sendable {
        private let lock = NSLock()
        private var waiters: [(predicate: @Sendable () -> Bool, cont: CheckedContinuation<Void, Never>)] = []

        func signal() {
            let ready = lock.withLock { () -> [CheckedContinuation<Void, Never>] in
                var fulfilled: [CheckedContinuation<Void, Never>] = []
                var remaining: [(predicate: @Sendable () -> Bool, cont: CheckedContinuation<Void, Never>)] = []
                for w in waiters {
                    if w.predicate() { fulfilled.append(w.cont) } else { remaining.append(w) }
                }
                waiters = remaining
                return fulfilled
            }
            for c in ready { c.resume() }
        }

        func wait(_ predicate: @escaping @Sendable () -> Bool) async {
            await withCheckedContinuation { (c: CheckedContinuation<Void, Never>) in
                let now = lock.withLock { () -> Bool in
                    if predicate() { return true }
                    waiters.append((predicate, c))
                    return false
                }
                if now { c.resume() }
            }
        }
    }

    @Test func twoPeersConnectAndCarryBothChannelsWithoutAnyMedia() async throws {
        let factory = WebRtcFactory()
        let waiter = Waiter()
        let a = Side(onEvent: { waiter.signal() }), b = Side(onEvent: { waiter.signal() })
        let pa = try factory.create(stun: [], listener: a), pb = try factory.create(stun: [], listener: b)
        let ra = try pa.createDataChannel(id: 0, ordered: true, maxRetransmits: nil, listener: a)
        let ua = try pa.createDataChannel(id: 1, ordered: false, maxRetransmits: 0, listener: a)
        let rb = try pb.createDataChannel(id: 0, ordered: true, maxRetransmits: nil, listener: b)
        _ = try pb.createDataChannel(id: 1, ordered: false, maxRetransmits: 0, listener: b)
        defer { pa.close(); pb.close() }
        let offer = try await pa.createOffer(iceRestart: false)
        #expect(!offer.contains("m=audio") && !offer.contains("m=video"), "a data-only offer asks for no microphone and no camera")
        try await pa.setLocalDescription(offer: true, sdp: offer)
        try await pb.setRemoteDescription(offer: true, sdp: offer)
        let answer = try await pb.createAnswer()
        try await pb.setLocalDescription(offer: false, sdp: answer)
        try await pa.setRemoteDescription(offer: false, sdp: answer)
        while !(a.connected && b.connected && ra.state == .open && rb.state == .open && ua.state == .open) {
            for c in a.drained { try? await pb.addIceCandidate(c.candidate, sdpMid: c.sdpMid, sdpMLineIndex: c.sdpMLineIndex, usernameFragment: "") }
            for c in b.drained { try? await pa.addIceCandidate(c.candidate, sdpMid: c.sdpMid, sdpMLineIndex: c.sdpMLineIndex, usernameFragment: "") }
            await waiter.wait {
                !a.lock.withLock({ a.candidates.isEmpty }) || !b.lock.withLock({ b.candidates.isEmpty }) ||
                    (a.connected && b.connected && ra.state == .open && rb.state == .open && ua.state == .open)
            }
        }
        #expect(a.connected && b.connected)
        #expect(ra.state == .open && rb.state == .open && ua.state == .open)
        let frame = Data((0..<16_384).map { UInt8($0 & 0xff) })
        #expect(ra.send(frame))
        await waiter.wait { !b.received.isEmpty }
        #expect(b.received == [frame])
        #expect(a.unexpected == 0 && b.unexpected == 0)
        pa.close()
        #expect(ra.state == .closed)
    }
}
