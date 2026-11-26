import Foundation

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

/// Paths carry raw stream frames only; unary RPCs flow through Core's byte-pipe `RelayRpcEffect`.
protocol Path: Sendable {
    func openStream(_ route: String, hello: Data) -> any FrameStream
}
