package io.github.lewin671.remoteagent.ui

import com.connectrpc.Code
import com.connectrpc.ConnectException
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.core.proto.AttachmentEvalScreenRequest
import io.github.lewin671.remoteagent.core.proto.AttachmentEvalViewModel
import io.github.lewin671.remoteagent.core.proto.ImageProblemInput
import io.github.lewin671.remoteagent.core.proto.ImageProblemKind

/** Stored in memory only. Classification, captions, and retryability come from Core (`AttachmentEval`). */
data class AttachmentFailure(
    val input: ImageProblemInput,
    val detail: MessageText,
)

/** Structured app causes mapped directly to Core `ImageProblemKind` (never via legacy `local_key`). */
enum class AppImageFailure(val kind: ImageProblemKind, val message: UiMessage) {
    SOURCE_MISSING(ImageProblemKind.IMAGE_PROBLEM_KIND_SOURCE_MISSING, UiMessages.imageAndroidThisImageHasNoLocalCopyRemoveItAndSelect()),
    FILE_MISSING(ImageProblemKind.IMAGE_PROBLEM_KIND_SOURCE_MISSING, UiMessages.attachmentFileMissing()),
    REPROCESS_SELECTION(ImageProblemKind.IMAGE_PROBLEM_KIND_PROCESSING_FAILED, UiMessages.imageAndroidReprocessOrSelectTheImageAgain()),
    FILE_UPLOAD_AGAIN(ImageProblemKind.IMAGE_PROBLEM_KIND_UPLOAD_FAILED, UiMessages.imageAndroidUploadToTheComputerAgainIfTheSourceCannotBe()),
    REPROCESS_MODE(ImageProblemKind.IMAGE_PROBLEM_KIND_PROCESSING_FAILED, UiMessages.imageAndroidReprocessUsingTheCurrentMode()),
    UPLOAD_AGAIN(ImageProblemKind.IMAGE_PROBLEM_KIND_UPLOAD_FAILED, UiMessages.imageAndroidUploadToTheComputerAgain()),
    CHOOSE_COMPUTER(ImageProblemKind.IMAGE_PROBLEM_KIND_PROCESSING_FAILED, UiMessages.attachmentAndroidChooseComputer()),
    REPROCESS_SOURCE_MISSING(ImageProblemKind.IMAGE_PROBLEM_KIND_SOURCE_MISSING, UiMessages.imageAndroidThisImageHasNoLocalCopyAvailableForReprocessingRemove()),
    UPDATE_REQUIRED(ImageProblemKind.IMAGE_PROBLEM_KIND_PROCESSING_FAILED, UiMessages.attachmentUpdateRequired());

    fun failure(): AttachmentFailure = AttachmentFailure(
        input = ImageProblemInput.newBuilder().setExplicitKind(kind).build(),
        detail = message.messageText(),
    )
}

/**
 * Extracts structured error signals (`explicit_kind`, `error_code`, `raw_diagnostic`, `is_reading_source`)
 * for Core's `AttachmentEval` classifier. Never feeds localized UI prose or legacy `local_key`.
 */
fun attachmentFailure(error: Throwable, upload: Boolean = false): AttachmentFailure {
    val detail = if (upload) uploadErrorText(error) else errorText(error)
    val b = ImageProblemInput.newBuilder()
    val declared = (error as? MessageFailure)?.imageKind?.takeIf { it != ImageProblemKind.IMAGE_PROBLEM_KIND_UNSPECIFIED }
    when {
        declared != null -> b.setExplicitKind(declared)
        upload && isUploadHashMismatch(error) -> b.setExplicitKind(ImageProblemKind.IMAGE_PROBLEM_KIND_SOURCE_MISSING)
        upload && error is java.io.IOException -> b.setErrorCode("io_error")
        error is ConnectException -> {
            when {
                error.code == Code.RESOURCE_EXHAUSTED -> b.setErrorCode("resource_exhausted")
                error.code == Code.UNAVAILABLE -> b.setErrorCode("unavailable")
                error.code == Code.DEADLINE_EXCEEDED -> b.setErrorCode("deadline_exceeded")
                error.exception is java.io.IOException -> b.setErrorCode("io_error")
                else -> b.setRawDiagnostic(error.message.orEmpty())
            }
        }
        error is CommandException -> {
            if (error.code.isNotEmpty()) b.setErrorCode(error.code)
            if (error.display is MessageText.Raw && !error.message.isNullOrEmpty()) {
                b.setRawDiagnostic(error.message.orEmpty())
            }
        }
        error is MessageFailure -> {
            b.setExplicitKind(ImageProblemKind.IMAGE_PROBLEM_KIND_PROCESSING_FAILED)
        }
        !upload && error is java.io.IOException -> {
            b.setIsReadingSource(true)
            b.setRawDiagnostic(error.message.orEmpty())
        }
        else -> b.setRawDiagnostic(error.message.orEmpty())
    }
    return AttachmentFailure(b.build(), detail)
}

internal fun evaluateImageProblems(remote: Remote, inputs: List<ImageProblemInput>): AttachmentEvalViewModel {
    if (inputs.isEmpty()) return AttachmentEvalViewModel.getDefaultInstance()
    return remote.evaluateAttachments(
        AttachmentEvalScreenRequest.newBuilder().addAllProblems(inputs).build(),
    )
}
