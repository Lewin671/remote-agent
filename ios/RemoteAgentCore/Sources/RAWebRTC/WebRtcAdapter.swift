import Foundation
import RACore
import RAProto
import WebRTC

/// DataChannel-only use of the WebRTC SDK (docs/transport.md §2): no media track is ever created,
/// so neither the microphone nor the camera is touched and no permission is asked; STUN is only
/// the relay host's own server, never TURN or a third party. SDP and candidate strings go to and
/// come from the SDK directly; Core validates them.
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

private func iceConnString(_ state: RTCIceConnectionState) -> String {
    switch state {
    case .new: "new"
    case .checking: "checking"
    case .connected: "connected"
    case .completed: "completed"
    case .failed: "failed"
    case .disconnected: "disconnected"
    case .closed: "closed"
    case .count: "count"
    @unknown default: ""
    }
}

private func iceGatherString(_ state: RTCIceGatheringState) -> String {
    switch state {
    case .new: "new"
    case .gathering: "gathering"
    case .complete: "complete"
    @unknown default: ""
    }
}

private func statsCandType(_ value: Any?) -> PBCoreDirectCandType {
    switch value as? String {
    case "host": .host
    case "srflx": .srflx
    case "prflx": .prflx
    case "relay": .relay
    case nil: .unspecified
    default: .other
    }
}

private let ipv4Regex = try! NSRegularExpression(pattern: #"^\d{1,3}(?:\.\d{1,3}){3}$"#)
private let v4MappedRegex = try! NSRegularExpression(pattern: #"^::ffff:\d{1,3}(?:\.\d{1,3}){3}$"#)

private func statsCandFamily(_ value: Any?) -> PBCoreDirectCandFamily {
    guard let s = (value as? String)?.trimmingCharacters(in: .whitespacesAndNewlines).lowercased(),
          !s.isEmpty else { return .unspecified }
    let base = String(s.split(separator: "%", maxSplits: 1, omittingEmptySubsequences: false).first ?? "")
    if base.isEmpty || base.hasSuffix(".local") { return .unspecified }
    let range = NSRange(base.startIndex..<base.endIndex, in: base)
    if ipv4Regex.firstMatch(in: base, range: range) != nil || v4MappedRegex.firstMatch(in: base, range: range) != nil {
        return .v4
    }
    return base.contains(":") ? .v6 : .unspecified
}

private func statsInt(_ value: Any?) -> Int32 {
    (value as? NSNumber)?.int32Value ?? 0
}

private final class PeerObserver: NSObject, RTCPeerConnectionDelegate, @unchecked Sendable {
    let listener: any RtcPeerListener
    weak var pc: RTCPeerConnection?
    init(listener: any RtcPeerListener) { self.listener = listener }
    func peerConnection(_ p: RTCPeerConnection, didChange stateChanged: RTCSignalingState) {}
    func peerConnection(_ p: RTCPeerConnection, didAdd stream: RTCMediaStream) {}
    func peerConnection(_ p: RTCPeerConnection, didRemove stream: RTCMediaStream) {}
    func peerConnectionShouldNegotiate(_ p: RTCPeerConnection) {}
    func peerConnection(_ p: RTCPeerConnection, didChange newState: RTCIceConnectionState) {
        let s = iceConnString(newState)
        if !s.isEmpty { listener.onIceConnectionStateChange(s) }
    }
    func peerConnection(_ p: RTCPeerConnection, didChange newState: RTCIceGatheringState) {
        let s = iceGatherString(newState)
        if !s.isEmpty { listener.onIceGatheringStateChange(s) }
        if newState == .complete { listener.onIceGatheringComplete() }
    }
    func peerConnection(_ p: RTCPeerConnection, didGenerate candidate: RTCIceCandidate) {
        listener.onIceCandidate(RtcIceCandidate(candidate: candidate.sdp, sdpMid: candidate.sdpMid ?? "", sdpMLineIndex: Int(candidate.sdpMLineIndex), isSrflx: candidate.sdp.contains(" typ srflx ")))
    }
    func peerConnection(_ p: RTCPeerConnection, didFailToGatherIceCandidate event: RTCIceCandidateErrorEvent) {
        listener.onIceCandidateError(code: Int(event.errorCode), address: event.address)
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
    private var closed = false
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
    func readStats() async -> PBCoreDirectStatsReport {
        let iceState = iceConnString(pc.iceConnectionState)
        if lock.withLock({ closed }) {
            return PBCoreDirectStatsReport.with { $0.iceState = iceState }
        }
        return await withCheckedContinuation { (c: CheckedContinuation<PBCoreDirectStatsReport, Never>) in
            pc.statistics { report in
                var dtlsState = ""
                var dcState = ""
                var locals: [String: (PBCoreDirectCandType, PBCoreDirectCandFamily)] = [:]
                var remotes: [String: (PBCoreDirectCandType, PBCoreDirectCandFamily)] = [:]
                var rawPairs: [RTCStatistics] = []
                for s in report.statistics.values {
                    let m = s.values
                    switch s.type {
                    case "local-candidate", "remote-candidate":
                        let address = m["address"] ?? m["ip"]
                        let info = (statsCandType(m["candidateType"]), statsCandFamily(address))
                        if s.type == "local-candidate" { locals[s.id] = info } else { remotes[s.id] = info }
                    case "candidate-pair":
                        rawPairs.append(s)
                    case "transport":
                        if let st = m["dtlsState"] as? String, !st.isEmpty { dtlsState = st }
                    case "data-channel":
                        let dcId = (m["dataChannelIdentifier"] as? NSNumber)?.intValue
                        if let st = m["state"] as? String, !st.isEmpty, dcId == nil || dcId == 0 {
                            dcState = st
                        }
                    default:
                        break
                    }
                }
                var out = PBCoreDirectStatsReport()
                out.iceState = iceState
                out.dtlsState = dtlsState
                out.dcState = dcState
                out.pairs = rawPairs.map { p in
                    let m = p.values
                    let localInfo = (m["localCandidateId"] as? String).flatMap { locals[$0] } ?? (.unspecified, .unspecified)
                    let remoteInfo = (m["remoteCandidateId"] as? String).flatMap { remotes[$0] } ?? (.unspecified, .unspecified)
                    let rtt = (m["currentRoundTripTime"] as? NSNumber)?.doubleValue
                    var pair = PBCoreDirectStatsPair()
                    pair.localType = localInfo.0
                    pair.localFamily = localInfo.1
                    pair.remoteType = remoteInfo.0
                    pair.remoteFamily = remoteInfo.1
                    pair.nominated = (m["nominated"] as? NSNumber)?.boolValue ?? false
                    pair.state = (m["state"] as? String) ?? ""
                    pair.requestsSent = statsInt(m["requestsSent"])
                    pair.responsesReceived = statsInt(m["responsesReceived"])
                    if let rtt, rtt.isFinite {
                        pair.hasCurrentRtt_p = true
                        pair.currentRttSeconds = rtt
                    }
                    return pair
                }
                c.resume(returning: out)
            }
        }
    }
    func close() {
        let all = lock.withLock { () -> [Channel] in
            if closed { return [] }
            closed = true
            let c = channels
            channels = []
            return c
        }
        for c in all { c.channel.delegate = nil; c.channel.close() }
        pc.delegate = nil
        pc.close()
    }
}
