import Foundation

/// Retrying a command whose outcome is unknown must send exactly the same
/// thing again: the same envelope under the same request ID and deadline, so
/// the daemon answers from its log instead of running it twice
/// (architecture §10). This holds the command while its outcome is unknown.
///
/// Once such a command has expired, whether it ran stays unknown: the daemon
/// only remembers a request until shortly after its deadline. The attempt
/// then stays stuck rather than sealing a new command, which could run the
/// same thing twice; only `abandon`, the user's explicit choice, frees it.
///
/// One screen owns an attempt and uses it from its own actor.
public final class Attempt {
    public private(set) var pending: PreparedCommand?

    /// The next attempt repeats one whose outcome is unknown.
    public private(set) var frozen = false

    /// The unknown command expired: it is not sent again.
    public private(set) var expired = false

    public init() {}

    /// What to send: an earlier command whose outcome is unknown wins over a new one.
    public func next(_ make: () throws -> PreparedCommand) throws -> PreparedCommand {
        if expired { throw CommandError(CommandError.unknownOutcome, Attempt.expiredUnknown) }
        if let pending { return pending }
        let made = try make()
        pending = made
        return made
    }

    /// Records how the command ended and returns the error to show. Success or
    /// a definite failure frees the next attempt; an unknown outcome freezes
    /// it; an expiry after an unknown outcome leaves it frozen and expired.
    @discardableResult
    public func settle(_ error: Error? = nil) -> Error? {
        guard let error else {
            abandon()
            return nil
        }
        let command = error as? CommandError
        if uncertain(error) {
            frozen = true
        } else if frozen, let command, Attempt.expiry(command) {
            expired = true
            return CommandError(CommandError.unknownOutcome, Attempt.expiredUnknown)
        } else if frozen, command?.fromDaemon != true {
            // Once unknown, only the daemon's own answer settles it; any
            // other failure (a network error, a failed login) leaves it
            // unknown, to be retried under the same request ID.
            return CommandError(CommandError.unknownOutcome, "上一次的结果仍不确定：\(errorText(error))。重试会发送同一个请求")
        } else {
            abandon()
        }
        return error
    }

    /// Gives up on an unknown command, e.g. to start over with other content.
    public func abandon() {
        pending = nil
        frozen = false
        expired = false
    }

    public static let expiredUnknown = "不确定上一次是否已经执行，它已过期，不会再重发。请先去列表里确认，需要时放弃后重新开始"

    private static func expiry(_ e: CommandError) -> Bool {
        e.code == CommandError.expired || (e.fromDaemon && e.code == "deadline_exceeded")
    }
}

/// The command may or may not have run.
public func uncertain(_ e: Error) -> Bool { (e as? CommandError)?.unknown == true }
