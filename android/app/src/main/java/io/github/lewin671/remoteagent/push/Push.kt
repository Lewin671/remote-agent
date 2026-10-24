package io.github.lewin671.remoteagent.push

import android.Manifest
import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.provider.Settings
import android.util.Log
import androidx.core.content.ContextCompat
import com.google.android.gms.common.ConnectionResult
import com.google.android.gms.common.GoogleApiAvailabilityLight
import com.google.firebase.FirebaseApp
import com.google.firebase.messaging.FirebaseMessaging
import io.github.lewin671.remoteagent.R
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.Api
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.value
import io.github.lewin671.remoteagent.proto.NotifyKind
import io.github.lewin671.remoteagent.proto.PushType
import io.github.lewin671.remoteagent.proto.SetPushTargetRequest
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlin.coroutines.resume
import kotlin.coroutines.resumeWithException

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
    private val registration by lazy { PushRegistration(RaApp.instance.scope) }

    fun stop() = registration.stop()

    /** The account is leaving: nothing registers until the next one is in use ([open]). */
    fun close() = registration.close()

    fun open() = registration.open()

    /** Withdraws the old account's subscription without waiting for a Google token. */
    suspend fun unregister(remote: Remote) {
        kotlinx.coroutines.withTimeoutOrNull(5_000) {
            try {
                remote.api.client.setPushTarget(
                    SetPushTargetRequest.newBuilder().setType(PushType.PUSH_TYPE_FCM).build(),
                    Api.headers(remote.currentToken()),
                ).value()
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                Log.w(TAG, "push withdrawal failed: ${e.javaClass.simpleName}")
            }
        }
    }

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

    fun available(context: Context): Boolean = context.resources.getBoolean(R.bool.push_enabled) && FirebaseApp.getApps(context).isNotEmpty()

    fun permitted(context: Context): Boolean =
        Build.VERSION.SDK_INT < 33 || ContextCompat.checkSelfPermission(context, Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED

    private suspend fun fcmToken(): String = suspendCancellableCoroutine { c ->
        FirebaseMessaging.getInstance().token.addOnCompleteListener { t ->
            if (!c.isActive) return@addOnCompleteListener
            if (t.isSuccessful) c.resume(t.result) else c.resumeWithException(t.exception ?: Exception("FCM token unavailable"))
        }
    }

    /** Registers this device's FCM token and the kinds it wants; safe to call often. */
    fun register(context: Context, remote: Remote, token: String? = null, autoRetry: Boolean = true) {
        registration.request({ attempt(context, remote, token, autoRetry) }, { _status.value = it }, autoRetry)
    }

    private suspend fun attempt(context: Context, remote: Remote, token: String?, autoRetry: Boolean): PushAttempt {
        try {
            if (!available(context)) {
                return PushAttempt(PushStatus("本版本未启用后台推送"))
            }
            val s = RaApp.instance.prefs.settings.first()
            val kinds = buildList {
                if (s.notifyAttention) add(NotifyKind.NOTIFY_KIND_ATTENTION)
                if (s.notifyTurnEnded) add(NotifyKind.NOTIFY_KIND_TURN_ENDED)
                add(NotifyKind.NOTIFY_KIND_SECURITY)
                add(NotifyKind.NOTIFY_KIND_SCHEDULED)
            }
            // Turning notifications off must not depend on obtaining a Google token.
            val fcm = if (kinds.isEmpty()) "" else {
                val gms = GoogleApiAvailabilityLight.getInstance().isGooglePlayServicesAvailable(context)
                if (gms != ConnectionResult.SUCCESS) {
                    val problem = when (gms) {
                        ConnectionResult.SERVICE_MISSING -> "未安装 Google Play 服务，收不到推送"
                        ConnectionResult.SERVICE_DISABLED -> "Google Play 服务已停用，收不到推送"
                        ConnectionResult.SERVICE_VERSION_UPDATE_REQUIRED -> "Google Play 服务需要更新，收不到推送"
                        ConnectionResult.SERVICE_UPDATING -> "Google Play 服务正在更新，后台提醒暂不可用"
                        else -> "Google Play 服务不可用，收不到推送"
                    }
                    return PushAttempt(PushStatus(problem, "修复"), retry = gms == ConnectionResult.SERVICE_UPDATING)
                }
                if (!permitted(context)) {
                    return PushAttempt(PushStatus("系统通知权限未开启", "开启"))
                }
                try {
                    token ?: fcmToken()
                } catch (e: CancellationException) {
                    throw e
                } catch (e: Exception) {
                    return tokenFailure(e)
                }
            }
            val res = remote.api.client.setPushTarget(
                SetPushTargetRequest.newBuilder().setType(PushType.PUSH_TYPE_FCM).setTarget(if (kinds.isEmpty()) "" else fcm).addAllKinds(kinds).build(),
                Api.headers(remote.currentToken()),
            ).value()
            val status = if (res.enabled || kinds.isEmpty()) PushStatus() else PushStatus("服务器没有配置 FCM，收不到推送")
            // Only the length: the token identifies this phone to FCM.
            Log.i(TAG, "push registered, token length ${fcm.length}")
            return PushAttempt(status)
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            val retry = e is java.io.IOException || e is com.connectrpc.ConnectException &&
                e.code in setOf(com.connectrpc.Code.UNKNOWN, com.connectrpc.Code.UNAVAILABLE, com.connectrpc.Code.DEADLINE_EXCEEDED)
            val problem = when {
                retry && autoRetry -> "通知订阅未能同步到服务器，正在自动重试"
                retry -> "通知订阅未能同步到服务器，请重试"
                else -> "服务器未能保存通知订阅，请重试"
            }
            return PushAttempt(PushStatus(problem, "重试"), retry)
        }
    }

    /** What the banner's button does. */
    fun fix(context: Context, remote: Remote) {
        val gms = GoogleApiAvailabilityLight.getInstance()
        val code = gms.isGooglePlayServicesAvailable(context)
        if (available(context) && code != ConnectionResult.SUCCESS) {
            val intent = if (gms.isUserResolvableError(code)) gms.getErrorResolutionIntent(context, code, null) else null
            try {
                if (intent != null) context.startActivity(intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
                else register(context, remote)
            } catch (e: ActivityNotFoundException) {
                _status.value = PushStatus("请在系统设置中安装、更新或启用 Google Play 服务，再重试", "重试")
            }
        } else if (!permitted(context)) {
            context.startActivity(
                Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS).putExtra(Settings.EXTRA_APP_PACKAGE, context.packageName)
                    .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
            )
        } else {
            register(context, remote)
        }
    }
}
