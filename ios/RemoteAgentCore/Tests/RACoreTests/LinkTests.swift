import Foundation
import RAProto
import Testing
import os
@testable import RACore

private func linkedState(_ id: String) -> RemoteState {
    var peer = PeerState()
    peer.reachable = true; peer.ready = true; peer.authorized = true
    peer.relayRequired = false
    peer.protocolVersion = protocolVersion
    peer.capabilities = .with { $0.fileUpload = true; $0.scheduledTasks = true }
    var state = RemoteState()
    state.conn = .offline
    state.link = .noServer
    state.machines[id] = MachineView(id: id, online: false, lastSeenMs: 0,
        meta: .with { $0.protocolVersion = protocolVersion - 1 }, linkState: peer)
    state.currentMachines = [id]
    return state
}

private final class GuardedLinkStorage: DeviceSecurity.Storage, Sendable {
    let base: any DeviceSecurity.Storage
    let blocked = OSAllocatedUnfairLock(initialState: false)
    init(_ base: any DeviceSecurity.Storage) { self.base = base }
    func readSecurity() throws -> DeviceSecurity.Record {
        if blocked.withLock({ $0 }) { throw DeviceSecurity.Failure.lease }
        return try base.readSecurity()
    }
    func initializeSecurity(_ record: DeviceSecurity.Record) throws { try base.initializeSecurity(record) }
    func writeSecurity(expectedHead: Data, _ record: DeviceSecurity.Record) throws -> DeviceSecurity.Record {
        try base.writeSecurity(expectedHead: expectedHead, record)
    }
}

extension ServerTests {
    @Suite struct LinkTests {
        @Test func linkProjectionControlsCommandsAndProjectGates() async throws {
            try await World.run(setUp: { w in
                await w.daemon.setAnswer { _, _ in .with { $0.createSession.session.id = "created" } }
                try await w.daemon.connect()
            }) { w in
                let remote = try await w.client()
                let state = linkedState(w.daemon.id)
                remote.state.set(state)
                #expect(state.peer(w.daemon.id).available)
                #expect(fileSupported(state.machines[w.daemon.id]))
                #expect(protocolMismatch(state.machines[w.daemon.id]) == nil)
                #expect(state.peer(w.daemon.id).canProbe(false))
                let command = PBCommand.with { $0.createSession.cwd = "/work/example"; $0.createSession.fileIds = ["original-file"] }
                let prepared = try remote.prepare(machineId: w.daemon.id, sessionId: "", command)
                _ = try await remote.forward(prepared)
                let received = try await w.daemon.commands.take()
                #expect(received.0.requestID == prepared.requestId)
                #expect(received.1.createSession.fileIds == ["original-file"])
            }
        }

        @Test func reachabilityCannotAuthorizeOrClearPriorEvidence() async throws {
            try await World.run(setUp: { _ in }) { w in
                let account = try await w.join()
                let store = GuardedLinkStorage(try #require(TestSecurity.find(w.server.url)?.store(account.deviceId)))
                let forwards = Counter()
                let http = Intercept.http(for: w.server.url) { request in
                    if request.url?.path.hasSuffix("/Forward") == true { forwards.increment() }
                    return request
                }
                let remote = try Remote(account: account, http: http, securityStorage: store)
                defer { remote.close() }
                _ = try await remote.currentToken()
                remote.state.set(linkedState(w.daemon.id))
                let prepared = try remote.prepare(machineId: w.daemon.id, sessionId: "", PBCommand.with { $0.getSettings = PBGetSettingsRequest() })
                store.blocked.withLock { $0 = true }
                do { _ = try await remote.forward(prepared); Issue.record("authorization bypassed") }
                catch {
                    #expect(forwards.value == 0)
                    #expect((error as? CommandError)?.code == "authorization_unavailable")
                    #expect((error as? CommandError)?.outcome == .notSent)
                }
                do { _ = try await remote.forward(prepared, retryOfUnknown: true); Issue.record("authorization bypassed on retry") }
                catch { #expect(uncertain(error)) }
                #expect(forwards.value == 0)
            }
        }
    }
}

@Suite struct LinkStreamTests {
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
