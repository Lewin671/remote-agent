package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.proto.SharedFileClearAllEffect
import io.github.lewin671.remoteagent.core.proto.SharedFileDeleteLocalEffect
import io.github.lewin671.remoteagent.core.proto.SharedFileDeletePartialEffect
import io.github.lewin671.remoteagent.core.proto.SharedFileDeleteSessionEffect
import io.github.lewin671.remoteagent.core.proto.SharedFileFailureKind
import io.github.lewin671.remoteagent.core.proto.SharedFileIOErrorKind
import io.github.lewin671.remoteagent.core.proto.SharedFileInspectDiskEffect
import io.github.lewin671.remoteagent.core.proto.SharedFileScanLocalEffect
import io.github.lewin671.remoteagent.core.proto.SharedFileVerifyAndCommitEffect
import io.github.lewin671.remoteagent.core.proto.SharedFileWriteChunkEffect
import java.io.File
import java.io.IOException
import java.nio.file.Files
import java.security.MessageDigest
import java.util.Properties
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class SharedFilesTest {
    @get:org.junit.Rule val hangGuard = hangGuard()

    private fun tempRoot(): File = Files.createTempDirectory("shared-files-io-test").toFile()

    private fun sha256(bytes: ByteArray): ByteArray =
        MessageDigest.getInstance("SHA-256").digest(bytes)

    @Test
    fun writeVerifyCommitScanAndDeleteLifecycle() = runTest {
        val root = tempRoot()
        val deletedCallbacks = mutableListOf<Pair<String?, String?>>()
        val executor = SharedFileIoExecutor(
            rootDir = root,
            availableBytesProvider = { 10_000_000L },
            durationExtractor = { 102_000L },
            onLocalDeleted = { sid, fid -> deletedCallbacks += sid to fid },
            ioDispatcher = StandardTestDispatcher(testScheduler),
        )
        try {
            val payload = ByteArray(2500) { (it % 251).toByte() }
            val digest = sha256(payload)

            // Inspect disk reports known=true with available bytes.
            val inspect = executor.executeInspectDisk(
                SharedFileInspectDiskEffect.newBuilder()
                    .setOpId(1L).setSessionId("s1").setFileId("f1").setRequiredBytes(2500L).build(),
            )
            assertEquals(1L, inspect.opId)
            assertTrue(inspect.known)
            assertEquals(10_000_000L, inspect.availableBytes)

            // Write two chunks: chunk 0 with reset_partial=true, chunk 1 at offset 1024.
            val w0 = executor.executeWriteChunk(
                SharedFileWriteChunkEffect.newBuilder()
                    .setOpId(2L)
                    .setSessionId("s1")
                    .setFileId("f1")
                    .setDisplayName("clip.mp4")
                    .setOffset(0L)
                    .setData(ByteString.copyFrom(payload, 0, 1024))
                    .setTotalSizeBytes(2500L)
                    .setChunkBytes(1024)
                    .setCommittedOffset(1024L)
                    .setExpectedSha256(ByteString.copyFrom(digest))
                    .setResetPartial(true)
                    .build(),
            )
            assertTrue(w0.ok)

            // Scan while partial exists.
            val partialScan = executor.executeScanLocal(SharedFileScanLocalEffect.newBuilder().setOpId(3L).build())
            assertEquals(3L, partialScan.opId)
            assertEquals(1, partialScan.entriesCount)
            val pe = partialScan.getEntries(0)
            assertEquals("s1", pe.sessionId)
            assertEquals("f1", pe.fileId)
            assertFalse(pe.completed)
            assertEquals(1024L, pe.bytesOnDisk)
            assertEquals(2500L, pe.expectedSizeBytes)
            assertEquals(1024, pe.chunkBytes)
            assertEquals(1024L, pe.committedOffset)
            assertArrayEquals(digest, pe.sha256.toByteArray())

            // Write remaining bytes.
            val w1 = executor.executeWriteChunk(
                SharedFileWriteChunkEffect.newBuilder()
                    .setOpId(4L)
                    .setSessionId("s1")
                    .setFileId("f1")
                    .setDisplayName("clip.mp4")
                    .setOffset(1024L)
                    .setData(ByteString.copyFrom(payload, 1024, 1476))
                    .setTotalSizeBytes(2500L)
                    .setChunkBytes(1024)
                    .setCommittedOffset(2500L)
                    .setExpectedSha256(ByteString.copyFrom(digest))
                    .setResetPartial(false)
                    .build(),
            )
            assertTrue(w1.ok)

            // Verify & commit with duration probing.
            val vc = executor.executeVerifyAndCommit(
                SharedFileVerifyAndCommitEffect.newBuilder()
                    .setOpId(5L)
                    .setSessionId("s1")
                    .setFileId("f1")
                    .setDisplayName("clip.mp4")
                    .setTotalSizeBytes(2500L)
                    .setExpectedSha256(ByteString.copyFrom(digest))
                    .setDeliverOnly(false)
                    .setProbeDuration(true)
                    .build(),
            )
            assertTrue(vc.ok)
            assertTrue(vc.hasDuration)
            assertEquals(102_000L, vc.durationMs)
            val localFile = executor.localFileIfDownloaded("s1", "f1", "clip.mp4")
            assertNotNull(localFile)
            assertArrayEquals(payload, localFile!!.readBytes())
            assertTrue(executor.usedBytes() >= 2500L)

            // DeletePartial must NOT touch an already committed copy.
            executor.executeDeletePartial(
                SharedFileDeletePartialEffect.newBuilder().setSessionId("s1").setFileId("f1").build(),
            )
            assertNotNull(executor.localFileIfDownloaded("s1", "f1", "clip.mp4"))

            // Scan after commit reports completed=true with duration.
            val doneScan = executor.executeScanLocal(SharedFileScanLocalEffect.newBuilder().setOpId(6L).build())
            assertEquals(1, doneScan.entriesCount)
            val de = doneScan.getEntries(0)
            assertTrue(de.completed)
            assertEquals(2500L, de.bytesOnDisk)
            assertEquals(2500L, de.expectedSizeBytes)
            assertTrue(de.hasDuration)
            assertEquals(102_000L, de.durationMs)

            // DeleteLocal removes committed copy and notifies callback.
            executor.executeDeleteLocal(
                SharedFileDeleteLocalEffect.newBuilder().setSessionId("s1").setFileId("f1").build(),
            )
            assertNull(executor.localFileIfDownloaded("s1", "f1", "clip.mp4"))
            assertEquals(listOf("s1" to "f1"), deletedCallbacks)
        } finally {
            executor.close()
            root.deleteRecursively()
        }
    }

    @Test
    fun verifyHandlesZeroByteFileAndChecksumMismatchAndStorageFull() = runTest {
        val root = tempRoot()
        val executor = SharedFileIoExecutor(
            rootDir = root,
            availableBytesProvider = { throw IOException("ENOSPC (No space left on device)") },
            ioDispatcher = StandardTestDispatcher(testScheduler),
        )
        try {
            // InspectDisk maps ENOSPC to STORAGE_FULL.
            val fullInspect = executor.executeInspectDisk(
                SharedFileInspectDiskEffect.newBuilder()
                    .setOpId(1L).setSessionId("s1").setFileId("f0").setRequiredBytes(100L).build(),
            )
            assertFalse(fullInspect.known)
            assertEquals(SharedFileIOErrorKind.SHARED_FILE_IO_ERROR_KIND_STORAGE_FULL, fullInspect.errorKind)

            // 0-byte file round-trip.
            val emptyDigest = sha256(ByteArray(0))
            val w0 = executor.executeWriteChunk(
                SharedFileWriteChunkEffect.newBuilder()
                    .setOpId(2L)
                    .setSessionId("s1")
                    .setFileId("f-empty")
                    .setDisplayName("empty.txt")
                    .setOffset(0L)
                    .setData(ByteString.EMPTY)
                    .setTotalSizeBytes(0L)
                    .setChunkBytes(1024)
                    .setCommittedOffset(0L)
                    .setExpectedSha256(ByteString.copyFrom(emptyDigest))
                    .setResetPartial(true)
                    .build(),
            )
            assertTrue(w0.ok)
            val vc0 = executor.executeVerifyAndCommit(
                SharedFileVerifyAndCommitEffect.newBuilder()
                    .setOpId(3L)
                    .setSessionId("s1")
                    .setFileId("f-empty")
                    .setDisplayName("empty.txt")
                    .setTotalSizeBytes(0L)
                    .setExpectedSha256(ByteString.copyFrom(emptyDigest))
                    .build(),
            )
            assertTrue(vc0.ok)
            val emptyFile = executor.localFileIfDownloaded("s1", "f-empty", "empty.txt")
            assertNotNull(emptyFile)
            assertEquals(0L, emptyFile!!.length())

            // Checksum mismatch deletes partial files and returns CHECKSUM_MISMATCH.
            val payload = ByteArray(100) { 7 }
            val wrongHash = ByteArray(32) { 0x5A }
            executor.executeWriteChunk(
                SharedFileWriteChunkEffect.newBuilder()
                    .setOpId(4L)
                    .setSessionId("s1")
                    .setFileId("f-bad")
                    .setDisplayName("bad.bin")
                    .setOffset(0L)
                    .setData(ByteString.copyFrom(payload))
                    .setTotalSizeBytes(100L)
                    .setChunkBytes(1024)
                    .setCommittedOffset(100L)
                    .setExpectedSha256(ByteString.copyFrom(wrongHash))
                    .setResetPartial(true)
                    .build(),
            )
            val badVc = executor.executeVerifyAndCommit(
                SharedFileVerifyAndCommitEffect.newBuilder()
                    .setOpId(5L)
                    .setSessionId("s1")
                    .setFileId("f-bad")
                    .setDisplayName("bad.bin")
                    .setTotalSizeBytes(100L)
                    .setExpectedSha256(ByteString.copyFrom(wrongHash))
                    .build(),
            )
            assertFalse(badVc.ok)
            assertEquals(SharedFileIOErrorKind.SHARED_FILE_IO_ERROR_KIND_CHECKSUM_MISMATCH, badVc.errorKind)
            assertFalse(File(root, "s1/f-bad/download.part").exists())
            assertFalse(File(root, "s1/f-bad/partial.meta").exists())
        } finally {
            executor.close()
            root.deleteRecursively()
        }
    }

    @Test
    fun scanLocalReportsLegacyMetaAndEvictedFilesTruthfullyForCoreCleanup() = runTest {
        val root = tempRoot()
        val executor = SharedFileIoExecutor(
            rootDir = root,
            ioDispatcher = StandardTestDispatcher(testScheduler),
        )
        try {
            // Legacy pre-3B directory with old meta.properties + download.part (no format=2).
            val legacyDir = File(root, "s-old/f-old").apply { mkdirs() }
            File(legacyDir, "download.part").writeBytes(ByteArray(1024))
            val legacyProps = Properties().apply {
                setProperty("sizeBytes", "2048")
                setProperty("blockBytes", "1024")
                setProperty("completedOffset", "1024")
                setProperty("sha256", "00".repeat(32))
            }
            File(legacyDir, "meta.properties").outputStream().use { legacyProps.store(it, null) }

            // Evicted committed file: valid committed.meta, but file/<name> was deleted by OS cache cleaner.
            val evictedFile = executor.seedCommittedFileForTest("s-evict", "f-evict", "gone.pdf", ByteArray(64))
            assertTrue(evictedFile.delete())

            // Valid v2 committed file should be reported with exact bytes.
            executor.seedCommittedFileForTest("s-ok", "f-ok", "keep.pdf", ByteArray(128))

            val scanned = executor.executeScanLocal(SharedFileScanLocalEffect.newBuilder().setOpId(9L).build())
            val byFile = scanned.entriesList.associateBy { it.fileId }
            assertEquals(3, scanned.entriesCount)

            val ok = byFile.getValue("f-ok")
            assertTrue(ok.completed)
            assertEquals(128L, ok.bytesOnDisk)
            assertEquals(128L, ok.expectedSizeBytes)

            val legacy = byFile.getValue("f-old")
            assertFalse(legacy.completed)
            assertEquals(0L, legacy.committedOffset)
            assertTrue(legacy.sha256.isEmpty)
            // Shell must NOT delete legacy files on its own during scan; Core emits DeletePartial.
            assertTrue(legacyDir.exists())

            val evicted = byFile.getValue("f-evict")
            assertTrue(evicted.completed)
            assertEquals(-1L, evicted.bytesOnDisk)
            assertTrue(File(root, "s-evict/f-evict").exists())
        } finally {
            executor.close()
            root.deleteRecursively()
        }
    }

    @Test
    fun globalFifoQueueSerializesDirectoryAndPerFileEffects() {
        val root = tempRoot()
        val order = CopyOnWriteArrayList<String>()
        val probeLatch = CountDownLatch(1)
        val allowProbeFinish = CountDownLatch(1)
        val executor = SharedFileIoExecutor(
            rootDir = root,
            availableBytesProvider = {
                order += "inspect-start"
                probeLatch.countDown()
                allowProbeFinish.await(5, TimeUnit.SECONDS)
                order += "inspect-end"
                1_000_000L
            },
            onLocalDeleted = { sid, fid -> order += "deleted:$sid:$fid" },
        )
        val host = CoreHost()
        try {
            executor.attach(host)
            // Enqueue InspectDisk (blocks on allowProbeFinish), followed by DeleteSession & ClearAll.
            host.onSharedFileInspectDisk(
                SharedFileInspectDiskEffect.newBuilder()
                    .setOpId(10L).setSessionId("s1").setFileId("f1").setRequiredBytes(10L).build(),
            )
            host.onSharedFileDeleteSession(
                SharedFileDeleteSessionEffect.newBuilder().setSessionId("s1").build(),
            )
            host.onSharedFileClearAll(SharedFileClearAllEffect.getDefaultInstance())

            assertTrue(probeLatch.await(5, TimeUnit.SECONDS))
            // DeleteSession and ClearAll MUST NOT run until InspectDisk finishes.
            assertEquals(listOf("inspect-start"), order.toList())
            allowProbeFinish.countDown()
            kotlinx.coroutines.runBlocking { executor.awaitIdle() }
            assertEquals(
                listOf("inspect-start", "inspect-end", "deleted:s1:null", "deleted:null:null"),
                order.toList(),
            )
        } finally {
            host.close()
            executor.close()
            root.deleteRecursively()
        }
    }

    @Test
    fun realCoreHostSmokeScansDeletesAndClearsThroughFifoExecutor() {
        val root = tempRoot()
        val executor = SharedFileIoExecutor(rootDir = root)
        val host = CoreHost()
        try {
            val committed = executor.seedCommittedFileForTest("s-smoke", "f-smoke", "report.pdf", ByteArray(200))
            assertTrue(committed.isFile)

            executor.attach(host)
            host.sendSharedFileConfig(
                localCacheMaxBytes = 0L,
                storageFullKind = SharedFileFailureKind.SHARED_FILE_FAILURE_KIND_PHONE_STORAGE_FULL,
                scanLocal = true,
            )
            // Wait for Core -> ScanLocal -> LocalScanned round-trip.
            kotlinx.coroutines.runBlocking {
                host.dispatchAndWait(
                    io.github.lewin671.remoteagent.core.proto.Event.newBuilder()
                        .setSharedFileSetVisibleSession(
                            io.github.lewin671.remoteagent.core.proto.SharedFileSetVisibleSessionAction
                                .newBuilder().setSessionId("s-smoke"),
                        ).build(),
                )
                executor.awaitIdle()
            }
            assertTrue(committed.isFile)

            // DeleteLocalAction -> Core emits SharedFileDeleteLocalEffect -> executor deletes file.
            kotlinx.coroutines.runBlocking {
                host.dispatchAndWait(
                    io.github.lewin671.remoteagent.core.proto.Event.newBuilder()
                        .setSharedFileDeleteLocal(
                            io.github.lewin671.remoteagent.core.proto.SharedFileDeleteLocalAction
                                .newBuilder().setSessionId("s-smoke").setFileId("f-smoke"),
                        ).build(),
                )
                executor.awaitIdle()
            }
            assertFalse("DeleteLocalAction must remove local file", committed.exists())

            // ClearAllAction -> Core emits SharedFileClearAllEffect -> executor wipes rootDir.
            val extra = executor.seedCommittedFileForTest("s-other", "f-other", "notes.txt", ByteArray(50))
            assertTrue(extra.isFile)
            kotlinx.coroutines.runBlocking {
                host.clearAllSharedFilesAndWait()
                executor.awaitIdle()
            }
            assertFalse("ClearAllAction must wipe all files", extra.exists())
            assertEquals(0L, executor.usedBytes())
        } finally {
            host.close()
            executor.close()
            root.deleteRecursively()
        }
    }

    @Test
    fun fifoQueueSurvivesTaskThrowableAndContinuesProcessing() {
        val root = tempRoot()
        val calls = CopyOnWriteArrayList<String>()
        val executor = SharedFileIoExecutor(
            rootDir = root,
            availableBytesProvider = {
                calls += "inspect"
                throw AssertionError("simulated unexpected error")
            },
            onLocalDeleted = { sid, fid ->
                calls += "delete:$sid:$fid"
                if (sid == "s-boom") throw IllegalStateException("callback exploded")
            },
        )
        val host = CoreHost()
        try {
            executor.attach(host)
            // 1. DeleteLocal whose callback throws IllegalStateException must NOT kill the queue.
            host.onSharedFileDeleteLocal(
                SharedFileDeleteLocalEffect.newBuilder().setSessionId("s-boom").setFileId("f1").build(),
            )
            // 2. InspectDisk whose provider throws AssertionError must report back and keep the queue alive.
            host.onSharedFileInspectDisk(
                SharedFileInspectDiskEffect.newBuilder()
                    .setOpId(42L).setSessionId("s-ok").setFileId("f2").setRequiredBytes(10L).build(),
            )
            // 3. Subsequent DeleteSession must still execute on the surviving FIFO worker.
            val survivor = executor.seedCommittedFileForTest("s-after", "f3", "keep.bin", ByteArray(16))
            assertTrue(survivor.isFile)
            host.onSharedFileDeleteSession(
                SharedFileDeleteSessionEffect.newBuilder().setSessionId("s-after").build(),
            )

            kotlinx.coroutines.runBlocking { executor.awaitIdle() }
            assertEquals(listOf("delete:s-boom:f1", "inspect", "delete:s-after:null"), calls.toList())
            assertFalse("subsequent task must still run after earlier task threw", survivor.exists())
        } finally {
            host.close()
            executor.close()
            root.deleteRecursively()
        }
    }
}
