package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.GetRecoveryContextResponse
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** Recovery submission against the real server; the 48-hour completion is Go-tested. */
class RecoveryTest {
    @Test
    fun aRecoveryKeyReadsTheChainAndSubmitsAStateWithoutTheOldContentKey() =
        runBlocking<Unit> {
            val server = TestServer()
            val http = Api.defaultHttp()
            try {
                val recoverySeed = ByteArray(32) { 0x31 }
                val phone =
                    newAuthorityAccount(
                        randomBytes(32),
                        server.url,
                        http,
                        server.setupToken,
                        recoverySeed,
                    )
                approvedClient(phone, http, io.github.lewin671.remoteagent.proto.DeviceKind.DEVICE_KIND_MACHINE)
                val before =
                    Recovery.context(
                        server.url,
                        http,
                        SigningKey(recoverySeed).publicKey,
                        recoverySeed,
                    )
                assertEquals(GetRecoveryContextResponse.Phase.PHASE_UNSPECIFIED, before.phase)
                assertEquals(2, before.chain.size)
                assertEquals(before.pin.account, phone.accountId)

                // A code for another genesis is refused before anything is
                // submitted, even if the relay's chain is otherwise valid.
                val wrong =
                    runCatching {
                        Recovery.context(
                            server.url,
                            http,
                            SigningKey(recoverySeed).publicKey,
                            recoverySeed,
                            expectedGenesis = ByteArray(10) { 0x7f },
                        )
                    }.exceptionOrNull()
                assertTrue(wrong is DeviceSecurity.Failure)

                val newDevice = randomBytes(32)
                val submitted =
                    Recovery.submit(
                        server.url,
                        http,
                        recoverySeed,
                        newDevice,
                        before.pin.genesis.copyOf(10),
                    )
                assertTrue(submitted.readyMs > System.currentTimeMillis() + 47L * 3600_000)
                assertNotNull(RecoveryCode.decode(submitted.newCode))
                assertEquals(before.accountId, submitted.accountId)
                assertEquals(phone.accountPublicKey.hex(), before.accountPublicKey.hex())

                val pending =
                    Recovery.context(
                        server.url,
                        http,
                        SigningKey(recoverySeed).publicKey,
                        recoverySeed,
                    )
                assertEquals(GetRecoveryContextResponse.Phase.PHASE_PENDING, pending.phase)
                assertEquals(submitted.recoveryId, pending.recoveryId)
                // Until the delay ends the published head is still the old authority's.
                assertEquals(phone.deviceId, pending.head.core.authorityID)
                // The new authority has the public key it needs for a later pairing payload.
                assertEquals(phone.accountId, deviceId(pending.accountPublicKey))
                val payload =
                    io.github.lewin671.remoteagent.proto.PairingPayload.newBuilder()
                        .setServer(server.url)
                        .setAccountPublicKey(
                            com.google.protobuf.ByteString.copyFrom(pending.accountPublicKey),
                        )
                        .build()
                assertEquals(phone.accountId, deviceId(payload.accountPublicKey.toByteArray()))
            } finally {
                server.close()
            }
        }
}
