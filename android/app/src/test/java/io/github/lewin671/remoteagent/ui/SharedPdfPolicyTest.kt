package io.github.lewin671.remoteagent.ui

import android.app.Application
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.ui.geometry.Offset
import io.github.lewin671.remoteagent.data.pdfBitmapWidth
import io.github.lewin671.remoteagent.data.pdfRetainedPages
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], application = Application::class)
class SharedPdfPolicyTest {
    @Test fun routesPdfCaseInsensitivelyWithoutChangingOtherRoutes() {
        for (name in listOf("report.pdf", "REPORT.PDF", "report.PdF")) assertEquals(SharedFileOpenRoute.PDF_VIEWER, sharedFileOpenRoute(name))
        for (name in listOf("pdf", "report.pdf.zip", "report.pdfx")) assertEquals(SharedFileOpenRoute.SYSTEM_CHOOSER, sharedFileOpenRoute(name))
        assertEquals(SharedFileOpenRoute.TEXT_VIEWER, sharedFileOpenRoute("notes.md"))
    }

    @Test fun retainOnlyVisiblePagesAndOneNeighborEachSide() {
        assertEquals(0..2, pdfRetainedPages(listOf(0, 1), 60))
        assertEquals(8..12, pdfRetainedPages(listOf(9, 10, 11), 60))
        assertEquals(58..59, pdfRetainedPages(listOf(59), 60))
        assertTrue(pdfRetainedPages(emptyList(), 60).isEmpty())
        assertTrue(pdfRetainedPages(emptyList(), 0).isEmpty())
    }

    @Test fun bitmapResolutionIsScreenPixelsCappedAt2048() {
        assertEquals(1080, pdfBitmapWidth(1080))
        assertEquals(2048, pdfBitmapWidth(4096))
        assertEquals(1, pdfBitmapWidth(0))
    }

    @Test fun zoomIsDocumentWideAndClampsScaleAndPan() {
        val zoom = PdfZoomState()
        val list = LazyListState()
        val center = Offset(200f, 400f)
        zoom.transform(2.5f, Offset.Zero, center, 400f, 376f, list)
        assertEquals(2.5f, zoom.scale)
        zoom.transform(5f, Offset(10_000f, 0f), center, 400f, 376f, list)
        assertEquals(4f, zoom.scale)
        assertEquals(564f, zoom.panX)
        zoom.transform(0.01f, Offset(-10_000f, 0f), center, 400f, 376f, list)
        assertEquals(1f, zoom.scale)
        assertEquals(0f, zoom.panX, 0f)
    }
}
