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
    @get:org.junit.Rule val hangGuard = hangGuard()
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
            CoreSecurityBridge(
                store,
                member.id,
                SigningKey(hex(if (clientRole) "phone_seed" else "machine_seed")),
                clock = { time },
                fetchStates = { error("no state pull expected") },
            ).use { sdk ->
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
                val plain =
                    when (purpose) {
                        DeviceSecurity.Purpose.EVENT ->
                            io.github.lewin671.remoteagent.proto.Event.newBuilder()
                                .setSeq(1L)
                                .setSessionId("lease-session")
                                .setAgentMessage(
                                    io.github.lewin671.remoteagent.proto.AgentMessage.newBuilder()
                                        .setText("ok"),
                                )
                                .build()
                                .toByteArray()
                        DeviceSecurity.Purpose.SESSION_META ->
                            io.github.lewin671.remoteagent.proto.Session.newBuilder()
                                .setId("lease-session")
                                .build()
                                .toByteArray()
                        DeviceSecurity.Purpose.SESSION_KEY -> ByteArray(32) { 9 }
                        DeviceSecurity.Purpose.MACHINE_META ->
                            io.github.lewin671.remoteagent.proto.MachineMeta.newBuilder()
                                .setHostname("m")
                                .build()
                                .toByteArray()
                        DeviceSecurity.Purpose.COMMAND ->
                            io.github.lewin671.remoteagent.proto.Command.newBuilder()
                                .setInterrupt(
                                    io.github.lewin671.remoteagent.proto.InterruptRequest.newBuilder()
                                        .setSessionId("s1"),
                                )
                                .build()
                                .toByteArray()
                        DeviceSecurity.Purpose.RESULT ->
                            io.github.lewin671.remoteagent.proto.CommandResult.newBuilder()
                                .setOutcome("executed")
                                .build()
                                .toByteArray()
                        DeviceSecurity.Purpose.TERMINAL_OUTPUT ->
                            io.github.lewin671.remoteagent.proto.TerminalFrame.newBuilder()
                                .setOutput(com.google.protobuf.ByteString.copyFromUtf8("x"))
                                .build()
                                .toByteArray()
                        DeviceSecurity.Purpose.TERMINAL_INPUT ->
                            io.github.lewin671.remoteagent.proto.TerminalFrame.newBuilder()
                                .setInput(com.google.protobuf.ByteString.copyFromUtf8("x"))
                                .build()
                                .toByteArray()
                        DeviceSecurity.Purpose.TERMINAL_RESIZE ->
                            io.github.lewin671.remoteagent.proto.TerminalFrame.newBuilder()
                                .setResize(
                                    io.github.lewin671.remoteagent.proto.TerminalResize.newBuilder()
                                        .setCols(80)
                                        .setRows(24),
                                )
                                .build()
                                .toByteArray()
                        DeviceSecurity.Purpose.TERMINAL_HISTORY ->
                            io.github.lewin671.remoteagent.proto.TerminalFrame.newBuilder()
                                .setHistoryRequest(io.github.lewin671.remoteagent.proto.TerminalHistoryRequest.getDefaultInstance())
                                .build()
                                .toByteArray()
                        DeviceSecurity.Purpose.NOTIFY ->
                            io.github.lewin671.remoteagent.proto.NotifyContent.newBuilder()
                                .setMachineId(machine)
                                .setSessionId("lease-session")
                                .setKind(io.github.lewin671.remoteagent.proto.NotifyKind.NOTIFY_KIND_ATTENTION)
                                .setTitle("t")
                                .build()
                                .toByteArray()
                        DeviceSecurity.Purpose.IMAGE ->
                            io.github.lewin671.remoteagent.proto.ImagePayload.newBuilder()
                                .setFormat(io.github.lewin671.remoteagent.proto.ImageFormat.IMAGE_FORMAT_PNG)
                                .setData(com.google.protobuf.ByteString.copyFrom(byteArrayOf(1)))
                                .build()
                                .toByteArray()
                        DeviceSecurity.Purpose.PREFS ->
                            io.github.lewin671.remoteagent.proto.ProjectPrefs.newBuilder()
                                .setSchema(1)
                                .build()
                                .toByteArray()
                        DeviceSecurity.Purpose.DEVICE_META ->
                            io.github.lewin671.remoteagent.proto.DeviceMeta.newBuilder()
                                .setName("dev")
                                .build()
                                .toByteArray()
                    }
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
}
