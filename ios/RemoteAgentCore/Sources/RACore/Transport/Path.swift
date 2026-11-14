import Connect
import Foundation
import RAProto

public struct PeerState: Equatable, Sendable {
    public var reachable = false
    public var ready = false
    public var authorized = false
    public var unlinked = false
    public var relayRequired = true
    public var protocolVersion: UInt32?
    public var capabilities: PBMachineCapabilities?
    public var available: Bool { reachable && ready }
    public func canProbe(_ serverReachable: Bool) -> Bool { serverReachable || !relayRequired }
    public init() {}
}

struct PathEnd: Sendable {
    var closeCode: Int?
    var error: Error?
}

protocol TerminalSocket: Sendable {
    func send(_ data: Data, done: @escaping @Sendable (Error?) -> Void)
    func cancel()
    func close()
}

protocol FrameStream: AnyObject, TerminalSocket {
    func opened() async -> Bool
    func receive() async throws -> Data
    func ended() async -> PathEnd
}

extension FrameStream {
    func send(_ data: Data) { send(data, done: { _ in }) }
}

struct PathRefusal: Sendable { let reason: String }
protocol PathAttempt: Sendable {
    var attemptId: String { get }
    func prepare(prior: Bool) async throws
    func send(machineId: String, command: PBEnvelope) async throws -> PBForwardResponse
    func refusal(_ error: ConnectError) -> PathRefusal?
    func refreshAuth(prior: Bool) async throws
}

/// Paths carry routing and sealed bytes, never command bodies or keys.
protocol Path: Sendable {
    func status(_ machineId: String) -> PeerState
    func attempt(handoff: @escaping @Sendable () throws -> Void) -> any PathAttempt
    func openStream(_ route: String, hello: Data) -> any FrameStream
}
