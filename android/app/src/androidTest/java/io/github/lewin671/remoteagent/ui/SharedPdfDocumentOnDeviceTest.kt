package io.github.lewin671.remoteagent.ui

import androidx.test.platform.app.InstrumentationRegistry
import io.github.lewin671.remoteagent.data.SharedPdfDocument
import java.io.File
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder

/** Native API unit tests: Robolectric does not implement PdfRenderer's PDF decoding. */
class SharedPdfDocumentOnDeviceTest {
    private val instrumentation = InstrumentationRegistry.getInstrumentation()
    @get:Rule val temporary = TemporaryFolder(instrumentation.targetContext.cacheDir)

    private fun fixture(name: String): File = temporary.newFile(name).apply {
        instrumentation.context.assets.open(name).use { input -> outputStream().use { input.copyTo(it) } }
    }

    @Test fun damagedPdfFallsBack() = runBlocking {
        assertNull(SharedPdfDocument.open(fixture("damaged.pdf")))
    }

    @Test fun encryptedPdfFallsBackWithoutAskingForAPassword() = runBlocking {
        assertNull(SharedPdfDocument.open(fixture("encrypted.pdf")))
    }

    @Test fun zeroPagesFallsBack() = runBlocking {
        assertNull(SharedPdfDocument.open(fixture("empty.pdf")))
    }

    @Test fun missingFileFallsBack() = runBlocking {
        assertNull(SharedPdfDocument.open(File(temporary.root, "missing.pdf")))
    }

    @Test fun metadataMatchesEachPageAndRenderingIsSerial() = runBlocking {
        val document = checkNotNull(SharedPdfDocument.open(fixture("preview.pdf")))
        try {
            assertEquals(3, document.pages.size)
            assertEquals(420, document.pages[0].width)
            assertEquals(594, document.pages[1].width)
            // Concurrent callers still never leave more than one native page open.
            val bitmaps = (0..2).map { async { document.render(it, 420) } }.awaitAll()
            try {
                assertEquals(594, bitmaps[0].height)
                assertEquals(297, bitmaps[1].height)
                assertTrue(bitmaps.all { it.width == 420 && !it.isRecycled })
            } finally { bitmaps.forEach { it.recycle() } }
        } finally { document.close() }
        document.close()
        try {
            document.render(0, 420)
            fail("A closed document must not render")
        } catch (_: IllegalStateException) { }
    }

}
