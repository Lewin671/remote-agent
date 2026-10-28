import Foundation
import RACore
import RAProto
import Testing
@testable import RemoteAgent

@MainActor @Suite struct FileCapabilityUITests {
    private func remote() throws -> Remote {
        try Remote(account: Account(server: "http://127.0.0.1:1", root: Data(count: 32), deviceSeed: Data(count: 32), deviceId: "00112233445566778899aabbccddeeff"), http: AppModel.http)
    }
    private func state(support: Bool) -> RemoteState {
        var state = RemoteState()
        state.machines["m"] = MachineView(id: "m", online: true, lastSeenMs: 0, meta: .with { $0.capabilities.fileUpload = support })
        state.sessions["s"] = SessionView(id: "s", machineId: "m")
        return state
    }
    private func readyFile() -> PendingImage {
        let p = PendingImage(id: "picked-file", uri: nil)
        p.file = PickedFile(remote: .with { $0.fileID = "file-1"; $0.name = "report.csv"; $0.sizeBytes = 42 })
        p.imageId = "file-1"; p.machineId = "m"; p.expiresMs = Int64.max; p.state = .ready
        return p
    }
    private func until(_ condition: @MainActor () -> Bool) async {
        for _ in 0..<200 where !condition() { try? await Task.sleep(for: .milliseconds(5)) }
        #expect(condition())
    }

    @Test func aReadyFileCannotLeaveTheComposerAfterCapabilityDisappears() async throws {
        let remote = try remote(); await remote.showForTest(state: state(support: false))
        ImageCenter.install(remote: remote, files: VolatileFiles()); defer { ImageCenter.remove(remote) }
        let center = ImageCenter.of(remote), draft = center.draft("session:s"), p = readyFile(), store = SessionStore(), commands = FakeCommands()
        await center.restoredDraft(draft.key); draft.append([p])
        var failure: String?
        #expect(center.send(sid: "s", machineId: "m", store: store, text: "whole message", mode: .queue, commands: commands) {
            if case .failure(let error) = $0 { failure = errorText(error) }
        })
        await until { failure != nil || !commands.sent.isEmpty }
        #expect(failure?.contains("更新") == true)
        #expect(commands.sent.isEmpty && store.outgoing("s").isEmpty && draft.images.first === p)
    }

    @Test(arguments: [false, true])
    func restoringAnOutgoingChecksBeforeChangingTheDraftOrRemovingTheRecord(direct: Bool) async throws {
        let remote = try remote(); await remote.showForTest(state: state(support: false))
        let files = VolatileFiles(); ImageCenter.install(remote: remote, files: files); defer { ImageCenter.remove(remote) }
        let center = ImageCenter.of(remote), draft = center.draft("session:s"), store = SessionStore()
        await center.restoredDraft(draft.key); store.setDraft("s", "existing text")
        let p = readyFile(), o = Outgoing(requestId: "original-request", text: "restore this", fileIds: ["file-1"], images: [p])
        o.state = .failed; try OutgoingRecords(files: files).save(sid: "s", o.record, original: false); store.add("s", o)
        let restored: Bool
        if direct { restored = center.putBack(sid: "s", max: 6, o) }
        else { restored = SessionImages.putBack(o, session: "s", state: state(support: false), remote: remote, store: store) }
        #expect(!restored && draft.images.isEmpty && p.imageId == "file-1")
        #expect(store.draft("s") == "existing text" && store.find("s", requestId: o.requestId) === o)
        #expect(try OutgoingRecords(files: files).load(sid: "s").count == 1)
        #expect(center.notices["session:s"]?.contains("更新") == true)
    }

    @Test(arguments: [Outgoing.State.failed, .unknown])
    func resendingAFileRecordCannotDowngradeItToTextOrSettleUnknown(previous: Outgoing.State) async throws {
        let remote = try remote(); await remote.showForTest(state: state(support: false))
        let store = SessionStore(), original = Outgoing(requestId: "original-request", text: "keep files", fileIds: ["file-1"])
        let o = previous == .unknown ? OutgoingRecords.revive(original.record) : original
        o.state = previous; store.add("s", o)
        await store.deliver(remote, sid: "s", o).value
        #expect(o.state == previous && o.error.contains("更新"))
        #expect(o.requestId == "original-request" && o.fileIds == ["file-1"])
    }

    @Test func remoteHistoryRestoreRejectsBeforeAddingPlaceholdersOrText() async throws {
        let remote = try remote(); await remote.showForTest(state: state(support: false))
        ImageCenter.install(remote: remote, files: VolatileFiles()); defer { ImageCenter.remove(remote) }
        let center = ImageCenter.of(remote), store = SessionStore(), draft = center.draft("session:s")
        await center.restoredDraft(draft.key); store.setDraft("s", "existing")
        #expect(!center.putBack(sid: "s", text: "restore", files: [.with { $0.name = "report.csv"; $0.fileID = "file-1" }], store: store))
        #expect(draft.images.isEmpty && store.draft("s") == "existing")
        #expect(center.notices[draft.key]?.contains("更新") == true)
        // Empty attachments still permit restoring a plain text message.
        #expect(center.putBack(sid: "s", text: "plain", files: [], store: store))
        #expect(store.draft("s") == "existing\nplain")
    }

    @Test func aCreateWithFilesIsRejectedBeforePreparingOrClearingItsDraft() async throws {
        let remote = try remote(); await remote.showForTest(state: state(support: false))
        let p = readyFile(), attachments = ImageDraft(key: "new", files: VolatileFiles(), saves: nil)
        attachments.append([p])
        let cfg = NewConfig(machineId: "m", cwd: "/work/example"), draft = NewSessionDraft(nil, settled: true)
        draft.text = "keep all attachments"
        draft.start(remote, cfg, images: attachments) { _, _, _ in Issue.record("unsupported create was sent") }
        await until { !draft.running }
        #expect(draft.error.contains("更新") && !draft.frozen)
        #expect(draft.text == "keep all attachments" && attachments.images.first === p && p.imageId == "file-1")
    }

    @Test func capabilityIsRecheckedAfterSavingTheOutgoingRecord() async throws {
        let remote = try remote(); await remote.showForTest(state: state(support: true))
        ImageCenter.install(remote: remote, files: VolatileFiles()); defer { ImageCenter.remove(remote) }
        let center = ImageCenter.of(remote), draft = center.draft("session:s"), store = SessionStore(), commands = FakeCommands()
        await center.restoredDraft(draft.key); draft.append([readyFile()])
        let gate = CapabilitySaveGate(); defer { gate.open.signal() }
        var failure: String?
        _ = center.send(sid: "s", machineId: "m", store: store, text: "whole message", mode: .queue, commands: commands, keep: { _, _, _ in
            gate.markEntered(); gate.open.wait()
        }) { if case .failure(let error) = $0 { failure = errorText(error) } }
        await until { gate.entered }
        await remote.showForTest(state: state(support: false)); gate.open.signal()
        await until { failure != nil || !commands.sent.isEmpty }
        #expect(failure?.contains("更新") == true && commands.sent.isEmpty)
        #expect(draft.images.count == 1 && store.outgoing("s").isEmpty)
    }
}

private final class CapabilitySaveGate: @unchecked Sendable {
    private let lock = NSLock()
    private var value = false
    let open = DispatchSemaphore(value: 0)
    var entered: Bool { lock.withLock { value } }
    func markEntered() { lock.withLock { value = true } }
}
