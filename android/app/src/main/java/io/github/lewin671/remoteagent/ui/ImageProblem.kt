package io.github.lewin671.remoteagent.ui

internal enum class ImageProblemKind { ORIGINAL_LIMIT, DIMENSIONS, SOURCE, FORMAT, QUOTA, NETWORK, OTHER }
internal data class ImageProblem(val kind: ImageProblemKind, val message: String, val retryable: Boolean)
internal const val ORIGINAL_IMAGE_HINT = "原图保住画质；CLI 仍可能缩小过大的图片。"

/** A codec/transport exception is diagnostic data, never the image's caption. */
internal fun imageProblem(error: String): ImageProblem {
    val e = error.lowercase()
    fun has(pattern: String) = Regex(pattern).containsMatchIn(e)
    return when {
        has("20\\s*(mb|mib)|原图超过") -> ImageProblem(ImageProblemKind.ORIGINAL_LIMIT, "图片超过 20 MB，关掉原图再发", false)
        has("尺寸|像素|pixels|dimensions|canvas|too large|图片超过|图片太大") -> ImageProblem(ImageProblemKind.DIMENSIONS, "图片尺寸太大，缩小后再试", false)
        has("本机副本|重新选择|source.*missing|source.*read") -> ImageProblem(ImageProblemKind.SOURCE, "这张图片没有本机副本，请移除后重新选择", false)
        has("decode|解不出|读不出|读不到|格式|规范|png|jpeg|heic|webp|metadata|exif|corrupt|unsupported") -> ImageProblem(ImageProblemKind.FORMAT, "读不出这张图片，换成 JPEG 或 PNG 再试", false)
        has("quota|空间|配额|storage.*full|disk.*full") -> ImageProblem(ImageProblemKind.QUOTA, "电脑附件空间不足，清理后重试", true)
        has("network|offline|unavailable|fetch|timeout|deadline|expired|网络|连接|离线|断线|超时|过期|重新传") -> ImageProblem(ImageProblemKind.NETWORK, "图片没能传到电脑，连接恢复后重试", true)
        else -> ImageProblem(ImageProblemKind.OTHER, "这张图片处理失败", false)
    }
}

internal fun imageFailureLine(errors: List<String>, countLine: (Int) -> String): String {
    val problems = errors.map(::imageProblem)
    return when { problems.isEmpty() -> ""; problems.map { it.kind }.distinct().size == 1 -> problems.first().message; else -> countLine(errors.size) }
}
