package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.proto.SecurityKeyAgreeEffect
import io.github.lewin671.remoteagent.core.proto.SecurityKeyAgreedEvent
import io.github.lewin671.remoteagent.core.proto.SecurityKeySignEffect
import io.github.lewin671.remoteagent.core.proto.SecurityKeySignedEvent
import io.github.lewin671.remoteagent.corebind.Corebind

/**
 * Executes host-side private-key operations requested by clientcore/security.
 * Android holds raw Ed25519/X25519 keys in memory and handles requests synchronously.
 */
internal class SecurityExecutor(
    private val signer: () -> SigningKey?,
    private val xPrivate: () -> ByteArray?,
) {
    fun keySign(effect: SecurityKeySignEffect): SecurityKeySignedEvent {
        val builder = SecurityKeySignedEvent.newBuilder().setOpId(effect.opId)
        val key = signer() ?: return builder.setError("security signer not configured").build()
        val msg = effect.message.toByteArray()
        if (msg.isEmpty()) {
            return builder.setError("empty message to sign").build()
        }
        return try {
            builder.setSignature(ByteString.copyFrom(key.sign(msg))).build()
        } catch (t: Throwable) {
            builder.setError(t.message ?: "key sign failed").build()
        }
    }

    fun keyAgree(effect: SecurityKeyAgreeEffect): SecurityKeyAgreedEvent {
        val builder = SecurityKeyAgreedEvent.newBuilder().setOpId(effect.opId)
        val priv = xPrivate()
        val peerPub = effect.peerPublic.toByteArray()
        if (priv == null || priv.size != KEY_SIZE || peerPub.size != KEY_SIZE) {
            return builder.setError("security key agreement key unavailable").build()
        }
        return try {
            val shared = Corebind.x25519Shared(priv, peerPub)
            builder.setShared(ByteString.copyFrom(shared)).build()
        } catch (t: Throwable) {
            builder.setError(t.message ?: "key agreement failed").build()
        }
    }
}
