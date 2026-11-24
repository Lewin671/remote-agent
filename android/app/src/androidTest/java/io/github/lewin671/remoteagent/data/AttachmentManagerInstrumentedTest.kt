package io.github.lewin671.remoteagent.data

import android.os.ParcelFileDescriptor
import androidx.test.platform.app.InstrumentationRegistry
import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.AttachmentIoExecutor
import io.github.lewin671.remoteagent.core.AttachmentKeyUnavailableException
import io.github.lewin671.remoteagent.core.AttachmentPlatformIo
import io.github.lewin671.remoteagent.core.HashedFileOutcome
import io.github.lewin671.remoteagent.core.PreparedImageOutcome
import io.github.lewin671.remoteagent.core.ReadChunkOutcome
import io.github.lewin671.remoteagent.core.proto.AttachmentHashFileEffect
import io.github.lewin671.remoteagent.core.proto.AttachmentIOErrorKind
import io.github.lewin671.remoteagent.core.proto.AttachmentPrepareImageEffect
import io.github.lewin671.remoteagent.core.proto.AttachmentReadChunkEffect
import io.github.lewin671.remoteagent.core.proto.DraftStorageBucket
import io.github.lewin671.remoteagent.core.proto.DraftStorageListEffect
import io.github.lewin671.remoteagent.core.proto.DraftStorageWriteEffect
import java.io.File
import java.security.KeyStoreException
import java.util.concurrent.atomic.AtomicInteger
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * On-device instrumentation tests for Android attachment I/O edge cases (Step 3C-1 review fixes):
 * - M3: real Linux/Android pipe descriptors (`ParcelFileDescriptor.createPipe()`) are non-regular
 *   (`S_ISREG == false`, `FileChannel.size() == 0`) and must stream sequentially in O(n) rather
 *   than triggering bogus `SOURCE_CHANGED("size_changed")`.
 * - M4: Keystore failures preserve persisted drafts, outbox records, and `key.bin` untouched, and
 *   recover cleanly once Keystore is healthy again (also verifies real AndroidKeyStore round-trip).
 */
class AttachmentManagerInstrumentedTest {
    private val context = InstrumentationRegistry.getInstrumentation().targetContext

    @Test
    fun realPipeDescriptorStreamsMultiChunkPayloadInSingleOpenWithoutBogusSizeChanged() {
        val chunkSize = 32 * 1024
        val chunkCount = 5
        val totalSize = chunkSize * chunkCount
        val expected = ByteArray(totalSize) { i -> ((i * 53 + 7) and 0xFF).toByte() }

        val (readEnd, writeEnd) = ParcelFileDescriptor.createPipe()
        assertFalse(
            "Android pipe fd must NOT be treated as S_ISREG",
            isRegularFileDescriptor(readEnd.fileDescriptor),
        )

        val writerError = arrayOfNulls<Throwable>(1)
        val writerThread = Thread {
            try {
                ParcelFileDescriptor.AutoCloseOutputStream(writeEnd).use { out ->
                    var off = 0
                    while (off < expected.size) {
                        val n = minOf(8192, expected.size - off)
                        out.write(expected, off, n)
                        off += n
                    }
                    out.flush()
                }
            } catch (t: Throwable) {
                writerError[0] = t
            }
        }
        writerThread.start()

        val openCalls = AtomicInteger(0)
        val reader = SourceChunkReader(
            access = object : AttachmentSourceAccess {
                override fun isDirectory(ref: String) = false
                override fun open(ref: String): OpenedAttachmentSource {
                    val call = openCalls.incrementAndGet()
                    assertEquals("Pipe source must be opened only once across sequential chunks", 1, call)
                    return openParcelFileDescriptorAsSource(readEnd)
                }
            },
        )
        try {
            val actual = ByteArray(totalSize)
            for (i in 0 until chunkCount) {
                val off = (i * chunkSize).toLong()
                val res = reader.readChunk("content://test-pipe/item", off, chunkSize, totalSize.toLong())
                assertTrue("chunk $i failed: ${res.errorKind} ${res.errorDetail}", res.ok)
                assertEquals(chunkSize, res.data.size)
                System.arraycopy(res.data, 0, actual, off.toInt(), chunkSize)
            }
            assertArrayEquals(expected, actual)
            assertEquals(1, openCalls.get())
        } finally {
            reader.close()
            writerThread.join(5_000L)
        }
        assertEquals("Pipe writer encountered error", null, writerError[0])
    }

    @Test
    fun realRegularFileUsesFastPositionalReadAndDetectsSizeMismatch() {
        val tmp = File(context.cacheDir, "ra-m3-regular-${System.nanoTime()}.bin")
        val payload = ByteArray(64 * 1024) { i -> ((i * 19 + 3) and 0xFF).toByte() }
        tmp.writeBytes(payload)
        try {
            ParcelFileDescriptor.open(tmp, ParcelFileDescriptor.MODE_READ_ONLY).use { pfd ->
                assertTrue(
                    "Regular file fd must pass S_ISREG",
                    isRegularFileDescriptor(pfd.fileDescriptor),
                )
            }
            val reader = SourceChunkReader(
                access = object : AttachmentSourceAccess {
                    override fun isDirectory(ref: String) = false
                    override fun open(ref: String): OpenedAttachmentSource =
                        openParcelFileDescriptorAsSource(
                            ParcelFileDescriptor.open(tmp, ParcelFileDescriptor.MODE_READ_ONLY),
                        )
                },
            )
            try {
                // Random-access slice in the middle succeeds via FileChannel.
                val slice = reader.readChunk("file://${tmp.absolutePath}", 16_384L, 8_192, payload.size.toLong())
                assertTrue(slice.ok)
                assertArrayEquals(payload.copyOfRange(16_384, 16_384 + 8_192), slice.data)

                // Mismatched expectedSize is caught cleanly.
                val drift = reader.readChunk("file://${tmp.absolutePath}", 0L, 1024, payload.size.toLong() + 1L)
                assertFalse(drift.ok)
                assertEquals(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_CHANGED, drift.errorKind)
                assertEquals("size_changed", drift.errorDetail)
            } finally {
                reader.close()
            }
        } finally {
            tmp.delete()
        }
    }

    @Test
    fun keystoreFailurePreservesDraftsAndRealAndroidKeyStoreRoundTripsIsolatedAlias() {
        val dir = File(context.cacheDir, "ra-m4-inst-${System.nanoTime()}").apply { mkdirs() }
        val noopIo = object : AttachmentPlatformIo {
            override fun prepareImage(eff: AttachmentPrepareImageEffect, storeBlob: (String, ByteArray) -> String) =
                PreparedImageOutcome(ok = false)
            override fun hashFile(eff: AttachmentHashFileEffect) = HashedFileOutcome(ok = false)
            override fun readFileChunk(eff: AttachmentReadChunkEffect) = ReadChunkOutcome(ok = false)
        }
        val testAlias = "ra-pending-inst-test-${System.nanoTime()}"
        try {
            // Part A: Injectable Keystore failure preserves key.bin, drafts, and outbox.
            var failNext = false
            val fakeKs = object : AndroidKeyStoreBackend {
                private var key: SecretKey? = null
                override fun getKey(alias: String): SecretKey? {
                    if (failNext) throw KeyStoreException("simulated keystore binder error")
                    return key
                }
                override fun generateKey(alias: String): SecretKey =
                    KeyGenerator.getInstance("AES").apply { init(256) }.generateKey().also { key = it }
                override fun deleteKey(alias: String) { key = null }
            }
            val fakeDir = File(dir, "fake").apply { mkdirs() }
            val p1 = AndroidAttachmentKeyProvider(fakeDir, fakeKs)
            val e1 = AttachmentIoExecutor(fakeDir, p1, noopIo)
            try {
                assertTrue(
                    e1.executeWrite(
                        DraftStorageWriteEffect.newBuilder()
                            .setOpId(1L)
                            .setBucket(DraftStorageBucket.DRAFT_STORAGE_BUCKET_DRAFT)
                            .setKey("session:pixel3")
                            .setData(ByteString.copyFromUtf8("device-draft"))
                            .build(),
                    ).ok,
                )
            } finally {
                e1.close()
            }
            val savedKeyBin = File(fakeDir, "key.bin").readBytes()

            failNext = true
            val p2 = AndroidAttachmentKeyProvider(fakeDir, fakeKs)
            val e2 = AttachmentIoExecutor(fakeDir, p2, noopIo)
            try {
                assertTrue(
                    runCatching { e2.executeList(DraftStorageListEffect.newBuilder().setOpId(2L).build()) }
                        .exceptionOrNull() is AttachmentKeyUnavailableException,
                )
                assertArrayEquals(savedKeyBin, File(fakeDir, "key.bin").readBytes())
            } finally {
                e2.close()
            }

            failNext = false
            val p3 = AndroidAttachmentKeyProvider(fakeDir, fakeKs)
            val e3 = AttachmentIoExecutor(fakeDir, p3, noopIo)
            try {
                val listed = e3.executeList(DraftStorageListEffect.newBuilder().setOpId(3L).build())
                assertEquals(1, listed.entriesCount)
                assertFalse(listed.entriesList[0].corrupt)
                assertEquals("device-draft", listed.entriesList[0].data.toStringUtf8())
            } finally {
                e3.close()
            }

            // Part B: Real AndroidKeyStore round-trip on Pixel 3 using an isolated test alias.
            val realDir = File(dir, "real-ks").apply { mkdirs() }
            val realProvider1 = AndroidAttachmentKeyProvider(realDir, RealAndroidKeyStoreBackend, alias = testAlias)
            val realExec1 = AttachmentIoExecutor(realDir, realProvider1, noopIo)
            try {
                assertTrue(
                    realExec1.executeWrite(
                        DraftStorageWriteEffect.newBuilder()
                            .setOpId(10L)
                            .setBucket(DraftStorageBucket.DRAFT_STORAGE_BUCKET_OUTBOX)
                            .setKey("out:pixel3:r1")
                            .setData(ByteString.copyFromUtf8("real-keystore-payload"))
                            .build(),
                    ).ok,
                )
            } finally {
                realExec1.close()
            }
            val realProvider2 = AndroidAttachmentKeyProvider(realDir, RealAndroidKeyStoreBackend, alias = testAlias)
            val realExec2 = AttachmentIoExecutor(realDir, realProvider2, noopIo)
            try {
                val listed = realExec2.executeList(DraftStorageListEffect.newBuilder().setOpId(11L).build())
                assertEquals(1, listed.entriesCount)
                assertFalse(listed.entriesList[0].corrupt)
                assertEquals("real-keystore-payload", listed.entriesList[0].data.toStringUtf8())
            } finally {
                realExec2.close()
                realProvider2.clearKey()
            }
        } finally {
            runCatching { RealAndroidKeyStoreBackend.deleteKey(testAlias) }
            dir.deleteRecursively()
        }
    }
}
