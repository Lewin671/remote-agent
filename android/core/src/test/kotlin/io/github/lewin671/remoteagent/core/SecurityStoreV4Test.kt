package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.proto.SecurityBootstrapKind
import io.github.lewin671.remoteagent.core.proto.SecurityEncapsulateRecipient
import io.github.lewin671.remoteagent.core.proto.SecurityKeyKind
import io.github.lewin671.remoteagent.core.proto.SecurityStorageWrite
import io.github.lewin671.remoteagent.core.proto.SecurityStorageEffect
import io.github.lewin671.remoteagent.core.proto.SecurityStorageOp
import io.github.lewin671.remoteagent.core.proto.SecurityStoreBucket
import io.github.lewin671.remoteagent.corebind.Corebind
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class SecurityStoreV4Test {
    @get:org.junit.Rule val hangGuard = hangGuard()

    @Test
    fun casAndBatchCommitAreAtomicAndPromoteCandidateKeys() {
        val store = MemorySecurityStoreV4()
        val edPub = store.generateKey(SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_ED, "slot-a", persist = true)
        val xPub = store.generateKey(SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_X, "slot-a", persist = true)
        store.saveLoginToken("https://example.com", "dev-1", LoginToken("tok-1", 10_000L))

        val firstWrite = store.executeStorage(
            SecurityStorageEffect.newBuilder()
                .setOpId(1L)
                .setOp(SecurityStorageOp.SECURITY_STORAGE_OP_WRITE_CAS)
                .setBucket(SecurityStoreBucket.SECURITY_STORE_BUCKET_OPERATION)
                .setKey("pending")
                .setCreateOnly(true)
                .setNextRevision("r1")
                .setData(ByteString.copyFromUtf8("op-v1"))
                .build(),
        )
        assertFalse(firstWrite.conflict)
        assertEquals("r1", firstWrite.revision)

        // Conflict on createOnly / bad revision leaves existing state untouched.
        val conflictWrite = store.executeStorage(
            SecurityStorageEffect.newBuilder()
                .setOpId(2L)
                .setOp(SecurityStorageOp.SECURITY_STORAGE_OP_WRITE_CAS)
                .setBucket(SecurityStoreBucket.SECURITY_STORE_BUCKET_OPERATION)
                .setKey("pending")
                .setExpectedRevision("stale-rev")
                .setNextRevision("r2")
                .setData(ByteString.copyFromUtf8("op-v2"))
                .build(),
        )
        assertTrue(conflictWrite.conflict)

        val failedBatch = store.executeStorage(
            SecurityStorageEffect.newBuilder()
                .setOpId(3L)
                .setOp(SecurityStorageOp.SECURITY_STORAGE_OP_BATCH_COMMIT)
                .setPromoteEdSlot("slot-a")
                .setPromoteXSlot("slot-a")
                .setClearLoginToken(true)
                .addBatch(
                    SecurityStorageWrite.newBuilder()
                        .setBucket(SecurityStoreBucket.SECURITY_STORE_BUCKET_OPERATION)
                        .setKey("pending")
                        .setRemove(true)
                        .setExpectedRevision("wrong-rev"),
                )
                .build(),
        )
        assertTrue(failedBatch.conflict)
        assertFalse(failedBatch.hasDeviceEd)
        assertNotNull(store.loadLoginToken("https://example.com", "dev-1"))

        val committedBatch = store.executeStorage(
            SecurityStorageEffect.newBuilder()
                .setOpId(4L)
                .setOp(SecurityStorageOp.SECURITY_STORAGE_OP_BATCH_COMMIT)
                .setPromoteEdSlot("slot-a")
                .setPromoteXSlot("slot-a")
                .setClearLoginToken(true)
                .addBatch(
                    SecurityStorageWrite.newBuilder()
                        .setBucket(SecurityStoreBucket.SECURITY_STORE_BUCKET_RECORD)
                        .setKey("record")
                        .setCreateOnly(true)
                        .setNextRevision("r-rec")
                        .setData(ByteString.copyFromUtf8("rec-v1")),
                )
                .addBatch(
                    SecurityStorageWrite.newBuilder()
                        .setBucket(SecurityStoreBucket.SECURITY_STORE_BUCKET_NOTIFY_RECORD)
                        .setKey("notify")
                        .setCreateOnly(true)
                        .setNextRevision("r-ntf")
                        .setData(ByteString.copyFromUtf8("ntf-v1")),
                )
                .addBatch(
                    SecurityStorageWrite.newBuilder()
                        .setBucket(SecurityStoreBucket.SECURITY_STORE_BUCKET_OPERATION)
                        .setKey("pending")
                        .setRemove(true)
                        .setExpectedRevision("r1"),
                )
                .build(),
        )
        assertFalse(committedBatch.conflict)
        assertTrue(committedBatch.hasDeviceEd)
        assertTrue(committedBatch.hasDeviceX)
        assertArrayEquals(edPub, committedBatch.deviceEdPub.toByteArray())
        assertArrayEquals(xPub, committedBatch.deviceXPub.toByteArray())
        assertNull(store.loadLoginToken("https://example.com", "dev-1"))
        assertEquals("ntf-v1", store.readNotifyBlob()!!.decodeToString())

        val sig = store.signKey(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_ED, "", "msg".toByteArray())
        assertTrue(Corebind.ed25519Verify(edPub, "msg".toByteArray(), sig))
    }

    @Test
    fun legacyClearFlagAndEncapsulateRoundTripWorkCorrectly() {
        val store = MemorySecurityStoreV4()
        store.seedLegacyForTest()
        val boot1 = store.executeStorage(
            SecurityStorageEffect.newBuilder()
                .setOpId(1L)
                .setOp(SecurityStorageOp.SECURITY_STORAGE_OP_READ_BOOTSTRAP)
                .build(),
        )
        assertTrue(boot1.legacyCleared)
        assertTrue(store.upgradeNoticeActive())

        store.dismissUpgradeNotice()
        assertFalse(store.upgradeNoticeActive())

        val xPub = store.generateKey(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_X, "", persist = true)
        val pairs = store.encapsulate(
            listOf(
                SecurityEncapsulateRecipient.newBuilder()
                    .setMemberId("m-1")
                    .setXPublic(ByteString.copyFrom(xPub))
                    .build(),
            ),
        )
        assertEquals(1, pairs.size)
        val agree = store.agreeKey(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_X, "", pairs[0].enc.toByteArray())
        assertArrayEquals(pairs[0].dh.toByteArray(), agree)
    }

    @Test
    fun standaloneHostPersistsPendingCreationDraftAndSurvivesRebootstrap() = runBlocking {
        val store = MemorySecurityStoreV4()
        store.seedLegacyForTest()
        val host1 = StandaloneSecurityHost(store, Api.defaultHttp())
        try {
            val st1 = host1.client.bootstrap()
            assertEquals(SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_EMPTY, st1.bootstrap)
            assertTrue(st1.legacyRepairNotice)

            val dismissed = host1.client.dismissUpgradeNotice()
            assertFalse(dismissed.legacyRepairNotice)

            val prep = host1.client.prepareCreation("https://example.com", storagePersisted = true)
            assertTrue(prep.recoveryCode.isNotBlank())
            host1.client.confirmCreationCodeSaved()
        } finally {
            host1.close()
        }

        val host2 = StandaloneSecurityHost(store, Api.defaultHttp())
        try {
            val st2 = host2.client.bootstrap()
            assertEquals(SecurityBootstrapKind.SECURITY_BOOTSTRAP_KIND_PENDING_CREATE, st2.bootstrap)
            assertTrue(st2.pendingOp.codeSaved)
            assertFalse(st2.legacyRepairNotice)
        } finally {
            host2.close()
        }
    }

    @Test
    fun batchCommitFailureInWriteBucketsPreservesCandidateSlotsForRetry() {
        var storedBuckets = emptyMap<SecurityStoreBucketId, SecurityStoreStoredBucket>()
        var storedKeys = emptyMap<SecurityStoreKeyId, SecurityStoreStoredKey>()
        var notifyBlob: ByteArray? = null
        var failNextWriteBuckets = false
        var writeKeysCall = 0
        var failWriteKeysOnCall: Int? = null

        val core = SecurityStoreV4Core(
            object : SecurityStoreV4Backend {
                override fun <T> transaction(block: () -> T): T = block()
                override fun readBuckets() = storedBuckets
                override fun writeBuckets(buckets: Map<SecurityStoreBucketId, SecurityStoreStoredBucket>) {
                    if (failNextWriteBuckets) {
                        failNextWriteBuckets = false
                        throw IllegalStateException("injected writeBuckets failure")
                    }
                    storedBuckets = buckets
                }
                override fun readKeys() = storedKeys
                override fun writeKeys(keys: Map<SecurityStoreKeyId, SecurityStoreStoredKey>) {
                    writeKeysCall++
                    if (writeKeysCall == failWriteKeysOnCall) {
                        failWriteKeysOnCall = null
                        throw IllegalStateException("injected cleanup writeKeys failure")
                    }
                    storedKeys = keys
                }
                override fun readNotifyBlob() = notifyBlob
                override fun writeNotifyBlob(blob: ByteArray) {
                    notifyBlob = blob
                }
                override fun deleteNotifyBlob() {
                    notifyBlob = null
                }
                override fun deleteLoginToken() {}
                override fun clearUpgradeNotice() {}
                override fun detectAndClearLegacy(): Boolean = false
                override fun clearAll() {
                    storedBuckets = emptyMap()
                    storedKeys = emptyMap()
                    notifyBlob = null
                }
            },
        )

        val oldEdPub = core.generateKey(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_ED, "", persist = true)
        val oldXPub = core.generateKey(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_X, "", persist = true)

        val edPub = core.generateKey(SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_ED, "slot-retry", persist = true)
        val xPub = core.generateKey(SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_X, "slot-retry", persist = true)

        val batchEffect = SecurityStorageEffect.newBuilder()
            .setOpId(1L)
            .setOp(SecurityStorageOp.SECURITY_STORAGE_OP_BATCH_COMMIT)
            .setPromoteEdSlot("slot-retry")
            .setPromoteXSlot("slot-retry")
            .addBatch(
                SecurityStorageWrite.newBuilder()
                    .setBucket(SecurityStoreBucket.SECURITY_STORE_BUCKET_RECORD)
                    .setKey("record")
                    .setNextRevision("rec-1")
                    .setData(ByteString.copyFromUtf8("rec-v1")),
            )
            .build()

        failNextWriteBuckets = true
        val failed = core.executeStorage(batchEffect)
        assertTrue(failed.error.isNotEmpty())
        assertTrue(storedBuckets.isEmpty())
        // Promoted DEVICE_ED/DEVICE_X must roll back to the previous keys if writeBuckets throws.
        assertArrayEquals(oldEdPub, storedKeys[SecurityStoreKeyId(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_ED.number, "")]?.publicKey)
        assertArrayEquals(oldXPub, storedKeys[SecurityStoreKeyId(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_X.number, "")]?.publicKey)
        // Candidate key remains intact so retrying the same BATCH_COMMIT promotes and commits cleanly.
        assertEquals(64, core.signKey(SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_ED, "slot-retry", byteArrayOf(1, 2, 3)).size)

        // Fail Phase 3 cleanup writeKeys once during the retry; single retry must still remove candidate slots.
        failWriteKeysOnCall = writeKeysCall + 2
        val retried = core.executeStorage(batchEffect)
        assertEquals("", retried.error)
        assertTrue(retried.hasDeviceEd)
        assertArrayEquals(edPub, retried.deviceEdPub.toByteArray())
        assertArrayEquals(xPub, retried.deviceXPub.toByteArray())
        assertEquals("rec-1", storedBuckets[SecurityStoreBucketId(SecurityStoreBucket.SECURITY_STORE_BUCKET_RECORD.number, "record")]?.revision)
        assertTrue(storedKeys.containsKey(SecurityStoreKeyId(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_ED.number, "")))
        assertFalse(storedKeys.containsKey(SecurityStoreKeyId(SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_ED.number, "slot-retry")))
    }
}
