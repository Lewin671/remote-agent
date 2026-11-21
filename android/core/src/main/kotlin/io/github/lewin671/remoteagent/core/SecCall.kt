package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.core.proto.SecBuildStateCall
import io.github.lewin671.remoteagent.core.proto.SecCallRequest
import io.github.lewin671.remoteagent.core.proto.SecCallResponse
import io.github.lewin671.remoteagent.core.proto.SecDerivePairSecretsCall
import io.github.lewin671.remoteagent.core.proto.SecDerivePurposeKeyCall
import io.github.lewin671.remoteagent.core.proto.SecEd25519SeedSignCall
import io.github.lewin671.remoteagent.core.proto.SecGrant
import io.github.lewin671.remoteagent.core.proto.SecGrantChallengeCall
import io.github.lewin671.remoteagent.core.proto.SecInflateBoundedCall
import io.github.lewin671.remoteagent.core.proto.SecKemPair
import io.github.lewin671.remoteagent.core.proto.SecMachineReplacementOriginsCall
import io.github.lewin671.remoteagent.core.proto.SecMarshalLeaseCall
import io.github.lewin671.remoteagent.core.proto.SecMarshalRecoveryStatementCall
import io.github.lewin671.remoteagent.core.proto.SecMarshalStateCall
import io.github.lewin671.remoteagent.core.proto.SecMember
import io.github.lewin671.remoteagent.core.proto.SecNewLeaseUnsignedCall
import io.github.lewin671.remoteagent.core.proto.SecNewRecoveryStatementUnsignedCall
import io.github.lewin671.remoteagent.core.proto.SecOpenGrantWithDHCall
import io.github.lewin671.remoteagent.core.proto.SecOpenNotifyEnvelopeCall
import io.github.lewin671.remoteagent.core.proto.SecOpenPrevSecretCall
import io.github.lewin671.remoteagent.core.proto.SecPairDomainCommitCall
import io.github.lewin671.remoteagent.core.proto.SecPairDomainTranscriptCall
import io.github.lewin671.remoteagent.core.proto.SecPairIdentityTranscriptCall
import io.github.lewin671.remoteagent.core.proto.SecPairTranscriptCall
import io.github.lewin671.remoteagent.core.proto.SecPairTrustDescriptorCall
import io.github.lewin671.remoteagent.core.proto.SecParsedApproval
import io.github.lewin671.remoteagent.core.proto.SecParsedLease
import io.github.lewin671.remoteagent.core.proto.SecParsedRecoveryStatement
import io.github.lewin671.remoteagent.core.proto.SecParsedState
import io.github.lewin671.remoteagent.core.proto.SecPin
import io.github.lewin671.remoteagent.core.proto.SecUnsignedState
import io.github.lewin671.remoteagent.core.proto.SecVerifyChainCall
import io.github.lewin671.remoteagent.core.proto.SecVerifyLeaseCall
import io.github.lewin671.remoteagent.core.proto.SecVerifyPairApprovalCall
import io.github.lewin671.remoteagent.core.proto.SecVerifyRecoveryStatementCall
import io.github.lewin671.remoteagent.corebind.Corebind
import io.github.lewin671.remoteagent.proto.Envelope
import io.github.lewin671.remoteagent.proto.NotifyContent
import java.security.SecureRandom

/** Stateless bridge from Kotlin security/authority helpers to Go `clientcore` `SecCall`. */
internal object SecCall {
    private val rng = SecureRandom()

    private fun call(req: SecCallRequest): SecCallResponse {
        val raw: ByteArray? = Corebind.secCall(req.toByteArray())
        val resp = if (raw == null || raw.isEmpty()) SecCallResponse.getDefaultInstance() else SecCallResponse.parseFrom(raw)
        if (resp.error.isNotEmpty()) {
            throw DeviceSecurity.Failure(resp.errorCode.ifEmpty { "invalid" })
        }
        return resp
    }

    fun toProto(p: DeviceSecurity.Pin): SecPin =
        SecPin.newBuilder()
            .setAccount(p.account)
            .setDomain(ByteString.copyFrom(p.domain))
            .setGenesis(ByteString.copyFrom(p.genesis))
            .build()

    fun pinFromProto(p: SecPin): DeviceSecurity.Pin =
        DeviceSecurity.Pin(
            account = p.account,
            domain = p.domain.toByteArray(),
            genesis = p.genesis.toByteArray(),
        )

    fun toProto(m: DeviceSecurity.Member): SecMember =
        SecMember.newBuilder()
            .setId(m.id)
            .setEdPublic(ByteString.copyFrom(m.ed))
            .setXPublic(ByteString.copyFrom(m.x))
            .setKind(m.kind.toInt())
            .build()

    fun memberFromProto(m: SecMember): DeviceSecurity.Member =
        DeviceSecurity.Member(
            id = m.id,
            ed = m.edPublic.toByteArray(),
            x = m.xPublic.toByteArray(),
            kind = m.kind.toUInt(),
        )

    fun toProto(g: DeviceSecurity.Grant): SecGrant =
        SecGrant.newBuilder()
            .setRecipient(g.recipient)
            .setEnc(ByteString.copyFrom(g.enc))
            .setCipher(ByteString.copyFrom(g.cipher))
            .build()

    fun grantFromProto(g: SecGrant): DeviceSecurity.Grant =
        DeviceSecurity.Grant(
            recipient = g.recipient,
            enc = g.enc.toByteArray(),
            cipher = g.cipher.toByteArray(),
        )

    fun stateFromProto(s: SecParsedState): DeviceSecurity.State =
        DeviceSecurity.State(
            core =
                DeviceSecurity.Core(
                    account = s.account,
                    domain = s.domain.toByteArray(),
                    authorityID = s.authorityId,
                    recoveryEd = s.recoveryEd.toByteArray(),
                    parent = s.parent.toByteArray(),
                    epoch = s.epoch.toULong(),
                    contentEpoch = s.contentEpoch.toULong(),
                    issuedAt = s.issuedAt.toULong(),
                    members = s.membersList.map(::memberFromProto),
                    removed = s.removedList.toList(),
                ),
            grants = s.grantsList.map(::grantFromProto),
            prevSecret = s.prevSecret.toByteArray(),
            signature = s.signature.toByteArray(),
        )

    fun leaseFromProto(l: SecParsedLease): HeadLease =
        HeadLease(
            domain = l.domain.toByteArray(),
            head = l.head.toByteArray(),
            issuedAt = l.issuedAt.toULong(),
            notAfter = l.notAfter.toULong(),
            signature = l.signature.toByteArray(),
        )

    fun recoveryFromProto(r: SecParsedRecoveryStatement): DeviceSecurity.RecoveryStatement =
        DeviceSecurity.RecoveryStatement(
            account = r.account,
            domain = r.domain.toByteArray(),
            parent = r.parent.toByteArray(),
            newAuthorityID = r.newAuthorityId,
            newRecoveryEd = r.newRecoveryEd.toByteArray(),
            newMember = memberFromProto(r.newMember),
            issuedAt = r.issuedAt.toULong(),
            stateHash = r.stateHash.toByteArray(),
            nonce = r.nonce.toByteArray(),
            signature = r.signature.toByteArray(),
        )

    fun parseState(raw: ByteArray): Pair<DeviceSecurity.State, ByteArray> {
        val resp = call(SecCallRequest.newBuilder().setParseState(ByteString.copyFrom(raw)).build())
        val state = stateFromProto(resp.parsedState)
        val hash = resp.parsedState.stateHash.toByteArray()
        DeviceSecurity.cacheState(state, raw, hash)
        return state to hash
    }

    fun verifyChain(
        pin: DeviceSecurity.Pin,
        states: List<ByteArray>,
        recoveryProofs: List<ByteArray?> = emptyList(),
        lease: ByteArray? = null,
        nowMs: Long = 0L,
        memberId: String = "",
        checkLeaseWindow: Boolean = false,
    ): DeviceSecurity.State {
        val callBuilder =
            SecVerifyChainCall.newBuilder()
                .setPin(toProto(pin))
                .setNowMs(nowMs)
                .setMemberId(memberId)
                .setCheckLeaseWindow(checkLeaseWindow)
        for (raw in states) {
            callBuilder.addStates(ByteString.copyFrom(raw))
        }
        for (proof in recoveryProofs) {
            callBuilder.addRecoveryProofs(
                if (proof == null) ByteString.EMPTY else ByteString.copyFrom(proof)
            )
        }
        if (lease != null && lease.isNotEmpty()) {
            callBuilder.setLease(ByteString.copyFrom(lease))
        }
        val resp = call(SecCallRequest.newBuilder().setVerifyChain(callBuilder).build())
        val headRaw = states.last()
        val head = stateFromProto(resp.parsedState)
        DeviceSecurity.cacheState(head, headRaw, resp.parsedState.stateHash.toByteArray())
        return head
    }

    fun grantChallenge(pin: DeviceSecurity.Pin, stateBytes: ByteArray, memberId: String): ByteArray {
        val resp =
            call(
                SecCallRequest.newBuilder()
                    .setGrantChallenge(
                        SecGrantChallengeCall.newBuilder()
                            .setPin(toProto(pin))
                            .setState(ByteString.copyFrom(stateBytes))
                            .setMemberId(memberId)
                    )
                    .build()
            )
        return resp.data.toByteArray()
    }

    fun openGrantWithDH(
        pin: DeviceSecurity.Pin,
        stateBytes: ByteArray,
        memberId: String,
        dh: ByteArray,
    ): ByteArray {
        val resp =
            call(
                SecCallRequest.newBuilder()
                    .setOpenGrantWithDh(
                        SecOpenGrantWithDHCall.newBuilder()
                            .setPin(toProto(pin))
                            .setState(ByteString.copyFrom(stateBytes))
                            .setMemberId(memberId)
                            .setDh(ByteString.copyFrom(dh))
                    )
                    .build()
            )
        return resp.data.toByteArray()
    }

    fun openPrevSecret(newSecret: ByteArray, blob: ByteArray): ByteArray =
        call(
            SecCallRequest.newBuilder()
                .setOpenPrevSecret(
                    SecOpenPrevSecretCall.newBuilder()
                        .setNewSecret(ByteString.copyFrom(newSecret))
                        .setBlob(ByteString.copyFrom(blob))
                )
                .build()
        ).data.toByteArray()

    fun parseLease(raw: ByteArray): HeadLease =
        leaseFromProto(
            call(SecCallRequest.newBuilder().setParseLease(ByteString.copyFrom(raw)).build()).parsedLease
        )

    fun verifyLease(leaseBytes: ByteArray, headStateBytes: ByteArray, nowMs: Long, checkWindow: Boolean) {
        call(
            SecCallRequest.newBuilder()
                .setVerifyLease(
                    SecVerifyLeaseCall.newBuilder()
                        .setLease(ByteString.copyFrom(leaseBytes))
                        .setHeadState(ByteString.copyFrom(headStateBytes))
                        .setNowMs(nowMs)
                        .setCheckWindow(checkWindow)
                )
                .build()
        )
    }

    fun newLeaseUnsigned(headStateBytes: ByteArray, nowMs: Long): Pair<SecParsedLease, ByteArray> {
        val resp =
            call(
                SecCallRequest.newBuilder()
                    .setNewLeaseUnsigned(
                        SecNewLeaseUnsignedCall.newBuilder()
                            .setHeadState(ByteString.copyFrom(headStateBytes))
                            .setNowMs(nowMs)
                    )
                    .build()
            )
        return resp.parsedLease to resp.data.toByteArray()
    }

    fun marshalLease(
        domain: ByteArray,
        headHash: ByteArray,
        issuedAt: Long,
        notAfter: Long,
        signature: ByteArray,
    ): Pair<ByteArray, HeadLease> {
        val resp =
            call(
                SecCallRequest.newBuilder()
                    .setMarshalLease(
                        SecMarshalLeaseCall.newBuilder()
                            .setDomain(ByteString.copyFrom(domain))
                            .setHead(ByteString.copyFrom(headHash))
                            .setIssuedAt(issuedAt)
                            .setNotAfter(notAfter)
                            .setSignature(ByteString.copyFrom(signature))
                    )
                    .build()
            )
        return resp.data.toByteArray() to leaseFromProto(resp.parsedLease)
    }

    fun parseRecoveryStatement(raw: ByteArray): DeviceSecurity.RecoveryStatement =
        recoveryFromProto(
            call(
                SecCallRequest.newBuilder()
                    .setParseRecoveryStatement(ByteString.copyFrom(raw))
                    .build()
            ).parsedRecovery
        )

    fun verifyRecoveryStatement(statementBytes: ByteArray, parentBytes: ByteArray, nextBytes: ByteArray) {
        call(
            SecCallRequest.newBuilder()
                .setVerifyRecoveryStatement(
                    SecVerifyRecoveryStatementCall.newBuilder()
                        .setStatement(ByteString.copyFrom(statementBytes))
                        .setParentState(ByteString.copyFrom(parentBytes))
                        .setNextState(ByteString.copyFrom(nextBytes))
                )
                .build()
        )
    }

    fun newRecoveryStatementUnsigned(
        parentBytes: ByteArray,
        nextBytes: ByteArray,
        nonce: ByteArray,
    ): Pair<DeviceSecurity.RecoveryStatement, ByteArray> {
        val resp =
            call(
                SecCallRequest.newBuilder()
                    .setNewRecoveryStatementUnsigned(
                        SecNewRecoveryStatementUnsignedCall.newBuilder()
                            .setParentState(ByteString.copyFrom(parentBytes))
                            .setNextState(ByteString.copyFrom(nextBytes))
                            .setNonce(ByteString.copyFrom(nonce))
                    )
                    .build()
            )
        return recoveryFromProto(resp.parsedRecovery) to resp.data.toByteArray()
    }

    fun marshalRecoveryStatement(stmt: DeviceSecurity.RecoveryStatement): Pair<ByteArray, DeviceSecurity.RecoveryStatement> {
        val resp =
            call(
                SecCallRequest.newBuilder()
                    .setMarshalRecoveryStatement(
                        SecMarshalRecoveryStatementCall.newBuilder()
                            .setAccount(stmt.account)
                            .setDomain(ByteString.copyFrom(stmt.domain))
                            .setParent(ByteString.copyFrom(stmt.parent))
                            .setNewAuthorityId(stmt.newAuthorityID)
                            .setNewRecoveryEd(ByteString.copyFrom(stmt.newRecoveryEd))
                            .setNewMember(toProto(stmt.newMember))
                            .setIssuedAt(stmt.issuedAt.toLong())
                            .setStateHash(ByteString.copyFrom(stmt.stateHash))
                            .setNonce(ByteString.copyFrom(stmt.nonce))
                            .setSignature(ByteString.copyFrom(stmt.signature))
                    )
                    .build()
            )
        return resp.data.toByteArray() to recoveryFromProto(resp.parsedRecovery)
    }

    fun encapsulateMembers(members: List<DeviceSecurity.Member>): List<SecKemPair> =
        members.map { m ->
            val eph = ByteArray(KEY_SIZE).also { rng.nextBytes(it) }
            val enc = Corebind.x25519PublicKey(eph)
            val dh = Corebind.x25519Shared(eph, m.x)
            SecKemPair.newBuilder()
                .setMemberId(m.id)
                .setEnc(ByteString.copyFrom(enc))
                .setDh(ByteString.copyFrom(dh))
                .build()
        }

    fun buildState(builder: SecBuildStateCall.Builder): Pair<SecUnsignedState, ByteArray> {
        val resp = call(SecCallRequest.newBuilder().setBuildState(builder).build())
        return resp.unsignedState to resp.data.toByteArray()
    }

    fun marshalState(
        unsignedCore: ByteString,
        grants: List<DeviceSecurity.Grant>,
        prevSecret: ByteArray,
        signature: ByteArray,
    ): Pair<ByteArray, DeviceSecurity.State> {
        val b =
            SecMarshalStateCall.newBuilder()
                .setUnsignedCore(unsignedCore)
                .addAllGrants(grants.map(::toProto))
                .setPrevSecret(ByteString.copyFrom(prevSecret))
                .setSignature(ByteString.copyFrom(signature))
        val resp = call(SecCallRequest.newBuilder().setMarshalState(b).build())
        val raw = resp.data.toByteArray()
        val parsed = stateFromProto(resp.parsedState)
        DeviceSecurity.cacheState(parsed, raw, resp.parsedState.stateHash.toByteArray())
        return raw to parsed
    }

    fun verifyPairApproval(
        approval: ByteArray,
        signature: ByteArray,
        expectedAccount: String = "",
        expectedCandidateId: String = "",
    ): SecParsedApproval =
        call(
            SecCallRequest.newBuilder()
                .setVerifyPairApproval(
                    SecVerifyPairApprovalCall.newBuilder()
                        .setApproval(ByteString.copyFrom(approval))
                        .setSignature(ByteString.copyFrom(signature))
                        .setExpectedAccount(expectedAccount)
                        .setExpectedCandidateId(expectedCandidateId)
                )
                .build()
        ).parsedApproval

    fun machineReplacementOrigins(
        account: String,
        targetMachineId: String,
        states: List<ByteArray>,
        proofs: List<io.github.lewin671.remoteagent.proto.MachineReplacementProof>,
    ): Set<String> {
        val b =
            SecMachineReplacementOriginsCall.newBuilder()
                .setAccount(account)
                .setTargetMachineId(targetMachineId)
                .addAllStates(states.map { ByteString.copyFrom(it) })
                .addAllProofs(proofs)
        return call(SecCallRequest.newBuilder().setMachineReplacementOrigins(b).build()).stringsList.toSet()
    }

    fun pairTrustDescriptor(pin: DeviceSecurity.Pin, approver: String): ByteArray =
        call(
            SecCallRequest.newBuilder()
                .setPairTrustDescriptor(
                    SecPairTrustDescriptorCall.newBuilder()
                        .setPin(toProto(pin))
                        .setApprover(approver)
                )
                .build()
        ).data.toByteArray()

    fun parsePairTrustDescriptor(raw: ByteArray): Pair<DeviceSecurity.Pin, String> {
        val resp =
            call(
                SecCallRequest.newBuilder()
                    .setParsePairTrustDescriptor(ByteString.copyFrom(raw))
                    .build()
            )
        return pinFromProto(resp.pin) to resp.text
    }

    fun pairDomainCommit(initiatorPublic: ByteArray, trustDescriptor: ByteArray): ByteArray =
        call(
            SecCallRequest.newBuilder()
                .setPairDomainCommit(
                    SecPairDomainCommitCall.newBuilder()
                        .setInitiator(ByteString.copyFrom(initiatorPublic))
                        .setTrustDescriptor(ByteString.copyFrom(trustDescriptor))
                )
                .build()
        ).data.toByteArray()

    fun pairTranscript(
        pairingId: String,
        initiatorPublic: ByteArray,
        joinerPublic: ByteArray,
        joinerKind: Int,
    ): ByteArray =
        call(
            SecCallRequest.newBuilder()
                .setPairTranscript(
                    SecPairTranscriptCall.newBuilder()
                        .setPairingId(pairingId)
                        .setInitiatorPublic(ByteString.copyFrom(initiatorPublic))
                        .setJoinerPublic(ByteString.copyFrom(joinerPublic))
                        .setJoinerKind(joinerKind)
                )
                .build()
        ).data.toByteArray()

    fun pairIdentityTranscript(
        pairingId: String,
        initiatorPublic: ByteArray,
        joinerPublic: ByteArray,
        deviceEdPublic: ByteArray,
        joinerKind: Int,
    ): ByteArray =
        call(
            SecCallRequest.newBuilder()
                .setPairIdentityTranscript(
                    SecPairIdentityTranscriptCall.newBuilder()
                        .setPairingId(pairingId)
                        .setInitiatorPublic(ByteString.copyFrom(initiatorPublic))
                        .setJoinerPublic(ByteString.copyFrom(joinerPublic))
                        .setDeviceEdPublic(ByteString.copyFrom(deviceEdPublic))
                        .setJoinerKind(joinerKind)
                )
                .build()
        ).data.toByteArray()

    fun pairDomainTranscript(
        pairingId: String,
        initiatorPublic: ByteArray,
        joinerPublic: ByteArray,
        deviceEdPublic: ByteArray,
        deviceXPublic: ByteArray,
        joinerKind: Int,
        trustDescriptor: ByteArray,
    ): ByteArray =
        call(
            SecCallRequest.newBuilder()
                .setPairDomainTranscript(
                    SecPairDomainTranscriptCall.newBuilder()
                        .setPairingId(pairingId)
                        .setInitiatorPublic(ByteString.copyFrom(initiatorPublic))
                        .setJoinerPublic(ByteString.copyFrom(joinerPublic))
                        .setDeviceEdPublic(ByteString.copyFrom(deviceEdPublic))
                        .setDeviceXPublic(ByteString.copyFrom(deviceXPublic))
                        .setJoinerKind(joinerKind)
                        .setTrustDescriptor(ByteString.copyFrom(trustDescriptor))
                )
                .build()
        ).data.toByteArray()

    fun derivePairSecrets(shared: ByteArray, transcript: ByteArray): Triple<ByteArray, ByteArray, List<String>> {
        val resp =
            call(
                SecCallRequest.newBuilder()
                    .setDerivePairSecrets(
                        SecDerivePairSecretsCall.newBuilder()
                            .setShared(ByteString.copyFrom(shared))
                            .setTranscript(ByteString.copyFrom(transcript))
                    )
                    .build()
            )
        return Triple(resp.data.toByteArray(), resp.secondaryData.toByteArray(), resp.stringsList.toList())
    }

    fun ed25519SeedPublic(seed: ByteArray): ByteArray =
        call(SecCallRequest.newBuilder().setEd25519SeedPublic(ByteString.copyFrom(seed)).build()).data.toByteArray()

    fun ed25519SeedSign(seed: ByteArray, message: ByteArray): ByteArray =
        call(
            SecCallRequest.newBuilder()
                .setEd25519SeedSign(
                    SecEd25519SeedSignCall.newBuilder()
                        .setSeed(ByteString.copyFrom(seed))
                        .setMessage(ByteString.copyFrom(message))
                )
                .build()
        ).data.toByteArray()

    fun inflateBounded(compressed: ByteArray, limit: Int): ByteArray =
        call(
            SecCallRequest.newBuilder()
                .setInflateBounded(
                    SecInflateBoundedCall.newBuilder()
                        .setCompressed(ByteString.copyFrom(compressed))
                        .setLimit(limit)
                )
                .build()
        ).data.toByteArray()

    fun derivePurposeKey(
        secret: ByteArray,
        accountId: String,
        domain: ByteArray,
        contentEpoch: ULong,
        purpose: UInt,
    ): ByteArray =
        call(
            SecCallRequest.newBuilder()
                .setDerivePurposeKey(
                    SecDerivePurposeKeyCall.newBuilder()
                        .setSecret(ByteString.copyFrom(secret))
                        .setAccountId(accountId)
                        .setDomain(ByteString.copyFrom(domain))
                        .setContentEpoch(contentEpoch.toLong())
                        .setPurpose(purpose.toInt())
                )
                .build()
        ).data.toByteArray()

    fun openNotifyEnvelope(
        pin: DeviceSecurity.Pin,
        states: List<ByteArray>,
        envelope: Envelope,
        purposeKey: ByteArray? = null,
        memberId: String = "",
        dh: ByteArray? = null,
        contentSecret: ByteArray? = null,
    ): NotifyContent {
        val b =
            SecOpenNotifyEnvelopeCall.newBuilder()
                .setPin(toProto(pin))
                .addAllStates(states.map { ByteString.copyFrom(it) })
                .setEnvelope(envelope)
        if (purposeKey != null) b.setPurposeKey(ByteString.copyFrom(purposeKey))
        if (memberId.isNotEmpty()) b.setMemberId(memberId)
        if (dh != null) b.setDh(ByteString.copyFrom(dh))
        if (contentSecret != null) b.setContentSecret(ByteString.copyFrom(contentSecret))
        val resp = call(SecCallRequest.newBuilder().setOpenNotifyEnvelope(b).build())
        return NotifyContent.parseFrom(resp.data)
    }
}
