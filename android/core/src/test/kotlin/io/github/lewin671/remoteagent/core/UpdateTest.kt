package io.github.lewin671.remoteagent.core

import java.io.File
import java.io.IOException
import java.nio.file.Files
import java.security.MessageDigest
import kotlinx.coroutines.runBlocking
import okhttp3.Interceptor
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Protocol
import okhttp3.Response
import okhttp3.ResponseBody.Companion.toResponseBody
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertThrows
import org.junit.Assert.assertTrue
import org.junit.Test

class UpdateTest {
    private val apk = ByteArray(5000) { (it % 251).toByte() }
    private val sum = MessageDigest.getInstance("SHA-256").digest(apk).joinToString("") { "%02x".format(it) }

    private fun manifest(sha: String = sum, size: Int = apk.size, name: String = "remote-agent-v0.29.0.apk") = """
        version=0.29.0
        versionCode=82
        protocol=4
        apk=$name
        sha256=$sha
        size=$size
        notes=项目列表统一。\n协议版本不变。
    """.trimIndent()

    @Test
    fun asksForTheLatestManifestAndForTheApkUnderItsOwnTag() = runBlocking {
        val asked = mutableListOf<String>()
        val http = OkHttpClient.Builder().addInterceptor(Interceptor { chain ->
            asked += chain.request().url.encodedPath
            val body = if (chain.request().url.pathSegments.last() == RELEASE_MANIFEST) manifest().toByteArray() else apk
            Response.Builder().request(chain.request()).protocol(Protocol.HTTP_1_1).code(200).message("OK")
                .body(body.toResponseBody("application/octet-stream".toMediaType())).build()
        }).build()
        val updates = Updates(http, "https://releases.test/r/")
        updates.download(updates.latest(), dir())
        assertEquals(listOf("/r/latest/download/latest.properties", "/r/download/v0.29.0/remote-agent-v0.29.0.apk"), asked)
    }

    /** A client whose every request is answered from [files], by the last path segment. */
    private fun http(files: Map<String, ByteArray>): OkHttpClient = OkHttpClient.Builder().addInterceptor(Interceptor { chain ->
        val body = files[chain.request().url.pathSegments.last()]
        Response.Builder().request(chain.request()).protocol(Protocol.HTTP_1_1)
            .code(if (body == null) 404 else 200).message(if (body == null) "Not Found" else "OK")
            .body((body ?: ByteArray(0)).toResponseBody("application/octet-stream".toMediaType())).build()
    }).build()

    private fun dir(): File = Files.createTempDirectory("ra-update").toFile()

    @Test
    fun readsTheManifest() {
        val r = parseRelease(manifest())
        assertEquals("0.29.0", r.version)
        assertEquals(82L, r.versionCode)
        assertEquals(4, r.protocol)
        assertEquals("项目列表统一。\n协议版本不变。", r.notes)
        assertTrue(isNewer(r, 81))
        assertFalse(isNewer(r, 82))
    }

    @Test
    fun readsNotesAsTheReleaseScriptWritesThem() {
        // The line scripts/release_test.py expects of a message with line breaks, a backslash, and "=".
        val notes = "notes=项目列表统一。\\n路径 C:\\\\tmp = 不变\\n协议版本不变 #1"
        val r = parseRelease(manifest().lines().filterNot { it.startsWith("notes=") }.joinToString("\n") + "\n" + notes + "\n")
        assertEquals("项目列表统一。\n路径 C:\\tmp = 不变\n协议版本不变 #1", r.notes)
    }

    @Test
    fun refusesAManifestThatCouldNotBeInstalled() {
        // A name that could leave the folder, a checksum that is not one, a field left out.
        assertThrows(IOException::class.java) { parseRelease(manifest(name = "../remote-agent-v0.29.0.apk")) }
        assertThrows(IOException::class.java) { parseRelease(manifest(sha = "abc")) }
        // The file name is the version's and nothing else: both go into the address it is fetched from.
        assertThrows(IOException::class.java) { parseRelease(manifest(name = "remote-agent-v0.29.9.apk")) }
        assertThrows(IOException::class.java) { parseRelease(manifest().replace("version=0.29.0", "version=0.29.0/../x")) }
        assertThrows(IOException::class.java) { parseRelease(manifest().lines().filterNot { it.startsWith("size=") }.joinToString("\n")) }
        assertThrows(IOException::class.java) { parseRelease("<html>not found</html>") }
    }

    @Test
    fun downloadsTheApkTheManifestDescribes() = runBlocking {
        val updates = Updates(http(mapOf(RELEASE_MANIFEST to manifest().toByteArray(), "remote-agent-v0.29.0.apk" to apk)), "https://releases.test/")
        val into = dir()
        var seen = 0L
        val file = updates.download(updates.latest(), into) { seen = it }
        assertArrayEquals(apk, file.readBytes())
        assertEquals(apk.size.toLong(), seen)
        assertEquals(listOf("remote-agent-v0.29.0.apk"), into.list()!!.toList())
    }

    @Test
    fun keepsNothingOfAFileThatIsNotTheRelease() = runBlocking {
        val into = dir()
        // Same length, other bytes; then a longer file than promised.
        for (served in listOf(ByteArray(apk.size), apk + apk)) {
            val updates = Updates(http(mapOf("remote-agent-v0.29.0.apk" to served)), "https://releases.test/")
            assertThrows(IOException::class.java) { runBlocking { updates.download(parseRelease(manifest()), into) } }
            assertEquals(emptyList<String>(), into.list()!!.toList())
        }
    }

    @Test
    fun usesACopyAlreadyDownloaded() = runBlocking {
        val into = dir()
        File(into, "remote-agent-v0.29.0.apk").writeBytes(apk)
        // Nothing is served: the copy on disk is the release's file.
        val file = Updates(http(emptyMap()), "https://releases.test/").download(parseRelease(manifest()), into)
        assertArrayEquals(apk, file.readBytes())
    }
}
