package io.github.lewin671.remoteagent.push

import android.app.Application
import android.app.NotificationManager
import io.github.lewin671.remoteagent.proto.NotifyKind
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class ScheduledPushIdentityTest {
    @Test
    fun scheduledResultsCoexistAndRetriesReplaceOnlyTheirOwnEvent() {
        val context = RuntimeEnvironment.getApplication()
        Push.createChannels(context)
        val manager = context.getSystemService(NotificationManager::class.java)
        for (event in listOf("scheduled:7", "scheduled:8", "scheduled:7")) {
            Notifier.show(context, "session", NotifyKind.NOTIFY_KIND_SCHEDULED, null, false, event)
        }
        assertEquals(2, shadowOf(manager).allNotifications.size)
        assertTrue(shadowOf(manager).allNotifications.all { it.channelId == Push.CHANNEL_SCHEDULED })
        assertEquals("定时任务结果", manager.getNotificationChannel(Push.CHANNEL_SCHEDULED).name.toString())
        Notifier.show(context, "session", NotifyKind.NOTIFY_KIND_ATTENTION, null, false)
        Notifier.show(context, "session", NotifyKind.NOTIFY_KIND_ATTENTION, null, false)
        assertEquals("ordinary notifications retain replacement behavior", 3, shadowOf(manager).allNotifications.size)
    }
    @Test fun scheduledPushShowsTheSignedDaemonTitleAndPreservesItsEventTarget() {
        val context = RuntimeEnvironment.getApplication()
        Push.createChannels(context)
        val manager = context.getSystemService(NotificationManager::class.java)
        val content = io.github.lewin671.remoteagent.proto.NotifyContent.newBuilder().setTitle("任务名来自签名内容").setText("通知正文").setSourceEventSeq(7).build()
        Notifier.show(context,"session",NotifyKind.NOTIFY_KIND_SCHEDULED,content,true,"scheduled:7")
        val n = shadowOf(manager).allNotifications.single()
        assertEquals("任务名来自签名内容", n.extras.getCharSequence("android.title").toString())
        assertEquals("通知正文", n.extras.getCharSequence("android.text").toString())
        assertEquals(7L, shadowOf(n.contentIntent).savedIntent.getLongExtra(Notifier.EXTRA_SCHEDULE_SEQ,0))
    }

}
