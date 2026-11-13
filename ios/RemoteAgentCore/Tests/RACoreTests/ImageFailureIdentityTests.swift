import Connect
import Foundation
import RAProto
import Testing
@testable import RACore

@Suite struct ImageFailureIdentityTests {
    /// These are the app-owned failures produced while picking, preparing,
    /// restoring, validating, persisting or uploading an attachment.
    private var origins: [(LocalizedMessage, ImageProblem.Kind)] { [
        (L10nMessage.imageIosImageExceedsMb(count: 80), .dimensions),
        (L10nMessage.imageIosOriginalExceedsMb(count: 80), .tooLarge),
        (L10nMessage.imageIosCannotReadThisImage(), .unreadable),
        (L10nMessage.imageIosCannotDecodeImage(), .unreadable),
        (L10nMessage.imageIosCannotDecodeThisImage(), .unreadable),
        (L10nMessage.imageIosCouldNotEncodeThisImage(), .unreadable),
        (L10nMessage.imageIosNotAReadableJpeg(), .unreadable),
        (L10nMessage.imageIosNotAReadablePng(), .unreadable),
        (L10nMessage.imageIosNotAReadableWebp(), .unreadable),
        (L10nMessage.imageIosNotAReadableGif(), .unreadable),
        (L10nMessage.imageIosGifIsTruncated(), .unreadable),
        (L10nMessage.imageIosGifContainsAnUnrecognizedBlock(), .unreadable),
        (L10nMessage.imageIosUnsupportedImageFormat(), .unreadable),
        (L10nMessage.imageIosImageHasNoDimensions(), .dimensions),
        (L10nMessage.attachmentIosThisPhoneSPendingImageStorageIsFullSend(), .storage),
        (L10nMessage.attachmentIosTheComputerSAttachmentStorageIsFullClearSpace(), .storage),
        (L10nMessage.attachmentFileMissing(), .sourceMissing),
        (L10nMessage.attachmentIosCannotReadTheSourceFileRemoveItAndSelect(), .sourceMissing),
        (L10nMessage.imageIosNeedsUploadingToTheComputerAgain(), .offline),
        (L10nMessage.attachmentIosChooseMachine(), .failed),
        (L10nMessage.attachmentIosTheComputerReportedInvalidUploadProgress(), .failed),
        (L10nMessage.imageIosTheComputerDidNotProvideAnImageId(), .failed),
        (L10nMessage.imageIosAttachmentsExceedThisComputerSFileCountOrSize(), .failed),
        (L10nMessage.imageIosThisComputerDoesNotSupportFileUploads(), .failed),
        (L10nMessage.imageIosThisComputerDoesNotSupportTheseFilesCheckTheir(), .failed),
        (L10nMessage.attachmentIosSignedOutOfTheAccount(), .failed),
        (L10nMessage.imageIosSessionRemoved(), .failed),
        (L10nMessage.attachmentIosFileUploadStateWasNotSaved(value: "offline 原图超过 20 MB"), .failed),
        // All localized connection-error mappings in Api/Remote that can
        // reach image commands. The first two English strings were the regression.
        (L10nMessage.errorsConnectionTimedOut(), .offline),
        (L10nMessage.connectionNoServer(), .offline),
        (L10nMessage.errorsIosCannotFindTheServerCheckYourNetwork(), .offline),
        (L10nMessage.errorsIosSecureConnectionFailed(value: "raw OS detail"), .offline),
        (L10nMessage.connectionIosServerError(reason: "raw relay detail"), .offline),
        (L10nMessage.connectionNoResponse(), .offline),
        (L10nMessage.connectionIosNoResponse(), .offline),
        (L10nMessage.errorsConnectionTimedOutReconnecting(), .offline),
        (L10nMessage.errorsConnectionDroppedMidway(), .offline),
        (L10nMessage.errorsIosNotConnectedToTheServer(), .offline),
        (L10nMessage.errorsIosNotConnectedToTheServerConnectToMakeChanges(), .offline),
        (L10nMessage.errorsTheComputerIsOfflineTheCommandWasNotSent(), .offline),
        (L10nMessage.errorsIosNetworkUnavailableTheCommandWasNotSent(), .offline),
        (L10nMessage.errorsIosThisCommandExpiredAndWasNotSent(), .offline),
        (L10nMessage.errorsIosUnsureWhetherTheComputerExecutedThis(value: "raw cause"), .offline),
        (L10nMessage.errorsIosLoginExpired(value: "raw login cause"), .failed),
        (L10nMessage.errorsTheServerDoesNotRecognizeThisDeviceItMay(), .failed),
        (L10nMessage.errorsIosRemoteControlExpiredSavingChangesSessionControlAndNew(), .failed)
    ] }

    private func caption(_ kind: ImageProblem.Kind, locale: String) -> String {
        switch kind {
        case .tooLarge: L10n.imageImageExceeds20MbTurnOffOriginalAndSend(locale: locale)
        case .dimensions: L10n.imageImageDimensionsAreTooLargeResizeItAndRetry(locale: locale)
        case .sourceMissing: L10n.imageThisImageHasNoLocalCopyRemoveItAnd(locale: locale)
        case .unreadable: L10n.imageCannotReadThisImageTryJpegOrPngInstead(locale: locale)
        case .storage: L10n.imageTheComputerSAttachmentStorageIsFullClearSpace(locale: locale)
        case .offline: L10n.imageTheImageCouldNotReachTheComputerRetryWhen(locale: locale)
        case .failed: L10n.imageThisImageCouldNotBeProcessed(locale: locale)
        }
    }

    @Test func allLocalOriginsHaveTheSameClassBeforeAndAfterRenderingInEitherLanguage() {
        for (message, expected) in origins {
            // Capture outside the locale override too: a stored failure cannot freeze a caption.
            let failures = [AttachmentFailure(catching: UserError(message)),
                            AttachmentFailure(catching: CommandError("internal", message)),
                            AttachmentFailure(catching: UploadError(code: "internal", message: message))]
            for locale in ["zh-CN", "en", "zh-CN"] {
                MessageLocale.$overrideForTesting.withValue(locale) {
                    _ = message.resolve() // Rendering cannot alter or supply classification.
                    for failure in failures {
                        #expect(failure.kind == expected, "\(message.resourceKey ?? "raw")")
                        #expect(failure.problem.retryable == [.offline, .storage].contains(expected))
                        #expect(failure.problem.message == caption(expected, locale: locale))
                        #expect(failure.detail == nil)
                        #expect(imageFailureLine([failure.problem], locale: locale) == caption(expected, locale: locale))
                    }
                }
            }
        }
        for size in [1, 20, 80, 1000] {
            #expect(imageProblem(ImageTooLarge(L10nMessage.imageIosOriginalExceedsMb(count: size))).kind == .tooLarge)
            #expect(imageProblem(ImageTooLarge(L10nMessage.imageIosImageExceedsMb(count: size))).kind == .dimensions)
        }
    }

    @Test func transportCodesAreAuthoritativeAndLocalDisplayTextIsNeverReverseMapped() {
        let cases: [(Error, ImageProblem.Kind)] = [
            (URLError(.timedOut), .offline), (URLError(.cannotConnectToHost), .offline),
            (URLError(.cannotFindHost), .offline), (URLError(.notConnectedToInternet), .offline),
            (URLError(.secureConnectionFailed), .offline),
            (ConnectError(code: .deadlineExceeded, message: "arbitrary"), .offline),
            (ConnectError(code: .unavailable, message: nil, exception: URLError(.cannotConnectToHost)), .offline),
            (CommandError("resource_exhausted", "arbitrary", fromDaemon: true), .storage),
            (CommandError("not_found", "arbitrary", fromDaemon: true,outcome:.executed), .offline),
            (CommandError("unknown_outcome", "arbitrary"), .offline),
            (CommandError("permission_denied", "arbitrary", fromDaemon: true,outcome:.executed), .failed),
            (CommandError("internal", "decode failed", fromDaemon: true), .unreadable),
            (NSError(domain: NSCocoaErrorDomain, code: NSFileWriteOutOfSpaceError), .storage),
            (UserError("Connection timed out"), .failed),
            (UserError("Cannot connect to the server"), .failed),
            (UserError("original exceeds 20 MB"), .failed)
        ]
        for locale in ["zh-CN", "en"] {
            MessageLocale.$overrideForTesting.withValue(locale) {
                for (error, expected) in cases {
                    let failure = AttachmentFailure(catching: error)
                    #expect(failure.kind == expected)
                    #expect(failure.problem.retryable == [.offline, .storage].contains(expected))
                    #expect(failure.problem.message == caption(expected, locale: locale))
                }
            }
        }
        #expect(rawImageProblem("codec: pixels exceed canvas").kind == .dimensions)
        #expect(AttachmentFailure.reading(NSError(domain: "NSItemProviderErrorDomain", code: -1)).kind == .unreadable)
        #expect(AttachmentFailure.reading(URLError(.timedOut)).kind == .offline)
        #expect(AttachmentFailure.reading(AttachmentFailure(kind: .sourceMissing)).kind == .sourceMissing)
    }

    private struct Refusing: ImageTestChannel {
        let error: any Error
        func begin(transferId: String, format: PBImageFormat, total: Int64, sha256: Data) async throws -> PBImageUploadStatus { throw error }
        func put(transferId: String, offset: Int64, data: Data) async throws -> PBImageUploadStatus { throw error }
        func status(transferId: String) async throws -> PBImageUploadStatus { throw error }
        func complete(transferId: String) async throws -> PBImageUploadStatus { throw error }
    }

    @MainActor @Test func sendingStoresKindsWithoutCallingTheErrorFormatter() async throws {
        for locale in ["zh-CN", "en"] {
            for (error, expected) in [(URLError(.timedOut), ImageProblem.Kind.offline), (URLError(.cannotConnectToHost), .offline)] {
                try await MessageLocale.$overrideForTesting.withValue(locale) {
                    let image = PendingImage(id: "fixture", uri: nil)
                    image.processed = Processed(bytes: Data([0x89]) + Data("PNG\r\n\u{1a}\n".utf8) + Data(repeating: 0, count: 40), format: .png, width: 1, height: 1)
                    let uploads = ImageUploads(prepare: { _, _ in throw error }, channel: { _ in Refusing(error: error) })
                    uploads.upload(image, machineId: "fixture-machine")
                    let job = try #require(image.job); await job.value
                    guard case .failed(let failure) = image.state else { Issue.record("upload did not fail"); return }
                    #expect(failure.kind == expected && failure.problem.retryable)
                    #expect(failure.problem.message == caption(expected, locale: locale))
                    #expect(failure.detail == nil)
                }
            }
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
        #expect(failure.kind == .offline && failure.problem.retryable)
        for locale in ["zh-CN", "en"] {
            MessageLocale.$overrideForTesting.withValue(locale) {
                #expect(failure.problem.message == caption(.offline, locale: locale))
            }
        }
        #expect(try files.get(folder: folder, name: "legacy.meta") == meta)
    }
}
