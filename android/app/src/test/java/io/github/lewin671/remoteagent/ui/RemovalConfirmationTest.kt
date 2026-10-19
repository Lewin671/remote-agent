package io.github.lewin671.remoteagent.ui

import org.junit.Assert.assertTrue
import org.junit.Test

/** Removing a device is one confirmation that names it and says what is deleted. */
class RemovalConfirmationTest {
    @Test
    fun aClientRemovalNamesTheDeviceAndTheOneConfirmation() {
        val (title, text) = removalDialog("我的 iPad", machine = false)
        assertTrue(title.contains("我的 iPad"))
        assertTrue(text.contains("断开它的登录"))
        assertTrue(text.contains("不结束电脑上的会话"))
    }

    @Test
    fun aComputerRemovalSaysItsSessionsAreDeleted() {
        val (title, text) = removalDialog("工作站", machine = true)
        assertTrue(title.contains("工作站"))
        assertTrue(text.contains("会话记录"))
    }
}
