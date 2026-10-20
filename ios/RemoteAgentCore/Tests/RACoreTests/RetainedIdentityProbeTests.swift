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
        let attempts = Counter(), cancelled = Counter()
        let start = ContinuousClock.now
        let denied = await probeRetainedIdentity(timeoutMs: 500) {
            if attempts.increment() == 1 {
                try await Task.sleep(for: .milliseconds(300))
                throw ConnectError(code: .permissionDenied, message: "login failed")
            }
            do { try await Task.sleep(for: .seconds(30)) }
            catch { cancelled.increment(); throw error }
        }
        #expect(!denied)
        #expect(attempts.value == 2)
        #expect(cancelled.value == 1)
        #expect(start.duration(to: .now) < .milliseconds(750))
    }

    @Test func aTimedOutRealLoginIsCancelledInsteadOfWaitingForTheHTTPTimeout() async throws {
        let origin = "https://probe.example.com"
        let requests = Counter()
        let http = Intercept.http(for: origin) { request in
            requests.increment()
            try await Task.sleep(for: .seconds(30))
            return request
        }
        let api = try Api(server: origin, http: http)
        let account = Account(server: origin, root: Data(), deviceSeed: Data(repeating: 1, count: 32), deviceId: "synthetic-device")
        let start = ContinuousClock.now
        let denied = await probeRetainedIdentity(timeoutMs: 100) {
            _ = try await login(api: api, account: account)
        }
        #expect(!denied)
        #expect(requests.value == 1)
        #expect(start.duration(to: .now) < .seconds(2))
    }
}
