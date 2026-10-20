package io.github.lewin671.remoteagent.data

import androidx.test.platform.app.InstrumentationRegistry
import io.github.lewin671.remoteagent.core.*
import java.io.File
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.SecretKey
import org.junit.After
import org.junit.Assert.*
import org.junit.Assume.assumeTrue
import org.junit.Before
import org.junit.Test

class VaultCacheTest {
    private val context = InstrumentationRegistry.getInstrumentation().targetContext
    private var decrypts = mutableMapOf<String, Int>()
    private var parses = 0
    private val vault = Vault(context, { decrypts[it] = (decrypts[it] ?: 0) + 1 }, { parses++ })
    private val file = File(context.noBackupFilesDir, "security-v3.bin")
    private val notify = File(context.noBackupFilesDir, "notify.bin")
    private val signer = SigningKey(ByteArray(32) { 1 })
    private val phoneX = ByteArray(32) { 2 }
    private val phone = Authority.newMember(signer, phoneX, 2u)
    private val machineSigner = SigningKey(ByteArray(32) { 3 })
    private val machineX = ByteArray(32) { 4 }
    private val machine = Authority.newMember(machineSigner, machineX, 1u)
    private val secret = ByteArray(32) { 5 }
    private val genesis = Authority.genesis(
        "a".repeat(32), ByteArray(32) { 6 }, phone,
        SigningKey(ByteArray(32) { 7 }).publicKey, secret, signer, 1_700_000_000_000,
    )
    private val paired = Authority.pair(genesis, machine, secret, secret, signer, 1_700_000_000_001)
    private fun record() = DeviceSecurity.Record.fromChain(
        genesis.core.account, genesis.core.domain, DeviceSecurity.stateHash(genesis), machine.id,
        machineX, listOf(genesis, paired).map { DeviceSecurity.stateBytes(it) }, listOf(null, null),
    )

    @Before fun setup() {
        assumeTrue(context.packageName.endsWith(".securityaudit"))
        vault.clear()
        vault.initializeSecurity(record())
        decrypts.clear()
        parses = 0
    }

    @After fun cleanup() {
        if (context.packageName.endsWith(".securityaudit")) vault.clear()
    }

    @Test fun unchangedCiphertextsAreDecryptedAndVerifiedOnce() {
        val first = vault.readSecurity()
        repeat(20) { assertSame(first, vault.readSecurity()) }
        assertEquals(1, parses)
        assertEquals(mapOf("security-v3.bin" to 1, "notify.bin" to 1), decrypts)
        vault.clearSecurityCache()
        assertNotSame(first, vault.readSecurity())
        assertEquals(2, parses)
    }

    @Test fun anotherVaultsWriteAndAnExactRollbackBothRequireVerification() {
        val first = vault.readSecurity()
        val original = file.readBytes()
        Vault(context).touchSecurity(DeviceSecurity.stateHash(first.head), 9000uL)
        assertEquals(9000uL, vault.readSecurity().floor)
        assertEquals(2, parses)
        // Same bytes as a previously verified file must not resurrect its evicted Record.
        file.writeBytes(original)
        assertNotSame(first, vault.readSecurity())
        assertEquals(3, parses)
        assertEquals(3, decrypts["security-v3.bin"])
    }

    @Test fun tamperedCiphertextCannotUseOrLeaveAStaleCache() {
        val first = vault.readSecurity()
        val original = file.readBytes()
        val broken = original.copyOf().apply { this[lastIndex] = (last().toInt() xor 1).toByte() }
        file.writeBytes(broken)
        repeat(2) { assertTrue(runCatching { vault.readSecurity() }.isFailure) }
        file.writeBytes(original)
        assertNotSame(first, vault.readSecurity())
        assertEquals(2, parses)
        assertEquals(4, decrypts["security-v3.bin"])
    }

    @Test fun anAuthenticatedFileWithAnInvalidSignedStateIsStillRejected() {
        val first = vault.readSecurity()
        val original = file.readBytes()
        val raw = first.bytes()
        val state = DeviceSecurity.stateBytes(first.head)
        val start = raw.indices.first { i -> i + state.size <= raw.size && raw.copyOfRange(i, i + state.size).contentEquals(state) }
        raw[start + state.lastIndex] = (raw[start + state.lastIndex].toInt() xor 1).toByte()
        // Wrap with this isolated app's real key, so rejection must come from chain verification.
        val key = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
            .getKey("ra-account-unlocked-v2", null) as SecretKey
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, key)
        file.writeBytes(cipher.iv + cipher.doFinal(raw))
        repeat(2) { assertTrue(runCatching { vault.readSecurity() }.isFailure) }
        file.writeBytes(original)
        assertNotSame(first, vault.readSecurity())
        assertEquals(4, parses)
    }

    @Test fun notifyRewriteAndDeletionAreNotHiddenByTheRecordCache() {
        val first = vault.readSecurity()
        val account = Account("https://example.com", ByteArray(32) { 8 }, ByteArray(32) { 9 }, phone.id)
        Vault(context).save(account, ByteArray(32) { 10 })
        assertSame(first, vault.readSecurity())
        assertArrayEquals(DeviceSecurity.stateHash(first.head), DeviceSecurity.stateHash(DeviceSecurity.NotifyRecord.parse(vault.notifyKey()!!).state))
        notify.delete()
        assertSame(first, vault.readSecurity())
        assertTrue(notify.exists())
        assertEquals(1, parses)
    }

    @Test fun removalDropsNotifyAndDoesNotRetainTheOldRecord() {
        val first = vault.readSecurity()
        val original = file.readBytes()
        val removal = Authority.remove(paired, listOf(machine.id), secret, ByteArray(32) { 11 }, signer, 1_700_000_000_002)
        Vault(context).installSecurity(DeviceSecurity.stateHash(first.head), listOf(removal), emptyList())
        assertFalse(vault.readSecurity().member)
        assertNull(vault.notifyKey())
        val removed = vault.readSecurity()
        assertNotSame(removed, vault.readSecurity())
        file.writeBytes(original)
        assertNotSame(first, vault.readSecurity())
        assertTrue(notify.exists())
    }

    @Test fun resetAndMissingFilesCannotUseTheCache() {
        val first = vault.readSecurity()
        val original = file.readBytes()
        file.delete()
        assertTrue(runCatching { vault.readSecurity() }.isFailure)
        file.writeBytes(original)
        assertNotSame(first, vault.readSecurity())
        vault.clear()
        assertTrue(runCatching { vault.readSecurity() }.isFailure)
        vault.initializeSecurity(record())
        assertNotSame(first, vault.readSecurity())
        assertEquals(3, parses)
    }
}
