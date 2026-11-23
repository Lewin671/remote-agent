import Connect
import Foundation
import RAProto

/// A draft failure stores structured error input for Core (`AttachmentEval`) rather than
/// pre-formatted prose. Only file chips carry a separate diagnostic detail.
public struct AttachmentFailure: Error, LocalizedError, Equatable, Sendable {
    public let input: PBImageProblemInput
    public let detail: LocalizedMessage?

    public init(kind: PBImageProblemKind, detail: LocalizedMessage? = nil) {
        self.input = .with { $0.explicitKind = kind }
        self.detail = detail
    }

    public init(input: PBImageProblemInput, detail: LocalizedMessage? = nil) {
        self.input = input
        self.detail = detail
    }

    public init(catching error: Error, isFile: Bool = false) {
        if let failure = error as? AttachmentFailure { self = failure; return }
        self.input = imageProblemInput(for: error)
        self.detail = isFile ? fileUploadMessage(error) : nil
    }

    /// Provider/codec reads have a known origin even when their vendor error has no structured code.
    public static func reading(_ error: Error) -> AttachmentFailure {
        if let failure = error as? AttachmentFailure { return failure }
        return AttachmentFailure(input: imageProblemInput(for: error, isReadingSource: true))
    }

    public var errorDescription: String? { detail?.resolve() }
    public var message: String { detail?.resolve() ?? "" }
}

/// Extract structured error signals for Core's `ClassifyImageProblem` without ever emitting
/// transitional `local_key` strings or leaking translated UI prose into `raw_diagnostic`.
public func imageProblemInput(for error: Error, isReadingSource: Bool = false) -> PBImageProblemInput {
    if let failure = error as? AttachmentFailure {
        var input = failure.input
        if isReadingSource, input.explicitKind == .unspecified, input.errorCode.isEmpty, input.rawDiagnostic.isEmpty {
            input.isReadingSource = true
        }
        return input
    }
    var input = PBImageProblemInput()
    input.isReadingSource = isReadingSource

    if let e = error as? ImageTooLarge {
        input.explicitKind = e.kind
        return input
    }
    if let e = error as? UserError {
        if let kind = e.problemKind {
            input.explicitKind = kind
        }
        return input
    }
    if let failure = error as? any UploadFailure {
        input.errorCode = failure.code
        if let command = error as? CommandError, command.fromDaemon, let raw = command.localized.rawText {
            input.rawDiagnostic = raw
        }
        return input
    }
    if error is CryptoError || error is OpenError {
        // App-owned errors without structured image codes stay generic (never bridge to NSError).
        return input
    }
    if error is TimedOut || networkFailure(error) != nil || error is URLError {
        input.errorCode = "network"
        return input
    }
    if let failure = error as? ConnectError {
        input.errorCode = failure.code.name
        if let raw = failure.message, !raw.isEmpty {
            input.rawDiagnostic = raw
        }
        return input
    }

    let ns = error as NSError
    if ns.domain == NSURLErrorDomain {
        input.errorCode = "url_error"
        return input
    }
    if ns.domain == NSCocoaErrorDomain {
        switch ns.code {
        case NSFileWriteOutOfSpaceError:
            input.errorCode = "out_of_space"
        case NSFileReadNoSuchFileError:
            input.errorCode = "no_such_file"
        default:
            input.explicitKind = .decode
        }
        return input
    }
    if ns.domain == NSPOSIXErrorDomain && ns.code == Int(ENOSPC) {
        input.errorCode = "enospc"
        return input
    }
    return input
}
