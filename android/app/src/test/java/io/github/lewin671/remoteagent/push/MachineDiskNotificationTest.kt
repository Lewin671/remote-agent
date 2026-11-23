package io.github.lewin671.remoteagent.push

import android.app.Application
import android.app.Notification
import android.app.NotificationManager
import android.content.Context
import androidx.test.core.app.ApplicationProvider
import io.github.lewin671.remoteagent.core.proto.SecurityNotifyDisplay
import io.github.lewin671.remoteagent.core.proto.Text as CoreText
import io.github.lewin671.remoteagent.core.proto.TextArg
import io.github.lewin671.remoteagent.proto.NotifyKind
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config

/** The notification of a low disk: renders Core's [SecurityNotifyDisplay] and opens that computer's 电脑状态. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class MachineDiskNotificationTest {
    @get:org.junit.Rule val resources = io.github.lewin671.remoteagent.ui.SessionMessageContextRule()
    private val context: Context get() = ApplicationProvider.getApplicationContext()

    private fun display(machine: String, sizeGb: String, critical: Boolean): SecurityNotifyDisplay {
        val sizeText = CoreText.newBuilder()
            .setKey("machine_status.size_gb")
            .addArgs(TextArg.newBuilder().setName("size").setStrValue(sizeGb))
            .build()
        return SecurityNotifyDisplay.newBuilder()
            .setValid(true)
            .setKind(NotifyKind.NOTIFY_KIND_MACHINE_DISK)
            .setMachineId(machine)
            .setTag("$machine/NOTIFY_KIND_MACHINE_DISK")
            .setCritical(critical)
            .setShowDetailedByDefault(true)
            .setTitle(
                CoreText.newBuilder()
                    .setKey(if (critical) "machine_status.notify_critical" else "machine_status.notify_warning")
                    .addArgs(TextArg.newBuilder().setName("computer").setStrValue("MacBook Pro"))
                    .addArgs(TextArg.newBuilder().setName("size").setTextValue(sizeText)),
            )
            .setBody(
                CoreText.newBuilder()
                    .setKey(if (critical) "machine_status.notify_critical_body" else "machine_status.notify_warning_body"),
            )
            .setFullGenericBody(CoreText.newBuilder().setKey("machine_status.home_warning"))
            .build()
    }

    private fun shown(): List<Notification> = shadowOf(context.getSystemService(NotificationManager::class.java)).allNotifications

    @Test fun lowDiskNotificationOpensThatComputersStatus() {
        Notifier.show(context, "", NotifyKind.NOTIFY_KIND_MACHINE_DISK, display("m-1", "8.2", false), showContent = true)
        val n = shown().single()
        assertEquals("MacBook Pro 磁盘只剩 8.2 GB", n.extras.getString(Notification.EXTRA_TITLE))
        assertEquals("空间用完后会话会启动不了。", n.extras.getString(Notification.EXTRA_TEXT))
        val intent = shadowOf(n.contentIntent).savedIntent
        assertEquals("m-1", intent.getStringExtra(Notifier.EXTRA_MACHINE_STATUS))
        // It names a computer, not a session.
        assertEquals("", intent.getStringExtra(io.github.lewin671.remoteagent.MainActivity.EXTRA_SESSION))
    }

    @Test fun criticalNotificationAndOneNotificationPerComputer() {
        Notifier.show(context, "", NotifyKind.NOTIFY_KIND_MACHINE_DISK, display("m-1", "8.2", false), showContent = true)
        Notifier.show(context, "", NotifyKind.NOTIFY_KIND_MACHINE_DISK, display("m-2", "1.2", true), showContent = true)
        Notifier.show(context, "", NotifyKind.NOTIFY_KIND_MACHINE_DISK, display("m-1", "1.9", true), showContent = true)
        val all = shown()
        // The second tier of m-1 replaces its first; m-2 keeps its own.
        assertEquals(2, all.size)
        assertEquals(setOf("m-1", "m-2"), all.map { shadowOf(it.contentIntent).savedIntent.getStringExtra(Notifier.EXTRA_MACHINE_STATUS) }.toSet())
        assertTrue(all.all { it.extras.getString(Notification.EXTRA_TITLE)!!.contains("磁盘快满了") || it.extras.getString(Notification.EXTRA_TITLE)!!.contains("磁盘只剩") })
        assertTrue(all.any { it.extras.getString(Notification.EXTRA_TEXT) == "现在清理，否则会话可能启动不了。" })
    }
}
