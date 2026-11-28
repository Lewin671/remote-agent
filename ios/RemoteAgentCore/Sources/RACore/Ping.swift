// 连接检测: the server's /healthz round trip.

/// The server's round trip: being measured, its time, or why it failed.
public enum Ping: Equatable, Sendable {
    case wait
    case ok(ms: Int64)
    case fail(error: String)
}

/// A server /healthz round trip this long is called slow.
public let slowServerMs: Int64 = 500

/// The middle one of the samples: one slow packet does not make the line slow.
public func median(_ samples: [Int64]) -> Int64 { samples.sorted()[samples.count / 2] }
