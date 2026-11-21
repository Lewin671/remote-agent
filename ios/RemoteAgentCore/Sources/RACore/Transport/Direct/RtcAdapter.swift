import Foundation
import RAProto

// The WebRTC SDK as RACore sees it: DataChannels only, no media, no TURN. The one implementation
// that touches the real SDK is the RAWebRTC target; tests use a fake. SDP and candidate strings
// pass through untouched; Core validates and drives signaling.

public protocol DirectPreferenceStore: Sendable {
    func isEnabled(server: String, deviceId: String, machineId: String) -> Bool
    func setEnabled(server: String, deviceId: String, machineId: String, enabled: Bool) throws
    func allEnabled(server: String, deviceId: String) -> [String: Bool]
}

extension DirectPreferenceStore {
    public func allEnabled(server: String, deviceId: String) -> [String: Bool] { [:] }
}

/// Per-device, account, and computer direct-path preference; automatic (`true`) by default.
public struct UserDefaultsDirectPreferences: DirectPreferenceStore, @unchecked Sendable {
    private let defaults: UserDefaults
    private let lock = NSLock()

    public init(_ defaults: UserDefaults = .standard) {
        self.defaults = defaults
    }

    private func prefix(_ s: String, _ d: String) -> String { "remote-agent-direct:\(s):\(d):" }
    private func key(_ s: String, _ d: String, _ m: String) -> String { prefix(s, d) + m }

    public func isEnabled(server: String, deviceId: String, machineId: String) -> Bool {
        lock.withLock {
            defaults.string(forKey: key(server, deviceId, machineId)) != "off"
        }
    }

    public func setEnabled(server: String, deviceId: String, machineId: String, enabled: Bool) throws {
        lock.withLock {
            defaults.set(enabled ? "auto" : "off", forKey: key(server, deviceId, machineId))
        }
    }

    public func allEnabled(server: String, deviceId: String) -> [String: Bool] {
        let pre = prefix(server, deviceId)
        return lock.withLock {
            var out: [String: Bool] = [:]
            for (k, v) in defaults.dictionaryRepresentation() where k.hasPrefix(pre) {
                let id = String(k.dropFirst(pre.count))
                guard !id.isEmpty, let str = v as? String else { continue }
                out[id] = (str != "off")
            }
            return out
        }
    }
}

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
    func onIceConnectionStateChange(_ state: String)
    func onIceGatheringStateChange(_ state: String)
    func onIceCandidateError(code: Int, address: String)
}

extension RtcPeerListener {
    public func onIceConnectionStateChange(_ state: String) {}
    public func onIceGatheringStateChange(_ state: String) {}
    public func onIceCandidateError(code: Int, address: String) {}
}

public protocol RtcPeerConnection: AnyObject, Sendable {
    func createDataChannel(id: Int, ordered: Bool, maxRetransmits: Int?, listener: any RtcDataChannelListener) throws -> any RtcDataChannel
    func createOffer(iceRestart: Bool) async throws -> String
    func createAnswer() async throws -> String
    func setLocalDescription(offer: Bool, sdp: String) async throws
    func setRemoteDescription(offer: Bool, sdp: String) async throws
    func addIceCandidate(_ candidate: String, sdpMid: String, sdpMLineIndex: Int, usernameFragment: String) async throws
    func readStats() async -> PBCoreDirectStatsReport
    var maxMessageSize: Int? { get }
    func close()
}

extension RtcPeerConnection {
    public func readStats() async -> PBCoreDirectStatsReport { PBCoreDirectStatsReport() }
}

public protocol RtcFactory: Sendable {
    func create(stun: [String], listener: any RtcPeerListener) throws -> any RtcPeerConnection
}
