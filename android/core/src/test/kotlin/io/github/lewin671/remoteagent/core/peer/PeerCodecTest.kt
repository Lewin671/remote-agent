package io.github.lewin671.remoteagent.core.peer

import kotlinx.serialization.json.*
import org.junit.Test
import org.junit.Assert.*
import java.io.File
import org.bouncycastle.crypto.params.Ed25519PrivateKeyParameters
import org.bouncycastle.crypto.params.X25519PrivateKeyParameters

class PeerCodecTest {
    private val now = 1700000000000L
    private fun hash(s: String) = Wire.hash(Wire.utf8(s))
    private fun identity(name: String, role: Long): Wire.Identity {
        val seed = hash("lab/sign/$name")
        val x = hash("lab/box/$name")
        val ed = Ed25519PrivateKeyParameters(seed, 0).generatePublicKey().encoded
        return Wire.Identity(
            Wire.utf8(Wire.hex(Wire.hash(ed).copyOf(16))),
            role,
            hash("lab/instance/$name").copyOf(16),
            hash("lab/boot/$name"),
            ed,
            X25519PrivateKeyParameters(x, 0).generatePublicKey().encoded,
            seed,
            x,
        )
    }

    private fun codec() = Wire(
        Wire.utf8("0123456789abcdef0123456789abcdef"),
        hash("lab/account-domain"),
        hash("lab/member-head/1"),
        1,
    )

    private fun transportRoot() = File(System.getProperty("outcomeVectors")).parentFile
    private fun vectors(name: String) = Json.parseToJsonElement(File(transportRoot(), "wire/$name").readText())

    @Test
    fun frozenCryptoAndSnapshotVectors() {
        val corpus = vectors("vectors.json").jsonObject
        val a = identity(corpus.getValue("sender").jsonPrimitive.content, 1)
        val b = identity(corpus.getValue("receiver").jsonPrimitive.content, 2)
        val rows = corpus.getValue("rows").jsonArray
        assertEquals(27, rows.size)
        for (row in rows) {
            val r = row.jsonObject
            val opened = runCatching { codec().open(Wire.unhex(r.getValue("packet_hex").jsonPrimitive.content), a, b, now) }
            assertEquals(r.getValue("name").jsonPrimitive.content, r.getValue("accept").jsonPrimitive.boolean, opened.isSuccess)
            if (opened.isSuccess) {
                assertEquals(r.getValue("payload_hex").jsonPrimitive.content, Wire.hex(opened.getOrThrow().payload))
            }
        }
        val machine = identity("machine", 2)
        val client = identity("client", 1)
        val snapRows = vectors("capability-snapshot.json").jsonArray
        assertEquals(7, snapRows.size)
        for (row in snapRows) {
            val r = row.jsonObject
            val result = runCatching {
                val o = codec().open(Wire.unhex(r.getValue("packet_hex").jsonPrimitive.content), machine, client, now)
                Schema.known(2, 14, o.payload)
                PeerResponder.capabilityExpiry(o, now)
            }
            assertEquals(r.getValue("name").jsonPrimitive.content, r.getValue("accept").jsonPrimitive.boolean, result.isSuccess)
        }
    }

    @Test
    fun validatesFrozenSdpVectors() {
        val sdpDir = File(transportRoot(), "sdp")
        val rows = Json.parseToJsonElement(File(sdpDir, "manifest.json").readText()).jsonArray
        assertEquals(45, rows.size)
        for (row in rows) {
            val obj = row.jsonObject
            val file = obj.getValue("file").jsonPrimitive.content
            val accept = obj.getValue("accept").jsonPrimitive.boolean
            val raw = File(sdpDir, file).readBytes()
            val result = runCatching { Sdp.inspectSDP(raw) }
            assertEquals(file, accept, result.isSuccess)
        }
    }

    @Test
    fun replaysFrozenSignalingScenarios() {
        val c = codec()
        val source = File(transportRoot(), "signal/scenarios.tsv").readText().trim().split('\n')
        var gates = emptyList<Signal>()
        var batch: Sender.CandidateBatcher? = null
        var sender: Sender? = null
        var queuedRaw = ByteArray(0)
        var cases = 0
        var assertions = 0

        for (line in source) {
            val p = line.split('\t')
            val n = p.getOrNull(1)?.toIntOrNull() ?: 0
            when (p[0]) {
                "BODY" -> {
                    val ok = runCatching { Schema.known(p[1].toLong(), p[2].toLong(), Wire.unhex(p[3])) }.isSuccess
                    assertEquals("BODY line $assertions", p[4] == "1", ok)
                    assertions++
                }
                "SCHEMA" -> {
                    val result = runCatching { Wire.hex(Schema.normalize(Wire.unhex(p[2]), p[1])) }.getOrElse { "!" }
                    assertEquals("SCHEMA case $cases", p[3], result)
                    assertions++
                }
                "AUTH" -> gates[n].setAuthorization(now + p[2].toLong(), p[3] == "1", p[4].toLong())
                "SEND_AUTH" -> sender!!.setAuthorization(p[1].toLong(), p[2] == "1", p[3].toLong())
                "REPLY" -> {
                    assertEquals("REPLY case $cases", p[3] == "1", gates[n].replyAllowed(Wire.unhex(p[4]), now + p[2].toLong()))
                    assertions++
                }
                "CASE" -> {
                    val a = identity(p[2], p[3].toLong())
                    val b = identity(p[4], p[5].toLong())
                    val conn = Wire.hash(Wire.utf8("signal/${p[1]}")).copyOf(16)
                    gates = listOf(
                        Signal(c, a, b, conn, p[6].toLong(), now),
                        Signal(c, b, a, conn, p[6].toLong(), now),
                    )
                    cases++
                }
                "BATCH_CASE" -> {
                    batch = Sender.CandidateBatcher(p[2].toLong(), p[3].toLong(), p[4].toLong())
                    cases++
                }
                "BATCH_ADD" -> {
                    assertEquals("BATCH_ADD case $cases", p[4] == "1", batch!!.add(Wire.unhex(p[3]), p[2].toLong(), p[1].toLong(), 1L))
                    assertions++
                }
                "BATCH_FLUSH" -> {
                    val groups = batch!!.flush(p[1].toLong(), p[2] == "1", 2L, 1L)
                    assertEquals("BATCH_FLUSH sizes case $cases", p[3], groups.joinToString(",") { it.size.toString() })
                    val combined = groups.flatten().fold(ByteArray(0)) { acc, bytes -> acc + bytes }
                    assertEquals("BATCH_FLUSH hex case $cases", p[4], Wire.hex(combined))
                    assertions++
                }
                "DISPATCH" -> {
                    assertEquals("DISPATCH case $cases", p[3] == "1", gates[n].dispatch(Wire.unhex(p[4]), now + p[2].toLong()))
                    assertions++
                }
                "FORGET" -> gates[n].forgetPeer()
                "QUEUE" -> {
                    queuedRaw = Wire.unhex(p[2])
                    c.open(queuedRaw, gates[n].peer, gates[n].self, now)
                }
                "DELIVER" -> {
                    val ok = runCatching { gates[n].receive(queuedRaw, now + p[2].toLong()) }.isSuccess
                    assertEquals("DELIVER case $cases", p[3] == "1", ok)
                    assertions++
                }
                "SENDER_CASE" -> {
                    sender = Sender(p[2].toLong())
                    cases++
                }
                "SEND_ADD" -> {
                    assertEquals("SEND_ADD case $cases", p[5] == "1", sender!!.enqueue(Wire.unhex(p[4]), p[1].toLong(), p[2].toLong(), p[3].toLong()))
                    assertions++
                }
                "SEND_TICK" -> {
                    assertEquals("SEND_TICK case $cases", p[2].toInt(), sender!!.tick(p[1].toLong()).size)
                    assertions++
                }
                "SEND_REBIND" -> sender!!.rebind(p[1].toLong())
                "SEND_LIMIT" -> sender!!.limited(Wire.hash(Wire.unhex(p[3])), p[1].toLong(), p[2].toLong())
                "SEND_ADMIT" -> {
                    assertEquals("SEND_ADMIT case $cases", p[3] == "1", sender!!.admit(Wire.unhex(p[2]), p[1].toLong()))
                    assertions++
                }
                "SEND_PENDING" -> {
                    assertEquals("SEND_PENDING case $cases", p[1].toInt(), sender!!.pending.size)
                    assertions++
                }
                "BATCH_BOUND_FLUSH" -> {
                    val out = batch!!.flush(p[1].toLong(), p[2] == "1", p[3].toLong(), p[4].toLong())
                    assertTrue("BATCH_BOUND_FLUSH case $cases", out.isEmpty() && batch.pending.isEmpty())
                    assertions++
                }
                "POLICY" -> {
                    val ok = runCatching {
                        Wire.validatePolicyReply(
                            c.open(Wire.unhex(p[3]), gates[0].self, gates[1].self, now),
                            c.open(Wire.unhex(p[4]), gates[1].self, gates[0].self, now),
                            now + p[5].toLong(),
                        )
                    }.isSuccess
                    assertEquals("POLICY case $cases", p[2] == "1", ok)
                    assertions++
                }
                "RECORD" -> {
                    val which = p[2].toInt()
                    gates[which].tick(now + p[3].toLong())
                    val sent = c.open(Wire.unhex(p[4]), gates[which].self, gates[1 - which].self, now)
                    val ok = runCatching {
                        if (p[1] == "probe") gates[which].rememberProbe(sent)
                        else gates[which].rememberChallenge(sent)
                    }.isSuccess
                    assertEquals("RECORD case $cases", p[5] == "1", ok)
                    assertions++
                }
                "PROBE" -> gates[n].rememberProbe(c.open(Wire.unhex(p[2]), gates[n].self, gates[1 - n].self, now))
                "CHALLENGE" -> gates[n].rememberChallenge(c.open(Wire.unhex(p[2]), gates[n].self, gates[1 - n].self, now))
                "RX" -> {
                    val ok = runCatching { gates[n].receive(Wire.unhex(p[4]), now + p[2].toLong()) }.isSuccess
                    assertEquals("RX case $cases", p[3] == "1", ok)
                    assertions++
                }
                "LOCAL" -> {
                    val ok = runCatching { gates[n].setLocal(Wire.unhex(p[5]), p[3] == "1", p[2].toLong()) }.isSuccess
                    assertEquals("LOCAL case $cases", p[4] == "1", ok)
                    assertions++
                }
                "DTLS" -> gates[n].dtls = true
                "NEXT" -> gates[n].nextGeneration(p[2].toLong())
                "TICK" -> gates[n].tick(now + p[2].toLong())
                "EXPECT" -> {
                    val s = gates[n]
                    val actual = when (p[2]) {
                        "seen" -> s.seen.size.toLong()
                        "sdk" -> s.sdkActions.toLong()
                        "duplicates" -> s.duplicates.toLong()
                        "conflicts" -> s.conflicts.toLong()
                        "unsupported" -> s.unsupported.toLong()
                        "pending" -> s.pending.size.toLong()
                        "ready" -> if (s.ready()) 1L else 0L
                        "closed" -> if (s.closed) 1L else 0L
                        "generation" -> s.generation
                        "next" -> s.next
                        "stale" -> s.staleChallenges.toLong()
                        "boot" -> if (s.bootReady) 1L else 0L
                        "gap_ms" -> if (s.gapDeadline != 0L) s.gapDeadline - now else 0L
                        "peer_known" -> if (s.peer.boot.size == 32) 1L else 0L
                        "dispatches" -> s.dispatches.toLong()
                        else -> error("unknown EXPECT ${p[2]}")
                    }
                    assertEquals("EXPECT ${p[2]} case $cases", p[3].toLong(), actual)
                    assertions++
                }
                else -> error("unknown trace op ${p[0]}")
            }
        }
        assertEquals(150, cases)
        assertEquals(2103, assertions)
    }

    @Test
    fun keepsAccountsAndHeadsIsolatedBetweenCodecs() {
        val a = identity("client", 1)
        val b = identity("machine", 2)
        val scope = codec()
        val ctx = Wire.Context(a, b, hash("connection").copyOf(16), 1, 1, 1, now, 1, false)
        val raw = scope.seal(a, b, ctx, 31337, Wire.utf8("opaque"), null, null, null)
        assertEquals(31337L, scope.open(raw, a, b, now).type)
        val otherAccount = Wire(Wire.utf8("1123456789abcdef0123456789abcdef"), hash("lab/account-domain"), hash("lab/member-head/1"), 1)
        val otherEpoch = Wire(Wire.utf8("0123456789abcdef0123456789abcdef"), hash("lab/account-domain"), hash("lab/member-head/1"), 2)
        val otherHead = Wire(Wire.utf8("0123456789abcdef0123456789abcdef"), hash("lab/account-domain"), hash("different"), 1)
        val otherDomain = Wire(Wire.utf8("0123456789abcdef0123456789abcdef"), hash("different"), hash("lab/member-head/1"), 1)
        for (bad in listOf(otherAccount, otherEpoch, otherHead, otherDomain)) {
            assertTrue(runCatching { bad.open(raw, a, b, now) }.isFailure)
        }
    }

    private fun delivery(output: ByteArray, from: Wire.Identity, to: Wire.Identity): ByteArray {
        val f = Wire.parse(Wire.parse(output, 1)[3]!!.b, 4)
        return Wire.pb(Wire.b(1, from.id), Wire.b(2, to.id), Wire.u(3, f[2]!!.u), Wire.b(4, f[3]!!.b), Wire.b(5, f[4]!!.b))
    }

    @Test
    fun authenticatesBeforeUnsupportedAndRetainsOriginalReply() {
        val client = identity("client", 1)
        val machine = identity("machine", 2)
        val handler = PeerResponder(hash("lab/sign/client"))
        val conn = hash("connection").copyOf(16)
        fun receive(raw: ByteArray) = handler.receive(
            raw,
            "0123456789abcdef0123456789abcdef",
            hash("lab/account-domain"),
            hash("lab/member-head/1"),
            1,
            String(client.id),
            hash("lab/box/client"),
            String(machine.id),
            Ed25519PrivateKeyParameters(hash("lab/sign/machine"), 0).generatePublicKey().encoded,
            X25519PrivateKeyParameters(hash("lab/box/machine"), 0).generatePublicKey().encoded,
            now,
            now + 60000,
        )
        val pc = Wire.probeContext(machine, client, conn, 1, now)
        val probe = codec().seal(machine, client, pc, 1, Wire.pb(Wire.b(1, conn), Wire.b(2, machine.instance), Wire.b(3, machine.boot)), null, null, null)
        val reply = receive(probe)!!
        assertArrayEquals(reply, receive(probe))
        val challenge = codec().open(delivery(reply, client, machine), client, machine, now)
        val bound = Wire.Identity(client)
        bound.instance = challenge.ctx.senderInstance
        bound.boot = challenge.ctx.senderBoot
        val context = Wire.Context(machine, bound, conn, 1, 1, 1, now, 1, false)
        val unknown = codec().seal(machine, bound, context, 31337, byteArrayOf(1), null, null, null)
        val refused = receive(unknown)!!
        assertArrayEquals(refused, receive(unknown))
        val result = codec().open(delivery(refused, bound, machine), bound, machine, now)
        assertEquals(10L, result.type)
        assertArrayEquals(Wire.hash(unknown), Schema.fields(result.payload, "Unsupported")[4]!!.b)
        assertNull(receive(unknown.clone().also { it[it.lastIndex] = (it.last().toInt() xor 1).toByte() }))
        val restarted = PeerResponder(hash("lab/sign/client"))
        assertNull(
            restarted.receive(
                unknown,
                "0123456789abcdef0123456789abcdef",
                hash("lab/account-domain"),
                hash("lab/member-head/1"),
                1,
                String(client.id),
                hash("lab/box/client"),
                String(machine.id),
                Ed25519PrivateKeyParameters(hash("lab/sign/machine"), 0).generatePublicKey().encoded,
                X25519PrivateKeyParameters(hash("lab/box/machine"), 0).generatePublicKey().encoded,
                now,
                now + 60000,
            ),
        )
    }
}
