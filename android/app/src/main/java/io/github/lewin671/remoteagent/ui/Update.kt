package io.github.lewin671.remoteagent.ui

import io.github.lewin671.remoteagent.core.UiMessages

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.PROTOCOL_VERSION
import io.github.lewin671.remoteagent.core.Release
import io.github.lewin671.remoteagent.data.Settings
import io.github.lewin671.remoteagent.update.UpdateCheck
import io.github.lewin671.remoteagent.update.UpdateState
import io.github.lewin671.remoteagent.update.Updater
import io.github.lewin671.remoteagent.update.release
import kotlinx.coroutines.launch

/** 5.7 MiB: a download's size, to one decimal. */
fun mibText(bytes: Long): String = "%.1f MiB".format(bytes / 1048576.0)

/** What the 版本 row says after the installed version, when there is something to say. */
fun updateWord(state: UpdateState, copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText): String? = when (state) {
    is UpdateState.Available, is UpdateState.DownloadFailed -> copy(UiMessages.settingsAndroidUpdateAvailableValue1((state.release!!.version).toString()))
    is UpdateState.Downloading -> copy(UiMessages.settingsAndroidDownloadingValue1((state.release.version).toString()))
    is UpdateState.Ready -> copy(UiMessages.settingsAndroidValue1Downloaded((state.release.version).toString()))
    else -> null
}

/**
 * The one line the sheet writes about the last look, under what the look was about: why a look
 * the user asked for failed, or that the release on screen is still the latest published one.
 */
data class CheckLine(val text: String, val error: Boolean)

fun checkLine(check: UpdateCheck, releaseOnScreen: Boolean, copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText): CheckLine? = when {
    // The reason comes first: it is what the user needs next, and a failure confirms nothing.
    check.error != null -> CheckLine(copy(UiMessages.settingsAndroidCouldNotCheckForUpdatesValue1((check.error).toString())), error = true)
    check.upToDate && releaseOnScreen -> CheckLine(copy(UiMessages.settingsAndroidAlreadyOnTheLatestPublishedVersion()), error = false)
    else -> null
}

/** A pending installation may continue only after checking, for the exact release requested. */
internal fun readyForInstall(state: UpdateState, check: UpdateCheck, versionCode: Long): UpdateState.Ready? =
    (state as? UpdateState.Ready)?.takeIf { !check.running && it.release.versionCode == versionCode }

/** The download/install action for a release, by how far getting it is. */
data class UpdateStep(val label: String, val enabled: Boolean, val error: String = "")

fun updateStep(state: UpdateState, copy: io.github.lewin671.remoteagent.core.MessageResolver = ::sessionText): UpdateStep? = when (state) {
    is UpdateState.Available -> UpdateStep(copy(UiMessages.settingsAndroidDownloadAndInstallValue1((mibText(state.release.size)).toString())), true)
    is UpdateState.Downloading -> UpdateStep(copy(UiMessages.settingsAndroidDownloadingValue1Value2((mibText(state.done)).toString(), (mibText(state.release.size)).toString())), false)
    is UpdateState.Ready -> UpdateStep(copy(UiMessages.settingsAndroidInstall()), true)
    is UpdateState.DownloadFailed -> UpdateStep(copy(UiMessages.settingsAndroidDownloadAgain()), true, state.error)
    else -> null
}

/**
 * 有新版本: one line on the home page while a newer release is known and has not been put off
 * (client-v35). It opens the same sheet as the 版本 row in 设置.
 */
@Composable
fun UpdateNotice(modifier: Modifier = Modifier, onOpen: () -> Unit) {
    val copy = uiText()
    val app = RaApp.instance
    val state by Updater.state.collectAsState()
    val settings by app.prefs.settings.collectAsState(Settings())
    val release = state.release ?: return
    if (release.versionCode <= settings.skippedUpdate) return
    Notice(copy(UiMessages.settingsAndroidNewVersionValue1Available((release.version).toString())), modifier, action = copy(UiMessages.settingsAndroidUpdate()), onAction = onOpen)
}

/** How handing the download to the system went, when there is something to say about it. */
enum class InstallNote(val text: String, val error: Boolean = false) {
    PERMISSION(sessionText(UiMessages.settingsAndroidAllowRemoteAgentToInstallAppsInSystemSettingsThen())),
    CANCELLED(sessionText(UiMessages.settingsAndroidInstallationDidNotFinishYouCanInstallAgain())),
    FAILED(sessionText(UiMessages.settingsAndroidTheSystemCouldNotInstallThisPackage()), error = true),
    NO_INSTALLER(sessionText(UiMessages.settingsAndroidCannotOpenTheSystemInstaller()), error = true),
}

/** 版本: the installed version, and the newer release when there is one, with what changed and the one step to take next. */
@Composable
fun UpdateSheet(onClose: () -> Unit) {
    val copy = uiText()
    val app = RaApp.instance
    val context = androidx.compose.ui.platform.LocalContext.current
    val p = pal
    val state by Updater.state.collectAsState()
    val release = state.release
    val check by Updater.checking.collectAsState()
    // What to say about the last hand-off to the installer, and the release it was about: another release drops it.
    var note by remember { mutableStateOf<InstallNote?>(null) }
    var noteFor by remember { mutableStateOf(0L) }
    var permissionReturnedFor by remember { mutableStateOf<Long?>(null) }
    val shown = note?.takeIf { noteFor == release?.versionCode && state is UpdateState.Ready }
    val installer = androidx.activity.compose.rememberLauncherForActivityResult(androidx.activity.result.contract.ActivityResultContracts.StartActivityForResult()) { r ->
        // On success the app is replaced and this never runs; anything else leaves the download ready to try again.
        note = when (r.resultCode) {
            android.app.Activity.RESULT_OK -> null
            android.app.Activity.RESULT_CANCELED -> InstallNote.CANCELLED
            else -> InstallNote.FAILED
        }
    }
    fun open(code: Long, file: java.io.File) {
        val ready = readyForInstall(Updater.state.value, Updater.checking.value, code) ?: return
        if (ready.file != file) return
        noteFor = code
        note = null
        try {
            installer.launch(Updater.installIntent(context, file))
        } catch (e: Exception) {
            note = InstallNote.NO_INSTALLER
        }
    }
    val permission = androidx.activity.compose.rememberLauncherForActivityResult(androidx.activity.result.contract.ActivityResultContracts.StartActivityForResult()) {
        // Defer the hand-off until a foreground check finishes, for the release permission was requested for.
        permissionReturnedFor = noteFor
    }
    androidx.compose.runtime.LaunchedEffect(state, check.running, permissionReturnedFor) {
        val code = permissionReturnedFor ?: return@LaunchedEffect
        if (check.running) return@LaunchedEffect
        permissionReturnedFor = null
        val ready = readyForInstall(Updater.state.value, Updater.checking.value, code) ?: return@LaunchedEffect
        if (Updater.canInstall(context)) open(code, ready.file) else note = InstallNote.PERMISSION
    }

    fun install(code: Long, file: java.io.File) {
        if (Updater.checking.value.running) return
        noteFor = code
        if (Updater.canInstall(context)) return open(code, file)
        note = InstallNote.PERMISSION
        try {
            permission.launch(Updater.permissionIntent(context))
        } catch (e: android.content.ActivityNotFoundException) {
            // Some vendor builds have no page per app; their security settings lead there.
            runCatching { permission.launch(android.content.Intent(android.provider.Settings.ACTION_SECURITY_SETTINGS)) }
        }
    }
    // Closing the sheet takes the request back: the package waits for a tap on 安装.
    androidx.compose.runtime.DisposableEffect(Unit) { onDispose { Updater.installWhenReady = false } }
    // The download asked for from here is in: on to the installer, while this sheet is in front.
    androidx.compose.runtime.LaunchedEffect(state, check.running) {
        val s = state
        if (s is UpdateState.Ready && Updater.installWhenReady && !check.running) {
            Updater.installWhenReady = false
            install(s.release.versionCode, s.file)
        }
    }
    Sheet(onClose) {
        if (release == null) {
            SheetHead(copy(UiMessages.settingsAndroidVersion()), copy(UiMessages.commonDone()), onClose)
            Text("remote-agent ${app.versionName}", style = T.s15, color = p.ink)
            if (state is UpdateState.Latest) Text(copy(UiMessages.settingsAndroidUpToDate()), style = T.s13, color = p.ink2, modifier = Modifier.padding(top = 6.dp))
            checkLine(check, releaseOnScreen = false, copy = copy)?.let { Text(it.text, style = T.s13, color = p.red, modifier = Modifier.padding(top = 6.dp)) }
            PillButton(if (check.running) copy(UiMessages.settingsAndroidCheckingForUpdates()) else copy(UiMessages.settingsCheckForUpdates()), { Updater.check(app, manual = true) }, Modifier.fillMaxWidth().padding(top = 14.dp), enabled = !check.running)
            return@Sheet
        }
        SheetHead(copy(UiMessages.settingsAndroidNewVersionValue1((release.version).toString())), copy(UiMessages.settingsAndroidLater()), {
            // Put off: the home page stops mentioning this release; 设置 still shows it. Saved in
            // the app's scope: this sheet's own ends with it, before the write may be through.
            app.scope.launch { app.prefs.update { it.copy(skippedUpdate = release.versionCode) } }
            onClose()
        })
        ReleaseNotes(release, app.versionName)
        val step = updateStep(state, copy = copy) ?: return@Sheet
        if (step.error.isNotEmpty()) Text(copy(UiMessages.settingsAndroidCouldNotDownloadValue1((step.error).toString())), style = T.s13, color = p.red, modifier = Modifier.padding(top = 10.dp))
        shown?.let { Text(it.text, style = T.s13, color = if (it.error) p.red else p.orangeInk, modifier = Modifier.padding(top = 10.dp)) }
        PillButton(if (shown == InstallNote.PERMISSION) copy(UiMessages.settingsAndroidAllowInSettings()) else step.label, {
            if (Updater.checking.value.running) return@PillButton
            val s = Updater.state.value
            if (s is UpdateState.Ready) install(s.release.versionCode, s.file) else Updater.download(app)
        }, Modifier.fillMaxWidth().padding(top = 14.dp), ButtonKind.INK, enabled = step.enabled && !check.running)
        Hint(copy(UiMessages.settingsAndroidTheSystemWillConfirmInstallationAfterDownloadingTheFirstTime()))
        // How the look for a newer release went. Either way the release above stays, and stays actionable.
        checkLine(check, releaseOnScreen = true, copy = copy)?.let {
            Text(it.text, style = T.s13, color = if (it.error) p.red else p.ink2, modifier = Modifier.padding(top = 6.dp))
        }
        TextAction(
            if (check.running) copy(UiMessages.settingsAndroidCheckingForUpdates()) else copy(UiMessages.settingsCheckForUpdates()),
            { Updater.check(app, manual = true) },
            Modifier.fillMaxWidth().padding(top = 8.dp),
            style = T.s15b,
            enabled = !check.running && state !is UpdateState.Downloading,
            alignment = Alignment.Center,
        )
    }
}

@Composable
private fun ReleaseNotes(release: Release, installed: String) {
    val copy = uiText()
    val p = pal
    Column {
        Text(copy(UiMessages.settingsAndroidCurrentVersionValue1((installed).toString())), style = T.s13, color = p.ink2)
        if (release.notes.isNotEmpty()) Text(release.notes, style = T.s15, color = p.ink, modifier = Modifier.padding(top = 8.dp))
        // Another protocol version: the computers must be upgraded too, or nothing connects.
        if (release.protocol != PROTOCOL_VERSION) Notice(copy(UiMessages.settingsAndroidThisReleaseChangesTheProtocolAfterInstallingAlsoUpdateRemote()), Modifier.padding(top = 10.dp), tone = NoticeTone.WARN)
    }
}
