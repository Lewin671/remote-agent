import Foundation
import RAProto
import Testing
@testable import RACore

private func fileCommand(create: Bool) -> PBCommand {
    .with {
        if create { $0.createSession.cwd = "/work/example"; $0.createSession.prompt = "keep files"; $0.createSession.fileIds = ["file-1"] }
        else { $0.send.sessionID = "s1"; $0.send.requestID = "message-1"; $0.send.text = "keep files"; $0.send.fileIds = ["file-1"] }
    }
}
private func showFileSupport(_ remote: Remote, machine: String, supported: Bool?) {
    var state = remote.current
    state.machines[machine] = MachineView(id: machine, online: true, lastSeenMs: 0,
        meta: supported.map { value in .with { $0.protocolVersion = protocolVersion; $0.capabilities.fileUpload = value } })
    remote.state.set(state)
}

@Suite struct FileCapabilityTests {
    @Test(arguments: [false, true], [false, nil] as [Bool?])
    func preparationRejectsTheWholeFileRequest(create: Bool, support: Bool?) throws {
        let remote = try Remote(account: Account(server: "http://127.0.0.1:1", root: Data(count: 32), deviceSeed: Data(count: 32), deviceId: "00112233445566778899aabbccddeeff"), http: HTTP())
        defer { remote.close() }
        showFileSupport(remote, machine: "m", supported: support)
        do { _ = try remote.prepare(machineId: "m", sessionId: create ? "" : "s1", fileCommand(create: create)); Issue.record("file request was prepared without current capability") }
        catch { #expect(errorText(error).contains("更新")) }
        // The gate must leave unrelated text and image requests usable.
        var text = fileCommand(create: create)
        if create { text.createSession.fileIds = []; text.createSession.imageIds = ["image"] }
        else { text.send.fileIds = []; text.send.imageIds = ["image"] }
        _ = try remote.prepare(machineId: "m", sessionId: create ? "" : "s1", text)
    }
}

extension ServerTests {
    @Suite struct FileCapabilityForwardTests {
        private func world(_ body: (World, Remote) async throws -> Void) async throws {
            try await World.run(setUp: { w in
                try await w.daemon.putMachine("fixture-mac")
                try await w.daemon.putSession("s1", session("/work/example", "files"))
                try await w.daemon.connect()
            }) { w in
                let r = try await w.client(); r.start()
                _ = try await r.awaitState("machine and session") { $0.conn == .online && $0.sessions["s1"]?.info != nil }
                showFileSupport(r, machine: w.daemon.id, supported: true)
                try await body(w, r)
            }
        }

        @Test(arguments: [false, true])
        func aPreparedOrUnknownRequestIsBlockedUntilSupportReturns(create: Bool) async throws {
            try await world { w, r in
                await w.daemon.setAnswer { _, command in
                    if case .createSession? = command.kind { return .with { $0.createSession.session.id = "created" } }
                    return FakeDaemon.defaultResult(command)
                }
                let cmd = fileCommand(create: create), a = Attempt()
                let p = try a.next { try r.prepare(machineId: w.daemon.id, sessionId: create ? "" : "s1", cmd) }
                showFileSupport(r, machine: w.daemon.id, supported: false)
                do { _ = try await r.forward(p); Issue.record("prepared file request crossed the disabled capability") }
                catch { #expect(!uncertain(error) && errorText(error).contains("更新")) }
                #expect(await w.daemon.commands.poll(30) == nil)
                showFileSupport(r, machine: w.daemon.id, supported: true)
                _ = try await r.forward(p)
                let first = try #require(await w.daemon.commands.poll(1000))
                a.settle(CommandError(CommandError.unknownOutcome, "lost"))
                showFileSupport(r, machine: w.daemon.id, supported: false)
                do { _ = try await r.forward(p, retryOfUnknown: true); Issue.record("unknown file request crossed the disabled capability") }
                catch {
                    #expect(uncertain(error) && errorText(error).contains("更新"))
                    a.settle(error)
                }
                #expect(a.frozen && a.pending === p)
                #expect(await w.daemon.commands.poll(30) == nil)
                showFileSupport(r, machine: w.daemon.id, supported: true)
                _ = try await r.forward(try a.next { throw UserError("must reuse the original") }, retryOfUnknown: true)
                let again = try #require(await w.daemon.commands.poll(1000))
                #expect(again.0.requestID == first.0.requestID && again.0.deadlineMs == first.0.deadlineMs)
                #expect(again.1 == first.1)
            }
        }

        @Test func signingCannotCarryAFilePastACapabilityRefresh() async throws {
            try await world { w, r in
                let p = try r.prepare(machineId: w.daemon.id, sessionId: "s1", fileCommand(create: false))
                let hash = try await r.authorizationStateHash()
                _ = p.authorizationTask(stateHash: hash) { verify in Task {
                    let sealed = try await r.authorizePrepared(p, verifyObservation: verify)
                    showFileSupport(r, machine: w.daemon.id, supported: false)
                    return sealed
                } }
                do { _ = try await r.forward(p); Issue.record("file was forwarded after capability changed while signing") }
                catch { #expect(errorText(error).contains("更新")) }
                #expect(await w.daemon.commands.poll(30) == nil)
            }
        }

        @Test func textAndImagesCanStillReachTheMachineWithoutFileSupport() async throws {
            try await world { w, r in
                showFileSupport(r, machine: w.daemon.id, supported: false)
                var command = fileCommand(create: false)
                command.send.fileIds = []; command.send.imageIds = ["image-1"]
                _ = try await r.command("s1", command)
                let received = try #require(await w.daemon.commands.poll(1000))
                #expect(received.1.send.text == "keep files" && received.1.send.imageIds == ["image-1"])
            }
        }

        @Test func anAutomaticNetworkRetryRechecksCapabilityAndKeepsUnknown() async throws {
            try await world { w, r in
                let calls = Counter(), active = Feed<Remote?>(nil)
                let http = Intercept.http(for: w.server.url) { request in
                    if request.url?.path.hasSuffix("/Forward") == true {
                        if calls.increment() == 1, let current = active.value {
                            showFileSupport(current, machine: w.daemon.id, supported: false)
                            throw URLError(.timedOut)
                        }
                    }
                    return request
                }
                let retrying = try w.remote(try await w.join(), http: http)
                active.set(retrying); retrying.start()
                _ = try await retrying.awaitState("retrying machine") { $0.conn == .online && $0.sessions["s1"]?.info != nil }
                showFileSupport(retrying, machine: w.daemon.id, supported: true)
                let p = try retrying.prepare(machineId: w.daemon.id, sessionId: "s1", fileCommand(create: false))
                do { _ = try await retrying.forward(p); Issue.record("expected unknown result") }
                catch { #expect(uncertain(error) && errorText(error).contains("更新")) }
                #expect(calls.value == 1)
                #expect(await w.daemon.commands.poll(30) == nil)
            }
        }
    }
}
