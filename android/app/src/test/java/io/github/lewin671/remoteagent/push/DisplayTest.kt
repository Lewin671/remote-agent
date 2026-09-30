package io.github.lewin671.remoteagent.push

import androidx.core.app.NotificationCompat
import org.junit.Assert.assertEquals
import org.junit.Test

class DisplayTest {
    @Test
    fun contentOnlyWhenTurnedOnAndUnlocked() {
        assertEquals(Notifier.Display(false, false, NotificationCompat.VISIBILITY_PRIVATE), Notifier.display(false, true, false, 35))
        // Locked: generic, whatever the lock screen settings.
        assertEquals(Notifier.Display(false, false, NotificationCompat.VISIBILITY_PRIVATE), Notifier.display(true, true, true, 35))
        // Unlocked: content, hidden from the lock screen altogether.
        assertEquals(Notifier.Display(true, true, NotificationCompat.VISIBILITY_SECRET), Notifier.display(true, true, false, 35))
        // Below API 31 buttons cannot require unlocking, so there are none.
        assertEquals(Notifier.Display(true, false, NotificationCompat.VISIBILITY_SECRET), Notifier.display(true, true, false, 30))
    }

    @Test
    fun namesApprovalsAsTheWebDoes() {
        val cmd = io.github.lewin671.remoteagent.proto.NotifyApproval.newBuilder().setToolName("Bash").setCommand("go test ./...").build()
        assertEquals("请求执行" to "go test ./...", Notifier.approvalText(cmd))
        val write = io.github.lewin671.remoteagent.proto.NotifyApproval.newBuilder().setToolName("Write").setToolInput("{\"file_path\":\"/tmp/a\"}").build()
        assertEquals("请求权限 · Write" to "{\"file_path\":\"/tmp/a\"}", Notifier.approvalText(write))
        // From a daemon that still put the input in command.
        val old = io.github.lewin671.remoteagent.proto.NotifyApproval.newBuilder().setToolName("Write").setCommand("{\"file_path\":\"/tmp/a\"}").build()
        assertEquals("请求权限 · Write", Notifier.approvalText(old).first)
    }
}
