import Connect
import Foundation
import RAProto

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

struct PathChanged: Error, Sendable {}
struct PathRefusal: Sendable { let reason: String }
protocol PathAttempt: Sendable {
    var attemptId: String { get }
    func prepare(prior: Bool) async throws
    func send(machineId: String, command: PBEnvelope) async throws -> PBForwardResponse
    func refusal(_ error: ConnectError) -> PathRefusal?
}

/// Paths carry routing and sealed bytes, never command bodies or keys.
protocol Path: Sendable {
    func attempt(handoff: @escaping @Sendable () throws -> Void) -> any PathAttempt
    func openStream(_ route: String, hello: Data) -> any FrameStream
}
