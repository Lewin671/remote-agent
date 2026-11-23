import Connect
import Foundation
import RAProto
import Testing
@testable import RACore

@Suite(.timeLimit(.minutes(2))) struct ImageFailureIdentityTests {
    private func classify(_ inputs: [PBImageProblemInput], locale: String = "zh-CN") throws -> PBAttachmentEvalViewModel {
        let client = try CoreClient()
        defer { client.close() }
        _ = try client.dispatch(.with { $0.localeChanged.uiLocale = locale })
        return try client.view(.with {
            $0.attachmentEval = .with { $0.problems = inputs }
        }).attachmentEval
    }

    @Test func structuredInputsNeverEmitLocalKeyAndClassifyConsistentlyAcrossLocales() throws {
        let cases: [(Error, PBImageProblemKind)] = [
            (ImageTooLarge(L10nMessage.imageIosOriginalExceedsMb(count: 20), kind: .originalTooLarge), .originalTooLarge),
            (ImageTooLarge(L10nMessage.imageIosImageExceedsMb(count: 80), kind: .dimensions), .dimensions),
            (PickedFile.unreadable, .sourceMissing),
            (UserError(L10nMessage.imageIosCannotDecodeThisImage(), problemKind: .decode), .decode),
            (UserError(L10nMessage.attachmentIosThisPhoneSPendingImageStorageIsFullSend(), problemKind: .storageFull), .storageFull),
            (URLError(.timedOut), .uploadFailed),
            (URLError(.cannotConnectToHost), .uploadFailed),
            (ConnectError(code: .deadlineExceeded, message: "arbitrary"), .uploadFailed),
            (ConnectError(code: .unavailable, message: nil, exception: URLError(.cannotConnectToHost)), .uploadFailed),
            (CommandError("resource_exhausted", "arbitrary", fromDaemon: true), .storageFull),
            (CommandError("not_found", "arbitrary", fromDaemon: true, outcome: .executed), .uploadFailed),
            (CommandError("generation_conflict", "arbitrary", fromDaemon: true), .uploadFailed),
            (CommandError("unknown_outcome", "arbitrary"), .uploadFailed),
            (CommandError("permission_denied", "arbitrary", fromDaemon: true, outcome: .executed), .processingFailed),
            (CommandError("internal", "decode failed", fromDaemon: true), .decode),
            (NSError(domain: NSCocoaErrorDomain, code: NSFileWriteOutOfSpaceError), .storageFull),
            (NSError(domain: NSCocoaErrorDomain, code: NSFileReadNoSuchFileError), .sourceMissing),
            (NSError(domain: NSPOSIXErrorDomain, code: Int(ENOSPC)), .storageFull),
            // Untyped local UserError prose is never leaked into rawDiagnostic or reverse-mapped.
            (UserError("Connection timed out"), .processingFailed),
            (UserError("original exceeds 20 MB"), .processingFailed),
            (AttachmentFailure.reading(NSError(domain: "NSItemProviderErrorDomain", code: -1)), .decode),
            (AttachmentFailure.reading(URLError(.timedOut)), .uploadFailed),
            (AttachmentFailure.reading(AttachmentFailure(kind: .sourceMissing)), .sourceMissing),
        ]

        let inputs = cases.map { imageProblemInput(for: $0.0) }

        for locale in ["zh-CN", "en"] {
            let vm = try classify(inputs, locale: locale)
            #expect(vm.problems.count == cases.count)
            for (problem, (_, expected)) in zip(vm.problems, cases) {
                #expect(problem.kind == expected)
                #expect(problem.retryable == (expected == .storageFull || expected == .uploadFailed))
                #expect(problem.hasMessage && !problem.message.rendered(locale: locale).isEmpty)
            }
        }
    }

    private struct Refusing: ImageTestChannel {
        let error: any Error
        func begin(transferId: String, format: PBImageFormat, total: Int64, sha256: Data) async throws -> PBImageUploadStatus { throw error }
        func put(transferId: String, offset: Int64, data: Data) async throws -> PBImageUploadStatus { throw error }
        func status(transferId: String) async throws -> PBImageUploadStatus { throw error }
        func complete(transferId: String) async throws -> PBImageUploadStatus { throw error }
    }

    @MainActor @Test func sendingStoresStructuredFailureInputWithoutFormattingProse() async throws {
        for error in [URLError(.timedOut), URLError(.cannotConnectToHost)] {
            let image = PendingImage(id: "fixture", uri: nil)
            image.processed = Processed(bytes: Data([0x89]) + Data("PNG\r\n\u{1a}\n".utf8) + Data(repeating: 0, count: 40), format: .png, width: 1, height: 1)
            let uploads = ImageUploads(prepare: { _, _ in throw error }, channel: { _ in Refusing(error: error) })
            uploads.upload(image, machineId: "fixture-machine")
            let job = try #require(image.job); await job.value
            guard case .failed(let failure) = image.state else { Issue.record("upload did not fail"); return }
            #expect(failure.input.errorCode == "network")
            #expect(failure.detail == nil)
            let classified = try #require(try classify([failure.input]).problems.first)
            #expect(classified.kind == .uploadFailed && classified.retryable)
        }
    }

    @MainActor @Test func previouslyPersistedDraftBytesStillRestoreWithoutMigration() throws {
        let files = MemoryFiles(), folder = "session:fixture"
        let bytes = Data([1, 2, 3])
        let meta = Data("\(PBImageFormat.png.rawValue)\n11\n22\n0\nfixture://picked\nlegacy note".utf8)
        // These are the pre-change on-disk records. No failure state was or is encoded.
        try files.put(folder: folder, name: "order", data: Data("0\nlegacy".utf8))
        try files.put(folder: folder, name: "legacy.meta", data: meta)
        try files.put(folder: folder, name: "legacy.img", data: bytes)
        let draft = ImageDraft(key: folder, files: files, saves: nil)
        try draft.restore()
        let image = try #require(draft.photos.first)
        #expect(image.uri == "fixture://picked" && image.processed?.bytes == bytes)
        #expect(image.processed?.note == "legacy note" && !draft.original && !image.needsProcessing)
        guard case .failed(let failure) = image.state else { Issue.record("missing restored failure"); return }
        #expect(failure.input.explicitKind == .uploadFailed)
        #expect(try files.get(folder: folder, name: "legacy.meta") == meta)
    }
}
