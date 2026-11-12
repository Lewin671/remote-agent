package io.github.lewin671.remoteagent.ui

import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.data.ThemeMode
import org.junit.*
import org.junit.Assert.*
import org.junit.runner.RunWith
import org.robolectric.ParameterizedRobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(ParameterizedRobolectricTestRunner::class)
@Config(sdk = [35], application = android.app.Application::class)
class ImageFailureLocaleRegressionTest(private val locale: String) {
    companion object {
        @JvmStatic @ParameterizedRobolectricTestRunner.Parameters(name = "{0}")
        fun locales() = listOf(arrayOf("zh-CN"), arrayOf("en"))
    }
    @get:Rule val ui = createComposeRule()
    private fun text(message: UiMessage) = messageTextForTest(message, locale)
    private data class Case(val cause: AppImageFailure, val kind: ImageProblemKind, val retryable: Boolean, val caption: UiMessage)
    private val cases get() = listOf(
        Case(AppImageFailure.SOURCE_MISSING, ImageProblemKind.SOURCE, false, UiMessages.imageThisImageHasNoLocalCopyRemoveItAnd()),
        Case(AppImageFailure.FILE_MISSING, ImageProblemKind.SOURCE, false, UiMessages.attachmentFileMissing()),
        Case(AppImageFailure.REPROCESS_SELECTION, ImageProblemKind.REPROCESS, false, UiMessages.imageAndroidReprocessOrSelectTheImageAgain()),
        Case(AppImageFailure.FILE_UPLOAD_AGAIN, ImageProblemKind.NETWORK, true, UiMessages.imageAndroidUploadToTheComputerAgainIfTheSourceCannotBe()),
        Case(AppImageFailure.REPROCESS_MODE, ImageProblemKind.REPROCESS, false, UiMessages.imageAndroidReprocessUsingTheCurrentMode()),
        Case(AppImageFailure.UPLOAD_AGAIN, ImageProblemKind.NETWORK, true, UiMessages.imageTheImageCouldNotReachTheComputerRetryWhen()),
        Case(AppImageFailure.CHOOSE_COMPUTER, ImageProblemKind.CHOOSE_COMPUTER, false, UiMessages.attachmentAndroidChooseComputer()),
        Case(AppImageFailure.REPROCESS_SOURCE_MISSING, ImageProblemKind.SOURCE, false, UiMessages.imageThisImageHasNoLocalCopyRemoveItAnd()),
        Case(AppImageFailure.UPDATE_REQUIRED, ImageProblemKind.UPDATE_REQUIRED, false, UiMessages.attachmentUpdateRequired()),
    )

    @Test fun everyAppCauseHasTheSameKindRetryAndSpecificCaptionInBothLanguages() {
        assertEquals(AppImageFailure.entries.toSet(), cases.map { it.cause }.toSet())
        for ((cause, kind, retryable, caption) in cases) {
            val failed = PendingImage.State.Failed(cause)
            val problem = imageProblem(failed.failure, ::text)
            assertEquals(cause.name, kind, problem.kind)
            assertEquals(cause.name, retryable, problem.retryable)
            assertEquals(cause.name, text(caption), problem.message)
            assertEquals(text(cause.message), failed.error.resolve(::text))
            assertNotEquals(text(UiMessages.imageThisImageCouldNotBeProcessed()), problem.message)
        }
    }

    @Test fun typedLocalExceptionsNeverClassifyTheirTranslatedDisplayText() {
        val codec = runCatching { stripPng(byteArrayOf()) }.exceptionOrNull()!!
        val dimensions = runCatching { decodeSample(0, 1, 2048, 1024) }.exceptionOrNull()!!
        val errors = listOf(
            Triple(codec, ImageProblemKind.FORMAT, UiMessages.imageCannotReadThisImageTryJpegOrPngInstead()),
            Triple(dimensions, ImageProblemKind.DIMENSIONS, UiMessages.imageImageDimensionsAreTooLargeResizeItAndRetry()),
            Triple(ImageTooLarge(UiMessages.imageAndroidOriginalExceeds20Mb(), ImageProblemKind.ORIGINAL_LIMIT), ImageProblemKind.ORIGINAL_LIMIT, UiMessages.imageImageExceeds20MbTurnOffOriginalAndSend()),
            Triple(ImageTooLarge(UiMessages.imageAndroidThisImageIsTooLargeForThePhoneSAvailable(), ImageProblemKind.DIMENSIONS), ImageProblemKind.DIMENSIONS, UiMessages.imageImageDimensionsAreTooLargeResizeItAndRetry()),
            Triple(FileSourceException(java.io.IOException("opaque provider detail")), ImageProblemKind.SOURCE, UiMessages.imageThisImageHasNoLocalCopyRemoveItAnd()),
            Triple(FileContentChanged(), ImageProblemKind.SOURCE, UiMessages.imageThisImageHasNoLocalCopyRemoveItAnd()),
            Triple(CommandException("unavailable", UiMessages.imageAndroidTheComputerHasNotConfirmedTheAttachmentUploadRetry().messageText(), imageKind = ImageProblemKind.NETWORK), ImageProblemKind.NETWORK, UiMessages.imageTheImageCouldNotReachTheComputerRetryWhen()),
        )
        for ((error, kind, caption) in errors) {
            val failure = attachmentFailure(error)
            val problem = imageProblem(failure, ::text)
            assertEquals(kind, problem.kind)
            assertEquals(kind == ImageProblemKind.NETWORK, problem.retryable)
            assertEquals(text(caption), problem.message)
        }
        val uploaded = imageProblem(attachmentFailure(java.io.IOException("opaque detail"), upload = true), ::text)
        assertEquals(ImageProblemKind.NETWORK, uploaded.kind)
        assertTrue(uploaded.retryable)
        assertEquals(text(UiMessages.imageTheImageCouldNotReachTheComputerRetryWhen()), uploaded.message)
        val local = MessageException(UiMessages.attachmentUpdateRequired().messageText())
        assertEquals(text(UiMessages.attachmentUpdateRequired()), imageProblem(attachmentFailure(local), ::text).message)
    }

    @Test fun existingDiskBytesRestoreKindsWithoutStoringOrReadingAResolvedFailure() {
        val files = MemoryFiles()
        // The pre-fix order/meta/upload format; no new fields and no error text persisted.
        files.put("draft", "order", "0\nimage\nstale\nfile".toByteArray())
        files.put("draft", "image.meta", "1\n10\n10\n0\ncontent://example/image\n".toByteArray())
        files.put("draft", "image.img", byteArrayOf(1, 2, 3))
        files.put("draft", "stale.meta", "1\n10\n10\n1\ncontent://example/stale\n".toByteArray())
        files.put("draft", "stale.img", byteArrayOf(4, 5, 6))
        val bytes = java.io.ByteArrayOutputStream()
        java.io.DataOutputStream(bytes).use { it.writeUTF("content://example/file"); it.writeUTF("example.txt"); it.writeLong(42); it.writeBoolean(true) }
        files.put("draft", "file.file", bytes.toByteArray())
        val original = files.data.mapValues { it.value.copyOf() }
        val (loaded, originalMode) = loadImages(files, "draft")
        assertFalse(originalMode)
        assertEquals(listOf("image", "stale", "file"), loaded.map { it.id })
        assertEquals(listOf(ImageProblemKind.NETWORK, ImageProblemKind.REPROCESS, ImageProblemKind.NETWORK), loaded.map { (it.state as PendingImage.State.Failed).failure.kind })
        assertEquals(text(UiMessages.imageTheImageCouldNotReachTheComputerRetryWhen()), imageProblem((loaded[0].state as PendingImage.State.Failed).failure, ::text).message)
        assertEquals(text(UiMessages.imageAndroidReprocessUsingTheCurrentMode()), imageProblem((loaded[1].state as PendingImage.State.Failed).failure, ::text).message)
        assertEquals(text(UiMessages.imageAndroidUploadToTheComputerAgainIfTheSourceCannotBe()), (loaded[2].state as PendingImage.State.Failed).error.resolve(::text))
        assertEquals(original.keys, files.data.keys)
        original.forEach { (key, value) -> assertArrayEquals(key, value, files.data.getValue(key)) }
    }

    @Test fun returningToTheComposerRetainsExplicitSourceAndProcessingCauses() {
        val source = PendingImage("source", null, retainSource = false)
        source.restoreToDraft()
        assertEquals(ImageProblemKind.SOURCE, (source.state as PendingImage.State.Failed).failure.kind)
        assertFalse(imageProblem((source.state as PendingImage.State.Failed).failure, ::text).retryable)
        val stale = PendingImage("stale", "content://example/stale", retainSource = false)
        stale.restoreToDraft()
        assertEquals(ImageProblemKind.REPROCESS, (stale.state as PendingImage.State.Failed).failure.kind)
        assertEquals(text(UiMessages.imageAndroidReprocessOrSelectTheImageAgain()), imageProblem((stale.state as PendingImage.State.Failed).failure, ::text).message)
    }

    private fun viewer(cause: AppImageFailure) {
        val remote = Remote(Account("http://127.0.0.1:1", ByteArray(32), ByteArray(32), "preview"), Api.defaultHttp())
        val draft = ImageDraft("locale-failure", MemoryFiles(), scope = null)
        draft.images += PendingImage("image", null, retainSource = false).apply { state = PendingImage.State.Failed(cause) }
        try {
            ui.setContent { RaTheme(ThemeMode.LIGHT) { CompositionLocalProvider(LocalMessageLocale provides locale) { DraftImageViewer(draft, remote, "computer", 0) {} } } }
            val problem = imageProblem((draft.images.single().state as PendingImage.State.Failed).failure, ::text)
            ui.onNodeWithText(problem.message).assertIsDisplayed()
            if (problem.retryable) ui.onNodeWithText(text(UiMessages.commonRetry())).assertIsDisplayed()
            else ui.onNodeWithText(text(UiMessages.commonRetry())).assertDoesNotExist()
        } finally { remote.close() }
    }
    @Test fun restoredNetworkFailureShowsRetryInTheEnglishAndChineseViewer() = viewer(AppImageFailure.UPLOAD_AGAIN)
    @Test fun missingSourceShowsTheSpecificCaptionWithoutRetryInBothLanguages() = viewer(AppImageFailure.SOURCE_MISSING)
}
