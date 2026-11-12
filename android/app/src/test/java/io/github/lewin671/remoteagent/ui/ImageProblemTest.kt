package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages

import io.github.lewin671.remoteagent.proto.ImageFormat
import org.junit.Assert.*
import org.junit.Test

@org.junit.runner.RunWith(org.robolectric.RobolectricTestRunner::class)
@org.robolectric.annotation.Config(sdk = [35], application = android.app.Application::class)
class ImageProblemTest {
    @get:org.junit.Rule val batchCResources = io.github.lewin671.remoteagent.ui.SessionMessageContextRule()
    @Test fun invalidImagesAreNotRetriedAndTransportFailuresCanBeRetried() {
        assertEquals(ImageProblem(ImageProblemKind.ORIGINAL_LIMIT, "图片超过 20 MB，关掉原图再发", false), imageProblem("原图超过20 MB"))
        assertFalse(imageProblem("the image does not decode").retryable)
        assertTrue(imageProblem("unavailable: machine offline").retryable)
        assertEquals(messageTextForTest(UiMessages.imageThisImageCouldNotBeProcessed()), imageProblem("opaque internal exception").message)
        assertEquals("2 张图没加上，点图片看原因", imageFailureLine(listOf("图片尺寸太大", "decode failed")) { "$it 张图没加上，点图片看原因" })
    }

    @Test fun uncertainMessagesAreRecoverableOnlyWhileTheirLocalAttachmentSurvives() {
        val image = PendingImage("image", null)
        val outgoing = Outgoing("request", "text", imageIds = listOf("uploaded"), images = listOf(image))
        outgoing.state = Outgoing.State.UNKNOWN
        assertFalse(outgoing.canPutBack)
        image.processed = Processed(byteArrayOf(1), ImageFormat.IMAGE_FORMAT_PNG, 1, 1)
        assertTrue(outgoing.canPutBack)
        image.processed = null
        assertFalse(outgoing.canPutBack)
    }
}
