import Foundation
import CoreTransferable
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
    /// A video copied out of the library, in a draft of a fresh account.
    private func video() throws -> (URL, PendingImage) {
        let source = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString + ".mov")
        try Data(repeating: 7, count: 64).write(to: source)
        defer { try? FileManager.default.removeItem(at: source) }
        let copy = try LibraryVideos.copy(source)
        let p = PendingImage(id: newRequestId(), uri: nil)
        p.file = try PickedFile(url: copy)
        return (copy, p)
    }

    private func center() throws -> (Remote, ImageCenter) {
        let account = Account(server: "http://127.0.0.1:1", deviceId: "00112233445566778899aabbccddeeff")
        let remote = try Remote(account: account, http: AppModel.http)
        ImageCenter.install(remote: remote, files: VolatileFiles(), live: false)
        return (remote, ImageCenter.of(remote))
    }

    @Test func aLibraryProviderKeepsOriginalVideoBytesBeyondThePicker() async throws {
        let source = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString + ".mov")
        let bytes = Data(repeating: 7, count: 128)
        try bytes.write(to: source)
        defer { try? FileManager.default.removeItem(at: source) }
        let provider = try #require(NSItemProvider(contentsOf: source))
        // A provider supplies the name separately from its file representation.
        provider.suggestedName = source.lastPathComponent
        let copied: LibraryVideo = try await withCheckedThrowingContinuation { done in
            _ = provider.loadTransferable(type: LibraryVideo.self) { done.resume(with: $0) }
        }
        defer { LibraryVideos.discard(copied.url) }
        try FileManager.default.removeItem(at: source)
        #expect(copied.url.lastPathComponent == source.lastPathComponent)
        #expect(try Data(contentsOf: copied.url) == bytes)
    }

    @Test func aVideosCopyGoesWithItsFolderAndNothingElseIsTouched() throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString, isDirectory: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let source = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString + ".mov")
        try Data("v".utf8).write(to: source)
        defer { try? FileManager.default.removeItem(at: source) }
        let a = try LibraryVideos.copy(source, root: root)
        let b = try LibraryVideos.copy(source, root: root)
        #expect(a.lastPathComponent == source.lastPathComponent && a != b)
        LibraryVideos.discard(a, root: root)
        #expect(!FileManager.default.fileExists(atPath: a.deletingLastPathComponent().path))
        #expect(FileManager.default.fileExists(atPath: b.path))
        // A document picked from elsewhere is the user's own: never deleted.
        LibraryVideos.discard(source, root: root)
        #expect(FileManager.default.fileExists(atPath: source.path))
        LibraryVideos.discardAll(root: root)
        #expect(!FileManager.default.fileExists(atPath: root.path))
    }

    @Test func aVideoTakenOutOfTheDraftOrCarriedByAStartedSessionIsDeleted() async throws {
        let (remote, center) = try center()
        defer { ImageCenter.remove(remote) }
        let key = "session:video"
        await center.restoredDraft(key)
        let (removed, r) = try video()
        let (used, u) = try video()
        center.draft(key).append([r, u])
        center.remove(key, r)
        #expect(!FileManager.default.fileExists(atPath: removed.path))
        #expect(FileManager.default.fileExists(atPath: used.path))
        center.used(key, [u])
        #expect(!FileManager.default.fileExists(atPath: used.path))
    }

    @Test func aVideoThatCannotBeAddedIsDeletedAtOnce() async throws {
        let (remote, center) = try center()
        defer { ImageCenter.remove(remote) }
        let (copy, _) = try video()
        let offline = AttachmentCapabilities(supportsFiles: true, fileUploadBlocked: L10n.attachmentComputerOfflineFiles(name: "Mac"))
        center.addFiles("session:video", [copy], caps: offline)
        #expect(!FileManager.default.fileExists(atPath: copy.path))
    }

    #if DEBUG
    @Test func filePickerRoutesImageAsFileUnchangedViaCoreEval() async throws {
        let account = Account(server: "http://127.0.0.1:1", deviceId: "00112233445566778899aabbccddeeff")
        let remote = try Remote(account: account, http: AppModel.http)
        var state = RemoteState()
        state.machines["mac"] = MachineView(id: "mac", online: true, lastSeenMs: 0, meta: .with {
            $0.hostname = "MacBook"
            $0.capabilities.imageUpload = true
            $0.capabilities.fileUpload = true
            $0.capabilities.maxFilesPerMessage = 6
            $0.capabilities.maxFileUploadBytes = 100 * 1024 * 1024
        })
        state.sessions["s-file"] = SessionView(id: "s-file", machineId: "mac")
        await remote.showForTest(state: state)
        let host = try CoreHost.live(
            remote: remote, account: account, defaults: UserDefaults(suiteName: "ra-files-\(UUID())")!,
            screens: CoreScreens(), demoMode: true, onNavigate: { _ in }
        )
        defer { host.close() }
        await host.seedDemo(state: state)
        ImageCenter.install(remote: remote, coreHost: host, files: VolatileFiles(), live: false)
        defer { ImageCenter.remove(remote) }
        let key = ImageCenter.sessionKey("s-file")
        let center = ImageCenter.of(remote)

        let pngURL = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString + ".png")
        try Data([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A]).write(to: pngURL)
        defer { try? FileManager.default.removeItem(at: pngURL) }

        center.addFiles(key, [pngURL], machineId: "mac", caps: AttachmentCapabilities(supportsFiles: true))
        let draft = center.draft(key)
        await eventually("image picked from Files added as document unchanged") {
            draft.documents.count == 1
        }
        #expect(draft.photos.isEmpty)
        #expect(draft.documents[0].file?.displayName == pngURL.lastPathComponent)
        #expect(draft.documents[0].file?.formattedSize == "8 B")
    }
    #endif

    @Test func losingFileSupportCancelsEvenAnAlreadyReadyFilesRefresh() async throws {
        let (url, p) = try document()
        defer { try? FileManager.default.removeItem(at: url) }
        let account = Account(server: "http://127.0.0.1:1", deviceId: "00112233445566778899aabbccddeeff")
        let remote = try Remote(account: account, http: AppModel.http)
        ImageCenter.install(remote: remote, files: VolatileFiles())
        defer { ImageCenter.remove(remote) }
        let center = ImageCenter.of(remote)
        let draft = center.draft("session:test")
        await center.restoredDraft(draft.key)
        draft.append([p])
        let timer = Task<Void, Never> { try? await Task.sleep(for: .seconds(3600)) }
        p.refreshJob = timer
        let unsupported = AttachmentCapabilities(supportsFiles: false)
        center.aim(draft.key, at: "mac", blocked: true, caps: unsupported)
        #expect(timer.isCancelled)
        #expect(!p.readyFor("mac"))
    }

    @Test func fileOnlySessionMessagePersistsAndRestoresWithOriginalRequest() async throws {
        let (url, p) = try document()
        defer { try? FileManager.default.removeItem(at: url) }
        let account = Account(server: "http://127.0.0.1:1", deviceId: "00112233445566778899aabbccddeeff")
        let remote = try Remote(account: account, http: AppModel.http)
        var state = RemoteState()
        state.machines["mac"] = MachineView(id: "mac", online: true, lastSeenMs: 0, meta: .with {
            $0.hostname = "MacBook"
            $0.capabilities.imageUpload = true
            $0.capabilities.fileUpload = true
        })
        state.sessions["session"] = SessionView(id: "session", machineId: "mac")
        await remote.showForTest(state: state)
        let host = try CoreHost.live(
            remote: remote, account: account, defaults: UserDefaults(suiteName: "ra-sf-\(UUID())")!,
            screens: CoreScreens(), demoMode: true, onNavigate: { _ in }
        )
        defer { host.close() }
        await host.seedDemo(state: state)
        let files = VolatileFiles()
        ImageCenter.install(remote: remote, coreHost: host, files: files)
        defer { ImageCenter.remove(remote) }
        let center = ImageCenter.of(remote)
        let draft = center.draft(ImageCenter.sessionKey("session"))
        await center.restoredDraft(draft.key)
        draft.append([p])
        var submitted: [Outgoing] = []
        let taken = center.send(sid: "session", machineId: "mac", text: "", mode: .queue, submit: { submitted.append($0) }) { _ in }
        #expect(taken)
        await eventually { !submitted.isEmpty }
        let outgoing = try #require(center.heldOutgoing("session", requestId: submitted[0].requestId))
        outgoing.state = .unknown
        #expect(outgoing.fileIds == ["uploaded-file"] && outgoing.imageIds.isEmpty)
        let restored = try #require(OutgoingRecords(files: files).load(sid: "session").first)
        #expect(restored.fileIds == ["uploaded-file"] && restored.requestId == outgoing.requestId)
        #expect(restored.images.first?.file == p.file)
        #expect(await center.putBack(sid: "session", outgoing))
        #expect(draft.documents.first?.imageId == nil)
    }
}
