package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.proto.ImageFormat
import org.junit.Assert.*
import org.junit.Test

class ImageProblemTest {
    @Test fun invalidImagesAreNotRetriedAndTransportFailuresCanBeRetried() {
        assertEquals(ImageProblem("图片超过 20 MB，关掉原图再发", false), imageProblem("原图超过20 MB"))
        assertFalse(imageProblem("the image does not decode").retryable)
        assertTrue(imageProblem("unavailable: machine offline").retryable)
        assertEquals("这张图片处理失败", imageProblem("opaque internal exception").message)
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
