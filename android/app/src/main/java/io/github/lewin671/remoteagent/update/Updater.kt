package io.github.lewin671.remoteagent.update

import android.content.Context
import android.content.Intent
import androidx.core.content.FileProvider
import androidx.core.content.pm.PackageInfoCompat
import io.github.lewin671.remoteagent.RaApp
import io.github.lewin671.remoteagent.core.Release
import io.github.lewin671.remoteagent.core.Updates
import io.github.lewin671.remoteagent.core.errorText
import io.github.lewin671.remoteagent.core.isNewer
import java.io.File
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch

/** Where downloaded APKs wait for the system's installer; see res/xml/file_paths.xml. */
const val UPDATES_DIR = "updates"

/** What is known of a newer release, and how far getting it is. */
sealed interface UpdateState {
    /** Nothing looked up yet in this run. */
    data object Unknown : UpdateState
    data object Checking : UpdateState
    /** This build is the latest, as read at [atMs]. */
    data class Latest(val atMs: Long) : UpdateState
    /** The lookup failed; nothing is known. */
    data class CheckFailed(val error: String) : UpdateState
    data class Available(val release: Release) : UpdateState
    data class Downloading(val release: Release, val done: Long) : UpdateState
    /** Downloaded and verified: only the system's confirmation is left. */
    data class Ready(val release: Release, val file: File) : UpdateState
    data class DownloadFailed(val release: Release, val error: String) : UpdateState
}

/** The newer release a state is about, if it is about one. */
val UpdateState.release: Release?
    get() = when (this) {
        is UpdateState.Available -> release
        is UpdateState.Downloading -> release
        is UpdateState.Ready -> release
        is UpdateState.DownloadFailed -> release
        else -> null
    }

/**
 * The app updating itself (client-v35): it looks for a newer release when
 * it comes to the foreground, at most every few hours, and on request;
 * downloading starts only when the user asks, and installing is the
 * system's dialog. Android installs the file only if it carries this
 * app's signature, whatever was downloaded.
 */
object Updater {
    private const val CHECK_EVERY_MS = 6 * 3600_000L
    private const val OFFICIAL_ID = "io.github.lewin671.remoteagent"

    private val _state = MutableStateFlow<UpdateState>(UpdateState.Unknown)
    val state: StateFlow<UpdateState> = _state.asStateFlow()
    private var checkedAt = 0L
    private var job: Job? = null

    /** Shows a state as if it had been reached, for rendering it in tests. */
    fun showForTest(state: UpdateState) {
        _state.value = state
    }

    /** Only the released app updates itself: a development copy has another signature and its own version. */
    fun enabled(app: RaApp): Boolean = app.packageName == OFFICIAL_ID && !app.debuggable && "-dev" !in app.versionName

    fun installedCode(app: RaApp): Long = PackageInfoCompat.getLongVersionCode(app.packageManager.getPackageInfo(app.packageName, 0))

    /** Looks for a newer release. Without [manual] only when the last look is a while ago, and failures stay silent. */
    fun check(app: RaApp, manual: Boolean = false) {
        if (!enabled(app) || job?.isActive == true) return
        val now = System.currentTimeMillis()
        if (!manual && now - checkedAt < CHECK_EVERY_MS) return
        // A release already found stays as it is until a newer look says otherwise.
        val before = _state.value
        if (before.release == null) _state.value = UpdateState.Checking
        job = app.scope.launch {
            try {
                val latest = Updates(app.http).latest()
                checkedAt = System.currentTimeMillis()
                if (!isNewer(latest, installedCode(app))) {
                    _state.value = UpdateState.Latest(checkedAt)
                    // What an earlier update left behind.
                    File(app.cacheDir, UPDATES_DIR).deleteRecursively()
                } else if (before.release != latest) _state.value = UpdateState.Available(latest)
            } catch (e: Exception) {
                if (e is kotlinx.coroutines.CancellationException) throw e
                if (before.release == null) _state.value = if (manual) UpdateState.CheckFailed(errorText(e).ifEmpty { "连不上发布地址" }) else UpdateState.Unknown
            }
        }
    }

    /** Downloads the release found, then hands it to the system's installer. */
    fun download(app: RaApp) {
        val release = _state.value.release ?: return
        if (job?.isActive == true) return
        _state.value = UpdateState.Downloading(release, 0)
        job = app.scope.launch {
            try {
                val file = Updates(app.http).download(release, File(app.cacheDir, UPDATES_DIR)) { done ->
                    _state.value = UpdateState.Downloading(release, done)
                }
                _state.value = UpdateState.Ready(release, file)
                install(app, file)
            } catch (e: Exception) {
                if (e is kotlinx.coroutines.CancellationException) throw e
                _state.value = UpdateState.DownloadFailed(release, errorText(e).ifEmpty { "下载失败" })
            }
        }
    }

    /** Opens the system's installer on a downloaded APK; the user confirms there. */
    fun install(context: Context, file: File) {
        val uri = FileProvider.getUriForFile(context, "${context.packageName}.files", file)
        context.startActivity(
            Intent(Intent.ACTION_VIEW).setDataAndType(uri, "application/vnd.android.package-archive")
                .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK),
        )
    }
}
