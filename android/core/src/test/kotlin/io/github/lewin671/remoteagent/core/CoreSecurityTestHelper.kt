package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.proto.*
import io.github.lewin671.remoteagent.core.proto.Event as CoreEvent
import io.github.lewin671.remoteagent.corebind.Core
import io.github.lewin671.remoteagent.corebind.Corebind
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.GetSecurityStatesResponse
import io.github.lewin671.remoteagent.proto.Header
import io.github.lewin671.remoteagent.proto.PayloadKind
import java.security.SecureRandom
import java.util.concurrent.atomic.AtomicBoolean

/**
 * Test-only synchronous bridge driving `clientcore/security` via `Corebind.new_()` + `AuthoritySecurity`
 * so Android test fixtures and security tests exercise Go's real gate without Kotlin crypto copies.
 */
internal class CoreSecurityBridge(
    private val store: DeviceSecurity.Storage,
    private val memberID: String,
    private val signer: SigningKey,
    expectedAccount: String? = null,
    private val clock: () -> Long = System::currentTimeMillis,
    fetchStates: suspend (afterEpoch: ULong) -> GetSecurityStatesResponse = { error("no state pull expected") },
) : AutoCloseable {
    private data class RecordSnapshot(
        val account: String,
        val chainSize: Int,
        val headEpoch: ULong,
        val headHash: ByteArray,
        val floor: ULong,
        val leaseBytes: ByteArray?,
        val proofsSize: Int,
        val cachedKeyCount: Int,
    ) {
        fun sameAs(other: RecordSnapshot?): Boolean =
            other != null &&
                account == other.account &&
                chainSize == other.chainSize &&
                headEpoch == other.headEpoch &&
                headHash.contentEquals(other.headHash) &&
                floor == other.floor &&
                ((leaseBytes == null && other.leaseBytes == null) ||
                    (leaseBytes != null && other.leaseBytes != null && leaseBytes.contentEquals(other.leaseBytes))) &&
                proofsSize == other.proofsSize &&
                cachedKeyCount == other.cachedKeyCount
    }

    val authority =
        AuthoritySecurity(
            store = store,
            memberID = memberID,
            expectedAccount = expectedAccount,
            clock = clock,
            fetchStates = fetchStates,
        )

    private val rng = SecureRandom()
    private val core: Core = Corebind.new_()
    private val closed = AtomicBoolean(false)
    private var configured = false
    private var opSeq = 1L
    private var pushSeq = 1L
    private var lastSnapshot: RecordSnapshot? = null
    @Volatile private var latestXPrivate: ByteArray? = null

    private val executor =
        SecurityExecutor(
            signer = { signer },
            xPrivate = { latestXPrivate ?: runCatching { store.readSecurity().peerBoxKey() }.getOrNull() },
        )

    private fun buildEntropy(minIds: Int, minFloats: Int, minRandomBytes: Int): EntropyProvided {
        val b = EntropyProvided.newBuilder()
        val idBytes = ByteArray(16)
        repeat(minIds.coerceAtLeast(0)) {
            rng.nextBytes(idBytes)
            b.addRandomIds(idBytes.hex())
        }
        repeat(minFloats.coerceAtLeast(0)) {
            b.addUnitFloats(rng.nextDouble())
        }
        if (minRandomBytes > 0) {
            val raw = ByteArray(minRandomBytes)
            rng.nextBytes(raw)
            b.setRandomBytes(ByteString.copyFrom(raw))
        }
        return b.build()
    }

    private fun step(initial: CoreEvent): List<Effect> {
        check(!closed.get()) { "CoreSecurityBridge closed" }
        val out = ArrayList<Effect>()
        val queue = ArrayDeque<CoreEvent>()
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
                            CoreEvent.newBuilder()
                                .setEntropyProvided(
                                    buildEntropy(
                                        maxOf(req.minIds, 32),
                                        maxOf(req.minFloats, 32),
                                        maxOf(req.minRandomBytes, 4096),
                                    ),
                                )
                                .build(),
                        )
                    }
                    Effect.EffectCase.SECURITY_KEY_SIGN -> {
                        val signed = executor.keySign(eff.securityKeySign)
                        queue.addLast(CoreEvent.newBuilder().setSecurityKeySigned(signed).build())
                    }
                    Effect.EffectCase.SECURITY_KEY_AGREE -> {
                        val agreed = executor.keyAgree(eff.securityKeyAgree)
                        queue.addLast(CoreEvent.newBuilder().setSecurityKeyAgreed(agreed).build())
                    }
                    else -> out += eff
                }
            }
        }
        return out
    }

    @Synchronized
    private fun prepareForOp() {
        val record = authority.record()
        latestXPrivate = runCatching { record.peerBoxKey() }.getOrNull()
        if (!configured) {
            step(
                CoreEvent.newBuilder()
                    .setEntropyProvided(buildEntropy(64, 64, 4096))
                    .build(),
            )
            step(
                CoreEvent.newBuilder()
                    .setSecurityConfig(
                        SecurityConfigEvent.newBuilder()
                            .setAccount(record.pin.account)
                            .setDeviceId(memberID)
                            .setInlineCrypto(true),
                    )
                    .build(),
            )
            configured = true
        }
        step(
            CoreEvent.newBuilder()
                .setClockUpdated(buildLocalCalendar(clock()))
                .build(),
        )
        syncRecordLocked(record)
    }

    @Synchronized
    fun syncRecord(record: DeviceSecurity.Record = authority.record()) {
        latestXPrivate = runCatching { record.peerBoxKey() }.getOrNull()
        if (!configured) {
            step(
                CoreEvent.newBuilder()
                    .setEntropyProvided(buildEntropy(64, 64, 1024))
                    .build(),
            )
            step(
                CoreEvent.newBuilder()
                    .setSecurityConfig(
                        SecurityConfigEvent.newBuilder()
                            .setAccount(record.pin.account)
                            .setDeviceId(memberID)
                            .setInlineCrypto(true),
                    )
                    .build(),
            )
            configured = true
        }
        syncRecordLocked(record)
    }

    private fun syncRecordLocked(record: DeviceSecurity.Record) {
        val head = record.head
        val headHash = DeviceSecurity.stateHash(head)
        val lease = record.lease?.takeIf { it.head.contentEquals(headHash) }
        val leaseBytes = lease?.bytes()
        val cachedKeys = record.cachedKeys()
        val snap =
            RecordSnapshot(
                account = record.pin.account,
                chainSize = record.chain.size,
                headEpoch = head.core.epoch,
                headHash = headHash,
                floor = record.floor,
                leaseBytes = leaseBytes,
                proofsSize = record.proofs.size,
                cachedKeyCount = cachedKeys.size,
            )
        if (snap.sameAs(lastSnapshot)) return
        val b =
            SecurityRecordEvent.newBuilder()
                .setOpId(0L)
                .setPushSeq(pushSeq++)
                .setNetworkGeneration(1L)
                .setAccount(record.pin.account)
                .setDomain(ByteString.copyFrom(record.pin.domain))
                .setGenesis(ByteString.copyFrom(record.pin.genesis))
                .setFloorMs(record.floor.toLong())
                .setRecoveryPending(authority.recoveryPending)
                .setRecoveryReadyMs(authority.recoveryReadyMs)
                .setRecoveryObjectsPending(false)
        for (s in record.chain) {
            b.addStates(ByteString.copyFrom(DeviceSecurity.stateBytes(s)))
            val proof = record.proofs[s.core.epoch]
            b.addRecoveryProofs(if (proof == null) ByteString.EMPTY else ByteString.copyFrom(proof))
        }
        if (leaseBytes != null) {
            b.setLease(ByteString.copyFrom(leaseBytes))
        }
        val headContentEpoch = head.core.contentEpoch
        for ((ep, sec) in cachedKeys) {
            if (ep > 0uL && ep < headContentEpoch && sec.size == KEY_SIZE) {
                b.addContentKeys(
                    SecurityContentKey.newBuilder()
                        .setContentEpoch(ep.toLong())
                        .setSecret(ByteString.copyFrom(sec)),
                )
            }
        }
        step(CoreEvent.newBuilder().setSecurityRecord(b).build())
        lastSnapshot = snap
    }

    private fun throwOpError(res: SecurityOpResultEffect): Nothing {
        when (res.errorCode) {
            "unsupported_history" -> throw UnsupportedHistoryException()
            "recovery_unavailable" -> throw DeviceSecurity.Failure("unavailable")
            "stale" ->
                if (res.error.contains("lease expired")) throw DeviceSecurity.Failure("lease")
                else throw DeviceSecurity.Failure("stale")
            "clock", "untrusted", "capacity" -> throw DeviceSecurity.Failure(res.errorCode)
            else -> throw DeviceSecurity.Failure(res.errorCode.ifEmpty { "invalid" })
        }
    }

    @Synchronized
    private fun runOp(configure: SecurityOpRequestEvent.Builder.() -> Unit): SecurityOpResultEffect {
        prepareForOp()
        val id = opSeq++
        val req = SecurityOpRequestEvent.newBuilder().setOpId(id).apply(configure).build()
        val effs = step(CoreEvent.newBuilder().setSecurityOpRequest(req).build())
        val res =
            effs.lastOrNull { it.hasSecurityOpResult() && it.securityOpResult.opId == id }
                ?.securityOpResult
                ?: throw DeviceSecurity.Failure("invalid")
        if (res.error.isNotEmpty()) throwOpError(res)
        return res
    }

    suspend fun sync(budgetMs: Long = Long.MAX_VALUE): DeviceSecurity.Record =
        authority.sync(budgetMs).also { syncRecord(it) }

    fun install(
        states: List<DeviceSecurity.State>,
        proofs: List<ByteArray?> = emptyList(),
        lease: HeadLease? = null,
    ) {
        authority.install(states, proofs, lease)
        syncRecord(authority.record())
    }

    fun record(): DeviceSecurity.Record = authority.record()
    fun chain(): List<DeviceSecurity.State> = authority.chain()
    fun signingTime(wall: Long): Long = authority.signingTime(wall)

    fun seal(
        purpose: DeviceSecurity.Purpose,
        machine: String = "",
        session: String = "",
        request: String = "",
        seq: ULong = 0uL,
        deadline: ULong = 0uL,
        plain: ByteArray,
        seed: ByteArray? = null,
    ): Envelope {
        val res =
            runOp {
                setSealEnvelope(
                    SecuritySealEnvelopeOp.newBuilder()
                        .setPurpose(purpose.value.toInt())
                        .setMachineId(machine)
                        .setSessionId(session)
                        .setRequestId(request)
                        .setSeq(seq.toLong())
                        .setDeadlineMs(deadline.toLong())
                        .setPlaintext(ByteString.copyFrom(plain))
                        .setUseSessionSeed(purpose.sessionKey)
                        .apply {
                            if (purpose.sessionKey && seed != null) {
                                setOverrideSessionSeed(ByteString.copyFrom(seed))
                            }
                        },
                )
            }
        if (!res.hasSealed()) throwOpError(res)
        return res.sealed.envelope
    }

    fun sealCommand(
        machine: String,
        session: String,
        request: String,
        deadline: ULong,
        plain: ByteArray,
    ): Pair<Envelope, Pair<ByteArray, ByteArray>> {
        val res =
            runOp {
                setSealEnvelope(
                    SecuritySealEnvelopeOp.newBuilder()
                        .setPurpose(DeviceSecurity.Purpose.COMMAND.value.toInt())
                        .setMachineId(machine)
                        .setSessionId(session)
                        .setRequestId(request)
                        .setDeadlineMs(deadline.toLong())
                        .setPlaintext(ByteString.copyFrom(plain))
                        .setUseSessionSeed(false),
                )
            }
        if (!res.hasSealed()) throwOpError(res)
        val s = res.sealed
        return s.envelope to (s.requestKeyHash.toByteArray() to s.payloadHash.toByteArray())
    }

    fun open(
        env: Envelope,
        expectedPurpose: DeviceSecurity.Purpose? = null,
        machine: String = "",
        session: String = "",
        request: String = "",
        seq: ULong = 0uL,
        history: Boolean = false,
        seed: ByteArray? = null,
    ): Pair<SecurityOpenEnvelopeResult, ByteArray> {
        val res =
            runOp {
                setOpenEnvelope(
                    SecurityOpenEnvelopeOp.newBuilder()
                        .setEnvelope(env)
                        .setExpectedPurpose(expectedPurpose?.value?.toInt() ?: 0)
                        .setMachineId(machine)
                        .setSessionId(session)
                        .setRequestId(request)
                        .setSeq(seq.toLong())
                        .setHistory(history)
                        .apply {
                            if (seed != null) setOverrideSessionSeed(ByteString.copyFrom(seed))
                        },
                )
            }
        if (!res.hasOpened()) throwOpError(res)
        val opened = res.opened
        return opened to opened.plaintext.toByteArray()
    }

    fun openHistory(env: Envelope, seed: ByteArray? = null): Pair<SecurityOpenEnvelopeResult, ByteArray> =
        open(env, history = true, seed = seed)

    fun <T> openMessage(
        env: Envelope,
        history: Boolean,
        seed: ByteArray? = null,
        parse: (ByteArray) -> T,
    ): Pair<SecurityOpenEnvelopeResult, T> {
        val (opened, plain) = open(env, history = history, seed = seed)
        return opened to parse(plain)
    }

    fun headerFromOpened(opened: SecurityOpenEnvelopeResult): Header {
        val purpose = DeviceSecurity.Purpose.entries.first { it.value == opened.purpose.toUInt() }
        return Header.newBuilder()
            .setVersion(DeviceSecurity.VERSION)
            .setKindValue(purpose.kind.toInt())
            .setMachineId(opened.machineId)
            .setSessionId(opened.sessionId)
            .setRequestId(opened.requestId)
            .setSeq(opened.seq)
            .setDeadlineMs(opened.deadlineMs)
            .setSender(opened.sender)
            .build()
    }

    /** Matches `shared/devicesec/identity.go` `RequestIdentity` + `AuthorizedPayloadHash`. */
    fun requestIdentity(opened: SecurityOpenEnvelopeResult, unpackedPlain: ByteArray): Pair<ByteArray, ByteArray> {
        val pin = authority.record().pin
        val purpose = DeviceSecurity.Purpose.entries.first { it.value == opened.purpose.toUInt() }
        val rk =
            sha256(
                "ra4/request-key\u0000".toByteArray(),
                DeviceSecurity.Writer()
                    .str(pin.account)
                    .raw(pin.domain)
                    .str(opened.sender)
                    .u32(opened.purpose.toUInt())
                    .str(opened.machineId)
                    .str(opened.sessionId)
                    .str(opened.requestId)
                    .u64(opened.seq.toULong())
                    .bytes(),
            )
        val ph =
            sha256(
                "ra4/authorized-payload\u0000".toByteArray(),
                DeviceSecurity.Writer()
                    .lp(rk)
                    .u32(purpose.kind)
                    .u64(opened.deadlineMs.toULong())
                    .lp(unpackedPlain)
                    .bytes(),
            )
        return rk to ph
    }

    override fun close() {
        authority.close()
        if (closed.compareAndSet(false, true)) {
            core.close()
        }
    }
}
