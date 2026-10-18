package io.github.lewin671.remoteagent.core

import java.util.Base64
import kotlinx.serialization.json.*
import org.bouncycastle.crypto.AsymmetricCipherKeyPair
import org.bouncycastle.crypto.hpke.HPKE
import org.bouncycastle.crypto.params.X25519PrivateKeyParameters
import org.bouncycastle.crypto.params.X25519PublicKeyParameters

/**
 * HPKE Base does not authenticate a sender. Requests and responses each carry an independent scoped
 * signature and an exact request hash and random nonce.
 */
object DeviceSecurityRPC {
    data class Capsule(
        val from: String,
        val to: String,
        val method: UInt,
        val enc: ByteArray,
        val cipher: ByteArray,
    )

    data class Waiter(
        val pin: DeviceSecurity.Pin,
        val sender: String,
        val method: UInt,
        val nonce: ByteArray,
        val requestHash: ByteArray,
    )

    private fun check(ok: Boolean, code: String = "invalid") {
        if (!ok) throw DeviceSecurity.Failure(code)
    }

    private fun id(s: String) = s.matches(Regex("[0-9a-f]{32}"))

    private fun text(s: String) = s.toByteArray(Charsets.UTF_8)

    private fun b64(b: ByteArray) = Base64.getEncoder().encodeToString(b)

    private fun unb64(s: String): ByteArray {
        check(s.length <= DeviceSecurity.MAX_RPC * 2)
        return try {
            Base64.getDecoder().decode(s).also { check(b64(it) == s) }
        } catch (_: IllegalArgumentException) {
            throw DeviceSecurity.Failure()
        }
    }

    private fun json(b: ByteArray): JsonElement {
        check(b.size <= DeviceSecurity.MAX_RPC)
        return try {
            Json.parseToJsonElement(b.toString(Charsets.UTF_8))
        } catch (_: Exception) {
            throw DeviceSecurity.Failure()
        }
    }

    private fun hash(a: JsonElement?): ByteArray {
        try {
            val arr = a!!.jsonArray
            check(arr.size == 32)
            return arr.map {
                    val n = it.jsonPrimitive.int
                    check(n in 0..255)
                    n.toByte()
                }
                .toByteArray()
        } catch (_: Exception) {
            throw DeviceSecurity.Failure()
        }
    }

    private fun hpke() =
        HPKE(HPKE.mode_base, HPKE.kem_X25519_SHA256, HPKE.kdf_HKDF_SHA256, HPKE.aead_AES_GCM256)

    fun capsuleBytes(c: Capsule): ByteArray {
        check(
            id(c.from) &&
                id(c.to) &&
                c.method in 1u..12u &&
                c.enc.size == 32 &&
                c.cipher.size in 16..DeviceSecurity.MAX_RPC,
        )
        return DeviceSecurity.Writer()
            .str(c.from)
            .str(c.to)
            .u32(c.method)
            .lp(c.enc)
            .lp(c.cipher)
            .bytes()
    }

    fun parseCapsule(b: ByteArray): Capsule {
        check(b.size <= DeviceSecurity.MAX_RPC + 512)
        val r = DeviceSecurity.Reader(b)
        val c = Capsule(r.str(), r.str(), r.u32(), r.lp(32), r.lp(DeviceSecurity.MAX_RPC))
        r.done()
        capsuleBytes(c)
        return c
    }

    private fun context(
        p: DeviceSecurity.Pin,
        from: String,
        to: String,
        method: UInt,
        response: Boolean,
    ) =
        DeviceSecurity.Writer()
            .raw(text("ra2/rpc\u0000"))
            .str(p.account)
            .raw(p.domain)
            .str(p.authorityID)
            .str(from)
            .str(to)
            .u32(method)
            .u32(if (response) 1u else 0u)
            .bytes()

    fun request(
        p: DeviceSecurity.Pin,
        sender: String,
        ed: SigningKey,
        method: UInt,
        body: JsonElement,
    ): Pair<Capsule, Waiter> {
        check(id(sender) && deviceId(ed.publicKey) == sender && method in 1u..12u)
        val bodyBytes = text(body.toString())
        check(bodyBytes.size <= DeviceSecurity.MAX_RPC - 1024)
        val nonce = randomBytes(32)
        val ctx = context(p, sender, p.authorityID, method, false)
        val signed =
            DeviceSecurity.Writer()
                .raw(text("ra2/auth-request\u0000"))
                .raw(ctx)
                .raw(nonce)
                .lp(bodyBytes)
                .bytes()
        val signature = ed.sign(signed)
        val requestHash = sha256(text("ra2/request-digest\u0000"), signed, signature)
        val plain =
            text(
                buildJsonObject {
                    put("Nonce", JsonArray(nonce.map { JsonPrimitive(it.toUByte().toInt()) }))
                    put("Body", b64(bodyBytes))
                    put("Signature", b64(signature))
                }
                    .toString(),
            )
        check(plain.size <= DeviceSecurity.MAX_RPC - 16)
        val hp =
            hpke()
                .setupBaseS(
                    X25519PublicKeyParameters(p.authorityX, 0),
                    text("ra2/rpc-info\u0000") + ctx,
                )
        val cipher = hp.seal(text("ra2/rpc-aad\u0000") + ctx, plain)
        return Capsule(sender, p.authorityID, method, hp.encapsulation, cipher) to
            Waiter(p, sender, method, nonce, requestHash)
    }

    fun reply(w: Waiter, c: Capsule, xPrivate: ByteArray): JsonElement {
        capsuleBytes(c)
        check(c.from == w.pin.authorityID && c.to == w.sender && c.method == w.method, "untrusted")
        check(xPrivate.size == 32)
        val ctx = context(w.pin, c.from, c.to, c.method, true)
        val x = X25519PrivateKeyParameters(xPrivate, 0)
        val hp =
            hpke()
                .setupBaseR(
                    c.enc,
                    AsymmetricCipherKeyPair(x.generatePublicKey(), x),
                    text("ra2/rpc-info\u0000") + ctx,
                )
        val plain = hp.open(text("ra2/rpc-aad\u0000") + ctx, c.cipher)
        // Do not propagate parser messages containing private JSON/key material.
        try {
            val r = json(plain).jsonObject
            val nonce = hash(r["Nonce"])
            val requestHash = hash(r["RequestHash"])
            val error = r["Error"]!!.jsonPrimitive.content
            check(error.length <= 32)
            val signature = unb64(r["Signature"]!!.jsonPrimitive.content)
            val body =
                if (r["Body"] == JsonNull || r["Body"] == null) ByteArray(0)
                else unb64(r["Body"]!!.jsonPrimitive.content)
            check(body.size <= DeviceSecurity.MAX_RPC - 1024)
            check(
                nonce.contentEquals(w.nonce) && requestHash.contentEquals(w.requestHash),
                "untrusted",
            )
            val signed =
                DeviceSecurity.Writer()
                    .raw(text("ra2/auth-response\u0000"))
                    .raw(ctx)
                    .raw(nonce)
                    .raw(requestHash)
                    .str(error)
                    .lp(body)
                    .bytes()
            check(verifyEd25519(w.pin.authorityEd, signed, signature), "untrusted")
            if (error.isNotEmpty())
                throw DeviceSecurity.Failure(
                    if (error in setOf("stale", "conflict", "pending", "capacity", "storage")) error
                    else "untrusted",
                )
            return json(body)
        } catch (e: DeviceSecurity.Failure) {
            throw e
        } catch (_: Exception) {
            throw DeviceSecurity.Failure()
        }
    }

    fun authorizationRequest(c: DeviceSecurity.Formed): JsonObject {
        val h = DeviceSecurity.parseHeader(c.header)
        val commit = DeviceSecurity.commitment(c)
        val formed = buildJsonObject {
            put("Header", b64(c.header))
            put("Nonce", b64(c.nonce))
            put("Cipher", b64(c.cipher))
            put("Origin", b64(c.origin))
        }
        return buildJsonObject {
            put("Header", b64(c.header))
            put("Commit", JsonArray(commit.map { JsonPrimitive(it.toUByte().toInt()) }))
            put("Origin", b64(c.origin))
            put("Cipher", if (h.purpose.mutable) formed else JsonNull)
        }
    }

    fun attach(
        c: DeviceSecurity.Formed,
        reply: JsonElement,
        p: DeviceSecurity.Pin,
        state: DeviceSecurity.State,
    ): DeviceSecurity.Wire {
        try {
            val r = reply.jsonObject
            val w =
                DeviceSecurity.Wire(
                    c,
                    unb64(r["Authorization"]!!.jsonPrimitive.content),
                    unb64(r["Signature"]!!.jsonPrimitive.content),
                )
            DeviceSecurity.verifyWire(p, state, w)
            return w
        } catch (e: DeviceSecurity.Failure) {
            throw e
        } catch (_: Exception) {
            throw DeviceSecurity.Failure()
        }
    }

    fun stateFromJSON(value: JsonElement): DeviceSecurity.State {
        try {
            val s = value.jsonObject
            val c = s["Core"]!!.jsonObject
            val epoch = c["Epoch"]!!.jsonPrimitive.long
            val ce = c["ContentEpoch"]!!.jsonPrimitive.long
            check(epoch in 1..32768 && ce == epoch)
            val members = c["Members"]!!.jsonArray
            val grants = s["Grants"]!!.jsonArray
            check(members.size <= 128 && grants.size <= 128)
            val ms = members.map {
                val m = it.jsonObject
                val kind = m["Kind"]!!.jsonPrimitive.int
                check(kind == 1 || kind == 2)
                DeviceSecurity.Member(
                    m["ID"]!!.jsonPrimitive.content,
                    hash(m["EdPublic"]),
                    hash(m["XPublic"]),
                    kind.toUInt(),
                )
            }
            val removed =
                if (c["Removed"] == null || c["Removed"] == JsonNull) emptyList()
                else c["Removed"]!!.jsonArray.map { it.jsonPrimitive.content }
            val core =
                DeviceSecurity.Core(
                    c["Account"]!!.jsonPrimitive.content,
                    hash(c["Domain"]),
                    c["AuthorityID"]!!.jsonPrimitive.content,
                    hash(c["AuthorityEd"]),
                    hash(c["AuthorityX"]),
                    hash(c["Parent"]),
                    epoch.toULong(),
                    ce.toULong(),
                    ms,
                    removed,
                )
            val gs = grants.map {
                val g = it.jsonObject
                DeviceSecurity.Grant(
                    g["Recipient"]!!.jsonPrimitive.content,
                    hash(g["Enc"]),
                    unb64(g["Cipher"]!!.jsonPrimitive.content),
                )
            }
            return DeviceSecurity.State(core, gs, unb64(s["Signature"]!!.jsonPrimitive.content))
                .also { DeviceSecurity.stateBytes(it) }
        } catch (e: DeviceSecurity.Failure) {
            throw e
        } catch (_: Exception) {
            throw DeviceSecurity.Failure()
        }
    }

    fun wireFromJSON(value: JsonElement): DeviceSecurity.Wire {
        try {
            val o = value.jsonObject
            val w =
                DeviceSecurity.Wire(
                    DeviceSecurity.Formed(
                        unb64(o["Header"]!!.jsonPrimitive.content),
                        unb64(o["Nonce"]!!.jsonPrimitive.content),
                        unb64(o["Cipher"]!!.jsonPrimitive.content),
                        unb64(o["Origin"]!!.jsonPrimitive.content),
                    ),
                    unb64(o["Authorization"]!!.jsonPrimitive.content),
                    unb64(o["AuthSignature"]!!.jsonPrimitive.content),
                )
            DeviceSecurity.wireBytes(w)
            return w
        } catch (e: DeviceSecurity.Failure) {
            throw e
        } catch (_: Exception) {
            throw DeviceSecurity.Failure()
        }
    }

    data class PairContext(
        val id: String,
        val pin: DeviceSecurity.Pin,
        val approver: String,
        val candidate: DeviceSecurity.Member,
        val expected: ByteArray,
        val nonce: ByteArray,
        val expires: ULong,
        val signature: ByteArray,
    )

    fun pairContextFromJSON(value: JsonElement): PairContext {
        try {
            val o = value.jsonObject
            val c = o["Candidate"]!!.jsonObject
            val kind = c["Kind"]!!.jsonPrimitive.int
            check(kind == 1 || kind == 2)
            val p =
                DeviceSecurity.Pin(
                    o["Account"]!!.jsonPrimitive.content,
                    hash(o["Domain"]),
                    hash(o["Genesis"]),
                    o["AuthorityID"]!!.jsonPrimitive.content,
                    hash(o["AuthorityEd"]),
                    hash(o["AuthorityX"]),
                )
            val result =
                PairContext(
                    o["ID"]!!.jsonPrimitive.content,
                    p,
                    o["Approver"]!!.jsonPrimitive.content,
                    DeviceSecurity.Member(
                        c["ID"]!!.jsonPrimitive.content,
                        hash(c["EdPublic"]),
                        hash(c["XPublic"]),
                        kind.toUInt(),
                    ),
                    hash(o["Expected"]),
                    hash(o["Nonce"]),
                    o["Expires"]!!.jsonPrimitive.content.toULong(),
                    unb64(o["Signature"]!!.jsonPrimitive.content),
                )
            pairBinding(result)
            return result
        } catch (e: DeviceSecurity.Failure) {
            throw e
        } catch (_: Exception) {
            throw DeviceSecurity.Failure()
        }
    }

    private fun nonzero(b: ByteArray) = check(b.size == 32 && b.any { it != 0.toByte() })

    fun pairTrustDescriptor(p: DeviceSecurity.Pin, approver: String): ByteArray {
        check(id(p.account) && id(p.authorityID) && id(approver))
        nonzero(p.domain)
        nonzero(p.genesis)
        nonzero(p.authorityEd)
        nonzero(p.authorityX)
        return DeviceSecurity.Writer()
            .raw(text("ra2/pair-trust\u0000"))
            .str(p.account)
            .raw(p.domain)
            .raw(p.genesis)
            .str(p.authorityID)
            .raw(p.authorityEd)
            .raw(p.authorityX)
            .str(approver)
            .bytes()
    }

    fun pairDomainCommit(ephemeral: ByteArray, trust: ByteArray): ByteArray {
        check(ephemeral.size == 32)
        return sha256(
            text("ra2/pair-commit\u0000"),
            DeviceSecurity.Writer().lp(ephemeral).lp(trust).bytes(),
        )
    }

    fun pairBinding(c: PairContext): ByteArray {
        check(
            c.id.matches(Regex("[A-Za-z0-9_:-]{16,128}")) &&
                id(c.approver) &&
                c.expires > 0uL &&
                id(c.candidate.id) &&
                c.candidate.ed.size == 32 &&
                deviceId(c.candidate.ed) == c.candidate.id &&
                (c.candidate.kind == 1u || c.candidate.kind == 2u),
        )
        nonzero(c.expected)
        nonzero(c.nonce)
        nonzero(c.candidate.x)
        pairTrustDescriptor(c.pin, c.approver)
        return DeviceSecurity.Writer()
            .raw(text("ra2/pair-context\u0000"))
            .str(c.id)
            .str(c.pin.account)
            .raw(c.pin.domain)
            .raw(c.pin.genesis)
            .str(c.pin.authorityID)
            .raw(c.pin.authorityEd)
            .raw(c.pin.authorityX)
            .str(c.approver)
            .str(c.candidate.id)
            .raw(c.candidate.ed)
            .raw(c.candidate.x)
            .u32(c.candidate.kind)
            .raw(c.expected)
            .raw(c.nonce)
            .u64(c.expires)
            .bytes()
    }

    fun verifyPairContext(c: PairContext, p: DeviceSecurity.Pin) {
        check(
            pairTrustDescriptor(c.pin, c.approver)
                .contentEquals(pairTrustDescriptor(p, c.approver)) &&
                verifyEd25519(p.authorityEd, pairBinding(c), c.signature),
            "untrusted",
        )
    }

    fun signPairContext(c: PairContext, ed: SigningKey, action: String): ByteArray {
        check(
            action in setOf("approve", "accept", "cancel") &&
                deviceId(ed.publicKey) == if (action == "accept") c.candidate.id else c.approver,
        )
        return ed.sign(text("ra2/pair-$action\u0000") + pairBinding(c))
    }
}
