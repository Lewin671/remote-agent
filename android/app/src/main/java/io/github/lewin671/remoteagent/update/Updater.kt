package io.github.lewin671.remoteagent.update

import android.content.ClipData
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
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/**
 * Where a downloaded APK waits for the system's installer, under the app's files (res/xml/file_paths.xml):
 * the cache may be cleared by the system while the installer still reads from it.
 */
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
 *
 * The installer is opened by the screen that is in front, never from
 * here: an activity started from the background is dropped by Android 10
 * and later, and a download can end after the app was left.
 */
object Updater {
    private const val CHECK_EVERY_MS = 6 * 3600_000L
    private const val OFFICIAL_ID = "io.github.lewin671.remoteagent"

    private val _state = MutableStateFlow<UpdateState>(UpdateState.Unknown)
    val state: StateFlow<UpdateState> = _state.asStateFlow()
    private var checkedAt = 0L

    /** The user asked to install what is being downloaded: the screen in front opens the installer once it is here. */
    @Volatile var installWhenReady = false
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
                    File(app.filesDir, UPDATES_DIR).deleteRecursively()
                } else if (before.release != latest) _state.value = UpdateState.Available(latest)
            } catch (e: Exception) {
                if (e is kotlinx.coroutines.CancellationException) throw e
                if (before.release == null) _state.value = if (manual) UpdateState.CheckFailed(errorText(e).ifEmpty { "连不上发布地址" }) else UpdateState.Unknown
            }
        }
    }

    /** Downloads the release found; the screen that asked then opens the installer on it. */
    fun download(app: RaApp) {
        val release = _state.value.release ?: return
        if (job?.isActive == true) return
        installWhenReady = true
        _state.value = UpdateState.Downloading(release, 0)
        job = app.scope.launch {
            try {
                val file = Updates(app.http).download(release, File(app.filesDir, UPDATES_DIR)) { done ->
                    _state.value = UpdateState.Downloading(release, done)
                }
                val refused = withContext(Dispatchers.IO) { notThisApp(app, file, release) }
                if (refused != null) {
                    file.delete()
                    throw java.io.IOException(refused)
                }
                _state.value = UpdateState.Ready(release, file)
            } catch (e: Exception) {
                if (e is kotlinx.coroutines.CancellationException) throw e
                installWhenReady = false
                // The mirror holds the latest release only: one published since this was found has
                // replaced it, and trying the same file again would fail for ever.
                val latest = runCatching { Updates(app.http).latest() }.getOrNull()
                _state.value = if (latest != null && latest != release && isNewer(latest, installedCode(app))) UpdateState.Available(latest)
                else UpdateState.DownloadFailed(release, errorText(e).ifEmpty { "下载失败" })
            }
        }
    }

    /**
     * Why [file] must not go to the installer, or null when it is this app's update. The
     * system checks the signature only of a package that replaces an installed one: a
     * package of another name would be installed as a new app, whoever signed it. So
     * whoever could change the mirror must not get that far: the package has to carry
     * this app's name, which leaves the signature to the system, and the version the
     * manifest promised.
     */
    fun notThisApp(app: RaApp, file: File, release: Release): String? {
        val theirs = app.packageManager.getPackageArchiveInfo(file.path, 0) ?: return "下载的文件不是安装包"
        return packageMismatch(app.packageName, theirs.packageName, PackageInfoCompat.getLongVersionCode(theirs), release)
    }

    /** [notThisApp] on what was read from the package. */
    fun packageMismatch(installedName: String, name: String?, versionCode: Long, release: Release): String? = when {
        name != installedName -> "下载的安装包不是 remote-agent"
        versionCode != release.versionCode -> "下载的安装包和发布信息不一致"
        else -> null
    }

    /**
     * Whether Android lets this app hand packages to the installer: the user allows it per
     * app, under 安装未知应用. Asked before the installer is opened, because not every
     * vendor's installer offers the way to that setting itself.
     */
    fun canInstall(context: Context): Boolean = context.packageManager.canRequestPackageInstalls()

    /** The system page where the user allows this app to install packages. */
    fun permissionIntent(context: Context): Intent =
        Intent(android.provider.Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, android.net.Uri.parse("package:${context.packageName}"))

    /**
     * The system's installer on a downloaded APK, to start for its result from an activity
     * in front. The file goes as a content URI the installer alone may read: the form that
     * vendor installers (MIUI among them) accept where a PackageInstaller session is
     * aborted without a word. The user confirms there.
     */
    @Suppress("DEPRECATION") // ACTION_INSTALL_PACKAGE returns a result; ACTION_VIEW does not.
    fun installIntent(context: Context, file: File): Intent {
        val uri = FileProvider.getUriForFile(context, "${context.packageName}.files", file)
        return Intent(Intent.ACTION_INSTALL_PACKAGE).apply {
            setDataAndType(uri, "application/vnd.android.package-archive")
            clipData = ClipData.newRawUri("remote-agent", uri)
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            putExtra(Intent.EXTRA_RETURN_RESULT, true)
        }
    }
}
