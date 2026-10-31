package io.github.lewin671.remoteagent.push

import android.app.Application
import android.app.KeyguardManager
import android.app.Notification
import androidx.core.app.NotificationCompat
import android.app.NotificationManager
import io.github.lewin671.remoteagent.MainActivity
import io.github.lewin671.remoteagent.proto.NotifyContent
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
    @Test fun collidingNumericIdsKeepBothEventsAndTheirOwnTapTargets() {
        val context = RuntimeEnvironment.getApplication()
        Push.createChannels(context)
        val manager = context.getSystemService(NotificationManager::class.java)
        // Valid decimal event sequences with equal Java hashes, not invented key formats.
        val first = 720232296L
        val second = 368856997L
        assertEquals(Notifier.id("session", "scheduled:$first"), Notifier.id("session", "scheduled:$second"))
        for (seq in listOf(first, second, first)) {
            val content = NotifyContent.newBuilder().setKind(NotifyKind.NOTIFY_KIND_SCHEDULED)
                .setTitle("event-$seq").setSourceEventSeq(seq).build()
            Notifier.show(context, "session", NotifyKind.NOTIFY_KIND_SCHEDULED, content, true, "scheduled:$seq")
        }
        val notifications = shadowOf(manager).allNotifications
        assertEquals("different events coexist and retry replaces only itself", 2, notifications.size)
        for (notification in notifications) {
            val expected = notification.extras.getCharSequence("android.title").toString().removePrefix("event-").toLong()
            assertEquals(expected, shadowOf(notification.contentIntent).savedIntent.getLongExtra(Notifier.EXTRA_SCHEDULE_SEQ, 0))
        }
    }

    @Test fun equalEventSequencesInDifferentSessionsKeepTheirOwnTapTargets() {
        val context = RuntimeEnvironment.getApplication()
        Push.createChannels(context)
        val manager = context.getSystemService(NotificationManager::class.java)
        for (session in listOf("first-session", "second-session")) {
            val content = NotifyContent.newBuilder().setKind(NotifyKind.NOTIFY_KIND_SCHEDULED)
                .setTitle(session).setSourceEventSeq(7).build()
            Notifier.show(context, session, NotifyKind.NOTIFY_KIND_SCHEDULED, content, true, "scheduled:7")
        }
        val notifications = shadowOf(manager).allNotifications
        assertEquals(2, notifications.size)
        for (notification in notifications) {
            assertEquals(notification.extras.getCharSequence("android.title").toString(),
                shadowOf(notification.contentIntent).savedIntent.getStringExtra(MainActivity.EXTRA_SESSION))
        }
    }

    @Test fun scheduledPushShowsTheSignedDaemonTitleAndPreservesItsEventTarget() {
        val context = RuntimeEnvironment.getApplication()
        Push.createChannels(context)
        val manager = context.getSystemService(NotificationManager::class.java)
        val content = NotifyContent.newBuilder().setKind(NotifyKind.NOTIFY_KIND_SCHEDULED).setTitle("任务名来自签名内容").setText("通知正文").setSourceEventSeq(7).build()
        Notifier.show(context,"session",NotifyKind.NOTIFY_KIND_SCHEDULED,content,false,"scheduled:7")
        val n = shadowOf(manager).allNotifications.single()
        assertEquals("任务名来自签名内容", n.extras.getCharSequence("android.title").toString())
        assertEquals("通知正文", n.extras.getCharSequence("android.text").toString())
        assertEquals(NotificationCompat.VISIBILITY_SECRET, n.visibility)
        assertEquals("定时任务有新结果", n.publicVersion.extras.getCharSequence(Notification.EXTRA_TEXT).toString())
        assertTrue(n.actions.isNullOrEmpty())
        assertEquals(7L, shadowOf(n.contentIntent).savedIntent.getLongExtra(Notifier.EXTRA_SCHEDULE_SEQ,0))
    }

    @Test fun approvalRelabelledAsScheduledStaysGenericWithoutContentSetting() {
        val context = RuntimeEnvironment.getApplication()
        Push.createChannels(context)
        val manager = context.getSystemService(NotificationManager::class.java)
        val content = NotifyContent.newBuilder().setKind(NotifyKind.NOTIFY_KIND_ATTENTION).setTitle("Private session")
            .setSourceEventSeq(7).setApproval(io.github.lewin671.remoteagent.proto.NotifyApproval.newBuilder()
                .setApprovalId("approval").setCommand("cat secret")).build()
        Notifier.show(context, "session", NotifyKind.NOTIFY_KIND_SCHEDULED, content, false, "scheduled:7")
        val n = shadowOf(manager).allNotifications.single()
        assertEquals("remote-agent", n.extras.getCharSequence("android.title").toString())
        assertEquals("定时任务有新结果", n.extras.getCharSequence("android.text").toString())
    }

    @Test fun lockedScheduledPushIsGenericEvenWhenContentIsEnabled() {
        val context = RuntimeEnvironment.getApplication()
        Push.createChannels(context)
        shadowOf(context.getSystemService(KeyguardManager::class.java)).setIsDeviceLocked(true)
        val manager = context.getSystemService(NotificationManager::class.java)
        val content = NotifyContent.newBuilder().setKind(NotifyKind.NOTIFY_KIND_SCHEDULED).setTitle("Private task").setText("Private summary").setSourceEventSeq(7).build()
        for (showContent in listOf(false, true)) {
            Notifier.show(context, "session", NotifyKind.NOTIFY_KIND_SCHEDULED, content, showContent, "scheduled:7")
            val n = shadowOf(manager).allNotifications.single()
            assertEquals("remote-agent", n.extras.getCharSequence(Notification.EXTRA_TITLE).toString())
            assertEquals("定时任务有新结果", n.extras.getCharSequence(Notification.EXTRA_TEXT).toString())
            assertEquals(NotificationCompat.VISIBILITY_PRIVATE, n.visibility)
            assertEquals("定时任务有新结果", n.publicVersion.extras.getCharSequence(Notification.EXTRA_TEXT).toString())
        }
    }

}
