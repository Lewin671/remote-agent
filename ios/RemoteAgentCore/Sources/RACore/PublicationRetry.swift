import Foundation

func retryRecoveryPublication(isolation: isolated (any Actor)? = #isolation, _ work: () async throws -> Bool) async throws {
    for _ in 0..<5 { if try await work() { return } }
    throw CommandError("aborted", "恢复对象同时更新，将在下次同步重试")
}
