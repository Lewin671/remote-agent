package io.github.lewin671.remoteagent.push

import android.app.Application
import android.app.Notification
import android.app.NotificationManager
import android.content.Context
import androidx.test.core.app.ApplicationProvider
import io.github.lewin671.remoteagent.proto.DiskStatus
import io.github.lewin671.remoteagent.proto.NotifyContent
import io.github.lewin671.remoteagent.proto.NotifyKind
import io.github.lewin671.remoteagent.proto.ResourcePressure
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config

/** The notification of a low disk: the computer's name and amount in the words, and a tap that opens that computer's 电脑状态. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class MachineDiskNotificationTest {
    @get:org.junit.Rule val resources = io.github.lewin671.remoteagent.ui.SessionMessageContextRule()
    private val context: Context get() = ApplicationProvider.getApplicationContext()

    private fun content(machine: String, avail: Long, critical: Boolean) = NotifyContent.newBuilder().setKind(NotifyKind.NOTIFY_KIND_MACHINE_DISK).setMachineId(machine).setMachineName("MacBook Pro")
        .setDiskStatus(DiskStatus.newBuilder().setAvailableBytes(avail).setTotalBytes(460_000_000_000)
            .setPressure(if (critical) ResourcePressure.RESOURCE_PRESSURE_CRITICAL else ResourcePressure.RESOURCE_PRESSURE_WARNING)).build()

    private fun shown(): List<Notification> = shadowOf(context.getSystemService(NotificationManager::class.java)).allNotifications

    @Test fun lowDiskNotificationOpensThatComputersStatus() {
        Notifier.show(context, "", NotifyKind.NOTIFY_KIND_MACHINE_DISK, content("m-1", 8_200_000_000, false), showContent = true)
        val n = shown().single()
        assertEquals("MacBook Pro 磁盘只剩 8.2 GB", n.extras.getString(Notification.EXTRA_TITLE))
        assertEquals("空间用完后会话会启动不了。", n.extras.getString(Notification.EXTRA_TEXT))
        val intent = shadowOf(n.contentIntent).savedIntent
        assertEquals("m-1", intent.getStringExtra(Notifier.EXTRA_MACHINE_STATUS))
        // It names a computer, not a session.
        assertEquals("", intent.getStringExtra(io.github.lewin671.remoteagent.MainActivity.EXTRA_SESSION))
    }

    @Test fun criticalNotificationAndOneNotificationPerComputer() {
        Notifier.show(context, "", NotifyKind.NOTIFY_KIND_MACHINE_DISK, content("m-1", 8_200_000_000, false), showContent = true)
        Notifier.show(context, "", NotifyKind.NOTIFY_KIND_MACHINE_DISK, content("m-2", 1_200_000_000, true), showContent = true)
        Notifier.show(context, "", NotifyKind.NOTIFY_KIND_MACHINE_DISK, content("m-1", 1_900_000_000, true), showContent = true)
        val all = shown()
        // The second tier of m-1 replaces its first; m-2 keeps its own.
        assertEquals(2, all.size)
        assertEquals(setOf("m-1", "m-2"), all.map { shadowOf(it.contentIntent).savedIntent.getStringExtra(Notifier.EXTRA_MACHINE_STATUS) }.toSet())
        assertTrue(all.all { it.extras.getString(Notification.EXTRA_TITLE)!!.contains("磁盘快满了") || it.extras.getString(Notification.EXTRA_TITLE)!!.contains("磁盘只剩") })
        assertTrue(all.any { it.extras.getString(Notification.EXTRA_TEXT) == "现在清理，否则会话可能启动不了。" })
    }
}
