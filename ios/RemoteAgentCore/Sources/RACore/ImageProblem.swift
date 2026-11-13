import Connect
import Foundation

public struct ImageProblem: Equatable, Sendable {
    public enum Kind: Hashable, Sendable { case tooLarge, dimensions, sourceMissing, unreadable, storage, offline, failed }
    public let kind: Kind
    public var message: String { localizedMessage() }
    public func localizedMessage(locale: String = MessageLocale.current) -> String {
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
    public init(kind: Kind) { self.kind = kind }
    public var retryable: Bool { kind == .storage || kind == .offline }
}

public var originalImageHint: String { L10n.imageOriginalHint() }

/// A draft failure is semantic state, not an already formatted error sentence.
/// Only file chips need a separate diagnostic; photos use the safe kind caption.
public struct AttachmentFailure: Error, LocalizedError, Equatable, Sendable {
    public let kind: ImageProblem.Kind
    public let detail: LocalizedMessage?
    public init(kind: ImageProblem.Kind, detail: LocalizedMessage? = nil) {
        self.kind = kind; self.detail = detail
    }
    public init(catching error: Error, isFile: Bool = false) {
        if let failure = error as? AttachmentFailure { self = failure; return }
        kind = imageProblem(error).kind
        detail = isFile ? fileUploadMessage(error) : nil
    }
    public var errorDescription: String? { message }
    /// Provider/codec reads have a known origin even when their vendor error has no code we know.
    public static func reading(_ error: Error) -> AttachmentFailure {
        let kind = imageProblem(error).kind
        return AttachmentFailure(kind: kind == .failed ? .unreadable : kind)
    }
    public var problem: ImageProblem { ImageProblem(kind: kind) }
    public var message: String { detail?.resolve() ?? problem.message }
}

/// Keys of app-owned errors are stable regardless of the active language.
/// Unknown local messages deliberately stay generic; their translations are never parsed.
func localImageProblem(_ message: LocalizedMessage) -> ImageProblem? {
    guard let key = message.resourceKey else { return nil }
    let kind: ImageProblem.Kind
    switch key {
    case "image_ios_original_exceeds_mb", "image_image_exceeds_20_mb_turn_off_original_and_send": kind = .tooLarge
    case "image_ios_image_exceeds_mb", "image_ios_image_has_no_dimensions", "image_image_dimensions_are_too_large_resize_it_and_retry": kind = .dimensions
    case "attachment_file_missing", "attachment_ios_cannot_read_the_source_file_remove_it_and_select",
         "image_this_image_has_no_local_copy_remove_it_and": kind = .sourceMissing
    case "image_ios_cannot_read_this_image", "image_ios_cannot_decode_image", "image_ios_cannot_decode_this_image",
         "image_ios_could_not_encode_this_image", "image_ios_not_a_readable_jpeg", "image_ios_not_a_readable_png",
         "image_ios_not_a_readable_webp", "image_ios_not_a_readable_gif", "image_ios_gif_is_truncated",
         "image_ios_gif_contains_an_unrecognized_block", "image_ios_unsupported_image_format",
         "image_cannot_read_this_image_try_jpeg_or_png_instead": kind = .unreadable
    case "attachment_ios_this_phone_s_pending_image_storage_is_full_send",
         "attachment_ios_the_computer_s_attachment_storage_is_full_clear_space",
         "image_the_computer_s_attachment_storage_is_full_clear_space": kind = .storage
    case "connection_no_server", "connection_no_response", "connection_ios_no_response", "connection_ios_server_error", "errors_connection_timed_out",
         "errors_connection_timed_out_reconnecting", "errors_connection_dropped_midway",
         "errors_ios_not_connected_to_the_server", "errors_ios_not_connected_to_the_server_connect_to_make_changes",
         "errors_ios_cannot_find_the_server_check_your_network", "errors_ios_secure_connection_failed",
         "errors_the_computer_is_offline_the_command_was_not_sent", "errors_ios_network_unavailable_the_command_was_not_sent",
         "errors_ios_this_command_expired_and_was_not_sent", "errors_ios_unsure_whether_the_computer_executed_this",
         "image_ios_needs_uploading_to_the_computer_again", "image_the_image_could_not_reach_the_computer_retry_when",
         "attachment_ios_could_not_connect_to_the_computer_retry_the_upload": kind = .offline
    default: return nil
    }
    return ImageProblem(kind: kind)
}

private func uploadProblem(_ code: String) -> ImageProblem? {
    switch code {
    case "resource_exhausted": ImageProblem(kind: .storage)
    case "network", "unavailable", "deadline_exceeded", "expired", "unknown_outcome", "not_found", "generation_conflict": ImageProblem(kind: .offline)
    case "invalid_argument", "data_loss": ImageProblem(kind: .unreadable)
    case "permission_denied", "unauthenticated": ImageProblem(kind: .failed)
    default: nil
    }
}

/// Classify typed local/transport failures before discarding their diagnostic.
public func imageProblem(_ error: Error) -> ImageProblem {
    if let failure = error as? AttachmentFailure { return failure.problem }
    let message: LocalizedMessage?
    switch error {
    case let e as ImageTooLarge: return localImageProblem(e.localized) ?? ImageProblem(kind: .tooLarge)
    case let e as UserError: message = e.localized
    case let e as CommandError: message = e.localized
    case let e as UploadError: message = e.localized
    case let e as PairError: message = e.localized
    case let e as CryptoError: message = e.localized
    case let e as OpenError: message = e.localized
    default: message = nil
    }
    if let message, let problem = localImageProblem(message) { return problem }
    if let failure = error as? any UploadFailure, let problem = uploadProblem(failure.code) { return problem }
    if let message {
        if let failure = error as? CommandError, failure.fromDaemon, let raw = message.rawText { return rawImageProblem(raw) }
        // Unknown local messages (including string-backed errors) stay generic.
        // Do not even bridge them to NSError, which could invoke errorDescription.
        return ImageProblem(kind: .failed)
    }
    if error is TimedOut || networkFailure(error) != nil || error is URLError {
        return ImageProblem(kind: .offline)
    }
    if let failure = error as? ConnectError {
        if let problem = uploadProblem(failure.code.name) { return problem }
        return failure.message.map(rawImageProblem) ?? ImageProblem(kind: .failed)
    }
    let ns = error as NSError
    if ns.domain == NSURLErrorDomain { return ImageProblem(kind: .offline) }
    if ns.domain == NSCocoaErrorDomain {
        switch ns.code {
        case NSFileWriteOutOfSpaceError: return ImageProblem(kind: .storage)
        case NSFileReadNoSuchFileError: return ImageProblem(kind: .sourceMissing)
        default: return ImageProblem(kind: .unreadable)
        }
    }
    if ns.domain == NSPOSIXErrorDomain && ns.code == Int(ENOSPC) { return ImageProblem(kind: .storage) }
    return ImageProblem(kind: .failed)
}

/// Only untranslated codec/transport/daemon exception text may enter this adapter.
/// Do not pass errorText(), L10n results, or a draft's displayed caption here.
public func rawImageProblem(_ error: String) -> ImageProblem {
    let e = error.lowercased()
    func has(_ pattern: String) -> Bool { e.range(of: pattern, options: .regularExpression) != nil }
    if has("20\\s*(mb|mib)|原图超过") { return ImageProblem(kind: .tooLarge) }
    if has("尺寸|像素|pixels|dimensions|canvas|too large|图片超过|图片太大") { return ImageProblem(kind: .dimensions) }
    if has("本机副本|重新选择|source.*missing|source.*read") { return ImageProblem(kind: .sourceMissing) }
    if has("decode|解不出|读不出|读不到|格式|规范|png|jpeg|heic|webp|metadata|exif|corrupt|unsupported") { return ImageProblem(kind: .unreadable) }
    if has("quota|空间|配额|storage.*full|disk.*full") { return ImageProblem(kind: .storage) }
    if has("network|offline|unavailable|fetch|timeout|deadline|expired|网络|连接|离线|断线|超时|过期|重新传") { return ImageProblem(kind: .offline) }
    return ImageProblem(kind: .failed)
}

public func imageFailureLine(_ problems: [ImageProblem], locale: String = MessageLocale.current) -> String {
    guard let first = problems.first else { return "" }
    return Set(problems.map(\.kind)).count == 1 ? first.localizedMessage(locale: locale) : L10n.imageAttachFailed(count: problems.count, locale: locale)
}
