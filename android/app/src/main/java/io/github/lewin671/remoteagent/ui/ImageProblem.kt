package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages

internal enum class ImageProblemKind { ORIGINAL_LIMIT, DIMENSIONS, SOURCE, FORMAT, QUOTA, NETWORK, OTHER }
internal data class ImageProblem(val kind: ImageProblemKind, val message: String, val retryable: Boolean)
internal val ORIGINAL_IMAGE_HINT = UiMessages.imageOriginalHint()

/** A codec/transport exception is diagnostic data, never the image's caption. */
internal fun imageProblem(error: String, copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText): ImageProblem {
    val e = error.lowercase()
    fun has(pattern: String) = Regex(pattern).containsMatchIn(e)
    return when {
        has("20\\s*(mb|mib)|原图超过") -> ImageProblem(ImageProblemKind.ORIGINAL_LIMIT, copy(UiMessages.imageImageExceeds20MbTurnOffOriginalAndSend()), false)
        has("尺寸|像素|pixels|dimensions|canvas|too large|图片超过|图片太大") -> ImageProblem(ImageProblemKind.DIMENSIONS, copy(UiMessages.imageImageDimensionsAreTooLargeResizeItAndRetry()), false)
        has("本机副本|重新选择|source.*missing|source.*read") -> ImageProblem(ImageProblemKind.SOURCE, copy(UiMessages.imageThisImageHasNoLocalCopyRemoveItAnd()), false)
        has("decode|解不出|读不出|读不到|格式|规范|png|jpeg|heic|webp|metadata|exif|corrupt|unsupported") -> ImageProblem(ImageProblemKind.FORMAT, copy(UiMessages.imageCannotReadThisImageTryJpegOrPngInstead()), false)
        has("quota|空间|配额|storage.*full|disk.*full") -> ImageProblem(ImageProblemKind.QUOTA, copy(UiMessages.imageTheComputerSAttachmentStorageIsFullClearSpace()), true)
        has("network|offline|unavailable|fetch|timeout|deadline|expired|网络|连接|离线|断线|超时|过期|重新传") -> ImageProblem(ImageProblemKind.NETWORK, copy(UiMessages.imageTheImageCouldNotReachTheComputerRetryWhen()), true)
        else -> ImageProblem(ImageProblemKind.OTHER, copy(UiMessages.imageThisImageCouldNotBeProcessed()), false)
    }
}

internal fun imageFailureLine(errors: List<String>, copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText, countLine: (Int) -> String): String {
    val problems = errors.map { imageProblem(it, copy = copy) }
    return when { problems.isEmpty() -> ""; problems.map { it.kind }.distinct().size == 1 -> problems.first().message; else -> countLine(errors.size) }
}
