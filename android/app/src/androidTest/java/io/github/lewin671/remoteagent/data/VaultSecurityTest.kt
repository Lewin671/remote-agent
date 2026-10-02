package io.github.lewin671.remoteagent.data

import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import androidx.test.platform.app.InstrumentationRegistry
import io.github.lewin671.remoteagent.core.Account
import org.junit.After
import org.junit.Before
import org.junit.Test
import org.junit.Assert.*
import org.junit.Assume.assumeTrue
import java.io.ByteArrayOutputStream
import java.io.DataOutputStream
import java.io.File
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator

/** Runs only in a separate test application; exercises the real Keystore. */
class VaultSecurityTest {
    private val context = InstrumentationRegistry.getInstrumentation().targetContext
    private val vault = Vault(context)
    private val oldFile = File(context.noBackupFilesDir, "account.bin")
    private val newFile = File(context.noBackupFilesDir, "account-v2.bin")
    private val tmp = File(context.noBackupFilesDir, "account-v2.bin.tmp")
    private val account = Account("https://relay.example", ByteArray(32) { 1 }, ByteArray(32) { 2 }, "device")

    @Before fun setup() {
        assumeTrue("Vault tests require the isolated audit application", context.packageName.endsWith(".securityaudit"))
        vault.clear(); tmp.deleteRecursively()
    }
    @After fun cleanup() {
        if (context.packageName.endsWith(".securityaudit")) { vault.clear(); tmp.deleteRecursively() }
    }

    private fun legacy() {
        val generator = KeyGenerator.getInstance("AES", "AndroidKeyStore")
        generator.init(KeyGenParameterSpec.Builder("ra-account", KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM).setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE).setKeySize(256).build())
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, generator.generateKey())
        val data = ByteArrayOutputStream()
        DataOutputStream(data).use {
            it.writeInt(1); it.writeUTF(account.server); it.writeUTF(account.deviceId)
            it.writeInt(32); it.write(account.root); it.writeInt(32); it.write(account.deviceSeed)
        }
        oldFile.writeBytes(cipher.iv + cipher.doFinal(data.toByteArray()))
    }

    @Test fun upgradesLegacyKeyAndRoundTripsAccount() {
        legacy()
        val loaded = vault.load()!!
        assertArrayEquals(account.root, loaded.root)
        assertTrue(newFile.isFile)
        assertFalse(oldFile.exists())
        val store = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        assertFalse(store.containsAlias("ra-account"))
        assertTrue(store.containsAlias("ra-account-unlocked-v2"))
        assertArrayEquals(account.deviceSeed, vault.load()!!.deviceSeed)
        vault.save(account, ByteArray(32) { 3 })
        assertArrayEquals(ByteArray(32) { 3 }, vault.notifyKey())
    }

    @Test fun failedMigrationRetainsUsableLegacyAccount() {
        legacy()
        assertTrue(tmp.mkdir()) // Force the replacement write to fail.
        try { vault.load(); fail("Expected failed migration") } catch (_: java.io.IOException) { }
        assertTrue(oldFile.isFile)
        assertFalse(newFile.exists())
        val store = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        assertTrue(store.containsAlias("ra-account"))
        tmp.delete()
        assertArrayEquals(account.root, vault.load()!!.root)
    }
}
