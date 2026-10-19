package io.github.lewin671.remoteagent.core

import java.io.File
import java.util.Base64
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.Parameterized

@RunWith(Parameterized::class)
class LeasePublicationTest(private val purpose: DeviceSecurity.Purpose) {
    companion object {
        @JvmStatic
        @Parameterized.Parameters(name = "{0}")
        fun purposes() = DeviceSecurity.Purpose.entries.map { arrayOf(it) }
    }

    @Test
    fun newPublicationStopsAtExpiryHistoryRemainsAndRenewalResumesIt() =
        runBlocking<Unit> {
            val v =
                Json.parseToJsonElement(File(System.getProperty("domainVectors")).readText())
                    .jsonObject
            fun bytes(key: String) = Base64.getDecoder().decode(v[key]!!.jsonPrimitive.content)
            fun hex(key: String) =
                v[key]!!
                    .jsonPrimitive
                    .content
                    .chunked(2)
                    .map { it.toInt(16).toByte() }
                    .toByteArray()
            fun state(key: String) =
                DeviceSecurity.parseState(
                    Base64.getDecoder()
                        .decode(v[key]!!.jsonObject["state"]!!.jsonPrimitive.content),
                )
            val genesis = state("genesis")
            val head = state("added")
            val first = HeadLease.parse(bytes("lease"))
            val renewed = HeadLease.parse(bytes("renewed_lease"))
            val clientRole =
                purpose.control ||
                    purpose in
                        listOf(DeviceSecurity.Purpose.PREFS, DeviceSecurity.Purpose.DEVICE_META)
            val member = head.core.members.first { it.kind == if (clientRole) 2u else 1u }
            val machine = head.core.members.first { it.kind == 1u }.id
            val store =
                TestSecurityStore(
                    DeviceSecurity.Record(
                        DeviceSecurity.pinOf(genesis),
                        listOf(genesis, head),
                        member.id,
                        hex(if (clientRole) "phone_x" else "machine_x"),
                        lease = first,
                    ),
                )
            var time = first.issuedAt.toLong()
            val sdk =
                DeviceSecurityClient(
                    store,
                    member.id,
                    SigningKey(hex(if (clientRole) "phone_seed" else "machine_seed")),
                    clock = { time },
                    fetchStates = { error("no state pull expected") },
                )
            val standalone =
                purpose in listOf(DeviceSecurity.Purpose.PREFS, DeviceSecurity.Purpose.DEVICE_META)
            val noSession =
                standalone ||
                    purpose in
                        listOf(
                            DeviceSecurity.Purpose.MACHINE_META,
                            DeviceSecurity.Purpose.COMMAND,
                            DeviceSecurity.Purpose.RESULT,
                        )
            val noRequest =
                purpose in
                    listOf(
                        DeviceSecurity.Purpose.EVENT,
                        DeviceSecurity.Purpose.SESSION_META,
                        DeviceSecurity.Purpose.SESSION_KEY,
                        DeviceSecurity.Purpose.MACHINE_META,
                    )
            val request =
                when (purpose) {
                    DeviceSecurity.Purpose.PREFS -> "project-prefs"
                    DeviceSecurity.Purpose.DEVICE_META -> member.id
                    else -> if (noRequest) "" else "lease-request-123456"
                }
            val plain = byteArrayOf(1, 2, 3)
            val seed = ByteArray(32) { 7 }
            fun seal() =
                sdk.seal(
                    purpose,
                    machine = if (standalone) "" else machine,
                    session = if (noSession) "" else "lease-session",
                    request = request,
                    seq = 1uL,
                    deadline = if (purpose.control) first.notAfter + 60000uL else 0uL,
                    plain = plain,
                    seed = seed,
                )
            val stored = seal()
            time = first.notAfter.toLong()
            var failure = runCatching { seal() }.exceptionOrNull()
            assertTrue(
                "$purpose must stop at lease expiry",
                failure is DeviceSecurity.Failure && failure.code == "lease",
            )
            if (!purpose.control) assertArrayEquals(plain, sdk.openHistory(stored, seed).second)
            store.touchSecurity(DeviceSecurity.stateHash(head), first.notAfter)
            time = first.issuedAt.toLong()
            failure = runCatching { seal() }.exceptionOrNull()
            assertTrue(
                "persisted floor must prevent rollback publication",
                failure is DeviceSecurity.Failure && failure.code == "lease",
            )
            time = first.notAfter.toLong()
            sdk.install(emptyList(), lease = renewed)
            assertNotNull(seal())
            assertEquals(2, sdk.chain().size)
        }
}
