package io.github.lewin671.remoteagent.ui

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.PROTOCOL_VERSION
import io.github.lewin671.remoteagent.core.Release
import io.github.lewin671.remoteagent.data.Settings
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

/** What the sheet's one button says and does for a release, by how far getting it is. */
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

/** 版本: the installed version, and the newer release when there is one, with what changed and the one step to take next. */
@Composable
fun UpdateSheet(onClose: () -> Unit) {
    val app = RaApp.instance
    val p = pal
    val state by Updater.state.collectAsState()
    val scope = rememberCoroutineScope()
    val release = state.release
    Sheet(onClose) {
        if (release == null) {
            SheetHead("版本", "完成", onClose)
            Text("remote-agent ${app.versionName}", style = T.body, color = p.fg)
            when (val s = state) {
                UpdateState.Checking -> IconText(Lucide.LoaderCircle, "正在检查更新…", p.muted, T.sm, Modifier.padding(top = 6.dp))
                is UpdateState.Latest -> Text("已是最新版本", style = T.sm, color = p.muted, modifier = Modifier.padding(top = 6.dp))
                is UpdateState.CheckFailed -> Text("没能检查更新：${s.error}", style = T.sm, color = p.red, modifier = Modifier.padding(top = 6.dp))
                else -> {}
            }
            Btn("检查更新", { Updater.check(app, manual = true) }, Modifier.fillMaxWidth().padding(top = 14.dp), enabled = state != UpdateState.Checking)
            return@Sheet
        }
        SheetHead("新版本 ${release.version}", "以后再说", {
            // Put off: the home page stops mentioning this release; 设置 still shows it.
            scope.launch { app.prefs.update { it.copy(skippedUpdate = release.versionCode) } }
            onClose()
        })
        ReleaseNotes(release, app.versionName)
        val step = updateStep(state) ?: return@Sheet
        if (step.error.isNotEmpty()) Text("没能下载：${step.error}", style = T.sm, color = p.red, modifier = Modifier.padding(top = 10.dp))
        Btn(step.label, {
            val s = Updater.state.value
            if (s is UpdateState.Ready) Updater.install(app, s.file) else Updater.download(app)
        }, Modifier.fillMaxWidth().padding(top = 14.dp), BtnKind.PRIMARY, enabled = step.enabled)
        Hint("下载后由系统确认安装。第一次会请你允许 remote-agent 安装应用。")
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
