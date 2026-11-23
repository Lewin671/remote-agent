package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test
import java.io.ByteArrayInputStream
import java.io.InputStream
import java.security.MessageDigest

class FilesTest {
    @get:org.junit.Rule val hangGuard = hangGuard()
    private val hundredMiB = 100L * 1024 * 1024
    private suspend fun testUploadFile(
        ch: UploadChannel,
        transferId: String,
        name: String,
        total: Long,
        open: () -> InputStream,
        onPrepared: (Long) -> Unit = {},
        onProgress: (Long, Long) -> Unit = { _, _ -> },
    ) = uploadFile(
        ch = ch,
        transferId = transferId,
        name = name,
        total = total,
        open = open,
        maxBytes = hundredMiB,
        maxNameBytes = 255,
        maxSizeText = "100.0 MB",
        onPrepared = onPrepared,
        onProgress = onProgress,
    )

    private class Channel(val expected: ByteArray, val lost: Boolean = false) : UploadChannel {
        var bytes = java.io.ByteArrayOutputStream()
        var maxSeen = 0
        var begins = 0
        var lostOnce = false
        private fun state() = ImageUploadStatus.newBuilder().setState(ImageUploadState.IMAGE_UPLOAD_STATE_RECEIVING)
            .setPersistedBytes(bytes.size().toLong()).setMaxChunkBytes(8192).build()
        override suspend fun begin(transferId: String, format: ImageFormat, total: Long, sha256: ByteArray): ImageUploadStatus = error("must use file begin")
        override suspend fun beginFile(transferId: String, name: String, total: Long, sha256: ByteArray): ImageUploadStatus {
            begins++
            assertEquals("data.zip", name)
            assertEquals(expected.size.toLong(), total)
            assertArrayEquals(MessageDigest.getInstance("SHA-256").digest(expected), sha256)
            return state()
        }
        override suspend fun put(transferId: String, offset: Long, data: ByteArray): ImageUploadStatus {
            maxSeen = maxOf(maxSeen, data.size)
            if (offset == bytes.size().toLong()) bytes.write(data)
            if (lost && !lostOnce) { lostOnce = true; throw CommandException(CommandException.UNKNOWN, "reply lost", outcome = Outcome.UNKNOWN) }
            return state()
        }
        override suspend fun status(transferId: String) = state()
        override suspend fun abort(transferId: String) {}
        override suspend fun complete(transferId: String): ImageUploadStatus {
            assertArrayEquals(expected, bytes.toByteArray())
            return ImageUploadStatus.newBuilder().setState(ImageUploadState.IMAGE_UPLOAD_STATE_COMPLETE).setFileId("file-id").setExpiresMs(123).build()
        }
    }

    @Test fun streamsChunksAndResumesAfterAnUnknownPutWithoutChangingBytes() = runBlocking {
        val expected = ByteArray(2 * 1024 * 1024 + 7) { (it % 251).toByte() }
        val ch = Channel(expected, lost = true)
        var opened = 0
        val progress = mutableListOf<Long>()
        val result = testUploadFile(ch, "transfer", "data.zip", expected.size.toLong(), {
            opened++
            object : ByteArrayInputStream(expected) {
                override fun read(b: ByteArray, off: Int, len: Int): Int {
                    assertTrue("bounded read", len <= DEFAULT_CHUNK_BYTES)
                    return super.read(b, off, len)
                }
            }
        }) { done, _ -> progress += done }
        assertEquals(UploadedFile("file-id", 123), result)
        assertEquals(2, opened) // hash and upload, no full-file materialization
        assertTrue(ch.maxSeen <= 8192)
        assertEquals(expected.size.toLong(), progress.last())
    }

    @Test fun successfulChunksDoNotConsumeTheRetryBudgetForComplete() = runBlocking {
        val data = byteArrayOf(1, 2, 3)
        val daemon = Channel(data)
        var puts = 0
        var completes = 0
        val ch = object : UploadChannel by daemon {
            override suspend fun put(transferId: String, offset: Long, data: ByteArray): ImageUploadStatus {
                if (puts++ < 3) throw CommandException(CommandException.UNKNOWN, "chunk reply lost", outcome = Outcome.UNKNOWN)
                return daemon.put(transferId, offset, data)
            }
            override suspend fun complete(transferId: String): ImageUploadStatus {
                if (completes++ < 3) throw CommandException(CommandException.UNKNOWN, "complete reply lost", outcome = Outcome.UNKNOWN)
                return daemon.complete(transferId)
            }
        }
        val result = testUploadFile(ch, "same-transfer", "data.zip", data.size.toLong(), { ByteArrayInputStream(data) })
        assertEquals("file-id", result.fileId)
        assertEquals(4, puts)
        assertEquals(4, completes)
    }

    @Test fun acceptsEmptyFilesAndRejectsAChangedSource() = runBlocking {
        assertEquals("file-id", testUploadFile(Channel(byteArrayOf()), "t", "data.zip", 0, { ByteArrayInputStream(byteArrayOf()) }).fileId)
        val bytes = byteArrayOf(1, 2)
        for (hint in listOf(0L, 1L, 999L)) {
            var actual = -1L
            testUploadFile(Channel(bytes), "t", "data.zip", hint, { ByteArrayInputStream(bytes) }, onPrepared = { actual = it })
            assertEquals(2L, actual)
        }
        var opens = 0
        val changed = runCatching { testUploadFile(Channel(bytes), "t", "data.zip", 2, { ByteArrayInputStream(if (opens++ == 0) bytes else byteArrayOf(2, 1)) }) }
        assertTrue(changed.exceptionOrNull() is FileContentChanged)
    }

    @Test fun aHundredMiBSourceIsReadInTwoBoundedPasses() = runBlocking {
        val expectedHash = MessageDigest.getInstance("SHA-256").apply {
            val zeros = ByteArray(1024 * 1024)
            repeat(100) { update(zeros) }
        }.digest()
        var uploaded = 0L
        val actual = MessageDigest.getInstance("SHA-256")
        val channel = object : UploadChannel {
            fun receiving() = ImageUploadStatus.newBuilder().setState(ImageUploadState.IMAGE_UPLOAD_STATE_RECEIVING).setPersistedBytes(uploaded).build()
            override suspend fun begin(transferId: String, format: ImageFormat, total: Long, sha256: ByteArray): ImageUploadStatus = error("image")
            override suspend fun beginFile(transferId: String, name: String, total: Long, sha256: ByteArray): ImageUploadStatus {
                assertEquals(hundredMiB, total)
                assertArrayEquals(expectedHash, sha256)
                return receiving()
            }
            override suspend fun put(transferId: String, offset: Long, data: ByteArray): ImageUploadStatus {
                assertTrue(data.size <= DEFAULT_CHUNK_BYTES)
                assertEquals(uploaded, offset)
                actual.update(data)
                uploaded += data.size
                return receiving()
            }
            override suspend fun status(transferId: String) = receiving()
            override suspend fun abort(transferId: String) {}
        override suspend fun complete(transferId: String): ImageUploadStatus {
                assertArrayEquals(expectedHash, actual.digest())
                return ImageUploadStatus.newBuilder().setFileId("large").build()
            }
        }
        var opens = 0
        var closes = 0
        testUploadFile(channel, "large", "large.bin", hundredMiB, {
            opens++
            object : InputStream() {
                var remaining = hundredMiB
                override fun read(): Int { if (remaining == 0L) return -1; remaining--; return 0 }
                override fun read(b: ByteArray, off: Int, len: Int): Int {
                    assertTrue(len <= DEFAULT_CHUNK_BYTES)
                    if (remaining == 0L) return -1
                    val n = minOf(remaining, len.toLong()).toInt()
                    java.util.Arrays.fill(b, off, off + n, 0.toByte())
                    remaining -= n
                    return n
                }
                override fun close() { closes++ }
            }
        })
        assertEquals(hundredMiB, uploaded)
        assertEquals(2, opens)
        assertEquals(2, closes)
    }
}
