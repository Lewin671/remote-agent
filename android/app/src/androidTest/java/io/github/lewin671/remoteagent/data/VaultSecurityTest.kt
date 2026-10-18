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
        val state =
            security.parseState(
                java.util.Base64.getDecoder()
                    .decode(
                        "AAABtAAAAAIAAAAgMDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWYREREREREREREREREREREREREREREREREREREREREREQAAACA0OGNjYTk3Zjg5OTNmZmFlYmNhYzk3MjhkN2Y5NGY3MduZX+JRadFByrm7upK6oB+fLh7OffTLKsBRkPN/zB+dEyxEK+AQ+9V+cmAzKKp25x/MwVA6riGTJ9FNnJmT9HKqkl9YtkzHiMuG0FTcFeLsH6OPWyeLGMjiIt4GObCowgAAAAAAAAACAAAAAAAAAAIAAAACAAAAIDI0YWEyYTU1ODllZGNiNTdmZWEwYmUzNTUyZjA2NWRlSAdaWX5yGhVuLgeZ3lzAxTJNxufq8c3UYlCGjsUyFd1Z2SJUc0Ue//5rNtvK79v3sYld5iCEUJp/W1i/AdBkGAAAAAIAAAAgNDhjY2E5N2Y4OTkzZmZhZWJjYWM5NzI4ZDdmOTRmNzGIS4hX9OqhYTxhUE2zTUvq80ZReg4x3jzd1Nm0IB2dCw+qaE7SiGe5f0pqLe5d+M6XTna3AY4/IqHEzyZ4Vw8gAAAAAQAAAAAAAAACAAAAIDI0YWEyYTU1ODllZGNiNTdmZWEwYmUzNTUyZjA2NWRlAAAAIBrcHM12/2sawLI/GOJC5dG2X5aUrJlzeBGrR20R8SAgAAAAMMYznI83qZkHBoN/97w0ZUkeFxrbQ1cJ5SPV1BNBbzfmqLdrN3rRRqZ8dwCZxz8XDgAAACA0OGNjYTk3Zjg5OTNmZmFlYmNhYzk3MjhkN2Y5NGY3MQAAACAcPttHOpm95eDd52A0QRc+Zr01E+MRKmHNLzmJAIRafwAAADCKEDzlhgIBD8aXwIN+K5Nw1qJSdn53sHMvAW0lgFnLXh7YjGTbNnoRdO9gVXtJ6R8AAABAK+MTq6jvnrE7nWP32ALKDtayJL9LP0gth/oOwtLIhu7BAIJZqZtbBj8j8B0h0eDJspThfW0XlzYGvAtQ8bxMCw==",
                    ),
            )
        val c = state.core
        val pin =
            io.github.lewin671.remoteagent.core.DeviceSecurity.Pin(
                c.account,
                c.domain,
                java.util.Base64.getDecoder()
                    .decode("qpJfWLZMx4jLhtBU3BXi7B+jj1snixjI4iLeBjmwqMI="),
                c.authorityID,
                c.authorityEd,
                c.authorityX,
            )
        return io.github.lewin671.remoteagent.core.DeviceSecurity.Record(
            pin,
            state,
            "48cca97f8993ffaebcac9728d7f94f71",
            ByteArray(32) { 0x22 },
        )
    }

    @Test
    fun counterAndNotificationChildSurviveAReopen() {
        val security = io.github.lewin671.remoteagent.core.DeviceSecurity
        val record = securityRecord()
        vault.initializeSecurity(record)
        val hash = security.stateHash(record.state)
        assertEquals(1uL, vault.reserveSecurity(hash).counter)
        val reopened = Vault(context)
        assertEquals(1uL, reopened.readSecurity().counter)
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
        val stored = File(context.noBackupFilesDir, "security-v2.bin")
        stored.writeBytes(byteArrayOf(0))
        assertTrue(runCatching { vault.readSecurity() }.isFailure)
        assertTrue(runCatching { vault.initializeSecurity(record) }.isFailure)
        assertArrayEquals(byteArrayOf(0), stored.readBytes())
    }
}
