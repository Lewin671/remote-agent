package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.corebind.Corebind
import io.github.lewin671.remoteagent.proto.Envelope
import java.io.ByteArrayOutputStream
import java.util.Collections
import java.util.WeakHashMap

/**
 * Protocol 10: the phone signs a membership chain; every device signs the envelopes it sends.
 * Canonical bytes, domain-separation labels and rules match internal/deviceauth, and the three
 * clients are kept in step by the Go vectors.
 */
object DeviceSecurity {
    /** State and header binary format version. */
    const val VERSION = 3
    private const val VERSION_U = 3u

    const val MAX_STATE = 64 shl 10
    const val MAX_HEADER = 1024
    const val MAX_CIPHER = 4 shl 20
    const val MAX_MUTABLE = 64 shl 10
    const val MAX_MEMBERS = 128
    private const val MAX_MEMBERS_U = 128u
    const val MAX_STATES = 4096
    /** Pairing carries at most this many states and this many bytes of chain. */
    const val MAX_PAIR_STATES = 256
    const val MAX_PAIR_CHAIN_BYTES = 800 shl 10

    const val LEASE = 72L * 60 * 60 * 1000
    /** How far ahead of the local clock a state's IssuedAt may be. */
    const val MAX_CLOCK_SKEW = 300_000uL
    const val LEASE_U = 259_200_000uL
    const val MAX_STATES_U = 4096uL
    private const val MAX_STATES_UINT = 4096u

    class Failure(val code: String = "invalid") : Exception("security: $code")

    private fun check(ok: Boolean, code: String = "invalid") {
        if (!ok) throw Failure(code)
    }

    private val zero = ByteArray(32)

    private fun id(s: String) = s.matches(Regex("[0-9a-f]{32}"))

    private fun opaque(s: String) = s.matches(Regex("[A-Za-z0-9_.:-]{1,128}"))

    private fun nonzero(b: ByteArray) = check(b.size == 32 && !b.contentEquals(zero))

    private fun digest(label: String, vararg parts: ByteArray) =
        sha256(label.toByteArray(Charsets.UTF_8), *parts)

    internal class Writer {
        private val out = ByteArrayOutputStream()

        fun raw(b: ByteArray): Writer {
            out.write(b)
            return this
        }

        fun u32(n: UInt): Writer {
            for (shift in 24 downTo 0 step 8) out.write((n shr shift).toInt() and 255)
            return this
        }

        fun u64(n: ULong): Writer {
            for (shift in 56 downTo 0 step 8) out.write((n shr shift).toInt() and 255)
            return this
        }

        fun lp(b: ByteArray): Writer = u32(b.size.toUInt()).raw(b)

        fun str(s: String): Writer = lp(s.toByteArray(Charsets.UTF_8))

        fun bytes(): ByteArray = out.toByteArray()
    }

    internal class Reader(private val b: ByteArray) {
        private var at = 0

        fun raw(n: Int): ByteArray {
            check(n >= 0 && n <= b.size - at)
            return b.copyOfRange(at, at + n).also { at += n }
        }

        fun u32(): UInt = raw(4).fold(0u) { a, b -> (a shl 8) or b.toUByte().toUInt() }

        fun u64(): ULong = raw(8).fold(0uL) { a, b -> (a shl 8) or b.toUByte().toULong() }

        fun lp(max: Int): ByteArray {
            val n = u32()
            check(n <= max.toUInt())
            return raw(n.toInt())
        }

        fun str(): String {
            val bytes = lp(MAX_STATE)
            val s = bytes.toString(Charsets.UTF_8)
            check(s.toByteArray(Charsets.UTF_8).contentEquals(bytes))
            return s
        }

        fun done() = check(at == b.size)
    }

    data class Member(val id: String, val ed: ByteArray, val x: ByteArray, val kind: UInt) {
        fun same(m: Member) =
            id == m.id && kind == m.kind && ed.contentEquals(m.ed) && x.contentEquals(m.x)
    }

    data class Core(
        val account: String,
        val domain: ByteArray,
        val authorityID: String,
        val recoveryEd: ByteArray,
        val parent: ByteArray,
        val epoch: ULong,
        val contentEpoch: ULong,
        val issuedAt: ULong,
        val members: List<Member>,
        val removed: List<String>,
    ) {
        fun member(memberId: String): Member? = members.firstOrNull { it.id == memberId }
    }

    data class Grant(val recipient: String, val enc: ByteArray, val cipher: ByteArray)

    data class State(
        val core: Core,
        val grants: List<Grant>,
        val prevSecret: ByteArray,
        val signature: ByteArray,
    )

    /** Immutable trust anchor: the account, the security domain and the genesis hash. */
    data class Pin(val account: String, val domain: ByteArray, val genesis: ByteArray)

    private data class CachedState(
        val raw: ByteArray,
        val hash: ByteArray,
        val coreRef: Core,
        val grantsRef: List<Grant>,
        val prevRef: ByteArray,
        val sigRef: ByteArray,
    )

    private val stateCache = Collections.synchronizedMap(WeakHashMap<State, CachedState>())

    internal fun cacheState(s: State, raw: ByteArray, hash: ByteArray) {
        stateCache[s] =
            CachedState(
                raw = raw.copyOf(),
                hash = hash.copyOf(),
                coreRef = s.core,
                grantsRef = s.grants,
                prevRef = s.prevSecret,
                sigRef = s.signature,
            )
    }

    private fun cachedEntry(s: State): CachedState? {
        val e = stateCache[s] ?: return null
        if (
            e.coreRef !== s.core ||
                e.grantsRef !== s.grants ||
                e.prevRef !== s.prevSecret ||
                e.sigRef !== s.signature
        ) {
            return null
        }
        return e
    }

    fun stateHash(s: State): ByteArray =
        cachedEntry(s)?.hash?.copyOf() ?: throw Failure("invalid")

    fun stateBytes(s: State): ByteArray =
        cachedEntry(s)?.raw?.copyOf() ?: throw Failure("invalid")

    fun parseState(b: ByteArray): State = SecCall.parseState(b).first

    fun tooFarAhead(s: State, now: ULong): Boolean = s.core.issuedAt > now + MAX_CLOCK_SKEW

    fun requireNotFromFuture(s: State, now: ULong) {
        check(!tooFarAhead(s, now), "clock")
    }

    fun verifyState(p: Pin, s: State) {
        check(s.core.account == p.account && s.core.domain.contentEquals(p.domain), "untrusted")
        nonzero(p.genesis)
        if (s.core.epoch == 1uL) check(stateHash(s).contentEquals(p.genesis), "untrusted")
    }

    fun pinOf(s: State): Pin {
        check(s.core.epoch == 1uL)
        return Pin(s.core.account, s.core.domain.copyOf(), stateHash(s))
    }

    fun verifyChain(
        pin: Pin,
        chain: List<State>,
        recoveryProofs: List<ByteArray?> = emptyList(),
        lease: ByteArray? = null,
        nowMs: Long = 0L,
        memberId: String = "",
        checkLeaseWindow: Boolean = false,
    ): State =
        SecCall.verifyChain(
            pin = pin,
            states = chain.map { stateBytes(it) },
            recoveryProofs = recoveryProofs,
            lease = lease,
            nowMs = nowMs,
            memberId = memberId,
            checkLeaseWindow = checkLeaseWindow,
        )

    fun openGrant(p: Pin, s: State, recipient: String, xPrivate: ByteArray): ByteArray {
        verifyState(p, s)
        check(xPrivate.size == 32)
        val raw = stateBytes(s)
        val enc = SecCall.grantChallenge(p, raw, recipient)
        val dh =
            try {
                Corebind.x25519Shared(xPrivate, enc)
            } catch (_: Throwable) {
                throw Failure("untrusted")
            }
        return SecCall.openGrantWithDH(p, raw, recipient, dh)
    }

    fun openPrevSecret(newSecret: ByteArray, blob: ByteArray): ByteArray =
        SecCall.openPrevSecret(newSecret, blob)

    enum class Purpose(val value: UInt) {
        EVENT(1u),
        SESSION_META(2u),
        SESSION_KEY(3u),
        MACHINE_META(4u),
        COMMAND(5u),
        RESULT(6u),
        TERMINAL_OUTPUT(7u),
        TERMINAL_INPUT(8u),
        NOTIFY(10u),
        IMAGE(11u),
        PREFS(12u),
        DEVICE_META(13u),
        TERMINAL_RESIZE(14u),
        TERMINAL_HISTORY(15u);

        val mutable
            get() = this in setOf(SESSION_META, MACHINE_META, DEVICE_META, PREFS)

        val control
            get() = this in setOf(COMMAND, TERMINAL_INPUT, TERMINAL_RESIZE, TERMINAL_HISTORY)

        val sessionKey
            get() =
                this in
                    setOf(
                        EVENT,
                        IMAGE,
                        TERMINAL_OUTPUT,
                        TERMINAL_INPUT,
                        TERMINAL_RESIZE,
                        TERMINAL_HISTORY,
                    )

        val kind
            get() = if (this == TERMINAL_RESIZE || this == TERMINAL_HISTORY) 8u else value
    }


    data class RecoveryStatement(
        val account: String,
        val domain: ByteArray,
        val parent: ByteArray,
        val newAuthorityID: String,
        val newRecoveryEd: ByteArray,
        val newMember: Member,
        val issuedAt: ULong,
        val stateHash: ByteArray,
        val nonce: ByteArray,
        val signature: ByteArray,
    ) {
        fun verify(parent: State, next: State) {
            SecCall.verifyRecoveryStatement(marshal(), stateBytes(parent), stateBytes(next))
        }

        fun marshal(): ByteArray = SecCall.marshalRecoveryStatement(this).first
    }

    fun signRecoveryStatement(
        parent: State,
        next: State,
        oldRecoverySeed: ByteArray,
        nonce: ByteArray = randomBytes(32),
    ): RecoveryStatement {
        check(oldRecoverySeed.size == 32)
        val (unsigned, signBytes) =
            SecCall.newRecoveryStatementUnsigned(stateBytes(parent), stateBytes(next), nonce)
        val sig = Corebind.ed25519Sign(oldRecoverySeed, signBytes)
        return SecCall.marshalRecoveryStatement(unsigned.copy(signature = sig)).second
    }

    fun parseRecoveryStatement(b: ByteArray): RecoveryStatement =
        SecCall.parseRecoveryStatement(b)

    /** Kept only behind the foreground account's unlocked-device key. */
    class Record(
        val pin: Pin,
        val chain: List<State>,
        val memberID: String,
        private val xPrivate: ByteArray,
        val floor: ULong = 0uL,
        private val keys: Map<ULong, ByteArray> = emptyMap(),
        /** Recovery statements that authorize recovery transitions, by their state epoch. */
        val proofs: Map<ULong, ByteArray> = emptyMap(),
        val lease: HeadLease? = null,
    ) {
        init {
            check(id(pin.account) && pin.domain.size == 32 && pin.genesis.size == 32)
            check(chain.isNotEmpty() && chain.size <= MAX_STATES)
            verifyChain(pin, chain, chain.map { proofs[it.core.epoch] })
            check(id(memberID) && xPrivate.size == 32)
            lastMember()?.let { openGrant(pin, it, memberID, xPrivate) }
        }

        fun signingTime(wall: Long): Long =
            maxOf(
                    maxOf(wall.coerceAtLeast(1).toULong(), floor, head.core.issuedAt),
                    lease?.issuedAt ?: 0uL,
                )
                .toLong()

        fun includesInstallation(other: Record): Boolean =
            pin.account == other.pin.account &&
                pin.domain.contentEquals(other.pin.domain) &&
                pin.genesis.contentEquals(other.pin.genesis) &&
                memberID == other.memberID &&
                chain.size >= other.chain.size &&
                stateHash(chain[other.chain.lastIndex]).contentEquals(stateHash(other.head)) &&
                (chain.size > other.chain.size || xPrivate.contentEquals(other.xPrivate))

        internal fun peerBoxKey(): ByteArray = xPrivate.copyOf()

        fun leaseExpiration(): Long =
            lease?.takeIf { runCatching { it.verify(head) }.isSuccess }?.notAfter?.toLong() ?: 0L

        override fun toString() = "DeviceSecurity.Record(epoch=${head.core.epoch})"

        val head: State
            get() = chain.last()

        /**
         * Whether the head still names this device. A removed device keeps its last content key so
         * its retained history stays readable.
         */
        val member: Boolean
            get() = head.core.member(memberID) != null

        fun stateAt(epoch: ULong): State? = chain.firstOrNull { it.core.epoch == epoch }

        private fun lastMember(): State? = chain.lastOrNull { it.core.member(memberID) != null }

        /** The content key of this device's last membership (the head while it is still one). */
        fun secret(): ByteArray {
            val own = lastMember() ?: throw Failure("unavailable")
            return openGrant(pin, own, memberID, xPrivate)
        }

        /**
         * The content key of [epoch], walking PrevSecret from this device's last membership; null
         * at a recovery boundary.
         */
        fun contentKey(epoch: ULong): ByteArray? {
            if (epoch == 0uL) return null
            keys[epoch]?.let {
                return it
            }
            val own = lastMember() ?: return null
            if (epoch > own.core.contentEpoch) return null
            var cur = own
            var key = openGrant(pin, own, memberID, xPrivate)
            while (cur.core.contentEpoch > epoch) {
                val prev =
                    chain.firstOrNull { it.core.epoch == cur.core.epoch - 1uL } ?: return null
                if (prev.core.contentEpoch == cur.core.contentEpoch) {
                    cur = prev
                    continue
                }
                if (cur.prevSecret.isEmpty()) return null
                key = openPrevSecret(key, cur.prevSecret)
                cur = prev
            }
            return if (cur.core.contentEpoch == epoch) key else null
        }

        fun withKey(epoch: ULong, secret: ByteArray): Record {
            check(secret.size == 32)
            return Record(
                pin,
                chain,
                memberID,
                xPrivate,
                floor,
                keys + (epoch to secret),
                proofs,
                lease,
            )
        }

        fun cachedKeys(): Map<ULong, ByteArray> = keys

        fun touched(now: ULong): Record =
            if (now <= floor) this
            else Record(pin, chain, memberID, xPrivate, now, keys, proofs, lease)

        fun installed(
            states: List<State>,
            proofs: List<ByteArray?>,
            certificate: HeadLease? = null,
        ): Record {
            check(states.size <= MAX_STATES)
            val all = chain.toMutableList()
            val newProofs = HashMap<ULong, ByteArray>()
            newProofs.putAll(this.proofs)
            val incoming = proofs.toMutableList()
            while (incoming.size < states.size) incoming.add(null)
            // Cache every key still derivable from this chain before a
            // recovery boundary can stop the PrevSecret walk. Retained
            // devices keep their pre-recovery history this way.
            val newKeys = HashMap(keys)
            for (ep in 1uL..head.core.contentEpoch) {
                contentKey(ep)?.let { newKeys.putIfAbsent(ep, it) }
            }
            states.forEachIndexed { i, next ->
                verifyState(pin, next)
                val existing = all.firstOrNull { it.core.epoch == next.core.epoch }
                if (existing != null) {
                    check(stateBytes(existing).contentEquals(stateBytes(next)), "stale")
                    return@forEachIndexed
                }
                check(next.core.epoch == all.last().core.epoch + 1uL, "stale")
                val rawProof = incoming[i]
                if (rawProof != null) newProofs[next.core.epoch] = rawProof
                all.add(next)
            }
            verifyChain(pin, all, all.map { newProofs[it.core.epoch] })
            var secret = lastMember()?.let { secret() }
            for (idx in chain.size until all.size) {
                val prev = all[idx - 1]
                val next = all[idx]
                val hasProof = newProofs.containsKey(next.core.epoch)
                if (next.core.member(memberID) != null) {
                    val key = openGrant(pin, next, memberID, xPrivate)
                    if (next.core.contentEpoch != prev.core.contentEpoch) {
                        if (!hasProof) {
                            val old = openPrevSecret(key, next.prevSecret)
                            check(secret != null && old.contentEquals(secret), "untrusted")
                        }
                        if (secret != null) newKeys[prev.core.contentEpoch] = secret
                        secret = key
                    } else {
                        secret = key
                    }
                    newKeys[next.core.contentEpoch] = key
                }
            }
            check(all.size <= MAX_STATES, "capacity")
            val finalHead = all.last()
            certificate?.let {
                it.verify(finalHead)
                check(
                    it.issuedAt <=
                        maxOf(System.currentTimeMillis().toULong(), floor) + MAX_CLOCK_SKEW,
                    "clock",
                )
                lease?.let { old ->
                    if (old.head.contentEquals(it.head))
                        check(
                            it.issuedAt >= old.issuedAt &&
                                it.notAfter >= old.notAfter &&
                                (it.issuedAt != old.issuedAt || it.notAfter == old.notAfter),
                            "stale",
                        )
                }
            }
            return Record(
                pin,
                all,
                memberID,
                xPrivate,
                floor,
                newKeys.filterKeys { it <= finalHead.core.contentEpoch },
                newProofs,
                certificate ?: lease,
            )
        }

        fun bytes(): ByteArray {
            val w =
                Writer()
                    .u32(VERSION_U)
                    .str(pin.account)
                    .raw(pin.domain)
                    .raw(pin.genesis)
                    .str(memberID)
                    .raw(xPrivate)
                    .u64(floor)
                    .u32(chain.size.toUInt())
            chain.forEach { w.lp(stateBytes(it)) }
            w.u32(keys.size.toUInt())
            keys.forEach { (epoch, key) -> w.u64(epoch).raw(key) }
            w.u32(proofs.size.toUInt())
            proofs.forEach { (epoch, raw) -> w.u64(epoch).lp(raw) }
            w.lp(lease?.bytes() ?: byteArrayOf())
            return w.bytes()
        }

        companion object {
            fun parse(b: ByteArray): Record {
                check(b.size <= MAX_STATES * (MAX_STATE + 4) + 512)
                val r = Reader(b)
                check(r.u32() == VERSION_U)
                val pin = Pin(r.str(), r.raw(32), r.raw(32))
                val memberID = r.str()
                val x = r.raw(32)
                val floor = r.u64()
                val n = r.u32()
                check(n in 1u..MAX_STATES_UINT)
                val chain = List(n.toInt()) { parseState(r.lp(MAX_STATE)) }
                val kn = r.u32()
                check(kn <= MAX_STATES_UINT)
                val keys = HashMap<ULong, ByteArray>()
                repeat(kn.toInt()) { keys[r.u64()] = r.raw(32) }
                val pn = r.u32()
                check(pn <= MAX_STATES_UINT)
                val proofs = HashMap<ULong, ByteArray>()
                repeat(pn.toInt()) { proofs[r.u64()] = r.lp(MAX_STATE) }
                val certificate = r.lp(256).takeIf { it.isNotEmpty() }?.let { HeadLease.parse(it) }
                r.done()
                return Record(pin, chain, memberID, x, floor, keys, proofs, certificate)
            }

            /** A record built from the Go cross-language fixture's JSON. */
            fun fromChain(
                account: String,
                domain: ByteArray,
                genesis: ByteArray,
                memberID: String,
                x: ByteArray,
                states: List<ByteArray>,
                proofs: List<ByteArray?>,
            ): Record {
                val chain = states.map { parseState(it) }
                check(chain.size == proofs.size)
                val pin = Pin(account, domain, genesis)
                verifyChain(pin, chain, proofs)
                val proofMap = HashMap<ULong, ByteArray>()
                chain.forEachIndexed { i, s -> proofs[i]?.let { proofMap[s.core.epoch] = it } }
                return Record(pin, chain, memberID, x, proofs = proofMap)
            }
        }
    }

    interface Storage {
        /** Releases verified records and derived secrets retained only for foreground reads. */
        fun clearSecurityCache() {}

        fun readSecurityOperation(): SecurityOperation? = null

        fun writeSecurityOperation(operation: SecurityOperation) {
            throw Failure("storage")
        }

        fun clearSecurityOperation() {
            throw Failure("storage")
        }

        fun readSecurity(): Record

        fun initializeSecurity(record: Record)

        /** Installs verified transitions atomically on the current stored record. */
        fun installSecurity(
            expectedState: ByteArray,
            states: List<State>,
            proofs: List<ByteArray?>,
            lease: HeadLease? = null,
        ): Record

        fun touchSecurity(expectedState: ByteArray, floor: ULong): Record

        fun cacheSecurityKey(expectedState: ByteArray, epoch: ULong, secret: ByteArray)
    }

    class Historical(val state: State, val secret: ByteArray) {
        init {
            check(secret.size == 32 && state.core.epoch in 1uL..MAX_STATES_U)
        }

        override fun toString() = "DeviceSecurity.Historical(epoch=${state.core.epoch})"

        fun bytes(): ByteArray = Writer().lp(stateBytes(state)).raw(secret).bytes()

        companion object {
            fun parse(b: ByteArray): Historical {
                check(b.size <= MAX_STATE + 36)
                val r = Reader(b)
                val state = parseState(r.lp(MAX_STATE))
                val key = r.raw(32)
                r.done()
                return Historical(state, key)
            }
        }
    }

    fun purposeKey(
        secret: ByteArray,
        account: String,
        domain: ByteArray,
        epoch: ULong,
        purpose: Purpose,
    ): ByteArray = SecCall.derivePurposeKey(secret, account, domain, epoch, purpose.value)

    class NotifyRecord(val pin: Pin, val state: State, private val child: ByteArray) {
        init {
            verifyState(pin, state)
            check(state.core.epoch > 0uL && child.size == 32)
        }

        override fun toString() = "DeviceSecurity.NotifyRecord(epoch=${state.core.contentEpoch})"

        fun bytes(): ByteArray =
            Writer()
                .u32(VERSION_U)
                .str(pin.account)
                .raw(pin.domain)
                .raw(pin.genesis)
                .lp(stateBytes(state))
                .raw(child)
                .bytes()

        fun open(env: Envelope): io.github.lewin671.remoteagent.proto.NotifyContent =
            SecCall.openNotifyEnvelope(
                pin = pin,
                states = listOf(stateBytes(state)),
                envelope = env,
                purposeKey = child,
            )

        companion object {
            fun fromRecord(r: Record): NotifyRecord =
                NotifyRecord(
                    r.pin,
                    r.head,
                    purposeKey(
                        r.secret(),
                        r.pin.account,
                        r.pin.domain,
                        r.head.core.contentEpoch,
                        Purpose.NOTIFY,
                    ),
                )

            fun parse(b: ByteArray): NotifyRecord {
                check(b.size <= MAX_STATE + 512)
                val r = Reader(b)
                check(r.u32() == VERSION_U)
                val pin = Pin(r.str(), r.raw(32), r.raw(32))
                val state = parseState(r.lp(MAX_STATE))
                val child = r.raw(32)
                r.done()
                return NotifyRecord(pin, state, child)
            }
        }
    }
}
