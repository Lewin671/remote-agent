package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.proto.ImageProblemInput
import io.github.lewin671.remoteagent.core.proto.ImageProblemKind
import io.github.lewin671.remoteagent.proto.ImageFormat
import org.junit.Assert.*
import org.junit.Test

@org.junit.runner.RunWith(org.robolectric.RobolectricTestRunner::class)
@org.robolectric.annotation.Config(sdk = [35], application = android.app.Application::class)
class ImageProblemTest {
    @get:org.junit.Rule val batchCResources = io.github.lewin671.remoteagent.ui.SessionMessageContextRule()

    @Test fun coreAttachmentEvalClassifiesStructuredProblemsAndSummary() {
        val remote = Remote(Account("http://127.0.0.1:1", "c1"), Api.defaultHttp())
        try {
            val eval = evaluateImageProblems(
                remote,
                listOf(
                    ImageProblemInput.newBuilder().setExplicitKind(ImageProblemKind.IMAGE_PROBLEM_KIND_ORIGINAL_TOO_LARGE).build(),
                    ImageProblemInput.newBuilder().setErrorCode("unavailable").build(),
                ),
            )
            assertEquals(2, eval.problemsCount)
            assertEquals(ImageProblemKind.IMAGE_PROBLEM_KIND_ORIGINAL_TOO_LARGE, eval.getProblems(0).kind)
            assertFalse(eval.getProblems(0).retryable)
            assertEquals(ImageProblemKind.IMAGE_PROBLEM_KIND_UPLOAD_FAILED, eval.getProblems(1).kind)
            assertTrue(eval.getProblems(1).retryable)
            assertTrue(eval.hasFailureSummary())
            assertEquals("image.attach_failed", eval.failureSummary.key)
        } finally {
            remote.close()
        }
    }

    @Test fun uncertainMessagesAreRecoverableOnlyWhileTheirLocalAttachmentSurvives() {
        val image = PendingImage("image", null)
        val outgoing = Outgoing("request", images = listOf(image))
        assertFalse(outgoing.hasLocalAttachments)
        image.processed = Processed(byteArrayOf(1), ImageFormat.IMAGE_FORMAT_PNG, 1, 1)
        assertTrue(outgoing.hasLocalAttachments)
        image.processed = null
        assertFalse(outgoing.hasLocalAttachments)
    }
}
