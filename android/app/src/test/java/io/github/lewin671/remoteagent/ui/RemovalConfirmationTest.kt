package io.github.lewin671.remoteagent.ui

import org.junit.Assert.assertTrue
import org.junit.Test

/** Removing a device is one confirmation that names it and says what is deleted. */
@org.junit.runner.RunWith(org.robolectric.RobolectricTestRunner::class)
@org.robolectric.annotation.Config(sdk = [35], application = android.app.Application::class)
class RemovalConfirmationTest {
    @get:org.junit.Rule val batchCResources = io.github.lewin671.remoteagent.ui.SessionMessageContextRule()
    @Test
    fun aClientRemovalNamesTheDeviceAndTheOneConfirmation() {
        val (title, text) = removalDialog("我的 iPad", machine = false)
        assertTrue(title.contains("我的 iPad"))
        assertTrue(text.contains("断开它的登录"))
        assertTrue(text.contains("不结束电脑上的会话"))
    }

    @Test
    fun aComputerRemovalSaysItsArchiveIsRetained() {
        val (title, text) = removalDialog("工作站", machine = true)
        assertTrue(title.contains("工作站"))
        assertTrue(text.contains("保留中继上的会话和图片"))
    }
}
