// The app updating itself: what the published release says, and fetching
// its APK. The source repository is private, so releases are mirrored as
// one manifest and one APK in a public repository that holds nothing else
// (architecture §1 「产品版本与发布」).
package io.github.lewin671.remoteagent.core

import java.io.File
import java.io.IOException
import java.io.StringReader
import java.security.MessageDigest
import java.util.Properties
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.withContext
import okhttp3.OkHttpClient
import okhttp3.Request

/** Where the manifest and the APK it names are published. */
const val RELEASES_URL = "https://raw.githubusercontent.com/Lewin671/remote-agent-releases/main/"
const val RELEASE_MANIFEST = "latest.properties"

/** The latest published release, as its manifest says. */
data class Release(
    val version: String,
    val versionCode: Long,
    /** The protocol version it speaks: another one than the daemon's does not work with it. */
    val protocol: Int,
    /** The APK's file name next to the manifest. */
    val apk: String,
    val sha256: String,
    val size: Long,
    /** What changed and what upgrading needs, as written for the release. */
    val notes: String,
)

private val APK_NAME = Regex("""remote-agent-v\d+\.\d+\.\d+\.apk""")
private val SHA256 = Regex("[0-9a-f]{64}")
/** No APK of this app is near this size; a manifest that says so is not one of ours. */
private const val MAX_APK_BYTES = 200L * 1024 * 1024

/**
 * Reads a manifest: `key=value` lines as in version.properties. Anything
 * missing or out of shape is refused, so a broken manifest offers nothing
 * rather than something that cannot be installed.
 */
fun parseRelease(text: String): Release {
    val p = Properties().apply { load(StringReader(text)) }
    fun field(name: String) = p.getProperty(name)?.trim().takeUnless { it.isNullOrEmpty() } ?: throw IOException("发布信息缺少 $name")
    val release = Release(
        version = field("version"),
        versionCode = field("versionCode").toLongOrNull() ?: throw IOException("发布信息的 versionCode 不是数字"),
        protocol = field("protocol").toIntOrNull() ?: throw IOException("发布信息的 protocol 不是数字"),
        apk = field("apk"),
        sha256 = field("sha256").lowercase(),
        size = field("size").toLongOrNull() ?: throw IOException("发布信息的 size 不是数字"),
        notes = p.getProperty("notes").orEmpty().trim(),
    )
    if (!APK_NAME.matches(release.apk) || !SHA256.matches(release.sha256) || release.versionCode <= 0 || release.size !in 1..MAX_APK_BYTES)
        throw IOException("发布信息格式不对")
    return release
}

/** Whether [release] is newer than the installed build. Only the version code decides, as it does for the system. */
fun isNewer(release: Release, installedCode: Long): Boolean = release.versionCode > installedCode

class Updates(private val http: OkHttpClient, private val base: String = RELEASES_URL) {
    /** The latest published release. */
    suspend fun latest(): Release = withContext(Dispatchers.IO) {
        http.newCall(Request.Builder().url(base + RELEASE_MANIFEST).header("Cache-Control", "no-cache").build()).execute().use { res ->
            if (!res.isSuccessful) throw IOException("读取发布信息失败（${res.code}）")
            parseRelease(res.body?.string().orEmpty())
        }
    }

    /**
     * Downloads [release]'s APK into [into] and returns it only if it is the
     * file the manifest describes, by size and SHA-256; otherwise nothing is
     * left behind. A copy already there that matches is kept.
     */
    suspend fun download(release: Release, into: File, progress: (done: Long) -> Unit = {}): File = withContext(Dispatchers.IO) {
        val file = File(into, release.apk)
        if (file.length() == release.size && sha256(file) == release.sha256) return@withContext file
        into.mkdirs()
        val part = File(into, release.apk + ".part")
        try {
            http.newCall(Request.Builder().url(base + release.apk).build()).execute().use { res ->
                if (!res.isSuccessful) throw IOException("下载失败（${res.code}）")
                val body = res.body ?: throw IOException("下载失败")
                part.outputStream().use { out ->
                    val buf = ByteArray(64 * 1024)
                    var done = 0L
                    val src = body.byteStream()
                    while (true) {
                        ensureActive()
                        val n = src.read(buf)
                        if (n < 0) break
                        done += n
                        // More than was promised is not this release's file: stop before filling the disk.
                        if (done > release.size) throw IOException("下载的文件和发布信息不一致")
                        out.write(buf, 0, n)
                        progress(done)
                    }
                }
            }
            if (part.length() != release.size || sha256(part) != release.sha256) throw IOException("下载的文件和发布信息不一致")
            if (!part.renameTo(file)) throw IOException("没能保存安装包")
            file
        } finally {
            part.delete()
        }
    }
}

private fun sha256(file: File): String {
    val md = MessageDigest.getInstance("SHA-256")
    file.inputStream().use { src ->
        val buf = ByteArray(64 * 1024)
        while (true) {
            val n = src.read(buf)
            if (n < 0) break
            md.update(buf, 0, n)
        }
    }
    return md.digest().joinToString("") { "%02x".format(it) }
}
