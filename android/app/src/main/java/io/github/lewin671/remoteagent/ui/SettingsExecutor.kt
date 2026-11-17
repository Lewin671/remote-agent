package io.github.lewin671.remoteagent.ui

import com.google.android.gms.common.ConnectionResult
import com.google.android.gms.common.GoogleApiAvailabilityLight
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.PROTOCOL_VERSION
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.proto.ClientUpdateStateEvent
import io.github.lewin671.remoteagent.core.proto.NotificationPermissionState
import io.github.lewin671.remoteagent.core.proto.PushRegistrationState
import io.github.lewin671.remoteagent.core.proto.ThemePreference
import io.github.lewin671.remoteagent.data.Settings as AppSettings
import io.github.lewin671.remoteagent.data.ThemeMode
import io.github.lewin671.remoteagent.push.Push
import io.github.lewin671.remoteagent.push.PushStatus
import io.github.lewin671.remoteagent.update.UpdateCheck
import io.github.lewin671.remoteagent.update.UpdateState
import io.github.lewin671.remoteagent.update.Updater
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlin.math.roundToInt

/** State of an in-flight or failed client rename effect for [ClientScreen]. */
data class RenameClientUiState(
    val savingDeviceId: String? = null,
    val error: String = "",
)

/**
 * Bridges Core's Settings / Update / Prefs / Client Rename effects to Android executors
 * ([Prefs], [Push], [Updater], [Remote.renameDevice]) for the active [Remote].
 */
object SettingsExecutor {
    private val _renameState = MutableStateFlow(RenameClientUiState())
    val renameState: StateFlow<RenameClientUiState> = _renameState.asStateFlow()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    private var activeJob: Job? = null
    @Volatile
    private var lastFromPrefs: AppSettings? = null

    fun attach(app: RaApp, remote: Remote) {
        activeJob?.cancel()
        val core = remote.coreHost
        _renameState.value = RenameClientUiState()
        lastFromPrefs = null

        core.onCheckUpdates = {
            Updater.check(app, manual = true)
        }

        core.onPersistClientPrefs = { eff ->
            scope.launch {
                val before = app.prefs.settings.first()
                val nextTheme = eff.theme.toThemeMode()
                val next = before.copy(
                    notifyAttention = eff.notifyAttention,
                    notifyTurnEnded = eff.notifyTurnEnded,
                    notifyScheduled = eff.notifyScheduled,
                    showContent = eff.showContent,
                    theme = nextTheme,
                )
                lastFromPrefs = next
                app.prefs.update { next }
                if (
                    before.notifyAttention != eff.notifyAttention ||
                    before.notifyTurnEnded != eff.notifyTurnEnded ||
                    before.notifyScheduled != eff.notifyScheduled
                ) {
                    Push.register(app, remote)
                }
            }
        }

        activeJob = scope.launch {
            launch {
                core.renameClientEvents.collect { eff ->
                    _renameState.value = RenameClientUiState(savingDeviceId = eff.deviceId, error = "")
                    try {
                        remote.renameDevice(eff.deviceId, eff.name)
                        _renameState.value = RenameClientUiState()
                    } catch (e: kotlinx.coroutines.CancellationException) {
                        throw e
                    } catch (e: Exception) {
                        _renameState.value = RenameClientUiState(error = displayError(e))
                    }
                }
            }

            launch {
                combine(app.prefs.settings, Push.status) { settings, push -> settings to push }
                    .collect { (settings, push) ->
                        val prev = lastFromPrefs
                        lastFromPrefs = settings
                        val supported = Push.available(app)
                        val permission = notificationPermissionState(app, supported)
                        val pushState = pushRegistrationState(app, settings, push, supported, permission)
                        core.reportNotificationPrefs(
                            supported = supported,
                            permission = permission,
                            pushState = pushState,
                            notifyAttention = settings.notifyAttention,
                            notifyTurnEnded = settings.notifyTurnEnded,
                            notifyScheduled = settings.notifyScheduled,
                            showContent = settings.showContent,
                            errorDetail = push.problem,
                        )
                        if (prev == null || prev.theme != settings.theme) {
                            core.setAppearancePref(settings.theme.toThemePreference())
                        }
                    }
            }

            launch {
                combine(Updater.state, Updater.checking) { state, check -> state to check }
                    .collect { (state, check) ->
                        core.reportClientUpdateState(buildUpdateStateEvent(app, state, check))
                    }
            }
        }
    }

    fun detach() {
        activeJob?.cancel()
        activeJob = null
        _renameState.value = RenameClientUiState()
    }

    fun clearRenameError() {
        if (_renameState.value.error.isNotEmpty()) {
            _renameState.value = _renameState.value.copy(error = "")
        }
    }

    private fun notificationPermissionState(app: RaApp, pushSupported: Boolean): NotificationPermissionState {
        if (!pushSupported) return NotificationPermissionState.NOTIFICATION_PERMISSION_STATE_UNSUPPORTED
        return if (Push.permitted(app)) {
            NotificationPermissionState.NOTIFICATION_PERMISSION_STATE_GRANTED
        } else {
            NotificationPermissionState.NOTIFICATION_PERMISSION_STATE_DENIED
        }
    }

    private fun pushRegistrationState(
        app: RaApp,
        settings: AppSettings,
        push: PushStatus,
        pushSupported: Boolean,
        permission: NotificationPermissionState,
    ): PushRegistrationState {
        if (!pushSupported || permission != NotificationPermissionState.NOTIFICATION_PERMISSION_STATE_GRANTED) {
            return PushRegistrationState.PUSH_REGISTRATION_STATE_DISABLED
        }
        val gmsOk = GoogleApiAvailabilityLight.getInstance().isGooglePlayServicesAvailable(app) == ConnectionResult.SUCCESS
        if (!gmsOk) return PushRegistrationState.PUSH_REGISTRATION_STATE_FAILED
        if (push.problem.isNotEmpty()) return PushRegistrationState.PUSH_REGISTRATION_STATE_FAILED
        val anyEnabled = settings.notifyAttention || settings.notifyTurnEnded || settings.notifyScheduled
        return if (anyEnabled) PushRegistrationState.PUSH_REGISTRATION_STATE_ACTIVE else PushRegistrationState.PUSH_REGISTRATION_STATE_DISABLED
    }

    private fun buildUpdateStateEvent(app: RaApp, state: UpdateState, check: UpdateCheck): ClientUpdateStateEvent {
        val b = ClientUpdateStateEvent.newBuilder()
            .setCurrentVersion(app.versionName)
            .setProtocolVersion(PROTOCOL_VERSION)
            .setChannel(if (Updater.enabled(app)) "stable" else "dev")
            .setChecking(check.running)
        when (state) {
            is UpdateState.Available -> b.setAvailableVersion(state.release.version)
            is UpdateState.Downloading -> {
                b.setAvailableVersion(state.release.version)
                val total = state.release.size
                val pct = if (total > 0L) ((state.done * 100.0) / total).roundToInt().coerceIn(0, 100) else 0
                b.setDownloadProgressPercent(pct)
            }
            is UpdateState.Ready -> {
                b.setAvailableVersion(state.release.version)
                b.setDownloadedVersion(state.release.version)
            }
            is UpdateState.DownloadFailed -> {
                b.setAvailableVersion(state.release.version)
                b.setErrorMessage(state.error)
            }
            is UpdateState.Latest, UpdateState.Unknown -> {}
        }
        if (check.error != null) b.setErrorMessage(check.error)
        return b.build()
    }
}

private fun ThemeMode.toThemePreference(): ThemePreference = when (this) {
    ThemeMode.SYSTEM -> ThemePreference.THEME_PREFERENCE_AUTO
    ThemeMode.LIGHT -> ThemePreference.THEME_PREFERENCE_LIGHT
    ThemeMode.DARK -> ThemePreference.THEME_PREFERENCE_DARK
}

private fun ThemePreference.toThemeMode(): ThemeMode = when (this) {
    ThemePreference.THEME_PREFERENCE_LIGHT -> ThemeMode.LIGHT
    ThemePreference.THEME_PREFERENCE_DARK -> ThemeMode.DARK
    else -> ThemeMode.SYSTEM
}
