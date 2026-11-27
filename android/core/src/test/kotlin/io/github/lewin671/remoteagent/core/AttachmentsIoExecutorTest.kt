package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.proto.AttachmentCandidateInput
import io.github.lewin671.remoteagent.core.proto.AttachmentHashFileEffect
import io.github.lewin671.remoteagent.core.proto.AttachmentIOErrorKind
import io.github.lewin671.remoteagent.core.proto.AttachmentItemKind
import io.github.lewin671.remoteagent.core.proto.AttachmentItemState
import io.github.lewin671.remoteagent.core.proto.AttachmentPrepareImageEffect
import io.github.lewin671.remoteagent.core.proto.AttachmentReadChunkEffect
import io.github.lewin671.remoteagent.core.proto.AttachmentReleaseLocalEffect
import io.github.lewin671.remoteagent.core.proto.DraftPickedItem
import io.github.lewin671.remoteagent.core.proto.DraftStorageBucket
import io.github.lewin671.remoteagent.core.proto.DraftStorageClearAllEffect
import io.github.lewin671.remoteagent.core.proto.DraftStorageDeleteEffect
import io.github.lewin671.remoteagent.core.proto.DraftStorageListEffect
import io.github.lewin671.remoteagent.core.proto.DraftStorageReadEffect
import io.github.lewin671.remoteagent.core.proto.DraftStorageWriteEffect
import io.github.lewin671.remoteagent.core.proto.CacheBootstrapLoadedEvent
import io.github.lewin671.remoteagent.core.proto.CacheReadEffect
import io.github.lewin671.remoteagent.core.proto.DemoSecurity
import io.github.lewin671.remoteagent.core.proto.Effect
import io.github.lewin671.remoteagent.core.proto.Event as CoreEvent
import io.github.lewin671.remoteagent.core.proto.RelayRpcMethod
import io.github.lewin671.remoteagent.core.proto.RelayRpcResultEvent
import io.github.lewin671.remoteagent.core.proto.SeedDemoWorldEvent
import io.github.lewin671.remoteagent.core.proto.StreamReadyEvent
import io.github.lewin671.remoteagent.core.proto.SyncLifecycleKind
import io.github.lewin671.remoteagent.proto.GetAccountPrefsResponse
import io.github.lewin671.remoteagent.proto.ImageFormat
import io.github.lewin671.remoteagent.proto.ListDevicesResponse
import io.github.lewin671.remoteagent.proto.ListMachinesResponse
import io.github.lewin671.remoteagent.proto.ListRemoteSessionsResponse
import io.github.lewin671.remoteagent.proto.Machine
import io.github.lewin671.remoteagent.proto.MachineCapabilities
import io.github.lewin671.remoteagent.proto.MachineMeta
import java.io.File
import java.nio.file.Files
import java.util.Collections
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout

class AttachmentsIoExecutorTest {

    private fun withTempDir(block: (File) -> Unit) {
        val dir = Files.createTempDirectory("ra-att-test-").toFile()
        try {
            block(dir)
        } finally {
            dir.deleteRecursively()
        }
    }

    private class FakePlatformIo(
        private val onExternalRelease: (String) -> Unit = {},
        private val externalRefs: () -> List<String> = { emptyList() },
        private val onPrepare: (AttachmentPrepareImageEffect, (String, ByteArray) -> String) -> PreparedImageOutcome = { _, storeBlob ->
            val bytes = ByteArray(1024) { (it % 251).toByte() }
            val prepRef = storeBlob(".prep", bytes)
            val thumbRef = storeBlob(".thumb", byteArrayOf(1, 2, 3, 4))
            PreparedImageOutcome(
                ok = true,
                preparedRef = prepRef,
                thumbRef = thumbRef,
                format = ImageFormat.IMAGE_FORMAT_JPEG,
                width = 640,
                height = 480,
                sizeBytes = bytes.size.toLong(),
                sha256 = sha256(bytes),
            )
        },
        private val onHash: (AttachmentHashFileEffect) -> HashedFileOutcome = { eff ->
            val data = eff.sourceRef.toByteArray()
            HashedFileOutcome(ok = true, sizeBytes = data.size.toLong(), sha256 = sha256(data))
        },
        private val onReadFile: (AttachmentReadChunkEffect) -> ReadChunkOutcome = { eff ->
            val data = eff.sourceRef.toByteArray()
            ReadChunkOutcome(ok = true, data = data.copyOfRange(eff.offset.toInt(), eff.offset.toInt() + eff.length))
        },
    ) : AttachmentPlatformIo {
        override fun prepareImage(
            eff: AttachmentPrepareImageEffect,
            storeBlob: (suffix: String, bytes: ByteArray) -> String,
        ): PreparedImageOutcome = onPrepare(eff, storeBlob)

        override fun hashFile(eff: AttachmentHashFileEffect): HashedFileOutcome = onHash(eff)

        override fun readFileChunk(eff: AttachmentReadChunkEffect): ReadChunkOutcome = onReadFile(eff)

        override fun listExternalLocalRefs(): List<String> = externalRefs()

        override fun releaseExternalRef(ref: String) = onExternalRelease(ref)
    }

    @Test
    fun encryptedRecordsRoundTripCorruptReportingAndLegacyCleanup() = withTempDir { root ->
        val keyProvider = InMemoryAttachmentKeyProvider()
        val releasedExternal = Collections.synchronizedList(ArrayList<String>())
        val exec = AttachmentIoExecutor(
            rootDir = root,
            keyProvider = keyProvider,
            platformIo = FakePlatformIo(
                externalRefs = { listOf("content://ext/orphan-1") },
                onExternalRelease = { releasedExternal += it },
            ),
        )
        try {
            // Seed a legacy pre-3C-1 directory directly under root.
            val legacyDir = File(root, "deadbeef01234567").apply { mkdirs() }
            File(legacyDir, "order").writeText("legacy")

            val payload = byteArrayOf(10, 20, 30, 40)
            val wr = exec.executeWrite(
                DraftStorageWriteEffect.newBuilder()
                    .setOpId(1L)
                    .setBucket(DraftStorageBucket.DRAFT_STORAGE_BUCKET_DRAFT)
                    .setKey("session:s1")
                    .setData(ByteString.copyFrom(payload))
                    .build(),
            )
            assertTrue(wr.ok)

            // Ensure plaintext does not appear on disk.
            val draftFiles = File(root, "drafts").listFiles().orEmpty()
            assertEquals(1, draftFiles.size)
            assertFalse(draftFiles.single().readBytes().toString(Charsets.ISO_8859_1).contains("session:s1"))

            // Read back matches exact payload.
            val rd = exec.executeRead(
                DraftStorageReadEffect.newBuilder()
                    .setOpId(2L)
                    .setBucket(DraftStorageBucket.DRAFT_STORAGE_BUCKET_DRAFT)
                    .setKey("session:s1")
                    .build(),
            )
            assertTrue(rd.found)
            assertEquals(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_UNSPECIFIED, rd.errorKind)
            assertArrayEquals(payload, rd.data.toByteArray())

            // Plant a corrupt record in outbox and an orphan blob in blobs/.
            val outboxDir = File(root, "outbox").apply { mkdirs() }
            val corruptFile = File(outboxDir, "corrupt0001.rec").apply { writeBytes(byteArrayOf(1, 2, 3)) }
            val blobRef = exec.writeEncryptedBlob(".prep", byteArrayOf(7, 8, 9))

            // List reports valid draft, corrupt outbox entry, blob ref + external ref, and drops legacy dir.
            val listed = exec.executeList(DraftStorageListEffect.newBuilder().setOpId(3L).build())
            assertFalse("legacy folder should be cleaned on list", legacyDir.exists())
            assertEquals(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_UNSPECIFIED, listed.errorKind)
            val validEntry = listed.entriesList.first { !it.corrupt }
            assertEquals(DraftStorageBucket.DRAFT_STORAGE_BUCKET_DRAFT, validEntry.bucket)
            assertEquals("session:s1", validEntry.key)
            assertArrayEquals(payload, validEntry.data.toByteArray())
            val corruptEntry = listed.entriesList.first { it.corrupt }
            assertEquals(DraftStorageBucket.DRAFT_STORAGE_BUCKET_OUTBOX, corruptEntry.bucket)
            assertTrue(listed.localRefsList.contains(blobRef))
            assertTrue(listed.localRefsList.contains("content://ext/orphan-1"))

            // Deleting the corrupt entry by its reported key removes the corrupt file on disk.
            exec.executeDelete(
                DraftStorageDeleteEffect.newBuilder()
                    .setBucket(corruptEntry.bucket)
                    .setKey(corruptEntry.key)
                    .build(),
            )
            assertFalse(corruptFile.exists())

            // Releasing local handles deletes blob and invokes external release callback.
            exec.executeReleaseLocal(
                AttachmentReleaseLocalEffect.newBuilder()
                    .addRefs(blobRef)
                    .addRefs("content://ext/orphan-1")
                    .build(),
            )
            assertNull(exec.readEncryptedBlobFull(blobRef))
            assertEquals(listOf("content://ext/orphan-1"), releasedExternal)
        } finally {
            exec.close()
        }
    }

    @Test
    fun chunkedEncryptedBlobSupportsMultiChunkSeekAndDetectsSizeChangeOrTamper() = withTempDir { root ->
        val keyProvider = InMemoryAttachmentKeyProvider()
        val exec = AttachmentIoExecutor(
            rootDir = root,
            keyProvider = keyProvider,
            platformIo = FakePlatformIo(),
        )
        try {
            // > 2 chunks (256 KiB * 2 + 12345 B) so reads cross chunk boundaries.
            val big = ByteArray(256 * 1024 * 2 + 12_345) { ((it * 31 + 7) and 0xFF).toByte() }
            val ref = exec.writeEncryptedBlob(".prep", big)

            val full = exec.readEncryptedBlobFull(ref)
            assertNotNull(full)
            assertArrayEquals(big, full)

            // Cross-boundary range read [250 KiB, +20 KiB].
            val off = 250 * 1024L
            val len = 20 * 1024
            val chunk = exec.executeReadChunk(
                AttachmentReadChunkEffect.newBuilder()
                    .setOpId(10L)
                    .setKind(AttachmentItemKind.ATTACHMENT_ITEM_KIND_IMAGE)
                    .setPreparedRef(ref)
                    .setOffset(off)
                    .setLength(len)
                    .setExpectedSizeBytes(big.size.toLong())
                    .build(),
            )
            assertTrue(chunk.ok)
            assertEquals(off, chunk.offset)
            assertArrayEquals(big.copyOfRange(off.toInt(), off.toInt() + len), chunk.data.toByteArray())

            // Size mismatch -> SOURCE_CHANGED.
            val mismatch = exec.executeReadChunk(
                AttachmentReadChunkEffect.newBuilder()
                    .setOpId(11L)
                    .setKind(AttachmentItemKind.ATTACHMENT_ITEM_KIND_IMAGE)
                    .setPreparedRef(ref)
                    .setOffset(0L)
                    .setLength(100)
                    .setExpectedSizeBytes(big.size.toLong() + 1L)
                    .build(),
            )
            assertFalse(mismatch.ok)
            assertEquals(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_SOURCE_CHANGED, mismatch.errorKind)

            // Tampering ciphertext -> CORRUPT_STORE.
            val file = exec.blobFile(ref)!!
            val raw = file.readBytes()
            raw[raw.size - 1] = (raw[raw.size - 1].toInt() xor 0xFF).toByte()
            file.writeBytes(raw)
            val tampered = exec.executeReadChunk(
                AttachmentReadChunkEffect.newBuilder()
                    .setOpId(12L)
                    .setKind(AttachmentItemKind.ATTACHMENT_ITEM_KIND_IMAGE)
                    .setPreparedRef(ref)
                    .setOffset(big.size.toLong() - 100L)
                    .setLength(100)
                    .setExpectedSizeBytes(big.size.toLong())
                    .build(),
            )
            assertFalse(tampered.ok)
            assertEquals(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_CORRUPT_STORE, tampered.errorKind)
        } finally {
            exec.close()
        }
    }

    @Test
    fun writeQuotaExceededMapsToStorageFull() = withTempDir { root ->
        val exec = AttachmentIoExecutor(
            rootDir = root,
            keyProvider = InMemoryAttachmentKeyProvider(),
            platformIo = FakePlatformIo(),
            draftLimitBytes = 64L,
        )
        try {
            val res = exec.executeWrite(
                DraftStorageWriteEffect.newBuilder()
                    .setOpId(1L)
                    .setBucket(DraftStorageBucket.DRAFT_STORAGE_BUCKET_DRAFT)
                    .setKey("session:full")
                    .setData(ByteString.copyFrom(ByteArray(128)))
                    .build(),
            )
            assertFalse(res.ok)
            assertEquals(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_STORAGE_FULL, res.errorKind)
        } finally {
            exec.close()
        }
    }

    @Test
    fun storageEffectsExecuteStrictlyInGlobalFifoOrder() = withTempDir { root ->
        val exec = AttachmentIoExecutor(
            rootDir = root,
            keyProvider = InMemoryAttachmentKeyProvider(),
            platformIo = FakePlatformIo(),
        )
        val host = CoreHost()
        try {
            exec.attach(host)
            val writeRes1 = CompletableDeferred<Boolean>()
            val writeRes2 = CompletableDeferred<Boolean>()
            val readData = CompletableDeferred<ByteArray?>()
            val blobRef = exec.writeEncryptedBlob(".prep", byteArrayOf(42, 43))

            // Queue: Write("v1") -> Write("v2") -> Read -> ReleaseLocal(blob) -> Delete -> Read(gone)
            // Because all 6 go through the single FIFO queue, the first read must see "v2",
            // and after Delete + awaitStorageIdle both record and blob must be gone.
            runBlocking {
                val origWrite = host.onDraftStorageWrite
                val origRead = host.onDraftStorageRead
                val origDel = host.onDraftStorageDelete
                val origRel = host.onAttachmentReleaseLocal
                val order = Collections.synchronizedList(ArrayList<String>())

                host.onDraftStorageWrite = { eff ->
                    order += "enqueue-write-${eff.opId}"
                    origWrite(eff)
                }
                host.onDraftStorageRead = { eff ->
                    order += "enqueue-read-${eff.opId}"
                    origRead(eff)
                }

                host.onDraftStorageWrite(
                    DraftStorageWriteEffect.newBuilder()
                        .setOpId(1L)
                        .setBucket(DraftStorageBucket.DRAFT_STORAGE_BUCKET_DRAFT)
                        .setKey("session:fifo")
                        .setData(ByteString.copyFromUtf8("v1"))
                        .build(),
                )
                host.onDraftStorageWrite(
                    DraftStorageWriteEffect.newBuilder()
                        .setOpId(2L)
                        .setBucket(DraftStorageBucket.DRAFT_STORAGE_BUCKET_DRAFT)
                        .setKey("session:fifo")
                        .setData(ByteString.copyFromUtf8("v2"))
                        .build(),
                )
                exec.awaitStorageIdle()
                val afterWrites = exec.executeRead(
                    DraftStorageReadEffect.newBuilder()
                        .setOpId(3L)
                        .setBucket(DraftStorageBucket.DRAFT_STORAGE_BUCKET_DRAFT)
                        .setKey("session:fifo")
                        .build(),
                )
                assertTrue(afterWrites.found)
                assertEquals("v2", afterWrites.data.toStringUtf8())

                origRel(AttachmentReleaseLocalEffect.newBuilder().addRefs(blobRef).build())
                origDel(
                    DraftStorageDeleteEffect.newBuilder()
                        .setBucket(DraftStorageBucket.DRAFT_STORAGE_BUCKET_DRAFT)
                        .setKey("session:fifo")
                        .build(),
                )
                exec.awaitStorageIdle()
                assertNull(exec.readEncryptedBlobFull(blobRef))
                val afterDelete = exec.executeRead(
                    DraftStorageReadEffect.newBuilder()
                        .setOpId(4L)
                        .setBucket(DraftStorageBucket.DRAFT_STORAGE_BUCKET_DRAFT)
                        .setKey("session:fifo")
                        .build(),
                )
                assertFalse(afterDelete.found)
            }
        } finally {
            exec.close()
            host.close()
        }
    }

    @Test
    fun realCoreHostSmokePersistsDraftTextAndPreparesPickedImageAcrossRestart() = withTempDir { root ->
        val keyProvider = InMemoryAttachmentKeyProvider()
        val prepareSeen = CountDownLatch(1)
        val exec1 = AttachmentIoExecutor(
            rootDir = root,
            keyProvider = keyProvider,
            platformIo = FakePlatformIo(
                onPrepare = { eff, storeBlob ->
                    // Verify Core supplies non-zero compression parameters (shell never hardcodes them).
                    assertTrue(eff.maxDimensionPx > 0)
                    assertTrue(eff.maxCompressedBytes > 0L)
                    assertTrue(eff.maxOriginalBytes > 0L)
                    val bytes = ByteArray(512) { 0x5A }
                    val prep = storeBlob(".prep", bytes)
                    val thumb = storeBlob(".thumb", byteArrayOf(9, 8, 7))
                    prepareSeen.countDown()
                    PreparedImageOutcome(
                        ok = true,
                        preparedRef = prep,
                        thumbRef = thumb,
                        format = ImageFormat.IMAGE_FORMAT_JPEG,
                        width = 320,
                        height = 240,
                        sizeBytes = bytes.size.toLong(),
                        sha256 = sha256(bytes),
                    )
                },
            ),
        )
        val host1 = CoreHost()
        val scope = sessionDraftScope("s-smoke")
        try {
            val machineMeta = MachineMeta.newBuilder()
                .setHostname("dev-mac")
                .setProtocolVersion(PEER_PROTOCOL_VERSION)
                .setCapabilities(
                    MachineCapabilities.newBuilder()
                        .setImageUpload(true)
                        .setFileUpload(true),
                )
                .build()
            host1.onCacheRead = { cr ->
                if (cr.kindCase == CacheReadEffect.KindCase.BOOTSTRAP) {
                    host1.dispatch(
                        CoreEvent.newBuilder()
                            .setCacheBootstrapLoaded(CacheBootstrapLoadedEvent.newBuilder().setOpId(cr.bootstrap.opId))
                            .build(),
                    )
                }
            }
            host1.onOpenStream = { st ->
                host1.dispatch(
                    CoreEvent.newBuilder()
                        .setStreamReady(
                            StreamReadyEvent.newBuilder()
                                .setLegId(st.legId)
                                .setServerTimeMs(System.currentTimeMillis()),
                        )
                        .build(),
                )
            }
            exec1.attach(host1)
            runBlocking {
                // Config MUST precede any Draft* event.
                host1.sendAttachmentConfig(
                    persistDrafts = true,
                    persistOutbox = true,
                    scanStorage = true,
                    maxChunkBytes = 0,
                    hasPersistedPreparedImages = true,
                )
                host1.dispatchAndWait(
                    CoreEvent.newBuilder()
                        .setSeedDemoWorld(
                            SeedDemoWorldEvent.newBuilder()
                                .addMachines(
                                    io.github.lewin671.remoteagent.core.proto.DemoMachine.newBuilder()
                                        .setMachineId("m1")
                                        .setOnline(true)
                                        .setRawMeta(machineMeta.toByteString()),
                                )
                                .setReplaceMachines(true)
                                .setClearDirectReports(true)
                                .setSecurity(
                                    DemoSecurity.newBuilder()
                                        .setIsMember(true)
                                        .setSelfDeviceId("c1")
                                        .setAuthorityDeviceId("c1")
                                        .addTrustedMachineIds("m1")
                                        .setHeadEpoch(1L),
                                ),
                        )
                        .build(),
                )
                host1.syncLifecycle(SyncLifecycleKind.SYNC_LIFECYCLE_KIND_START)
                // Wait for machine m1 to be online and connected.
                withTimeout(5_000L) {
                    while (host1.snapshotSettingsView().machinesList.none { it.machineId == "m1" && it.online }) {
                        kotlinx.coroutines.delay(10L)
                    }
                }
                exec1.awaitStorageIdle()
                host1.openDraft(scope, "m1")
                // Wait for DraftOpen's DraftStorageRead to settle before adding items.
                withTimeout(5_000L) {
                    while (true) {
                        exec1.awaitStorageIdle()
                        host1.dispatchAndWait(CoreEvent.newBuilder().build())
                        if (!host1.snapshotAttachmentDraft(scope).restoring) break
                        kotlinx.coroutines.delay(10L)
                    }
                }
                host1.setDraftText(scope, "hello draft")
                host1.addDraftItems(
                    scope,
                    listOf(
                        DraftPickedItem.newBuilder()
                            .setCandidate(
                                AttachmentCandidateInput.newBuilder()
                                    .setName("photo.jpg")
                                    .setMimeType("image/jpeg")
                                    .setSizeBytes(1024L)
                                    .setForceFile(false),
                            )
                            .setSourceRef("content://photos/1")
                            .setHasOriginalSource(true)
                            .build(),
                    ),
                )
                assertTrue("expected AttachmentPrepareImageEffect", prepareSeen.await(5, TimeUnit.SECONDS))
                // Wait for prepare result + coalesced draft write to settle on disk.
                withTimeout(5_000L) {
                    while (true) {
                        exec1.awaitStorageIdle()
                        val view = host1.snapshotAttachmentDraft(scope)
                        if (view.text == "hello draft" && view.imagesCount == 1 && view.imagesList[0].preparedRef.isNotEmpty()) {
                            break
                        }
                        kotlinx.coroutines.delay(20L)
                    }
                }
                exec1.awaitStorageIdle()
            }
        } finally {
            host1.close()
            exec1.close()
        }

        // Simulate cold restart with a brand-new CoreHost + AttachmentIoExecutor over the same encrypted rootDir.
        val exec2 = AttachmentIoExecutor(
            rootDir = root,
            keyProvider = keyProvider,
            platformIo = FakePlatformIo(),
        )
        val host2 = CoreHost()
        try {
            exec2.attach(host2)
            runBlocking {
                host2.sendAttachmentConfig(
                    persistDrafts = true,
                    persistOutbox = true,
                    scanStorage = true,
                    maxChunkBytes = 0,
                    hasPersistedPreparedImages = true,
                )
                withTimeout(5_000L) {
                    while (true) {
                        exec2.awaitStorageIdle()
                        val view = host2.snapshotAttachmentDraft(scope)
                        if (view.text == "hello draft" && view.imagesCount == 1) {
                            assertTrue(view.imagesList[0].preparedRef.startsWith(LOCAL_BLOB_REF_PREFIX))
                            assertTrue(view.imagesList[0].thumbRef.startsWith(LOCAL_BLOB_REF_PREFIX))
                            assertNotNull(exec2.readEncryptedBlobFull(view.imagesList[0].preparedRef))
                            break
                        }
                        kotlinx.coroutines.delay(20L)
                    }
                }
            }
        } finally {
            host2.close()
            exec2.close()
        }
    }

    @Test
    fun executeListDeletesResidualTmpFilesAcrossDraftsOutboxAndBlobs() = withTempDir { root ->
        val keyProvider = InMemoryAttachmentKeyProvider()
        val exec = AttachmentIoExecutor(rootDir = root, keyProvider = keyProvider, platformIo = FakePlatformIo())
        try {
            val draftTmp = File(File(root, "drafts").apply { mkdirs() }, "stale.rec.123.tmp").apply { writeBytes(ByteArray(1024)) }
            val outboxTmp = File(File(root, "outbox").apply { mkdirs() }, "stale.rec.456.tmp").apply { writeBytes(ByteArray(1024)) }
            val blobTmp = File(File(root, "blobs").apply { mkdirs() }, "orphan.prep.789.tmp").apply { writeBytes(ByteArray(4096)) }

            val validRef = exec.writeEncryptedBlob(".prep", byteArrayOf(1, 2, 3, 4))
            val beforeBytes = exec.usedBytes()
            val listed = exec.executeList(DraftStorageListEffect.newBuilder().setOpId(1L).build())

            assertFalse("drafts .tmp must be deleted on scan", draftTmp.exists())
            assertFalse("outbox .tmp must be deleted on scan", outboxTmp.exists())
            assertFalse("blobs .tmp must be deleted on scan", blobTmp.exists())
            assertEquals(listOf(validRef), listed.localRefsList)
            assertTrue("quota bytes must decrease after .tmp cleanup", exec.usedBytes() < beforeBytes)
        } finally {
            exec.close()
        }
    }

    @Test
    fun keyUnavailableDuringListReturnsErrorWithoutDeletingDraftsOrOutbox() = withTempDir { root ->
        val inner = InMemoryAttachmentKeyProvider()
        var failKey = false
        val flakyProvider = object : AttachmentStorageKeyProvider {
            override fun loadExistingKey(): ByteArray? {
                if (failKey) throw AttachmentKeyUnavailableException("keystore_locked")
                return inner.loadExistingKey()
            }
            override fun ensureKey(): ByteArray {
                if (failKey) throw AttachmentKeyUnavailableException("keystore_locked")
                return inner.ensureKey()
            }
            override fun clearKey() = inner.clearKey()
        }
        val exec = AttachmentIoExecutor(rootDir = root, keyProvider = flakyProvider, platformIo = FakePlatformIo())
        try {
            exec.executeWrite(
                DraftStorageWriteEffect.newBuilder()
                    .setOpId(1L)
                    .setBucket(DraftStorageBucket.DRAFT_STORAGE_BUCKET_DRAFT)
                    .setKey("session:s-keep")
                    .setData(ByteString.copyFrom(byteArrayOf(7, 8, 9)))
                    .build(),
            )
            val blobRef = exec.writeEncryptedBlob(".prep", byteArrayOf(1, 2, 3, 4))

            // Simulate transient Keystore failure on list + read + write -> throws / returns non-CORRUPT error and preserves disk.
            failKey = true
            val listErr = runCatching { exec.executeList(DraftStorageListEffect.newBuilder().setOpId(2L).build()) }.exceptionOrNull()
            assertTrue("expected AttachmentKeyUnavailableException", listErr is AttachmentKeyUnavailableException)
            val blobRead = exec.readBlobRange(blobRef, 0L, 4, 4L)
            assertFalse(blobRead.ok)
            assertEquals(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_OTHER, blobRead.errorKind)
            assertNull(exec.readEncryptedBlobFull(blobRef))

            // Next startup after Keystore recovers -> all drafts & blobs decrypt intact!
            failKey = false
            val recovered = exec.executeList(DraftStorageListEffect.newBuilder().setOpId(3L).build())
            assertEquals(AttachmentIOErrorKind.ATTACHMENT_IO_ERROR_KIND_UNSPECIFIED, recovered.errorKind)
            assertEquals(1, recovered.entriesCount)
            assertFalse(recovered.entriesList[0].corrupt)
            assertEquals("session:s-keep", recovered.entriesList[0].key)
            assertArrayEquals(byteArrayOf(7, 8, 9), recovered.entriesList[0].data.toByteArray())
            assertArrayEquals(byteArrayOf(1, 2, 3, 4), exec.readEncryptedBlobFull(blobRef))
        } finally {
            exec.close()
        }
    }
}
