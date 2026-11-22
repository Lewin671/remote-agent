import Foundation
import RAProto
import Testing
import os
@testable import RACore

private func linkedState(_ id: String) -> RemoteState {
    var peer = PeerState()
    peer.reachable = true; peer.ready = true; peer.authorized = true
    peer.relayRequired = false
    peer.protocolVersion = peerProtocolVersion
    peer.capabilities = .with { $0.fileUpload = true; $0.scheduledTasks = true }
    var state = RemoteState()
    state.conn = .offline
    state.link = .noServer
    state.machines[id] = MachineView(id: id, online: false, lastSeenMs: 0,
        meta: .with { $0.protocolVersion = peerProtocolVersion - 1 }, linkState: peer)
    state.currentMachines = [id]
    return state
}

private final class GuardedSecurityStore: SecurityStoreV4, Sendable {
    let base: MemorySecurityStoreV4
    let blocked = OSAllocatedUnfairLock(initialState: false)
    init(_ base: MemorySecurityStoreV4) { self.base = base }
    func hasPersistedState() -> Bool { base.hasPersistedState() }
    func generateKey(kind: PBSecurityKeyKind, slot: String, persist: Bool) throws -> Data {
        try base.generateKey(kind: kind, slot: slot, persist: persist)
    }
    func signKey(kind: PBSecurityKeyKind, slot: String, message: Data) throws -> Data {
        if blocked.withLock({ $0 }) { throw SecurityFailure.lease }
        return try base.signKey(kind: kind, slot: slot, message: message)
    }
    func agreeKey(kind: PBSecurityKeyKind, slot: String, peerPublic: Data) throws -> Data {
        try base.agreeKey(kind: kind, slot: slot, peerPublic: peerPublic)
    }
    func encapsulate(recipients: [PBSecurityEncapsulateRecipient]) throws -> [PBSecurityEncapsulatedPair] {
        try base.encapsulate(recipients: recipients)
    }
    func discardKey(kind: PBSecurityKeyKind, slot: String) { base.discardKey(kind: kind, slot: slot) }
    func executeStorage(_ effect: PBSecurityStorageEffect) -> PBSecurityStorageResultEvent {
        base.executeStorage(effect)
    }
    func dismissLegacyRepairNotice() { base.dismissLegacyRepairNotice() }
    func clearMemoryCache() { base.clearMemoryCache() }
    func loadLoginToken(server: String, deviceId: String) -> SavedLogin? {
        base.loadLoginToken(server: server, deviceId: deviceId)
    }
    func saveLoginToken(_ token: SavedLogin, server: String, deviceId: String) {
        base.saveLoginToken(token, server: server, deviceId: deviceId)
    }
    func clearLoginToken(server: String, deviceId: String) {
        base.clearLoginToken(server: server, deviceId: deviceId)
    }
}

extension ServerTests {
    @Suite(.timeLimit(.minutes(2))) struct LinkTests {
        @Test func linkProjectionControlsCommandsAndProjectGates() async throws {
            try await World.run(setUp: { w in
                await w.daemon.setAnswer { _, _ in .with { $0.createSession.session.id = "created" } }
                try await w.daemon.connect()
            }) { w in
                let remote = try await w.client()
                let state = linkedState(w.daemon.id)
                await remote.showForTest(state: state)
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
                let baseStore = try #require(TestSecurity.find(w.server.url)?.store(account.deviceId))
                let store = GuardedSecurityStore(baseStore)
                let forwards = Counter()
                let http = Intercept.http(for: w.server.url) { request in
                    if request.url?.path.hasSuffix("/Forward") == true { forwards.increment() }
                    return request
                }
                let remote = try Remote(account: account, http: http, securityStore: store, allowPlainHttp: true)
                defer { remote.close() }
                _ = try await remote.currentToken()
                try await remote.ensureSecurityBootstrapped()
                await remote.showForTest(state: linkedState(w.daemon.id))
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
