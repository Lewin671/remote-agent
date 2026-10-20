package io.github.lewin671.remoteagent.data

import androidx.test.platform.app.InstrumentationRegistry
import io.github.lewin671.remoteagent.core.*
import java.io.File
import java.security.KeyStore
import javax.crypto.SecretKey
import org.junit.After
import org.junit.Assert.*
import org.junit.Assume.assumeTrue
import org.junit.Before
import org.junit.Test

/** Real encryption tests, restricted to an isolated app with no daily account. */
class VaultLoginTokenTest {
    private val context = InstrumentationRegistry.getInstrumentation().targetContext
    private val vault = Vault(context)
    private val account = Account("https://example.com", ByteArray(32) { 1 }, ByteArray(32) { 2 }, "test-phone")
    private val file = File(context.noBackupFilesDir, "login-token.bin")
    private val token = LoginToken("synthetic-bearer-secret", 1_900_000_000_000)

    @Before fun setup() {
        assumeTrue(context.packageName.endsWith(".securityaudit"))
        vault.clear()
        vault.save(account, ByteArray(32) { 3 })
    }
    @After fun cleanup() {
        if (context.packageName.endsWith(".securityaudit")) vault.clear()
    }

    @Test fun survivesReopenUnderTheAccountUnlockedOnlyKeyWithoutPlaintext() {
        assertNull(vault.loadLoginToken(account))
        vault.saveLoginToken(account, token)
        val loaded = Vault(context).loadLoginToken(account)!!
        assertEquals(token.value, loaded.value)
        assertEquals(token.expiresMs, loaded.expiresMs)
        assertFalse(String(file.readBytes(), Charsets.ISO_8859_1).contains(token.value))
        val key = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
            .getKey("ra-account-unlocked-v2", null) as SecretKey
        assertNull(key.encoded)

    }

    @Test fun tamperingIsDiscardedAndAnotherIdentityCannotLoadOrOverwrite() {
        vault.saveLoginToken(account, token)
        val other = Account(account.server, account.root, ByteArray(32) { 9 }, account.deviceId)
        assertNull(vault.loadLoginToken(other))
        vault.saveLoginToken(other, LoginToken("wrong", token.expiresMs))
        vault.clearLoginToken(other)
        assertEquals(token.value, vault.loadLoginToken(account)!!.value)
        file.writeBytes(file.readBytes().apply { this[lastIndex] = (last().toInt() xor 1).toByte() })
        assertNull(vault.loadLoginToken(account))
        assertFalse(file.exists())
    }

    @Test fun logoutResetDiscardAndLateLoginCompletionCannotResurrectCredentials() {
        vault.saveLoginToken(account, token)
        vault.clearLoginToken(account)
        assertFalse(file.exists())
        vault.saveLoginToken(account, token)
        vault.clear(preserveSecurityOperation = true)
        assertFalse(file.exists())
        vault.saveLoginToken(account, token)
        assertFalse(file.exists())
        vault.save(account, ByteArray(32) { 3 })
        vault.saveLoginToken(account, token)
        vault.save(account, ByteArray(32) { 4 })
        assertNull(vault.loadLoginToken(account))
        vault.saveLoginToken(account, token)
        vault.clear()
        assertFalse(file.exists())
    }
}
