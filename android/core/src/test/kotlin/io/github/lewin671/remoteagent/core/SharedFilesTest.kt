package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.proto.GetSharedFileChunkResponse
import io.github.lewin671.remoteagent.proto.SharedFile
import io.github.lewin671.remoteagent.proto.Command
import io.github.lewin671.remoteagent.proto.GetSharedFileChunkRequest
import kotlinx.serialization.json.boolean
import java.io.File
import java.io.IOException
import java.nio.file.Files
import java.util.concurrent.atomic.AtomicInteger
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class SharedFilesTest {


    @Test fun outcomeVectorsPreserveSingleDownloadRetry() = runTest {
        val fixture=Json.parseToJsonElement(File(System.getProperty("outcomeVectors")).readText()).jsonObject
        for (row in fixture.getValue("downloads").jsonArray) {
            val v=row.jsonObject
            val code=v.getValue("code").jsonPrimitive.content
            val signed=v.getValue("signed").jsonPrimitive.boolean
            val retry=v.getValue("androidRetry").jsonPrimitive.boolean
            val root=tempRoot()
            val command=Command.newBuilder().setGetSharedFileChunk(GetSharedFileChunkRequest.getDefaultInstance()).build()
            val error=CommandException(code,"fixture",fromDaemon=signed,readOnly=command.isReadOnlyCommand(),outcome=Outcome.valueOf(v.getValue("outcome").jsonPrimitive.content.uppercase()))
            assertEquals(v.getValue("outcome").jsonPrimitive.content=="unknown",error.unknown)
            var calls=0
            val file=sharedFile(id="policy",name="file.bin",sizeBytes=32L,sha256=ByteArray(32))
            val store=SharedFileStore(rootDir=root,fetcher=SharedFileChunkFetcher {_,_,offset->
                calls++;if(offset>0)throw error
                GetSharedFileChunkResponse.newBuilder().setData(ByteString.copyFrom(ByteArray(16))).setTotalBytes(32L).build()
            },isMachineOnline={true},blockBytesProvider={16},clock={testScheduler.currentTime},sleeper={kotlinx.coroutines.delay(it)},ioDispatcher=StandardTestDispatcher(testScheduler))
            try {
                store.startOrResume("s",file);testScheduler.advanceUntilIdle()
                assertEquals(row.toString(),if(retry)3 else 2,calls)
                val state=store.states.value["s:policy"]
                if(code=="not_found") assertEquals(SharedFileTransferState.CleanedUp,state)
                else {
                    assertTrue(row.toString(),state is SharedFileTransferState.Failed)
                    assertEquals(if(retry)SharedFileFailureKind.DISCONNECTED else SharedFileFailureKind.OTHER,(state as SharedFileTransferState.Failed).kind)
                }
            } finally {store.close();root.deleteRecursively()}
        }
    }

    private data class VectorCase(
        val name: String,
        val sizeBytes: Long,
        val offsets: List<Long>,
        val lastBlockFileBytes: Int,
        val invalidOffsets: List<Long>,
        val sha256: String,
    )

    private fun tempRoot(): File = Files.createTempDirectory("shared-files-test").toFile()

    private fun deterministicBytes(size: Int, offset: Long = 0L): ByteArray =
        ByteArray(size) { idx -> ((offset + idx) % 251L).toByte() }

    private fun hexToBytes(hex: String): ByteArray =
        ByteArray(hex.length / 2) { i -> hex.substring(i * 2, i * 2 + 2).toInt(16).toByte() }

    private fun sharedFile(
        id: String = "file-1",
        name: String = "video.mp4",
        sizeBytes: Long,
        sha256: ByteArray = ByteArray(0),
        path: String = "/Users/alex/Movies/video.mp4",
    ): SharedFile =
        SharedFile.newBuilder()
            .setFileId(id)
            .setName(name)
            .setSizeBytes(sizeBytes)
            .setSha256(ByteString.copyFrom(sha256))
            .setPath(path)
            .build()

    @Test
    fun vectorsBlockMathAndDownloadsMatchFixture() = runTest {
        val path = System.getProperty("sharedFileVectors")
            ?: File("../testdata/shared-files/blocks.json").absolutePath
        val obj = Json.parseToJsonElement(File(path).readText()).jsonObject
        val blockBytes = obj.getValue("blockBytes").jsonPrimitive.content.toInt()
        assertEquals(SHARED_FILE_BLOCK_BYTES, blockBytes)
        val cases = obj.getValue("cases").jsonArray.map { el ->
            val c = el.jsonObject
            VectorCase(
                name = c.getValue("name").jsonPrimitive.content,
                sizeBytes = c.getValue("sizeBytes").jsonPrimitive.content.toLong(),
                offsets = c.getValue("offsets").jsonArray.map { it.jsonPrimitive.content.toLong() },
                lastBlockFileBytes = c.getValue("lastBlockFileBytes").jsonPrimitive.content.toInt(),
                invalidOffsets = c.getValue("invalidOffsets").jsonArray.map { it.jsonPrimitive.content.toLong() },
                sha256 = c["sha256"]?.jsonPrimitive?.content.orEmpty(),
            )
        }

        val root = tempRoot()
        try {
            val dispatcher = StandardTestDispatcher(testScheduler)
            val store = SharedFileStore(
                rootDir = root,
                fetcher = SharedFileChunkFetcher { _, _, _ ->
                    error("handled per-case")
                },
                isMachineOnline = { true },
                ioDispatcher = dispatcher,
            )
            store.close()

            for (case in cases) {
                val caseSize = case.sizeBytes
                val caseOffsets = case.offsets
                val caseLastBlockBytes = case.lastBlockFileBytes
                val caseInvalidOffsets = case.invalidOffsets
                val plan = planSharedFileBlocks(caseSize, blockBytes)
                assertEquals("offsets for ${case.name}", caseOffsets, plan.offsets)
                assertEquals("lastBlockFileBytes for ${case.name}", caseLastBlockBytes, plan.lastBlockFileBytes)
                for (validOffset in caseOffsets) {
                    assertTrue("valid $validOffset for ${case.name}", isValidSharedFileOffset(validOffset, caseSize, blockBytes))
                }
                for (badOffset in caseInvalidOffsets) {
                    assertFalse("invalid $badOffset for ${case.name}", isValidSharedFileOffset(badOffset, caseSize, blockBytes))
                }

                // Run end-to-end chunk assembly for all non-largest cases (up to 2 blocks).
                if (caseSize <= blockBytes * 2L) {
                    val requestedOffsets = mutableListOf<Long>()
                    val caseStore = SharedFileStore(
                        rootDir = File(root, case.name),
                        fetcher = SharedFileChunkFetcher { _, _, offset ->
                            requestedOffsets += offset
                            val block = ByteArray(blockBytes)
                            val copyLen = minOf(blockBytes.toLong(), (caseSize - offset).coerceAtLeast(0L)).toInt()
                            for (i in 0 until copyLen) {
                                block[i] = ((offset + i) % 251L).toByte()
                            }
                            GetSharedFileChunkResponse.newBuilder()
                                .setData(ByteString.copyFrom(block))
                                .setTotalBytes(caseSize)
                                .build()
                        },
                        isMachineOnline = { true },
                        clock = { testScheduler.currentTime },
                        sleeper = { ms -> kotlinx.coroutines.delay(ms) },
                        ioDispatcher = dispatcher,
                    )
                    try {
                        val file = sharedFile(
                            id = "id-${case.name}",
                            name = "${case.name}.bin",
                            sizeBytes = caseSize,
                            sha256 = hexToBytes(case.sha256),
                        )
                        caseStore.startOrResume("s1", file)
                        testScheduler.advanceUntilIdle()
                        val st = caseStore.states.value["s1:${file.fileId}"]
                        assertTrue("expected Downloaded for ${case.name}, got $st", st is SharedFileTransferState.Downloaded)
                        val local = (st as SharedFileTransferState.Downloaded).localFile
                        assertEquals(caseOffsets, requestedOffsets)
                        assertEquals(caseSize, local.length())
                        assertArrayEquals(deterministicBytes(caseSize.toInt()), local.readBytes())
                    } finally {
                        caseStore.close()
                    }
                }
            }
        } finally {
            root.deleteRecursively()
        }
    }

    @Test
    fun tileStylesSeparateAudioFromArchivesAndDocs() {
        assertEquals(SharedFileTileStyle.VIDEO, classifySharedFile("clip.mp4").tileStyle())
        assertEquals(SharedFileTileStyle.IMAGE, classifySharedFile("cover.png").tileStyle())
        assertEquals(SharedFileTileStyle.AUDIO, classifySharedFile("track.mp3").tileStyle())
        assertEquals(SharedFileTileStyle.ARCHIVE_OR_OTHER, classifySharedFile("proj.zip").tileStyle())
        assertEquals(SharedFileTileStyle.ARCHIVE_OR_OTHER, classifySharedFile("blob.bin").tileStyle())
        assertEquals(SharedFileTileStyle.PDF_OR_DOC, classifySharedFile("report.pdf").tileStyle())
    }

    @Test
    fun pauseAndAppRestartResumesFromPersistedBlocks() = runTest {
        val root = tempRoot()
        try {
            val dispatcher = StandardTestDispatcher(testScheduler)
            val blockBytes = 1024
            val sizeBytes = 3500L
            val fullPayload = deterministicBytes(sizeBytes.toInt())
            val digest = java.security.MessageDigest.getInstance("SHA-256").digest(fullPayload)
            val file = sharedFile(id = "f-resume", name = "movie.mp4", sizeBytes = sizeBytes, sha256 = digest)

            val calls = AtomicInteger(0)
            var pauseAtCall = 2
            lateinit var store1: SharedFileStore
            store1 = SharedFileStore(
                rootDir = root,
                fetcher = SharedFileChunkFetcher { _, _, offset ->
                    val n = calls.incrementAndGet()
                    if (n == pauseAtCall) {
                        store1.pause("s1", file)
                        kotlinx.coroutines.delay(50)
                    }
                    val block = ByteArray(blockBytes)
                    val len = minOf(blockBytes.toLong(), sizeBytes - offset).toInt()
                    System.arraycopy(fullPayload, offset.toInt(), block, 0, len)
                    GetSharedFileChunkResponse.newBuilder()
                        .setData(ByteString.copyFrom(block))
                        .setTotalBytes(sizeBytes)
                        .build()
                },
                isMachineOnline = { true },
                blockBytesProvider = { blockBytes },
                clock = { testScheduler.currentTime },
                sleeper = { ms -> kotlinx.coroutines.delay(ms) },
                ioDispatcher = dispatcher,
            )

            store1.startOrResume("s1", file)
            testScheduler.advanceUntilIdle()
            val pausedState = store1.states.value["s1:f-resume"]
            assertEquals(SharedFileTransferState.Paused(blockBytes.toLong(), sizeBytes), pausedState)
            store1.close()

            // Simulate process restart with fresh SharedFileStore reading persisted .part/.meta.
            val resumedOffsets = mutableListOf<Long>()
            pauseAtCall = -1
            val store2 = SharedFileStore(
                rootDir = root,
                fetcher = SharedFileChunkFetcher { _, _, offset ->
                    resumedOffsets += offset
                    val block = ByteArray(blockBytes)
                    val len = minOf(blockBytes.toLong(), sizeBytes - offset).toInt()
                    System.arraycopy(fullPayload, offset.toInt(), block, 0, len)
                    GetSharedFileChunkResponse.newBuilder()
                        .setData(ByteString.copyFrom(block))
                        .setTotalBytes(sizeBytes)
                        .build()
                },
                isMachineOnline = { true },
                blockBytesProvider = { blockBytes },
                clock = { testScheduler.currentTime },
                sleeper = { ms -> kotlinx.coroutines.delay(ms) },
                ioDispatcher = dispatcher,
                durationExtractor = { 102_000L },
            )
            try {
                assertEquals(
                    SharedFileTransferState.Paused(blockBytes.toLong(), sizeBytes),
                    store2.hydrate("s1", file),
                )
                store2.startOrResume("s1", file)
                testScheduler.advanceUntilIdle()
                // Should NOT re-fetch block 0!
                assertEquals(listOf(1024L, 2048L, 3072L), resumedOffsets)
                val finalState = store2.states.value["s1:f-resume"] as SharedFileTransferState.Downloaded
                assertEquals(102_000L, finalState.durationMs)
                assertArrayEquals(fullPayload, finalState.localFile.readBytes())
            } finally {
                store2.close()
            }
        } finally {
            root.deleteRecursively()
        }
    }

    @Test
    fun pauseAndCancelCannotBeOverwrittenByALateChunkResponse() = runTest {
        for (cancel in listOf(false, true)) {
            val root = tempRoot()
            val file = sharedFile(sizeBytes = 2048L)
            lateinit var store: SharedFileStore
            var interrupt = true
            val offsets = mutableListOf<Long>()
            store = SharedFileStore(
                rootDir = root,
                fetcher = SharedFileChunkFetcher { _, _, offset ->
                    offsets += offset
                    if (offset == 1024L && interrupt) {
                        // Model a synchronous response arriving while the UI cancels its job.
                        if (cancel) store.cancel("s", file) else store.pause("s", file)
                    }
                    GetSharedFileChunkResponse.newBuilder()
                        .setData(ByteString.copyFrom(ByteArray(1024)))
                        .setTotalBytes(2048L).build()
                },
                isMachineOnline = { true },
                blockBytesProvider = { 1024 },
                ioDispatcher = StandardTestDispatcher(testScheduler),
            )
            try {
                store.startOrResume("s", file)
                testScheduler.advanceUntilIdle()
                if (cancel) {
                    assertEquals(SharedFileTransferState.Idle, store.states.value["s:file-1"])
                    assertFalse(File(root, "s/file-1/download.part").exists())
                } else {
                    assertEquals(SharedFileTransferState.Paused(1024, 2048), store.states.value["s:file-1"])
                    assertEquals(1024L, File(root, "s/file-1/download.part").length())
                }
                interrupt = false
                offsets.clear()
                store.startOrResume("s", file)
                testScheduler.advanceUntilIdle()
                assertEquals(if (cancel) listOf(0L, 1024L) else listOf(1024L), offsets)
                assertTrue(store.states.value["s:file-1"] is SharedFileTransferState.Downloaded)
            } finally {
                store.close()
                root.deleteRecursively()
            }
        }
    }

    @Test
    fun cancelAndChecksumMismatchDeletePartialFile() = runTest {
        val root = tempRoot()
        try {
            val dispatcher = StandardTestDispatcher(testScheduler)
            val blockBytes = 512
            val payload = deterministicBytes(1200)
            val wrongHash = ByteArray(32) { 0x5A }
            val file = sharedFile(id = "f-bad", name = "bad.zip", sizeBytes = 1200L, sha256 = wrongHash)

            val store = SharedFileStore(
                rootDir = root,
                fetcher = SharedFileChunkFetcher { _, _, offset ->
                    val block = ByteArray(blockBytes)
                    val len = minOf(blockBytes.toLong(), 1200L - offset).toInt()
                    System.arraycopy(payload, offset.toInt(), block, 0, len)
                    GetSharedFileChunkResponse.newBuilder()
                        .setData(ByteString.copyFrom(block))
                        .setTotalBytes(1200L)
                        .build()
                },
                isMachineOnline = { true },
                blockBytesProvider = { blockBytes },
                clock = { testScheduler.currentTime },
                sleeper = { ms -> kotlinx.coroutines.delay(ms) },
                ioDispatcher = dispatcher,
            )
            try {
                store.startOrResume("s1", file)
                testScheduler.advanceUntilIdle()
                assertEquals(
                    SharedFileTransferState.Failed(SharedFileFailureKind.CHECKSUM_MISMATCH),
                    store.states.value["s1:f-bad"],
                )
                assertEquals(0L, store.usedBytes())

                // Cancel deletes partial files too.
                val goodHash = java.security.MessageDigest.getInstance("SHA-256").digest(payload)
                val goodFile = sharedFile(id = "f-cancel", name = "good.zip", sizeBytes = 1200L, sha256 = goodHash)
                val cancelStore = SharedFileStore(
                    rootDir = File(root, "cancel"),
                    fetcher = SharedFileChunkFetcher { _, _, _ ->
                        kotlinx.coroutines.delay(100)
                        GetSharedFileChunkResponse.getDefaultInstance()
                    },
                    isMachineOnline = { true },
                    blockBytesProvider = { blockBytes },
                    ioDispatcher = dispatcher,
                )
                cancelStore.startOrResume("s1", goodFile)
                testScheduler.runCurrent()
                cancelStore.cancel("s1", goodFile)
                testScheduler.advanceUntilIdle()
                assertEquals(SharedFileTransferState.Idle, cancelStore.states.value["s1:f-cancel"])
                assertFalse(File(root, "cancel/s1/f-cancel/download.part").exists())
                cancelStore.close()
            } finally {
                store.close()
            }
        } finally {
            root.deleteRecursively()
        }
    }

    @Test
    fun transientDisconnectWaitsUpTo30SecondsAndRetriesOnce() = runTest {
        val root = tempRoot()
        try {
            val dispatcher = StandardTestDispatcher(testScheduler)
            val blockBytes = 512
            val payload = deterministicBytes(800)
            val hash = java.security.MessageDigest.getInstance("SHA-256").digest(payload)
            val file = sharedFile(id = "f-retry", name = "doc.pdf", sizeBytes = 800L, sha256 = hash)

            var online = true
            var attempts = 0
            val store = SharedFileStore(
                rootDir = root,
                fetcher = SharedFileChunkFetcher { _, _, offset ->
                    attempts++
                    if (attempts == 2) {
                        online = false
                        throw CommandException(CommandException.UNAVAILABLE, "offline")
                    }
                    val block = ByteArray(blockBytes)
                    val len = minOf(blockBytes.toLong(), 800L - offset).toInt()
                    System.arraycopy(payload, offset.toInt(), block, 0, len)
                    GetSharedFileChunkResponse.newBuilder()
                        .setData(ByteString.copyFrom(block))
                        .setTotalBytes(800L)
                        .build()
                },
                isMachineOnline = {
                    // Come back online after 5 seconds inside the 30s window.
                    if (!online && testScheduler.currentTime >= 5_000L) online = true
                    online
                },
                blockBytesProvider = { blockBytes },
                clock = { testScheduler.currentTime },
                sleeper = { ms -> kotlinx.coroutines.delay(ms) },
                ioDispatcher = dispatcher,
            )
            try {
                store.startOrResume("s1", file)
                testScheduler.advanceUntilIdle()
                val st = store.states.value["s1:f-retry"]
                assertTrue("should recover after reconnect, got $st", st is SharedFileTransferState.Downloaded)
                assertEquals(3, attempts) // block0 ok, block1 fails, block1 retried ok
            } finally {
                store.close()
            }
        } finally {
            root.deleteRecursively()
        }
    }

    @Test
    fun remainingTimeIsHiddenForTheFirstThreeSeconds() = runTest {
        val root = tempRoot()
        val file = sharedFile(sizeBytes = 5120)
        lateinit var store: SharedFileStore
        store = SharedFileStore(
            rootDir = root,
            fetcher = SharedFileChunkFetcher { _, _, offset ->
                val state = store.states.value["s:file-1"] as SharedFileTransferState.Downloading
                if (offset < 3072) assertEquals(null, state.etaSeconds)
                if (offset == 3072L) assertTrue(state.etaSeconds != null)
                kotlinx.coroutines.delay(1000)
                GetSharedFileChunkResponse.newBuilder().setTotalBytes(5120)
                    .setData(ByteString.copyFrom(ByteArray(1024))).build()
            },
            isMachineOnline = { true },
            blockBytesProvider = { 1024 },
            clock = { testScheduler.currentTime },
            ioDispatcher = StandardTestDispatcher(testScheduler),
        )
        try {
            store.startOrResume("s", file)
            testScheduler.advanceUntilIdle()
            assertTrue(store.states.value["s:file-1"] is SharedFileTransferState.Downloaded)
        } finally {
            store.close()
            root.deleteRecursively()
        }
    }

    @Test
    fun longDisconnectFailsWithBlocksIntactAndRetryStartsAtTheirOffset() = runTest {
        val root = tempRoot()
        val offsets = mutableListOf<Long>()
        var online = true
        var drop = true
        val file = sharedFile(sizeBytes = 2048)
        val store = SharedFileStore(
            rootDir = root,
            fetcher = SharedFileChunkFetcher { _, _, offset ->
                offsets += offset
                if (drop && offset > 0) {
                    online = false
                    throw CommandException("network", "connection dropped")
                }
                GetSharedFileChunkResponse.newBuilder().setTotalBytes(2048)
                    .setData(ByteString.copyFrom(ByteArray(1024))).build()
            },
            isMachineOnline = { online },
            blockBytesProvider = { 1024 },
            clock = { testScheduler.currentTime },
            sleeper = { kotlinx.coroutines.delay(it) },
            ioDispatcher = StandardTestDispatcher(testScheduler),
        )
        try {
            store.startOrResume("s", file)
            testScheduler.advanceUntilIdle()
            assertEquals(30_000L, testScheduler.currentTime)
            assertEquals(SharedFileTransferState.Failed(SharedFileFailureKind.DISCONNECTED), store.states.value["s:file-1"])
            assertEquals(1024L, File(root, "s/file-1/download.part").length())
            online = true
            drop = false
            offsets.clear()
            store.startOrResume("s", file)
            testScheduler.advanceUntilIdle()
            assertEquals(listOf(1024L), offsets)
            assertTrue(store.states.value["s:file-1"] is SharedFileTransferState.Downloaded)
        } finally {
            store.close()
            root.deleteRecursively()
        }
    }

    @Test
    fun disconnectAfter30sTimeoutAndNotFoundAndDiskFull() = runTest {
        val root = tempRoot()
        try {
            val dispatcher = StandardTestDispatcher(testScheduler)
            val blockBytes = 512
            val payload = deterministicBytes(800)
            val hash = java.security.MessageDigest.getInstance("SHA-256").digest(payload)

            val fileDropZero = sharedFile(id = "f-drop0", name = "a.bin", sizeBytes = 800L, sha256 = hash)
            val storeDrop = SharedFileStore(
                rootDir = File(root, "drop"),
                fetcher = SharedFileChunkFetcher { _, _, _ ->
                    throw CommandException("network", "network dropped")
                },
                isMachineOnline = { true },
                blockBytesProvider = { blockBytes },
                clock = { testScheduler.currentTime },
                sleeper = { ms -> kotlinx.coroutines.delay(ms) },
                ioDispatcher = dispatcher,
            )
            storeDrop.startOrResume("s1", fileDropZero)
            testScheduler.advanceUntilIdle()
            assertEquals(
                SharedFileTransferState.Failed(SharedFileFailureKind.DISCONNECTED),
                storeDrop.states.value["s1:f-drop0"],
            )
            storeDrop.close()

            val fileNotFound = sharedFile(id = "f-gone", name = "gone.pdf", sizeBytes = 100L)
            val storeGone = SharedFileStore(
                rootDir = File(root, "gone"),
                fetcher = SharedFileChunkFetcher { _, _, _ ->
                    throw CommandException("not_found", "cleaned")
                },
                isMachineOnline = { true },
                blockBytesProvider = { blockBytes },
                ioDispatcher = dispatcher,
            )
            storeGone.startOrResume("s1", fileNotFound)
            testScheduler.advanceUntilIdle()
            assertEquals(SharedFileTransferState.CleanedUp, storeGone.states.value["s1:f-gone"])
            storeGone.close()

            val fileFull = sharedFile(id = "f-full", name = "big.zip", sizeBytes = 100L)
            val storeFull = SharedFileStore(
                rootDir = File(root, "full"),
                fetcher = SharedFileChunkFetcher { _, _, _ ->
                    throw IOException("write failed: ENOSPC (No space left on device)")
                },
                isMachineOnline = { true },
                blockBytesProvider = { blockBytes },
                ioDispatcher = dispatcher,
            )
            storeFull.startOrResume("s1", fileFull)
            testScheduler.advanceUntilIdle()
            assertEquals(
                SharedFileTransferState.Failed(SharedFileFailureKind.DISK_FULL, "write failed: ENOSPC (No space left on device)"),
                storeFull.states.value["s1:f-full"],
            )
            storeFull.close()
        } finally {
            root.deleteRecursively()
        }
    }

    @Test
    fun uiStateResolutionPreservesDownloadedEvenWhenOfflineOrExited() {
        val root = tempRoot()
        try {
            val localFile = File(root, "done.mp4").apply { writeBytes(byteArrayOf(1, 2, 3)) }
            val sf = sharedFile(id = "f1", name = "done.mp4", sizeBytes = 3L)

            val dl = resolveSharedFileUiState(
                sf,
                SharedFileTransferState.Downloaded(localFile, durationMs = 102_000L),
                machineOnline = false,
                sessionExited = true,
            )
            assertEquals(CardVisualState.DOWNLOADED, dl.visual)
            assertEquals(102_000L, dl.durationMs)

            val pausedOffline = resolveSharedFileUiState(
                sf,
                SharedFileTransferState.Paused(512L, 1024L),
                machineOnline = false,
                sessionExited = false,
            )
            assertEquals(CardVisualState.PAUSED, pausedOffline.visual)

            val offline = resolveSharedFileUiState(
                sf,
                SharedFileTransferState.Idle,
                machineOnline = false,
                sessionExited = false,
            )
            assertEquals(CardVisualState.MACHINE_OFFLINE, offline.visual)

            val exited = resolveSharedFileUiState(
                sf,
                SharedFileTransferState.Paused(512L, 1024L),
                machineOnline = false,
                sessionExited = true,
            )
            assertEquals(CardVisualState.CLEANED_UP, exited.visual)
        } finally {
            root.deleteRecursively()
        }
    }

    @Test
    fun classificationPathSanitizationAndFormatting() {
        assertEquals(SharedFileKind.VIDEO, classifySharedFile("daocheng.mp4"))
        assertEquals(SharedFileKind.IMAGE, classifySharedFile("cover.PNG"))
        assertEquals(SharedFileKind.AUDIO, classifySharedFile("voice.m4a"))
        assertEquals(SharedFileKind.ARCHIVE, classifySharedFile("project.zip"))
        assertEquals(SharedFileKind.PDF, classifySharedFile("report.pdf"))
        assertEquals(SharedFileKind.DOC, classifySharedFile("notes.md"))
        assertEquals(SharedFileKind.OTHER, classifySharedFile("raw.unknown"))

        assertEquals("evil.png", sanitizeSharedFileName("../../\u202eevil.png\u0000"))
        assertEquals("file", sanitizeSharedFileName("../.."))

        assertEquals("~/Movies/daocheng/out/daocheng-final.mp4", displaySharedPath("/Users/alice/Movies/daocheng/out/daocheng-final.mp4", "x.mp4"))
        assertEquals("~/work/a.zip", displaySharedPath("/home/bob/work/a.zip", "a.zip"))

        assertEquals("24.3 MB", formatSharedFileSize(24_300_000L))
        assertEquals("8.1" to "24.3 MB", formatSharedProgress(8_100_000L, 24_300_000L))
        assertEquals("512.0 KB" to "24.3 MB", formatSharedProgress(512_000L, 24_300_000L))
        assertEquals("1:42", formatMediaDuration(102_000L))
    }
}
