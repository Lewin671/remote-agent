package io.github.lewin671.remoteagent.ui

import android.graphics.Bitmap
import android.os.ParcelFileDescriptor
import android.os.Bundle
import android.system.Os
import android.accessibilityservice.AccessibilityService
import androidx.compose.foundation.ScrollState
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.verticalScroll
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.doubleClick
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollToIndex
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.test.swipeUp
import androidx.compose.ui.unit.dp
import androidx.test.platform.app.InstrumentationRegistry
import io.github.lewin671.remoteagent.core.SharedFileTransferState
import io.github.lewin671.remoteagent.data.SharedFileManager
import io.github.lewin671.remoteagent.proto.SharedFile
import java.io.File
import org.junit.After
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test

/** Offline fixture cards; all PDF decoding, scrolling, zooming and chooser routing are production code. */
class SharedPdfViewerOnDeviceTest {
    @get:Rule val ui = createComposeRule()
    private val instrumentation = InstrumentationRegistry.getInstrumentation()
    private val context = instrumentation.targetContext
    private val manager = SharedFileManager(context) { null }
    private var dark by mutableStateOf(false)
    private val chatScroll = ScrollState(240)
    private lateinit var local: File
    private lateinit var host: SharedFileHostState
    private val output = File(context.getExternalFilesDir(null), "pdf-preview-shots").apply { mkdirs() }

    @After fun cleanup() { manager.clearAll() }

    private fun show(name: String) {
        val bytes = instrumentation.context.assets.open(name).use { it.readBytes() }
        val file = SharedFile.newBuilder().setFileId("pdf-preview").setName(name).setSizeBytes(bytes.size.toLong()).build()
        local = manager.store.localFileFor("pdf-preview", file).apply { parentFile!!.mkdirs(); writeBytes(bytes) }
        manager.store.setStateForTest("pdf-preview", file.fileId, SharedFileTransferState.Downloaded(local))
        ui.setContent {
            RaTheme(if (dark) DarkPalette else LightPalette) {
                SharedFileHost("pdf-preview", "Test computer", true, false, Modifier.fillMaxSize(), manager) {
                    host = LocalSharedFileHost.current!!
                    Column(Modifier.fillMaxSize().background(pal.paper).verticalScroll(chatScroll)) {
                        Spacer(Modifier.height(320.dp))
                        SharedFilesBlock(listOf(file), "pdf-preview", "Test computer", true, false)
                        Spacer(Modifier.height(900.dp))
                    }
                }
            }
        }
        ui.onNodeWithTag(SharedFileCardTag).performClick()
    }

    private fun page(index: Int) {
        ui.waitUntil(10_000) { ui.onAllNodesWithTag("pdf-page-$index").fetchSemanticsNodes().isNotEmpty() }
        ui.waitForIdle()
    }

    private fun shot(name: String, settleMillis: Long = 300) {
        ui.waitForIdle()
        Thread.sleep(settleMillis)
        val bitmap = checkNotNull(instrumentation.uiAutomation.takeScreenshot())
        File(output, "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        bitmap.recycle()
    }

    private fun pdfDescriptors(): Int = File("/proc/self/fd").listFiles().orEmpty().count {
        runCatching { File(Os.readlink(it.absolutePath)).canonicalPath == local.canonicalPath }.getOrDefault(false)
    }

    private fun backAndCheckClosed() {
        ui.onNodeWithContentDescription("返回").performClick()
        ui.waitUntil(10_000) { pdfDescriptors() == 0 }
        ui.onNodeWithTag("pdf-viewer").assertDoesNotExist()
        assertEquals(240, chatScroll.value)
    }

    @Test fun pagesZoomThemesAndDisposal() {
        show("preview.pdf")
        page(0)
        assertTrue(pdfDescriptors() > 0)
        shot("06-pdf-first-page")
        ui.onNodeWithTag("pdf-pages").performScrollToIndex(1)
        page(1)
        ui.onNodeWithTag("pdf-page-position").assertIsDisplayed()
        shot("07-pdf-scrolling-position", 80)
        ui.onNodeWithTag("pdf-pages").performScrollToIndex(0)
        page(0)
        ui.onNodeWithTag("pdf-pages").performTouchInput { doubleClick(Offset(center.x, 120f)) }
        assertEquals(2.5f, ui.onNodeWithTag("pdf-pages").fetchSemanticsNode().config[PdfZoomScale])
        shot("08-pdf-zoom-2_5x")
        ui.onNodeWithTag("pdf-pages").performTouchInput {
            down(0, center - Offset(60f, 0f))
            down(1, center + Offset(60f, 0f))
            moveTo(0, center - Offset(130f, 0f), delayMillis = 160)
            moveTo(1, center + Offset(130f, 0f), delayMillis = 160)
            up(0)
            up(1)
        }
        val pinched = ui.onNodeWithTag("pdf-pages").fetchSemanticsNode().config[PdfZoomScale]
        assertTrue(pinched > 2.5f && pinched <= 4f)
        ui.onNodeWithTag("pdf-pages").performTouchInput { swipeUp() }
        assertEquals(pinched, ui.onNodeWithTag("pdf-pages").fetchSemanticsNode().config[PdfZoomScale])
        ui.onNodeWithTag("pdf-pages").performTouchInput { doubleClick() }
        assertEquals(1f, ui.onNodeWithTag("pdf-pages").fetchSemanticsNode().config[PdfZoomScale])
        ui.onNodeWithTag("pdf-pages").performScrollToIndex(0)
        page(0)
        dark = true
        shot("09-pdf-dark")
        ui.waitUntil(5_000) { ui.onAllNodesWithTag("pdf-page-position").fetchSemanticsNodes().isEmpty() }
        backAndCheckClosed()
    }

    @Test fun encryptedPdfShowsTheSystemChooser() {
        show("encrypted.pdf")
        ui.waitUntil(10_000) { host.pdfPreview == null }
        ui.onNodeWithTag("pdf-viewer").assertDoesNotExist()
        assertNull(host.unopenableTarget)
        shot("10-pdf-encrypted-chooser", 600)
        instrumentation.uiAutomation.performGlobalAction(AccessibilityService.GLOBAL_ACTION_BACK)
        ui.waitUntil(10_000) { pdfDescriptors() == 0 }
    }

    @Test fun sixtyPagesFastScrollHasBoundedCacheAndRecordsPss() {
        show("long-document.pdf")
        page(0)
        val samples = mutableListOf<Pair<Int, Long>>()
        fun sample(index: Int) {
            // A host may take an independent adb dumpsys sample at each workload checkpoint.
            instrumentation.sendStatus(2, Bundle().apply { putInt("pdf_memory_page", index + 1) })
            val dump = ParcelFileDescriptor.AutoCloseInputStream(
                instrumentation.uiAutomation.executeShellCommand("dumpsys meminfo ${context.packageName}"),
            ).bufferedReader().use { it.readText() }
            val pss = Regex("TOTAL PSS:\\s+(\\d+)").find(dump)?.groupValues?.get(1)?.toLong()
                ?: error("No TOTAL PSS in dumpsys meminfo")
            samples += index to pss
        }
        sample(0)
        // Finite scroll workload, not a wait/poll loop. Native rendering must keep up with jumps.
        for (index in 1..59) {
            ui.onNodeWithTag("pdf-pages").performScrollToIndex(index)
            if (index % 5 == 0 || index == 59) {
                page(index)
                val cached = ui.onNodeWithTag("pdf-pages").fetchSemanticsNode().config[PdfCachedPages]
                assertTrue(cached.size <= 5)
                assertTrue(cached.all { it in (index - 2).coerceAtLeast(0)..(index + 3).coerceAtMost(59) })
                sample(index)
            }
        }
        page(59)
        shot("11-pdf-sixty-pages-bottom", 80)
        File(output, "memory.txt").writeText(
            samples.joinToString("\n") { (page, pss) -> "page=${page + 1} total_pss_kib=$pss" } +
                "\npeak_total_pss_kib=${samples.maxOf { it.second }}\n",
        )
        backAndCheckClosed()
    }
}
