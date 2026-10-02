package io.github.lewin671.remoteagent.data

import androidx.test.platform.app.InstrumentationRegistry
import io.github.lewin671.remoteagent.core.Account
import org.junit.After
import org.junit.Before
import org.junit.Test
import org.junit.Assert.*
import org.junit.Assume.assumeTrue
import java.io.File
import java.security.KeyStore

/** Runs only in a separate test application; exercises the real Keystore. */
class VaultSecurityTest {
    private val context = InstrumentationRegistry.getInstrumentation().targetContext
    private val vault = Vault(context)
    private val file = File(context.noBackupFilesDir, "account-v2.bin")
    private val tmp = File(context.noBackupFilesDir, "account-v2.bin.tmp")
    private val account = Account("https://relay.example", ByteArray(32) { 1 }, ByteArray(32) { 2 }, "device")

    @Before fun setup() {
        assumeTrue("Vault tests require the isolated audit application", context.packageName.endsWith(".securityaudit"))
        vault.clear(); tmp.deleteRecursively()
    }
    @After fun cleanup() {
        if (context.packageName.endsWith(".securityaudit")) { vault.clear(); tmp.deleteRecursively() }
    }

    @Test fun roundTripsAccountAndNotifyKey() {
        assertFalse(vault.hasAccount())
        vault.save(account, ByteArray(32) { 3 })
        assertTrue(file.isFile)
        assertTrue(KeyStore.getInstance("AndroidKeyStore").apply { load(null) }.containsAlias("ra-account-unlocked-v2"))
        val loaded = vault.load()!!
        assertArrayEquals(account.root, loaded.root)
        assertArrayEquals(account.deviceSeed, loaded.deviceSeed)
        assertArrayEquals(ByteArray(32) { 3 }, vault.notifyKey())
        vault.clear()
        assertFalse(vault.hasAccount())
    }
}
