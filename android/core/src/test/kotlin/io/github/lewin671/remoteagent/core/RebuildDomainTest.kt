package io.github.lewin671.remoteagent.core

import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test

class RebuildDomainTest {
    @Test
    fun rebuildsOfflineAndKeepsTheRootAndDeviceIdentity() = runBlocking<Unit> {
        val root = randomBytes(32)
        val deviceSeed = randomBytes(32)
        val keys = Keys.derive(root)
        val account =
            Account(
                "http://127.0.0.1:1",
                root,
                deviceSeed,
                deviceId(SigningKey(deviceSeed).publicKey),
                deviceId(keys.account.publicKey),
                keys.account.publicKey,
            )
        val created = rebuildSecurityDomain(account.server, Api.defaultHttp(), account)
        // The relay is unreachable (the upgrade stops it for the offline pin):
        // the domain is built locally and the app says to pin offline.
        assertTrue(created.needsOfflinePin)
        assertEquals(account.deviceId, created.record.memberID)
        assertEquals(account.accountId, created.record.head.core.account)
        assertEquals(1, created.record.chain.size)
        assertEquals(created.record.head.core.authorityID, created.record.memberID)
        val decoded = RecoveryCode.decode(created.recoveryCode)
        assertNotNull(decoded)
        assertEquals(
            DeviceSecurity.pinOf(created.record.head).genesis.copyOf(10).hex(),
            decoded!!.genesis.hex(),
        )
    }
}
