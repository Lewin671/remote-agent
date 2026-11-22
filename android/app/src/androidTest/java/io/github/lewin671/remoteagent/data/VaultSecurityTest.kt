package io.github.lewin671.remoteagent.data

import android.content.Context
import androidx.test.platform.app.InstrumentationRegistry
import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.LoginToken
import io.github.lewin671.remoteagent.core.proto.SecurityEncapsulateRecipient
import io.github.lewin671.remoteagent.core.proto.SecurityKeyKind
import io.github.lewin671.remoteagent.core.proto.SecurityStorageEffect
import io.github.lewin671.remoteagent.core.proto.SecurityStorageOp
import io.github.lewin671.remoteagent.core.proto.SecurityStorageWrite
import io.github.lewin671.remoteagent.core.proto.SecurityStoreBucket
import io.github.lewin671.remoteagent.corebind.Corebind
import java.io.File
import java.security.KeyStore
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import org.junit.After
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeTrue
import org.junit.Before
import org.junit.Test

/** Runs only in a separate test application; exercises real Android Keystore v4 Vault. */
class VaultSecurityTest {
    private val context = InstrumentationRegistry.getInstrumentation().targetContext
    private val vault = Vault(context)
    private val bucketsFile = File(context.noBackupFilesDir, "security-v4-buckets.bin")

    @Before
    fun setup() {
        assumeTrue(
            "Vault tests require the isolated audit application",
            context.packageName.endsWith(".securityaudit"),
        )
        vault.clear()
        File(context.noBackupFilesDir, "account-v2.bin").delete()
        File(context.noBackupFilesDir, "security-v3.bin").delete()
    }

    @After
    fun cleanup() {
        if (context.packageName.endsWith(".securityaudit")) {
            vault.clear()
            File(context.noBackupFilesDir, "account-v2.bin").delete()
            File(context.noBackupFilesDir, "security-v3.bin").delete()
        }
    }

    @Test
    fun casBatchCommitKeyPromotionAndReopenWorkWithRealKeystore() {
        val candEd = vault.generateKey(SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_ED, "slot-a", persist = true)
        val candX = vault.generateKey(SecurityKeyKind.SECURITY_KEY_KIND_CANDIDATE_X, "slot-a", persist = true)
        vault.saveLoginToken(BenchmarkVectors.SERVER, "dev-1", LoginToken("tok-1", 10_000L))

        val firstOp = vault.executeStorage(
            SecurityStorageEffect.newBuilder()
                .setOpId(1L)
                .setOp(SecurityStorageOp.SECURITY_STORAGE_OP_WRITE_CAS)
                .setBucket(SecurityStoreBucket.SECURITY_STORE_BUCKET_OPERATION)
                .setKey("pending")
                .setCreateOnly(true)
                .setNextRevision("r1")
                .setData(ByteString.copyFromUtf8("op-1"))
                .build(),
        )
        assertFalse(firstOp.conflict)
        assertEquals("r1", firstOp.revision)

        val conflictOp = vault.executeStorage(
            SecurityStorageEffect.newBuilder()
                .setOpId(2L)
                .setOp(SecurityStorageOp.SECURITY_STORAGE_OP_WRITE_CAS)
                .setBucket(SecurityStoreBucket.SECURITY_STORE_BUCKET_OPERATION)
                .setKey("pending")
                .setExpectedRevision("stale")
                .setNextRevision("r2")
                .setData(ByteString.copyFromUtf8("op-2"))
                .build(),
        )
        assertTrue(conflictOp.conflict)

        val batch = vault.executeStorage(
            SecurityStorageEffect.newBuilder()
                .setOpId(3L)
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
                        .setData(ByteString.copyFrom(BenchmarkVectors.phoneRecordBytes)),
                )
                .addBatch(
                    SecurityStorageWrite.newBuilder()
                        .setBucket(SecurityStoreBucket.SECURITY_STORE_BUCKET_NOTIFY_RECORD)
                        .setKey("notify")
                        .setCreateOnly(true)
                        .setNextRevision("r-ntf")
                        .setData(ByteString.copyFrom(BenchmarkVectors.phoneNotifyBytes)),
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
        assertFalse(batch.conflict)
        assertEquals("", batch.error)
        assertTrue(batch.hasDeviceEd)
        assertTrue(batch.hasDeviceX)
        assertArrayEquals(candEd, batch.deviceEdPub.toByteArray())
        assertArrayEquals(candX, batch.deviceXPub.toByteArray())
        assertNull(vault.loadLoginToken(BenchmarkVectors.SERVER, "dev-1"))

        val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        assertTrue(ks.containsAlias("ra-account-unlocked-v2"))
        assertTrue(ks.containsAlias("ra-notify"))

        val reopened = Vault(context)
        assertTrue(reopened.hasAccount())
        val boot = reopened.executeStorage(
            SecurityStorageEffect.newBuilder()
                .setOpId(4L)
                .setOp(SecurityStorageOp.SECURITY_STORAGE_OP_READ_BOOTSTRAP)
                .build(),
        )
        assertFalse(boot.legacyCleared)
        assertTrue(boot.hasDeviceEd)
        assertTrue(boot.hasDeviceX)
        assertArrayEquals(candEd, boot.deviceEdPub.toByteArray())
        assertArrayEquals(candX, boot.deviceXPub.toByteArray())
        assertArrayEquals(BenchmarkVectors.phoneNotifyBytes, reopened.readNotifyBlob())

        val sig = reopened.signKey(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_ED, "", "verify-me".toByteArray())
        assertTrue(Corebind.ed25519Verify(candEd, "verify-me".toByteArray(), sig))

        val encap = reopened.encapsulate(
            listOf(
                SecurityEncapsulateRecipient.newBuilder()
                    .setMemberId("self")
                    .setXPublic(ByteString.copyFrom(candX))
                    .build(),
            ),
        )
        assertEquals(1, encap.size)
        val agreed = reopened.agreeKey(SecurityKeyKind.SECURITY_KEY_KIND_DEVICE_X, "", encap[0].enc.toByteArray())
        assertArrayEquals(encap[0].dh.toByteArray(), agreed)
    }

    @Test
    fun roundTripsLoginTokenAcrossReopen() {
        val tok = LoginToken("jwt-test-token", 1_900_000_000_000L)
        vault.saveLoginToken(BenchmarkVectors.SERVER, "dev-token", tok)
        val reopened = Vault(context)
        val loaded = reopened.loadLoginToken(BenchmarkVectors.SERVER, "dev-token")
        assertNotNull(loaded)
        assertEquals(tok.token, loaded!!.token)
        assertEquals(tok.expiresMs, loaded.expiresMs)
        reopened.clearLoginToken(BenchmarkVectors.SERVER, "dev-token")
        assertNull(Vault(context).loadLoginToken(BenchmarkVectors.SERVER, "dev-token"))
    }

    @Test
    fun legacyV3WipePreservesPrefsAndRaisesRepairNoticeUntilDismissed() = runBlocking {
        val prefs = Prefs(context)
        prefs.update { it.copy(theme = ThemeMode.DARK) }
        File(context.noBackupFilesDir, "account-v2.bin").writeBytes(byteArrayOf(1, 2, 3))
        File(context.noBackupFilesDir, "security-v3.bin").writeBytes(byteArrayOf(4, 5, 6))
        var wipes = 0
        val fresh = Vault(context, onLegacyWipe = { wipes++ })
        val boot1 = fresh.executeStorage(
            SecurityStorageEffect.newBuilder()
                .setOpId(1L)
                .setOp(SecurityStorageOp.SECURITY_STORAGE_OP_READ_BOOTSTRAP)
                .build(),
        )
        assertTrue(boot1.legacyCleared)
        assertTrue(fresh.hasUpgradeNotice())
        assertEquals(1, wipes)
        assertFalse(File(context.noBackupFilesDir, "account-v2.bin").exists())
        assertFalse(File(context.noBackupFilesDir, "security-v3.bin").exists())
        assertEquals(ThemeMode.DARK, prefs.settings.first().theme)

        fresh.dismissUpgradeNotice()
        assertFalse(fresh.hasUpgradeNotice())
        val boot2 = Vault(context).executeStorage(
            SecurityStorageEffect.newBuilder()
                .setOpId(2L)
                .setOp(SecurityStorageOp.SECURITY_STORAGE_OP_READ_BOOTSTRAP)
                .build(),
        )
        assertFalse(boot2.legacyCleared)
    }

    @Test
    fun existingCorruptBucketsFailClosedWithoutReinitializing() {
        val res = vault.executeStorage(
            SecurityStorageEffect.newBuilder()
                .setOpId(1L)
                .setOp(SecurityStorageOp.SECURITY_STORAGE_OP_WRITE_CAS)
                .setBucket(SecurityStoreBucket.SECURITY_STORE_BUCKET_RECORD)
                .setKey("record")
                .setCreateOnly(true)
                .setNextRevision("r1")
                .setData(ByteString.copyFrom(BenchmarkVectors.phoneRecordBytes))
                .build(),
        )
        assertEquals("", res.error)
        bucketsFile.writeBytes(byteArrayOf(0))
        val reopened = Vault(context)
        assertTrue(
            runCatching {
                reopened.executeStorage(
                    SecurityStorageEffect.newBuilder()
                        .setOpId(2L)
                        .setOp(SecurityStorageOp.SECURITY_STORAGE_OP_READ_BOOTSTRAP)
                        .build(),
                )
            }.isFailure,
        )
        assertArrayEquals(byteArrayOf(0), bucketsFile.readBytes())
    }
}
