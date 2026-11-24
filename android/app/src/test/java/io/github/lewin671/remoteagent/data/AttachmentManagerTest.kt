package io.github.lewin671.remoteagent.data

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
import java.io.ByteArrayInputStream
import java.io.Closeable
import java.io.File
import java.io.FilterInputStream
import java.nio.ByteBuffer
import java.nio.file.Files
import java.security.KeyStoreException
import java.util.concurrent.atomic.AtomicInteger
import java.util.concurrent.atomic.AtomicLong
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class AttachmentManagerTest {
    private class FakeKeyStoreBackend : AndroidKeyStoreBackend {
        private val keys = HashMap<String, SecretKey>()
        var failGetWith: Throwable? = null
        var generateCount = 0

        override fun getKey(alias: String): SecretKey? {
            failGetWith?.let { throw it }
            return keys[alias]
        }

        override fun generateKey(alias: String): SecretKey {
            generateCount++
            val kg = KeyGenerator.getInstance("AES").apply { init(256) }
            return kg.generateKey().also { keys[alias] = it }
        }

        override fun deleteKey(alias: String) {
            keys.remove(alias)
        }
    }

    private val noopIo = object : AttachmentPlatformIo {
        override fun prepareImage(eff: AttachmentPrepareImageEffect, storeBlob: (String, ByteArray) -> String) =
            PreparedImageOutcome(ok = false)
        override fun hashFile(eff: AttachmentHashFileEffect) = HashedFileOutcome(ok = false)
        override fun readFileChunk(eff: AttachmentReadChunkEffect) = ReadChunkOutcome(ok = false)
    }

    @Test
    fun keystoreFailureDoesNotDeleteDraftsOrOverwriteKeyBinAndRecoversNextStart() {
        val dir = Files.createTempDirectory("ra-ks-m4-").toFile()
        try {
            val fakeKs = FakeKeyStoreBackend()
            val provider1 = AndroidAttachmentKeyProvider(dir, fakeKs)

            // 1. Fresh install: no key.bin and no alias -> loadExistingKey returns null (not an error).
            assertNull(provider1.loadExistingKey())

            // 2. Write encrypted draft + outbox + blob using ensureKey().
            val exec1 = AttachmentIoExecutor(rootDir = dir, keyProvider = provider1, platformIo = noopIo)
            try {
                val wr1 = exec1.executeWrite(
                    DraftStorageWriteEffect.newBuilder()
                        .setOpId(1L)
                        .setBucket(DraftStorageBucket.DRAFT_STORAGE_BUCKET_DRAFT)
                        .setKey("session:s-m4")
                        .setData(ByteString.copyFromUtf8("keep-my-draft"))
                        .build(),
                )
                assertTrue(wr1.ok)
                val wr2 = exec1.executeWrite(
                    DraftStorageWriteEffect.newBuilder()
                        .setOpId(2L)
                        .setBucket(DraftStorageBucket.DRAFT_STORAGE_BUCKET_OUTBOX)
                        .setKey("out:s-m4:req-1")
                        .setData(ByteString.copyFromUtf8("keep-my-outbox"))
                        .build(),
                )
                assertTrue(wr2.ok)
            } finally {
                exec1.close()
            }
            val keyFile = File(dir, "key.bin")
            assertTrue(keyFile.isFile)
            val originalKeyBin = keyFile.readBytes()
            assertEquals(1, fakeKs.generateCount)

            // 3. Cold restart with transient Keystore failure:
            //    - loadExistingKey() throws AttachmentKeyUnavailableException (NOT null)
            //    - ensureKey() throws AttachmentKeyUnavailableException and NEVER overwrites key.bin
            //    - executeList() throws instead of marking records corrupt
            fakeKs.failGetWith = KeyStoreException("keystore daemon unavailable")
            val provider2 = AndroidAttachmentKeyProvider(dir, fakeKs)
            val exec2 = AttachmentIoExecutor(rootDir = dir, keyProvider = provider2, platformIo = noopIo)
            try {
                assertTrue(runCatching { provider2.loadExistingKey() }.exceptionOrNull() is AttachmentKeyUnavailableException)
                assertTrue(runCatching { provider2.ensureKey() }.exceptionOrNull() is AttachmentKeyUnavailableException)
                assertTrue(
                    runCatching { exec2.executeList(DraftStorageListEffect.newBuilder().setOpId(3L).build()) }
                        .exceptionOrNull() is AttachmentKeyUnavailableException,
                )
                assertArrayEquals("key.bin must not be overwritten on Keystore failure", originalKeyBin, keyFile.readBytes())
                assertEquals("must not generate replacement Keystore key", 1, fakeKs.generateCount)
            } finally {
                exec2.close()
            }

            // 4. Next startup after Keystore recovers -> all drafts and outbox records restore cleanly!
            fakeKs.failGetWith = null
            val provider3 = AndroidAttachmentKeyProvider(dir, fakeKs)
            val exec3 = AttachmentIoExecutor(rootDir = dir, keyProvider = provider3, platformIo = noopIo)
            try {
                val listed = exec3.executeList(DraftStorageListEffect.newBuilder().setOpId(4L).build())
                assertEquals(2, listed.entriesCount)
                assertTrue(listed.entriesList.none { it.corrupt })
                val byKey = listed.entriesList.associate { it.key to it.data.toStringUtf8() }
                assertEquals("keep-my-draft", byKey["session:s-m4"])
                assertEquals("keep-my-outbox", byKey["out:s-m4:req-1"])
            } finally {
                exec3.close()
            }
        } finally {
            dir.deleteRecursively()
        }
    }

    @Test
    fun nonRegularStreamReadsSequentialChunksInLinearTimeAndDetectsSizeDrift() {
        val total = 5 * 1024 // 5 chunks of 1 KiB
        val payload = ByteArray(total) { i -> ((i * 37 + 11) and 0xFF).toByte() }
        val opens = AtomicInteger(0)
        val totalBytesRead = AtomicLong(0L)
        val closedCount = AtomicInteger(0)

        fun countingStream(data: ByteArray) = object : FilterInputStream(ByteArrayInputStream(data)) {
            override fun read(): Int {
                val b = super.read()
                if (b >= 0) totalBytesRead.incrementAndGet()
                return b
            }

            override fun read(b: ByteArray, off: Int, len: Int): Int {
                val n = super.read(b, off, len)
                if (n > 0) totalBytesRead.addAndGet(n.toLong())
                return n
            }

            override fun close() {
                closedCount.incrementAndGet()
                super.close()
            }
        }

        var currentData = payload
        val reader = SourceChunkReader(
            access = object : AttachmentSourceAccess {
                override fun isDirectory(ref: String) = false
                override fun open(ref: String): OpenedAttachmentSource {
                    opens.incrementAndGet()
                    return OpenedAttachmentSource.Stream(countingStream(currentData))
                }
            },
        )
        try {
            // 1. Five sequential chunks: opened ONCE, total bytes read == total (strictly O(n), NOT O(n^2)).
            val reassembled = ByteArray(total)
            for (chunkIdx in 0 until 5) {
                val off = chunkIdx * 1024L
                val out = reader.readChunk("content://pipe/doc1", off, 1024, total.toLong())
                assertTrue("chunk $chunkIdx failed: ${out.errorKind} ${out.errorDetail}", out.ok)
                assertEquals(1024, out.data.size)
                System.arraycopy(out.data, 0, reassembled, off.toInt(), 1024)
            }
            assertArrayEquals(payload, reassembled)
            assertEquals("sequential chunks must reuse single stream cursor", 1, opens.get())
            assertEquals("must read payload once (O(n)) instead of re-skipping from 0", total.toLong(), totalBytesRead.get())
            assertEquals("cursor must be closed after final chunk EOF check", 1, closedCount.get())

            // 2. Out-of-order retry (backward offset) reopens cleanly and reads the requested slice.
            val c0 = reader.readChunk("content://pipe/doc2", 0L, 1024, total.toLong())
            assertTrue(c0.ok)
            val c1 = reader.readChunk("content://pipe/doc2", 1024L, 1024, total.toLong())
            assertTrue(c1.ok)
            val retry0 = reader.readChunk("content://pipe/doc2", 0L, 1024, total.toLong())
            assertTrue(retry0.ok)
            assertArrayEquals(payload.copyOfRange(0, 1024), retry0.data)
            reader.closeRef("content://pipe/doc2")

            // 3. Truncated stream returns SOURCE_CHANGED.
            currentData = payload.copyOfRange(0, 1500)
            val okFirst = reader.readChunk("content://pipe/trunc", 0L, 1024, total.toLong())
            assertTrue(okFirst.ok)
            val eofSecond = reader.readChunk("content://pipe/trunc", 1024L, 1024, total.toLong())
            assertFalse(eofSecond.ok)
            assertEquals(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_CHANGED, eofSecond.errorKind)

            // 4. Oversized stream (extra trailing byte beyond expectedSize) returns SOURCE_CHANGED on final chunk.
            currentData = payload + byteArrayOf(0x7F)
            val over = reader.readChunk("content://pipe/over", 0L, total, total.toLong())
            assertFalse(over.ok)
            assertEquals(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_CHANGED, over.errorKind)
            assertEquals("size_exceeded", over.errorDetail)
        } finally {
            reader.close()
        }
    }

    @Test
    fun regularFileSourceUsesPositionalReadAndDetectsSizeChange() {
        val data = ByteArray(4096) { (it and 0xFF).toByte() }
        val reader = SourceChunkReader(
            access = object : AttachmentSourceAccess {
                override fun isDirectory(ref: String) = false
                override fun open(ref: String): OpenedAttachmentSource = OpenedAttachmentSource.Regular(
                    closeable = Closeable {},
                    sizeProvider = { data.size.toLong() },
                    reader = { dst: ByteBuffer, pos: Long ->
                        if (pos >= data.size) return@Regular -1
                        val n = minOf(dst.remaining(), (data.size - pos).toInt())
                        dst.put(data, pos.toInt(), n)
                        n
                    },
                )
            },
        )
        try {
            val mid = reader.readChunk("file:///regular", 1000L, 512, data.size.toLong())
            assertTrue(mid.ok)
            assertArrayEquals(data.copyOfRange(1000, 1512), mid.data)

            val wrongSize = reader.readChunk("file:///regular", 0L, 512, data.size.toLong() - 1L)
            assertFalse(wrongSize.ok)
            assertEquals(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_CHANGED, wrongSize.errorKind)
            assertEquals("size_changed", wrongSize.errorDetail)
        } finally {
            reader.close()
        }
    }
}
