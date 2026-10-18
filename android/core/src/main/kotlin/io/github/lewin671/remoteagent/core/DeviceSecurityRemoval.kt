package io.github.lewin671.remoteagent.core

import java.util.Base64
import kotlinx.serialization.json.*

/**
 * Public completion evidence is checked independently of the authority reply: exact original
 * operation, contiguous head, and ALL retained-machine ACKs.
 */
object DeviceSecurityRemoval {
    private fun must(ok: Boolean) {
        if (!ok) throw DeviceSecurity.Failure("untrusted")
    }

    private fun id(s: String) = s.matches(Regex("[0-9a-f]{32}"))

    private fun opaque(s: String) = s.matches(Regex("[A-Za-z0-9_:-]{16,128}"))

    private fun text(s: String) = s.toByteArray(Charsets.UTF_8)

    private fun s(o: JsonObject, k: String) = o[k]!!.jsonPrimitive.content

    private fun h(v: JsonElement?): ByteArray {
        val a = v!!.jsonArray
        must(a.size == 32)
        return a.map {
                val n = it.jsonPrimitive.int
                must(n in 0..255)
                n.toByte()
            }
            .toByteArray()
    }

    private fun b(o: JsonObject, k: String): ByteArray {
        val value = s(o, k)
        must(value.length <= DeviceSecurity.MAX_RPC * 2)
        val out = Base64.getDecoder().decode(value)
        must(Base64.getEncoder().encodeToString(out) == value)
        return out
    }

    private fun nonzero(b: ByteArray) = must(b.size == 32 && b.any { it != 0.toByte() })

    fun deleteBytes(r: JsonObject): ByteArray {
        val account = s(r, "Account")
        val requester = s(r, "Requester")
        val operation = s(r, "Operation")
        must(id(account) && id(requester) && opaque(operation))
        val domain = h(r["Domain"])
        val expected = h(r["Expected"])
        val nonce = h(r["Nonce"])
        nonzero(domain)
        nonzero(expected)
        nonzero(nonce)
        val exact = r["ExactIDs"]!!.jsonPrimitive.boolean
        val targets = r["Targets"]!!.jsonArray
        must(targets.size in 1..128)
        val w =
            DeviceSecurity.Writer()
                .raw(text("ra2/delete\u0000"))
                .str(account)
                .raw(domain)
                .str(requester)
                .str(operation)
                .raw(expected)
                .raw(nonce)
                .u32(if (exact) 1u else 0u)
                .u32(targets.size.toUInt())
        var previous = ""
        for (v in targets) {
            val t = v.jsonObject
            val target = s(t, "ID")
            must(id(target) && (previous.isEmpty() || previous < target))
            val commit = h(t["MetadataCommit"])
            if (!exact) nonzero(commit)
            w.str(target).raw(commit)
            previous = target
        }
        return w.bytes()
    }

    private fun digest(r: JsonObject) =
        sha256(text("ra2/delete-digest\u0000"), deleteBytes(r), b(r, "Signature"))

    fun prepare(
        pin: DeviceSecurity.Pin,
        requester: String,
        target: String,
        expected: ByteArray,
        commit: ByteArray,
        ed: SigningKey,
    ): JsonObject {
        must(deviceId(ed.publicKey) == requester)
        val body = buildJsonObject {
            put("Account", pin.account)
            put("Domain", array(pin.domain))
            put("Requester", requester)
            put("Operation", randomBytes(16).hex())
            put("Expected", array(expected))
            put("Nonce", array(randomBytes(32)))
            put("ExactIDs", false)
            put(
                "Targets",
                buildJsonArray {
                    add(
                        buildJsonObject {
                            put("ID", target)
                            put("MetadataCommit", array(commit))
                        },
                    )
                },
            )
        }
        val sig = ed.sign(deleteBytes(body))
        return JsonObject(
            body + mapOf("Signature" to JsonPrimitive(Base64.getEncoder().encodeToString(sig))),
        )
    }

    private fun array(b: ByteArray) = JsonArray(b.map { JsonPrimitive(it.toUByte().toInt()) })

    fun unnamedCommit(state: DeviceSecurity.State, m: DeviceSecurity.Member) =
        sha256(
            text("ra2/unnamed-member\u0000"),
            DeviceSecurity.Writer()
                .raw(DeviceSecurity.stateHash(state))
                .str(m.id)
                .raw(m.ed)
                .raw(m.x)
                .u32(m.kind)
                .bytes(),
        )

    private fun fenceBytes(r: JsonObject): ByteArray {
        val account = s(r, "Account")
        val authority = s(r, "AuthorityID")
        val executor = s(r, "Executor")
        val group = s(r, "Group")
        val rev = r["Revision"]!!.jsonPrimitive.long
        val epoch = r["Epoch"]!!.jsonPrimitive.long
        must(
            id(account) &&
                id(authority) &&
                id(executor) &&
                opaque(group) &&
                rev in 1..128 &&
                epoch >= 2,
        )
        val domain = h(r["Domain"])
        val state = h(r["State"])
        nonzero(domain)
        nonzero(state)
        return DeviceSecurity.Writer()
            .raw(text("ra2/fence-receipt\u0000"))
            .str(account)
            .raw(domain)
            .str(authority)
            .str(group)
            .u64(rev.toULong())
            .u64(epoch.toULong())
            .raw(state)
            .str(executor)
            .bytes()
    }

    fun verify(
        pin: DeviceSecurity.Pin,
        completion: JsonElement,
        original: JsonObject,
        chain: List<DeviceSecurity.State>,
    ) {
        try {
            val c = completion.jsonObject
            val g = c["Group"]!!.jsonObject
            val group = s(g, "ID")
            val ops = g["Operations"]!!.jsonArray
            must(
                g["Complete"]!!.jsonPrimitive.boolean &&
                    opaque(group) &&
                    ops.size in 1..128 &&
                    chain.size == ops.size,
            )
            val base = DeviceSecurityRPC.stateFromJSON(g["Base"]!!)
            val head = DeviceSecurityRPC.stateFromJSON(g["Head"]!!)
            DeviceSecurity.verifyState(pin, base)
            DeviceSecurity.verifyState(pin, head)
            val bh = DeviceSecurity.stateHash(base)
            val hh = DeviceSecurity.stateHash(head)
            val required =
                base.core.members
                    .filter { m -> m.kind == 1u && head.core.members.any { it.id == m.id } }
                    .map { it.id }
            must(g["Required"]!!.jsonArray.map { it.jsonPrimitive.content } == required)
            val receipts = g["Receipts"]!!.jsonArray
            must(receipts.size == required.size)
            val w =
                DeviceSecurity.Writer()
                    .raw(text("ra2/completion\u0000"))
                    .str(pin.account)
                    .raw(pin.domain)
                    .str(pin.authorityID)
                    .str(group)
                    .raw(bh)
                    .raw(hh)
                    .u32(ops.size.toUInt())
            for (op in ops) {
                val o = op.jsonObject
                w.str(s(o["Request"]!!.jsonObject, "Operation"))
                    .raw(h(o["Digest"]))
                    .raw(h(o["NewState"]))
            }
            w.u32(required.size.toUInt())
            receipts.forEachIndexed { i, v ->
                val r = v.jsonObject
                val executor = s(r, "Executor")
                val member =
                    head.core.members.firstOrNull { it.id == executor }
                        ?: throw DeviceSecurity.Failure("untrusted")
                must(
                    member.kind == 1u &&
                        executor == required[i] &&
                        s(r, "Account") == pin.account &&
                        h(r["Domain"]).contentEquals(pin.domain) &&
                        s(r, "AuthorityID") == pin.authorityID &&
                        s(r, "Group") == group &&
                        r["Revision"]!!.jsonPrimitive.int == ops.size &&
                        r["Epoch"]!!.jsonPrimitive.long.toULong() == head.core.epoch &&
                        h(r["State"]).contentEquals(hh),
                )
                val fb = fenceBytes(r)
                val sig = b(r, "Signature")
                must(verifyEd25519(member.ed, fb, sig))
                w.str(executor).lp(fb).lp(sig)
            }
            must(verifyEd25519(pin.authorityEd, w.bytes(), b(c, "Signature")))
            var prior = base
            var found = false
            val removed = HashSet<String>()
            val ownDigest = digest(original)
            ops.forEachIndexed { i, v ->
                val o = v.jsonObject
                val r = o["Request"]!!.jsonObject
                val next = chain[i]
                DeviceSecurity.advance(pin, prior, next)
                must(
                    DeviceSecurity.stateHash(prior).contentEquals(h(r["Expected"])) &&
                        DeviceSecurity.stateHash(next).contentEquals(h(o["NewState"])) &&
                        s(r, "Account") == pin.account &&
                        h(r["Domain"]).contentEquals(pin.domain),
                )
                val requester =
                    prior.core.members.firstOrNull { it.id == s(r, "Requester") }
                        ?: throw DeviceSecurity.Failure("untrusted")
                must(verifyEd25519(requester.ed, deleteBytes(r), b(r, "Signature")))
                val dh = digest(r)
                must(dh.contentEquals(h(o["Digest"])))
                val ids = r["Targets"]!!.jsonArray.map { s(it.jsonObject, "ID") }
                must(ids == next.core.removed)
                removed.addAll(ids)
                if (s(r, "Operation") == s(original, "Operation")) {
                    must(dh.contentEquals(ownDigest))
                    found = true
                }
                prior = next
            }
            must(found && DeviceSecurity.stateHash(prior).contentEquals(hh))
            for (id in removed) must(head.core.members.none { it.id == id })
        } catch (e: DeviceSecurity.Failure) {
            throw e
        } catch (_: Exception) {
            throw DeviceSecurity.Failure("untrusted")
        }
    }
}
