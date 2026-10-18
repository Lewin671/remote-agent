// Pairing flows, as in internal/relay/pair.go and web/src/pairing.ts: an
// existing device shows a code, a new one joins with it, both show four
// emoji, and after the user checks them the existing device sends the root
// key sealed for the new one.
package io.github.lewin671.remoteagent.core

import com.connectrpc.Code
import com.connectrpc.ConnectException
import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.proto.ApprovePairingRequest
import io.github.lewin671.remoteagent.proto.CancelPairingRequest
import io.github.lewin671.remoteagent.proto.CreatePairingRequest
import io.github.lewin671.remoteagent.proto.DeviceKind
import io.github.lewin671.remoteagent.proto.GetPairingRequest
import io.github.lewin671.remoteagent.proto.GetPairingResponse
import io.github.lewin671.remoteagent.proto.Header
import io.github.lewin671.remoteagent.proto.JoinPairingRequest
import io.github.lewin671.remoteagent.proto.PairingPayload
import io.github.lewin671.remoteagent.proto.PairingState
import io.github.lewin671.remoteagent.proto.PayloadKind
import io.github.lewin671.remoteagent.proto.RevealPairingRequest
import kotlinx.coroutines.delay
import okhttp3.OkHttpClient
import java.net.URLDecoder
import java.net.URLEncoder
import java.util.Base64

class PairException(message: String) : Exception(message)

/** What a QR code carries: the code, the initiator's commitment and the server. */
data class PairLink(val code: String, val server: String, val commit: ByteArray? = null) {
    /** The link for the web client at [web]; opening it starts joining. */
    fun url(web: String): String {
        val q = buildList {
            add("code=" + enc(code))
            commit?.let { add("c=" + Base64.getUrlEncoder().withoutPadding().encodeToString(it)) }
            add("s=" + enc(server))
        }.joinToString("&")
        return web.trimEnd('/') + "/#/pair?" + q
    }

    companion object {
        private fun enc(s: String) = URLEncoder.encode(s, "UTF-8")

        /** Reads a link made by [url], or by the web client and `remote-agent account pair`. */
        fun parse(raw: String): PairLink? {
            val i = raw.indexOf("#/pair?")
            if (i < 0) return null
            // QR contents are untrusted: malformed percent escapes must not
            // escape into the scanner's main-thread callback.
            try {
                val q = raw.substring(i + 7).split("&").mapNotNull {
                    val kv = it.split("=", limit = 2)
                    if (kv.size == 2) kv[0] to URLDecoder.decode(kv[1], "UTF-8") else null
                }.toMap()
                val code = q["code"] ?: return null
                val server = q["s"] ?: return null
                val commit = q["c"]?.let { Base64.getUrlDecoder().decode(it.trimEnd('=')) }
                if (!Regex("[0-9]{8}").matches(code) || (commit != null && commit.size != 32)) return null
                return PairLink(code, Secret.checkServer(server, allowPlainHttp = true), commit)
            } catch (e: IllegalArgumentException) {
                return null
            }
        }
    }
}

private suspend fun poll(stopped: () -> Boolean, want: PairingState, get: suspend () -> GetPairingResponse): GetPairingResponse {
    while (true) {
        if (stopped()) throw PairException("已取消")
        val res = try {
            get()
        } catch (e: ConnectException) {
            if (e.code == Code.NOT_FOUND) throw PairException("配对已过期或被取消")
            throw e
        }
        if (res.state == PairingState.PAIRING_STATE_CANCELED) throw PairException("另一台设备取消了配对")
        if (res.stateValue >= want.number) return res
        delay(1000)
    }
}

/** Adding a device from this one. */
class Initiation internal constructor(
    val code: String,
    val link: PairLink,
    private val remote: Remote,
    private val pairingId: String,
    private val key: PairKey,
    private val expires: Long,
) {
    @Volatile private var stopped = false
    private var secrets: PairSecrets? = null
    private var candidate: ByteArray? = null
    private var candidateKind: DeviceKind? = null
    private val approvalLock = Any()
    private var approvalRequest: ApprovePairingRequest? = null

    /** Waits for a device to ask to join; returns the emoji to compare and its kind. */
    suspend fun joined(): Pair<List<String>, DeviceKind> {
        val tok = remote.currentToken()
        val st = poll({ stopped }, PairingState.PAIRING_STATE_JOINED) {
            remote.api.pairing.getPairing(GetPairingRequest.newBuilder().setPairingId(pairingId).build(), Api.headers(tok)).value()
        }
        remote.api.pairing.revealPairing(
            RevealPairingRequest.newBuilder().setPairingId(pairingId).setInitiatorPublic(ByteString.copyFrom(key.public)).build(),
            Api.headers(tok),
        ).value()
        require(st.devicePublicKey.size() == 32 && st.joinerKind in listOf(DeviceKind.DEVICE_KIND_CLIENT, DeviceKind.DEVICE_KIND_MACHINE)) { "无效的候选设备身份" }
        candidate = st.devicePublicKey.toByteArray(); candidateKind = st.joinerKind
        val tr = Pairing.identityTranscript(pairingId, key.public, st.joinerPublic.toByteArray(), st.devicePublicKey.toByteArray(), st.joinerKindValue)
        val s = key.derive(st.joinerPublic.toByteArray(), tr)
        secrets = s
        return s.emoji to st.joinerKind
    }

    /** Sends the root key; returns when the new device has it. */
    suspend fun approve() {
        val s = secrets ?: throw PairException("还没有设备加入")
        val acct = remote.account
        val tok = remote.currentToken()
        val request = synchronized(approvalLock) {
            approvalRequest ?: run {
                val (approval, signature) = makePairApproval(acct, pairingId, candidate ?: throw PairException("没有候选设备"), candidateKind ?: throw PairException("没有候选设备"), expires)
                val env = Envelopes.seal(
                    s.key,
                    Header.newBuilder().setKind(PayloadKind.PAYLOAD_KIND_PAIRING).setRequestId(pairingId).setSender(acct.deviceId).build(),
                    PairingPayload.newBuilder().setRoot(ByteString.copyFrom(acct.root)).setServer(acct.server).setApproval(ByteString.copyFrom(approval)).setApprovalSignature(ByteString.copyFrom(signature)).build(),
                )
                ApprovePairingRequest.newBuilder().setPairingId(pairingId).setPayload(env).setApproval(ByteString.copyFrom(approval)).setApprovalSignature(ByteString.copyFrom(signature)).build()
            }.also { approvalRequest = it }
        }
        remote.api.pairing.approvePairing(request, Api.headers(tok)).value()
        // Only the new device confirming it has the key counts as success. A
        // pairing that disappears meanwhile leaves the outcome unknown.
        repeat(120) {
            if (stopped) throw PairException("已取消")
            try {
                val res = remote.api.pairing.getPairing(GetPairingRequest.newBuilder().setPairingId(pairingId).build(), Api.headers(tok)).value()
                if (res.state == PairingState.PAIRING_STATE_DELIVERED) return
                if (res.state == PairingState.PAIRING_STATE_CANCELED) throw PairException("另一台设备取消了配对")
            } catch (e: ConnectException) {
                if (e.code == Code.NOT_FOUND) throw PairException("配对已过期，不确定新设备有没有拿到账户密钥。请在新设备上确认，必要时重新配对。")
            }
            delay(1000)
        }
        throw PairException("新设备没有来取账户密钥，它可能没有加入成功。请在新设备上确认，必要时重新配对。")
    }

    fun cancel() {
        stopped = true
    }

    /** Tells the server; best effort. */
    suspend fun cancelOnServer() {
        stopped = true
        try {
            remote.api.pairing.cancelPairing(CancelPairingRequest.newBuilder().setPairingId(pairingId).build(), Api.headers(remote.currentToken()))
        } catch (e: Exception) {
            // The pairing expires by itself.
        }
    }

    companion object {
        /** Starts adding a device to this device's account. */
        suspend fun start(remote: Remote): Initiation {
            val key = PairKey()
            val commit = Pairing.commit(key.public)
            val created = remote.api.pairing.createPairing(
                CreatePairingRequest.newBuilder().setCommit(ByteString.copyFrom(commit)).build(),
                Api.headers(remote.currentToken()),
            ).value()
            return Initiation(created.code, PairLink(created.code, remote.account.server, commit), remote, created.pairingId, key, created.expiresMs)
        }
    }
}

/**
 * Joins an account with a code or link from an existing device. [onEmoji]
 * gets the emoji to compare with the other device's.
 */
suspend fun joinByPairing(
    link: PairLink,
    http: OkHttpClient,
    onEmoji: (List<String>) -> Unit,
    stopped: () -> Boolean,
    /** Called when registering starts: from here on the join is not canceled, its outcome is reported. */
    onRegistering: () -> Unit = {},
): Account {
    val api = Api(link.server, http)
    val key = PairKey()
    val deviceSeed = randomBytes(32)
    val devicePublic = SigningKey(deviceSeed).publicKey
    val joined = try {
        api.pairing.joinPairing(
            JoinPairingRequest.newBuilder()
                .setCode(link.code.filterNot { it.isWhitespace() })
                .setJoinerPublic(ByteString.copyFrom(key.public))
                .setJoinerKind(DeviceKind.DEVICE_KIND_CLIENT)
                .setDevicePublicKey(ByteString.copyFrom(devicePublic))
                .build(),
            emptyMap(),
        ).value()
    } catch (e: ConnectException) {
        if (e.code == Code.NOT_FOUND || e.code == Code.INVALID_ARGUMENT || e.code == Code.FAILED_PRECONDITION) throw PairException("配对码不对，或者已经用过了")
        if (e.code == Code.RESOURCE_EXHAUSTED) throw PairException("试得太频繁了，稍等一分钟再试")
        throw e
    }
    val id = joined.pairingId
    val token = joined.candidateToken
    if (link.commit != null && !link.commit.contentEquals(joined.commit.toByteArray())) {
        throw PairException("服务器返回的不是扫码那台设备的信息，已停止配对")
    }
    val get: suspend () -> GetPairingResponse = {
        api.pairing.getPairing(GetPairingRequest.newBuilder().setPairingId(id).setCandidateToken(token).build(), emptyMap()).value()
    }
    val st = poll(stopped, PairingState.PAIRING_STATE_REVEALED, get)
    if (!Pairing.commit(st.initiatorPublic.toByteArray()).contentEquals(joined.commit.toByteArray())) {
        throw PairException("另一台设备的密钥和它的承诺对不上，已停止配对")
    }
    val tr = Pairing.identityTranscript(id, st.initiatorPublic.toByteArray(), key.public, devicePublic, DeviceKind.DEVICE_KIND_CLIENT.number)
    val s = key.derive(st.initiatorPublic.toByteArray(), tr)
    onEmoji(s.emoji)
    val done = poll(stopped, PairingState.PAIRING_STATE_APPROVED, get)
    val (_, payload) = Envelopes.openMessage(s.key, done.payload, Place(PayloadKind.PAYLOAD_KIND_PAIRING, requestId = id), PairingPayload::parseFrom)
    if (payload.server != link.server) throw PairException("账户属于另一个服务器")
    // Canceled while the other device was allowing it: stop before registering.
    if (stopped()) throw PairException("已取消本次加入")
    onRegistering()
    val acct = joinWithRoot(payload.root.toByteArray(), payload.server, http, enrollment = Enrollment(deviceSeed, id, payload.approval.toByteArray(), payload.approvalSignature.toByteArray()))
    // Tells the other device it went through. If this is lost, that device
    // reports the outcome as unknown; the join itself stands.
    try {
        api.pairing.getPairing(GetPairingRequest.newBuilder().setPairingId(id).setCandidateToken(token).setReceived(true).build(), emptyMap())
    } catch (e: Exception) {
        // See above.
    }
    return acct
}
