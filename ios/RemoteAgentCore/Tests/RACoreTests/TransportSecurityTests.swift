import Foundation
import RAProto
import Testing

@testable import RACore

@Suite struct TransportSecurityTests {
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
        let api = try Api(
            server: "http://127.0.0.1:\(listener.port)", http: HTTP(configuration: .default))
        var ask = PBGetChallengeRequest()
        ask.deviceID = "test"
        #expect(await api.auth.getChallenge(request: ask).error != nil)
        await sleepMs(300)
        #expect(requests.value == 1)
    }

    @Test func aPlainRequestDoesNotFollowRedirects() async throws {
        let (listener, requests) = try redirector()
        defer { listener.close() }
        let (_, response) = try await HTTP().data(
            for: URLRequest(url: URL(string: "http://127.0.0.1:\(listener.port)/healthz")!))
        // The redirect is the answer.
        #expect(response.statusCode == 307)
        await sleepMs(300)
        #expect(requests.value == 1)
    }

    /// The first frame of a stream is the login token: a redirected upgrade would hand it to another address.
    @Test func aWebSocketUpgradeDoesNotFollowRedirects() async throws {
        let (listener, requests) = try redirector()
        defer { listener.close() }
        let socket = Socket(
            http: HTTP(), url: URL(string: "ws://127.0.0.1:\(listener.port)/v1/stream")!,
            hello: Data("token".utf8), sleep: realSleep)
        #expect(await !socket.opened())
        let end = await socket.ended()
        #expect(end.error != nil)
        await #expect(throws: (any Error).self) { try await socket.receive() }
        await sleepMs(300)
        #expect(requests.value == 1)
    }

    /// A link that died silently answers no ping: the socket is given up on at the second one.
    @Test func aSocketWhosePingIsNotAnsweredIsEnded() async throws {
        let hole = try StreamBlackhole()
        defer { hole.close() }
        let socket = Socket(
            http: HTTP(), url: URL(string: "ws" + hole.url.dropFirst(4) + "/v1/stream")!,
            hello: Data("x".utf8), pingMs: 150,
            sleep: realSleep)
        #expect(await socket.opened())
        let started = Date()
        let end = await socket.ended()
        #expect(networkFailure(end.error) == .timedOut)
        #expect(end.closeCode == nil)
        #expect(Date().timeIntervalSince(started) < 5)
    }

    /// A close code is how the server says why; the connection did not break.
    @Test func aSocketTheServerClosesTellsItsCloseCode() async throws {
        let hole = try StreamBlackhole(firstClose: 1008)
        defer { hole.close() }
        let socket = Socket(
            http: HTTP(), url: URL(string: "ws" + hole.url.dropFirst(4) + "/v1/stream")!,
            hello: Data("x".utf8), sleep: realSleep)
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
            let socket = Socket(http: http, url: url, hello: Data("x".utf8), sleep: realSleep)
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
            #expect(throws: UserError.self, "\(server)") { try Api(server: server, http: HTTP()) }
            #expect(throws: UserError.self, "\(server)") {
                try fixtureRemote(
                    account: Account(
                        server: server, root: Data(count: 32), deviceSeed: Data(count: 32),
                        deviceId: "d"), http: HTTP())
            }
        }
        // Plain http only on this device: a simulator's server, or a test's.
        #expect(
            try Api(server: "http://127.0.0.1:8080", http: HTTP()).server == "http://127.0.0.1:8080"
        )
        #expect(
            try Api(server: " HTTPS://Relay.Example/ ", http: HTTP()).server
                == "https://relay.example")
        #expect(
            try Api(server: "https://relay.example", http: HTTP()).socketURL("/v1/stream")
                .absoluteString == "wss://relay.example/v1/stream")
        #expect(
            try Api(server: "http://localhost:8787", http: HTTP()).socketURL("/v1/term")
                .absoluteString == "ws://localhost:8787/v1/term")
    }

    @Test func theDefaultStackKeepsNothingBetweenRequests() {
        let c = HTTP.defaultConfiguration()
        #expect(c.urlCache == nil)
        #expect(c.httpCookieStorage == nil)
        #expect(c.urlCredentialStorage == nil)
        #expect(c.requestCachePolicy == .reloadIgnoringLocalCacheData)
        #expect(Api.headers(token: "") == [:])
        #expect(Api.headers(token: "t") == ["Authorization": ["Bearer t"]])
    }
}
