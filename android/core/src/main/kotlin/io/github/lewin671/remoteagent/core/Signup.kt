package io.github.lewin671.remoteagent.core

import io.github.lewin671.remoteagent.proto.GetSignupChallengeRequest
import io.github.lewin671.remoteagent.proto.SignupProof
import java.nio.ByteBuffer
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.withContext

// Port of the existing Go relay client's SolveSignup (before CLI account
// creation moved to the phone): SHA-256(challenge || big-endian uint64 nonce),
// with the same maximum of 28 leading zero bits and cooperative cancellation.
internal suspend fun solveSignup(api: Api): SignupProof {
    val challenge =
        api.auth
            .getSignupChallenge(GetSignupChallengeRequest.getDefaultInstance(), emptyMap())
            .value()
    val bits = challenge.difficultyBits
    if (bits !in 0..28) throw PairException("服务器要求的注册计算量过高（$bits 位，最多 28 位）")
    if (challenge.challenge.size() != 32) throw PairException("服务器返回了无效的注册挑战")
    val nonce =
        withContext(Dispatchers.Default) {
            val input = challenge.challenge.toByteArray() + ByteArray(8)
            val bytes = ByteBuffer.wrap(input)
            var attempt = 0L
            while (true) {
                if (attempt and 0xffffL == 0L) {
                    currentCoroutineContext().ensureActive()
                    if (System.currentTimeMillis() >= challenge.expiresMs)
                        throw PairException("注册挑战已过期，请重试")
                }
                bytes.putLong(32, attempt)
                val hash = sha256(input)
                var zeros = 0
                for (byte in hash) {
                    val value = byte.toInt() and 255
                    if (value == 0) zeros += 8
                    else {
                        zeros += Integer.numberOfLeadingZeros(value) - 24
                        break
                    }
                }
                if (zeros >= bits) break
                attempt++
            }
            input.copyOfRange(32, 40)
        }
    return SignupProof.newBuilder()
        .setChallenge(challenge.challenge)
        .setNonce(com.google.protobuf.ByteString.copyFrom(nonce))
        .build()
}
