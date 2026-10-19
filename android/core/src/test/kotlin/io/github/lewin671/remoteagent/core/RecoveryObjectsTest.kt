package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.*
import java.io.File
import java.nio.file.Files
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import org.junit.Assert.*
import org.junit.Test

class RecoveryObjectsTest {
    @Test
    fun retainedClientRepublishesAndUnreadableOfflineMetadataKeepsSignedMembers() =
        runBlocking<Unit> {
            val dir = Files.createTempDirectory("ra-recovery-objects").toFile()
            val server = TestServer(dir)
            val http = Api.defaultHttp()
            val seed = ByteArray(32) { 0x31 }
            val remotes = mutableListOf<Remote>()
            try {
                val phone =
                    newAuthorityAccount(randomBytes(32), server.url, http, server.setupToken, seed)
                val kept = approvedClient(phone, http)
                val offline = approvedClient(phone, http)
                val r = fixtureRemote(kept, http).also { remotes += it }
                val o = fixtureRemote(offline, http).also { remotes += it }
                r.start()
                o.start()
                withTimeout(10000) {
                    r.state.first { it.conn == ConnState.ONLINE }
                    o.state.first { it.conn == ConnState.ONLINE }
                }
                r.renameDevice(kept.deviceId, "retained-client")
                o.renameDevice(offline.deviceId, "offline-client")
                r.changePrefs(PrefsOp.Pin("/synthetic/kept"))
                o.close()
                val context = Recovery.context(server.url, http, SigningKey(seed).publicKey, seed)
                val submitted =
                    Recovery.submit(
                        server.url,
                        http,
                        seed,
                        randomBytes(32),
                        context.pin.genesis.copyOf(10),
                        persist = {},
                    )
                val sqlite =
                    ProcessBuilder(
                            "sqlite3",
                            File(dir, "ra.db").absolutePath,
                            "UPDATE security_recovery SET ready_ms=0;",
                        )
                        .start()
                check(sqlite.waitFor() == 0)
                val (acct, record) =
                    Recovery.complete(
                        server.url,
                        http,
                        submitted.recoveryId,
                        submitted.accountId,
                        submitted.deviceSeed,
                        submitted.xPrivate,
                        submitted.stateHash,
                    )
                TestSecurity.get(server.url).stores[acct.deviceId] = TestSecurityStore(record)
                val recovered = fixtureRemote(acct, http).also { remotes += it }
                recovered.start()
                // RaApp also requests this immediately after recovery adoption.
                recovered.renewLeaseIfNeeded()
                withTimeout(10000) { recovered.state.first {it.leaseEndMs>System.currentTimeMillis()} }
                val before =
                    withTimeout(10000) {
                        recovered.state.first {
                            it.conn == ConnState.ONLINE && it.devices.containsKey(offline.deviceId)
                        }
                    }
                assertTrue(before.devices.containsKey(kept.deviceId))
                assertNull(before.devices.getValue(offline.deviceId).meta)
                // A retained reader with an expired lease must not be blocked by
                // the pending maintenance publication of its pre-recovery objects.
                val old = TestSecurity.get(server.url).stores.getValue(kept.deviceId).readSecurity()
                val expired = Remote(kept,http,clock={System.currentTimeMillis()+73L*60*60*1000},securityStorage=TestSecurityStore(old)).also {remotes+=it}
                expired.syncSecurity()
                expired.refreshDevices()
                assertEquals("retained-client",expired.state.value.devices.getValue(kept.deviceId).meta!!.name)
                val failure=runCatching {expired.renameDevice(kept.deviceId,"cannot-publish")}.exceptionOrNull()
                assertTrue(failure is DeviceSecurity.Failure && failure.code=="lease")
                r.syncSecurity()
                recovered.changePrefs(PrefsOp.Hide("/synthetic/new"))
                val prefs = recovered.state.value.prefs!!
                assertTrue(prefs.pinned.contains("/synthetic/kept"))
                assertTrue(prefs.hidden.contains("/synthetic/new"))
                recovered.refreshDevices()
                assertEquals(
                    "retained-client",
                    recovered.state.value.devices.getValue(kept.deviceId).meta!!.name,
                )
                assertTrue(recovered.state.value.devices.containsKey(offline.deviceId))
            } finally {
                remotes.forEach { it.close() }
                server.close()
            }
        }

    @Test
    fun removedIdentityIsDetectedByChallengeLoginWithoutALocalRemovalState() =
        runBlocking<Unit> {
            val server = TestServer()
            val http = Api.defaultHttp()
            try {
                val phone =
                    newAuthorityAccount(randomBytes(32), server.url, http, server.setupToken)
                val removed = approvedClient(phone, http)
                assertFalse(existingIdentityRemoved(Api(server.url, http), removed))
                removeAsAuthority(phone, removed.deviceId, http)
                assertTrue(existingIdentityRemoved(Api(server.url, http), removed))
            } finally {
                server.close()
            }
        }
}
