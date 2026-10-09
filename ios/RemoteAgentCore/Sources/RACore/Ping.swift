// 连接检测: what one stretch of the way to a computer measured.

/// One stretch's round trip: being measured, its time, why it failed, or left out because a nearer stretch failed.
public enum Ping: Equatable, Sendable {
    case wait
    case ok(ms: Int64)
    case fail(error: String)
    case skip
}

/// A round trip this long is called slow. A computer's goes through the server twice and is sealed and opened on the way.
public let slowServerMs: Int64 = 500
public let slowMachineMs: Int64 = 1500

/// The middle one of the samples: one slow packet does not make the line slow.
public func median(_ samples: [Int64]) -> Int64 { samples.sorted()[samples.count / 2] }
