package io.github.lewin671.remoteagent.core

import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.io.DataInputStream
import java.io.DataOutputStream

/**
 * Drafts are wrapped with the foreground account key. Recovery-code seeds and text never enter
 * these bytes.
 */
internal fun draftBytes(write: (DataOutputStream) -> Unit): ByteArray =
    ByteArrayOutputStream().also { out -> DataOutputStream(out).use(write) }.toByteArray()

internal fun DataOutputStream.blob(b: ByteArray) {
    writeInt(b.size)
    write(b)
}

internal fun DataInputStream.blob(): ByteArray {
    val n = readInt()
    require(n in 0..(16 shl 20))
    return ByteArray(n).also { readFully(it) }
}

internal fun <T> parseDraft(b: ByteArray, read: (DataInputStream) -> T): T =
    DataInputStream(ByteArrayInputStream(b)).use { input ->
        require(input.readInt() == 1)
        read(input).also { require(input.available() == 0) }
    }

fun CreatedAccount.draftBytes(): ByteArray = draftBytes { s ->
    s.writeInt(1)
    s.writeUTF(account.server)
    s.blob(account.root)
    s.blob(account.deviceSeed)
    s.writeUTF(account.deviceId)
    s.writeUTF(account.accountId)
    s.blob(account.accountPublicKey)
    s.blob(record.bytes())
}

/**
 * Exact signed submission, saved before SubmitRecovery, including its own identity and expected
 * state. The new recovery private key is absent.
 */
class RecoveryDraft(
    val submission: RecoverySubmission,
    val statement: ByteArray,
    val state: ByteArray,
) {
    override fun toString() = "RecoveryDraft"

    fun bytes(): ByteArray = draftBytes { s ->
        s.writeInt(1)
        s.writeUTF(submission.recoveryId)
        s.writeLong(submission.readyMs)
        s.blob(submission.deviceSeed)
        s.blob(submission.xPrivate)
        s.writeUTF(submission.accountId)
        s.blob(submission.accountPublicKey)
        s.blob(submission.stateHash)
        s.blob(submission.genesisPrefix)
        s.blob(statement)
        s.blob(state)
    }

    companion object {
        fun parse(b: ByteArray): RecoveryDraft =
            parseDraft(b) { s ->
                val id = s.readUTF()
                val ready = s.readLong()
                val seed = s.blob()
                val x = s.blob()
                val account = s.readUTF()
                val pub = s.blob()
                val hash = s.blob()
                val genesis = s.blob()
                val statement = s.blob()
                val state = s.blob()
                require(seed.size == 32 && x.size == 32 && hash.size == 32 && genesis.size == 10)
                val parsed = DeviceSecurity.parseState(state)
                require(DeviceSecurity.stateHash(parsed).contentEquals(hash))
                RecoveryDraft(
                    RecoverySubmission(
                        id,
                        ready,
                        "",
                        seed,
                        deviceId(SigningKey(seed).publicKey),
                        x,
                        account,
                        pub,
                        hash,
                        genesis,
                    ),
                    statement,
                    state,
                )
            }
    }
}
