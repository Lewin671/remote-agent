package io.github.lewin671.remoteagent.ui

import android.app.Application
import io.github.lewin671.remoteagent.core.CommandException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class MachineStatusLogicTest {
    @get:org.junit.Rule val resources = SessionMessageContextRule()

    @Test fun anActionRemembersWhenTheDiskWasFull() {
        val action = Action(CoroutineScope(Dispatchers.Unconfined))
        action.run({ throw CommandException("disk_full", "mkdir /Users/x/.state: no space left on device") })
        assertTrue(action.diskFull)
        // Said in plain words; the raw error never reaches the screen.
        assertEquals("这台电脑磁盘满了", action.error)
        action.run({ throw CommandException("storage_exhausted", "write: quota") })
        assertFalse(action.diskFull)
        assertEquals("存储配额已用尽，或写入目标的空间不足", action.error)
        action.run({ throw CommandException("internal", "boom") })
        assertFalse(action.diskFull)
    }

    @Test fun otherFailuresKeepTheirPrefixAroundThePlainWords() {
        val e = CommandException("disk_full", "no space left on device")
        assertEquals("启动失败：这台电脑磁盘满了", sessionText(io.github.lewin671.remoteagent.core.UiMessages.newSessionStartFailed(reason = displayError(e))))
    }
}
