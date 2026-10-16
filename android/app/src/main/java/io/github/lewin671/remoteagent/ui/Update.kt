package io.github.lewin671.remoteagent.ui

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
fun updateWord(state: UpdateState): String? = when (state) {
    is UpdateState.Available, is UpdateState.DownloadFailed -> "可更新到 ${state.release!!.version}"
    is UpdateState.Downloading -> "正在下载 ${state.release.version}"
    is UpdateState.Ready -> "${state.release.version} 已下载"
    else -> null
}

/**
 * The one line the sheet writes about the last look, under what the look was about: why a look
 * the user asked for failed, or that the release on screen is still the latest published one.
 */
data class CheckLine(val text: String, val error: Boolean)

fun checkLine(check: UpdateCheck, releaseOnScreen: Boolean): CheckLine? = when {
    // The reason comes first: it is what the user needs next, and a failure confirms nothing.
    check.error != null -> CheckLine("没能检查更新：${check.error}", error = true)
    check.upToDate && releaseOnScreen -> CheckLine("已是最新发布版本", error = false)
    else -> null
}

/** A pending installation may continue only after checking, for the exact release requested. */
internal fun readyForInstall(state: UpdateState, check: UpdateCheck, versionCode: Long): UpdateState.Ready? =
    (state as? UpdateState.Ready)?.takeIf { !check.running && it.release.versionCode == versionCode }

/** The download/install action for a release, by how far getting it is. */
data class UpdateStep(val label: String, val enabled: Boolean, val error: String = "")

fun updateStep(state: UpdateState): UpdateStep? = when (state) {
    is UpdateState.Available -> UpdateStep("下载并安装（${mibText(state.release.size)}）", true)
    is UpdateState.Downloading -> UpdateStep("正在下载 ${mibText(state.done)} / ${mibText(state.release.size)}", false)
    is UpdateState.Ready -> UpdateStep("安装", true)
    is UpdateState.DownloadFailed -> UpdateStep("重新下载", true, state.error)
    else -> null
}

/**
 * 有新版本: one line on the home page while a newer release is known and has not been put off
 * (client-v35). It opens the same sheet as the 版本 row in 设置.
 */
@Composable
fun UpdateNotice(modifier: Modifier = Modifier, onOpen: () -> Unit) {
    val app = RaApp.instance
    val state by Updater.state.collectAsState()
    val settings by app.prefs.settings.collectAsState(Settings())
    val release = state.release ?: return
    if (release.versionCode <= settings.skippedUpdate) return
    Banner("有新版本 ${release.version}", modifier, action = "更新", onAction = onOpen)
}

/** How handing the download to the system went, when there is something to say about it. */
enum class InstallNote(val text: String, val error: Boolean = false) {
    PERMISSION("先在系统设置里允许 remote-agent 安装应用，回来后继续。"),
    CANCELLED("安装没有完成，可以再装一次。"),
    FAILED("系统没能安装这个包。", error = true),
    NO_INSTALLER("打不开系统的安装器。", error = true),
}

/** 版本: the installed version, and the newer release when there is one, with what changed and the one step to take next. */
@Composable
fun UpdateSheet(onClose: () -> Unit) {
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
            SheetHead("版本", "完成", onClose)
            Text("remote-agent ${app.versionName}", style = T.body, color = p.fg)
            if (state is UpdateState.Latest) Text("已是最新版本", style = T.sm, color = p.muted, modifier = Modifier.padding(top = 6.dp))
            checkLine(check, releaseOnScreen = false)?.let { Text(it.text, style = T.sm, color = p.red, modifier = Modifier.padding(top = 6.dp)) }
            Btn(if (check.running) "正在检查更新…" else "检查更新", { Updater.check(app, manual = true) }, Modifier.fillMaxWidth().padding(top = 14.dp), enabled = !check.running)
            return@Sheet
        }
        SheetHead("新版本 ${release.version}", "以后再说", {
            // Put off: the home page stops mentioning this release; 设置 still shows it. Saved in
            // the app's scope: this sheet's own ends with it, before the write may be through.
            app.scope.launch { app.prefs.update { it.copy(skippedUpdate = release.versionCode) } }
            onClose()
        })
        ReleaseNotes(release, app.versionName)
        val step = updateStep(state) ?: return@Sheet
        if (step.error.isNotEmpty()) Text("没能下载：${step.error}", style = T.sm, color = p.red, modifier = Modifier.padding(top = 10.dp))
        shown?.let { Text(it.text, style = T.sm, color = if (it.error) p.red else p.orangeInk, modifier = Modifier.padding(top = 10.dp)) }
        Btn(if (shown == InstallNote.PERMISSION) "去允许" else step.label, {
            if (Updater.checking.value.running) return@Btn
            val s = Updater.state.value
            if (s is UpdateState.Ready) install(s.release.versionCode, s.file) else Updater.download(app)
        }, Modifier.fillMaxWidth().padding(top = 14.dp), BtnKind.PRIMARY, enabled = step.enabled && !check.running)
        Hint("下载后由系统确认安装。第一次会请你允许 remote-agent 安装应用。")
        // How the look for a newer release went. Either way the release above stays, and stays actionable.
        checkLine(check, releaseOnScreen = true)?.let {
            Text(it.text, style = T.sm, color = if (it.error) p.red else p.muted, modifier = Modifier.padding(top = 6.dp))
        }
        TextAction(
            if (check.running) "正在检查更新…" else "检查更新",
            { Updater.check(app, manual = true) },
            Modifier.fillMaxWidth().padding(top = 8.dp),
            style = T.bodyB,
            enabled = !check.running && state !is UpdateState.Downloading,
            alignment = Alignment.Center,
        )
    }
}

@Composable
private fun ReleaseNotes(release: Release, installed: String) {
    val p = pal
    Column {
        Text("当前版本 $installed", style = T.sm, color = p.muted)
        if (release.notes.isNotEmpty()) Text(release.notes, style = T.body, color = p.fg2, modifier = Modifier.padding(top = 8.dp))
        // Another protocol version: the computers must be upgraded too, or nothing connects.
        if (release.protocol != PROTOCOL_VERSION) Banner("这一版换了协议版本：装好后，电脑上的 remote-agent 也要升级才能继续用。", Modifier.padding(top = 10.dp), tone = Tone2.WARN)
    }
}
