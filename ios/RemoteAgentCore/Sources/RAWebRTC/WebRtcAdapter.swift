import Foundation
import RACore
import WebRTC

/// DataChannel-only use of the WebRTC SDK (docs/transport.md §2): no media track is ever created,
/// so neither the microphone nor the camera is touched and no permission is asked; STUN is only
/// the relay host's own server, never TURN or a third party. SDP and candidate strings go to and
/// come from the SDK exactly as they are; RACore validates them.
public final class WebRtcFactory: RtcFactory, @unchecked Sendable {
    private final class Sdk: @unchecked Sendable {
        let factory: RTCPeerConnectionFactory
        init() {
            RTCInitializeSSL()
            RTCSetMinDebugLogLevel(.none)     // the SDK never logs addresses, SDP or credentials
            factory = RTCPeerConnectionFactory()
        }
    }
    private static let sdk = Sdk()
    public init() {}

    public func create(stun: [String], listener: any RtcPeerListener) throws -> any RtcPeerConnection {
        let config = RTCConfiguration()
        config.iceServers = stun.isEmpty ? [] : [RTCIceServer(urlStrings: stun)]
        config.sdpSemantics = .unifiedPlan
        let observer = PeerObserver(listener: listener)
        guard let pc = Self.sdk.factory.peerConnection(with: config, constraints: RTCMediaConstraints(mandatoryConstraints: nil, optionalConstraints: nil), delegate: observer) else { throw SdkFailure.create }
        observer.pc = pc
        return PeerConnection(pc: pc, observer: observer)
    }
}

enum SdkFailure: Error { case create, channel, description }

private final class PeerObserver: NSObject, RTCPeerConnectionDelegate, @unchecked Sendable {
    let listener: any RtcPeerListener
    weak var pc: RTCPeerConnection?
    init(listener: any RtcPeerListener) { self.listener = listener }
    func peerConnection(_ p: RTCPeerConnection, didChange stateChanged: RTCSignalingState) {}
    func peerConnection(_ p: RTCPeerConnection, didAdd stream: RTCMediaStream) {}
    func peerConnection(_ p: RTCPeerConnection, didRemove stream: RTCMediaStream) {}
    func peerConnectionShouldNegotiate(_ p: RTCPeerConnection) {}
    func peerConnection(_ p: RTCPeerConnection, didChange newState: RTCIceConnectionState) {}
    func peerConnection(_ p: RTCPeerConnection, didChange newState: RTCIceGatheringState) { if newState == .complete { listener.onIceGatheringComplete() } }
    func peerConnection(_ p: RTCPeerConnection, didGenerate candidate: RTCIceCandidate) {
        listener.onIceCandidate(RtcIceCandidate(candidate: candidate.sdp, sdpMid: candidate.sdpMid ?? "", sdpMLineIndex: Int(candidate.sdpMLineIndex), isSrflx: candidate.sdp.contains(" typ srflx ")))
    }
    func peerConnection(_ p: RTCPeerConnection, didRemove candidates: [RTCIceCandidate]) {}
    func peerConnection(_ p: RTCPeerConnection, didOpen dataChannel: RTCDataChannel) { dataChannel.close(); listener.onUnexpectedDataChannel() }
    func peerConnection(_ p: RTCPeerConnection, didChange newState: RTCPeerConnectionState) {
        switch newState {
        case .new: listener.onConnectionStateChange(.new)
        case .connecting: listener.onConnectionStateChange(.connecting)
        case .connected: listener.onConnectionStateChange(.connected)
        case .disconnected: listener.onConnectionStateChange(.disconnected)
        case .failed: listener.onConnectionStateChange(.failed)
        case .closed: listener.onConnectionStateChange(.closed)
        @unknown default: break
        }
    }
}

private final class ChannelObserver: NSObject, RTCDataChannelDelegate, @unchecked Sendable {
    let listener: any RtcDataChannelListener
    init(listener: any RtcDataChannelListener) { self.listener = listener }
    func dataChannelDidChangeState(_ dataChannel: RTCDataChannel) { listener.onStateChange() }
    func dataChannel(_ dataChannel: RTCDataChannel, didReceiveMessageWith buffer: RTCDataBuffer) { listener.onMessage(buffer.data) }
    func dataChannel(_ dataChannel: RTCDataChannel, didChangeBufferedAmount amount: UInt64) { listener.onBufferedAmountChange() }
}

private final class Channel: RtcDataChannel, @unchecked Sendable {
    let channel: RTCDataChannel, observer: ChannelObserver
    init(channel: RTCDataChannel, observer: ChannelObserver) { self.channel = channel; self.observer = observer }
    var state: RtcChannelState {
        switch channel.readyState {
        case .connecting: .connecting
        case .open: .open
        case .closing: .closing
        case .closed: .closed
        @unknown default: .closed
        }
    }
    var bufferedAmount: Int { Int(min(channel.bufferedAmount, UInt64(Int.max))) }
    func send(_ data: Data) -> Bool { channel.sendData(RTCDataBuffer(data: data, isBinary: true)) }
    func close() { channel.close() }
}

private final class PeerConnection: RtcPeerConnection, @unchecked Sendable {
    let pc: RTCPeerConnection, observer: PeerObserver
    private let lock = NSLock()
    private var channels: [Channel] = []
    init(pc: RTCPeerConnection, observer: PeerObserver) { self.pc = pc; self.observer = observer }
    var maxMessageSize: Int? { nil }

    func createDataChannel(id: Int, ordered: Bool, maxRetransmits: Int?, listener: any RtcDataChannelListener) throws -> any RtcDataChannel {
        let config = RTCDataChannelConfiguration()
        config.isNegotiated = true; config.channelId = Int32(id); config.isOrdered = ordered
        if let maxRetransmits { config.maxRetransmits = Int32(maxRetransmits) }
        guard let dc = pc.dataChannel(forLabel: "remote-agent", configuration: config) else { throw SdkFailure.channel }
        let observer = ChannelObserver(listener: listener)
        dc.delegate = observer
        let channel = Channel(channel: dc, observer: observer)
        lock.withLock { channels.append(channel) }
        return channel
    }
    private func description(answer: Bool, restart: Bool) async throws -> String {
        let constraints = RTCMediaConstraints(mandatoryConstraints: restart ? ["IceRestart": "true"] : nil, optionalConstraints: nil)
        return try await withCheckedThrowingContinuation { (c: CheckedContinuation<String, Error>) in
            let done: @Sendable (RTCSessionDescription?, Error?) -> Void = { s, e in
                if let e { c.resume(throwing: e) } else if let s { c.resume(returning: s.sdp) } else { c.resume(throwing: SdkFailure.description) }
            }
            if answer { pc.answer(for: constraints, completionHandler: done) } else { pc.offer(for: constraints, completionHandler: done) }
        }
    }
    func createOffer(iceRestart: Bool) async throws -> String { try await description(answer: false, restart: iceRestart) }
    func createAnswer() async throws -> String { try await description(answer: true, restart: false) }
    func setLocalDescription(offer: Bool, sdp: String) async throws {
        try await withCheckedThrowingContinuation { (c: CheckedContinuation<Void, Error>) in
            pc.setLocalDescription(RTCSessionDescription(type: offer ? .offer : .answer, sdp: sdp)) { e in if let e { c.resume(throwing: e) } else { c.resume() } }
        }
    }
    func setRemoteDescription(offer: Bool, sdp: String) async throws {
        try await withCheckedThrowingContinuation { (c: CheckedContinuation<Void, Error>) in
            pc.setRemoteDescription(RTCSessionDescription(type: offer ? .offer : .answer, sdp: sdp)) { e in if let e { c.resume(throwing: e) } else { c.resume() } }
        }
    }
    func addIceCandidate(_ candidate: String, sdpMid: String, sdpMLineIndex: Int, usernameFragment: String) async throws {
        try await withCheckedThrowingContinuation { (c: CheckedContinuation<Void, Error>) in
            pc.add(RTCIceCandidate(sdp: candidate, sdpMLineIndex: Int32(sdpMLineIndex), sdpMid: sdpMid)) { e in if let e { c.resume(throwing: e) } else { c.resume() } }
        }
    }
    func close() {
        let all = lock.withLock { () -> [Channel] in let c = channels; channels = []; return c }
        for c in all { c.channel.delegate = nil; c.channel.close() }
        pc.delegate = nil
        pc.close()
    }
}
