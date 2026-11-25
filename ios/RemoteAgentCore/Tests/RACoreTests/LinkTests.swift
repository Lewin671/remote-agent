import Foundation
import RAProto
import Testing
@testable import RACore

@Suite(.timeLimit(.minutes(2))) struct LinkCommandTests {
    @Test func missingSealedEnvelopeFailsLocallyWithoutForwarding() async throws {
        let account = Account(server: "https://example.com", deviceId: "dev-1", accountId: "acc-1")
        let remote = try Remote(account: account, http: HTTP(), securityStore: MemorySecurityStoreV4())
        defer { remote.close() }
        let cmdBytes = try PBCommand.with { $0.getSettings = PBGetSettingsRequest() }.serializedData()
        let eff = PBCoreDispatchCommandEffect.with {
            $0.requestID = newRequestId()
            $0.attemptID = newRequestId()
            $0.machineID = "mach-1"
            $0.deadlineMs = nowMs() + 60_000
            $0.commandBytes = cmdBytes
            $0.routeLeg = .relay
        }
        let outcome = await remote.dispatchCommandAttempt(eff)
        #expect(outcome.requestID == eff.requestID)
        #expect(outcome.attemptID == eff.attemptID)
        #expect(outcome.source == .local)
        #expect(outcome.notForwarded)
        #expect(outcome.transportCode == "failed_precondition")
        #expect(outcome.transportDetail == "missing sealed command")
    }
}

@Suite(.timeLimit(.minutes(2))) struct LinkStreamTests {
    private static let parkedSleep: Sleeper = { _ in
        try await Task.sleep(for: .seconds(3600))
    }

    @Test func closedLinkDisposesAStreamRegisteredAfterSuspendedOpening() throws {
        let path = RelayPath(api: try Api(server: "https://example.com", http: HTTP()), sleep: Self.parkedSleep)
        let link = Link(relay: path)
        let stopped = Counter(), notified = Counter()
        link.closeStreams()
        let stream = link.openStream(stop: { _ in stopped.increment() }, onClose: { _ in notified.increment() })
        #expect(stopped.value == 1)
        stream.end("late callback")
        link.closeStreams()
        #expect(stopped.value == 1)
        #expect(notified.value == 0)
    }

    @Test func physicalPathEndingDoesNotOwnProductClose() async throws {
        let server = try StreamBlackhole(firstClose: 1000)
        defer { server.close() }
        let path = RelayPath(api: try Api(server: server.url, http: HTTP()), sleep: Self.parkedSleep)
        let link = Link(relay: path)
        let closed = Counter(), stopped = Counter()
        let stream = link.openStream(stop: { _ in stopped.increment() }, onClose: { _ in closed.increment() })
        let socket = path.openStream("/v1/term", hello: Data("opaque".utf8))
        await #expect(throws: (any Error).self) { try await socket.receive() }
        #expect(await socket.ended().closeCode == 1000)
        #expect(closed.value == 0)
        stream.end("closed")
        stream.end("late callback")
        #expect(closed.value == 1)
        #expect(stopped.value == 1)
    }
}
