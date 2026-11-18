import Foundation
@testable import RACore

/// An in-process stand-in for the WebRTC SDK: two fake PeerConnections that exchanged valid SDP
/// become a pair, channels open, messages cross. It can be cut (a failed ICE) or made to drop
/// everything (UDP lost), and gathers a server-reflexive candidate only when STUN "works".
final class FakeRtcNet: @unchecked Sendable {
    private let lock = NSLock()
    private var byUfrag: [String: FakePC] = [:]
    private(set) var created: [FakePC] = []
    var stunWorks = true { didSet {} }
    var blackhole = false
    var failCreate = false
    var autoOpenChannels = true
    private var _onEvent: (@Sendable () -> Void)?
    var onEvent: (@Sendable () -> Void)? { get { lock.withLock { _onEvent } } set { lock.withLock { _onEvent = newValue } } }
    /// Explicit close barrier so tests can pause SDK shutdown without sleeping on the cooperative pool.
    private var closeGate: DispatchSemaphore?
    private var _closeEntered = 0
    private var _closeFinished = 0
    var closeEntered: Int { lock.withLock { _closeEntered } }
    var closeFinished: Int { lock.withLock { _closeFinished } }
    func holdClose() { lock.withLock { closeGate = DispatchSemaphore(value: 0) } }
    func releaseClose() {
        let gate = lock.withLock { () -> DispatchSemaphore? in
            let g = closeGate
            closeGate = nil
            return g
        }
        // Unblock any waiters (up to a few concurrent PCs).
        for _ in 0..<16 { gate?.signal() }
    }
    fileprivate func waitCloseIfHeld() {
        let gate = lock.withLock { () -> DispatchSemaphore? in
            _closeEntered += 1
            return closeGate
        }
        onEvent?()
        _ = gate?.wait(timeout: .now() + 120)
        lock.withLock { _closeFinished += 1 }
        onEvent?()
    }
    func register(_ pc: FakePC) { lock.withLock { byUfrag[pc.ufrag] = pc; created.append(pc) }; onEvent?() }
    func find(_ ufrag: String) -> FakePC? { lock.withLock { byUfrag[ufrag] } }
    var all: [FakePC] { lock.withLock { created } }
    /// Every live pair fails at once, as a dropped network does.
    func cutAll() { for pc in all { pc.fail() } }
}

struct FakeRtcFactory: RtcFactory {
    let net: FakeRtcNet
    func create(stun: [String], listener: any RtcPeerListener) throws -> any RtcPeerConnection {
        if net.failCreate { throw PathChanged() }
        return FakePC(net: net, listener: listener)
    }
}

final class FakeRtcChannel: RtcDataChannel, @unchecked Sendable {
    let id: Int, listener: any RtcDataChannelListener
    private let lock = NSLock()
    private var _state = RtcChannelState.connecting
    weak var owner: FakePC?
    init(id: Int, listener: any RtcDataChannelListener) { self.id = id; self.listener = listener }
    var state: RtcChannelState { lock.withLock { _state } }
    var bufferedAmount: Int { 0 }
    func set(_ s: RtcChannelState) { lock.withLock { _state = s }; listener.onStateChange(); owner?.net.onEvent?() }
    func send(_ data: Data) -> Bool {
        guard state == .open, let owner, let peer = owner.peer, !owner.net.blackhole else { return state == .open }
        let target = peer.channels[id]
        Task.detached { target?.listener.onMessage(data) }
        return true
    }
    func close() { set(.closed) }
}

final class FakePC: RtcPeerConnection, @unchecked Sendable {
    let net: FakeRtcNet, listener: any RtcPeerListener, ufrag: String
    private let lock = NSLock()
    private var _channels: [Int: FakeRtcChannel] = [:]
    private var _peer: FakePC?, local = false, remote = false, closed = false, connected = false
    init(net: FakeRtcNet, listener: any RtcPeerListener) {
        self.net = net; self.listener = listener
        ufrag = String((0..<8).map { _ in "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789".randomElement()! })
        net.register(self)
    }
    var debug: String { lock.withLock { "\(ufrag) local=\(local) remote=\(remote) connected=\(connected) closed=\(closed) peer=\(_peer?.ufrag ?? "-") ch=\(_channels.mapValues { $0.state })" } }
    var channels: [Int: FakeRtcChannel] { lock.withLock { _channels } }
    var peer: FakePC? { lock.withLock { _peer } }
    var maxMessageSize: Int? { 262_144 }
    func createDataChannel(id: Int, ordered: Bool, maxRetransmits: Int?, listener: any RtcDataChannelListener) throws -> any RtcDataChannel {
        let c = FakeRtcChannel(id: id, listener: listener); c.owner = self
        lock.withLock { _channels[id] = c }; net.onEvent?(); return c
    }
    private func sdp(setup: String) -> String {
        let fp = (0..<32).map { _ in String(format: "%02X", UInt8.random(in: 0...255)) }.joined(separator: ":")
        return ["v=0", "o=- 1 2 IN IP4 127.0.0.1", "s=-", "t=0 0", "a=group:BUNDLE 0", "a=extmap-allow-mixed", "a=msid-semantic: WMS", "m=application 9 UDP/DTLS/SCTP webrtc-datachannel", "c=IN IP4 0.0.0.0",
                "a=ice-ufrag:\(ufrag)", "a=ice-pwd:PjX0jwnRPTdAUEW/wWFamz6T", "a=ice-options:trickle renomination", "a=fingerprint:sha-256 \(fp)", "a=setup:\(setup)", "a=mid:0", "a=sctp-port:5000", "a=max-message-size:262144"].joined(separator: "\r\n") + "\r\n"
    }
    func createOffer(iceRestart: Bool) async throws -> String { sdp(setup: "actpass") }
    func createAnswer() async throws -> String { sdp(setup: "active") }
    func setLocalDescription(offer: Bool, sdp: String) async throws {
        lock.withLock { local = true }
        let works = net.stunWorks
        Task.detached { [self] in
            if works { listener.onIceCandidate(RtcIceCandidate(candidate: "candidate:1 1 udp 1686052607 203.0.113.9 50000 typ srflx raddr 0.0.0.0 rport 0", sdpMid: "0", sdpMLineIndex: 0, isSrflx: true)) }
            listener.onIceGatheringComplete()
        }
        try await tryConnect()
        net.onEvent?()
    }
    func setRemoteDescription(offer: Bool, sdp: String) async throws {
        guard let line = sdp.components(separatedBy: "\r\n").first(where: { $0.hasPrefix("a=ice-ufrag:") }), let other = net.find(String(line.dropFirst(12))) else { throw PathChanged() }
        lock.withLock { remote = true; _peer = other }
        other.lock.withLock { other._peer = self }
        try await tryConnect()
        net.onEvent?()
    }
    func addIceCandidate(_ candidate: String, sdpMid: String, sdpMLineIndex: Int, usernameFragment: String) async throws {}
    private func tryConnect() async throws {
        guard let other = peer else { return }
        let ready = lock.withLock { local && remote && !closed } && other.lock.withLock { other.local && other.remote && !other.closed }
        guard !net.blackhole, ready, !lock.withLock({ connected }) else { return }
        lock.withLock { connected = true }; other.lock.withLock { other.connected = true }
        for pc in [self, other] {
            pc.listener.onConnectionStateChange(.connected)
            if net.autoOpenChannels {
                for c in pc.channels.values { c.set(.open) }
            }
        }
        net.onEvent?()
    }
    func fail() {
        let was = lock.withLock { () -> Bool in let c = connected; connected = false; return c }
        guard was || peer != nil else { return }
        listener.onConnectionStateChange(.failed)
    }
    var hasExchangedSDP: Bool { lock.withLock { local && remote } }
    var isConnectedAndOpen: Bool { lock.withLock { connected && !closed && !_channels.isEmpty && _channels.values.allSatisfy { $0.state == .open } } }
    func close() {
        net.waitCloseIfHeld()
        let first = lock.withLock { () -> Bool in if closed { return false }; closed = true; return true }
        guard first else { return }
        for c in channels.values { c.set(.closed) }
        if let other = peer, !other.lock.withLock({ other.closed }) { other.listener.onConnectionStateChange(.disconnected) }
    }
}
