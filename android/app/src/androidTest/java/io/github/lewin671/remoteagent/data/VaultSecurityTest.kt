package io.github.lewin671.remoteagent.data

import androidx.test.platform.app.InstrumentationRegistry
import io.github.lewin671.remoteagent.core.Account
import java.io.File
import java.security.KeyStore
import org.junit.After
import org.junit.Assert.*
import org.junit.Assume.assumeTrue
import org.junit.Before
import org.junit.Test

/** Runs only in a separate test application; exercises the real Keystore. */
class VaultSecurityTest {
    private val context = InstrumentationRegistry.getInstrumentation().targetContext
    private val vault = Vault(context)
    private val file = File(context.noBackupFilesDir, "account-v2.bin")
    private val tmp = File(context.noBackupFilesDir, "account-v2.bin.tmp")
    private val account =
        Account("https://relay.example", ByteArray(32) { 1 }, ByteArray(32) { 2 }, "device")

    @Before
    fun setup() {
        assumeTrue(
            "Vault tests require the isolated audit application",
            context.packageName.endsWith(".securityaudit"),
        )
        vault.clear()
        tmp.deleteRecursively()
    }

    @After
    fun cleanup() {
        if (context.packageName.endsWith(".securityaudit")) {
            vault.clear()
            tmp.deleteRecursively()
        }
    }

    @Test
    fun roundTripsAccountAndNotifyKey() {
        assertFalse(vault.hasAccount())
        vault.save(account, ByteArray(32) { 3 })
        assertTrue(file.isFile)
        assertTrue(
            KeyStore.getInstance("AndroidKeyStore")
                .apply { load(null) }
                .containsAlias("ra-account-unlocked-v2"),
        )
        val loaded = vault.load()!!
        assertArrayEquals(account.root, loaded.root)
        assertArrayEquals(account.deviceSeed, loaded.deviceSeed)
        assertArrayEquals(ByteArray(32) { 3 }, vault.notifyKey())
        vault.clear()
        assertFalse(vault.hasAccount())
    }

    private fun securityRecord(): io.github.lewin671.remoteagent.core.DeviceSecurity.Record {
        val security = io.github.lewin671.remoteagent.core.DeviceSecurity
        val signer = io.github.lewin671.remoteagent.core.SigningKey(account.deviceSeed)
        val x = ByteArray(32) { 0x22 }
        val phone =
            io.github.lewin671.remoteagent.core.Authority.newMember(signer, x, 2u)
        val genesis =
            io.github.lewin671.remoteagent.core.Authority.genesis(
                account.accountId,
                ByteArray(32) { 0x33 },
                phone,
                ByteArray(32) { 0x44 },
                ByteArray(32) { 0x55 },
                signer,
                1_700_000_000_000,
            )
        return io.github.lewin671.remoteagent.core.DeviceSecurity.Record.fromChain(
            account.accountId,
            genesis.core.domain,
            security.stateHash(genesis),
            phone.id,
            x,
            listOf(security.stateBytes(genesis)),
            listOf(null),
        )
    }

    @Test
    fun counterAndNotificationChildSurviveAReopen() {
        val security = io.github.lewin671.remoteagent.core.DeviceSecurity
        val record = securityRecord()
        vault.initializeSecurity(record)
        val hash = security.stateHash(record.head)
        // A block is persisted once; the counter then moves in memory.
        val block = vault.readSecurity().reservingBlock()
        vault.writeSecurity(hash, block)
        val used = block.incremented().incremented()
        vault.writeSecurity(hash, used)
        val reopened = Vault(context)
        // A restart skips the rest of the block already handed out.
        assertEquals(block.counterBlockEnd, reopened.readSecurity().counter)
        assertEquals(block.counterBlockEnd + 1uL, reopened.readSecurity().incremented().counter)
        // Simulate an old notification-only item, never a root/epoch grant.
        vault.save(account, ByteArray(32) { 3 })
        reopened.readSecurity()
        val notify =
            io.github.lewin671.remoteagent.core.DeviceSecurity.NotifyRecord.parse(
                vault.notifyKey()!!,
            )
        assertArrayEquals(hash, security.stateHash(notify.state))
        assertFalse(notify.bytes().contentEquals(record.bytes()))
    }

    @Test
    fun existingCorruptStateIsNeverReinitialized() {
        val record = securityRecord()
        vault.initializeSecurity(record)
        val stored = File(context.noBackupFilesDir, "security-v3.bin")
        stored.writeBytes(byteArrayOf(0))
        assertTrue(runCatching { vault.readSecurity() }.isFailure)
        assertTrue(runCatching { vault.initializeSecurity(record) }.isFailure)
        assertArrayEquals(byteArrayOf(0), stored.readBytes())
    }
}
