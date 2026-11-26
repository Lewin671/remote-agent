import Foundation
import RAProto
import Testing

@testable import RACore

private final class PingGate: @unchecked Sendable {
    private let lock = NSLock()
    private var arrived = 0
    private var arrivalWaiters: [(target: Int, cont: CheckedContinuation<Void, Never>)] = []
    private var sleepers: [(id: UUID, cont: CheckedContinuation<Void, any Error>)] = []
    private var credits = 0

    func sleep(_ ms: Int64) async throws {
        try Task.checkCancellation()
        let id = UUID()
        let toWake = lock.withLock { () -> [CheckedContinuation<Void, Never>] in
            arrived += 1
            let ready = arrivalWaiters.filter { arrived >= $0.target }.map(\.cont)
            arrivalWaiters.removeAll { arrived >= $0.target }
            return ready
        }
        for w in toWake { w.resume() }
        try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { (c: CheckedContinuation<Void, any Error>) in
                let action = lock.withLock { () -> Int in
                    if Task.isCancelled { return -1 }
                    if credits > 0 { credits -= 1; return 1 }
                    sleepers.append((id, c))
                    return 0
                }
                if action == 1 { c.resume() }
                else if action == -1 { c.resume(throwing: CancellationError()) }
            }
        } onCancel: {
            let c = lock.withLock { () -> CheckedContinuation<Void, any Error>? in
                guard let i = sleepers.firstIndex(where: { $0.id == id }) else { return nil }
                return sleepers.remove(at: i).cont
            }
            c?.resume(throwing: CancellationError())
        }
    }

    func step(_ count: Int) async {
        await withCheckedContinuation { (c: CheckedContinuation<Void, Never>) in
            let ready = lock.withLock { () -> Bool in
                if arrived >= count { return true }
                arrivalWaiters.append((count, c))
                return false
            }
            if ready { c.resume() }
        }
        let sleeper = lock.withLock { () -> CheckedContinuation<Void, any Error>? in
            if !sleepers.isEmpty { return sleepers.removeFirst().cont }
            credits += 1
            return nil
        }
        sleeper?.resume()
    }
}

@Suite(.timeLimit(.minutes(2))) struct TransportSecurityTests {
    /// A server that answers every request with a redirect to itself, and counts the requests it got.
    private func redirector() throws -> (Listener, Counter) {
        let listener = try Listener()
        let requests = Counter()
        let port = listener.port
        listener.serve { fd in
            guard readHead(fd) != nil else { return }
            requests.increment()
            writeAll(
                fd,
                Data(
                    "HTTP/1.1 307 Temporary Redirect\r\nLocation: http://127.0.0.1:\(port)/leak\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"
                        .utf8))
        }
        return (listener, requests)
    }

    @Test func rpcDoesNotFollowRedirectsEvenWithAnInjectedPermissiveConfiguration() async throws {
        let (listener, requests) = try redirector()
        defer { listener.close() }
        // The system's default configuration follows redirects; the stack built on it must not.
        var ask = PBGetChallengeRequest()
        ask.deviceID = "test"
        let res = await SecurityExecutor.executeRelayRpcUnary(
            http: HTTP(configuration: .default),
            method: .getChallenge,
            server: "http://127.0.0.1:\(listener.port)",
            requestBytes: (try? ask.serializedData()) ?? Data(),
            allowPlainHttp: true,
            opID: 1
        )
        #expect(res?.connectCode != 0)
        #expect(requests.value == 1)
    }

    @Test func aPlainRequestDoesNotFollowRedirects() async throws {
        let (listener, requests) = try redirector()
        defer { listener.close() }
        let (_, response) = try await HTTP().data(
            for: URLRequest(url: URL(string: "http://127.0.0.1:\(listener.port)/healthz")!))
        // The redirect is the answer.
        #expect(response.statusCode == 307)
        #expect(requests.value == 1)
    }

    private static let parkedSleep: Sleeper = { _ in
        try await Task.sleep(for: .seconds(3600))
    }

    /// The first frame of a stream is the login token: a redirected upgrade would hand it to another address.
    @Test func aWebSocketUpgradeDoesNotFollowRedirects() async throws {
        let (listener, requests) = try redirector()
        defer { listener.close() }
        let socket = Socket(
            http: HTTP(), url: URL(string: "ws://127.0.0.1:\(listener.port)/v1/stream")!,
            hello: Data("token".utf8), sleep: Self.parkedSleep)
        #expect(await !socket.opened())
        let end = await socket.ended()
        #expect(end.error != nil)
        await #expect(throws: (any Error).self) { try await socket.receive() }
        #expect(requests.value == 1)
    }

    /// A link that died silently answers no ping: the socket is given up on at the second one.
    @Test func aSocketWhosePingIsNotAnsweredIsEnded() async throws {
        let hole = try StreamBlackhole()
        defer { hole.close() }
        let gate = PingGate()
        let socket = Socket(
            http: HTTP(), url: URL(string: "ws" + hole.url.dropFirst(4) + "/v1/stream")!,
            hello: Data("x".utf8), pingMs: 150,
            sleep: { ms in
                #expect(ms == 150)
                try await gate.sleep(ms)
            })
        #expect(await socket.opened())
        await gate.step(1)
        await gate.step(2)
        let end = await socket.ended()
        #expect(networkFailure(end.error) == .timedOut)
        #expect(end.closeCode == nil)
    }

    /// A close code is how the server says why; the connection did not break.
    @Test func aSocketTheServerClosesTellsItsCloseCode() async throws {
        let hole = try StreamBlackhole(firstClose: 1008)
        defer { hole.close() }
        let socket = Socket(
            http: HTTP(), url: URL(string: "ws" + hole.url.dropFirst(4) + "/v1/stream")!,
            hello: Data("x".utf8), sleep: Self.parkedSleep)
        await #expect(throws: (any Error).self) { try await socket.receive() }
        let end = await socket.ended()
        #expect(end.closeCode == 1008)
        #expect(end.error == nil)
    }

    @Test func aSocketClosedBeforeReceiveStartsStillKeepsItsCloseCode() async throws {
        let hole = try StreamBlackhole(firstClose: 1008)
        defer { hole.close() }
        let socket = Socket(
            http: HTTP(), url: URL(string: "ws" + hole.url.dropFirst(4) + "/v1/stream")!,
            hello: Data("x".utf8), sleep: Self.parkedSleep)
        #expect(await socket.opened())
        await #expect(throws: (any Error).self) { try await socket.receive() }
        let end = await socket.ended()
        #expect(end.closeCode == 1008)
        #expect(end.error == nil)
    }

    /// The engine drops a socket as soon as its frames end, which may be before the task says it is over.
    @Test func droppingASocketThatAlreadyBrokeKeepsWhyItBroke() async throws {
        // Nothing listens here: the connection is refused.
        let url = URL(string: "ws://127.0.0.1:\(freePort())/v1/stream")!
        let http = HTTP()
        for _ in 0..<40 {
            let socket = Socket(http: http, url: url, hello: Data("x".utf8), sleep: Self.parkedSleep)
            do {
                _ = try await socket.receive()
                Issue.record("a refused connection gave a frame")
            } catch {
                socket.cancel()
            }
            let end = await socket.ended()
            #expect(networkFailure(end.error) == .refused)
        }
    }

    @Test func everyRpcEntryRejectsInsecureRemoteOrigins() throws {
        for server in [
            "http://relay.example", "http://192.168.1.2:8080",
            "https://user:password@relay.example", "ftp://relay.example", "",
        ] {
            #expect(throws: UserError.self, "\(server)") { try RelayOrigin(server: server) }
            #expect(throws: UserError.self, "\(server)") {
                try fixtureRemote(
                    account: Account(
                        server: server,
                        deviceId: "d"), http: HTTP())
            }
        }
        // Plain http only on this device: a simulator's server, or a test's.
        #expect(
            try RelayOrigin(server: "http://127.0.0.1:8080").server == "http://127.0.0.1:8080"
        )
        #expect(
            try RelayOrigin(server: " HTTPS://Relay.Example/ ").server
                == "https://relay.example")
        #expect(
            try RelayOrigin(server: "https://relay.example").socketURL("/v1/stream")
                .absoluteString == "wss://relay.example/v1/stream")
        #expect(
            try RelayOrigin(server: "http://localhost:8787").socketURL("/v1/term")
                .absoluteString == "ws://localhost:8787/v1/term")
    }

    @Test func theDefaultStackKeepsNothingBetweenRequests() {
        let c = HTTP.defaultConfiguration()
        #expect(c.urlCache == nil)
        #expect(c.httpCookieStorage == nil)
        #expect(c.urlCredentialStorage == nil)
        #expect(c.requestCachePolicy == .reloadIgnoringLocalCacheData)
    }
}
