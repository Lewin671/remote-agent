package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.Header as PBHeader
import java.io.ByteArrayOutputStream
import javax.crypto.Cipher as AESCipher
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.SecretKeySpec
import org.bouncycastle.crypto.AsymmetricCipherKeyPair
import org.bouncycastle.crypto.hpke.HPKE
import org.bouncycastle.crypto.params.X25519PrivateKeyParameters

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
    const val MAX_LEASE = LEASE
    /** How far ahead of the local clock a state's IssuedAt may be. */
    const val MAX_CLOCK_SKEW = 300_000uL
    const val MAX_LEASE_U = 259_200_000uL
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

    private fun validate(c: Core) {
        check(
            id(c.account) &&
                id(c.authorityID) &&
                c.epoch > 0uL &&
                c.contentEpoch > 0uL &&
                c.contentEpoch <= c.epoch &&
                c.issuedAt > 0uL &&
                c.members.size in 1..MAX_MEMBERS &&
                c.removed.size <= MAX_MEMBERS,
        )
        nonzero(c.domain)
        nonzero(c.recoveryEd)
        check(c.parent.size == 32)
        c.members.forEachIndexed { i, m ->
            check(
                id(m.id) &&
                    m.ed.size == 32 &&
                    deviceId(m.ed) == m.id &&
                    (m.kind == 1u || m.kind == 2u),
            )
            nonzero(m.x)
            if (i > 0) check(c.members[i - 1].id < m.id)
        }
        val a = c.member(c.authorityID) ?: throw Failure()
        check(a.kind == 2u)
        c.removed.forEachIndexed { i, s ->
            check(
                id(s) &&
                    (i == 0 || c.removed[i - 1] < s) &&
                    c.member(s) == null &&
                    s != c.authorityID,
            )
        }
        if (c.epoch == 1uL)
            check(
                c.parent.contentEquals(zero) &&
                    c.members.size == 1 &&
                    c.removed.isEmpty() &&
                    c.contentEpoch == 1uL,
            )
        else nonzero(c.parent)
    }

    fun coreBytes(c: Core): ByteArray {
        validate(c)
        val w =
            Writer()
                .u32(VERSION_U)
                .str(c.account)
                .raw(c.domain)
                .str(c.authorityID)
                .raw(c.recoveryEd)
                .raw(c.parent)
                .u64(c.epoch)
                .u64(c.contentEpoch)
                .u64(c.issuedAt)
                .u32(c.members.size.toUInt())
        c.members.forEach { w.str(it.id).raw(it.ed).raw(it.x).u32(it.kind) }
        w.u32(c.removed.size.toUInt())
        c.removed.forEach { w.str(it) }
        return w.bytes()
    }

    fun parseCore(b: ByteArray): Core {
        check(b.size <= MAX_STATE)
        val r = Reader(b)
        check(r.u32() == VERSION_U)
        val account = r.str()
        val domain = r.raw(32)
        val authorityID = r.str()
        val recoveryEd = r.raw(32)
        val parent = r.raw(32)
        val epoch = r.u64()
        val contentEpoch = r.u64()
        val issuedAt = r.u64()

        var n = r.u32()
        check(n <= MAX_MEMBERS_U)
        val members = List(n.toInt()) { Member(r.str(), r.raw(32), r.raw(32), r.u32()) }
        n = r.u32()
        check(n <= MAX_MEMBERS_U)
        val removed = List(n.toInt()) { r.str() }
        r.done()
        return Core(
                account,
                domain,
                authorityID,
                recoveryEd,
                parent,
                epoch,
                contentEpoch,
                issuedAt,
                members,
                removed,
            )
            .also { validate(it) }
    }

    private fun grantBytes(s: State): ByteArray {
        check(s.grants.size == s.core.members.size)
        val w = Writer().u32(s.grants.size.toUInt())
        s.grants.forEachIndexed { i, g ->
            check(g.recipient == s.core.members[i].id && g.cipher.size == 48)
            nonzero(g.enc)
            w.str(g.recipient).lp(g.enc).lp(g.cipher)
        }
        return w.bytes()
    }

    private fun coreHash(s: State): ByteArray = digest("ra3/core\u0000", coreBytes(s.core))

    fun stateHash(s: State): ByteArray =
        digest("ra3/state\u0000", coreHash(s), grantBytes(s), Writer().lp(s.prevSecret).bytes())

    fun signBytes(s: State): ByteArray =
        Writer()
            .raw("ra3/state-sig\u0000".toByteArray())
            .str(s.core.account)
            .raw(s.core.domain)
            .raw(s.core.parent)
            .u64(s.core.epoch)
            .u64(s.core.contentEpoch)
            .u64(s.core.issuedAt)
            .str(s.core.authorityID)
            .raw(s.core.recoveryEd)
            .raw(stateHash(s))
            .bytes()

    fun stateBytes(s: State): ByteArray {
        check(s.signature.size == 64)
        return Writer()
            .lp(coreBytes(s.core))
            .raw(grantBytes(s))
            .lp(s.prevSecret)
            .lp(s.signature)
            .bytes()
            .also { check(it.size <= MAX_STATE) }
    }

    fun parseState(b: ByteArray): State {
        check(b.size <= MAX_STATE)
        val r = Reader(b)
        val core = parseCore(r.lp(MAX_STATE))
        val n = r.u32()
        check(n <= MAX_MEMBERS_U)
        val grants = List(n.toInt()) { Grant(r.str(), r.lp(32), r.lp(48)) }
        val s = State(core, grants, r.lp(MAX_STATE), r.lp(64))
        r.done()
        check(s.signature.size == 64)
        stateHash(s)
        return s
    }

    /** Anchors account, domain and, at epoch 1, the exact genesis bytes. */
    fun tooFarAhead(s: State, now: ULong): Boolean = s.core.issuedAt > now + MAX_CLOCK_SKEW

    fun requireNotFromFuture(s: State, now: ULong) {
        check(!tooFarAhead(s, now), "clock")
    }

    fun verifyState(p: Pin, s: State) {
        check(s.core.account == p.account && s.core.domain.contentEquals(p.domain), "untrusted")
        nonzero(p.genesis)
        if (s.core.epoch == 1uL) check(stateHash(s).contentEquals(p.genesis), "untrusted")
    }

    fun verifyGenesis(p: Pin, s: State) {
        verifyState(p, s)
        check(
            s.core.epoch == 1uL &&
                s.core.members.size == 1 &&
                s.core.members[0].id == s.core.authorityID,
            "untrusted",
        )
        val a = s.core.members[0]
        check(a.kind == 2u && verifyEd25519(a.ed, signBytes(s), s.signature), "untrusted")
    }

    fun pinOf(s: State): Pin {
        check(s.core.epoch == 1uL)
        return Pin(s.core.account, s.core.domain, stateHash(s))
    }

    /**
     * Checks one signed transition. A recovery transition also needs the statement signed by the
     * parent's recovery key; opening the grants and PrevSecret is the installer's job.
     */
    fun verifyTransition(parent: State, next: State, recovery: RecoveryStatement? = null) {
        val ph = stateHash(parent)
        check(
            next.core.parent.contentEquals(ph) &&
                parent.core.epoch != ULong.MAX_VALUE &&
                next.core.epoch == parent.core.epoch + 1uL &&
                next.core.account == parent.core.account &&
                next.core.domain.contentEquals(parent.core.domain),
            "stale",
        )
        check(next.core.issuedAt >= parent.core.issuedAt)
        val authority = next.core.member(next.core.authorityID) ?: throw Failure()
        check(authority.kind == 2u)
        if (recovery != null) {
            recovery.verify(parent, next)
            check(
                next.core.contentEpoch == parent.core.contentEpoch + 1uL &&
                    next.prevSecret.isEmpty() &&
                    next.core.member(parent.core.authorityID) == null &&
                    next.core.removed.contains(parent.core.authorityID),
            )
            check(verifyEd25519(authority.ed, signBytes(next), next.signature), "untrusted")
            recoveryDelta(parent, next)
            return
        }
        // A normal authority-signed state may rotate RecoveryEd (veto); it
        // still has to be signed by the parent's authority key below.
        val oldAuthority = parent.core.member(parent.core.authorityID)
        check(
            oldAuthority != null && verifyEd25519(oldAuthority.ed, signBytes(next), next.signature),
            "untrusted",
        )
        if (next.core.authorityID != parent.core.authorityID) {
            check(
                parent.core.member(next.core.authorityID)?.kind == 2u &&
                    next.core.member(parent.core.authorityID) == null &&
                    !next.core.recoveryEd.contentEquals(parent.core.recoveryEd) &&
                    next.core.contentEpoch == parent.core.contentEpoch + 1uL &&
                    next.prevSecret.size == 60,
            )
            recoveryDelta(parent, next)
            return
        }
        when (next.core.contentEpoch) {
            parent.core.contentEpoch -> {
                check(
                    next.core.removed == parent.core.removed &&
                        !next.core.recoveryEd.contentEquals(parent.core.recoveryEd) &&
                        sameMembers(next.core.members, parent.core.members) &&
                        next.prevSecret.isEmpty(),
                )
            }
            parent.core.contentEpoch + 1uL -> {
                check(next.prevSecret.isNotEmpty())
                membershipDelta(parent, next)
            }
            else -> throw Failure()
        }
    }

    private fun sameMembers(a: List<Member>, b: List<Member>): Boolean =
        a.size == b.size && a.indices.all { a[it].same(b[it]) }

    private fun membershipDelta(parent: State, next: State) {
        val removed = mutableListOf<String>()
        var added = 0
        var rotated = 0
        parent.core.members.forEach { m ->
            val n = next.core.member(m.id)
            if (n == null) removed.add(m.id)
            else {
                check(n.ed.contentEquals(m.ed) && n.kind == m.kind)
                if (!n.x.contentEquals(m.x)) rotated++
            }
        }
        next.core.members.forEach { m ->
            if (parent.core.member(m.id) == null) {
                check(m.id !in parent.core.removed)
                added++
            }
        }
        check((parent.core.removed + removed).sorted() == next.core.removed)
        check(
            added <= 1 &&
                rotated <= 1 &&
                !(added > 0 && removed.isNotEmpty()) &&
                !(rotated > 0 && (added > 0 || removed.isNotEmpty())) &&
                !(added == 0 && removed.isEmpty() && rotated == 0),
        )
    }

    private fun recoveryDelta(parent: State, next: State) {
        val removed = (parent.core.removed + parent.core.authorityID).sorted()
        check(removed == next.core.removed)
        parent.core.members.forEach { m ->
            if (m.id == parent.core.authorityID) return@forEach
            check(next.core.member(m.id)?.same(m) == true)
        }
        next.core.members.forEach { m ->
            if (m.id == next.core.authorityID) return@forEach
            check(parent.core.member(m.id) != null)
        }
    }

    private fun hpke() =
        HPKE(HPKE.mode_base, HPKE.kem_X25519_SHA256, HPKE.kdf_HKDF_SHA256, HPKE.aead_AES_GCM256)

    fun openGrant(p: Pin, s: State, recipient: String, xPrivate: ByteArray): ByteArray {
        verifyState(p, s)
        check(xPrivate.size == 32)
        val x = X25519PrivateKeyParameters(xPrivate, 0)
        val m = s.core.member(recipient) ?: throw Failure("untrusted")
        check(m.x.contentEquals(x.generatePublicKey().encoded), "untrusted")
        val g = s.grants.firstOrNull { it.recipient == recipient } ?: throw Failure("untrusted")
        val ctx =
            Writer()
                .str(p.account)
                .raw(p.domain)
                .u64(s.core.contentEpoch)
                .raw(coreHash(s))
                .str(recipient)
                .bytes()
        val r =
            hpke()
                .setupBaseR(
                    g.enc,
                    AsymmetricCipherKeyPair(x.generatePublicKey(), x),
                    "ra3/grant-info\u0000".toByteArray() + ctx,
                )
        return r.open("ra3/grant-aad\u0000".toByteArray() + ctx, g.cipher).also {
            check(it.size == 32, "untrusted")
        }
    }

    internal fun sealGrant(
        recipient: Member,
        c: Core,
        coreHash: ByteArray,
        secret: ByteArray,
    ): Grant {
        val ctx =
            Writer()
                .str(c.account)
                .raw(c.domain)
                .u64(c.contentEpoch)
                .raw(coreHash)
                .str(recipient.id)
                .bytes()
        val s =
            hpke()
                .setupBaseS(
                    org.bouncycastle.crypto.params.X25519PublicKeyParameters(recipient.x, 0),
                    "ra3/grant-info\u0000".toByteArray() + ctx,
                )
        val cipher = s.seal("ra3/grant-aad\u0000".toByteArray() + ctx, secret)
        return Grant(recipient.id, s.encapsulation, cipher)
    }

    fun sealPrevSecret(newSecret: ByteArray, oldSecret: ByteArray): ByteArray {
        check(newSecret.size == 32 && oldSecret.size == 32)
        val nonce = randomBytes(12)
        val c = AESCipher.getInstance("AES/GCM/NoPadding")
        c.init(
            AESCipher.ENCRYPT_MODE,
            SecretKeySpec(newSecret, "AES"),
            GCMParameterSpec(128, nonce),
        )
        c.updateAAD("ra3/prev-secret\u0000".toByteArray())
        return nonce + c.doFinal(oldSecret)
    }

    fun openPrevSecret(newSecret: ByteArray, blob: ByteArray): ByteArray {
        check(newSecret.size == 32 && blob.size == 60)
        val c = AESCipher.getInstance("AES/GCM/NoPadding")
        c.init(
            AESCipher.DECRYPT_MODE,
            SecretKeySpec(newSecret, "AES"),
            GCMParameterSpec(128, blob, 0, 12),
        )
        c.updateAAD("ra3/prev-secret\u0000".toByteArray())
        val out =
            try {
                c.doFinal(blob, 12, blob.size - 12)
            } catch (_: Exception) {
                throw Failure("untrusted")
            }
        check(out.size == 32)
        return out
    }

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

    data class Header(
        val account: String,
        val domain: ByteArray,
        val epoch: ULong,
        val state: ByteArray,
        val contentEpoch: ULong,
        val sender: String,
        val kind: UInt,
        val purpose: Purpose,
        val machine: String,
        val session: String,
        val request: String,
        val seq: ULong,
        val deadline: ULong,
        val compression: UInt,
    )

    private fun validate(h: Header) {
        check(
            id(h.account) &&
                id(h.sender) &&
                h.epoch > 0uL &&
                h.contentEpoch > 0uL &&
                h.contentEpoch <= h.epoch &&
                h.compression <= 1u &&
                h.kind == h.purpose.kind &&
                (h.machine.isEmpty() || id(h.machine)) &&
                (h.session.isEmpty() || opaque(h.session)) &&
                (h.request.isEmpty() || opaque(h.request)),
        )
        nonzero(h.domain)
        nonzero(h.state)
        when (h.purpose) {
            Purpose.PREFS ->
                check(
                    h.request == "project-prefs" &&
                        h.machine.isEmpty() &&
                        h.session.isEmpty() &&
                        h.seq > 0uL,
                )
            Purpose.DEVICE_META ->
                check(id(h.request) && h.machine.isEmpty() && h.session.isEmpty() && h.seq > 0uL)
            Purpose.MACHINE_META ->
                check(
                    h.machine.isNotEmpty() &&
                        h.session.isEmpty() &&
                        h.request.isEmpty() &&
                        h.seq > 0uL,
                )
            Purpose.SESSION_META,
            Purpose.SESSION_KEY,
            Purpose.EVENT ->
                check(
                    h.machine.isNotEmpty() &&
                        h.session.isNotEmpty() &&
                        h.request.isEmpty() &&
                        h.seq > 0uL,
                )
            Purpose.COMMAND,
            Purpose.RESULT -> check(h.machine.isNotEmpty() && h.request.length >= 16)
            Purpose.TERMINAL_INPUT,
            Purpose.TERMINAL_RESIZE,
            Purpose.TERMINAL_HISTORY,
            Purpose.TERMINAL_OUTPUT ->
                check(
                    h.machine.isNotEmpty() &&
                        h.session.isNotEmpty() &&
                        h.request.length >= 16 &&
                        h.seq > 0uL,
                )
            Purpose.IMAGE,
            Purpose.NOTIFY ->
                check(h.machine.isNotEmpty() && h.session.isNotEmpty() && h.request.isNotEmpty())
        }
        if (h.purpose.control) check(h.deadline > 0uL)
    }

    fun headerBytes(h: Header): ByteArray {
        validate(h)
        return Writer()
            .u32(VERSION_U)
            .str(h.account)
            .raw(h.domain)
            .u64(h.epoch)
            .raw(h.state)
            .u64(h.contentEpoch)
            .str(h.sender)
            .u32(h.kind)
            .u32(h.purpose.value)
            .str(h.machine)
            .str(h.session)
            .str(h.request)
            .u64(h.seq)
            .u64(h.deadline)
            .u32(h.compression)
            .bytes()
    }

    fun parseHeader(b: ByteArray): Header {
        check(b.size <= MAX_HEADER)
        val r = Reader(b)
        check(r.u32() == VERSION_U)
        val account = r.str()
        val domain = r.raw(32)
        val epoch = r.u64()
        val state = r.raw(32)
        val contentEpoch = r.u64()
        val sender = r.str()
        val kind = r.u32()
        val purposeValue = r.u32()
        val purpose = Purpose.entries.firstOrNull { it.value == purposeValue }
        val h =
            Header(
                account,
                domain,
                epoch,
                state,
                contentEpoch,
                sender,
                kind,
                purpose ?: throw Failure(),
                r.str(),
                r.str(),
                r.str(),
                r.u64(),
                r.u64(),
                r.u32(),
            )
        r.done()
        validate(h)
        return h
    }

    private fun scope(h: Header, s: State): Member {
        validate(h)
        check(
            h.account == s.core.account &&
                h.domain.contentEquals(s.core.domain) &&
                h.epoch == s.core.epoch &&
                h.contentEpoch == s.core.contentEpoch &&
                h.state.contentEquals(stateHash(s)),
            "stale",
        )
        val m = s.core.member(h.sender) ?: throw Failure("untrusted")
        if (h.machine.isNotEmpty())
            check(s.core.members.any { it.id == h.machine && it.kind == 1u }, "untrusted")
        when (h.purpose) {
            Purpose.DEVICE_META -> {
                check(h.sender == h.request)
                check(s.core.member(h.request)?.kind == 2u, "untrusted")
            }
            Purpose.PREFS -> check(m.kind == 2u, "untrusted")
            Purpose.COMMAND,
            Purpose.TERMINAL_INPUT,
            Purpose.TERMINAL_RESIZE,
            Purpose.TERMINAL_HISTORY -> check(m.kind == 2u, "untrusted")
            else -> check(m.kind == 1u && m.id == h.machine, "untrusted")
        }
        return m
    }

    data class Formed(
        val header: ByteArray,
        val nonce: ByteArray,
        val cipher: ByteArray,
        val origin: ByteArray,
    )

    data class Wire(val formed: Formed)

    fun commitment(c: Formed): ByteArray {
        check(c.header.size <= MAX_HEADER && c.nonce.size == 12 && c.cipher.size in 16..MAX_CIPHER)
        parseHeader(c.header)
        return digest(
            "ra3/ciphertext\u0000",
            Writer().lp(c.header).lp(c.nonce).lp(c.cipher).bytes(),
        )
    }

    private fun originBytes(h: Header, hh: ByteArray, commit: ByteArray) =
        Writer()
            .raw("ra3/cipher-origin\u0000".toByteArray())
            .str(h.account)
            .raw(h.domain)
            .raw(h.state)
            .str(h.sender)
            .raw(hh)
            .raw(commit)
            .bytes()

    fun contentKey(secret: ByteArray, seed: ByteArray?, h: Header): ByteArray {
        check(secret.size == 32)
        validate(h)
        val salt =
            if (h.purpose.sessionKey) {
                check(seed?.size == 32)
                seed
            } else null
        val ctx =
            Writer()
                .raw("ra3/content-key\u0000".toByteArray())
                .str(h.account)
                .raw(h.domain)
                .u64(h.epoch)
                .raw(h.state)
                .u64(h.contentEpoch)
                .u32(h.purpose.value)
                .str(h.sender)
                .str(h.machine)
                .str(h.session)
                .str(h.request)
                .u64(h.seq)
                .bytes()
        val child = purposeKey(secret, h.account, h.domain, h.contentEpoch, h.purpose)
        return hkdf(child, salt, ctx, 32)
    }

    /** Forms a signed envelope using an independent random nonce. */
    fun form(
        secret: ByteArray,
        seed: ByteArray?,
        h: Header,
        plain: ByteArray,
        ed: SigningKey,
    ): Formed {
        check(plain.size <= MAX_CIPHER - 16 && deviceId(ed.publicKey) == h.sender)
        val hb = headerBytes(h)
        val nonce = randomBytes(12)
        val aes = AESCipher.getInstance("AES/GCM/NoPadding")
        aes.init(
            AESCipher.ENCRYPT_MODE,
            SecretKeySpec(contentKey(secret, seed, h), "AES"),
            GCMParameterSpec(128, nonce),
        )
        aes.updateAAD(hb)
        val c = Formed(hb, nonce, aes.doFinal(plain), ByteArray(0))
        return c.copy(origin = ed.sign(originBytes(h, sha256(hb), commitment(c))))
    }

    /**
     * Verifies the origin against the state the header names. For live traffic the sender must also
     * be in [head]; control needs a valid head lease and the head's content epoch.
     */
    fun verifyOrigin(s: State, head: State, live: Boolean, now: ULong, w: Wire): Header {
        val h = parseHeader(w.formed.header)
        val m = scope(h, s)
        if (live) {
            check(head.core.member(h.sender) != null, "untrusted")
            if (h.purpose.control) {
                check(!tooFarAhead(head, now), "clock")
                check(head.core.contentEpoch == h.contentEpoch, "stale")
            }
        }
        val commit = commitment(w.formed)
        check(
            verifyEd25519(m.ed, originBytes(h, sha256(w.formed.header), commit), w.formed.origin),
            "untrusted",
        )
        return h
    }

    fun open(
        s: State,
        head: State,
        live: Boolean,
        now: ULong,
        w: Wire,
        secret: ByteArray,
        seed: ByteArray?,
    ): Pair<Header, ByteArray> {
        val h = verifyOrigin(s, head, live, now, w)
        val aes = AESCipher.getInstance("AES/GCM/NoPadding")
        aes.init(
            AESCipher.DECRYPT_MODE,
            SecretKeySpec(contentKey(secret, seed, h), "AES"),
            GCMParameterSpec(128, w.formed.nonce),
        )
        aes.updateAAD(w.formed.header)
        return h to aes.doFinal(w.formed.cipher)
    }

    fun wireBytes(w: Wire): ByteArray {
        commitment(w.formed)
        check(w.formed.origin.size == 64)
        return Writer()
            .lp(w.formed.header)
            .lp(w.formed.nonce)
            .lp(w.formed.cipher)
            .lp(w.formed.origin)
            .bytes()
    }

    fun parseWire(b: ByteArray): Wire {
        check(b.size <= MAX_CIPHER + 4096)
        val r = Reader(b)
        val w = Wire(Formed(r.lp(MAX_HEADER), r.lp(12), r.lp(MAX_CIPHER), r.lp(64)))
        r.done()
        wireBytes(w)
        return w
    }

    fun envelope(w: Wire): Envelope {
        val h = parseHeader(w.formed.header)
        wireBytes(w)
        val ph =
            PBHeader.newBuilder()
                .setVersion(VERSION)
                .setKindValue(h.kind.toInt())
                .setMachineId(h.machine)
                .setSessionId(h.session)
                .setRequestId(h.request)
                .setSeq(h.seq.toLong())
                .setDeadlineMs(h.deadline.toLong())
                .setSender(h.sender)
                .setCompressionValue(h.compression.toInt())
                .build()
        return Envelope.newBuilder()
            .setHeader(ph.toByteString())
            .setNonce(ByteString.copyFrom(w.formed.nonce))
            .setCiphertext(ByteString.copyFrom(w.formed.cipher))
            .setSignature(ByteString.copyFrom(w.formed.origin))
            .setSecurityHeader(ByteString.copyFrom(w.formed.header))
            .build()
    }

    fun fromEnvelope(e: Envelope): Wire {
        check(e.header.size() <= MAX_HEADER)
        val w =
            Wire(
                Formed(
                    e.securityHeader.toByteArray(),
                    e.nonce.toByteArray(),
                    e.ciphertext.toByteArray(),
                    e.signature.toByteArray(),
                ),
            )
        val h = parseHeader(w.formed.header)
        val p = PBHeader.parseFrom(e.header)
        check(
            p.version == VERSION &&
                p.kindValue == h.kind.toInt() &&
                p.machineId == h.machine &&
                p.sessionId == h.session &&
                p.requestId == h.request &&
                p.seq.toULong() == h.seq &&
                p.deadlineMs.toULong() == h.deadline &&
                p.sender == h.sender &&
                p.compressionValue == h.compression.toInt(),
            "untrusted",
        )
        wireBytes(w)
        return w
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
        fun signBytes(): ByteArray {
            check(
                id(account) &&
                    domain.size == 32 &&
                    !domain.contentEquals(zero) &&
                    parent.size == 32 &&
                    !parent.contentEquals(zero) &&
                    id(newAuthorityID) &&
                    newAuthorityID == newMember.id &&
                    newMember.kind == 2u &&
                    id(newMember.id) &&
                    deviceId(newMember.ed) == newMember.id &&
                    newMember.x.size == 32 &&
                    !newMember.x.contentEquals(zero) &&
                    newRecoveryEd.size == 32 &&
                    !newRecoveryEd.contentEquals(zero) &&
                    issuedAt > 0uL &&
                    stateHash.size == 32 &&
                    !stateHash.contentEquals(zero) &&
                    nonce.size == 32 &&
                    !nonce.contentEquals(zero),
            )
            return Writer()
                .raw("ra3/recovery\u0000".toByteArray())
                .str(account)
                .raw(domain)
                .raw(parent)
                .str(newAuthorityID)
                .raw(newRecoveryEd)
                .str(newMember.id)
                .raw(newMember.ed)
                .raw(newMember.x)
                .u32(newMember.kind)
                .u64(issuedAt)
                .raw(stateHash)
                .raw(nonce)
                .bytes()
        }

        fun verify(parent: State, next: State) {
            val b = signBytes()
            check(signature.size == 64)
            val nextAuthority = next.core.member(next.core.authorityID)
            check(
                nextAuthority != null &&
                    account == parent.core.account &&
                    domain.contentEquals(parent.core.domain) &&
                    this.parent.contentEquals(stateHash(parent)) &&
                    newAuthorityID == next.core.authorityID &&
                    newRecoveryEd.contentEquals(next.core.recoveryEd) &&
                    newMember.same(nextAuthority) &&
                    issuedAt == next.core.issuedAt &&
                    stateHash.contentEquals(stateHash(next)) &&
                    verifyEd25519(parent.core.recoveryEd, b, signature),
                "untrusted",
            )
        }

        fun marshal(): ByteArray {
            val body = signBytes()
            check(signature.size == 64)
            return Writer().lp(body).lp(signature).bytes()
        }
    }

    fun signRecoveryStatement(
        stmt: RecoveryStatement,
        oldRecoverySeed: ByteArray,
    ): RecoveryStatement {
        check(oldRecoverySeed.size == 32)
        return stmt.copy(signature = SigningKey(oldRecoverySeed).sign(stmt.signBytes()))
    }

    fun parseRecoveryStatement(b: ByteArray): RecoveryStatement {
        check(b.size <= MAX_STATE)
        val r = Reader(b)
        val body = r.lp(MAX_STATE)
        val sig = r.lp(64)
        r.done()
        check(sig.size == 64)
        val label = "ra3/recovery\u0000".toByteArray()
        check(body.size >= label.size && body.copyOf(label.size).contentEquals(label))
        val s = Reader(body.copyOfRange(label.size, body.size))
        val stmt =
            RecoveryStatement(
                s.str(),
                s.raw(32),
                s.raw(32),
                s.str(),
                s.raw(32),
                Member(s.str(), s.raw(32), s.raw(32), s.u32()),
                s.u64(),
                s.raw(32),
                s.raw(32),
                sig,
            )
        s.done()
        check(stmt.signBytes().contentEquals(body), "untrusted")
        return stmt
    }

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
            check(pin.account.isNotEmpty() && pin.domain.size == 32 && pin.genesis.size == 32)
            check(chain.isNotEmpty() && chain.size <= MAX_STATES)
            verifyGenesis(pin, chain[0])
            chain.forEachIndexed { i, s ->
                verifyState(pin, s)
                if (i > 0) {
                    val proof = proofs[s.core.epoch]?.let { parseRecoveryStatement(it) }
                    verifyTransition(chain[i - 1], s, proof)
                }
            }
            check(id(memberID) && xPrivate.size == 32)
            lastMember()?.let { openGrant(pin, it, memberID, xPrivate) }
        }

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
            var secret = lastMember()?.let { secret() }
            states.forEachIndexed { i, next ->
                verifyState(pin, next)
                val existing = all.firstOrNull { it.core.epoch == next.core.epoch }
                if (existing != null) {
                    check(stateBytes(existing).contentEquals(stateBytes(next)), "stale")
                    return@forEachIndexed
                }
                check(next.core.epoch == all.last().core.epoch + 1uL, "stale")
                val rawProof = incoming[i]
                val proof = rawProof?.let { parseRecoveryStatement(it) }
                verifyTransition(all.last(), next, proof)
                if (rawProof != null) newProofs[next.core.epoch] = rawProof
                val prev = all.last()
                if (next.core.member(memberID) != null) {
                    val key = openGrant(pin, next, memberID, xPrivate)
                    if (next.core.contentEpoch != prev.core.contentEpoch) {
                        // A renewal may carry a changed grant (accepted, as Go
                        // does); a content-epoch change must open the previous
                        // key unless a recovery proves it has none.
                        if (proof == null) {
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
                all.add(next)
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
                chain.forEachIndexed { i, s ->
                    if (i > 0) {
                        val proof = proofs[i]?.let { parseRecoveryStatement(it) }
                        verifyTransition(chain[i - 1], s, proof)
                    }
                }
                val proofMap = HashMap<ULong, ByteArray>()
                chain.forEachIndexed { i, s -> proofs[i]?.let { proofMap[s.core.epoch] = it } }
                return Record(Pin(account, domain, genesis), chain, memberID, x, proofs = proofMap)
            }
        }
    }

    interface Storage {
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
    ): ByteArray {
        check(secret.size == 32 && id(account) && epoch > 0uL)
        nonzero(domain)
        return hkdf(
            secret,
            null,
            Writer()
                .raw("ra3/purpose-key\u0000".toByteArray())
                .str(account)
                .raw(domain)
                .u64(epoch)
                .u32(purpose.value)
                .bytes(),
            32,
        )
    }

    private fun messageKeyFromPurpose(child: ByteArray, h: Header): ByteArray {
        check(child.size == 32)
        validate(h)
        val info =
            Writer()
                .raw("ra3/content-key\u0000".toByteArray())
                .str(h.account)
                .raw(h.domain)
                .u64(h.epoch)
                .raw(h.state)
                .u64(h.contentEpoch)
                .u32(h.purpose.value)
                .str(h.sender)
                .str(h.machine)
                .str(h.session)
                .str(h.request)
                .u64(h.seq)
                .bytes()
        return hkdf(child, null, info, 32)
    }

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

        fun open(env: Envelope): io.github.lewin671.remoteagent.proto.NotifyContent {
            val w = fromEnvelope(env)
            val h = verifyOrigin(state, state, false, 0uL, w)
            check(h.purpose == Purpose.NOTIFY, "untrusted")
            val aes = AESCipher.getInstance("AES/GCM/NoPadding")
            aes.init(
                AESCipher.DECRYPT_MODE,
                SecretKeySpec(messageKeyFromPurpose(child, h), "AES"),
                GCMParameterSpec(128, w.formed.nonce),
            )
            aes.updateAAD(w.formed.header)
            val p = unpackSecurity(h, aes.doFinal(w.formed.cipher))
            val content = io.github.lewin671.remoteagent.proto.NotifyContent.parseFrom(p)
            validateSecurityBody(h, content)
            return content
        }

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
