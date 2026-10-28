import Foundation
import RACore
import RAProto
import Testing
@testable import RemoteAgent

@MainActor @Suite struct SessionFilesTests {
    private func document() throws -> (URL, PendingImage) {
        let url = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString + ".csv")
        try Data("a,b\n1,2\n".utf8).write(to: url)
        let p = PendingImage(id: newRequestId(), uri: nil)
        p.file = try PickedFile(url: url)
        p.state = .ready; p.machineId = "mac"; p.imageId = "uploaded-file"; p.expiresMs = Int64.max
        return (url, p)
    }
    private func until(_ done: @MainActor () -> Bool) async {
        for _ in 0..<200 where !done() { try? await Task.sleep(for: .milliseconds(10)) }
    }

    @Test func losingFileSupportCancelsEvenAnAlreadyReadyFilesRefresh() async throws {
        let (url, p) = try document()
        defer { try? FileManager.default.removeItem(at: url) }
        let account = Account(server: "http://127.0.0.1:1", root: Data(count: 32), deviceSeed: Data(count: 32), deviceId: "00112233445566778899aabbccddeeff")
        let remote = try Remote(account: account, http: AppModel.http)
        ImageCenter.install(remote: remote, files: VolatileFiles())
        defer { ImageCenter.remove(remote) }
        let center = ImageCenter.of(remote)
        let draft = center.draft("session:test")
        await center.restoredDraft(draft.key)
        draft.append([p])
        let timer = Task<Void, Never> { try? await Task.sleep(for: .seconds(3600)) }
        p.refreshJob = timer
        let machine = MachineView(id: "mac", online: true, lastSeenMs: 0, meta: .with { $0.capabilities.fileUpload = false })
        center.aim(draft.key, at: "mac", blocked: true, machine: machine)
        #expect(timer.isCancelled)
        #expect(!p.readyFor("mac"))
        #expect(!draft.filesAllowed(for: machine))
    }

    @Test func fileOnlyNewSessionAndUnknownRetryKeepTheFirstFileIDs() async throws {
        let (url, p) = try document()
        defer { try? FileManager.default.removeItem(at: url) }
        let commands = FakeCommands()
        commands.answer(CommandError(CommandError.unknownOutcome, "unknown"))
        let cfg = NewConfig(machineId: "mac", cwd: "/work/example")
        let draft = NewSessionDraft(cfg, settled: true)
        let attachments = ImageDraft(key: "new", files: VolatileFiles(), saves: nil)
        attachments.append([p])
        draft.start(commands, cfg, images: attachments) { _, _, _ in }
        await until { !draft.running }
        #expect(draft.frozen)
        #expect(commands.sent.first?.createSession.fileIds == ["uploaded-file"])
        #expect(commands.sent.first?.createSession.imageIds.isEmpty == true)
        p.imageId = "renewed-file"
        commands.answer(CommandError(CommandError.unknownOutcome, "unknown"))
        draft.start(commands, cfg, images: attachments) { _, _, _ in }
        await until { !draft.running }
        #expect(commands.sent.count == 1)
        #expect(commands.forwarded.count == 2 && commands.forwarded.last?.retry == true)
    }

    @Test func fileOnlySessionMessagePersistsAndRestoresWithOriginalRequest() async throws {
        let (url, p) = try document()
        defer { try? FileManager.default.removeItem(at: url) }
        let account = Account(server: "http://127.0.0.1:1", root: Data(count: 32), deviceSeed: Data(count: 32), deviceId: "00112233445566778899aabbccddeeff")
        let remote = try Remote(account: account, http: AppModel.http)
        var state = RemoteState()
        state.machines["mac"] = MachineView(id: "mac", online: true, lastSeenMs: 0, meta: .with { $0.capabilities.fileUpload = true })
        state.sessions["session"] = SessionView(id: "session", machineId: "mac")
        await remote.showForTest(state: state)
        let files = VolatileFiles()
        ImageCenter.install(remote: remote, files: files)
        defer { ImageCenter.remove(remote) }
        let center = ImageCenter.of(remote)
        let draft = center.draft(ImageCenter.sessionKey("session"))
        await center.restoredDraft(draft.key)
        draft.append([p])
        let commands = FakeCommands()
        commands.answer(CommandError(CommandError.unknownOutcome, "unknown"))
        let store = SessionStore()
        let taken = center.send(sid: "session", machineId: "mac", store: store, text: "", mode: .queue, commands: commands) { _ in }
        #expect(taken)
        await until { store.outgoing("session").first?.state == .unknown }
        let outgoing = try #require(store.outgoing("session").first)
        #expect(outgoing.fileIds == ["uploaded-file"] && outgoing.imageIds.isEmpty)
        #expect(commands.sent.first?.send.fileIds == ["uploaded-file"])
        let restored = try #require(OutgoingRecords(files: files).load(sid: "session").first)
        #expect(restored.fileIds == ["uploaded-file"] && restored.requestId == outgoing.requestId)
        #expect(restored.images.first?.file == p.file)
        #expect(center.putBack(sid: "session", max: 6, outgoing))
        #expect(draft.documents.first?.imageId == nil)
    }
}
