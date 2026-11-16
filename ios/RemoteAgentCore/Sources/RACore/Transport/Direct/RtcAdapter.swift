import Foundation

// The WebRTC SDK as RACore sees it: DataChannels only, no media, no TURN. The one implementation
// that touches the real SDK is the RAWebRTC target; tests use a fake. SDP and candidate strings
// pass through untouched (docs/transport.md §5: the validated original is what the SDK gets).

public enum RtcPeerState: Sendable { case new, connecting, connected, disconnected, failed, closed }
public enum RtcChannelState: Sendable { case connecting, open, closing, closed }

public struct RtcIceCandidate: Sendable, Equatable {
    public var candidate: String
    public var sdpMid: String
    public var sdpMLineIndex: Int
    public var isSrflx: Bool
    public init(candidate: String, sdpMid: String, sdpMLineIndex: Int, isSrflx: Bool) {
        self.candidate = candidate; self.sdpMid = sdpMid; self.sdpMLineIndex = sdpMLineIndex; self.isSrflx = isSrflx
    }
}

public protocol RtcDataChannel: AnyObject, Sendable {
    var state: RtcChannelState { get }
    var bufferedAmount: Int { get }
    func send(_ data: Data) -> Bool
    func close()
}

public protocol RtcDataChannelListener: Sendable {
    func onStateChange()
    func onBufferedAmountChange()
    func onMessage(_ data: Data)
}

public protocol RtcPeerListener: Sendable {
    func onIceCandidate(_ candidate: RtcIceCandidate)
    func onIceGatheringComplete()
    func onConnectionStateChange(_ state: RtcPeerState)
    func onUnexpectedDataChannel()
}

public protocol RtcPeerConnection: AnyObject, Sendable {
    func createDataChannel(id: Int, ordered: Bool, maxRetransmits: Int?, listener: any RtcDataChannelListener) throws -> any RtcDataChannel
    func createOffer(iceRestart: Bool) async throws -> String
    func createAnswer() async throws -> String
    func setLocalDescription(offer: Bool, sdp: String) async throws
    func setRemoteDescription(offer: Bool, sdp: String) async throws
    func addIceCandidate(_ candidate: String, sdpMid: String, sdpMLineIndex: Int, usernameFragment: String) async throws
    var maxMessageSize: Int? { get }
    func close()
}

public protocol RtcFactory: Sendable {
    func create(stun: [String], listener: any RtcPeerListener) throws -> any RtcPeerConnection
}
