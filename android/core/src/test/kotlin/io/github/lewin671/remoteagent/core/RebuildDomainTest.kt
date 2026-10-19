package io.github.lewin671.remoteagent.core

import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test

class RebuildDomainTest {
    @Test
    fun rebuildPersistsBeforeNetworkAndResumesTheExactExportableGenesis() =
        runBlocking<Unit> {
            val root = randomBytes(32)
            val seed = randomBytes(32)
            val account =
                Account("http://127.0.0.1:1", root, seed, deviceId(SigningKey(seed).publicKey))
            var saved: CreatedAccount? = null
            val http =
                Api.defaultHttp()
                    .newBuilder()
                    .addInterceptor { chain ->
                        assertNotNull(
                            "rebuild touched network before durable keys and signed genesis",
                            saved,
                        )
                        chain.proceed(chain.request())
                    }
                    .build()
            val first =
                rebuildSecurityDomain(
                    account.server,
                    http,
                    account,
                    persistDraft = {
                        saved = CreatedAccount.readOperationMaterial(it.operationMaterial())
                    },
                )
            val restored = saved!!
            val resumed =
                rebuildSecurityDomain(
                    account.server,
                    http,
                    account,
                    draft = restored,
                    persistDraft = {
                        saved = CreatedAccount.readOperationMaterial(it.operationMaterial())
                    },
                )
            org.junit.Assert.assertArrayEquals(
                DeviceSecurity.stateBytes(first.record.chain.first()),
                DeviceSecurity.stateBytes(resumed.record.chain.first()),
            )
            org.junit.Assert.assertArrayEquals(first.record.bytes(), resumed.record.bytes())
            org.junit.Assert.assertArrayEquals(
                first.record.contentKey(first.record.head.core.contentEpoch),
                resumed.record.contentKey(resumed.record.head.core.contentEpoch),
            )
        }

    @Test
    fun rebuildsOfflineAndKeepsTheRootAndDeviceIdentity() =
        runBlocking<Unit> {
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
            val created =
                rebuildSecurityDomain(account.server, Api.defaultHttp(), account, persistDraft = {})
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
