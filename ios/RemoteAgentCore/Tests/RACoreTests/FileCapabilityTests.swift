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

/// Only for a Remote that was never started: shows fixed machine capability snapshot.
private func showFileSupport(_ remote: Remote, machine: String, supported: Bool?) {
    var state = remote.current
    state.machines[machine] = MachineView(id: machine, online: true, lastSeenMs: 0,
        meta: supported.map { value in .with { $0.protocolVersion = protocolVersion; $0.capabilities.fileUpload = value } })
    remote.state.set(state)
}

@Suite struct FileCapabilityTests {
    @Test(arguments: [false, true])
    func sessionEntriesPreserveTheUpdateNoticeWithoutALiveRoute(prepareOnly: Bool) async throws {
        let remote = try Remote(account: Account(server: "http://127.0.0.1:1", root: Data(count: 32), deviceSeed: Data(count: 32), deviceId: "00112233445566778899aabbccddeeff"), http: HTTP())
        defer { remote.close() }
        showFileSupport(remote, machine: "m", supported: false)
        var shown = remote.current
        shown.sessions["s1"] = SessionView(id: "s1", machineId: "m")
        remote.state.set(shown)
        do {
            if prepareOnly { _ = try await remote.prepare("s1", fileCommand(create: false)) }
            else { _ = try await remote.command("s1", fileCommand(create: false)) }
            Issue.record("file request passed the capability gate")
        } catch {
            #expect((error as? CommandError)?.code == "file_upload_unsupported")
            #expect(errorText(error).contains("更新"))
        }
    }

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
