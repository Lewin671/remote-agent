// Looking a session up by its ID (architecture §8 「按 ID 取」). On Android
// this lives in core/Model.kt; it is apart here because it only needs the
// sync engine's `fetchSession` and `errorText`.
import Foundation

/// How looking a session up stands while it is not held.
public enum SessionLookup: Equatable, Sendable {
    /// Being asked for; nothing to say yet.
    case reading
    /// The server does not have it.
    case missing
    /// The server could not be asked.
    case failed(reason: String)
}

public let sessionAbsentTries = 5
public let sessionAbsentWaitMs: Int64 = 2_500

/// Looks a session up by its ID until it is held (`fetch` returns true) or
/// known to be missing. A session just started is acknowledged by its daemon
/// before the daemon has uploaded it, so the server not having it only
/// counts once it said so for ten seconds. The server not answering is no
/// answer: it is asked again, after three seconds and then twice as long
/// each time up to half a minute, and `report` says why meanwhile.
public func lookUpSession(
    fetch: () async throws -> Bool,
    wait: (Int64) async throws -> Void = { try await Task.sleep(nanoseconds: UInt64($0) * 1_000_000) },
    report: (SessionLookup) -> Void
) async throws {
    var absent = 0
    var pause: Int64 = 3_000
    while true {
        do {
            if try await fetch() { return }
            absent += 1
            if absent >= sessionAbsentTries { return report(.missing) }
            report(.reading)
            pause = 3_000
            try await wait(sessionAbsentWaitMs)
        } catch is CancellationError {
            throw CancellationError()
        } catch {
            try Task.checkCancellation()
            report(.failed(reason: errorText(error)))
            try await wait(pause)
            pause = min(pause * 2, 30_000)
        }
    }
}

/// What a page about a session says instead of its content while the session is not held; nil while it is only being read.
public func lookupNote(_ l: SessionLookup?) -> String? {
    switch l {
    case .missing?: return "找不到这个会话"
    case .failed(let reason)?: return "没能读取这个会话：\(reason)"
    default: return nil
    }
}
