import Connect
import CryptoKit
import Foundation
import os
import RAProto
import SwiftProtobuf
import Testing

@testable import RACore

@Suite(.timeLimit(.minutes(2))) struct SecurityExecutorTests {
    @Test func keySignAndKeyAgreeDelegateToStoreAndValidateInputs() async throws {
        let store = MemorySecurityStoreV4()
        let edPub = try store.generateKey(kind: .deviceEd, slot: "", persist: true)
        let xPub = try store.generateKey(kind: .deviceX, slot: "", persist: true)
        let peerXPriv = Curve25519.KeyAgreement.PrivateKey()
        let peerXPub = peerXPriv.publicKey.rawRepresentation

        let mailbox = Mailbox<PBCoreEvent>()
        let exec = SecurityExecutor(store: store, http: HTTP()) { ev in
            mailbox.put(ev)
        }
        defer { exec.close() }

        let msg = Data("sign-this-transcript".utf8)
        exec.keySign(.with { $0.opID = 11; $0.keyKind = .deviceEd; $0.message = msg })
        let signed = try await mailbox.take().securityKeySigned
        #expect(signed.opID == 11 && signed.error.isEmpty)
        let edKey = try Curve25519.Signing.PublicKey(rawRepresentation: edPub)
        #expect(edKey.isValidSignature(signed.signature, for: msg))

        exec.keyAgree(.with { $0.opID = 22; $0.keyKind = .deviceX; $0.peerPublic = peerXPub })
        let agreed = try await mailbox.take().securityKeyAgreed
        #expect(agreed.opID == 22 && agreed.error.isEmpty)
        let ourXKey = try Curve25519.KeyAgreement.PublicKey(rawRepresentation: xPub)
        let expectedShared = try peerXPriv.sharedSecretFromKeyAgreement(with: ourXKey).withUnsafeBytes { Data($0) }
        #expect(agreed.shared == expectedShared)

        exec.keySign(.with { $0.opID = 1; $0.keyKind = .deviceEd; $0.message = Data() })
        let emptySign = try await mailbox.take().securityKeySigned
        #expect(emptySign.opID == 1 && !emptySign.error.isEmpty)

        exec.keyAgree(.with { $0.opID = 2; $0.keyKind = .deviceX; $0.peerPublic = Data([1, 2, 3]) })
        let shortPeer = try await mailbox.take().securityKeyAgreed
        #expect(shortPeer.opID == 2 && !shortPeer.error.isEmpty)
    }

    @Test func missingStoreKeysFailCleanly() async throws {
        let empty = MemorySecurityStoreV4()
        let mailbox = Mailbox<PBCoreEvent>()
        let exec = SecurityExecutor(store: empty, http: HTTP()) { ev in
            mailbox.put(ev)
        }
        defer { exec.close() }

        exec.keySign(.with { $0.opID = 4; $0.keyKind = .deviceEd; $0.message = Data("ok".utf8) })
        let signRes = try await mailbox.take().securityKeySigned
        #expect(signRes.opID == 4 && !signRes.error.isEmpty)

        exec.keyAgree(.with { $0.opID = 5; $0.keyKind = .deviceX; $0.peerPublic = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation })
        let agreeRes = try await mailbox.take().securityKeyAgreed
        #expect(agreeRes.opID == 5 && !agreeRes.error.isEmpty)
    }

    @Test func securityClientAndExecutorAbortPromptlyOnCancellationAndTimeout() async throws {
        // 1. SecurityClient cancelled while waiting on a silent dispatcher throws CancellationError immediately.
        let dispatched = TestLatch()
        let client = SecurityClient(
            dispatchEvent: { _ in Task { await dispatched.open() } },
            setForegroundFn: { _ in },
            isClosedFn: { false }
        )
        let waiter = Task { try await client.bootstrap() }
        await dispatched.wait()
        waiter.cancel()
        await #expect(throws: CancellationError.self) {
            _ = try await waiter.value
        }

        // 2. Stalled HTTP unary call is cancelled when rpc timeout fires instead of hanging forever.
        final class StalledHTTP: HTTPClientInterface, @unchecked Sendable {
            let started = TestLatch()
            let canceledCount = OSAllocatedUnfairLock(initialState: 0)
            func unary(
                request: HTTPRequest<Data?>,
                onMetrics: @escaping @Sendable (HTTPMetrics) -> Void,
                onResponse: @escaping @Sendable (HTTPResponse) -> Void
            ) -> Cancelable {
                Task { await started.open() }
                return Cancelable { self.canceledCount.withLock { $0 += 1 } }
            }
            func stream(request: HTTPRequest<Data?>, responseCallbacks: ResponseCallbacks) -> RequestCallbacks<Data> {
                RequestCallbacks(cancel: {}, sendData: { _ in }, sendClose: {})
            }
        }
        let stalled = StalledHTTP()
        let mailbox = Mailbox<PBCoreEvent>()
        let exec = SecurityExecutor(
            store: MemorySecurityStoreV4(),
            http: stalled,
            allowPlainHttp: true,
            rpcTimeoutMs: { _ in 25 },
            emit: { mailbox.put($0) }
        )
        defer { exec.close() }
        exec.rpc(.with {
            $0.opID = 77
            $0.method = .getSecurityStates
            $0.server = "https://example.com"
        })
        await stalled.started.wait()
        let rpcRes = try await mailbox.take().securityRpcResult
        #expect(rpcRes.opID == 77)
        #expect(rpcRes.connectCode == Int32(Code.deadlineExceeded.rawValue))
        #expect(rpcRes.networkError)
        #expect(stalled.canceledCount.withLock { $0 == 1 })
    }
}

extension ServerTests {
    @Suite(.serialized, .timeLimit(.minutes(2))) struct NotifyCoreOpeningTests {
        @Test func openNotifyEnvelopeAndMachineDiskNoticeWorkEndToEnd() async throws {
            try await World.run(setUp: { w in
                try await w.daemon.putMachine("notify-mac")
            }) { w in
                let adminStore = try #require(TestSecurity.find(w.server.url)?.store(w.phone.account.deviceId))
                let admin = try await w.remote(w.phone.account)
                _ = try await admin.syncSecurity()
                _ = try await admin.renewLeaseBlocking(force: true)
                let notifyBlob = try #require(adminStore.readNotifyBlob())

                let daemon = w.daemon
                let daemonStore = try #require(TestSecurity.find(w.server.url)?.store(daemon.id))
                let daemonHost = try StandaloneSecurityHost(store: daemonStore, http: w.http, allowPlainHttp: true)
                defer { daemonHost.close() }
                let daemonBoot = try await daemonHost.client.bootstrap()
                #expect(daemonBoot.bootstrap == .ready)

                var content = PBNotifyContent()
                content.kind = .machineDisk
                content.machineID = daemon.id
                content.machineName = "Notify Mac"
                content.diskStatus.availableBytes = 1_500_000_000
                content.diskStatus.pressure = .critical
                let payload = try content.serializedData()
                let sealed = try await daemonHost.securityOp {
                    $0.sealEnvelope = .with {
                        $0.purpose = SecurityPurpose.notify.rawValue
                        $0.machineID = daemon.id
                        $0.requestID = "disk:1:1"
                        $0.plaintext = payload
                    }
                }
                #expect(sealed.error.isEmpty)
                let notice = try receiveMachineDiskNotification(
                    envelope: try sealed.sealed.envelope.serializedData(),
                    notifyRecord: notifyBlob,
                    locale: "en"
                )
                #expect(notice?.machineID == daemon.id)

                var tampered = sealed.sealed.envelope
                tampered.signature[0] ^= 0xff
                #expect(throws: SecurityWorkflowError.self) {
                    _ = try receiveMachineDiskNotification(
                        envelope: try tampered.serializedData(),
                        notifyRecord: notifyBlob,
                        locale: "en"
                    )
                }
            }
        }
    }
}
