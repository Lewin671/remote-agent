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
    /** Nothing known about a newer release: not looked up yet in this run, or the last look said nothing. */
    data object Unknown : UpdateState
    /** This build is the latest, as read at [atMs]. */
    data class Latest(val atMs: Long) : UpdateState
    data class Available(val release: Release) : UpdateState
    data class Downloading(val release: Release, val done: Long) : UpdateState
    /** Downloaded and verified: only the system's confirmation is left. */
    data class Ready(val release: Release, val file: File) : UpdateState
    data class DownloadFailed(val release: Release, val error: String) : UpdateState
}

/**
 * How the look for a newer release is going, or how the last one went. Kept apart from
 * [UpdateState] because a release that was already found stays on screen, and stays
 * usable, while a new look runs and afterwards too, whether that look failed or came
 * back with the same release.
 */
data class UpdateCheck(
    /** A look is in flight: nothing that depends on what is known should be acted on. */
    val running: Boolean = false,
    /** Why the last look the user asked for failed; one nobody asked for says nothing. */
    val error: String? = null,
    /** The last look found the release already known to be the latest published one. */
    val upToDate: Boolean = false,
)

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
 * The app updating itself (client-v35, rechecking as in client-v42): it looks
 * for a newer release when it comes to the foreground, at most every few hours,
 * and on request; a look never takes away a release already found unless a
 * newer one takes its place. Downloading starts only when the user asks, and
 * installing is the system's dialog. Android installs the file only if it
 * carries this app's signature, whatever was downloaded.
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
    private val _checking = MutableStateFlow(UpdateCheck())
    val checking: StateFlow<UpdateCheck> = _checking.asStateFlow()
    private var checkedAt = 0L

    /** The user asked to install what is being downloaded: the screen in front opens the installer once it is here. */
    @Volatile var installWhenReady = false
    private var job: Job? = null

    /** Shows a state and a look as if they had been reached, for rendering them in tests. */
    fun showForTest(state: UpdateState, check: UpdateCheck = UpdateCheck()) {
        _state.value = state
        _checking.value = check
    }

    /** Only the released app updates itself: a development copy has another signature and its own version. */
    fun enabled(app: RaApp): Boolean = app.packageName == OFFICIAL_ID && !app.debuggable && "-dev" !in app.versionName

    fun installedCode(app: RaApp): Long = PackageInfoCompat.getLongVersionCode(app.packageManager.getPackageInfo(app.packageName, 0))

    /** Whether a look is due: the user asked for one, or the last took place a while ago. */
    internal fun due(manual: Boolean, now: Long, lastAtMs: Long = checkedAt): Boolean =
        manual || now - lastAtMs >= CHECK_EVERY_MS

    /**
     * Looks for a newer release. A look the user asked for happens whatever [due] says and
     * reports why it failed; one nobody asked for keeps quiet. Either way a release already
     * found stays on screen and installable, and a look already under way is not joined.
     */
    fun check(app: RaApp, manual: Boolean = false) {
        if (!enabled(app) || job?.isActive == true) return
        if (!due(manual, System.currentTimeMillis())) return
        // Set before the coroutine starts: until the answer is in, the sheet must not offer to download.
        _checking.value = UpdateCheck(running = true)
        job = app.scope.launch {
            refresh(manual, installedCode(app), { Updates(app.http).latest() }) {
                File(app.filesDir, UPDATES_DIR).deleteRecursively()
            }
        }
    }

    /** One look. A release already on screen is left alone unless a newer one takes its place. */
    internal suspend fun refresh(manual: Boolean, installedCode: Long, latest: suspend () -> Release, clearDownloads: () -> Unit) {
        val before = _state.value
        _checking.value = UpdateCheck(running = true)
        try {
            val found = latest()
            checkedAt = System.currentTimeMillis()
            when {
                // This build is the latest: nothing downloaded for an older one is worth keeping.
                !isNewer(found, installedCode) -> {
                    _checking.value = UpdateCheck()
                    _state.value = UpdateState.Latest(checkedAt)
                    clearDownloads()
                }
                // The release already on screen is still the latest published one: its verified APK stays.
                found == before.release -> _checking.value = UpdateCheck(upToDate = true)
                else -> {
                    _checking.value = UpdateCheck()
                    // Another release takes its place: what was offered or downloaded is not what would be installed now.
                    // The system installer may still be reading a previously handed-off APK.
                    // Leave files until this app is installed and a check finds it current.
                    _state.value = UpdateState.Available(found)
                }
            }
        } catch (e: Exception) {
            if (e is kotlinx.coroutines.CancellationException) throw e
            val error = errorText(e).ifEmpty { "连不上发布地址" }
            // A failure says nothing about what is already known, and only a look the user asked for says why.
            _checking.value = UpdateCheck(error = error.takeIf { manual })
        } finally {
            // A look the app was taken out of leaves nothing running behind it.
            if (_checking.value.running) _checking.value = _checking.value.copy(running = false)
        }
    }

    /** Downloads the release found; the screen that asked then opens the installer on it. */
    fun download(app: RaApp) {
        val release = _state.value.release ?: return
        if (job?.isActive == true) return
        installWhenReady = true
        // What a look failed at says nothing about getting this release.
        _checking.value = _checking.value.copy(error = null)
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
                _state.value = UpdateState.DownloadFailed(release, errorText(e).ifEmpty { "下载失败" })
            }
        }
    }

    /**
     * Why [file] must not go to the installer, or null when it is this app's update. The
     * system checks the signature only of a package that replaces an installed one: a
     * package of another name would be installed as a new app, whoever signed it. So
     * whoever could change the public releases must not get that far: the package has to carry
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
