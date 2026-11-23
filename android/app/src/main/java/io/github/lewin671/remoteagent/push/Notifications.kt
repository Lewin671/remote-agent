package io.github.lewin671.remoteagent.push

import io.github.lewin671.remoteagent.ui.resolveUiMessage
import io.github.lewin671.remoteagent.ui.sessionText

import io.github.lewin671.remoteagent.core.UiMessages

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
import io.github.lewin671.remoteagent.MainActivity
import io.github.lewin671.remoteagent.R
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.ConnState
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.coreTextToMessageText
import io.github.lewin671.remoteagent.core.openNotifyEnvelopeViaCore
import io.github.lewin671.remoteagent.core.proto.SecurityNotifyDisplay
import io.github.lewin671.remoteagent.core.proto.SessionCommandStatus
import io.github.lewin671.remoteagent.core.proto.SessionDockKind
import io.github.lewin671.remoteagent.data.Settings
import io.github.lewin671.remoteagent.proto.Command
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.NotifyKind
import io.github.lewin671.remoteagent.proto.ResolveApprovalRequest
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeoutOrNull

private const val TAG = "remote-agent"
private const val ERROR_CODE_NETWORK = "network"

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
        val eventKey = if (kind == NotifyKind.NOTIFY_KIND_SCHEDULED) {
            data["event"]?.takeIf { it.isNotEmpty() } ?: return
        } else null
        val machine = data["machine"].orEmpty()
        val tag = when (kind) {
            NotifyKind.NOTIFY_KIND_MACHINE_DISK -> data["tag"].orEmpty()
            NotifyKind.NOTIFY_KIND_SCHEDULED -> data["tag"]?.takeIf { it.isNotEmpty() } ?: "$session/$eventKey/NOTIFY_KIND_SCHEDULED"
            NotifyKind.NOTIFY_KIND_TURN_ENDED -> data["tag"]?.takeIf { it.isNotEmpty() } ?: "$session/NOTIFY_KIND_TURN_ENDED"
            NotifyKind.NOTIFY_KIND_ATTENTION -> data["tag"]?.takeIf { it.isNotEmpty() } ?: "$session/NOTIFY_KIND_ATTENTION"
            else -> data["tag"].orEmpty()
        }
        val display = data["content"]?.let {
            open(session, it, machine, tag, kind, eventKey.orEmpty())
        }
        if (data["content"] != null && (display == null || !display.valid)) return
        if (kind == NotifyKind.NOTIFY_KIND_MACHINE_DISK && (display == null || !display.valid)) return
        // Only the actual session page suppresses scheduled results in the foreground.
        // Other kinds retain their foreground rule.
        val foreground = ProcessLifecycleOwner.get().lifecycle.currentState.isAtLeast(Lifecycle.State.STARTED)
        if (foreground && (kind != NotifyKind.NOTIFY_KIND_SCHEDULED || app.visibleSession == session))
            return
        Notifier.show(this, session, kind, display, settings.showContent, eventKey)
        // Timing only, for the notes on cold starts; no content.
        Log.i(
            TAG,
            "push shown in ${SystemClock.elapsedRealtime() - started} ms, content=${display != null}",
        )
    }

    private fun open(
        session: String,
        b64: String,
        machine: String,
        tag: String,
        expectedKind: NotifyKind,
        eventKey: String,
    ): SecurityNotifyDisplay? =
        try {
            val blob = RaApp.instance.vault.readNotifyBlob() ?: return null
            val env = Envelope.parseFrom(Base64.decode(b64, Base64.DEFAULT))
            openNotifyEnvelopeViaCore(
                notifyBlob = blob,
                envelope = env,
                sessionId = session,
                tag = tag,
                machineId = machine,
                expectedKind = expectedKind,
                eventKey = eventKey,
            )
        } catch (e: Exception) {
            Log.w(TAG, "push content does not open: ${e.javaClass.simpleName}")
            null
        }
}

object Notifier {
    const val EXTRA_APPROVAL = "approval"
    const val EXTRA_SECURITY = "security"
    const val EXTRA_MACHINE_STATUS = "machine_status"
    const val EXTRA_SCHEDULE_SEQ = "schedule_seq"
    const val EXTRA_SCHEDULED = "scheduled"

    /** A recovery request: generic text only, tapping opens the device page. */
    fun showSecurity(context: Context, copy: io.github.lewin671.remoteagent.core.MessageResolver = { io.github.lewin671.remoteagent.ui.resolveUiMessage(context, it) }) {
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
                .setContentText(copy(UiMessages.notificationsAndroidRecoveryRequestedOpenDevicesToViewIt()))
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
        scheduleEvent: String? = null,
        scheduled: Boolean = false,
        machineStatus: String? = null,
    ): PendingIntent {
        val i =
            Intent(context, MainActivity::class.java)
                .putExtra(MainActivity.EXTRA_SESSION, session)
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_SINGLE_TOP)
        if (machineStatus!=null) { i.putExtra(EXTRA_MACHINE_STATUS,machineStatus);i.action="io.github.lewin671.remoteagent.machine-status/$machineStatus" }
        if (scheduled) i.putExtra(EXTRA_SCHEDULED, true)
        if (approval != null) i.putExtra(EXTRA_APPROVAL, approval)
        if (scheduleSeq > 0) i.putExtra(EXTRA_SCHEDULE_SEQ, scheduleSeq)
        // Extras do not identify PendingIntents; the numeric request code may collide.
        if (scheduleEvent != null) i.action = "io.github.lewin671.remoteagent.scheduled/$session/$scheduleEvent"
        return PendingIntent.getActivity(
            context,
            code,
            i,
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
    }

    fun id(session: String, approval: String?): Int = (approval ?: session).hashCode()

    /** Run records identify the latest event exactly; never infer sequences for earlier notices. */
    fun dismissScheduled(
        context: Context,
        runs: List<io.github.lewin671.remoteagent.core.proto.ScheduleNotificationRef>,
    ) {
        val nm = context.getSystemService(NotificationManager::class.java)
        runs.forEach { run ->
            val seq = run.notificationSeq
            // notification_seq belongs to the execution log, not notification_session_id.
            if (run.sessionId.isNotEmpty() && seq > 0) {
                val event = "scheduled:$seq"
                nm.cancel("scheduled/${run.sessionId}/$event", id(run.sessionId, event))
            }
        }
    }

    /** How a push is shown. */
    data class Display(val detailed: Boolean, val buttons: Boolean, val visibility: Int)

    /**
     * Scheduled and disk content is on by default; other content requires the setting. Only while
     * unlocked, and then hidden from the lock screen altogether (VISIBILITY_SECRET):
     * VISIBILITY_PRIVATE alone shows everything to users who allow sensitive content on the lock
     * screen. Buttons only from API 31, where they can require unlocking; below that a tap opens
     * the approval page in the app.
     */
    fun display(
        showContent: Boolean,
        hasContent: Boolean,
        locked: Boolean,
        sdk: Int,
        showDetailedByDefault: Boolean = false,
    ): Display {
        val detailed = (showDetailedByDefault || showContent) && hasContent && !locked
        return Display(
            detailed = detailed,
            buttons = detailed && !showDetailedByDefault && sdk >= 31,
            visibility =
                if (detailed) NotificationCompat.VISIBILITY_SECRET
                else NotificationCompat.VISIBILITY_PRIVATE,
        )
    }

    /**
     * Shows a push from Core's validated [SecurityNotifyDisplay]. When [notifyDisplay] is null or
     * invalid, or the device is locked, or content display is disabled for kinds without
     * [SecurityNotifyDisplay.getShowDetailedByDefault], only generic text is shown. The lock screen
     * only ever gets generic text.
     */
    fun show(
        context: Context,
        session: String,
        kind: NotifyKind,
        notifyDisplay: SecurityNotifyDisplay?,
        showContent: Boolean,
        eventKey: String? = null,
        copy: io.github.lewin671.remoteagent.core.MessageResolver = { io.github.lewin671.remoteagent.ui.resolveUiMessage(context, it) },
    ) {
        if (kind == NotifyKind.NOTIFY_KIND_UNSPECIFIED) return
        require(kind != NotifyKind.NOTIFY_KIND_SCHEDULED || !eventKey.isNullOrEmpty())
        fun resolveText(t: io.github.lewin671.remoteagent.core.proto.Text): String =
            coreTextToMessageText(t).resolve(copy)

        val nm = context.getSystemService(NotificationManager::class.java)
        val valid = notifyDisplay?.takeIf { it.valid && it.kind == kind }
        val generic = valid?.takeIf { it.hasGenericBody() }?.let { resolveText(it.genericBody) }?.ifEmpty { null }
            ?: notifyDisplay?.takeIf { it.hasGenericBody() }?.let { resolveText(it.genericBody) }?.ifEmpty { null }
            ?: when (kind) {
                NotifyKind.NOTIFY_KIND_SCHEDULED -> copy(UiMessages.notificationsScheduledTasksHaveNewResults())
                NotifyKind.NOTIFY_KIND_TURN_ENDED -> copy(UiMessages.notificationsAndroidAConversationTurnFinished())
                NotifyKind.NOTIFY_KIND_MACHINE_DISK -> copy(UiMessages.machineStatusHomeWarning())
                else -> copy(UiMessages.notificationsAndroidASessionNeedsYourAttention())
            }
        val channel = when (kind) {
            NotifyKind.NOTIFY_KIND_SCHEDULED -> Push.CHANNEL_SCHEDULED
            NotifyKind.NOTIFY_KIND_TURN_ENDED -> Push.CHANNEL_TURN
            else -> Push.CHANNEL_ATTENTION
        }
        val isDisk = kind == NotifyKind.NOTIFY_KIND_MACHINE_DISK
        val diskMachineId = valid?.machineId?.takeIf { isDisk && it.isNotEmpty() }
        val approvalId = valid?.approvalId?.takeIf { it.isNotEmpty() }
        val nid = diskMachineId?.hashCode()
            ?: id(session, if (kind == NotifyKind.NOTIFY_KIND_SCHEDULED) eventKey else approvalId)
        val locked = context.getSystemService(KeyguardManager::class.java).isDeviceLocked
        val how = display(
            showContent = showContent,
            hasContent = valid != null,
            locked = locked,
            sdk = Build.VERSION.SDK_INT,
            showDetailedByDefault = valid?.showDetailedByDefault == true,
        )
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
                .setContentIntent(
                    openIntent(
                        context, session, approvalId, nid,
                        machineStatus = diskMachineId,
                        scheduled = kind == NotifyKind.NOTIFY_KIND_SCHEDULED,
                        scheduleSeq = if (kind == NotifyKind.NOTIFY_KIND_SCHEDULED) valid?.sourceEventSeq ?: 0L else 0L,
                        scheduleEvent = eventKey.takeIf { kind == NotifyKind.NOTIFY_KIND_SCHEDULED },
                    ),
                )
        if (!how.detailed || valid == null) {
            b.setContentTitle("remote-agent").setContentText(generic)
        } else {
            val title = if (valid.hasTitle()) resolveText(valid.title).ifEmpty { "remote-agent" } else "remote-agent"
            val body = if (valid.hasBody()) resolveText(valid.body).ifEmpty { generic } else generic
            b.setContentTitle(title).setContentText(body)
            if (valid.hasSubText()) {
                val sub = resolveText(valid.subText)
                if (sub.isNotEmpty()) b.setSubText(sub)
            }
            if (valid.hasBigText()) {
                val big = resolveText(valid.bigText)
                if (big.isNotEmpty()) b.setStyle(NotificationCompat.BigTextStyle().bigText(big))
            }
            if (approvalId != null) {
                if (valid.deadlineMs > 0) {
                    b.setTimeoutAfter((valid.deadlineMs - System.currentTimeMillis()).coerceAtLeast(1000))
                }
                if (
                    how.buttons &&
                        valid.sourceEventSeq > 0 &&
                        (valid.deadlineMs == 0L || valid.deadlineMs > System.currentTimeMillis())
                ) {
                    b.addAction(action(context, valid, false, nid, copy = copy))
                    // A command not shown in full is allowed from the full page.
                    b.addAction(
                        if (valid.truncated)
                            NotificationCompat.Action.Builder(
                                    0,
                                    copy(UiMessages.notificationsAndroidViewFullText()),
                                    openIntent(context, session, approvalId, nid + 1),
                                )
                                .build()
                        else action(context, valid, true, nid, copy = copy),
                    )
                }
            }
        }
        try {
            val tag = when {
                isDisk && valid != null -> valid.tag.ifEmpty { "${valid.machineId}/NOTIFY_KIND_MACHINE_DISK" }
                kind == NotifyKind.NOTIFY_KIND_SCHEDULED -> "scheduled/$session/$eventKey"
                else -> null
            }
            nm.notify(tag, nid, b.build())
        } catch (e: SecurityException) {
            // Notifications are not allowed; Settings shows the banner.
        }
    }

    private fun action(
        context: Context,
        d: SecurityNotifyDisplay,
        allow: Boolean,
        nid: Int,
        copy: io.github.lewin671.remoteagent.core.MessageResolver = { io.github.lewin671.remoteagent.ui.resolveUiMessage(context, it) },
    ): NotificationCompat.Action {
        val i =
            Intent(context, ApprovalReceiver::class.java)
                .setAction("io.github.lewin671.remoteagent.APPROVAL")
                .putExtra("machine", d.machineId)
                .putExtra("session", d.sessionId)
                .putExtra("approval", d.approvalId)
                .putExtra("sourceSeq", d.sourceEventSeq)
                .putExtra("deadline", d.deadlineMs)
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
        val b = NotificationCompat.Action.Builder(0, if (allow) copy(UiMessages.approvalAllow()) else copy(UiMessages.approvalAndroidDenyDecision()), pi)
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
                Notifier.update(context, intent.getIntExtra("nid", 0), sessionText(UiMessages.notificationsAndroidCurrentAuthorizationCouldNotBeVerifiedOpenTheAppTo()), true)
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
            Notifier.update(context, nid, sessionText(UiMessages.notificationsAndroidUnlockThePhoneThenOpenTheAppToApprove()), true)
            return
        }
        if (deadline in 1..System.currentTimeMillis()) {
            Notifier.update(context, nid, sessionText(UiMessages.notificationsAndroidThisApprovalTimedOutAndWasRejectedByPolicy()), true)
            return
        }
        val acct =
            try {
                RaApp.instance.loadAccountForBackgroundWork()
            } catch (e: Exception) {
                null
            } ?: return Notifier.update(context, nid, sessionText(UiMessages.notificationsAndroidCouldNotReadTheAccountOpenTheAppToContinue()), true)
        val sourceSeq = intent.getLongExtra("sourceSeq", 0)
        Notifier.update(context, nid, if (allow) sessionText(UiMessages.notificationsAndroidAllowing()) else sessionText(UiMessages.notificationsAndroidRejecting()), false)
        val data =
            Data.Builder()
                .putString("machine", machine)
                .putString("session", session)
                .putString("approval", approval)
                .putLong("sourceSeq", sourceSeq)
                .putLong("deadline", deadline)
                .putBoolean("allow", allow)
                .putInt("nid", nid)
                .build()
        val work =
            OneTimeWorkRequestBuilder<ApprovalWorker>()
                .setInputData(data)
                .addTag("approval-$approval")
                .setExpedited(OutOfQuotaPolicy.RUN_AS_NON_EXPEDITED_WORK_REQUEST)
                .setBackoffCriteria(BackoffPolicy.LINEAR, 10, TimeUnit.SECONDS)
                .build()
        WorkManager.getInstance(context)
            .enqueueUniqueWork("approval-$approval", ExistingWorkPolicy.KEEP, work)
    }
}

/** Local choices apply even before the server has received a subscription update. */
fun acceptPush(hasAccount: Boolean, kind: NotifyKind, settings: Settings): Boolean = hasAccount && when (kind) {
    NotifyKind.NOTIFY_KIND_ATTENTION -> settings.notifyAttention
    NotifyKind.NOTIFY_KIND_TURN_ENDED -> settings.notifyTurnEnded
    NotifyKind.NOTIFY_KIND_SECURITY, NotifyKind.NOTIFY_KIND_MACHINE_DISK -> true
    NotifyKind.NOTIFY_KIND_SCHEDULED -> settings.notifyScheduled
    else -> false
}

private enum class ApprovalRecord { SENT, DONE }

/**
 * Dispatches an approval reply from a push notification through CoreHost.
 */
class ApprovalWorker(context: Context, params: WorkerParameters) :
    CoroutineWorker(context, params) {
    override suspend fun doWork(): Result {
        val d = inputData
        val nid = d.getInt("nid", 0)
        val machine = d.getString("machine").orEmpty()
        val session = d.getString("session").orEmpty()
        val approval = d.getString("approval").orEmpty()
        val sourceSeq = d.getLong("sourceSeq", 0L)
        val allow = d.getBoolean("allow", false)
        val deadline = d.getLong("deadline", 0L)
        val key = "$session:$approval"
        val records =
            applicationContext.getSharedPreferences("approval-attempts", Context.MODE_PRIVATE)
        fun recorded() =
            records.getString(key, null)?.let {
                runCatching { ApprovalRecord.valueOf(it) }.getOrNull()
            }
        fun wasUnknown() = recorded() == ApprovalRecord.SENT
        fun saveRecord(r: ApprovalRecord?): Boolean =
            records.edit().apply { if (r == null) remove(key) else putString(key, r.name) }.commit()
        fun done(): Result {
            Notifier.update(applicationContext, nid, if (allow) sessionText(UiMessages.approvalAllowed()) else sessionText(UiMessages.approvalDenied()), true)
            return Result.success()
        }
        fun finish(text: String): Result {
            Notifier.update(applicationContext, nid, text, true)
            return Result.failure()
        }
        withContext(Dispatchers.IO) {
            val wm = WorkManager.getInstance(applicationContext)
            val over =
                records.all.keys.filter {
                    it != key && wm.getWorkInfosByTag(it).get().all { w -> w.state.isFinished }
                }
            if (over.isNotEmpty()) records.edit().apply { over.forEach { remove(it) } }.commit()
        }
        if (recorded() == ApprovalRecord.DONE) return done()
        val acct =
            try {
                RaApp.instance.loadAccountForBackgroundWork()
            } catch (e: Exception) {
                if (deadline == 0L || System.currentTimeMillis() + 5_000 < deadline) {
                    Notifier.update(applicationContext, nid, sessionText(UiMessages.notificationsAndroidPhoneLockedSubmissionContinuesAfterUnlocking()), false)
                    return Result.retry()
                }
                return finish(if (wasUnknown()) sessionText(UiMessages.notificationsAndroidOutcomeUnknownOpenTheAppToCheck()) else sessionText(UiMessages.notificationsAndroidCouldNotSubmitPhoneStayedLockedUntilApprovalTimedOut()))
            } ?: return finish(if (wasUnknown()) sessionText(UiMessages.notificationsAndroidOutcomeUnknownOpenTheAppToCheck()) else sessionText(UiMessages.notificationsAndroidCouldNotSubmitThisPhoneHasNoAccount()))
        if (machine.isEmpty() || session.isEmpty() || approval.isEmpty()) return Result.failure()
        val remote = Remote(acct, RaApp.instance.http, securityStore = RaApp.instance.vault)
        try {
            remote.start()
            val ready = withTimeoutOrNull(10_000L) {
                remote.state.first {
                    it.conn == ConnState.ONLINE && it.machines[machine]?.online == true && it.sessions[session]?.info != null
                }
                true
            } ?: false
            if (!ready) {
                if ((deadline == 0L || System.currentTimeMillis() + 5_000 < deadline) && runAttemptCount < 5) {
                    Notifier.update(applicationContext, nid, sessionText(UiMessages.notificationsAndroidSubmittingApproval()), false)
                    return Result.retry()
                }
                return finish(if (wasUnknown()) sessionText(UiMessages.notificationsAndroidOutcomeUnknownOpenTheAppToCheck()) else sessionText(UiMessages.errorsTheComputerIsOfflineTheCommandWasNotSent()))
            }
            val vmFlow = remote.watchSession(session)
            remote.openSession(session)
            val initialVm = withTimeoutOrNull(5_000L) {
                vmFlow.first { vm ->
                    vm.timeline.loaded || (vm.dock.hasItem && vm.dock.kind == SessionDockKind.SESSION_DOCK_KIND_APPROVAL)
                }
            }
            if (initialVm != null && initialVm.timeline.loaded &&
                (!initialVm.dock.hasItem || initialVm.dock.kind != SessionDockKind.SESSION_DOCK_KIND_APPROVAL || initialVm.dock.approval.approvalId != approval)
            ) {
                saveRecord(ApprovalRecord.DONE)
                return done()
            }
            check(saveRecord(ApprovalRecord.SENT)) {
                sessionText(UiMessages.notificationsAndroidCouldNotSaveApprovalSubmissionStateNothingWasSent())
            }
            remote.submitSessionCommand(
                session,
                Command.newBuilder()
                    .setResolveApproval(
                        ResolveApprovalRequest.newBuilder()
                            .setSessionId(session)
                            .setApprovalId(approval)
                            .setAllow(allow)
                            .apply { if (sourceSeq > 0) setObservedEventSeq(sourceSeq) },
                    )
                    .build(),
                machineId = machine,
            )
            val settled = withTimeoutOrNull(30_000L) {
                vmFlow.first { vm ->
                    val same = vm.dock.hasItem &&
                        vm.dock.kind == SessionDockKind.SESSION_DOCK_KIND_APPROVAL &&
                        vm.dock.approval.approvalId == approval
                    if (!same) return@first true
                    val st = vm.dock.command.status
                    st == SessionCommandStatus.SESSION_COMMAND_STATUS_FAILED ||
                        st == SessionCommandStatus.SESSION_COMMAND_STATUS_UNKNOWN
                }
            }
            if (settled == null) {
                if ((deadline == 0L || System.currentTimeMillis() + 5_000 < deadline) && runAttemptCount < 5) {
                    return Result.retry()
                }
                return finish(sessionText(UiMessages.notificationsAndroidOutcomeUnknownOpenTheAppToCheck()))
            }
            val same = settled.dock.hasItem &&
                settled.dock.kind == SessionDockKind.SESSION_DOCK_KIND_APPROVAL &&
                settled.dock.approval.approvalId == approval
            if (!same) {
                saveRecord(ApprovalRecord.DONE)
                return done()
            }
            val cmdState = settled.dock.command
            if (cmdState.status == SessionCommandStatus.SESSION_COMMAND_STATUS_FAILED) {
                saveRecord(null)
                val msg = if (cmdState.hasErrorText()) {
                    coreTextToMessageText(cmdState.errorText).resolve { resolveUiMessage(applicationContext, it) }
                } else cmdState.errorCode.ifEmpty { sessionText(UiMessages.approvalAndroidUnknownResult()) }
                return finish(sessionText(UiMessages.dockSubmitFailed(msg)))
            }
            if (cmdState.errorCode == ERROR_CODE_NETWORK || !cmdState.expired) {
                if ((deadline == 0L || System.currentTimeMillis() + 5_000 < deadline) && runAttemptCount < 5) {
                    val msg = if (cmdState.hasErrorText()) {
                        coreTextToMessageText(cmdState.errorText).resolve { resolveUiMessage(applicationContext, it) }
                    } else sessionText(UiMessages.approvalAndroidUnknownResult())
                    Notifier.update(applicationContext, nid, sessionText(UiMessages.notificationsAndroidStillSubmittingValue1(msg)), false)
                    return Result.retry()
                }
            }
            return finish(sessionText(UiMessages.notificationsAndroidOutcomeUnknownOpenTheAppToCheck()))
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            return finish(if (wasUnknown()) sessionText(UiMessages.notificationsAndroidOutcomeUnknownOpenTheAppToCheck()) else sessionText(UiMessages.dockSubmitFailed(e.message ?: e.javaClass.simpleName)))
        } finally {
            remote.close()
        }
    }

    override suspend fun getForegroundInfo(): androidx.work.ForegroundInfo {
        val n: Notification =
            NotificationCompat.Builder(applicationContext, Push.CHANNEL_ATTENTION)
                .setSmallIcon(R.drawable.ic_notify)
                .setContentTitle("remote-agent")
                .setContentText(sessionText(UiMessages.notificationsAndroidSubmittingApproval()))
                .setSilent(true)
                .build()
        return androidx.work.ForegroundInfo(inputData.getInt("nid", 0) + 7, n)
    }
}
