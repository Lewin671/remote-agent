package io.github.lewin671.remoteagent.core.transport

import io.github.lewin671.remoteagent.core.Account
import io.github.lewin671.remoteagent.core.Authority
import io.github.lewin671.remoteagent.core.DeviceSecurity
import io.github.lewin671.remoteagent.core.HeadLease
import io.github.lewin671.remoteagent.core.Remote
import io.github.lewin671.remoteagent.core.SigningKey
import io.github.lewin671.remoteagent.core.TestSecurityStore
import io.github.lewin671.remoteagent.core.buildDirectSecuritySnapshot
import io.github.lewin671.remoteagent.core.deviceId
import io.github.lewin671.remoteagent.core.proto.DirectSecuritySnapshotEvent
import okhttp3.OkHttpClient
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class DirectSecuritySnapshotTest {
    @Test
    fun tableDrivenPolicyAndFreshnessGates() {
        val nowMs = 1_700_000_000_000L
        val adminSigner = SigningKey(ByteArray(32) { 0x11 })
        val adminX = ByteArray(32) { 0x21 }
        val machineSigner = SigningKey(ByteArray(32) { 0x31 })
        val machineX = ByteArray(32) { 0x41 }
        val extraSigner = SigningKey(ByteArray(32) { 0x51 })
        val extraX = ByteArray(32) { 0x61 }
        val recoveryPub = SigningKey(ByteArray(32) { 0x71 }).publicKey
        val accountId = deviceId(SigningKey(ByteArray(32) { 0x81.toByte() }).publicKey)
        val domain = ByteArray(32) { 0x91.toByte() }
        val s1 = ByteArray(32) { 0xA1.toByte() }
        val s2 = ByteArray(32) { 0xA2.toByte() }
        val s3 = ByteArray(32) { 0xA3.toByte() }

        val adminMember = Authority.newMember(adminSigner, adminX, 2u)
        val machineMember = Authority.newMember(machineSigner, machineX, 1u)
        val extraMember = Authority.newMember(extraSigner, extraX, 2u)

        val g = Authority.genesis(accountId, domain, adminMember, recoveryPub, s1, adminSigner, nowMs)
        val pMachine = Authority.pair(g, machineMember, s1, s2, adminSigner, nowMs + 1)
        val lease = HeadLease.sign(pMachine, adminSigner, nowMs + 1)
        val pin = DeviceSecurity.pinOf(g)
        val validRec = DeviceSecurity.Record(
            pin = pin,
            chain = listOf(g, pMachine),
            memberID = adminMember.id,
            xPrivate = adminX,
            floor = (nowMs + 1).toULong(),
            keys = mapOf(1uL to s1, 2uL to s2),
            lease = lease,
        )

        val noLeaseRec = DeviceSecurity.Record(
            pin = pin,
            chain = listOf(g, pMachine),
            memberID = adminMember.id,
            xPrivate = adminX,
            floor = (nowMs + 1).toULong(),
            keys = mapOf(1uL to s1, 2uL to s2),
            lease = null,
        )

        // Removed client record: extraMember was paired at pExtra then removed at rmExtra -> extraRec.member == false.
        val pExtra = Authority.pair(pMachine, extraMember, s2, s3, adminSigner, nowMs + 2)
        val s4 = ByteArray(32) { 0xA4.toByte() }
        val rmExtra = Authority.remove(pExtra, listOf(extraMember.id), s3, s4, adminSigner, nowMs + 3)
        val leaseRm = HeadLease.sign(rmExtra, adminSigner, nowMs + 3)
        val removedRec = DeviceSecurity.Record(
            pin = pin,
            chain = listOf(g, pMachine, pExtra, rmExtra),
            memberID = extraMember.id,
            xPrivate = extraX,
            floor = (nowMs + 3).toULong(),
            keys = mapOf(1uL to s1, 2uL to s2, 3uL to s3),
            lease = leaseRm,
        )
        assertFalse(removedRec.member)

        // Record holding corrupted/mismatched xPrivate relative to head's selfMember.x.
        val badXRec = DeviceSecurity.Record(
            pin = pin,
            chain = listOf(g, pMachine),
            memberID = adminMember.id,
            xPrivate = adminX.copyOf(),
            floor = (nowMs + 1).toULong(),
            keys = mapOf(1uL to s1, 2uL to s2),
            lease = lease,
        )
        val xField = DeviceSecurity.Record::class.java.getDeclaredField("xPrivate")
        xField.isAccessible = true
        (xField.get(badXRec) as ByteArray).fill(0xCC.toByte())

        val started = nowMs - 5_000L
        val validNow = nowMs + 1_000L
        val wrongEdPub = SigningKey(ByteArray(32) { 0xEE.toByte() }).publicKey

        data class Case(
            val name: String,
            val record: DeviceSecurity.Record,
            val expectedDeviceId: String,
            val devicePublicKey: ByteArray,
            val started: Long,
            val nowMs: Long,
            val wantOk: Boolean,
        )

        val cases = listOf(
            Case(
                name = "valid member with machine (kind=1) and client (kind=2)",
                record = validRec,
                expectedDeviceId = adminMember.id,
                devicePublicKey = adminSigner.publicKey,
                started = started,
                nowMs = validNow,
                wantOk = true,
            ),
            Case(
                name = "non-member rejected",
                record = removedRec,
                expectedDeviceId = extraMember.id,
                devicePublicKey = extraSigner.publicKey,
                started = started,
                nowMs = validNow,
                wantOk = false,
            ),
            Case(
                name = "memberID != deviceId rejected",
                record = validRec,
                expectedDeviceId = "00000000000000000000000000000000",
                devicePublicKey = adminSigner.publicKey,
                started = started,
                nowMs = validNow,
                wantOk = false,
            ),
            Case(
                name = "missing lease rejected",
                record = noLeaseRec,
                expectedDeviceId = adminMember.id,
                devicePublicKey = adminSigner.publicKey,
                started = started,
                nowMs = validNow,
                wantOk = false,
            ),
            Case(
                name = "expired lease rejected",
                record = validRec,
                expectedDeviceId = adminMember.id,
                devicePublicKey = adminSigner.publicKey,
                started = lease.notAfter.toLong() - 1_000L,
                nowMs = lease.notAfter.toLong(),
                wantOk = false,
            ),
            Case(
                name = "20s start window expired rejected",
                record = validRec,
                expectedDeviceId = adminMember.id,
                devicePublicKey = adminSigner.publicKey,
                started = started,
                nowMs = started + 20_000L,
                wantOk = false,
            ),
            Case(
                name = "mismatched Ed25519 devicePublicKey rejected",
                record = validRec,
                expectedDeviceId = adminMember.id,
                devicePublicKey = wrongEdPub,
                started = started,
                nowMs = validNow,
                wantOk = false,
            ),
            Case(
                name = "mismatched X25519 xPrivate rejected",
                record = badXRec,
                expectedDeviceId = adminMember.id,
                devicePublicKey = adminSigner.publicKey,
                started = started,
                nowMs = validNow,
                wantOk = false,
            ),
        )

        for (tc in cases) {
            val built = buildDirectSecuritySnapshot(
                record = tc.record,
                expectedDeviceId = tc.expectedDeviceId,
                devicePublicKey = tc.devicePublicKey,
                started = tc.started,
                nowMs = tc.nowMs,
            )
            if (!tc.wantOk) {
                assertNull(tc.name, built)
            } else {
                assertNotNull(tc.name, built)
                val snap = built!!.first
                assertTrue(tc.name, snap.member && !snap.rejected)
                assertTrue(tc.name, snap.membersList.any { it.kind == 1 })
                assertTrue(tc.name, snap.membersList.any { it.kind == 2 && it.isSelf })
            }
        }
    }

    @Test
    fun onInstallOnlyInvalidatesNeverFabricatesMemberSnapshot() {
        val nowMs = System.currentTimeMillis()
        val adminSeed = ByteArray(32) { 0x12 }
        val adminSigner = SigningKey(adminSeed)
        val adminX = ByteArray(32) { 0x22 }
        val extraSigner = SigningKey(ByteArray(32) { 0x52 })
        val extraX = ByteArray(32) { 0x62 }
        val rootSeed = ByteArray(32) { 0x05 }
        val s1 = ByteArray(32) { 0xB1.toByte() }
        val s2 = ByteArray(32) { 0xB2.toByte() }

        val adminMember = Authority.newMember(adminSigner, adminX, 2u)
        val extraMember = Authority.newMember(extraSigner, extraX, 1u)
        val account = Account(
            server = "https://example.com",
            root = rootSeed,
            deviceSeed = adminSeed,
            deviceId = adminMember.id,
        )
        val g = Authority.genesis(
            account.accountId,
            ByteArray(32) { 0x92.toByte() },
            adminMember,
            SigningKey(ByteArray(32) { 0x72 }).publicKey,
            s1,
            adminSigner,
            nowMs,
        )
        val lease1 = HeadLease.sign(g, adminSigner, nowMs)
        val rec1 = DeviceSecurity.Record(
            pin = DeviceSecurity.pinOf(g),
            chain = listOf(g),
            memberID = adminMember.id,
            xPrivate = adminX,
            floor = nowMs.toULong(),
            keys = mapOf(1uL to s1),
            lease = lease1,
        )
        val store = TestSecurityStore(rec1)
        val remote = Remote(
            account = account,
            http = OkHttpClient(),
            clock = { nowMs + 100L },
            securityStorage = store,
        )
        try {
            val emitMethod = Remote::class.java.getDeclaredMethod(
                "emitDirectSecuritySnapshot",
                DeviceSecurity.Record::class.java,
                Long::class.javaPrimitiveType,
            )
            emitMethod.isAccessible = true
            emitMethod.invoke(remote, rec1, nowMs)

            val lastField = Remote::class.java.getDeclaredField("lastDirectSecurity")
            lastField.isAccessible = true
            val before = lastField.get(remote) as DirectSecuritySnapshotEvent
            assertTrue(before.member && !before.rejected)

            val nextState = Authority.pair(g, extraMember, s1, s2, adminSigner, nowMs + 1L)
            val nextLease = HeadLease.sign(nextState, adminSigner, nowMs + 1L)
            remote.security.install(listOf(nextState), lease = nextLease)

            val after = lastField.get(remote) as DirectSecuritySnapshotEvent
            assertTrue(after.rejected)
            assertFalse(after.member)
        } finally {
            remote.close()
        }
    }
}
