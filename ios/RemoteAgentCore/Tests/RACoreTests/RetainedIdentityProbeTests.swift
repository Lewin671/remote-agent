import Connect
import Foundation
import Testing

@testable import RACore

@Suite struct RetainedIdentityProbeTests {
    @Test func aForgottenChallengeGetsOneFreshLoginBeforeDenial() async {
        for deniedAgain in [false, true] {
            let attempts = Counter()
            let denied = await probeRetainedIdentity {
                if attempts.increment() == 1 || deniedAgain {
                    throw ConnectError(code: .permissionDenied, message: "login failed")
                }
            }
            #expect(denied == deniedAgain)
            #expect(attempts.value == 2)
        }
    }

    @Test func offlineOnTheRetryDoesNotEstablishDenial() async {
        let attempts = Counter()
        let denied = await probeRetainedIdentity {
            if attempts.increment() == 1 {
                throw ConnectError(code: .permissionDenied, message: "login failed")
            }
            throw URLError(.notConnectedToInternet)
        }
        #expect(!denied)
        #expect(attempts.value == 2)
    }

    @Test func bothAttemptsShareTheDeadlineAndCancelTheSlowRetry() async {
        let attempts = Counter(), cancelled = Counter(), timers = Counter()
        let retry = AsyncStream<Void>.makeStream()
        let deadline = AsyncStream<Void>.makeStream()
        let probe = Task {
            await probeRetainedIdentity(timeoutMs: 500, sleep: { ms in
                #expect(ms == 500)
                timers.increment()
                for await _ in deadline.stream { break }
                try Task.checkCancellation()
            }) {
                if attempts.increment() == 1 {
                    throw ConnectError(code: .permissionDenied, message: "login failed")
                }
                retry.continuation.yield()
                do { try await Task.sleep(for: .seconds(30)) }
                catch { cancelled.increment(); throw error }
            }
        }
        // Fire the one shared deadline only once the second attempt is in
        // flight. Executor scheduling no longer consumes an asserted budget.
        for await _ in retry.stream { break }
        deadline.continuation.finish()
        let denied = await probe.value
        #expect(!denied)
        #expect(attempts.value == 2)
        #expect(cancelled.value == 1)
        #expect(timers.value == 1)
    }

    @Test func aTimedOutRealLoginIsCancelledInsteadOfWaitingForTheHTTPTimeout() async throws {
        let origin = "https://probe.example.com"
        let requests = Counter()
        // The deadline fires once the request is in flight, not after a fixed
        // 100 ms: a loaded machine can spend longer than that signing the login,
        // and then the request was never sent.
        let sent = AsyncStream<Void>.makeStream()
        let http = Intercept.http(for: origin) { request in
            requests.increment()
            sent.continuation.yield()
            try await Task.sleep(for: .seconds(30))
            return request
        }
        let api = try Api(server: origin, http: http)
        let account = Account(server: origin, root: Data(), deviceSeed: Data(repeating: 1, count: 32), deviceId: "synthetic-device")
        let start = ContinuousClock.now
        let denied = await probeRetainedIdentity(timeoutMs: 100, sleep: { _ in
            for await _ in sent.stream { break }
            try Task.checkCancellation()
        }) {
            _ = try await login(api: api, account: account)
        }
        #expect(!denied)
        #expect(requests.value == 1)
        #expect(start.duration(to: .now) < .seconds(2))
    }
}
