package io.github.lewin671.remoteagent.push

import android.app.Application
import android.app.NotificationManager
import android.content.Context
import android.content.ContextWrapper
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleRegistry
import androidx.lifecycle.ProcessLifecycleOwner
import com.google.firebase.messaging.RemoteMessage
import io.github.lewin671.remoteagent.MainActivity
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.proto.NotifyKind
import java.io.File
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class ForegroundPushTest {
    @Test
    fun scheduledPushBypassesForegroundUnlessItsSessionIsVisible() = runBlocking {
        val context = RuntimeEnvironment.getApplication()
        val app = RaApp()
        ContextWrapper::class.java.getDeclaredMethod("attachBaseContext", Context::class.java)
            .apply { isAccessible = true }.invoke(app, context)
        RaApp::class.java.getDeclaredField("instance").apply { isAccessible = true }.set(null, app)
        // Only account presence is needed for a generic push, not any account keys.
        val account = File(context.noBackupFilesDir, "account-v2.bin")
        account.writeBytes(byteArrayOf(0))
        app.prefs.update { it.copy(notifyAttention = true, notifyTurnEnded = true) }
        Push.createChannels(context)
        val service = Robolectric.buildService(RaMessagingService::class.java).create().get()
        val manager = context.getSystemService(NotificationManager::class.java)
        val process = ProcessLifecycleOwner.get().lifecycle as LifecycleRegistry
        try {
            for (foreground in listOf(false, true)) {
                process.currentState = if (foreground) Lifecycle.State.STARTED else Lifecycle.State.CREATED
                for (visible in listOf(null, "target-session", "other-session")) {
                    app.visibleSession = visible
                    for (kind in listOf(NotifyKind.NOTIFY_KIND_SCHEDULED, NotifyKind.NOTIFY_KIND_ATTENTION, NotifyKind.NOTIFY_KIND_TURN_ENDED, NotifyKind.NOTIFY_KIND_SECURITY)) {
                        manager.cancelAll()
                        val message = RemoteMessage.Builder("synthetic-sender")
                            .setData(mapOf("session" to "target-session", "kind" to kind.number.toString()))
                            .build()
                        service.onMessageReceived(message)
                        val expected = !foreground || kind == NotifyKind.NOTIFY_KIND_SCHEDULED && visible != "target-session"
                        val notifications = shadowOf(manager).allNotifications
                        assertEquals("$kind foreground=$foreground visible=$visible", if (expected) 1 else 0, notifications.size)
                        if (expected && kind == NotifyKind.NOTIFY_KIND_SCHEDULED) {
                            assertEquals("定时任务有新结果", notifications.single().extras.getCharSequence("android.text"))
                            assertEquals("target-session", shadowOf(notifications.single().contentIntent).savedIntent.getStringExtra(MainActivity.EXTRA_SESSION))
                        }
                    }
                }
            }
        } finally {
            app.visibleSession = null
            process.currentState = Lifecycle.State.CREATED
            account.delete()
            manager.cancelAll()
            service.onDestroy()
        }
    }
}
