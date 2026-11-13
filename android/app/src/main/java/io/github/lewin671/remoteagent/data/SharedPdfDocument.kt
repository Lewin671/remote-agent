package io.github.lewin671.remoteagent.data

import android.graphics.Bitmap
import android.graphics.Color
import android.graphics.pdf.PdfRenderer
import android.os.ParcelFileDescriptor
import java.io.File
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.withContext
import kotlin.math.ceil

// Every native PDF operation, including disposal, runs serially off the UI thread.
private val PdfWorker = Dispatchers.IO.limitedParallelism(1)

internal data class PdfPageSize(val width: Int, val height: Int)

internal fun pdfBitmapWidth(screenWidthPx: Int): Int = screenWidthPx.coerceIn(1, 2048)

internal fun pdfRetainedPages(visible: List<Int>, pageCount: Int): IntRange {
    if (pageCount == 0 || visible.isEmpty()) return IntRange.EMPTY
    return (visible.min() - 1).coerceAtLeast(0)..(visible.max() + 1).coerceAtMost(pageCount - 1)
}

/** Owns the descriptor and renderer, never an open Page or a cache of rendered bitmaps. */
internal class SharedPdfDocument private constructor(
    private val renderer: PdfRenderer,
    private val descriptor: ParcelFileDescriptor,
    val pages: List<PdfPageSize>,
) {
    private var closed = false

    suspend fun render(index: Int, screenWidthPx: Int): Bitmap {
        var rendered: Bitmap? = null
        try {
            return withContext(PdfWorker) {
                check(!closed)
                renderer.openPage(index).use { page ->
                    val width = pdfBitmapWidth(screenWidthPx)
                    val height = ceil(width.toDouble() * page.height / page.width).toInt().coerceAtLeast(1)
                    Bitmap.createBitmap(width, height, Bitmap.Config.ARGB_8888).also { bitmap ->
                        rendered = bitmap
                        bitmap.eraseColor(Color.WHITE)
                        page.render(bitmap, null, null, PdfRenderer.Page.RENDER_MODE_FOR_DISPLAY)
                    }
                }
            }
        } catch (error: Throwable) {
            // Also reclaim a native result discarded by withContext's prompt cancellation.
            rendered?.recycle()
            throw error
        }
    }

    suspend fun close() = withContext(NonCancellable + PdfWorker) {
        if (!closed) {
            closed = true
            try { renderer.close() } finally { descriptor.close() }
        }
    }

    companion object {
        /** Null means use the external chooser; passwords and parser details never reach the UI. */
        suspend fun open(file: File): SharedPdfDocument? {
            var opened: SharedPdfDocument? = null
            try {
                return withContext(PdfWorker) {
                    val descriptor = ParcelFileDescriptor.open(file, ParcelFileDescriptor.MODE_READ_ONLY)
                    var renderer: PdfRenderer? = null
                    try {
                        val native = PdfRenderer(descriptor).also { renderer = it }
                        require(native.pageCount > 0)
                        // Only dimensions are read ahead, so every placeholder has its final size.
                        val sizes = List(native.pageCount) { index ->
                            ensureActive()
                            native.openPage(index).use { page ->
                                require(page.width > 0 && page.height > 0)
                                PdfPageSize(page.width, page.height)
                            }
                        }
                        SharedPdfDocument(native, descriptor, sizes).also { opened = it }
                    } catch (error: Throwable) {
                        try { renderer?.close() } finally { descriptor.close() }
                        throw error
                    }
                }
            } catch (cancelled: CancellationException) {
                opened?.close()
                throw cancelled
            } catch (_: Exception) {
                opened?.close()
                return null
            }
        }
    }
}
