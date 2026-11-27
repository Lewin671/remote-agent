package io.github.lewin671.remoteagent.data

import io.github.lewin671.remoteagent.core.KEY_SIZE
import io.github.lewin671.remoteagent.core.hex
import io.github.lewin671.remoteagent.corebind.Corebind
import java.io.ByteArrayOutputStream
import java.io.DataOutputStream
import java.nio.ByteBuffer
import java.security.MessageDigest
import java.util.Base64

/** Vectors mirrored from `internal/deviceauth/testdata/domain-v3.json` for offline on-device vault tests. */
internal object BenchmarkVectors {
    const val SERVER = "https://example.com"
    const val ACCOUNT_ID = "0123456789abcdef0123456789abcdef"
    val domain = "0502f489b4cebe52e25e238aabf226f4c573cf68b01b33af4dfa0d8329873bec".unhex()
    val genesisHash = "52fb306d06ee6fe8fa2e3165ad3a10650fdd46288a35dbd38d0f7a37bf69437f".unhex()
    val genesisRaw = Base64.getDecoder().decode(
        "AAABNAAAAAMAAAAgMDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWYFAvSJtM6+UuJeI4qr8ib0xXPPaLAbM69N+g2DKYc77AAAACAxMGJhNjgyYzhhZDEzNTEzOTcxZThiNTY4ODFhYWI4YqCapfR6Z1mAL/lV+NwtKhSlyZ0jvpf4ZBJ/+Tg0VaTwAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAQAAAAAAAAABAAABi8/laAAAAAABAAAAIDEwYmE2ODJjOGFkMTM1MTM5NzFlOGI1Njg4MWFhYjhi0EqyMnQrtKs6E2i9RhXk5tAiSrcaAWuvhSCjMsl3hzcMy1pMEx1AAEdimAVZF13nwrO9ybDBSzGI+pbT8c5JDQAAAAIAAAAAAAAAAQAAACAxMGJhNjgyYzhhZDEzNTEzOTcxZThiNTY4ODFhYWI4YgAAACD0tG7uoaqb8Zw8ZNseegb3LyVgCArL5O0QbCq+aBxaQQAAADA2DpES664liUfvMhj8qTr5bSydcmStTCgB502c9+TqfXeZjA5jV01FHa0L5i/mWmoAAAAAAAAAQFf1kKriAgtRH7OT8xnd6BiXDqO5OPTH6tMYZFsIEMZA2wL+qiBSxL24T8DFjzF0hSNwtNRuWgDX3iwRdn1tIw0=",
    )
    val addedRaw = Base64.getDecoder().decode(
        "AAABnAAAAAMAAAAgMDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWYFAvSJtM6+UuJeI4qr8ib0xXPPaLAbM69N+g2DKYc77AAAACAxMGJhNjgyYzhhZDEzNTEzOTcxZThiNTY4ODFhYWI4YqCapfR6Z1mAL/lV+NwtKhSlyZ0jvpf4ZBJ/+Tg0VaTwUvswbQbub+j6LjFlrToQZQ/dRiiKNdvTjQ96N79pQ38AAAAAAAAAAgAAAAAAAAACAAABi8/m7qAAAAACAAAAIDEwYmE2ODJjOGFkMTM1MTM5NzFlOGI1Njg4MWFhYjhi0EqyMnQrtKs6E2i9RhXk5tAiSrcaAWuvhSCjMsl3hzcMy1pMEx1AAEdimAVZF13nwrO9ybDBSzGI+pbT8c5JDQAAAAIAAAAgYjE0NzA1ODg4ZjRhNjgzOTFhMDlhYTU5NjhkZDI1ZDHXWXk7vBOigZqCfHattvuopJruAH9J8tCZLZm4Ja0sSDZquvbZ+NYKY1u10cIzMLXwIQQ3jE7r+FpEQ4aBTjY1AAAAAQAAAAAAAAACAAAAIDEwYmE2ODJjOGFkMTM1MTM5NzFlOGI1Njg4MWFhYjhiAAAAIN1P+iYa45PBtSdGV0y0Seq8IUWMIzAOQ497+0Ve5iNgAAAAMHipErZaMrnVmDHovf6pdWgfyxoyn5QkcZ2eXEQiXJ1xD+aMm+G3uPc9c4tGMtHEkQAAACBiMTQ3MDU4ODhmNGE2ODM5MWEwOWFhNTk2OGRkMjVkMQAAACAadSFhbJXvBhabxkCXWK9iQWxPU/D0SXQhzKpNgaBffAAAADDYUdWuTZJcw8b1k7uCNIfC/qe9t3CbYUX7+vT8FbfN9iftoNnEQdq2JfzlvZ+vz4MAAAA8ohEwO8ywnsVnQJd7MYYPPw7GqnOH3jy0Iikz30yiX7EdjXLdvdLlTXBoYRakjojo9hWkhoIvbMEUdxbbAAAAQH3L8AmWZL52Q9Gp7H+Nsg0qfyi88wqQxU2mkIU9SOpOiLpPjVpSzYf24m8IV1fCRL2lxGmhYQBg+79OftVZigY=",
    )
    val leaseBytes = Base64.getDecoder().decode(
        "AAAAWnJhMy9sZWFzZQAFAvSJtM6+UuJeI4qr8ib0xXPPaLAbM69N+g2DKYc77CmFlK+p4nhssyBWI34QAm8gjEJPRy7A9q8vSA/GnvM/AAABi8/m7qAAAAGL31oCoAAAAEBIaFtA5n9zv7yxFiiNqAiP/bsVGZtUwMBp1zXLcdMXt6JEyWNjbz5sKH3EaDoaKLNf7wg96IryCVyn4xvO36ED",
    )
    val phoneSeed = "1111111111111111111111111111111111111111111111111111111111111111".unhex()
    val phoneX = "7268259908c56878cd0168407c6940971ddc77b6905e89bb2be57325683ecadd".unhex()
    val phoneEdPub = Corebind.ed25519PublicKey(phoneSeed)
    val phoneId = deviceIdForEdPublic(phoneEdPub)
    val machineSeed = "4444444444444444444444444444444444444444444444444444444444444444".unhex()
    val machineX = "34a87223bf3bf9f552f31eb240d7fc975b31d10450ae56aa465587d5e16ce196".unhex()
    val machineEdPub = Corebind.ed25519PublicKey(machineSeed)
    val machineId = deviceIdForEdPublic(machineEdPub)
    val contentSecret = "5555555555555555555555555555555555555555555555555555555555555555".unhex()
    val contentEpoch = parseStateContentEpoch(addedRaw)
    val nowMs = parseLeaseIssuedAt(leaseBytes)

    val phoneRecordBytes = encodeV4RecordBytes(
        account = ACCOUNT_ID,
        domain = domain,
        genesis = genesisHash,
        memberId = phoneId,
        member = true,
        server = SERVER,
        states = listOf(genesisRaw, addedRaw),
        lease = leaseBytes,
        floorMs = nowMs,
        contentKeys = listOf(contentEpoch to contentSecret),
    )

    val machineRecordBytes = encodeV4RecordBytes(
        account = ACCOUNT_ID,
        domain = domain,
        genesis = genesisHash,
        memberId = machineId,
        member = true,
        server = SERVER,
        states = listOf(genesisRaw, addedRaw),
        lease = leaseBytes,
        floorMs = nowMs,
        contentKeys = listOf(contentEpoch to contentSecret),
    )

    val phoneNotifyBytes = encodeV4NotifyRecordBytes(
        account = ACCOUNT_ID,
        domain = domain,
        genesis = genesisHash,
        headState = addedRaw,
        purposeKey = deriveNotifyPurposeKey(contentSecret, ACCOUNT_ID, domain, contentEpoch),
    )
}

internal fun String.unhex(): ByteArray {
    check(length % 2 == 0)
    return ByteArray(length / 2) { i -> substring(i * 2, i * 2 + 2).toInt(16).toByte() }
}

internal fun deviceIdForEdPublic(edPub: ByteArray): String =
    MessageDigest.getInstance("SHA-256").digest(edPub).copyOf(16).hex()

private fun parseLeaseIssuedAt(lease: ByteArray): Long {
    if (lease.size < 86) return 0L
    return ByteBuffer.wrap(lease, 78, 8).long
}

private fun parseStateContentEpoch(state: ByteArray): Long {
    val bb = ByteBuffer.wrap(state)
    val coreLen = bb.int
    require(coreLen > 0 && state.size >= 4 + coreLen)
    val ver = bb.int
    require(ver == 3)
    val accLen = bb.int; bb.position(bb.position() + accLen)
    bb.position(bb.position() + 32)
    val authLen = bb.int; bb.position(bb.position() + authLen)
    bb.position(bb.position() + 64) // recoveryEd + parent
    bb.long // epoch
    return bb.long
}

private fun lpBytes(out: DataOutputStream, b: ByteArray) {
    out.writeInt(b.size)
    out.write(b)
}

private fun lpStr(out: DataOutputStream, s: String) =
    lpBytes(out, s.toByteArray(Charsets.UTF_8))

internal fun encodeV4RecordBytes(
    account: String,
    domain: ByteArray,
    genesis: ByteArray,
    memberId: String,
    member: Boolean = true,
    server: String = BenchmarkVectors.SERVER,
    accountPublicKey: ByteArray = ByteArray(0),
    states: List<ByteArray>,
    proofs: List<ByteArray?> = emptyList(),
    lease: ByteArray = ByteArray(0),
    floorMs: Long = parseLeaseIssuedAt(lease),
    contentKeys: List<Pair<Long, ByteArray>> = emptyList(),
): ByteArray {
    val bos = ByteArrayOutputStream()
    DataOutputStream(bos).use { s ->
        s.write("RA4REC\u0000".toByteArray(Charsets.US_ASCII))
        s.writeInt(4)
        lpStr(s, account)
        require(domain.size == 32 && genesis.size == 32)
        s.write(domain)
        s.write(genesis)
        lpStr(s, memberId)
        s.writeByte(if (member) 1 else 0)
        lpStr(s, server)
        lpBytes(s, accountPublicKey)
        s.writeInt(states.size)
        for (i in states.indices) {
            lpBytes(s, states[i])
            lpBytes(s, proofs.getOrNull(i) ?: ByteArray(0))
        }
        lpBytes(s, lease)
        s.writeLong(floorMs)
        s.writeInt(contentKeys.size)
        for ((ep, sec) in contentKeys) {
            require(sec.size == KEY_SIZE)
            s.writeLong(ep)
            s.write(sec)
        }
        s.writeByte(0) // needsRecoveryRotation
        s.writeByte(0) // recoveryObjectsPending
    }
    return bos.toByteArray()
}

internal fun encodeV4NotifyRecordBytes(
    account: String,
    domain: ByteArray,
    genesis: ByteArray,
    headState: ByteArray,
    purposeKey: ByteArray,
): ByteArray {
    require(domain.size == 32 && genesis.size == 32 && purposeKey.size == KEY_SIZE)
    val bos = ByteArrayOutputStream()
    DataOutputStream(bos).use { s ->
        s.write("RA4NTF\u0000".toByteArray(Charsets.US_ASCII))
        s.writeInt(4)
        lpStr(s, account)
        s.write(domain)
        s.write(genesis)
        lpBytes(s, headState)
        s.write(purposeKey)
    }
    return bos.toByteArray()
}

internal fun deriveNotifyPurposeKey(
    contentSecret: ByteArray,
    account: String,
    domain: ByteArray,
    contentEpoch: Long,
): ByteArray {
    val bos = ByteArrayOutputStream()
    DataOutputStream(bos).use { s ->
        s.write("ra3/purpose-key\u0000".toByteArray(Charsets.US_ASCII))
        lpStr(s, account)
        s.write(domain)
        s.writeLong(contentEpoch)
        s.writeInt(10)
    }
    return Corebind.hkdfSha256(contentSecret, ByteArray(0), bos.toByteArray(), KEY_SIZE)
}
