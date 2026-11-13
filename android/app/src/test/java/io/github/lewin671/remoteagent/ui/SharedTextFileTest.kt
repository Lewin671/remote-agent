package io.github.lewin671.remoteagent.ui

import android.app.Application
import android.content.ComponentName
import android.content.Intent
import android.content.IntentFilter
import androidx.test.core.app.ApplicationProvider
import io.github.lewin671.remoteagent.data.SharedFileManager
import io.github.lewin671.remoteagent.data.SharedTextMaxBytes
import io.github.lewin671.remoteagent.data.isSharedMarkdownFile
import io.github.lewin671.remoteagent.data.readSharedTextFile
import io.github.lewin671.remoteagent.proto.SharedFile
import java.io.File
import java.util.Locale
import org.junit.Assert.*
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class SharedTextFileTest {
    @get:Rule val temporary = TemporaryFolder()
    private val context: Application = ApplicationProvider.getApplicationContext()
    private lateinit var manager: SharedFileManager
    private lateinit var local: File
    private val extensions = "md markdown txt log json yaml yml toml csv tsv xml ini conf env sh py js ts tsx jsx go kt swift java c h cpp rs rb sql diff patch html".split(" ")

    @Before fun setup() {
        // Robolectric changes cache directories between tests; FileProvider keeps a JVM-wide map.
        (androidx.core.content.FileProvider::class.java.getDeclaredField("sCache").apply { isAccessible = true }
            .get(null) as MutableMap<*, *>).clear()
        manager = SharedFileManager(context) { null }
        local = File(manager.rootDir, "text-test/document").apply { parentFile!!.mkdirs(); writeText("hello") }
    }

    @Test fun routesEverySpecifiedExtensionCaseInsensitively() {
        val before = Locale.getDefault()
        try {
            Locale.setDefault(Locale.forLanguageTag("tr-TR"))
            for (ext in extensions) for (name in listOf("a.$ext", "a.${ext.uppercase(Locale.ROOT)}")) {
                assertEquals(name, SharedFileOpenRoute.TEXT_VIEWER, sharedFileOpenRoute(name))
            }
            assertEquals(SharedFileOpenRoute.TEXT_VIEWER, sharedFileOpenRoute(".env"))
            for (name in listOf("noext", "md", "file.", "file.cfg", "file.properties", "file.htm", "a.md.zip")) {
                assertEquals(name, SharedFileOpenRoute.SYSTEM_CHOOSER, sharedFileOpenRoute(name))
            }
        } finally { Locale.setDefault(before) }
    }

    @Test fun markdownIsLimitedToItsTwoExtensions() {
        assertTrue(isSharedMarkdownFile("readme.MD"))
        assertTrue(isSharedMarkdownFile("readme.Markdown"))
        assertFalse(isSharedMarkdownFile("readme.txt"))
        assertFalse(isSharedMarkdownFile("readme.html"))
    }

    @Test fun readsEmptyUnicodeAndBomWithoutChangingSource() {
        val file = temporary.newFile()
        assertEquals("", readSharedTextFile(file))
        file.writeText("\uFEFF中文 🐈\r\n<script>alert(1)</script>")
        assertEquals("中文 🐈\r\n<script>alert(1)</script>", readSharedTextFile(file))
    }

    @Test fun acceptsExactlyOneMiBAndRejectsOneByteMore() {
        val file = temporary.newFile()
        file.writeBytes(ByteArray(SharedTextMaxBytes) { 65 })
        assertEquals(SharedTextMaxBytes, readSharedTextFile(file)?.length)
        file.appendText("a")
        assertNull(readSharedTextFile(file))
    }

    @Test fun nulProbeCoversExactlyTheFirstEightKiB() {
        val file = temporary.newFile()
        for (position in listOf(0, 8191)) {
            file.writeBytes(ByteArray(9000) { if (it == position) 0 else 65 })
            assertNull(readSharedTextFile(file))
        }
        file.writeBytes(ByteArray(9000) { if (it == 8192) 0 else 65 })
        assertEquals(9000, readSharedTextFile(file)?.length)
    }

    @Test fun validatesUtf8PastTheProbeAndRejectsTruncatedSequences() {
        val file = temporary.newFile()
        for (bad in listOf(byteArrayOf(0xC0.toByte(), 0xAF.toByte()), byteArrayOf(0xFF.toByte()), byteArrayOf(0xE2.toByte(), 0x82.toByte()))) {
            file.writeBytes(ByteArray(9000) { 65 } + bad)
            assertNull(readSharedTextFile(file))
        }
        file.writeBytes(ByteArray(8191) { 65 } + "中".toByteArray())
        assertTrue(readSharedTextFile(file)!!.endsWith("中"))
    }

    @Test fun missingOrDirectoryFallsBack() {
        assertNull(readSharedTextFile(File(temporary.root, "missing")))
        assertNull(readSharedTextFile(temporary.root))
    }

    private fun handler(pkg: String, type: String, activity: String = "Main") {
        val component = ComponentName(pkg, "$pkg.$activity")
        shadowOf(context.packageManager).apply {
            addActivityIfNotPresent(component)
            addIntentFilterForActivity(component, IntentFilter(Intent.ACTION_VIEW).apply {
                addCategory(Intent.CATEGORY_DEFAULT)
                addDataType(type)
            })
        }
    }

    @Suppress("DEPRECATION")
    private fun target(chooser: Intent): Intent = chooser.getParcelableExtra(Intent.EXTRA_INTENT)!!

    @Test fun allTextTypesUsePlainTextOnlyForExternalViewing() {
        handler("com.example.reader", "*/*")
        for (ext in extensions) {
            assertTrue(manager.openExternal(context, local, "a.$ext"))
            val chooser = shadowOf(context).nextStartedActivity
            val view = target(chooser)
            assertEquals(Intent.ACTION_VIEW, view.action)
            assertEquals("text/plain", view.type)
            assertEquals(manager.contentUri(local), view.data)
            assertTrue(view.flags and Intent.FLAG_GRANT_READ_URI_PERMISSION != 0)
            assertNull(chooser.getParcelableArrayExtra(Intent.EXTRA_INITIAL_INTENTS))
        }
        manager.shareFile(context, local, "a.html")
        assertEquals(manager.mimeTypeFor("a.html"), target(shadowOf(context).nextStartedActivity).type)
    }

    @Test fun markdownReadersAreExplicitInitialIntentsWithoutDuplicateApps() {
        handler("com.example.generic", "*/*")
        handler("com.example.plain", "text/plain")
        handler("com.example.plain", "text/markdown", "Markdown")
        handler("com.example.markdown", "text/markdown")
        handler("com.example.markdown", "text/markdown", "Second")
        for (name in listOf("notes.md", "notes.MARKDOWN")) {
            assertTrue(manager.openExternal(context, local, name))
            val chooser = shadowOf(context).nextStartedActivity
            assertEquals(Intent.ACTION_CHOOSER, chooser.action)
            assertEquals(name, chooser.getCharSequenceExtra(Intent.EXTRA_TITLE))
            assertFalse(chooser.getBooleanExtra(Intent.EXTRA_AUTO_LAUNCH_SINGLE_CHOICE, true))
            val initial = chooser.getParcelableArrayExtra(Intent.EXTRA_INITIAL_INTENTS)!!
            assertEquals(1, initial.size)
            val markdown = initial.single() as Intent
            assertEquals("com.example.markdown", markdown.component!!.packageName)
            assertEquals("text/markdown", markdown.type)
            assertEquals(target(chooser).data, markdown.data)
            assertTrue(markdown.flags and Intent.FLAG_GRANT_READ_URI_PERMISSION != 0)
        }
    }

    @Test fun markdownOnlyReaderStillOpensAndNoReaderReturnsFalse() {
        assertFalse(manager.openExternal(context, local, "notes.md"))
        assertNull(shadowOf(context).nextStartedActivity)
        handler("com.example.markdown", "text/markdown")
        assertTrue(manager.openExternal(context, local, "notes.md"))
        assertEquals(1, shadowOf(context).nextStartedActivity.getParcelableArrayExtra(Intent.EXTRA_INITIAL_INTENTS)!!.size)
    }

    @Test fun nonTextTypesKeepTheirMimeAndNeverAddMarkdownReaders() {
        handler("com.example.reader", "*/*")
        handler("com.example.markdown", "text/markdown")
        for (name in listOf("photo.jpg", "document.pdf", "archive.zip", "clip.mp4")) {
            assertTrue(manager.openExternal(context, local, name))
            val chooser = shadowOf(context).nextStartedActivity
            assertEquals(manager.mimeTypeFor(name), target(chooser).type)
            assertNull(chooser.getParcelableArrayExtra(Intent.EXTRA_INITIAL_INTENTS))
        }
    }

    @Test fun openingTextDefersIoToTheHostAndDoesNotStartAnActivity() {
        val host = SharedFileHostState(manager, "text-test", "Test computer", true, false)
        val file = SharedFile.newBuilder().setFileId("text").setName("notes.md").build()
        openSharedFile(context, file, local, host)
        assertEquals(local, host.pendingTextPreview?.localFile)
        assertNull(host.textPreview)
        assertNull(shadowOf(context).nextStartedActivity)
    }
}
