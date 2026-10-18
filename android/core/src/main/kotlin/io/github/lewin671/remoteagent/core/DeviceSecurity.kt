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
 * Trust is SAS-pinned on endpoints, not inferred from a root-signed relay list. Canonical signed
 * objects match internal/deviceauth and the other clients.
 */
object DeviceSecurity {
    const val MAX_STATE = 64 shl 10
    const val MAX_CIPHER = 4 shl 20
    const val MAX_RPC = 1 shl 20

    class Failure(val code: String = "invalid") : Exception("security: $code")

    private fun check(ok: Boolean, code: String = "invalid") {
        if (!ok) throw Failure(code)
    }

    private val zero = ByteArray(32)

    private fun id(s: String) = s.matches(Regex("[0-9a-f]{32}"))

    private fun opaque(s: String) = s.matches(Regex("[A-Za-z0-9_:-]{1,128}"))

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
            val bytes = lp(128)
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
        val authorityEd: ByteArray,
        val authorityX: ByteArray,
        val parent: ByteArray,
        val epoch: ULong,
        val contentEpoch: ULong,
        val members: List<Member>,
        val removed: List<String>,
    )

    data class Grant(val recipient: String, val enc: ByteArray, val cipher: ByteArray)

    data class State(val core: Core, val grants: List<Grant>, val signature: ByteArray)

    data class Pin(
        val account: String,
        val domain: ByteArray,
        val genesis: ByteArray,
        val authorityID: String,
        val authorityEd: ByteArray,
        val authorityX: ByteArray,
    )

    private fun validate(c: Core) {
        check(
            id(c.account) &&
                id(c.authorityID) &&
                c.epoch > 0uL &&
                c.contentEpoch == c.epoch &&
                c.members.size in 1..128 &&
                c.removed.size <= 128,
        )
        nonzero(c.domain)
        nonzero(c.authorityEd)
        nonzero(c.authorityX)
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
        val a = c.members.firstOrNull { it.id == c.authorityID } ?: throw Failure()
        check(
            a.kind == 1u && !a.ed.contentEquals(c.authorityEd) && !a.x.contentEquals(c.authorityX),
        )
        c.removed.forEachIndexed { i, s ->
            check(id(s) && (i == 0 || c.removed[i - 1] < s) && c.members.none { it.id == s })
        }
        if (c.epoch == 1uL)
            check(c.parent.contentEquals(zero) && c.members.size == 1 && c.removed.isEmpty())
        else nonzero(c.parent)
    }

    fun coreBytes(c: Core): ByteArray {
        validate(c)
        val w =
            Writer()
                .u32(2u)
                .str(c.account)
                .raw(c.domain)
                .str(c.authorityID)
                .raw(c.authorityEd)
                .raw(c.authorityX)
                .raw(c.parent)
                .u64(c.epoch)
                .u64(c.contentEpoch)
                .u32(c.members.size.toUInt())
        c.members.forEach { w.str(it.id).raw(it.ed).raw(it.x).u32(it.kind) }
        w.u32(c.removed.size.toUInt())
        c.removed.forEach { w.str(it) }
        return w.bytes()
    }

    fun parseCore(b: ByteArray): Core {
        check(b.size <= MAX_STATE)
        val r = Reader(b)
        check(r.u32() == 2u)
        val account = r.str()
        val domain = r.raw(32)
        val authorityID = r.str()
        val ed = r.raw(32)
        val x = r.raw(32)
        val parent = r.raw(32)
        val epoch = r.u64()
        val contentEpoch = r.u64()
        var n = r.u32()
        check(n <= 128u)
        val members = List(n.toInt()) { Member(r.str(), r.raw(32), r.raw(32), r.u32()) }
        n = r.u32()
        check(n <= 128u)
        val removed = List(n.toInt()) { r.str() }
        r.done()
        return Core(
                account,
                domain,
                authorityID,
                ed,
                x,
                parent,
                epoch,
                contentEpoch,
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

    fun stateHash(s: State): ByteArray =
        digest(
            "ra2/state\u0000",
            digest("ra2/core\u0000", coreBytes(s.core)),
            digest("ra2/grants\u0000", grantBytes(s)),
        )

    fun stateBytes(s: State): ByteArray {
        check(s.signature.size == 64)
        return Writer().lp(coreBytes(s.core)).raw(grantBytes(s)).lp(s.signature).bytes().also {
            check(it.size <= MAX_STATE)
        }
    }

    fun parseState(b: ByteArray): State {
        check(b.size <= MAX_STATE)
        val r = Reader(b)
        val core = parseCore(r.lp(MAX_STATE))
        val n = r.u32()
        check(n <= 128u)
        val grants = List(n.toInt()) { Grant(r.str(), r.lp(32), r.lp(48)) }
        val s = State(core, grants, r.lp(64))
        r.done()
        stateBytes(s)
        return s
    }

    fun verifyState(p: Pin, s: State) {
        val c = s.core
        check(
            c.account == p.account &&
                c.domain.contentEquals(p.domain) &&
                c.authorityID == p.authorityID &&
                c.authorityEd.contentEquals(p.authorityEd) &&
                c.authorityX.contentEquals(p.authorityX),
            "untrusted",
        )
        nonzero(p.genesis)
        val h = stateHash(s)
        if (c.epoch == 1uL) check(h.contentEquals(p.genesis), "untrusted")
        val b =
            Writer()
                .raw("ra2/state-sig\u0000".toByteArray())
                .str(p.account)
                .raw(p.domain)
                .u64(c.epoch)
                .raw(h)
                .bytes()
        check(verifyEd25519(p.authorityEd, b, s.signature), "untrusted")
    }

    fun advance(p: Pin, old: State, next: State) {
        verifyState(p, old)
        verifyState(p, next)
        check(
            old.core.epoch != ULong.MAX_VALUE &&
                next.core.epoch == old.core.epoch + 1u &&
                next.core.parent.contentEquals(stateHash(old)),
            "stale",
        )
        val removed = mutableListOf<String>()
        var added = 0
        old.core.members.forEach { m ->
            val n = next.core.members.firstOrNull { it.id == m.id }
            if (n == null) removed.add(m.id) else check(m.same(n))
        }
        next.core.members.forEach { m -> if (old.core.members.none { it.id == m.id }) added++ }
        check(
            removed == next.core.removed &&
                added <= 1 &&
                !(added > 0 && removed.isNotEmpty()) &&
                (added > 0 || removed.isNotEmpty()),
        )
    }

    private fun hpke() =
        HPKE(HPKE.mode_base, HPKE.kem_X25519_SHA256, HPKE.kdf_HKDF_SHA256, HPKE.aead_AES_GCM256)

    fun openGrant(p: Pin, s: State, recipient: String, xPrivate: ByteArray): ByteArray {
        verifyState(p, s)
        check(xPrivate.size == 32)
        val x = X25519PrivateKeyParameters(xPrivate, 0)
        val m = s.core.members.firstOrNull { it.id == recipient } ?: throw Failure("untrusted")
        check(m.x.contentEquals(x.generatePublicKey().encoded), "untrusted")
        val g = s.grants.firstOrNull { it.recipient == recipient } ?: throw Failure("untrusted")
        val ctx =
            Writer()
                .str(p.account)
                .raw(p.domain)
                .u64(s.core.contentEpoch)
                .raw(digest("ra2/core\u0000", coreBytes(s.core)))
                .str(recipient)
                .bytes()
        val r =
            hpke()
                .setupBaseR(
                    g.enc,
                    AsymmetricCipherKeyPair(x.generatePublicKey(), x),
                    "ra2/grant-info\u0000".toByteArray() + ctx,
                )
        return r.open("ra2/grant-aad\u0000".toByteArray() + ctx, g.cipher).also {
            check(it.size == 32, "untrusted")
        }
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
        val counter: ULong,
    )

    private fun validate(h: Header) {
        check(
            id(h.account) &&
                id(h.sender) &&
                h.epoch > 0uL &&
                h.contentEpoch == h.epoch &&
                h.counter > 0uL &&
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
            .u32(2u)
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
            .u64(h.counter)
            .bytes()
    }

    fun parseHeader(b: ByteArray): Header {
        check(b.size <= 1024)
        val r = Reader(b)
        check(r.u32() == 2u)
        val account = r.str()
        val domain = r.raw(32)
        val epoch = r.u64()
        val state = r.raw(32)
        val contentEpoch = r.u64()
        val sender = r.str()
        val kind = r.u32()
        val purposeValue = r.u32()
        val purpose = Purpose.entries.firstOrNull { it.value == purposeValue }
        return finishHeader(r, account, domain, epoch, state, contentEpoch, sender, kind, purpose)
    }

    private fun finishHeader(
        r: Reader,
        account: String,
        domain: ByteArray,
        epoch: ULong,
        state: ByteArray,
        contentEpoch: ULong,
        sender: String,
        kind: UInt,
        purpose: Purpose?,
    ): Header {
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
                r.u64(),
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
        val m = s.core.members.firstOrNull { it.id == h.sender } ?: throw Failure("untrusted")
        if (h.machine.isNotEmpty())
            check(s.core.members.any { it.id == h.machine && it.kind == 1u }, "untrusted")
        if (h.purpose == Purpose.DEVICE_META)
            check(s.core.members.any { it.id == h.request && it.kind == 2u }, "untrusted")
        else if (h.purpose != Purpose.PREFS && !h.purpose.control)
            check(m.kind == 1u && m.id == h.machine, "untrusted")
        return m
    }

    data class Formed(
        val header: ByteArray,
        val nonce: ByteArray,
        val cipher: ByteArray,
        val origin: ByteArray,
    )

    data class Wire(val formed: Formed, val authorization: ByteArray, val authSignature: ByteArray)

    fun commitment(c: Formed): ByteArray {
        check(c.header.size <= 1024 && c.nonce.size == 12 && c.cipher.size in 16..MAX_CIPHER)
        parseHeader(c.header)
        return digest(
            "ra2/ciphertext\u0000",
            Writer().lp(c.header).lp(c.nonce).lp(c.cipher).bytes(),
        )
    }

    private fun originBytes(h: Header, hh: ByteArray, commit: ByteArray) =
        Writer()
            .raw("ra2/cipher-origin\u0000".toByteArray())
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
                .raw("ra2/content-key\u0000".toByteArray())
                .str(h.account)
                .raw(h.domain)
                .u64(h.contentEpoch)
                .u32(h.purpose.value)
                .str(h.sender)
                .str(h.machine)
                .str(h.session)
                .str(h.request)
                .u64(h.seq)
                .u64(h.counter)
                .bytes()
        val child = purposeKey(secret, h.account, h.domain, h.contentEpoch, h.purpose)
        return hkdf(child, salt, ctx, 32)
    }

    /** The platform vault must reserve h.counter durably BEFORE this call. */
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

    data class WriteAuth(
        val header: Header,
        val writerKind: UInt,
        val headerHash: ByteArray,
        val commit: ByteArray,
        val nonce: ByteArray,
    )

    fun parseAuthorization(b: ByteArray): WriteAuth {
        check(b.size <= 1152)
        val r = Reader(b)
        val a = WriteAuth(parseHeader(r.lp(1024)), r.u32(), r.raw(32), r.raw(32), r.raw(32))
        r.done()
        check(
            (a.writerKind == 1u || a.writerKind == 2u) &&
                a.headerHash.contentEquals(sha256(headerBytes(a.header))),
        )
        nonzero(a.commit)
        nonzero(a.nonce)
        return a
    }

    fun verifyWire(p: Pin, s: State, w: Wire): Header {
        verifyState(p, s)
        val h = parseHeader(w.formed.header)
        val m = scope(h, s)
        val hh = sha256(w.formed.header)
        val commit = commitment(w.formed)
        check(verifyEd25519(m.ed, originBytes(h, hh, commit), w.formed.origin), "untrusted")
        val a = parseAuthorization(w.authorization)
        check(
            headerBytes(a.header).contentEquals(w.formed.header) &&
                a.writerKind == m.kind &&
                a.headerHash.contentEquals(hh) &&
                a.commit.contentEquals(commit),
            "untrusted",
        )
        check(
            verifyEd25519(
                p.authorityEd,
                "ra2/write-auth\u0000".toByteArray() + w.authorization,
                w.authSignature,
            ),
            "untrusted",
        )
        return h
    }

    /** The body decoder must additionally enforce purpose and frame direction. */
    fun open(
        p: Pin,
        s: State,
        w: Wire,
        secret: ByteArray,
        seed: ByteArray?,
    ): Pair<Header, ByteArray> {
        val h = verifyWire(p, s, w)
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
        parseAuthorization(w.authorization)
        check(w.formed.origin.size == 64 && w.authSignature.size == 64)
        return Writer()
            .lp(w.formed.header)
            .lp(w.formed.nonce)
            .lp(w.formed.cipher)
            .lp(w.formed.origin)
            .lp(w.authorization)
            .lp(w.authSignature)
            .bytes()
    }

    fun parseWire(b: ByteArray): Wire {
        check(b.size <= MAX_CIPHER + 4096)
        val r = Reader(b)
        val w = Wire(Formed(r.lp(1024), r.lp(12), r.lp(MAX_CIPHER), r.lp(64)), r.lp(1152), r.lp(64))
        r.done()
        wireBytes(w)
        return w
    }

    fun envelope(w: Wire): Envelope {
        val h = parseHeader(w.formed.header)
        wireBytes(w)
        val ph =
            PBHeader.newBuilder()
                .setVersion(2)
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
            .setAuthorization(ByteString.copyFrom(w.authorization))
            .setAuthorizationSignature(ByteString.copyFrom(w.authSignature))
            .build()
    }

    fun fromEnvelope(e: Envelope): Wire {
        check(e.header.size() <= 1024)
        val w =
            Wire(
                Formed(
                    e.securityHeader.toByteArray(),
                    e.nonce.toByteArray(),
                    e.ciphertext.toByteArray(),
                    e.signature.toByteArray(),
                ),
                e.authorization.toByteArray(),
                e.authorizationSignature.toByteArray(),
            )
        val h = parseHeader(w.formed.header)
        val p = PBHeader.parseFrom(e.header)
        check(
            p.version == 2 &&
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

    /** Kept only behind the foreground account's unlocked-device key. */
    class Record(
        val pin: Pin,
        val state: State,
        val memberID: String,
        private val xPrivate: ByteArray,
        val counter: ULong = 0uL,
    ) {
        init {
            verifyState(pin, state)
            check(id(memberID) && xPrivate.size == 32)
            openGrant(pin, state, memberID, xPrivate)
        }

        override fun toString() = "DeviceSecurity.Record(epoch=${state.core.epoch})"

        fun secret(): ByteArray = openGrant(pin, state, memberID, xPrivate)

        fun openReply(
            waiter: DeviceSecurityRPC.Waiter,
            capsule: DeviceSecurityRPC.Capsule,
        ): kotlinx.serialization.json.JsonElement {
            check(
                waiter.sender == memberID &&
                    waiter.pin.account == pin.account &&
                    waiter.pin.domain.contentEquals(pin.domain) &&
                    waiter.pin.genesis.contentEquals(pin.genesis) &&
                    waiter.pin.authorityID == pin.authorityID &&
                    waiter.pin.authorityEd.contentEquals(pin.authorityEd) &&
                    waiter.pin.authorityX.contentEquals(pin.authorityX),
                "untrusted",
            )
            return DeviceSecurityRPC.reply(waiter, capsule, xPrivate)
        }

        fun incremented(): Record {
            check(counter < ULong.MAX_VALUE, "capacity")
            return Record(pin, state, memberID, xPrivate, counter + 1u)
        }

        fun advanced(next: State): Record {
            advance(pin, state, next)
            return Record(pin, next, memberID, xPrivate)
        }

        fun bytes(): ByteArray =
            Writer()
                .u32(2u)
                .str(pin.account)
                .raw(pin.domain)
                .raw(pin.genesis)
                .str(pin.authorityID)
                .raw(pin.authorityEd)
                .raw(pin.authorityX)
                .lp(stateBytes(state))
                .str(memberID)
                .raw(xPrivate)
                .u64(counter)
                .bytes()

        companion object {
            fun parse(b: ByteArray): Record {
                check(b.size <= MAX_STATE + 512)
                val r = Reader(b)
                check(r.u32() == 2u)
                val pin = Pin(r.str(), r.raw(32), r.raw(32), r.str(), r.raw(32), r.raw(32))
                val state = parseState(r.lp(MAX_STATE))
                val memberID = r.str()
                val x = r.raw(32)
                val counter = r.u64()
                r.done()
                return Record(pin, state, memberID, x, counter)
            }
        }
    }

    interface Storage {
        fun readSecurity(): Record

        fun initializeSecurity(record: Record)

        fun reserveSecurity(expectedState: ByteArray): Record

        fun advanceSecurity(expectedState: ByteArray, next: State): Record

        fun readSecurityHistory(epoch: ULong): Historical?

        fun cacheSecurityHistory(history: Historical)

        fun readRemovalOperations(): ByteArray?

        fun writeRemovalOperations(data: ByteArray)
    }

    class Historical(val state: State, val secret: ByteArray) {
        init {
            check(secret.size == 32 && state.core.epoch in 1uL..32768uL)
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
                .raw("ra2/purpose-key\u0000".toByteArray())
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
                .raw("ra2/content-key\u0000".toByteArray())
                .str(h.account)
                .raw(h.domain)
                .u64(h.contentEpoch)
                .u32(h.purpose.value)
                .str(h.sender)
                .str(h.machine)
                .str(h.session)
                .str(h.request)
                .u64(h.seq)
                .u64(h.counter)
                .bytes()
        return hkdf(child, null, info, 32)
    }

    class NotifyRecord(val pin: Pin, val state: State, private val child: ByteArray) {
        init {
            verifyState(pin, state)
            check(child.size == 32)
        }

        override fun toString() = "DeviceSecurity.NotifyRecord(epoch=${state.core.contentEpoch})"

        fun bytes(): ByteArray =
            Writer()
                .u32(2u)
                .str(pin.account)
                .raw(pin.domain)
                .raw(pin.genesis)
                .str(pin.authorityID)
                .raw(pin.authorityEd)
                .raw(pin.authorityX)
                .lp(stateBytes(state))
                .raw(child)
                .bytes()

        fun open(env: Envelope): io.github.lewin671.remoteagent.proto.NotifyContent {
            val w = fromEnvelope(env)
            val h = verifyWire(pin, state, w)
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
                    r.state,
                    purposeKey(
                        r.secret(),
                        r.pin.account,
                        r.pin.domain,
                        r.state.core.contentEpoch,
                        Purpose.NOTIFY,
                    ),
                )

            fun parse(b: ByteArray): NotifyRecord {
                check(b.size <= MAX_STATE + 512)
                val r = Reader(b)
                check(r.u32() == 2u)
                val pin = Pin(r.str(), r.raw(32), r.raw(32), r.str(), r.raw(32), r.raw(32))
                val state = parseState(r.lp(MAX_STATE))
                val child = r.raw(32)
                r.done()
                return NotifyRecord(pin, state, child)
            }
        }
    }
}
