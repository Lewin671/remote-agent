import Foundation
import RACore
import RAProto
import Testing
@testable import RemoteAgent

@MainActor @Suite struct FileCapabilityUITests {
    private func remote() throws -> Remote {
        try Remote(account: Account(server: "http://127.0.0.1:1", deviceId: "00112233445566778899aabbccddeeff"), http: AppModel.http)
    }
    private func state(support: Bool, metaRev: UInt64 = 1) -> RemoteState {
        var state = RemoteState()
        state.machines["m"] = MachineView(id: "m", online: true, lastSeenMs: 0, meta: .with {
            $0.hostname = "MacBook"
            $0.capabilities.imageUpload = true
            $0.capabilities.fileUpload = support
        }, metaRev: metaRev)
        state.sessions["s"] = SessionView(id: "s", machineId: "m")
        return state
    }
    private func host(for remote: Remote, state: RemoteState) async throws -> CoreHost {
        await remote.showForTest(state: state)
        let host = try CoreHost.live(
            remote: remote, account: remote.account, defaults: UserDefaults(suiteName: "ra-fc-\(UUID())")!,
            screens: CoreScreens(), demoMode: true, onNavigate: { _ in }
        )
        await host.seedDemo(state: state)
        return host
    }
    private func readyFile() -> PendingImage {
        let p = PendingImage(id: "picked-file", uri: nil)
        p.file = PickedFile(remote: .with { $0.fileID = "file-1"; $0.name = "report.csv"; $0.sizeBytes = 42 })
        p.imageId = "file-1"; p.machineId = "m"; p.expiresMs = Int64.max; p.state = .ready
        return p
    }
    @Test func aReadyFileCannotLeaveTheComposerAfterCapabilityDisappears() async throws {
        let remote = try remote(); let coreHost = try await host(for: remote, state: state(support: false)); defer { coreHost.close() }
        ImageCenter.install(remote: remote, coreHost: coreHost, files: VolatileFiles()); defer { ImageCenter.remove(remote) }
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
        let remote = try remote(); let coreHost = try await host(for: remote, state: state(support: false)); defer { coreHost.close() }
        let files = VolatileFiles(); ImageCenter.install(remote: remote, coreHost: coreHost, files: files); defer { ImageCenter.remove(remote) }
        let center = ImageCenter.of(remote), draft = center.draft("session:s"), store = SessionStore()
        await center.restoredDraft(draft.key); store.setDraft("s", "existing text")
        let p = readyFile(), o = Outgoing(requestId: "original-request", text: "restore this", fileIds: ["file-1"], images: [p])
        o.state = .failed; try OutgoingRecords(files: files).save(sid: "s", o.record, original: false)
        center.restoreOutgoing("s"); await center.restoredOutgoing("s")
        #expect(await !SessionImages.canPutBack(
            requestId: o.requestId,
            files: [.with { $0.name = "report.csv" }],
            session: "s", remote: remote))
        let restored: Bool
        if direct { restored = await center.putBack(sid: "s", o) }
        else { restored = await SessionImages.putBack(o, session: "s", remote: remote, store: store) }
        #expect(!restored && draft.images.isEmpty && p.imageId == "file-1")
        #expect(store.draft("s") == "existing text" && center.restoredOutgoing["s"]?.first?.requestId == o.requestId)
        #expect(try OutgoingRecords(files: files).load(sid: "s").count == 1)
        #expect(center.notices["session:s"]?.text.contains("更新") == true)
    }

    @Test func remoteHistoryRestoreRejectsBeforeAddingPlaceholdersOrText() async throws {
        let remote = try remote(); let coreHost = try await host(for: remote, state: state(support: false)); defer { coreHost.close() }
        ImageCenter.install(remote: remote, coreHost: coreHost, files: VolatileFiles()); defer { ImageCenter.remove(remote) }
        let center = ImageCenter.of(remote), store = SessionStore(), draft = center.draft("session:s")
        await center.restoredDraft(draft.key); store.setDraft("s", "existing")
        #expect(await !center.putBack(sid: "s", text: "restore", files: [.with { $0.name = "report.csv"; $0.fileID = "file-1" }], store: store))
        #expect(draft.images.isEmpty && store.draft("s") == "existing")
        #expect(center.notices[draft.key]?.text.contains("更新") == true)
        // Empty attachments still permit restoring a plain text message.
        #expect(await center.putBack(sid: "s", text: "plain", files: [], store: store))
        #expect(store.draft("s") == "existing\nplain")
    }

    @Test func capabilityIsRecheckedAfterSavingTheOutgoingRecord() async throws {
        let remote = try remote()
        let allowedHost = try await host(for: remote, state: state(support: true, metaRev: 1)); defer { allowedHost.close() }
        let blockedHost = try await host(for: remote, state: state(support: false, metaRev: 2)); defer { blockedHost.close() }
        let switcher = SwitchableAttachmentEvaluator(allowedHost)
        ImageCenter.install(remote: remote, coreHost: switcher, files: VolatileFiles()); defer { ImageCenter.remove(remote) }
        let center = ImageCenter.of(remote), draft = center.draft("session:s")
        await center.restoredDraft(draft.key); draft.append([readyFile()])
        let gate = CapabilitySaveGate(); defer { gate.open.signal() }
        var failure: String?
        var submitted: [Outgoing] = []
        _ = center.send(sid: "s", machineId: "m", text: "whole message", mode: .queue, keep: { _, _, _ in
            gate.markEntered(); gate.open.wait()
        }, submit: { submitted.append($0) }) { if case .failure(let error) = $0 { failure = errorText(error) } }
        await eventually { gate.entered }
        await switcher.set(blockedHost)
        gate.open.signal()
        await eventually { failure != nil || !submitted.isEmpty }
        #expect(failure?.contains("更新") == true && submitted.isEmpty)
        #expect(draft.images.count == 1 && center.heldOutgoing("s", requestId: "original") == nil)
    }
}

private actor SwitchableAttachmentEvaluator: AttachmentEvaluating {
    private var current: any AttachmentEvaluating
    init(_ initial: any AttachmentEvaluating) { current = initial }
    func set(_ next: any AttachmentEvaluating) { current = next }
    func evaluateAttachments(_ request: PBAttachmentEvalScreenRequest) async -> PBAttachmentEvalViewModel {
        await current.evaluateAttachments(request)
    }
}

private final class CapabilitySaveGate: @unchecked Sendable {
    private let lock = NSLock()
    private var value = false
    let open = DispatchSemaphore(value: 0)
    var entered: Bool { lock.withLock { value } }
    func markEntered() { lock.withLock { value = true } }
}
