package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test
import java.io.ByteArrayInputStream
import java.io.InputStream
import java.security.MessageDigest

class FilesTest {
    private fun machine(supported: Boolean, count: Int = 6, bytes: Long = MAX_FILE_BYTES) = MachineView("m", true, 0,
        MachineMeta.newBuilder().setProtocolVersion(PROTOCOL_VERSION).setCapabilities(MachineCapabilities.newBuilder()
            .setFileUpload(supported).setMaxFilesPerMessage(count).setMaxFileUploadBytes(bytes)).build())

    @Test fun capabilityAndIndependentLimits() {
        assertFalse(filesSupported(null))
        assertFalse(filesSupported(machine(false)))
        assertTrue(filesSupported(machine(true)))
        assertEquals(2, maxFiles(machine(true, 2)))
        assertEquals(12L, maxFileBytes(machine(true, bytes = 12)))
        assertEquals(6, maxFiles(machine(true, 99)))
        assertNull(fileLimitError(0, 0, 6, MAX_FILE_BYTES))
        assertNull(fileLimitError(MAX_FILE_BYTES, 5, 6, MAX_FILE_BYTES))
        assertTrue(fileLimitError(MAX_FILE_BYTES + 1, 0, 6, MAX_FILE_BYTES)!!.contains("文件过大"))
        assertTrue(fileLimitError(1, 6, 6, MAX_FILE_BYTES)!!.contains("最多 6 个文件"))
        assertNotNull(fileLimitError(-1, 0, 6, MAX_FILE_BYTES))
    }

    @Test fun middleEllipsisKeepsExtensionAndUnicode() {
        assertEquals("a.zip", fileChipName("a.zip"))
        val name = "😀非常长的季度分析报告".repeat(5) + ".csv"
        val shown = fileChipName(name)
        assertTrue(shown.startsWith("😀非常"))
        assertTrue(shown.endsWith(".csv"))
        assertEquals(32, shown.codePointCount(0, shown.length))
        assertFalse(shown.contains('\uFFFD'))
    }

    @Test fun hidesOnlyWholeAttestedLinesAndTheirBlankSeparator() {
        val file = SentFile.newBuilder().setPath("/uploads/session/files/id/data.csv").build()
        val path = file.path
        assertEquals("Read this", withoutFileLines("Read this\n\n@$path", listOf(file)))
        assertEquals("", withoutFileLines(path, listOf(file)))
        assertEquals("", withoutFileLines("File: $path", listOf(file)))
        assertEquals("File: @$path (file)", withoutFileLines("File: @$path (file)", listOf(file)))
        assertEquals("", withoutFileLines("File: @\"$path\" (file)", listOf(file)))
        assertEquals("File: @\"$path\" (file) trailing", withoutFileLines("File: @\"$path\" (file) trailing", listOf(file)))
        assertEquals("File: $path trailing", withoutFileLines("File: $path trailing", listOf(file)))
        assertEquals("keep $path here\n$path ", withoutFileLines("keep $path here\n$path ", listOf(file)))
        assertEquals("/other\n", withoutFileLines("/other\n", listOf(file)))
        assertEquals("a\nb", withoutFileLines("a\n\n$path\nb", listOf(file)))
        assertEquals("a\n\n", withoutFileLines("a\n\n", emptyList()))
    }

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
            if (lost && !lostOnce) { lostOnce = true; throw CommandException(CommandException.UNKNOWN, "reply lost") }
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
        val result = uploadFile(ch, "transfer", "data.zip", expected.size.toLong(), {
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
                if (puts++ < 3) throw CommandException(CommandException.UNKNOWN, "chunk reply lost")
                return daemon.put(transferId, offset, data)
            }
            override suspend fun complete(transferId: String): ImageUploadStatus {
                if (completes++ < 3) throw CommandException(CommandException.UNKNOWN, "complete reply lost")
                return daemon.complete(transferId)
            }
        }
        val result = uploadFile(ch, "same-transfer", "data.zip", data.size.toLong(), { ByteArrayInputStream(data) })
        assertEquals("file-id", result.fileId)
        assertEquals(4, puts)
        assertEquals(4, completes)
    }

    @Test fun acceptsEmptyFilesAndRejectsAChangedSource() = runBlocking {
        assertEquals("file-id", uploadFile(Channel(byteArrayOf()), "t", "data.zip", 0, { ByteArrayInputStream(byteArrayOf()) }).fileId)
        val bytes = byteArrayOf(1, 2)
        for (hint in listOf(0L, 1L, 999L)) {
            var actual = -1L
            uploadFile(Channel(bytes), "t", "data.zip", hint, { ByteArrayInputStream(bytes) }, onPrepared = { actual = it })
            assertEquals(2L, actual)
        }
        var opens = 0
        val changed = runCatching { uploadFile(Channel(bytes), "t", "data.zip", 2, { ByteArrayInputStream(if (opens++ == 0) bytes else byteArrayOf(2, 1)) }) }
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
                assertEquals(MAX_FILE_BYTES, total)
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
        uploadFile(channel, "large", "large.bin", MAX_FILE_BYTES, {
            opens++
            object : InputStream() {
                var remaining = MAX_FILE_BYTES
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
        assertEquals(MAX_FILE_BYTES, uploaded)
        assertEquals(2, opens)
        assertEquals(2, closes)
    }

    @Test fun fileOnlyQueueRetainsMetadataThroughAStatusOnlyUpdate() {
        val file = SentFile.newBuilder().setFileId("f").setName("data.csv").setSizeBytes(7).build()
        val queued = Event.newBuilder().setSeq(1).setInputState(InputState.newBuilder().setRequestId("r")
            .setStatus(InputStatus.INPUT_STATUS_QUEUED).addFiles(file)).build()
        val held = Event.newBuilder().setSeq(2).setInputState(InputState.newBuilder().setRequestId("r")
            .setStatus(InputStatus.INPUT_STATUS_HELD_BY_CLI)).build()
        assertEquals(listOf(file), pendingInputs(derive(listOf(queued, held))).single().files)
    }
}
