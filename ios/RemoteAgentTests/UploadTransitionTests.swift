import Foundation
import RACore
import RAProto
import Testing
@testable import RemoteAgent

private actor TransitionAborts {
    var ids: [String] = []
    func add(_ id: String) { ids.append(id) }
}
private struct TransitionChannel: UploadChannel {
    let aborts: TransitionAborts
    func begin(transferId: String, format: PBImageFormat, total: Int64, sha256: Data) async throws -> PBImageUploadStatus { throw UserError("unexpected upload") }
    func beginFile(transferId: String, name: String, total: Int64, sha256: Data) async throws -> PBImageUploadStatus { throw UserError("unexpected upload") }
    func put(transferId: String, offset: Int64, data: Data) async throws -> PBImageUploadStatus { throw UserError("unexpected upload") }
    func status(transferId: String) async throws -> PBImageUploadStatus { throw UserError("unexpected upload") }
    func complete(transferId: String) async throws -> PBImageUploadStatus { throw UserError("unexpected upload") }
    func abort(transferId: String) async throws { await aborts.add(transferId) }
}

@MainActor @Suite struct UploadTransitionTests {
    private func ready() throws -> (URL, PendingImage) {
        let url = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString + ".txt")
        try Data("fixture".utf8).write(to: url)
        let p = PendingImage(id: newRequestId(), uri: nil)
        p.file = try PickedFile(url: url)
        p.file?.machineId = "a"
        p.file?.transfers["a"] = FileTransfer(transferId: p.transferId, fileId: "first-file", expiresMs: Int64.max)
        p.machineId = "a"; p.imageId = "first-file"; p.expiresMs = Int64.max; p.state = .ready
        return (url, p)
    }
    private func remote() throws -> Remote {
        try Remote(account: Account(server: "http://127.0.0.1:1", root: Data(count: 32), deviceSeed: Data(count: 32), deviceId: "00112233445566778899aabbccddeeff"), http: AppModel.http)
    }
    private func machine(_ id: String) -> MachineView {
        MachineView(id: id, online: true, lastSeenMs: 0, meta: .with { $0.capabilities.fileUpload = true })
    }
    private func until(_ condition: @MainActor () -> Bool) async {
        for _ in 0..<200 where !condition() { try? await Task.sleep(for: .milliseconds(5)) }
        #expect(condition())
    }

    @Test func offlineAToBToANeverRestoresOldReadiness() async throws {
        let (url, p) = try ready(); defer { try? FileManager.default.removeItem(at: url) }
        let remote = try remote()
        ImageCenter.install(remote: remote, files: VolatileFiles()); defer { ImageCenter.remove(remote) }
        let center = ImageCenter.of(remote), key = "session:transition"
        let draft = center.draft(key); await center.restoredDraft(key); draft.append([p])
        center.aim(key, at: "a", blocked: false, machine: machine("a"))
        #expect(p.readyFor("a"))
        let token = p.generation
        center.aim(key, at: "b", blocked: true, machine: machine("b"))
        #expect(p.generation > token && p.imageId == nil && p.machineId == nil && p.expiresMs == 0)
        center.aim(key, at: "a", blocked: true, machine: machine("a"))
        #expect(!draft.ready(machineId: "a") && draft.fileIds(machineId: "a").isEmpty)
        #expect(p.imageId == nil && p.machineId == nil && p.expiresMs == 0)
    }

    @Test func removalIsVisibleBeforeTheCancellationHandlerRuns() async throws {
        let (url, p) = try ready(); defer { try? FileManager.default.removeItem(at: url) }
        let remote = try remote()
        ImageCenter.install(remote: remote, files: VolatileFiles()); defer { ImageCenter.remove(remote) }
        let center = ImageCenter.of(remote), key = "session:remove"
        let draft = center.draft(key); await center.restoredDraft(key); draft.append([p])
        var installed = false, observed = false
        let job = Task<Void, Never> {
            await withTaskCancellationHandler {
                installed = true
                try? await Task.sleep(for: .seconds(3600))
            } onCancel: {
                MainActor.assumeIsolated {
                    #expect(draft.images.isEmpty)
                    #expect(p.imageId == nil && p.machineId == nil && p.expiresMs == 0)
                    observed = true
                }
            }
        }
        p.job = job
        await until { installed }
        center.remove(key, p)
        await job.value
        #expect(observed)
    }

    @Test(arguments: [true, false]) func aCreateOwnsItsSnapshotBeforeTheNextUIAction(outcomeUnknown: Bool) async throws {
        let (url, p) = try ready(); defer { try? FileManager.default.removeItem(at: url) }
        let aborts = TransitionAborts(), commands = FakeCommands()
        commands.answer(outcomeUnknown ? CommandError(CommandError.unknownOutcome, "unknown") : UserError("refused"))
        let attachments = ImageDraft(key: "new", files: VolatileFiles(), saves: nil); attachments.append([p])
        let cfg = NewConfig(machineId: "a", cwd: "/work/example"), draft = NewSessionDraft(nil, settled: true)
        draft.start(commands, cfg, images: attachments) { _, _, _ in }
        if outcomeUnknown {
            // No actor turn has elapsed since pressing Start. The original request
            // must already own this reservation when the next action supersedes it.
            p.invalidate(retiring: true)
        }
        await until { !draft.running }
        let uploads = ImageUploads(prepare: { _, _ in throw UserError("unexpected preparation") }, channel: { _ in TransitionChannel(aborts: aborts) })
        await uploads.discard(p).value
        #expect(commands.sent.first?.createSession.fileIds == ["first-file"])
        #expect(draft.frozen == outcomeUnknown)
        if outcomeUnknown { #expect(await aborts.ids.isEmpty) }
        else { #expect(await aborts.ids.count == 1) }
    }
}
