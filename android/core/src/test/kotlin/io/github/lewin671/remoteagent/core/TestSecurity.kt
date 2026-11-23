package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import com.google.protobuf.MessageLite
import io.github.lewin671.remoteagent.core.proto.Effect
import io.github.lewin671.remoteagent.core.proto.EffectBatch
import io.github.lewin671.remoteagent.core.proto.EntropyProvided
import io.github.lewin671.remoteagent.core.proto.Event as CoreProtoEvent
import io.github.lewin671.remoteagent.core.proto.LocalCalendar
import io.github.lewin671.remoteagent.core.proto.SecurityActionEmpty
import io.github.lewin671.remoteagent.core.proto.SecurityConfigEvent
import io.github.lewin671.remoteagent.core.proto.SecurityKeyAgreedEvent
import io.github.lewin671.remoteagent.core.proto.SecurityKeyEncapsulatedEvent
import io.github.lewin671.remoteagent.core.proto.SecurityKeyGeneratedEvent
import io.github.lewin671.remoteagent.core.proto.SecurityKeyKind
import io.github.lewin671.remoteagent.core.proto.SecurityKeySignedEvent
import io.github.lewin671.remoteagent.core.proto.SecurityOpRequestEvent
import io.github.lewin671.remoteagent.core.proto.SecurityOpResultEffect
import io.github.lewin671.remoteagent.core.proto.SecurityOpenEnvelopeOp
import io.github.lewin671.remoteagent.core.proto.SecurityOpenEnvelopeResult
import io.github.lewin671.remoteagent.core.proto.SecuritySealEnvelopeOp
import io.github.lewin671.remoteagent.core.proto.SecurityWorkflowActionEvent
import io.github.lewin671.remoteagent.corebind.Core
import io.github.lewin671.remoteagent.corebind.Corebind
import io.github.lewin671.remoteagent.proto.DeviceKind
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.Header
import io.github.lewin671.remoteagent.proto.PayloadKind
import java.io.ByteArrayOutputStream
import java.io.DataOutputStream
import java.io.File
import java.nio.ByteBuffer
import java.security.SecureRandom
import java.util.Base64
import java.util.UUID
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.atomic.AtomicBoolean
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import okhttp3.OkHttpClient

internal fun deviceIdForEdPublic(edPublic: ByteArray): String =
    sha256(edPublic).copyOf(16).hex()

internal data class FixtureRecord(
    val version: Int,
    val lease: ByteArray,
    val account: String,
    val domain: ByteArray,
    val genesis: ByteArray,
    val member: String,
    val xPrivate: ByteArray,
    val states: List<ByteArray>,
    val proofs: List<ByteArray?>,
)

private fun lpBytes(out: DataOutputStream, b: ByteArray) {
    out.writeInt(b.size)
    out.write(b)
}

private fun lpStr(out: DataOutputStream, s: String) =
    lpBytes(out, s.toByteArray(Charsets.UTF_8))

internal fun parseLeaseIssuedAt(lease: ByteArray): Long {
    if (lease.size < 86) return 0L
    return ByteBuffer.wrap(lease, 78, 8).long
}

internal fun parseStateContentEpochAndParent(state: ByteArray): Pair<Long, ByteArray> {
    // Wire state framing: u32 coreLen, then inside core: u32 ver, lp account, 32 domain, lp authorityId,
    // 32 recoveryEd, 32 parent, u64 epoch, u64 contentEpoch.
    val bb = ByteBuffer.wrap(state)
    val coreLen = bb.int
    require(coreLen > 0 && state.size >= 4 + coreLen) { "invalid state" }
    val ver = bb.int
    require(ver == 3) { "unexpected state version $ver" }
    val accLen = bb.int; bb.position(bb.position() + accLen)
    bb.position(bb.position() + 32) // domain
    val authLen = bb.int; bb.position(bb.position() + authLen)
    bb.position(bb.position() + 32) // recoveryEd
    val parent = ByteArray(32).also { bb.get(it) }
    bb.long // epoch
    val contentEpoch = bb.long
    return contentEpoch to parent
}

internal fun stateHashDigest(state: ByteArray): ByteArray {
    // ra3 state framing: lp coreBytes, u32 grantCount + grants..., lp prevSecret, lp sig.
    val bb = ByteBuffer.wrap(state)
    val coreLen = bb.int
    val coreBytes = ByteArray(coreLen).also { bb.get(it) }
    val grantStart = bb.position()
    val grantCount = bb.int
    repeat(grantCount) {
        val rLen = bb.int; bb.position(bb.position() + rLen)
        val eLen = bb.int; bb.position(bb.position() + eLen)
        val cLen = bb.int; bb.position(bb.position() + cLen)
    }
    val grantBytes = state.copyOfRange(grantStart, bb.position())
    val psLen = bb.int
    val prevSecret = ByteArray(psLen).also { bb.get(it) }
    val coreHash = sha256("ra3/core\u0000".toByteArray(), coreBytes)
    val psLenBytes = ByteBuffer.allocate(4).putInt(prevSecret.size).array()
    return sha256("ra3/state\u0000".toByteArray(), coreHash, grantBytes, psLenBytes, prevSecret)
}

internal fun encodeV4RecordBytes(
    account: String,
    domain: ByteArray,
    genesis: ByteArray,
    memberId: String,
    member: Boolean = true,
    server: String = "https://ra.example.com",
    accountPublicKey: ByteArray = ByteArray(0),
    states: List<ByteArray>,
    proofs: List<ByteArray?> = emptyList(),
    lease: ByteArray = ByteArray(0),
    floorMs: Long = parseLeaseIssuedAt(lease),
    contentKeys: List<Pair<Long, ByteArray>> = emptyList(),
    needsRecoveryRotation: Boolean = false,
    recoveryObjectsPending: Boolean = false,
): ByteArray {
    val bos = ByteArrayOutputStream()
    DataOutputStream(bos).use { s ->
        s.write("RA4REC\u0000".toByteArray(Charsets.US_ASCII))
        s.writeInt(4)
        lpStr(s, account)
        require(domain.size == 32 && genesis.size == 32)
        s.write(domain)
        s.write(genesis)
        lpStr(s, memberId)
        s.writeByte(if (member) 1 else 0)
        lpStr(s, server)
        lpBytes(s, accountPublicKey)
        s.writeInt(states.size)
        for (i in states.indices) {
            lpBytes(s, states[i])
            lpBytes(s, proofs.getOrNull(i) ?: ByteArray(0))
        }
        lpBytes(s, lease)
        s.writeLong(floorMs)
        s.writeInt(contentKeys.size)
        for ((ep, sec) in contentKeys) {
            require(sec.size == KEY_SIZE)
            s.writeLong(ep)
            s.write(sec)
        }
        s.writeByte(if (needsRecoveryRotation) 1 else 0)
        s.writeByte(if (recoveryObjectsPending) 1 else 0)
    }
    return bos.toByteArray()
}

internal fun encodeV4NotifyRecordBytes(
    account: String,
    domain: ByteArray,
    genesis: ByteArray,
    headState: ByteArray,
    purposeKey: ByteArray,
): ByteArray {
    require(domain.size == 32 && genesis.size == 32 && purposeKey.size == KEY_SIZE)
    val bos = ByteArrayOutputStream()
    DataOutputStream(bos).use { s ->
        s.write("RA4NTF\u0000".toByteArray(Charsets.US_ASCII))
        s.writeInt(4)
        lpStr(s, account)
        s.write(domain)
        s.write(genesis)
        lpBytes(s, headState)
        s.write(purposeKey)
    }
    return bos.toByteArray()
}

internal fun deriveNotifyPurposeKey(
    contentSecret: ByteArray,
    account: String,
    domain: ByteArray,
    contentEpoch: Long,
): ByteArray {
    val bos = ByteArrayOutputStream()
    DataOutputStream(bos).use { s ->
        s.write("ra3/purpose-key\u0000".toByteArray(Charsets.US_ASCII))
        lpStr(s, account)
        s.write(domain)
        s.writeLong(contentEpoch)
        s.writeInt(10) // Purpose Notify
    }
    return Corebind.hkdfSha256(contentSecret, ByteArray(0), bos.toByteArray(), KEY_SIZE)
}

internal fun requestIdentityForTest(
    account: String,
    domain: ByteArray,
    opened: SecurityOpenEnvelopeResult,
    unpacked: ByteArray,
): Pair<ByteArray, ByteArray> {
    val keyBos = ByteArrayOutputStream()
    DataOutputStream(keyBos).use { s ->
        s.write("ra4/request-key\u0000".toByteArray(Charsets.US_ASCII))
        lpStr(s, account)
        s.write(domain)
        lpStr(s, opened.sender)
        s.writeInt(opened.purpose)
        lpStr(s, opened.machineId)
        lpStr(s, opened.sessionId)
        lpStr(s, opened.requestId)
        s.writeLong(opened.seq)
    }
    val key = sha256(keyBos.toByteArray())
    val kind = when (opened.purpose) {
        14, 15 -> 8 // TerminalResize / TerminalHistory map to PayloadKind 8
        else -> opened.purpose
    }
    val payloadBos = ByteArrayOutputStream()
    DataOutputStream(payloadBos).use { s ->
        s.write("ra4/authorized-payload\u0000".toByteArray(Charsets.US_ASCII))
        lpBytes(s, key)
        s.writeInt(kind)
        s.writeLong(opened.deadlineMs)
        lpBytes(s, unpacked)
    }
    val payload = sha256(payloadBos.toByteArray())
    return key to payload
}

/** Synchronous Go Core runner backed by [MemorySecurityStoreV4] for unit tests & fake daemons. */
internal class CoreMemoryBridge(
    private val store: MemorySecurityStoreV4,
    private val server: String,
    private val deviceId: String,
    private val accountId: String,
    private val clock: () -> Long = System::currentTimeMillis,
) : AutoCloseable {
    private val rng = SecureRandom()
    private val core: Core = Corebind.new_()
    private val closed = AtomicBoolean(false)
    private var opSeq = 1L

    init {
        step(
            CoreProtoEvent.newBuilder()
                .setNowMs(clock())
                .setSecurityConfig(
                    SecurityConfigEvent.newBuilder()
                        .setAccount(accountId)
                        .setDeviceId(deviceId)
                        .setServer(server)
                        .setInlineCrypto(true)
                        .setForeground(true),
                )
                .build(),
        )
        step(
            CoreProtoEvent.newBuilder()
                .setNowMs(clock())
                .setClockUpdated(LocalCalendar.newBuilder().setNowMs(clock()))
                .build(),
        )
        step(
            CoreProtoEvent.newBuilder()
                .setNowMs(clock())
                .setSecurityAction(
                    SecurityWorkflowActionEvent.newBuilder()
                        .setActionId(1L)
                        .setBootstrap(SecurityActionEmpty.getDefaultInstance()),
                )
                .build(),
        )
    }

    private fun buildEntropy(minIds: Int, minFloats: Int, minRandomBytes: Int): EntropyProvided {
        val b = EntropyProvided.newBuilder()
        repeat(minIds.coerceAtLeast(16)) { b.addRandomIds(UUID.randomUUID().toString()) }
        repeat(minFloats.coerceAtLeast(16)) {
            b.addUnitFloats((rng.nextInt().toLong() and 0xFFFFFFFFL).toDouble() / 4294967296.0)
        }
        val nBytes = minRandomBytes.coerceAtLeast(4096)
        b.setRandomBytes(ByteString.copyFrom(ByteArray(nBytes).also { rng.nextBytes(it) }))
        return b.build()
    }

    private fun step(initial: CoreProtoEvent): List<Effect> {
        check(!closed.get()) { "CoreMemoryBridge closed" }
        val out = ArrayList<Effect>()
        val queue = ArrayDeque<CoreProtoEvent>()
        queue.addLast(initial)
        while (queue.isNotEmpty()) {
            val next = queue.removeFirst()
            val raw: ByteArray? = core.dispatch(next.toByteArray())
            val batch = if (raw == null || raw.isEmpty()) EffectBatch.getDefaultInstance() else EffectBatch.parseFrom(raw)
            for (eff in batch.effectsList) {
                when (eff.effectCase) {
                    Effect.EffectCase.REQUEST_ENTROPY -> {
                        val req = eff.requestEntropy
                        queue.addLast(
                            CoreProtoEvent.newBuilder()
                                .setNowMs(clock())
                                .setEntropyProvided(
                                    buildEntropy(req.minIds, req.minFloats, req.minRandomBytes),
                                )
                                .build(),
                        )
                    }
                    Effect.EffectCase.SECURITY_KEY_GENERATE -> {
                        val g = eff.securityKeyGenerate
                        val pub = store.generateKey(g.keyKind, g.slot, g.persist)
                        queue.addLast(
                            CoreProtoEvent.newBuilder()
                                .setNowMs(clock())
                                .setSecurityKeyGenerated(
                                    SecurityKeyGeneratedEvent.newBuilder()
                                        .setOpId(g.opId)
                                        .setPublicKey(ByteString.copyFrom(pub)),
                                )
                                .build(),
                        )
                    }
                    Effect.EffectCase.SECURITY_KEY_SIGN -> {
                        val s = eff.securityKeySign
                        val ev = runCatching {
                            val sig = store.signKey(s.keyKind, s.slot, s.message.toByteArray())
                            SecurityKeySignedEvent.newBuilder()
                                .setOpId(s.opId)
                                .setSignature(ByteString.copyFrom(sig))
                                .build()
                        }.getOrElse { e ->
                            SecurityKeySignedEvent.newBuilder()
                                .setOpId(s.opId)
                                .setError(e.message ?: "sign error")
                                .build()
                        }
                        queue.addLast(CoreProtoEvent.newBuilder().setNowMs(clock()).setSecurityKeySigned(ev).build())
                    }
                    Effect.EffectCase.SECURITY_KEY_AGREE -> {
                        val a = eff.securityKeyAgree
                        val ev = runCatching {
                            val sh = store.agreeKey(a.keyKind, a.slot, a.peerPublic.toByteArray())
                            SecurityKeyAgreedEvent.newBuilder()
                                .setOpId(a.opId)
                                .setShared(ByteString.copyFrom(sh))
                                .build()
                        }.getOrElse { e ->
                            SecurityKeyAgreedEvent.newBuilder()
                                .setOpId(a.opId)
                                .setError(e.message ?: "agree error")
                                .build()
                        }
                        queue.addLast(CoreProtoEvent.newBuilder().setNowMs(clock()).setSecurityKeyAgreed(ev).build())
                    }
                    Effect.EffectCase.SECURITY_KEY_ENCAPSULATE -> {
                        val e = eff.securityKeyEncapsulate
                        val pairs = store.encapsulate(e.recipientsList)
                        queue.addLast(
                            CoreProtoEvent.newBuilder()
                                .setNowMs(clock())
                                .setSecurityKeyEncapsulated(
                                    SecurityKeyEncapsulatedEvent.newBuilder()
                                        .setOpId(e.opId)
                                        .addAllPairs(pairs),
                                )
                                .build(),
                        )
                    }
                    Effect.EffectCase.SECURITY_KEY_DISCARD -> {
                        store.discardKey(eff.securityKeyDiscard.keyKind, eff.securityKeyDiscard.slot)
                    }
                    Effect.EffectCase.SECURITY_STORAGE -> {
                        val rep = store.executeStorage(eff.securityStorage)
                        queue.addLast(
                            CoreProtoEvent.newBuilder()
                                .setNowMs(clock())
                                .setSecurityStorageResult(rep)
                                .build(),
                        )
                    }
                    else -> out += eff
                }
            }
        }
        return out
    }

    fun syncClock() {
        step(
            CoreProtoEvent.newBuilder()
                .setNowMs(clock())
                .setClockUpdated(LocalCalendar.newBuilder().setNowMs(clock()))
                .build(),
        )
    }

    private fun runOp(configure: SecurityOpRequestEvent.Builder.() -> Unit): SecurityOpResultEffect {
        syncClock()
        val id = opSeq++
        val effs = step(
            CoreProtoEvent.newBuilder()
                .setNowMs(clock())
                .setSecurityOpRequest(SecurityOpRequestEvent.newBuilder().setOpId(id).apply(configure))
                .build(),
        )
        val res = effs.lastOrNull { it.hasSecurityOpResult() && it.securityOpResult.opId == id }
            ?.securityOpResult
            ?: throw SecurityFailure("invalid")
        if (res.error.isNotEmpty()) {
            throw SecurityFailure(securityErrorCodeKey(res.structuredErrorCode), res.error)
        }
        return res
    }

    fun seal(
        purpose: Int,
        machine: String = "",
        session: String = "",
        request: String = "",
        seq: Long = 0L,
        deadline: Long = 0L,
        plain: ByteArray,
        useSessionSeed: Boolean = false,
        overrideSessionSeed: ByteArray? = null,
    ): Envelope {
        val res = runOp {
            setSealEnvelope(
                SecuritySealEnvelopeOp.newBuilder()
                    .setPurpose(purpose)
                    .setMachineId(machine)
                    .setSessionId(session)
                    .setRequestId(request)
                    .setSeq(seq)
                    .setDeadlineMs(deadline)
                    .setPlaintext(ByteString.copyFrom(plain))
                    .setUseSessionSeed(useSessionSeed || overrideSessionSeed != null)
                    .apply {
                        if (overrideSessionSeed != null) {
                            setOverrideSessionSeed(ByteString.copyFrom(overrideSessionSeed))
                        }
                    },
            )
        }
        return res.sealed.envelope
    }

    fun open(
        env: Envelope,
        expectedPurpose: Int = 0,
        history: Boolean = false,
        overrideSessionSeed: ByteArray? = null,
    ): Pair<SecurityOpenEnvelopeResult, ByteArray> {
        val res = runOp {
            setOpenEnvelope(
                SecurityOpenEnvelopeOp.newBuilder()
                    .setEnvelope(env)
                    .setExpectedPurpose(expectedPurpose)
                    .setHistory(history)
                    .apply {
                        if (overrideSessionSeed != null) {
                            setOverrideSessionSeed(ByteString.copyFrom(overrideSessionSeed))
                        }
                    },
            )
        }
        return res.opened to res.opened.plaintext.toByteArray()
    }

    fun headerFromOpened(opened: SecurityOpenEnvelopeResult): Header {
        val kind = PayloadKind.forNumber(opened.purpose) ?: PayloadKind.PAYLOAD_KIND_UNSPECIFIED
        return Header.newBuilder()
            .setVersion(ENVELOPE_VERSION)
            .setKind(kind)
            .setMachineId(opened.machineId)
            .setSessionId(opened.sessionId)
            .setRequestId(opened.requestId)
            .setSeq(opened.seq)
            .setDeadlineMs(opened.deadlineMs)
            .setSender(opened.sender)
            .build()
    }

    override fun close() {
        if (closed.compareAndSet(false, true)) {
            core.close()
        }
    }
}

/**
 * Actual Go state chain and envelopes for synthetic cross-language fixtures. The separate process
 * has no account files, keychain or production access.
 */
class TestSecurity private constructor(private val process: Process, val serverUrl: String) : AutoCloseable {
    private val input = process.outputStream.bufferedWriter()
    private val output = process.inputStream.bufferedReader()
    val stores = ConcurrentHashMap<String, MemorySecurityStoreV4>()
    val deviceSeeds = ConcurrentHashMap<String, ByteArray>()
    val xPrivates = ConcurrentHashMap<String, ByteArray>()
    val accountRoots = ConcurrentHashMap<String, ByteArray>()
    val domains = ConcurrentHashMap<String, ByteArray>()

    @Synchronized
    fun call(op: String, vararg fields: Pair<String, JsonElement>): JsonObject {
        input.write(
            buildJsonObject {
                put("Op", JsonPrimitive(op))
                for ((key, value) in fields) put(key, value)
            }.toString(),
        )
        input.newLine()
        input.flush()
        val line = output.readLine() ?: error("synthetic security fixture exited")
        val reply = Json.parseToJsonElement(line).jsonObject
        check(reply["Error"] == null) { "synthetic security fixture: ${reply["Error"]}" }
        return reply
    }

    fun bytes(reply: JsonObject, key: String): ByteArray =
        Base64.getDecoder().decode(reply[key]!!.jsonPrimitive.content)

    internal fun parseRecord(reply: JsonObject): FixtureRecord {
        val o = reply["Record"]!!.jsonObject
        val states = o["states"]!!.jsonArray.map { Base64.getDecoder().decode(it.jsonPrimitive.content) }
        val proofs = o["proofs"]!!.jsonArray.map {
            if (it is JsonNull) null else Base64.getDecoder().decode(it.jsonPrimitive.content)
        }
        return FixtureRecord(
            version = o["version"]!!.jsonPrimitive.content.toInt(),
            lease = Base64.getDecoder().decode(o["lease"]!!.jsonPrimitive.content),
            account = o["account"]!!.jsonPrimitive.content,
            domain = Base64.getDecoder().decode(o["domain"]!!.jsonPrimitive.content),
            genesis = Base64.getDecoder().decode(o["genesis"]!!.jsonPrimitive.content),
            member = o["member"]!!.jsonPrimitive.content,
            xPrivate = Base64.getDecoder().decode(o["x"]!!.jsonPrimitive.content),
            states = states,
            proofs = proofs,
        )
    }

    internal fun seedMemberStore(rec: FixtureRecord, edSeed: ByteArray, store: MemorySecurityStoreV4 = stores.getOrPut(rec.member) { MemorySecurityStoreV4() }): MemorySecurityStoreV4 {
        deviceSeeds[rec.member] = edSeed.copyOf()
        xPrivates[rec.member] = rec.xPrivate.copyOf()
        domains[rec.account] = rec.domain.copyOf()
        val edPub = Corebind.ed25519PublicKey(edSeed)
        val xPub = Corebind.x25519PublicKey(rec.xPrivate)
        val recBytes = encodeV4RecordBytes(
            account = rec.account,
            domain = rec.domain,
            genesis = rec.genesis,
            memberId = rec.member,
            member = true,
            server = serverUrl,
            accountPublicKey = accountRoots[rec.account]?.let { Corebind.ed25519PublicKey(it) } ?: ByteArray(0),
            states = rec.states,
            proofs = rec.proofs,
            lease = rec.lease,
        )
        store.seedTestRecord(edSeed, edPub, rec.xPrivate, xPub, recBytes)
        return store
    }

    internal fun enroll(seed: ByteArray, kind: DeviceKind): Pair<FixtureRecord, ByteArray> {
        val reply = call("enroll", "Seed" to blob(seed), "Kind" to JsonPrimitive(kind.number))
        return parseRecord(reply) to bytes(reply, "State")
    }

    internal fun record(member: String): FixtureRecord =
        parseRecord(call("record", "Member" to JsonPrimitive(member)))

    fun syncStoresForAll() {
        for ((memberId, store) in stores.entries) {
            val seed = deviceSeeds[memberId] ?: continue
            runCatching {
                val rec = record(memberId)
                seedMemberStore(rec, seed, store)
            }
        }
    }

    fun remove(targets: List<String>): ByteArray {
        val reply = call("remove", "Targets" to JsonArray(targets.map { JsonPrimitive(it) }))
        return bytes(reply, "State")
    }

    fun seal(account: Account, h: Header, plain: ByteArray, seed: ByteArray? = null): Envelope {
        val reply = call(
            "seal",
            "Member" to JsonPrimitive(account.deviceId),
            "Header" to blob(h.toByteArray()),
            "Plain" to blob(plain),
            "SessionSeed" to blob(seed ?: byteArrayOf()),
        )
        return Envelope.parseFrom(bytes(reply, "Envelope"))
    }

    fun seal(account: Account, h: Header, message: MessageLite, seed: ByteArray? = null) =
        seal(account, h, message.toByteArray(), seed)

    fun open(account: Account, env: Envelope, seed: ByteArray? = null): Pair<Header, ByteArray> {
        val reply = call(
            "open",
            "Member" to JsonPrimitive(account.deviceId),
            "Envelope" to blob(env.toByteArray()),
            "SessionSeed" to blob(seed ?: byteArrayOf()),
        )
        return Header.parseFrom(bytes(reply, "Header")) to bytes(reply, "Plain")
    }

    override fun close() {
        input.close()
        process.waitFor(5, java.util.concurrent.TimeUnit.SECONDS)
        process.destroyForcibly()
        stores.clear()
        deviceSeeds.clear()
        xPrivates.clear()
        accountRoots.clear()
        domains.clear()
    }

    companion object {
        private val all = ConcurrentHashMap<String, TestSecurity>()

        fun blob(b: ByteArray) = JsonPrimitive(Base64.getEncoder().encodeToString(b))

        fun get(server: String) = all[server] ?: error("synthetic authority was not initialized")

        fun find(server: String) = all[server]

        fun remove(server: String) {
            all.remove(server)?.close()
        }

        /** Starts the fixture on the account's authority client identity. */
        fun initialize(
            account: Account,
            edSeed: ByteArray,
            rootSeed: ByteArray? = null,
            recovery: ByteArray? = null,
        ): Pair<TestSecurity, ByteArray> {
            check(!all.containsKey(account.server))
            val bin = File(
                System.getProperty("raSecurityFixture")
                    ?: error("security fixture binary missing"),
            )
            val process = ProcessBuilder(bin.absolutePath)
                .redirectError(ProcessBuilder.Redirect.INHERIT)
                .start()
            val fixture = TestSecurity(process, account.server)
            val accountId = account.accountId.ifEmpty {
                rootSeed?.let { deviceIdForEdPublic(Corebind.ed25519PublicKey(it)) } ?: account.deviceId
            }
            if (rootSeed != null) {
                fixture.accountRoots[accountId] = rootSeed.copyOf()
            }
            val reply = fixture.call(
                "init",
                "Account" to JsonPrimitive(accountId),
                "Seed" to blob(edSeed),
                "Recovery" to blob(recovery ?: byteArrayOf()),
            )
            val rec = fixture.parseRecord(reply)
            fixture.seedMemberStore(rec, edSeed)
            all[account.server] = fixture
            return fixture to fixture.bytes(reply, "Genesis")
        }
    }
}

class MemoryTokens(private val delegate: SecurityStoreV4 = MemorySecurityStoreV4()) : SecurityStoreV4 by delegate {
    var saved: LoginToken? = null
    var reads = 0
    var clears = 0

    override fun loadLoginToken(server: String, deviceId: String): LoginToken? {
        reads++
        return saved
    }

    override fun saveLoginToken(server: String, deviceId: String, token: LoginToken) {
        saved = token
    }

    override fun clearLoginToken(server: String, deviceId: String) {
        clears++
        saved = null
    }
}

fun fixtureRemote(
    account: Account,
    http: OkHttpClient,
    cache: CipherCache? = null,
    log: (String) -> Unit = {},
    clock: () -> Long = System::currentTimeMillis,
    frameQueue: Int = 1024,
    historyIdleMs: Long = 60000,
    historyIdleCount: Int = 8,
    historyIdleBytes: Long = 16L * 1024 * 1024,
    streamOpenMs: Long = 15000,
    linkGraceMs: Long = 10000,
    securityStore: SecurityStoreV4? = null,
    onSessionRemoved: suspend (String) -> Unit = {},
): Remote = Remote(
    account = account,
    http = http,
    cache = cache,
    log = log,
    clock = clock,
    frameQueue = frameQueue,
    historyIdleMs = historyIdleMs,
    historyIdleCount = historyIdleCount,
    historyIdleBytes = historyIdleBytes,
    streamOpenMs = streamOpenMs,
    linkGraceMs = linkGraceMs,
    securityStore = securityStore
        ?: TestSecurity.find(account.server)?.stores?.get(account.deviceId)
        ?: MemorySecurityStoreV4(),
    onSessionRemoved = onSessionRemoved,
)
