package io.github.lewin671.remoteagent.push

import android.app.Application
import android.app.KeyguardManager
import android.app.Notification
import android.app.NotificationManager
import io.github.lewin671.remoteagent.core.UiMessages
import io.github.lewin671.remoteagent.proto.NotifyContent
import io.github.lewin671.remoteagent.proto.NotifyKind
import io.github.lewin671.remoteagent.ui.messageTextForTest
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class TurnEndedContentTest {
    @get:org.junit.Rule val resources = io.github.lewin671.remoteagent.ui.SessionMessageContextRule()

    private fun show(text: String, count: Int?, locale: String = "zh-CN", showContent: Boolean = true): Notification {
        val context = RuntimeEnvironment.getApplication()
        Push.createChannels(context)
        val content = NotifyContent.newBuilder().setKind(NotifyKind.NOTIFY_KIND_TURN_ENDED)
            .setTitle("Session").setText(text)
        if (count != null) content.setSharedFileCount(count)
        // Exercise the wire field instead of supplying a separate display argument.
        val decoded = NotifyContent.parseFrom(content.build().toByteArray())
        Notifier.show(context, "session", NotifyKind.NOTIFY_KIND_TURN_ENDED, decoded, showContent,
            copy = { messageTextForTest(it, locale) })
        return shadowOf(context.getSystemService(NotificationManager::class.java)).allNotifications.last()
    }

    @Test fun missingOrZeroCountKeepsExistingBody() {
        for (count in listOf(null, 0)) {
            assertEquals("Summary", show("Summary", count).extras.getString(Notification.EXTRA_TEXT))
            assertEquals(messageTextForTest(UiMessages.notificationsAndroidAConversationTurnFinished()),
                show("", count).extras.getString(Notification.EXTRA_TEXT))
        }
    }

    @Test fun positiveCountUsesOneSpaceAndEmptyBodyUsesOnlySuffix() {
        assertEquals("Summary 附 1 个文件", show("Summary", 1).extras.getString(Notification.EXTRA_TEXT))
        assertEquals("附 2 个文件", show("", 2).extras.getString(Notification.EXTRA_TEXT))
        assertEquals("Summary 1 file attached", show("Summary", 1, "en").extras.getString(Notification.EXTRA_TEXT))
        assertEquals("2 files attached", show("", 2, "en").extras.getString(Notification.EXTRA_TEXT))
    }

    @Test fun hiddenContentAndLockScreenKeepGenericBody() {
        val generic = messageTextForTest(UiMessages.notificationsAndroidAConversationTurnFinished())
        val detailed = show("Summary", 2)
        assertEquals(generic, detailed.publicVersion.extras.getString(Notification.EXTRA_TEXT))
        assertEquals(generic, show("Summary", 2, showContent = false).extras.getString(Notification.EXTRA_TEXT))
        val keyguard = RuntimeEnvironment.getApplication().getSystemService(KeyguardManager::class.java)
        shadowOf(keyguard).setIsDeviceLocked(true)
        assertEquals(generic, show("Summary", 2).extras.getString(Notification.EXTRA_TEXT))
    }
}
