package io.github.lewin671.remoteagent.core

import java.io.File
import java.util.Base64
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test

class HeadLeaseTest {
    @Test
    fun staleOperationCannotRewindCommittedProgressWithTheSameSignature() {
        val old =
            SecurityOperation(
                "state",
                byteArrayOf(1),
                byteArrayOf(2),
                phase = SecurityOperation.Phase.DELIVERED,
                codeSaved = true,
            )
        assertFalse(old.advanced(SecurityOperation.Phase.PREPARED).preservesProgressOf(old))
        assertFalse(
            SecurityOperation(
                    old.kind,
                    old.parent,
                    old.signed,
                    phase = old.phase,
                    codeSaved = false,
                )
                .preservesProgressOf(old),
        )
        assertTrue(old.preservesProgressOf(old))
    }

    @Test
    fun rollbackNeverSignsALeaseBeforeItsState() {
        val signer = SigningKey(ByteArray(32) { 11 })
        val x = ByteArray(32) { 12 }
        val state =
            Authority.genesis(
                "0123456789abcdef0123456789abcdef",
                ByteArray(32) { 15 },
                Authority.newMember(signer, x, 2u),
                ByteArray(32) { 13 },
                ByteArray(32) { 14 },
                signer,
                10000,
            )
        val lease = HeadLease.sign(state, signer, 9000)
        lease.verify(state)
        assertEquals(10000uL, lease.issuedAt)
        val record =
            DeviceSecurity.Record(
                DeviceSecurity.pinOf(state),
                listOf(state),
                state.core.authorityID,
                x,
                floor = 20000uL,
                lease = lease,
            )
        val store = TestSecurityStore(record)
        val sdk =
            DeviceSecurityClient(
                store,
                state.core.authorityID,
                signer,
                fetchStates = { error("unused") },
            )
        val time = sdk.signingTime(9000)
        assertEquals(20000L, time)
        val next = Authority.veto(state, ByteArray(32) { 16 }, record.secret(), signer, time)
        assertEquals(20000uL, next.core.issuedAt)
        val draft =
            CreatedAccount(
                Account(
                    "https://relay.example.com",
                    ByteArray(32) { 1 },
                    ByteArray(32) { 11 },
                    state.core.authorityID,
                ),
                record,
                "",
            )
        val resumed = draft.copy(record = record.installed(listOf(next), listOf(null)))
        // A chain extension retains the previous-head certificate until its independent renewal.
        resumed.checkSecurityProgressFrom(draft)
        assertTrue(resumed.record.includesInstallation(draft.record))
        assertFalse(draft.record.includesInstallation(resumed.record))
        val renewed = HeadLease.sign(next, signer, 9000, record.floor)
        renewed.verify(next)
        assertEquals(20000uL, renewed.issuedAt)
        val later = HeadLease.sign(state, signer, 21000)
        store.installSecurity(DeviceSecurity.stateHash(state), emptyList(), emptyList(), later)
        assertTrue(
            runCatching {
                store.installSecurity(
                    DeviceSecurity.stateHash(state),
                    emptyList(),
                    emptyList(),
                    lease,
                )
            }
                .isFailure,
        )
        assertEquals(later, store.readSecurity().lease)
    }

    @Test
    fun diagnosticsReportOnlyACertificateBoundToTheInstalledHead() {
        val v =
            Json.parseToJsonElement(File(System.getProperty("domainVectors")).readText()).jsonObject
        fun raw(name: String) =
            Base64.getDecoder().decode(v[name]!!.jsonObject["state"]!!.jsonPrimitive.content)
        val genesis = DeviceSecurity.parseState(raw("genesis"))
        val added = DeviceSecurity.parseState(raw("added"))
        val later = DeviceSecurity.parseState(raw("renewal"))
        val lease = HeadLease.parse(Base64.getDecoder().decode(v["lease"]!!.jsonPrimitive.content))
        val x =
            v["phone_x"]!!
                .jsonPrimitive
                .content
                .chunked(2)
                .map { it.toInt(16).toByte() }
                .toByteArray()
        val record =
            DeviceSecurity.Record(
                DeviceSecurity.pinOf(genesis),
                listOf(genesis, added),
                genesis.core.authorityID,
                x,
                lease = lease,
            )
        assertEquals(
            lease.notAfter.toLong(),
            record.leaseExpiration(),
        ) // Expired is still a real date.
        assertEquals(0L, record.installed(listOf(later), listOf(null)).leaseExpiration())
    }

    @Test
    fun expiredLeaseExplainsWhyChangesCannotBeSaved() {
        val text = errorText(DeviceSecurity.Failure("lease"))
        assertTrue(text.contains("无法保存"))
        assertTrue(text.contains("续期"))
        assertTrue(text.contains("历史"))
    }

    @Test
    fun ordinaryHandoffKeepsTheHistoricalKeyWalk() {
        val v =
            Json.parseToJsonElement(File(System.getProperty("domainVectors")).readText()).jsonObject
        fun raw(key: String) =
            Base64.getDecoder().decode(v[key]!!.jsonObject["state"]!!.jsonPrimitive.content)
        val parent = DeviceSecurity.parseState(raw("handoff_parent"))
        val next = DeviceSecurity.parseState(raw("handoff"))
        DeviceSecurity.verifyTransition(parent, next)
        HeadLease.parse(Base64.getDecoder().decode(v["handoff_lease"]!!.jsonPrimitive.content))
            .verify(next)
        assertNull(next.core.member(parent.core.authorityID))
        assertEquals(60, next.prevSecret.size)
    }

    @Test
    fun sharedIndependentCertificatesDoNotChangeTheChain() {
        val v =
            Json.parseToJsonElement(File(System.getProperty("domainVectors")).readText()).jsonObject
        fun bytes(s: String) = Base64.getDecoder().decode(s)
        val state =
            DeviceSecurity.parseState(
                bytes(v["added"]!!.jsonObject["state"]!!.jsonPrimitive.content),
            )
        val first = HeadLease.parse(bytes(v["lease"]!!.jsonPrimitive.content))
        val renewed = HeadLease.parse(bytes(v["renewed_lease"]!!.jsonPrimitive.content))
        first.verify(state)
        renewed.verify(state)
        assertArrayEquals(first.head, renewed.head)
        assertTrue(renewed.issuedAt > first.issuedAt)
        assertEquals(2uL, state.core.epoch)
        assertTrue(runCatching { first.valid(state, first.notAfter) }.isFailure)
        assertTrue(
            runCatching {
                first.copy(notAfter = first.issuedAt + DeviceSecurity.LEASE_U + 1uL).bytes()
            }
                .isFailure,
        )
        val op =
            SecurityOperation(
                "state",
                state.core.parent,
                DeviceSecurity.stateBytes(state),
                local = byteArrayOf(1, 2),
                phase = SecurityOperation.Phase.PUBLISHED,
            )
        val oldRequest =
            io.github.lewin671.remoteagent.proto.PutSecurityStateRequest.newBuilder()
                .addStates(
                    io.github.lewin671.remoteagent.proto.SecurityState.newBuilder()
                        .setState(
                            com.google.protobuf.ByteString.copyFrom(
                                DeviceSecurity.stateBytes(state),
                            ),
                        ),
                )
                .setLease(com.google.protobuf.ByteString.copyFrom(first.bytes()))
                .build()
        val oldOp = SecurityOperation("state", state.core.parent, oldRequest.toByteArray())
        val renewedOp =
            SecurityOperation(
                "state",
                state.core.parent,
                oldRequest
                    .toBuilder()
                    .setLease(com.google.protobuf.ByteString.copyFrom(renewed.bytes()))
                    .build()
                    .toByteArray(),
            )
        assertTrue(renewedOp.renewsCertificateOf(oldOp))
        assertFalse(
            SecurityOperation(
                    "state",
                    state.core.parent,
                    oldRequest.toBuilder().clearStates().build().toByteArray(),
                )
                .renewsCertificateOf(oldOp),
        )
        assertFalse(
            SecurityOperation(
                    "state",
                    state.core.parent,
                    renewedOp.signed,
                    delivery = byteArrayOf(1),
                )
                .renewsCertificateOf(oldOp),
        )
        val reopened = SecurityOperation.parse(op.bytes())
        assertArrayEquals(op.signed, reopened.signed)
        assertArrayEquals(op.local, reopened.local)
        assertEquals(op.phase, reopened.phase)
    }
}
