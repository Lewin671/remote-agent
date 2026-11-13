package io.github.lewin671.remoteagent.data

import android.app.Application
import android.content.ContentProvider
import android.content.ContentValues
import android.database.Cursor
import android.net.Uri
import android.os.ParcelFileDescriptor
import android.provider.MediaStore
import androidx.test.core.app.ApplicationProvider
import java.io.File
import java.nio.file.Files
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import org.robolectric.shadows.ShadowContentResolver

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [30], application = Application::class)
class SharedFileManagerTest {
    @Test fun downloadExportUsesAppFolderAndPublishesTheCompleteBytes() = runBlocking {
        val context = ApplicationProvider.getApplicationContext<Application>()
        val dir = Files.createTempDirectory("shared-export").toFile()
        val source = File(dir, "source.zip").apply { writeBytes(ByteArray(2048) { it.toByte() }) }
        val output = File(dir, "export.zip")
        val provider = ExportProvider(output)
        provider.attachInfo(context, android.content.pm.ProviderInfo().apply { authority = "media" })
        ShadowContentResolver.registerProviderInternal("media", provider)
        val manager = SharedFileManager(context) { null }
        try {
            assertTrue(manager.saveToDownloads(source, "project.zip").isSuccess)
            assertEquals("Download/RemoteAgent", provider.inserted?.getAsString(MediaStore.MediaColumns.RELATIVE_PATH))
            assertEquals("project.zip", provider.inserted?.getAsString(MediaStore.MediaColumns.DISPLAY_NAME))
            assertEquals(1, provider.inserted?.getAsInteger(MediaStore.MediaColumns.IS_PENDING))
            assertArrayEquals(source.readBytes(), output.readBytes())
            assertEquals(0, provider.published?.getAsInteger(MediaStore.MediaColumns.IS_PENDING))
        } finally {
            manager.store.close()
            dir.deleteRecursively()
        }
    }

    private class ExportProvider(private val output: File) : ContentProvider() {
        var inserted: ContentValues? = null
        var published: ContentValues? = null
        override fun onCreate() = true
        override fun insert(uri: Uri, values: ContentValues?): Uri {
            inserted = values?.let { ContentValues(it) }
            return Uri.parse("content://media/external/downloads/1")
        }
        override fun openFile(uri: Uri, mode: String): ParcelFileDescriptor =
            ParcelFileDescriptor.open(output, ParcelFileDescriptor.MODE_CREATE or ParcelFileDescriptor.MODE_READ_WRITE)
        override fun update(uri: Uri, values: ContentValues?, selection: String?, args: Array<out String>?): Int {
            published = values?.let { ContentValues(it) }
            return 1
        }
        override fun delete(uri: Uri, selection: String?, args: Array<out String>?) = 0
        override fun getType(uri: Uri) = "application/zip"
        override fun query(uri: Uri, projection: Array<out String>?, selection: String?, args: Array<out String>?, sortOrder: String?): Cursor? = null
    }
}
