package io.github.lewin671.remoteagent.data

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.DeviceSecurity
import io.github.lewin671.remoteagent.core.KEY_SIZE
import io.github.lewin671.remoteagent.core.SigningKey
import io.github.lewin671.remoteagent.core.hex
import io.github.lewin671.remoteagent.core.proto.*
import io.github.lewin671.remoteagent.core.proto.Event as CoreEvent
import io.github.lewin671.remoteagent.corebind.Corebind
import io.github.lewin671.remoteagent.proto.Envelope
import java.security.SecureRandom

internal class BenchmarkCoreSecurity(
    private val record: DeviceSecurity.Record,
    private val memberID: String,
    private val signer: SigningKey,
    private val xPrivate: ByteArray,
    private val nowMs: Long,
) : AutoCloseable {
    private val rng = SecureRandom()
    private val core = Corebind.new_()
    private var opSeq = 1L

    init {
        step(CoreEvent.newBuilder().setEntropyProvided(entropy(64, 64, 4096)).build())
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
        step(
            CoreEvent.newBuilder()
                .setClockUpdated(LocalCalendar.newBuilder().setNowMs(nowMs))
                .build(),
        )
        val recEv =
            SecurityRecordEvent.newBuilder()
                .setOpId(0L)
                .setPushSeq(1L)
                .setNetworkGeneration(1L)
                .setAccount(record.pin.account)
                .setDomain(ByteString.copyFrom(record.pin.domain))
                .setGenesis(ByteString.copyFrom(record.pin.genesis))
                .setFloorMs(record.floor.toLong())
        for (s in record.chain) {
            recEv.addStates(ByteString.copyFrom(DeviceSecurity.stateBytes(s)))
            val proof = record.proofs[s.core.epoch]
            recEv.addRecoveryProofs(if (proof == null) ByteString.EMPTY else ByteString.copyFrom(proof))
        }
        val headHash = DeviceSecurity.stateHash(record.head)
        record.lease?.takeIf { it.head.contentEquals(headHash) }?.let {
            recEv.setLease(ByteString.copyFrom(it.bytes()))
        }
        val headContentEpoch = record.head.core.contentEpoch
        for ((ep, sec) in record.cachedKeys()) {
            if (ep > 0uL && ep < headContentEpoch && sec.size == KEY_SIZE) {
                recEv.addContentKeys(
                    SecurityContentKey.newBuilder()
                        .setContentEpoch(ep.toLong())
                        .setSecret(ByteString.copyFrom(sec)),
                )
            }
        }
        step(CoreEvent.newBuilder().setSecurityRecord(recEv).build())
    }

    private fun entropy(ids: Int, floats: Int, bytes: Int): EntropyProvided {
        val b = EntropyProvided.newBuilder()
        val raw = ByteArray(16)
        repeat(ids) {
            rng.nextBytes(raw)
            b.addRandomIds(raw.hex())
        }
        repeat(floats) { b.addUnitFloats(rng.nextDouble()) }
        if (bytes > 0) {
            val r = ByteArray(bytes)
            rng.nextBytes(r)
            b.setRandomBytes(ByteString.copyFrom(r))
        }
        return b.build()
    }

    private fun step(ev: CoreEvent): List<Effect> {
        val out = ArrayList<Effect>()
        val q = ArrayDeque<CoreEvent>()
        q.addLast(ev)
        while (q.isNotEmpty()) {
            val raw: ByteArray? = core.dispatch(q.removeFirst().toByteArray())
            val batch = if (raw == null || raw.isEmpty()) EffectBatch.getDefaultInstance() else EffectBatch.parseFrom(raw)
            for (eff in batch.effectsList) {
                when (eff.effectCase) {
                    Effect.EffectCase.REQUEST_ENTROPY -> {
                        val req = eff.requestEntropy
                        q.addLast(
                            CoreEvent.newBuilder()
                                .setEntropyProvided(entropy(maxOf(req.minIds, 32), maxOf(req.minFloats, 32), maxOf(req.minRandomBytes, 512)))
                                .build(),
                        )
                    }
                    Effect.EffectCase.SECURITY_KEY_SIGN -> {
                        val s = eff.securityKeySign
                        q.addLast(
                            CoreEvent.newBuilder()
                                .setSecurityKeySigned(
                                    SecurityKeySignedEvent.newBuilder()
                                        .setOpId(s.opId)
                                        .setSignature(ByteString.copyFrom(signer.sign(s.message.toByteArray()))),
                                )
                                .build(),
                        )
                    }
                    Effect.EffectCase.SECURITY_KEY_AGREE -> {
                        val a = eff.securityKeyAgree
                        val shared = Corebind.x25519Shared(xPrivate, a.peerPublic.toByteArray())
                        q.addLast(
                            CoreEvent.newBuilder()
                                .setSecurityKeyAgreed(
                                    SecurityKeyAgreedEvent.newBuilder()
                                        .setOpId(a.opId)
                                        .setShared(ByteString.copyFrom(shared)),
                                )
                                .build(),
                        )
                    }
                    else -> out += eff
                }
            }
        }
        return out
    }

    fun sealEvent(sessionId: String, seq: Long, seed: ByteArray, plain: ByteArray): Envelope {
        val id = opSeq++
        val req =
            SecurityOpRequestEvent.newBuilder()
                .setOpId(id)
                .setSealEnvelope(
                    SecuritySealEnvelopeOp.newBuilder()
                        .setPurpose(DeviceSecurity.Purpose.EVENT.value.toInt())
                        .setMachineId(memberID)
                        .setSessionId(sessionId)
                        .setSeq(seq)
                        .setPlaintext(ByteString.copyFrom(plain))
                        .setUseSessionSeed(true)
                        .setOverrideSessionSeed(ByteString.copyFrom(seed)),
                )
                .build()
        val effs = step(CoreEvent.newBuilder().setSecurityOpRequest(req).build())
        val res = effs.last { it.hasSecurityOpResult() && it.securityOpResult.opId == id }.securityOpResult
        check(res.error.isEmpty() && res.hasSealed()) { "sealEvent failed: ${res.error}" }
        return res.sealed.envelope
    }

    fun openHistory(env: Envelope, seed: ByteArray): ByteArray {
        val id = opSeq++
        val req =
            SecurityOpRequestEvent.newBuilder()
                .setOpId(id)
                .setOpenEnvelope(
                    SecurityOpenEnvelopeOp.newBuilder()
                        .setEnvelope(env)
                        .setExpectedPurpose(DeviceSecurity.Purpose.EVENT.value.toInt())
                        .setHistory(true)
                        .setOverrideSessionSeed(ByteString.copyFrom(seed)),
                )
                .build()
        val effs = step(CoreEvent.newBuilder().setSecurityOpRequest(req).build())
        val res = effs.last { it.hasSecurityOpResult() && it.securityOpResult.opId == id }.securityOpResult
        check(res.error.isEmpty() && res.hasOpened()) { "openHistory failed: ${res.error}" }
        return res.opened.plaintext.toByteArray()
    }

    override fun close() {
        core.close()
    }
}
