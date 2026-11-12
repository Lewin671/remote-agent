import Foundation

public struct ImageProblem: Equatable, Sendable {
    public enum Kind: Hashable, Sendable { case tooLarge, dimensions, sourceMissing, unreadable, storage, offline, failed }
    public let kind: Kind
    public let message: String
    public let retryable: Bool
}

public var originalImageHint: String { L10n.imageOriginalHint() }

/// Codec, transport and OS details stay out of image captions.
public func imageProblem(_ error: String) -> ImageProblem {
    let e = error.lowercased()
    func has(_ pattern: String) -> Bool { e.range(of: pattern, options: .regularExpression) != nil }
    if has("20\\s*(mb|mib)|原图超过") { return ImageProblem(kind: .tooLarge, message: "图片超过 20 MB，关掉原图再发", retryable: false) }
    if has("尺寸|像素|pixels|dimensions|canvas|too large|图片超过|图片太大") { return ImageProblem(kind: .dimensions, message: "图片尺寸太大，缩小后再试", retryable: false) }
    if has("本机副本|重新选择|source.*missing|source.*read") { return ImageProblem(kind: .sourceMissing, message: "这张图片没有本机副本，请移除后重新选择", retryable: false) }
    if has("decode|解不出|读不出|读不到|格式|规范|png|jpeg|heic|webp|metadata|exif|corrupt|unsupported") { return ImageProblem(kind: .unreadable, message: "读不出这张图片，换成 JPEG 或 PNG 再试", retryable: false) }
    if has("quota|空间|配额|storage.*full|disk.*full") { return ImageProblem(kind: .storage, message: "电脑附件空间不足，清理后重试", retryable: true) }
    if has("network|offline|unavailable|fetch|timeout|deadline|expired|网络|连接|离线|断线|超时|过期|重新传") { return ImageProblem(kind: .offline, message: "图片没能传到电脑，连接恢复后重试", retryable: true) }
    return ImageProblem(kind: .failed, message: "这张图片处理失败", retryable: false)
}

public func imageFailureLine(_ errors: [String], locale: String = MessageLocale.current) -> String {
    let problems = errors.map { imageProblem($0) }
    guard let first = problems.first else { return "" }
    return Set(problems.map(\.kind)).count == 1 ? first.message : L10n.imageAttachFailed(count: errors.count, locale: locale)
}
