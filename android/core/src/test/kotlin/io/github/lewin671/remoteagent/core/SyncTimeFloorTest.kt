package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.proto.GetSecurityStatesResponse
import java.io.File
import java.util.Base64
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test

class SyncTimeFloorTest {
    @Test
    fun unchangedHeadSyncPersistsExpiryAcrossRestartAndClockRollback() =
        runBlocking<Unit> {
            val v =
                Json.parseToJsonElement(File(System.getProperty("domainVectors")).readText())
                    .jsonObject
            fun raw(key: String) = Base64.getDecoder().decode(v[key]!!.jsonPrimitive.content)
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
            val lease = HeadLease.parse(raw("lease"))
            val renewed = HeadLease.parse(raw("renewed_lease"))
            val phone = head.core.members.first { it.kind == 2u }
            for (response in listOf("no certificate", "certificate", "network failure")) {
                val store =
                    TestSecurityStore(
                        DeviceSecurity.Record(
                            DeviceSecurity.pinOf(genesis),
                            listOf(genesis, head),
                            phone.id,
                            hex("phone_x"),
                            lease = lease,
                        ),
                    )
                var clock = lease.notAfter.toLong()
                var certificate = if (response == "certificate") lease else null
                var failPull = response == "network failure"
                fun sdk() =
                    DeviceSecurityClient(
                        store,
                        phone.id,
                        SigningKey(hex("phone_seed")),
                        clock = { clock },
                        fetchStates = {
                            if (failPull) error("synthetic transient pull failure")
                            GetSecurityStatesResponse.newBuilder()
                                .setHeadEpoch(head.core.epoch.toLong())
                                .apply {
                                    certificate?.let { setLease(ByteString.copyFrom(it.bytes())) }
                                }
                                .build()
                        },
                    )
                val before = sdk()
                val firstSync = runCatching { before.sync() }
                if (failPull) assertTrue(firstSync.isFailure) else firstSync.getOrThrow()
                before.close()
                assertTrue(
                    "unchanged sync must persist local observed time",
                    store.readSecurity().floor >= lease.notAfter,
                )
                clock = lease.issuedAt.toLong()
                val reopened = sdk()
                val failure = runCatching {
                    reopened.seal(
                        DeviceSecurity.Purpose.PREFS,
                        request = "project-prefs",
                        seq = 1uL,
                        plain = byteArrayOf(1),
                    )
                }
                    .exceptionOrNull()
                assertTrue(
                    "clock rollback resurrected content publication",
                    failure is DeviceSecurity.Failure && failure.code == "lease",
                )
                clock = lease.notAfter.toLong()
                certificate = renewed
                failPull = false
                reopened.sync()
                assertNotNull(
                    reopened.seal(
                        DeviceSecurity.Purpose.PREFS,
                        request = "project-prefs",
                        seq = 1uL,
                        plain = byteArrayOf(1),
                    ),
                )
                reopened.close()
            }
        }
}
