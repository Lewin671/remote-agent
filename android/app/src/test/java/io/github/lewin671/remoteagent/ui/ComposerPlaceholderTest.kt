package io.github.lewin671.remoteagent.ui

import org.junit.Assert.assertEquals
import org.junit.Test

@org.junit.runner.RunWith(org.robolectric.RobolectricTestRunner::class)
@org.robolectric.annotation.Config(sdk = [35], application = android.app.Application::class)
class ComposerPlaceholderTest {
    @get:org.junit.Rule(order = 0) val sessionMessages = io.github.lewin671.remoteagent.ui.SessionMessageContextRule()
    @Test fun theBoxSaysOnlyWhatTheMessageWillDo() {
        assertEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.composerMessage()), composerPlaceholder(questionPending = false, planOpen = false))
        assertEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.composerQueueAfterAnswer()), composerPlaceholder(questionPending = true, planOpen = false))
        assertEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.composerPlanPlaceholder()), composerPlaceholder(questionPending = false, planOpen = true))
        assertEquals(messageTextForTest(io.github.lewin671.remoteagent.core.UiMessages.composerQueueAfterAnswer()), composerPlaceholder(questionPending = true, planOpen = true))
    }
}
