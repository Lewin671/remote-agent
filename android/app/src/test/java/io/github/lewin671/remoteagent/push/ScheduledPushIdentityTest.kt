package io.github.lewin671.remoteagent.push

import io.github.lewin671.remoteagent.ui.messageTextForTest

import io.github.lewin671.remoteagent.core.UiMessages

import android.app.Application
import android.app.KeyguardManager
import android.app.Notification
import androidx.core.app.NotificationCompat
import android.app.NotificationManager
import io.github.lewin671.remoteagent.MainActivity
import io.github.lewin671.remoteagent.core.proto.SecurityNotifyDisplay
import io.github.lewin671.remoteagent.core.proto.Text as CoreText
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
    @get:org.junit.Rule val batchCResources = io.github.lewin671.remoteagent.ui.SessionMessageContextRule()
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
        assertEquals(messageTextForTest(UiMessages.settingsScheduledTaskResults()), manager.getNotificationChannel(Push.CHANNEL_SCHEDULED).name.toString())
        Notifier.show(context, "session", NotifyKind.NOTIFY_KIND_ATTENTION, null, false)
        Notifier.show(context, "session", NotifyKind.NOTIFY_KIND_ATTENTION, null, false)
        assertEquals("ordinary notifications retain replacement behavior", 3, shadowOf(manager).allNotifications.size)
    }
    @Test fun readingARunDismissesOnlyItsExactLatestNotification() {
        val context = RuntimeEnvironment.getApplication()
        Push.createChannels(context)
        val manager = context.getSystemService(NotificationManager::class.java)
        for ((session, seq) in listOf("session" to 7, "session" to 8, "other" to 8)) {
            Notifier.show(context, session, NotifyKind.NOTIFY_KIND_SCHEDULED, null, false, "scheduled:$seq")
        }
        Notifier.show(context, "session", NotifyKind.NOTIFY_KIND_ATTENTION, null, false)
        fun ref(sessionId: String, seq: Long) =
            io.github.lewin671.remoteagent.core.proto.ScheduleNotificationRef.newBuilder()
                .setSessionId(sessionId)
                .setNotificationSeq(seq)
                .build()
        Notifier.dismissScheduled(context, listOf(ref("session", 8)))
        assertEquals(3, shadowOf(manager).allNotifications.size)
        assertTrue(manager.activeNotifications.none { it.tag == "scheduled/session/scheduled:8" })
        assertTrue(manager.activeNotifications.any { it.tag == "scheduled/other/scheduled:8" })
        // Missing sequence numbers must never cancel a guessed event or an ordinary notification.
        Notifier.dismissScheduled(context, listOf(ref("session", 0)))
        assertEquals(3, shadowOf(manager).allNotifications.size)
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
            val display = SecurityNotifyDisplay.newBuilder()
                .setValid(true)
                .setKind(NotifyKind.NOTIFY_KIND_SCHEDULED)
                .setSessionId("session")
                .setShowDetailedByDefault(true)
                .setTitle(CoreText.newBuilder().setLiteral("event-$seq"))
                .setBody(CoreText.newBuilder().setLiteral("body-$seq"))
                .setGenericBody(CoreText.newBuilder().setKey("notifications.scheduled_tasks_have_new_results"))
                .setSourceEventSeq(seq)
                .build()
            Notifier.show(context, "session", NotifyKind.NOTIFY_KIND_SCHEDULED, display, true, "scheduled:$seq")
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
            val display = SecurityNotifyDisplay.newBuilder()
                .setValid(true)
                .setKind(NotifyKind.NOTIFY_KIND_SCHEDULED)
                .setSessionId(session)
                .setShowDetailedByDefault(true)
                .setTitle(CoreText.newBuilder().setLiteral(session))
                .setBody(CoreText.newBuilder().setLiteral("body"))
                .setGenericBody(CoreText.newBuilder().setKey("notifications.scheduled_tasks_have_new_results"))
                .setSourceEventSeq(7)
                .build()
            Notifier.show(context, session, NotifyKind.NOTIFY_KIND_SCHEDULED, display, true, "scheduled:7")
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
        val display = SecurityNotifyDisplay.newBuilder()
            .setValid(true)
            .setKind(NotifyKind.NOTIFY_KIND_SCHEDULED)
            .setSessionId("session")
            .setShowDetailedByDefault(true)
            .setTitle(CoreText.newBuilder().setLiteral("任务名来自签名内容"))
            .setBody(CoreText.newBuilder().setLiteral("通知正文"))
            .setGenericBody(CoreText.newBuilder().setKey("notifications.scheduled_tasks_have_new_results"))
            .setSourceEventSeq(7)
            .build()
        Notifier.show(context, "session", NotifyKind.NOTIFY_KIND_SCHEDULED, display, false, "scheduled:7")
        val n = shadowOf(manager).allNotifications.single()
        assertEquals("任务名来自签名内容", n.extras.getCharSequence("android.title").toString())
        assertEquals("通知正文", n.extras.getCharSequence("android.text").toString())
        assertEquals(NotificationCompat.VISIBILITY_SECRET, n.visibility)
        assertEquals(messageTextForTest(UiMessages.notificationsScheduledTasksHaveNewResults()), n.publicVersion.extras.getCharSequence(Notification.EXTRA_TEXT).toString())
        assertTrue(n.actions.isNullOrEmpty())
        assertEquals(7L, shadowOf(n.contentIntent).savedIntent.getLongExtra(Notifier.EXTRA_SCHEDULE_SEQ, 0))
    }

    @Test fun invalidOrRelabelledDisplayStaysGeneric() {
        val context = RuntimeEnvironment.getApplication()
        Push.createChannels(context)
        val manager = context.getSystemService(NotificationManager::class.java)
        val invalid = SecurityNotifyDisplay.newBuilder()
            .setValid(false)
            .setKind(NotifyKind.NOTIFY_KIND_SCHEDULED)
            .setTitle(CoreText.newBuilder().setLiteral("Private session"))
            .setBody(CoreText.newBuilder().setLiteral("cat secret"))
            .setGenericBody(CoreText.newBuilder().setKey("notifications.scheduled_tasks_have_new_results"))
            .setSourceEventSeq(7)
            .build()
        Notifier.show(context, "session", NotifyKind.NOTIFY_KIND_SCHEDULED, invalid, true, "scheduled:7")
        val n = shadowOf(manager).allNotifications.single()
        assertEquals("remote-agent", n.extras.getCharSequence("android.title").toString())
        assertEquals(messageTextForTest(UiMessages.notificationsScheduledTasksHaveNewResults()), n.extras.getCharSequence("android.text").toString())
    }

    @Test fun lockedScheduledPushIsGenericEvenWhenContentIsEnabled() {
        val context = RuntimeEnvironment.getApplication()
        Push.createChannels(context)
        shadowOf(context.getSystemService(KeyguardManager::class.java)).setIsDeviceLocked(true)
        val manager = context.getSystemService(NotificationManager::class.java)
        val display = SecurityNotifyDisplay.newBuilder()
            .setValid(true)
            .setKind(NotifyKind.NOTIFY_KIND_SCHEDULED)
            .setSessionId("session")
            .setShowDetailedByDefault(true)
            .setTitle(CoreText.newBuilder().setLiteral("Private task"))
            .setBody(CoreText.newBuilder().setLiteral("Private summary"))
            .setGenericBody(CoreText.newBuilder().setKey("notifications.scheduled_tasks_have_new_results"))
            .setSourceEventSeq(7)
            .build()
        for (showContent in listOf(false, true)) {
            Notifier.show(context, "session", NotifyKind.NOTIFY_KIND_SCHEDULED, display, showContent, "scheduled:7")
            val n = shadowOf(manager).allNotifications.single()
            assertEquals("remote-agent", n.extras.getCharSequence(Notification.EXTRA_TITLE).toString())
            assertEquals(messageTextForTest(UiMessages.notificationsScheduledTasksHaveNewResults()), n.extras.getCharSequence(Notification.EXTRA_TEXT).toString())
            assertEquals(NotificationCompat.VISIBILITY_PRIVATE, n.visibility)
            assertEquals(messageTextForTest(UiMessages.notificationsScheduledTasksHaveNewResults()), n.publicVersion.extras.getCharSequence(Notification.EXTRA_TEXT).toString())
        }
    }

}
