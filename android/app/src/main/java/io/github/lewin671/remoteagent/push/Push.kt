package io.github.lewin671.remoteagent.push

import android.Manifest
import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.provider.Settings
import android.util.Log
import androidx.core.content.ContextCompat
import com.google.firebase.FirebaseApp
import com.google.firebase.messaging.FirebaseMessaging
import io.github.lewin671.remoteagent.BuildConfig
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.errorText
import io.github.lewin671.remoteagent.core.value
import io.github.lewin671.remoteagent.proto.NotifyKind
import io.github.lewin671.remoteagent.proto.PushType
import io.github.lewin671.remoteagent.proto.SetPushTargetRequest
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlin.coroutines.resume
import kotlin.coroutines.resumeWithException

/** Whether push works, and what the user can do about it. */
data class PushStatus(val problem: String = "", val action: String = "")

/**
 * Push notifications through FCM (architecture §15). The app registers its
 * FCM token with the server; pushes carry the daemon's content sealed with
 * the notify key, decrypted here in the background. Without a Firebase
 * configuration in the build, push is off and the app says so.
 */
object Push {
    const val CHANNEL_ATTENTION = "attention"
    const val CHANNEL_TURN = "turn"
    private const val TAG = "remote-agent"

    private val _status = MutableStateFlow(PushStatus())
    val status: StateFlow<PushStatus> = _status

    fun createChannels(context: Context) {
        val nm = context.getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(NotificationChannel(CHANNEL_ATTENTION, "等我处理时", NotificationManager.IMPORTANCE_HIGH).apply {
            description = "审批、提问、计划和终端弹窗"
            lockscreenVisibility = android.app.Notification.VISIBILITY_PRIVATE
        })
        nm.createNotificationChannel(NotificationChannel(CHANNEL_TURN, "一轮结束时", NotificationManager.IMPORTANCE_DEFAULT).apply {
            lockscreenVisibility = android.app.Notification.VISIBILITY_PRIVATE
        })
    }

    fun available(context: Context): Boolean = BuildConfig.PUSH_ENABLED && FirebaseApp.getApps(context).isNotEmpty()

    fun permitted(context: Context): Boolean =
        Build.VERSION.SDK_INT < 33 || ContextCompat.checkSelfPermission(context, Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED

    private suspend fun fcmToken(): String = suspendCancellableCoroutine { c ->
        FirebaseMessaging.getInstance().token.addOnCompleteListener { t ->
            if (t.isSuccessful) c.resume(t.result) else c.resumeWithException(t.exception ?: Exception("FCM token unavailable"))
        }
    }

    /** Registers this device's FCM token and the kinds it wants; safe to call often. */
    suspend fun register(context: Context, remote: Remote, token: String? = null) {
        if (!available(context)) {
            _status.value = PushStatus("这个版本没有配置推送，审批来了不会提醒你")
            return
        }
        if (!permitted(context)) {
            _status.value = PushStatus("推送没开，审批来了不会提醒你", "开启")
            return
        }
        try {
            val fcm = token ?: fcmToken()
            val s = RaApp.instance.prefs.settings.first()
            val kinds = buildList {
                if (s.notifyAttention) add(NotifyKind.NOTIFY_KIND_ATTENTION)
                if (s.notifyTurnEnded) add(NotifyKind.NOTIFY_KIND_TURN_ENDED)
            }
            val res = remote.api.client.setPushTarget(
                SetPushTargetRequest.newBuilder().setType(PushType.PUSH_TYPE_FCM).setTarget(if (kinds.isEmpty()) "" else fcm).addAllKinds(kinds).build(),
                Api.headers(remote.currentToken()),
            ).value()
            _status.value = if (res.enabled || kinds.isEmpty()) PushStatus() else PushStatus("服务器没有配置 FCM，收不到推送")
            // Only the length: the token identifies this phone to FCM.
            Log.i(TAG, "push registered, token length ${fcm.length}")
        } catch (e: Exception) {
            _status.value = PushStatus("订阅失败：${errorText(e)}", "重试")
        }
    }

    /** What the banner's button does. */
    fun fix(context: Context, remote: Remote) {
        if (!permitted(context)) {
            context.startActivity(
                Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS).putExtra(Settings.EXTRA_APP_PACKAGE, context.packageName)
                    .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
            )
        } else {
            RaApp.instance.scope.launch { register(context, remote) }
        }
    }
}
