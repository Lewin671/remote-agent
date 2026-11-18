import Testing
@testable import RACore

@Suite(.timeLimit(.minutes(2))) struct PublicationRetryTests {
    @Test func exhaustedCASRemainsPendingAndCanFinishOnTheNextPoll() async throws {
        let calls = Counter()
        await #expect(throws: CommandError.self) {
            try await retryRecoveryPublication { calls.increment() > 5 }
        }
        #expect(calls.value == 5)
        try await retryRecoveryPublication { calls.increment() > 5 }
        #expect(calls.value == 6)
    }
}
