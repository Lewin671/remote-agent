import Foundation

func retryRecoveryPublication(isolation: isolated (any Actor)? = #isolation, _ work: () async throws -> Bool) async throws {
    for _ in 0..<5 { if try await work() { return } }
    throw CommandError("aborted", L10nMessage.errorsTheRecoveryObjectWasUpdatedConcurrentlyRetryingOnThe())
}
