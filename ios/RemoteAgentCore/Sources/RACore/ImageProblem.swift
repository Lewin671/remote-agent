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
    public let retryable: Bool
}

public var originalImageHint: String { L10n.imageOriginalHint() }

/// Codec, transport and OS details stay out of image captions.
public func imageProblem(_ error: String) -> ImageProblem {
    let e = error.lowercased()
    func has(_ pattern: String) -> Bool { e.range(of: pattern, options: .regularExpression) != nil }
    if has("20\\s*(mb|mib)|原图超过") { return ImageProblem(kind: .tooLarge, retryable: false) }
    if has("尺寸|像素|pixels|dimensions|canvas|too large|image exceeds|original exceeds|图片超过|图片太大") { return ImageProblem(kind: .dimensions, retryable: false) }
    if has("本机副本|重新选择|source.*missing|source.*read|local copy|select.*again") { return ImageProblem(kind: .sourceMissing, retryable: false) }
    if has("decode|解不出|读不出|读不到|格式|规范|png|jpeg|heic|webp|metadata|exif|corrupt|unsupported|cannot read|could not encode|not a readable|truncated|unrecognized block") { return ImageProblem(kind: .unreadable, retryable: false) }
    if has("quota|空间|配额|storage.*full|disk.*full") { return ImageProblem(kind: .storage, retryable: true) }
    if has("network|offline|unavailable|fetch|timeout|deadline|expired|网络|连接|离线|断线|超时|过期|重新传|needs? uploading|upload.*again|could not reach|connect.*computer") { return ImageProblem(kind: .offline, retryable: true) }
    return ImageProblem(kind: .failed, retryable: false)
}

public func imageFailureLine(_ errors: [String], locale: String = MessageLocale.current) -> String {
    let problems = errors.map { imageProblem($0) }
    guard let first = problems.first else { return "" }
    return Set(problems.map(\.kind)).count == 1 ? first.localizedMessage(locale: locale) : L10n.imageAttachFailed(count: errors.count, locale: locale)
}
