package io.github.lewin671.remoteagent.push

import android.app.KeyguardManager
import android.app.Notification
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.os.Build
import android.os.SystemClock
import android.util.Base64
import android.util.Log
import androidx.core.app.NotificationCompat
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.ProcessLifecycleOwner
import androidx.work.BackoffPolicy
import androidx.work.CoroutineWorker
import androidx.work.Data
import androidx.work.ExistingWorkPolicy
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.OutOfQuotaPolicy
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import com.google.firebase.messaging.FirebaseMessagingService
import com.google.firebase.messaging.RemoteMessage
import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.MainActivity
import io.github.lewin671.remoteagent.R
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.CommandException
import io.github.lewin671.remoteagent.core.PreparedCommand
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.errorText
import io.github.lewin671.remoteagent.proto.Command
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.NotifyContent
import io.github.lewin671.remoteagent.proto.NotifyKind
import io.github.lewin671.remoteagent.proto.ResolveApprovalRequest
import io.github.lewin671.remoteagent.ui.approvalDisplay
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withContext

private const val TAG = "remote-agent"

/** Receives FCM data messages; decrypts them with the notify key and shows a notification. */
class RaMessagingService : FirebaseMessagingService() {
    override fun onNewToken(token: String) {
        RaApp.instance.registerPush(token)
    }

    override fun onMessageReceived(message: RemoteMessage) {
        val started = SystemClock.elapsedRealtime()
        val data = message.data
        val kind =
            NotifyKind.forNumber(data["kind"]?.toIntOrNull() ?: 0)
                ?: NotifyKind.NOTIFY_KIND_UNSPECIFIED
        val app = RaApp.instance
        val settings = runBlocking { app.prefs.settings.first() }
        if (!acceptPush(app.acceptsPush, kind, settings)) return
        if (kind == NotifyKind.NOTIFY_KIND_SECURITY) {
            if (ProcessLifecycleOwner.get().lifecycle.currentState.isAtLeast(Lifecycle.State.STARTED))
                return
            Notifier.showSecurity(this)
            return
        }
        val session = data["session"] ?: return
        var content = data["content"]?.let { open(session, it) }
        if (data["content"] != null && content == null) return
        // Only the actual session page suppresses scheduled results in the foreground.
        // Other kinds retain their foreground rule.
        val foreground = ProcessLifecycleOwner.get().lifecycle.currentState.isAtLeast(Lifecycle.State.STARTED)
        if (foreground && (kind != NotifyKind.NOTIFY_KIND_SCHEDULED || app.visibleSession == session))
            return
        val eventKey = if (kind == NotifyKind.NOTIFY_KIND_SCHEDULED) {
            data["event"]?.takeIf { it.isNotEmpty() } ?: return
        } else null
        if (kind == NotifyKind.NOTIFY_KIND_SCHEDULED && content != null) {
            // Push reception never loads account keys or starts a metadata request. If the
            // foreground client already verified the destination, use its conversation title.
            content = scheduledPushContent(content, app.remote.value?.state?.value?.sessions?.get(session)?.info)
        }
        Notifier.show(this, session, kind, content, settings.showContent, eventKey)
        // Timing only, for the notes on cold starts; no content.
        Log.i(
            TAG,
            "push shown in ${SystemClock.elapsedRealtime() - started} ms, content=${content != null}",
        )
    }

    private fun open(session: String, b64: String): NotifyContent? =
        try {
            val key = RaApp.instance.vault.notifyKey() ?: return null
            val env = Envelope.parseFrom(Base64.decode(b64, Base64.DEFAULT))
            val nc =
                io.github.lewin671.remoteagent.core.DeviceSecurity.NotifyRecord.parse(key).open(env)
            nc.takeIf { it.sessionId == session }
        } catch (e: Exception) {
            Log.w(TAG, "push content does not open: ${e.javaClass.simpleName}")
            null
        }
}

object Notifier {
    const val EXTRA_APPROVAL = "approval"
    const val EXTRA_SECURITY = "security"
    const val EXTRA_SCHEDULE_SEQ = "schedule_seq"

    /** A recovery request: generic text only, tapping opens the device page. */
    fun showSecurity(context: Context) {
        val nm = context.getSystemService(NotificationManager::class.java)
        val i =
            Intent(context, MainActivity::class.java)
                .putExtra(EXTRA_SECURITY, true)
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP)
        val pi =
            PendingIntent.getActivity(
                context,
                EXTRA_SECURITY.hashCode(),
                i,
                PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
            )
        val n =
            NotificationCompat.Builder(context, Push.CHANNEL_ATTENTION)
                .setSmallIcon(R.drawable.ic_notify)
                .setContentTitle("remote-agent")
                .setContentText("有恢复请求，打开设备页查看")
                .setVisibility(NotificationCompat.VISIBILITY_PRIVATE)
                .setAutoCancel(true)
                .setContentIntent(pi)
                .build()
        nm.notify(EXTRA_SECURITY.hashCode(), n)
    }

    private fun openIntent(
        context: Context,
        session: String,
        approval: String? = null,
        code: Int,
        scheduleSeq: Long = 0,
    ): PendingIntent {
        val i =
            Intent(context, MainActivity::class.java)
                .putExtra(MainActivity.EXTRA_SESSION, session)
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP)
        if (approval != null) i.putExtra(EXTRA_APPROVAL, approval)
        if (scheduleSeq > 0) i.putExtra(EXTRA_SCHEDULE_SEQ, scheduleSeq)
        return PendingIntent.getActivity(
            context,
            code,
            i,
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
    }

    fun id(session: String, approval: String?): Int = (approval ?: session).hashCode()

    /**
     * An approval's title and line, as the web names it: a command is 请求执行 with the command, any
     * other tool 请求权限 · <tool> with its description or input. Daemons before
     * NotifyApproval.tool_input put a tool's JSON input in command.
     */
    fun approvalText(a: io.github.lewin671.remoteagent.proto.NotifyApproval): Pair<String, String> {
        val oldToolInput =
            a.toolInput.isEmpty() && a.command.trimStart().startsWith("{") && a.toolName != "Bash"
        val command = if (oldToolInput) "" else a.command
        if (command.isNotEmpty()) return "请求执行" to approvalDisplay(command)
        val input = a.toolInput.ifEmpty { if (oldToolInput) a.command else "" }
        return "请求权限" +
            (if (a.toolName.isNotEmpty()) " · ${approvalDisplay(a.toolName)}" else "") to
            approvalDisplay(a.description.ifEmpty { input }.ifEmpty { a.toolName })
    }

    /** How a push is shown. */
    data class Display(val detailed: Boolean, val buttons: Boolean, val visibility: Int)

    /**
     * Content only when turned on and the phone is unlocked, and then hidden from the lock screen
     * altogether (VISIBILITY_SECRET): VISIBILITY_PRIVATE alone shows everything to users who allow
     * sensitive content on the lock screen. Buttons only from API 31, where they can require
     * unlocking; below that a tap opens the approval page in the app.
     */
    fun display(showContent: Boolean, hasContent: Boolean, locked: Boolean, sdk: Int): Display {
        val detailed = showContent && hasContent && !locked
        return Display(
            detailed = detailed,
            buttons = detailed && sdk >= 31,
            visibility =
                if (detailed) NotificationCompat.VISIBILITY_SECRET
                else NotificationCompat.VISIBILITY_PRIVATE,
        )
    }

    /**
     * Shows a push. Without [showContent], only generic text: tapping it opens the session. With
     * it, the approval itself and 拒绝 / 允许 buttons that need the phone unlocked. The lock screen
     * only ever gets generic text.
     */
    fun show(
        context: Context,
        session: String,
        kind: NotifyKind,
        c: NotifyContent?,
        showContent: Boolean,
        eventKey: String? = null,
    ) {
        require(kind != NotifyKind.NOTIFY_KIND_SCHEDULED || !eventKey.isNullOrEmpty())
        val nm = context.getSystemService(NotificationManager::class.java)
        val generic = when (kind) {
            NotifyKind.NOTIFY_KIND_SCHEDULED -> "定时任务有新结果"
            NotifyKind.NOTIFY_KIND_TURN_ENDED -> "一轮对话结束了"
            else -> "有会话在等你处理"
        }
        val channel = when (kind) {
            NotifyKind.NOTIFY_KIND_SCHEDULED -> Push.CHANNEL_SCHEDULED
            NotifyKind.NOTIFY_KIND_TURN_ENDED -> Push.CHANNEL_TURN
            else -> Push.CHANNEL_ATTENTION
        }
        val approval = c?.takeIf { it.hasApproval() }?.approval
        val nid = id(session, if (kind == NotifyKind.NOTIFY_KIND_SCHEDULED) eventKey else approval?.approvalId)
        val locked = context.getSystemService(KeyguardManager::class.java).isDeviceLocked
        val how = display(showContent, c != null, locked, Build.VERSION.SDK_INT)
        val public =
            NotificationCompat.Builder(context, channel)
                .setSmallIcon(R.drawable.ic_notify)
                .setContentTitle("remote-agent")
                .setContentText(generic)
                .build()
        val b =
            NotificationCompat.Builder(context, channel)
                .setSmallIcon(R.drawable.ic_notify)
                .setVisibility(how.visibility)
                .setPublicVersion(public)
                .setAutoCancel(true)
                .setCategory(NotificationCompat.CATEGORY_MESSAGE)
                // No "Open link" chips guessed from the command.
                .setAllowSystemGeneratedContextualActions(false)
                .setPriority(NotificationCompat.PRIORITY_HIGH)
                .setContentIntent(openIntent(context, session, approval?.approvalId, nid, if (kind == NotifyKind.NOTIFY_KIND_SCHEDULED) c?.sourceEventSeq ?: 0 else 0))
        if (!how.detailed || c == null) {
            b.setContentTitle("remote-agent").setContentText(generic)
        } else if (approval != null) {
            val (title, what) = approvalText(approval)
            b.setContentTitle(title)
                .setContentText(what)
                .setSubText(c.title.takeIf { it.isNotEmpty() })
                .setStyle(
                    NotificationCompat.BigTextStyle()
                        .bigText(
                            listOf(approvalDisplay(approval.description), what)
                                .filter { it.isNotEmpty() }
                                .distinct()
                                .joinToString("\n"),
                        ),
                )
            if (approval.deadlineMs > 0)
                b.setTimeoutAfter(
                    (approval.deadlineMs - System.currentTimeMillis()).coerceAtLeast(1000),
                )
            if (
                how.buttons &&
                    c.sourceEventSeq > 0 &&
                    (approval.deadlineMs == 0L || approval.deadlineMs > System.currentTimeMillis())
            ) {
                b.addAction(action(context, c, false, nid))
                // A command not shown in full is allowed from the full page.
                b.addAction(
                    if (approval.truncated)
                        NotificationCompat.Action.Builder(
                                0,
                                "查看全文",
                                openIntent(context, session, approval.approvalId, nid + 1),
                            )
                            .build()
                    else action(context, c, true, nid),
                )
            }
        } else {
            b.setContentTitle(c.title.ifEmpty { "remote-agent" })
                .setContentText(c.text.ifEmpty { generic })
        }
        try {
            val tag = if (kind == NotifyKind.NOTIFY_KIND_SCHEDULED) "scheduled/$session/$eventKey" else null
            nm.notify(tag, nid, b.build())
        } catch (e: SecurityException) {
            // Notifications are not allowed; Settings shows the banner.
        }
    }

    private fun action(
        context: Context,
        c: NotifyContent,
        allow: Boolean,
        nid: Int,
    ): NotificationCompat.Action {
        val i =
            Intent(context, ApprovalReceiver::class.java)
                .setAction("io.github.lewin671.remoteagent.APPROVAL")
                .putExtra("machine", c.machineId)
                .putExtra("session", c.sessionId)
                .putExtra("approval", c.approval.approvalId)
                .putExtra("sourceSeq", c.sourceEventSeq)
                .putExtra("deadline", c.approval.deadlineMs)
                .putExtra("allow", allow)
                .putExtra("nid", nid)
        // One immutable PendingIntent per notification and button, bound to this approval only.
        val pi =
            PendingIntent.getBroadcast(
                context,
                nid * 2 + if (allow) 1 else 0,
                i,
                PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
            )
        val b = NotificationCompat.Action.Builder(0, if (allow) "允许" else "拒绝", pi)
        if (Build.VERSION.SDK_INT >= 31) b.setAuthenticationRequired(true)
        return b.build()
    }

    fun update(context: Context, nid: Int, text: String, done: Boolean) {
        val nm = context.getSystemService(NotificationManager::class.java)
        val b =
            NotificationCompat.Builder(context, Push.CHANNEL_ATTENTION)
                .setSmallIcon(R.drawable.ic_notify)
                .setContentTitle("remote-agent")
                .setContentText(text)
                .setVisibility(NotificationCompat.VISIBILITY_PRIVATE)
                .setOnlyAlertOnce(true)
                .setSilent(true)
        if (done) b.setTimeoutAfter(8_000) else b.setOngoing(true).setProgress(0, 0, true)
        try {
            nm.notify(nid, b.build())
        } catch (e: SecurityException) {
            // See show.
        }
    }
}

/** A tap on 允许 / 拒绝: seals the reply and hands it to WorkManager. */
class ApprovalReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val pending = goAsync()
        kotlinx.coroutines.CoroutineScope(kotlinx.coroutines.Dispatchers.IO).launch {
            try {
                kotlinx.coroutines.withTimeout(8000) { handleTap(context, intent) }
            } catch (_: Exception) {
                Notifier.update(context, intent.getIntExtra("nid", 0), "无法核实当前授权，请打开 App 处理", true)
            } finally {
                pending.finish()
            }
        }
    }

    private suspend fun handleTap(context: Context, intent: Intent) {

        val session = intent.getStringExtra("session") ?: return
        val machine = intent.getStringExtra("machine") ?: return
        val approval = intent.getStringExtra("approval") ?: return
        val allow = intent.getBooleanExtra("allow", false)
        val deadline = intent.getLongExtra("deadline", 0)
        val nid = intent.getIntExtra("nid", 0)
        // The buttons require unlocking (API 31+); check anyway.
        if (context.getSystemService(KeyguardManager::class.java).isDeviceLocked) {
            Notifier.update(context, nid, "请先解锁手机，再打开 App 审批", true)
            return
        }
        if (deadline in 1..System.currentTimeMillis()) {
            Notifier.update(context, nid, "这条审批已超时，按规则拒绝了", true)
            return
        }
        val acct =
            try {
                RaApp.instance.vault.load()
            } catch (e: Exception) {
                null
            } ?: return Notifier.update(context, nid, "没能读取账户，请打开 App 处理", true)
        val remote = Remote(acct, RaApp.instance.http, securityStorage = RaApp.instance.vault)
        val p =
            remote.prepare(
                machine,
                session,
                Command.newBuilder()
                    .setResolveApproval(
                        ResolveApprovalRequest.newBuilder()
                            .setSessionId(session)
                            .setApprovalId(approval)
                            .setAllow(allow)
                            .setObservedEventSeq(intent.getLongExtra("sourceSeq", 0)),
                    )
                    .build(),
            )
        val authorized =
            try {
                remote.authorize(p)
            } catch (_: Exception) {
                remote.close()
                Notifier.update(context, nid, "无法核实当前授权，请打开 App 处理", true)
                return
            }
        remote.close()
        Notifier.update(context, nid, if (allow) "允许中…" else "拒绝中…", false)
        val data =
            Data.Builder()
                .putString("machine", machine)
                .putString("session", session)
                .putString("request", p.requestId)
                .putLong("deadline", p.deadlineMs)
                .putByteArray("envelope", authorized.toByteArray())
                .putBoolean("allow", allow)
                .putInt("nid", nid)
                .build()
        val work =
            OneTimeWorkRequestBuilder<ApprovalWorker>()
                .setInputData(data)
                // Finds the work of a recorded reply (ApprovalWorker).
                .addTag(p.requestId)
                .setExpedited(OutOfQuotaPolicy.RUN_AS_NON_EXPEDITED_WORK_REQUEST)
                .setBackoffCriteria(BackoffPolicy.LINEAR, 10, TimeUnit.SECONDS)
                .build()
        WorkManager.getInstance(context)
            .enqueueUniqueWork("approval-$approval", ExistingWorkPolicy.KEEP, work)
    }
}

/**
 * Forwards a sealed approval reply. Retries reuse the same request ID, which the daemon runs at
 * most once; the notification says what happened, or that it is unknown.
 */
class ApprovalWorker(context: Context, params: WorkerParameters) :
    CoroutineWorker(context, params) {
    override suspend fun doWork(): Result {
        val d = inputData
        val nid = d.getInt("nid", 0)
        val allow = d.getBoolean("allow", false)
        val request = d.getString("request").orEmpty()
        val deadline = d.getLong("deadline", 0)
        val records =
            applicationContext.getSharedPreferences("approval-attempts", Context.MODE_PRIVATE)
        // What an earlier run of this work recorded: it may be run again after the process was
        // killed.
        fun recorded() =
            records.getString(request, null)?.let {
                runCatching { ApprovalRecord.valueOf(it) }.getOrNull()
            }
        // An earlier attempt of this reply may have gone through.
        fun wasUnknown() = recorded() == ApprovalRecord.SENT
        fun done(): Result {
            Notifier.update(applicationContext, nid, if (allow) "已允许" else "已拒绝", true)
            return Result.success()
        }
        fun finish(text: String): Result {
            Notifier.update(applicationContext, nid, text, true)
            return Result.failure()
        }
        // A record is kept until WorkManager has recorded the end of its work: only then is no run
        // left to read it.
        withContext(Dispatchers.IO) {
            val wm = WorkManager.getInstance(applicationContext)
            val over =
                records.all.keys.filter {
                    it != request && wm.getWorkInfosByTag(it).get().all { w -> w.state.isFinished }
                }
            if (over.isNotEmpty()) records.edit().apply { over.forEach { remove(it) } }.commit()
        }
        // Confirmed by an earlier run that was killed before its success was recorded.
        if (recorded() == ApprovalRecord.DONE) return done()
        val acct =
            try {
                RaApp.instance.vault.load()
            } catch (e: Exception) {
                // The phone locked again before this ran: the account key needs it unlocked.
                if (System.currentTimeMillis() + 5_000 < deadline) {
                    Notifier.update(applicationContext, nid, "手机锁上了，解锁后继续提交…", false)
                    return Result.retry()
                }
                return finish(if (wasUnknown()) "不确定是否生效，请打开 App 看一眼" else "没能提交：手机一直锁着，审批已超时")
            } ?: return finish(if (wasUnknown()) "不确定是否生效，请打开 App 看一眼" else "没能提交：这台手机没有账户")
        val p =
            PreparedCommand(
                d.getString("machine").orEmpty(),
                d.getString("session").orEmpty(),
                d.getString("request").orEmpty(),
                d.getLong("deadline", 0),
                Envelope.parseFrom(
                    ByteString.copyFrom(d.getByteArray("envelope") ?: return Result.failure()),
                ),
            )
        val remote = Remote(acct, RaApp.instance.http, securityStorage = RaApp.instance.vault)
        try {
            sendApproval(
                p,
                recorded(),
                record = { r ->
                    records
                        .edit()
                        .apply { if (r == null) remove(request) else putString(request, r.name) }
                        .commit()
                },
            ) { prepared, retry ->
                remote.forward(prepared, retryOfUnknown = retry)
            }
            return done()
        } catch (e: kotlinx.coroutines.CancellationException) {
            throw e
        } catch (e: CommandException) {
            if (
                (e.unknown || e.code == "network") &&
                    System.currentTimeMillis() + 5_000 < p.deadlineMs &&
                    runAttemptCount < 5
            ) {
                Notifier.update(applicationContext, nid, "还在提交…（${errorText(e)}）", false)
                return Result.retry()
            }
            // Expired after an unknown attempt: that attempt may have worked.
            return finish(
                if (e.unknown || wasUnknown()) "不确定是否生效，请打开 App 看一眼" else "没能提交：${errorText(e)}",
            )
        } catch (e: Exception) {
            return finish(if (wasUnknown()) "不确定是否生效，请打开 App 看一眼" else "没能提交：${errorText(e)}")
        } finally {
            remote.close()
        }
    }

    override suspend fun getForegroundInfo(): androidx.work.ForegroundInfo {
        val n: Notification =
            NotificationCompat.Builder(applicationContext, Push.CHANNEL_ATTENTION)
                .setSmallIcon(R.drawable.ic_notify)
                .setContentTitle("remote-agent")
                .setContentText("正在提交审批…")
                .setSilent(true)
                .build()
        return androidx.work.ForegroundInfo(inputData.getInt("nid", 0) + 7, n)
    }
}

/** The daemon supplies the task title; a previously verified ordinary conversation supplies its own. */
internal fun scheduledPushContent(content: NotifyContent, info: io.github.lewin671.remoteagent.proto.Session?): NotifyContent =
    if (info != null && info.scheduledTaskId.isEmpty() && info.title.isNotEmpty()) content.toBuilder().setTitle(info.title).build() else content
