package io.github.lewin671.remoteagent.ui

import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import io.github.lewin671.remoteagent.core.*
import io.github.lewin671.remoteagent.core.proto.ImageProblemKind
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

    @Test fun typedLocalExceptionsPassStructuredSignalsWithoutLocalKeyOrProse() {
        val remote = Remote(Account("http://127.0.0.1:1", "preview"), Api.defaultHttp())
        try {
            val codec = runCatching { stripPng(byteArrayOf()) }.exceptionOrNull()!!
            val dimensions = runCatching { decodeSample(0, 1, 2048, 1024) }.exceptionOrNull()!!
            val errors = listOf(
                codec to ImageProblemKind.IMAGE_PROBLEM_KIND_DECODE,
                dimensions to ImageProblemKind.IMAGE_PROBLEM_KIND_DIMENSIONS,
                ImageTooLarge(UiMessages.imageAndroidOriginalExceeds20Mb(), ImageProblemKind.IMAGE_PROBLEM_KIND_ORIGINAL_TOO_LARGE) to ImageProblemKind.IMAGE_PROBLEM_KIND_ORIGINAL_TOO_LARGE,
                ImageTooLarge(UiMessages.imageAndroidThisImageIsTooLargeForThePhoneSAvailable(), ImageProblemKind.IMAGE_PROBLEM_KIND_DIMENSIONS) to ImageProblemKind.IMAGE_PROBLEM_KIND_DIMENSIONS,
                FileSourceException(java.io.IOException("opaque provider detail")) to ImageProblemKind.IMAGE_PROBLEM_KIND_SOURCE_MISSING,
                FileContentChanged() to ImageProblemKind.IMAGE_PROBLEM_KIND_SOURCE_MISSING,
                CommandException("unavailable", UiMessages.imageAndroidTheComputerHasNotConfirmedTheAttachmentUploadRetry().messageText(), imageKind = ImageProblemKind.IMAGE_PROBLEM_KIND_UPLOAD_FAILED) to ImageProblemKind.IMAGE_PROBLEM_KIND_UPLOAD_FAILED,
            )
            val failures = errors.map { attachmentFailure(it.first) }
            val eval = evaluateImageProblems(remote, failures.map { it.input })
            assertEquals(errors.size, eval.problemsCount)
            for (i in errors.indices) {
                val expectedKind = errors[i].second
                val view = eval.getProblems(i)
                assertEquals(expectedKind, view.kind)
                assertEquals(expectedKind == ImageProblemKind.IMAGE_PROBLEM_KIND_UPLOAD_FAILED, view.retryable)
            }
            val ioUpload = attachmentFailure(java.io.IOException("opaque detail"), upload = true)
            val ioView = evaluateImageProblems(remote, listOf(ioUpload.input)).getProblems(0)
            assertEquals(ImageProblemKind.IMAGE_PROBLEM_KIND_UPLOAD_FAILED, ioView.kind)
            assertTrue(ioView.retryable)
        } finally {
            remote.close()
        }
    }

    @Test fun existingDiskBytesRestoreStructuredKindsWithoutStoringOrReadingAResolvedFailure() {
        val files = MemoryFiles()
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
        assertEquals(
            listOf(
                ImageProblemKind.IMAGE_PROBLEM_KIND_UPLOAD_FAILED,
                ImageProblemKind.IMAGE_PROBLEM_KIND_PROCESSING_FAILED,
                ImageProblemKind.IMAGE_PROBLEM_KIND_UPLOAD_FAILED,
            ),
            loaded.map { (it.state as PendingImage.State.Failed).failure.input.explicitKind },
        )
        assertEquals(text(UiMessages.imageAndroidUploadToTheComputerAgainIfTheSourceCannotBe()), (loaded[2].state as PendingImage.State.Failed).error.resolve(::text))
        assertEquals(original.keys, files.data.keys)
        original.forEach { (key, value) -> assertArrayEquals(key, value, files.data.getValue(key)) }
    }

    @Test fun returningToTheComposerRetainsExplicitSourceAndProcessingCauses() {
        val source = PendingImage("source", null, retainSource = false)
        source.restoreToDraft()
        assertEquals(ImageProblemKind.IMAGE_PROBLEM_KIND_SOURCE_MISSING, (source.state as PendingImage.State.Failed).failure.input.explicitKind)
        val stale = PendingImage("stale", "content://example/stale", retainSource = false)
        stale.restoreToDraft()
        assertEquals(ImageProblemKind.IMAGE_PROBLEM_KIND_PROCESSING_FAILED, (stale.state as PendingImage.State.Failed).failure.input.explicitKind)
    }

    private fun viewer(cause: AppImageFailure, expectedMessage: UiMessage, expectRetry: Boolean) {
        val remote = Remote(Account("http://127.0.0.1:1", "preview"), Api.defaultHttp())
        val draft = ImageDraft("locale-failure", MemoryFiles(), scope = null)
        draft.images += PendingImage("image", null, retainSource = false).apply { state = PendingImage.State.Failed(cause) }
        try {
            ui.setContent { RaTheme(ThemeMode.LIGHT) { CompositionLocalProvider(LocalMessageLocale provides locale) { DraftImageViewer(draft, remote, "computer", 0, originalMaxBytes = 20L * 1024 * 1024) {} } } }
            ui.onNodeWithText(text(expectedMessage)).assertIsDisplayed()
            if (expectRetry) ui.onNodeWithText(text(UiMessages.commonRetry())).assertIsDisplayed()
            else ui.onNodeWithText(text(UiMessages.commonRetry())).assertDoesNotExist()
        } finally {
            remote.close()
        }
    }

    @Test fun restoredNetworkFailureShowsRetryInTheEnglishAndChineseViewer() =
        viewer(AppImageFailure.UPLOAD_AGAIN, UiMessages.imageTheImageCouldNotReachTheComputerRetryWhen(), expectRetry = true)

    @Test fun missingSourceShowsTheSpecificCaptionWithoutRetryInBothLanguages() =
        viewer(AppImageFailure.SOURCE_MISSING, UiMessages.imageThisImageHasNoLocalCopyRemoveItAnd(), expectRetry = false)
}
