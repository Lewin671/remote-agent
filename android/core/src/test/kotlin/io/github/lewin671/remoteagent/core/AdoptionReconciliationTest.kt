package io.github.lewin671.remoteagent.core

import com.google.protobuf.ByteString
import io.github.lewin671.remoteagent.proto.SubmitRecoveryRequest
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test

class AdoptionReconciliationTest {
    private data class Adopted(
        val account: Account,
        val record: DeviceSecurity.Record,
        val operation: SecurityOperation,
    )

    private fun adopted(recovery: Boolean): Adopted {
        val root = ByteArray(32) { 3 }
        val oldSeed = ByteArray(32) { 0x11 }
        val x = ByteArray(32) { 0x22 }
        val oldSigner = SigningKey(oldSeed)
        val account = Account("https://example.com", root, oldSeed, deviceId(oldSigner.publicKey))
        val oldRecovery = ByteArray(32) { 0x33 }
        val genesis =
            Authority.genesis(
                account.accountId,
                ByteArray(32) { 4 },
                Authority.newMember(oldSigner, x, 2u),
                SigningKey(oldRecovery).publicKey,
                ByteArray(32) { 5 },
                oldSigner,
                System.currentTimeMillis(),
            )
        val pin = DeviceSecurity.pinOf(genesis)
        val original =
            DeviceSecurity.Record(
                pin,
                listOf(genesis),
                account.deviceId,
                x,
                lease = HeadLease.sign(genesis, oldSigner, System.currentTimeMillis()),
            )
        if (!recovery) {
            val created = CreatedAccount(account, original, "")
            return Adopted(
                account,
                original,
                SecurityOperation(
                    "creation",
                    byteArrayOf(),
                    DeviceSecurity.stateBytes(genesis),
                    local = created.operationMaterial(),
                ),
            )
        }
        val seed = ByteArray(32) { 0x44 }
        val signer = SigningKey(seed)
        val rx = ByteArray(32) { 0x55 }
        val member = Authority.newMember(signer, rx, 2u)
        val (state, proof) =
            Authority.recovery(
                genesis,
                member,
                listOf(member),
                ByteArray(32) { 6 },
                signer,
                oldRecovery,
                SigningKey(ByteArray(32) { 0x66 }).publicKey,
                System.currentTimeMillis(),
            )
        val recovered =
            Account(
                account.server,
                byteArrayOf(),
                seed,
                member.id,
                account.accountId,
                account.accountPublicKey,
            )
        val record =
            DeviceSecurity.Record(
                pin,
                listOf(genesis, state),
                member.id,
                rx,
                proofs = mapOf(state.core.epoch to proof.marshal()),
                lease = HeadLease.sign(state, signer, System.currentTimeMillis()),
            )
        val raw = DeviceSecurity.stateBytes(state)
        val publication =
            RecoveryPublication(
                account.server,
                RecoverySubmission(
                    "synthetic-recovery",
                    0,
                    "",
                    seed,
                    member.id,
                    rx,
                    account.accountId,
                    account.accountPublicKey,
                    DeviceSecurity.stateHash(state),
                    pin.genesis.copyOf(10),
                ),
                proof.marshal(),
                raw,
            )
        val request =
            SubmitRecoveryRequest.newBuilder()
                .setState(ByteString.copyFrom(raw))
                .setStatement(ByteString.copyFrom(proof.marshal()))
                .build()
        return Adopted(
            recovered,
            record,
            SecurityOperation(
                "recovery",
                state.core.parent,
                request.toByteArray(),
                local = publication.bytes(),
                phase = SecurityOperation.Phase.PUBLISHED,
            ),
        )
    }

    @Test
    fun completedCreationAndRecoveryAdoptionsDoNotLeaveABlockingOperation() =
        runBlocking<Unit> {
            for (recovery in listOf(false, true)) {
                val a = adopted(recovery)
                val store = TestSecurityStore(a.record)
                store.writeSecurityOperation(a.operation)
                val remote = Remote(a.account, Api.defaultHttp(), securityStorage = store)
                try {
                    remote.resumeSecurityOperation()
                    assertNull(
                        "completed ${a.operation.kind} adoption still blocks management",
                        store.readSecurityOperation(),
                    )
                    assertArrayEquals(
                        DeviceSecurity.stateBytes(a.record.head),
                        DeviceSecurity.stateBytes(store.readSecurity().head),
                    )
                } finally {
                    remote.close()
                }
            }
        }

    @Test
    fun unrelatedOrIncompleteAdoptionIsRetained() {
        for (recovery in listOf(false, true)) {
            val a = adopted(recovery)
            val store = TestSecurityStore(a.record)
            store.writeSecurityOperation(a.operation)
            val wrongServer =
                Account(
                    "https://other.example.com",
                    a.account.root,
                    a.account.deviceSeed,
                    a.account.deviceId,
                    a.account.accountId,
                    a.account.accountPublicKey,
                )
            assertThrows(DeviceSecurity.Failure::class.java) {
                reconcileCompletedAdoption(wrongServer, a.record, store)
            }
            assertNotNull(store.readSecurityOperation())
            if (recovery) {
                val before = adopted(false).record
                assertThrows(DeviceSecurity.Failure::class.java) {
                    reconcileCompletedAdoption(a.account, before, store)
                }
                assertNotNull(store.readSecurityOperation())
            }
        }
    }

    @Test
    fun completedAdoptionDoesNotDependOnDraftPhase() {
        for (recovery in listOf(false, true)) {
            val a = adopted(recovery)
            val store = TestSecurityStore(a.record)
            store.writeSecurityOperation(a.operation.advanced(SecurityOperation.Phase.DELIVERED))
            val advanced = a.record
            assertTrue(reconcileCompletedAdoption(a.account, advanced, store))
            assertNull(store.readSecurityOperation())
        }
    }
}
