package io.github.lewin671.remoteagent.ui

import org.junit.Assert.assertEquals
import org.junit.Test

class ComposerPlaceholderTest {
    @Test fun theBoxSaysOnlyWhatTheMessageWillDo() {
        assertEquals("消息", composerPlaceholder(questionPending = false, planOpen = false))
        assertEquals("排队发送，回答后继续", composerPlaceholder(questionPending = true, planOpen = false))
        assertEquals("补充你对计划的要求…", composerPlaceholder(questionPending = false, planOpen = true))
        assertEquals("排队发送，回答后继续", composerPlaceholder(questionPending = true, planOpen = true))
    }
}
