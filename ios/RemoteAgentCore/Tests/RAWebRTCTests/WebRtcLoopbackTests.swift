import Foundation
import RACore
import RAWebRTC
import Testing

/// The real SDK, in one process, no network beyond loopback: two peer connections with the two
/// negotiated channels (id 0 reliable, id 1 unreliable), no STUN, candidates handed across by hand.
/// This is the adapter's own check; every behavior rule is tested against the fake SDK in RACore.
@Suite(.serialized) struct WebRtcLoopbackTests {
    final class Side: RtcPeerListener, RtcDataChannelListener, @unchecked Sendable {
        let lock = NSLock()
        var candidates: [RtcIceCandidate] = []
        var states: [RtcPeerState] = []
        var messages: [Data] = []
        var unexpected = 0
        var stateChanges = 0
        func onIceCandidate(_ c: RtcIceCandidate) { lock.withLock { candidates.append(c) } }
        func onIceGatheringComplete() {}
        func onConnectionStateChange(_ s: RtcPeerState) { lock.withLock { states.append(s) } }
        func onUnexpectedDataChannel() { lock.withLock { unexpected += 1 } }
        func onStateChange() { lock.withLock { stateChanges += 1 } }
        func onBufferedAmountChange() {}
        func onMessage(_ d: Data) { lock.withLock { messages.append(d) } }
        var drained: [RtcIceCandidate] { lock.withLock { let c = candidates; candidates = []; return c } }
        var connected: Bool { lock.withLock { states.contains(.connected) } }
        var received: [Data] { lock.withLock { messages } }
    }

    @Test func twoPeersConnectAndCarryBothChannelsWithoutAnyMedia() async throws {
        let factory = WebRtcFactory()
        let a = Side(), b = Side()
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
        let deadline = Date().addingTimeInterval(20)
        while Date() < deadline, !(a.connected && b.connected && ra.state == .open && rb.state == .open) {
            for c in a.drained { try? await pb.addIceCandidate(c.candidate, sdpMid: c.sdpMid, sdpMLineIndex: c.sdpMLineIndex, usernameFragment: "") }
            for c in b.drained { try? await pa.addIceCandidate(c.candidate, sdpMid: c.sdpMid, sdpMLineIndex: c.sdpMLineIndex, usernameFragment: "") }
            try await Task.sleep(nanoseconds: 50_000_000)
        }
        #expect(a.connected && b.connected)
        #expect(ra.state == .open && rb.state == .open && ua.state == .open)
        let frame = Data((0..<16_384).map { UInt8($0 & 0xff) })
        #expect(ra.send(frame))
        let until = Date().addingTimeInterval(5)
        while Date() < until, b.received.isEmpty { try await Task.sleep(nanoseconds: 20_000_000) }
        #expect(b.received == [frame])
        #expect(a.unexpected == 0 && b.unexpected == 0)
        pa.close()
        let closing = Date().addingTimeInterval(5)
        while Date() < closing, !a.lock.withLock({ a.states.contains(.closed) }) { try await Task.sleep(nanoseconds: 20_000_000) }
    }
}
