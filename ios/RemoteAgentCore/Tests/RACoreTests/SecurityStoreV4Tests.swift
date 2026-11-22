import CryptoKit
import Foundation
import RAProto
import Testing

@testable import RACore

@Suite(.timeLimit(.minutes(2))) struct SecurityStoreV4Tests {
    @Test func casAndBatchCommitAreAtomicAndPromoteCandidateKeys() throws {
        let store = MemorySecurityStoreV4()
        let edPub = try store.generateKey(kind: .candidateEd, slot: "slot-a", persist: true)
        let xPub = try store.generateKey(kind: .candidateX, slot: "slot-a", persist: true)
        store.saveLoginToken(SavedLogin(token: "tok-1", expiresMs: 10_000), server: "https://example.com", deviceId: "dev-1")

        let firstWrite = store.executeStorage(PBSecurityStorageEffect.with {
            $0.opID = 1
            $0.op = .writeCas
            $0.bucket = .operation
            $0.key = "pending"
            $0.createOnly = true
            $0.nextRevision = "r1"
            $0.data = Data("op-v1".utf8)
        })
        #expect(!firstWrite.conflict)
        #expect(firstWrite.revision == "r1")

        let conflictWrite = store.executeStorage(PBSecurityStorageEffect.with {
            $0.opID = 2
            $0.op = .writeCas
            $0.bucket = .operation
            $0.key = "pending"
            $0.expectedRevision = "stale-rev"
            $0.nextRevision = "r2"
            $0.data = Data("op-v2".utf8)
        })
        #expect(conflictWrite.conflict)

        let failedBatch = store.executeStorage(PBSecurityStorageEffect.with {
            $0.opID = 3
            $0.op = .batchCommit
            $0.promoteEdSlot = "slot-a"
            $0.promoteXSlot = "slot-a"
            $0.clearLoginToken_p = true
            $0.batch = [
                PBSecurityStorageWrite.with {
                    $0.bucket = .operation
                    $0.key = "pending"
                    $0.remove = true
                    $0.expectedRevision = "wrong-rev"
                }
            ]
        })
        #expect(failedBatch.conflict)
        #expect(!failedBatch.hasDeviceEd_p)
        #expect(store.loadLoginToken(server: "https://example.com", deviceId: "dev-1") != nil)

        let committedBatch = store.executeStorage(PBSecurityStorageEffect.with {
            $0.opID = 4
            $0.op = .batchCommit
            $0.promoteEdSlot = "slot-a"
            $0.promoteXSlot = "slot-a"
            $0.clearLoginToken_p = true
            $0.batch = [
                PBSecurityStorageWrite.with {
                    $0.bucket = .record
                    $0.key = "record"
                    $0.createOnly = true
                    $0.nextRevision = "r-rec"
                    $0.data = Data("rec-v1".utf8)
                },
                PBSecurityStorageWrite.with {
                    $0.bucket = .notifyRecord
                    $0.key = "notify"
                    $0.createOnly = true
                    $0.nextRevision = "r-ntf"
                    $0.data = Data("ntf-v1".utf8)
                },
                PBSecurityStorageWrite.with {
                    $0.bucket = .operation
                    $0.key = "pending"
                    $0.remove = true
                    $0.expectedRevision = "r1"
                },
            ]
        })
        #expect(!committedBatch.conflict)
        #expect(committedBatch.hasDeviceEd_p)
        #expect(committedBatch.hasDeviceX_p)
        #expect(committedBatch.deviceEdPub == edPub)
        #expect(committedBatch.deviceXPub == xPub)
        #expect(store.loadLoginToken(server: "https://example.com", deviceId: "dev-1") == nil)
        #expect(store.readNotifyBlob() == Data("ntf-v1".utf8))

        let sig = try store.signKey(kind: .deviceEd, slot: "", message: Data("msg".utf8))
        let pubKey = try Curve25519.Signing.PublicKey(rawRepresentation: edPub)
        #expect(pubKey.isValidSignature(sig, for: Data("msg".utf8)))
    }

    @Test func legacyClearFlagAndEncapsulateRoundTripWorkCorrectly() throws {
        let store = MemorySecurityStoreV4(seedLegacyMarker: true)
        let boot1 = store.executeStorage(PBSecurityStorageEffect.with {
            $0.opID = 1
            $0.op = .readBootstrap
        })
        #expect(boot1.legacyCleared)
        #expect(store.hasUpgradeNotice())

        store.dismissLegacyRepairNotice()
        #expect(!store.hasUpgradeNotice())

        let xPub = try store.generateKey(kind: .deviceX, slot: "", persist: true)
        let pairs = try store.encapsulate(recipients: [
            PBSecurityEncapsulateRecipient.with {
                $0.memberID = "m-1"
                $0.xPublic = xPub
            }
        ])
        #expect(pairs.count == 1)
        let agree = try store.agreeKey(kind: .deviceX, slot: "", peerPublic: pairs[0].enc)
        #expect(pairs[0].dh == agree)
    }

    @Test func standaloneHostPersistsPendingCreationDraftAndSurvivesRebootstrap() async throws {
        let store = MemorySecurityStoreV4(seedLegacyMarker: true)
        let host1 = try StandaloneSecurityHost(store: store, http: HTTP(), allowPlainHttp: true)
        let st1 = try await host1.client.bootstrap()
        #expect(st1.bootstrap == .empty)
        #expect(st1.legacyRepairNotice)

        let dismissed = try await host1.client.dismissUpgradeNotice()
        #expect(!dismissed.legacyRepairNotice)

        let prep = try await host1.client.prepareCreation(server: "https://example.com", storagePersisted: true)
        #expect(!prep.recoveryCode.isEmpty)
        try await host1.client.confirmCreationCodeSaved()
        host1.close()

        let host2 = try StandaloneSecurityHost(store: store, http: HTTP(), allowPlainHttp: true)
        defer { host2.close() }
        let st2 = try await host2.client.bootstrap()
        #expect(st2.bootstrap == .pendingCreate)
        #expect(st2.pendingOp.codeSaved)
        #expect(!st2.legacyRepairNotice)
    }

    @Test func batchCommitFailureInWriteBucketsPreservesCandidateSlotsForRetry() throws {
        enum InjectError: Error { case diskFull }

        final class FaultyBacking: SecurityStoreV4Backend, @unchecked Sendable {
            var buckets: [SecurityStoreBucketKey: SecurityStoreStoredBucket] = [:]
            var keys: [SecurityStoreKeyId: SecurityStoreStoredKey] = [:]
            var notify: Data?
            var loginToken: Data?
            var upgradeNotice = false
            var failNextWriteBuckets = true

            func readBuckets() -> [SecurityStoreBucketKey: SecurityStoreStoredBucket] { buckets }
            func writeBuckets(_ next: [SecurityStoreBucketKey: SecurityStoreStoredBucket]) throws {
                if failNextWriteBuckets {
                    failNextWriteBuckets = false
                    throw InjectError.diskFull
                }
                buckets = next
            }
            func readKeys() -> [SecurityStoreKeyId: SecurityStoreStoredKey] { keys }
            func writeKeys(_ next: [SecurityStoreKeyId: SecurityStoreStoredKey]) { keys = next }
            func readNotify() -> Data? { notify }
            func writeNotify(_ data: Data) { notify = data }
            func deleteNotify() { notify = nil }
            func readLoginTokenRaw() -> Data? { loginToken }
            func writeLoginTokenRaw(_ data: Data) { loginToken = data }
            func deleteLoginToken() { loginToken = nil }
            func hasUpgradeNotice() -> Bool { upgradeNotice }
            func setUpgradeNotice(_ on: Bool) { upgradeNotice = on }
            func detectAndClearLegacy() -> Bool { false }
            func clearAllBacking() {
                buckets.removeAll()
                keys.removeAll()
                notify = nil
                loginToken = nil
                upgradeNotice = false
            }
        }

        let backing = FaultyBacking()
        let store = SecurityStoreV4Core(backend: backing)

        let edPub = try store.generateKey(kind: .candidateEd, slot: "slot-retry", persist: true)
        let xPub = try store.generateKey(kind: .candidateX, slot: "slot-retry", persist: true)

        let batchEffect = PBSecurityStorageEffect.with {
            $0.opID = 1
            $0.op = .batchCommit
            $0.promoteEdSlot = "slot-retry"
            $0.promoteXSlot = "slot-retry"
            $0.batch = [
                PBSecurityStorageWrite.with {
                    $0.bucket = .record
                    $0.key = "record"
                    $0.nextRevision = "rec-1"
                    $0.data = Data("rec-v1".utf8)
                }
            ]
        }

        let failed = store.executeStorage(batchEffect)
        #expect(!failed.error.isEmpty)
        #expect(backing.buckets.isEmpty)
        // Candidate slots remain present so retry promotion does not fail with missing promote slot.
        #expect(!((try store.signKey(kind: .candidateEd, slot: "slot-retry", message: Data("probe".utf8))).isEmpty))

        let retried = store.executeStorage(batchEffect)
        #expect(retried.error.isEmpty)
        #expect(retried.hasDeviceEd_p)
        #expect(retried.deviceEdPub == edPub)
        #expect(retried.deviceXPub == xPub)
        #expect(backing.buckets[SecurityStoreBucketKey(bucket: Int32(PBSecurityStoreBucket.record.rawValue), key: "record")]?.revision == "rec-1")
        #expect(throws: Error.self) {
            _ = try store.signKey(kind: .candidateEd, slot: "slot-retry", message: Data("gone".utf8))
        }
    }
}
