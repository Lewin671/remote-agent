import Foundation
import RACore
import RAProto
import Testing
@testable import RemoteAgent

@MainActor @Suite struct FileCapabilityUITests {
    private func remote() throws -> Remote {
        try Remote(account: Account(server: "http://127.0.0.1:1", deviceId: "00112233445566778899aabbccddeeff"), http: AppModel.http)
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
    @Test func aReadyFileCannotLeaveTheComposerAfterCapabilityDisappears() async throws {
        let remote = try remote(); await remote.showForTest(state: state(support: false))
        ImageCenter.install(remote: remote, files: VolatileFiles()); defer { ImageCenter.remove(remote) }
        let center = ImageCenter.of(remote), draft = center.draft("session:s"), p = readyFile()
        await center.restoredDraft(draft.key); draft.append([p])
        var failure: String?
        var submitted: [Outgoing] = []
        #expect(center.send(sid: "s", machineId: "m", text: "whole message", mode: .queue, submit: { submitted.append($0) }) {
            if case .failure(let error) = $0 { failure = errorText(error) }
        })
        await eventually { failure != nil || !submitted.isEmpty }
        #expect(failure?.contains("更新") == true)
        #expect(submitted.isEmpty && draft.images.first === p)
    }

    @Test(arguments: [false, true])
    func restoringAnOutgoingChecksBeforeChangingTheDraftOrRemovingTheRecord(direct: Bool) async throws {
        let remote = try remote(); await remote.showForTest(state: state(support: false))
        let files = VolatileFiles(); ImageCenter.install(remote: remote, files: files); defer { ImageCenter.remove(remote) }
        let center = ImageCenter.of(remote), draft = center.draft("session:s"), store = SessionStore()
        await center.restoredDraft(draft.key); store.setDraft("s", "existing text")
        let p = readyFile(), o = Outgoing(requestId: "original-request", text: "restore this", fileIds: ["file-1"], images: [p])
        o.state = .failed; try OutgoingRecords(files: files).save(sid: "s", o.record, original: false)
        center.restoreOutgoing("s"); await center.restoredOutgoing("s")
        #expect(!SessionImages.canPutBack(
            requestId: o.requestId,
            files: [.with { $0.name = "report.csv" }],
            session: "s", state: state(support: false), remote: remote))
        let restored: Bool
        if direct { restored = center.putBack(sid: "s", max: 6, o) }
        else { restored = SessionImages.putBack(o, session: "s", state: state(support: false), remote: remote, store: store) }
        #expect(!restored && draft.images.isEmpty && p.imageId == "file-1")
        #expect(store.draft("s") == "existing text" && center.restoredOutgoing["s"]?.first?.requestId == o.requestId)
        #expect(try OutgoingRecords(files: files).load(sid: "s").count == 1)
        #expect(center.notices["session:s"]?.text.contains("更新") == true)
    }

    @Test func remoteHistoryRestoreRejectsBeforeAddingPlaceholdersOrText() async throws {
        let remote = try remote(); await remote.showForTest(state: state(support: false))
        ImageCenter.install(remote: remote, files: VolatileFiles()); defer { ImageCenter.remove(remote) }
        let center = ImageCenter.of(remote), store = SessionStore(), draft = center.draft("session:s")
        await center.restoredDraft(draft.key); store.setDraft("s", "existing")
        #expect(!center.putBack(sid: "s", text: "restore", files: [.with { $0.name = "report.csv"; $0.fileID = "file-1" }], store: store))
        #expect(draft.images.isEmpty && store.draft("s") == "existing")
        #expect(center.notices[draft.key]?.text.contains("更新") == true)
        // Empty attachments still permit restoring a plain text message.
        #expect(center.putBack(sid: "s", text: "plain", files: [], store: store))
        #expect(store.draft("s") == "existing\nplain")
    }

    @Test func capabilityIsRecheckedAfterSavingTheOutgoingRecord() async throws {
        let remote = try remote(); await remote.showForTest(state: state(support: true))
        ImageCenter.install(remote: remote, files: VolatileFiles()); defer { ImageCenter.remove(remote) }
        let center = ImageCenter.of(remote), draft = center.draft("session:s")
        await center.restoredDraft(draft.key); draft.append([readyFile()])
        let gate = CapabilitySaveGate(); defer { gate.open.signal() }
        var failure: String?
        var submitted: [Outgoing] = []
        _ = center.send(sid: "s", machineId: "m", text: "whole message", mode: .queue, keep: { _, _, _ in
            gate.markEntered(); gate.open.wait()
        }, submit: { submitted.append($0) }) { if case .failure(let error) = $0 { failure = errorText(error) } }
        await eventually { gate.entered }
        await remote.showForTest(state: state(support: false)); gate.open.signal()
        await eventually { failure != nil || !submitted.isEmpty }
        #expect(failure?.contains("更新") == true && submitted.isEmpty)
        #expect(draft.images.count == 1 && center.heldOutgoing("s", requestId: "original") == nil)
    }
}

private final class CapabilitySaveGate: @unchecked Sendable {
    private let lock = NSLock()
    private var value = false
    let open = DispatchSemaphore(value: 0)
    var entered: Bool { lock.withLock { value } }
    func markEntered() { lock.withLock { value = true } }
}
