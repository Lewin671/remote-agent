package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.*
import com.connectrpc.Code
import com.connectrpc.ConnectException

typealias ImageProblemKind = ImageFailureKind
internal data class ImageProblem(val kind: ImageProblemKind, val message: String, val retryable: Boolean)
internal val ORIGINAL_IMAGE_HINT = UiMessages.imageOriginalHint()

/** Stored in memory only. The disk still contains source/bytes/upload intent, never display text. */
data class AttachmentFailure(val kind: ImageProblemKind, val detail: MessageText, val caption: MessageText) {
    val retryable: Boolean get() = kind == ImageProblemKind.NETWORK || kind == ImageProblemKind.QUOTA
}

/** Each app producer names its cause before any locale is resolved. */
enum class AppImageFailure(val kind: ImageProblemKind, val message: UiMessage) {
    SOURCE_MISSING(ImageProblemKind.SOURCE, UiMessages.imageAndroidThisImageHasNoLocalCopyRemoveItAndSelect()),
    FILE_MISSING(ImageProblemKind.SOURCE, UiMessages.attachmentFileMissing()),
    REPROCESS_SELECTION(ImageProblemKind.REPROCESS, UiMessages.imageAndroidReprocessOrSelectTheImageAgain()),
    FILE_UPLOAD_AGAIN(ImageProblemKind.NETWORK, UiMessages.imageAndroidUploadToTheComputerAgainIfTheSourceCannotBe()),
    REPROCESS_MODE(ImageProblemKind.REPROCESS, UiMessages.imageAndroidReprocessUsingTheCurrentMode()),
    UPLOAD_AGAIN(ImageProblemKind.NETWORK, UiMessages.imageAndroidUploadToTheComputerAgain()),
    CHOOSE_COMPUTER(ImageProblemKind.CHOOSE_COMPUTER, UiMessages.attachmentAndroidChooseComputer()),
    REPROCESS_SOURCE_MISSING(ImageProblemKind.SOURCE, UiMessages.imageAndroidThisImageHasNoLocalCopyAvailableForReprocessingRemove()),
    UPDATE_REQUIRED(ImageProblemKind.UPDATE_REQUIRED, UiMessages.attachmentUpdateRequired());

    fun failure(): AttachmentFailure = if (this == FILE_MISSING || this == FILE_UPLOAD_AGAIN)
        AttachmentFailure(kind, message.messageText(), message.messageText())
    else localAttachmentFailure(kind, message.messageText())
}

private fun categoryCaption(kind: ImageProblemKind): UiMessage? = when (kind) {
    ImageProblemKind.ORIGINAL_LIMIT -> UiMessages.imageImageExceeds20MbTurnOffOriginalAndSend()
    ImageProblemKind.DIMENSIONS -> UiMessages.imageImageDimensionsAreTooLargeResizeItAndRetry()
    ImageProblemKind.SOURCE -> UiMessages.imageThisImageHasNoLocalCopyRemoveItAnd()
    ImageProblemKind.FORMAT -> UiMessages.imageCannotReadThisImageTryJpegOrPngInstead()
    ImageProblemKind.QUOTA -> UiMessages.imageTheComputerSAttachmentStorageIsFullClearSpace()
    ImageProblemKind.NETWORK -> UiMessages.imageTheImageCouldNotReachTheComputerRetryWhen()
    else -> null
}

private fun localAttachmentFailure(kind: ImageProblemKind, detail: MessageText): AttachmentFailure =
    AttachmentFailure(kind, detail, categoryCaption(kind)?.messageText() ?: detail)

/** Only genuine raw codec/transport/daemon diagnostics enter this lexical classifier. */
fun rawAttachmentFailure(diagnostic: String): AttachmentFailure {
    val e = diagnostic.lowercase()
    fun has(pattern: String) = Regex(pattern).containsMatchIn(e)
    val kind = when {
        has("20\\s*(mb|mib)|原图超过") -> ImageProblemKind.ORIGINAL_LIMIT
        has("尺寸|像素|pixels|dimensions|canvas|too large|图片超过|图片太大") -> ImageProblemKind.DIMENSIONS
        has("本机副本|重新选择|source.*missing|source.*read") -> ImageProblemKind.SOURCE
        has("decode|解不出|读不出|读不到|格式|规范|png|jpeg|heic|webp|metadata|exif|corrupt|unsupported") -> ImageProblemKind.FORMAT
        has("quota|空间|配额|storage.*full|disk.*full") -> ImageProblemKind.QUOTA
        has("network|offline|unavailable|fetch|timeout|deadline|expired|网络|连接|离线|断线|超时|过期|重新传") -> ImageProblemKind.NETWORK
        else -> ImageProblemKind.OTHER
    }
    return AttachmentFailure(kind, diagnostic.messageText(),
        (categoryCaption(kind) ?: UiMessages.imageThisImageCouldNotBeProcessed()).messageText())
}

/** Inspect typed causes/codes, never errorText()/uploadErrorText() after resolution. */
fun attachmentFailure(error: Throwable, upload: Boolean = false): AttachmentFailure {
    val detail = if (upload) uploadErrorText(error) else errorText(error)
    val declared = (error as? MessageFailure)?.imageKind
    if (declared != null) return localAttachmentFailure(declared, detail)
    if (upload && isUploadHashMismatch(error)) return localAttachmentFailure(ImageProblemKind.SOURCE, detail)
    if (upload && error is CommandException && error.code in setOf("already_exists", "aborted"))
        return localAttachmentFailure(ImageProblemKind.OTHER, detail)
    if (upload && error is java.io.IOException) return localAttachmentFailure(ImageProblemKind.NETWORK, detail)
    if (error is ConnectException) {
        val kind = when {
            error.code == Code.RESOURCE_EXHAUSTED -> ImageProblemKind.QUOTA
            error.code in setOf(Code.UNAVAILABLE, Code.DEADLINE_EXCEEDED) || error.exception is java.io.IOException -> ImageProblemKind.NETWORK
            else -> rawAttachmentFailure(error.message.orEmpty()).kind
        }
        return localAttachmentFailure(kind, detail)
    }
    if (error is MessageFailure && (error !is CommandException || error.display !is MessageText.Raw)) {
        val kind = when ((error as? CommandException)?.code) {
            "network", CommandException.UNAVAILABLE, CommandException.UNKNOWN, CommandException.EXPIRED -> ImageProblemKind.NETWORK
            "resource_exhausted" -> ImageProblemKind.QUOTA
            else -> ImageProblemKind.OTHER
        }
        return localAttachmentFailure(kind, detail)
    }
    val raw = rawAttachmentFailure(error.message.orEmpty())
    return raw.copy(detail = detail)
}

internal fun imageProblem(failure: AttachmentFailure, copy: MessageResolver = ::sessionText): ImageProblem =
    ImageProblem(failure.kind, failure.caption.resolve(copy), failure.retryable)

internal fun imageFailureLine(failures: List<AttachmentFailure>, copy: MessageResolver = ::sessionText, countLine: (Int) -> String): String {
    val problems = failures.map { imageProblem(it, copy = copy) }
    return when { problems.isEmpty() -> ""; problems.map { it.kind }.distinct().size == 1 -> problems.first().message; else -> countLine(failures.size) }
}
