package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.*
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = android.app.Application::class)
class OutgoingFailureRegressionTest {
    @get:Rule val resources = SessionMessageContextRule()
    private fun feedback(o: Outgoing, locale: String = "zh-CN") = outgoingFailure(o) { messageTextForTest(it, locale) }

    // Frozen from origin/main: do not derive the expected visible sentence from the replacement.
    private fun mainFeedback(reason: String, unknown: Boolean): String {
        val short = when {
            reason.contains("回复打不开") || reason.contains("可验证的执行结果") -> "回复验证失败"
            reason.contains("连接中途断了") -> "连接中途断了"
            reason.contains("不确定电脑有没有执行") || reason.contains("停止等待回复") -> "结果未知"
            reason.contains("图片不在电脑上") -> "图片不在电脑上"
            reason.contains("电脑不在线") -> "电脑不在线"
            else -> reason.trim().ifEmpty { "原因未知" }
        }
        return (if (unknown) "可能没发出去：" else "没发出去：") + short
    }

    @Test fun missingFilesUseTheRetainedAttachmentMessageRatherThanTheDaemonId() = runBlocking {
        for (images in listOf(emptyList(), listOf("image-id"))) {
            val o = Outgoing("request", "keep text", imageIds = images, fileIds = listOf("file-id"))
            o.deliver { throw CommandException("not_found", "file-id is not on this machine; upload it again", fromDaemon = true) }
            assertTrue(o.missingImages)
            assertEquals(mainFeedback("附件不在电脑上。放回输入框后会重新上传，文字和附件都会保留", false), feedback(o).text)
            assertEquals(messageTextForTest(UiMessages.composerSendFailure(messageTextForTest(UiMessages.attachmentAndroidMissingFiles(), "en")), "en"), feedback(o, "en").text)
            assertFalse(feedback(o).text.contains("file-id"))
        }
    }

    @Test fun everySenderFailureBranchKeepsTheMainChineseCaption() = runBlocking {
        val cases = listOf(
            Triple(UiMessages.errorsAndroidComputerOutcomeUnknownTheConnectionDropped(), CommandFailureKind.CONNECTION_DROPPED, CommandException.UNKNOWN),
            Triple(UiMessages.errorsAndroidCannotOpenTheComputerSReplyCommandOutcomeUnknown(), CommandFailureKind.REPLY_UNVERIFIED, CommandException.UNKNOWN),
            Triple(UiMessages.errorsTheRelayDidNotProvideAVerifiableExecutionResult(), CommandFailureKind.REPLY_UNVERIFIED, CommandException.UNKNOWN),
            Triple(UiMessages.errorsAndroidNoVerifiableExecutionResultReceivedRetryTheSameRequest(), CommandFailureKind.REPLY_UNVERIFIED, CommandException.UNKNOWN),
            Triple(UiMessages.errorsAndroidComputerOutcomeUnknownStoppedWaitingForAReplyRetryingSends(), CommandFailureKind.UNKNOWN, CommandException.UNKNOWN),
            Triple(UiMessages.errorsAndroidComputerOutcomeUnknownValue1("diagnostic"), CommandFailureKind.UNKNOWN, CommandException.UNKNOWN),
            Triple(UiMessages.errorsAndroidComputerOutcomeUnknownTheRequestMayHaveArrivedBeforeLogin("diagnostic"), CommandFailureKind.UNKNOWN, CommandException.UNKNOWN),
            Triple(UiMessages.errorsTheComputerIsOfflineTheCommandWasNotSent(), CommandFailureKind.COMPUTER_OFFLINE, CommandException.UNAVAILABLE),
            Triple(UiMessages.errorsAndroidThisCommandExpiredAndWasNotSent(), CommandFailureKind.RAW, CommandException.EXPIRED),
            Triple(UiMessages.errorsAndroidThePreviousOutcomeIsUnknownTheRequestExpiredAndWill(), CommandFailureKind.RAW, CommandException.UNKNOWN),
        )
        for ((message, kind, code) in cases) {
            val o = Outgoing("request", "text")
            o.deliver { throw CommandException(code, message.messageText(), failureKind = kind) }
            assertEquals(message.key, mainFeedback(messageTextForTest(message), code == CommandException.UNKNOWN), feedback(o).text)
        }
        for (reason in listOf("raw daemon diagnostic", "  raw reason  ", "")) {
            val o = Outgoing("request", "text")
            o.deliver { throw CommandException("invalid_argument", reason, fromDaemon = true) }
            assertEquals(mainFeedback(reason, false), feedback(o).text)
        }
        val image = Outgoing("image", "text", imageIds = listOf("image-id"))
        image.deliver { throw CommandException("not_found", "image-id is not on this machine; upload it again", fromDaemon = true) }
        assertEquals(mainFeedback("图片不在电脑上。放回输入框后会重新上传，文字和图片都会保留", false), feedback(image).text)
    }

    @Test fun unknownOutcomesNeverBecomeDefiniteMissingAttachments() = runBlocking {
        for (fromDaemon in listOf(false, true)) {
            val o = Outgoing("request", "text", fileIds = listOf("file-id"))
            o.deliver { throw CommandException(CommandException.UNKNOWN, "raw timeout") }
            o.deliver { throw CommandException("not_found", "file-id is not on this machine; upload it again", fromDaemon = fromDaemon) }
            assertEquals(fromDaemon, o.missingImages)
            assertEquals(!fromDaemon, o.state == Outgoing.State.UNKNOWN)
            val original = if (fromDaemon) "附件不在电脑上。放回输入框后会重新上传，文字和附件都会保留" else "file-id is not on this machine; upload it again"
            assertEquals(mainFeedback(original, !fromDaemon), feedback(o).text)
        }
        val attempt = Attempt()
        attempt.settle(CommandException(CommandException.UNKNOWN, "raw timeout"))
        val expired = checkNotNull(attempt.settle(CommandException(CommandException.EXPIRED, "expired")))
        val o = Outgoing("request", "text")
        o.deliver { throw expired }
        assertEquals(mainFeedback(errorText(expired).resolve(::messageTextForTest), true), feedback(o).text)
        val fallback = Outgoing("fallback", "text").apply { state = Outgoing.State.UNKNOWN; error = "App 重启过，不确定这条有没有送到" }
        assertNull(fallback.failureDisplay)
        assertEquals(mainFeedback(fallback.error, true), feedback(fallback).text)
    }

    @Test fun diskRestorationAndRetryCannotRetainOrShortenAnEarlierFailure() = runBlocking {
        val files = MemoryFiles()
        val records = OutgoingRecords(files)
        records.save("session", Outgoing("saved", "original text", fileIds = listOf("file-id")), false)
        val restored = records.load("session").single()
        assertEquals(mainFeedback("App 重启过，不确定这条有没有送到", true), feedback(restored).text)
        assertEquals(messageTextForTest(UiMessages.composerMaybeFailure(messageTextForTest(UiMessages.composerAndroidRestartUnknown(), "en")), "en"), feedback(restored, "en").text)
        val command = restored.commandRequestId
        restored.deliver {
            assertEquals(CommandFailureKind.RAW, restored.failureKind)
            assertNull(restored.failureDisplay)
            assertEquals("", restored.error)
            assertFalse(restored.missingImages)
        }
        assertEquals(Outgoing.State.SENT, restored.state)
        assertEquals(command, restored.commandRequestId)
        assertNull(restored.failureDisplay)
        val failed = Outgoing("failed", "text", fileIds = listOf("file-id"))
        failed.deliver { throw CommandException("not_found", "file-id is not on this machine; upload it again", fromDaemon = true) }
        val previous = failed.commandRequestId
        failed.deliver {
            assertEquals(CommandFailureKind.RAW, failed.failureKind)
            assertNull(failed.failureDisplay)
            assertEquals("", failed.error)
            assertFalse(failed.missingImages)
            throw CommandException(CommandException.UNKNOWN, "raw timeout")
        }
        assertNotEquals(previous, failed.commandRequestId)
        assertEquals(mainFeedback("raw timeout", true), feedback(failed).text)
    }
}
