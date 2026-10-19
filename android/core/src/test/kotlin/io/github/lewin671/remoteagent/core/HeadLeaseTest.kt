package io.github.lewin671.remoteagent.core

import java.io.File
import java.util.Base64
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test

class HeadLeaseTest {
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
