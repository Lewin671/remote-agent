package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.proto.DeviceKind
import io.github.lewin671.remoteagent.proto.Event
import io.github.lewin671.remoteagent.proto.GetRecoveryContextResponse
import io.github.lewin671.remoteagent.proto.Header
import io.github.lewin671.remoteagent.proto.PairApproval
import io.github.lewin671.remoteagent.proto.PayloadKind
import io.github.lewin671.remoteagent.proto.ProjectPrefs
import io.github.lewin671.remoteagent.proto.SecurityState
import kotlinx.coroutines.runBlocking
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test

/** Live frames from a removed sender, removal records, future chains and join approvals. */
class LiveSecurityTest {
    @Test
    fun aRemovedSenderCannotWriteAndItsRetainedHistoryStillOpens() = runBlocking<Unit> {
        val server = TestServer()
        val http = Api.defaultHttp()
        try {
            val phone = newAuthorityAccount(randomBytes(32), server.url, http, server.setupToken)
            val victim = fixtureMember(phone, http)
            val machine =
                fixtureMember(phone, http, DeviceKind.DEVICE_KIND_MACHINE)
            val security = TestSecurity.get(server.url)

            val sessionSeed = randomBytes(32)
            val event =
                Event.newBuilder()
                    .setSessionId("session-live")
                    .setSeq(1)
                    .setAgentMessage(
                        io.github.lewin671.remoteagent.proto.AgentMessage.newBuilder()
                            .setText("retained history"),
                    )
                    .build()
            val eventEnv =
                security.seal(
                    machine,
                    Header.newBuilder()
                        .setKind(PayloadKind.PAYLOAD_KIND_EVENT)
                        .setMachineId(machine.deviceId)
                        .setSessionId("session-live")
                        .setSeq(1)
                        .build(),
                    event,
                    sessionSeed,
                )
            val prefs = ProjectPrefs.newBuilder().setSchema(1).build()
            val prefsEnv =
                security.seal(
                    victim,
                    Header.newBuilder()
                        .setKind(PayloadKind.PAYLOAD_KIND_ACCOUNT_PREFS)
                        .setRequestId("project-prefs")
                        .setSeq(1)
                        .build(),
                    prefs,
                )

            removeAsAuthority(phone, victim.deviceId, http)

            val phoneStore = security.stores.getValue(phone.deviceId)
            val phoneClient =
                DeviceSecurityClient(
                    phoneStore,
                    phone.deviceId,
                    SigningKey(phone.deviceSeed),
                    fetchStates = { error("no state pull expected") },
                )
            assertTrue(phoneStore.readSecurity().member)
            try {
                phoneClient.openMessage(prefsEnv, history = false, parse = ProjectPrefs::parseFrom)
                fail("a removed sender's prefs accepted as live")
            } catch (_: DeviceSecurity.Failure) {}

            // Installing the removal keeps the victim's record and its old key.
            val victimStore = security.stores.getValue(victim.deviceId)
            assertFalse(victimStore.readSecurity().member)
            val victimClient =
                DeviceSecurityClient(
                    victimStore,
                    victim.deviceId,
                    SigningKey(victim.deviceSeed),
                    fetchStates = { error("no state pull expected") },
                )
            val (_, opened) =
                victimClient.openMessage(
                    eventEnv,
                    history = true,
                    seed = sessionSeed,
                    parse = Event::parseFrom,
                )
            assertArrayEquals(event.toByteArray(), opened.toByteArray())
        } finally {
            server.close()
        }
    }

    @Test
    fun aFutureDatedRecoveryChainIsRefusedBeforeInstall() {
        val accountSigner = SigningKey(randomBytes(32))
        val accountId = deviceId(accountSigner.publicKey)
        val phone = SigningKey(randomBytes(32))
        val member = Authority.newMember(phone, randomBytes(32), 2u)
        val future = System.currentTimeMillis() + 10 * 60_000
        val genesis =
            Authority.genesis(
                accountId,
                randomBytes(32),
                member,
                randomBytes(32),
                Authority.newSecret(),
                phone,
                future,
            )
        val states =
            listOf(
                SecurityState.newBuilder()
                    .setState(ByteString.copyFrom(DeviceSecurity.stateBytes(genesis)))
                    .build()
            )
        val now = System.currentTimeMillis()
        try {
            RecoveryChain.parse(
                accountId,
                accountSigner.publicKey,
                states,
                GetRecoveryContextResponse.Phase.PHASE_UNSPECIFIED,
                0,
                "",
                now = now,
            )
            fail("future-dated recovery chain accepted")
        } catch (e: DeviceSecurity.Failure) {
            assertTrue(e.code == "clock" || e.code == "untrusted")
        }
        // The account bindings are checked before anything is pinned.
        try {
            RecoveryChain.parse(
                accountId,
                SigningKey(randomBytes(32)).publicKey,
                states,
                GetRecoveryContextResponse.Phase.PHASE_UNSPECIFIED,
                0,
                "",
                now = now,
            )
            fail("a chain for another account was accepted")
        } catch (e: DeviceSecurity.Failure) {
            assertTrue(e.code == "untrusted")
        }
    }

    @Test
    fun joinApprovalChecksExpiryAndTheApproverIdentity() {
        val accountSigner = SigningKey(randomBytes(32))
        val accountId = deviceId(accountSigner.publicKey)
        val phoneSeed = randomBytes(32)
        val phoneId = deviceId(SigningKey(phoneSeed).publicKey)
        val account =
            Account(
                "https://ra.example.com",
                ByteArray(0),
                phoneSeed,
                phoneId,
                accountId,
                accountSigner.publicKey,
            )
        val candidate = SigningKey(randomBytes(32)).publicKey
        val pairingId = newRequestId()
        val now = System.currentTimeMillis()
        val (raw, signature) =
            makePairApproval(
                account,
                pairingId,
                candidate,
                DeviceKind.DEVICE_KIND_CLIENT,
                now + 60_000,
            )
        val approval = PairApproval.parseFrom(raw)
        assertTrue(
            validJoinApproval(
                approval,
                raw,
                signature,
                accountId,
                pairingId,
                phoneId,
                candidate,
                now,
            )
        )
        // Expired.
        assertFalse(
            validJoinApproval(
                approval,
                raw,
                signature,
                accountId,
                pairingId,
                phoneId,
                candidate,
                now + 60_001,
            )
        )
        // Signed by a key that is not the claimed approver.
        val other =
            Account(
                "https://ra.example.com",
                ByteArray(0),
                randomBytes(32),
                phoneId,
                accountId,
                accountSigner.publicKey,
            )
        val (otherRaw, otherSig) =
            makePairApproval(
                other,
                pairingId,
                candidate,
                DeviceKind.DEVICE_KIND_CLIENT,
                now + 60_000,
            )
        assertFalse(
            validJoinApproval(
                PairApproval.parseFrom(otherRaw),
                otherRaw,
                otherSig,
                accountId,
                pairingId,
                phoneId,
                candidate,
                now,
            )
        )
        // An approver cannot approve itself as the candidate.
        val (selfRaw, selfSig) =
            makePairApproval(
                account,
                pairingId,
                SigningKey(phoneSeed).publicKey,
                DeviceKind.DEVICE_KIND_CLIENT,
                now + 60_000,
            )
        assertFalse(
            validJoinApproval(
                PairApproval.parseFrom(selfRaw),
                selfRaw,
                selfSig,
                accountId,
                pairingId,
                phoneId,
                SigningKey(phoneSeed).publicKey,
                now,
            )
        )
    }
}
