package io.github.lewin671.remoteagent.core

import java.io.File
import java.util.Base64
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test

class HeadLeaseTest {
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
        val reopened = SecurityOperation.parse(op.bytes())
        assertArrayEquals(op.signed, reopened.signed)
        assertArrayEquals(op.local, reopened.local)
        assertEquals(op.phase, reopened.phase)
    }
}
