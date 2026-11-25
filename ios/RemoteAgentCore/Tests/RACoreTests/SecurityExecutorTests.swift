import Connect
import CryptoKit
import Foundation
import os
import RAProto
import SwiftProtobuf
import Testing

@testable import RACore

private final class StalledRPCProtocol: URLProtocol, @unchecked Sendable {
    static let latches = OSAllocatedUnfairLock<(started: TestLatch?, stopped: TestLatch?)>(initialState: (nil, nil))
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let started = Self.latches.withLock { $0.started }
        if let started { Task { await started.open() } }
    }
    override func stopLoading() {
        let stopped = Self.latches.withLock { $0.stopped }
        if let stopped { Task { await stopped.open() } }
    }
}

private final class TimeoutRPCProtocol: URLProtocol, @unchecked Sendable {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        client?.urlProtocol(self, didFailWithError: URLError(.timedOut))
    }
    override func stopLoading() {}
}

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

        // 2. Closing executor cancels in-flight HTTP request and stops URLSession loading promptly.
        let started = TestLatch()
        let stopped = TestLatch()
        StalledRPCProtocol.latches.withLock { $0 = (started, stopped) }
        defer { StalledRPCProtocol.latches.withLock { $0 = (nil, nil) } }

        let stalledCfg = HTTP.defaultConfiguration()
        stalledCfg.protocolClasses = [StalledRPCProtocol.self]
        let stalledExec = SecurityExecutor(
            store: MemorySecurityStoreV4(),
            http: HTTP(configuration: stalledCfg),
            allowPlainHttp: true,
            emit: { _ in }
        )
        stalledExec.rpc(.with {
            $0.opID = 77
            $0.method = .getSecurityStates
            $0.server = "https://example.com"
        })
        await started.wait()
        stalledExec.close()
        await stopped.wait()

        // 3. Timed-out HTTP unary call maps cleanly to deadlineExceeded with networkError = true.
        let timeoutCfg = HTTP.defaultConfiguration()
        timeoutCfg.protocolClasses = [TimeoutRPCProtocol.self]
        let mailbox = Mailbox<PBCoreEvent>()
        let exec = SecurityExecutor(
            store: MemorySecurityStoreV4(),
            http: HTTP(configuration: timeoutCfg),
            allowPlainHttp: true,
            emit: { mailbox.put($0) }
        )
        defer { exec.close() }
        exec.rpc(.with {
            $0.opID = 78
            $0.method = .getSecurityStates
            $0.server = "https://example.com"
        })
        let rpcRes = try await mailbox.take().securityRpcResult
        #expect(rpcRes.opID == 78)
        #expect(rpcRes.connectCode == Int32(Code.deadlineExceeded.rawValue))
        #expect(rpcRes.networkError)
    }

    @Test func parseConnectErrorBodyForwardsUnpaddedBase64Details() throws {
        var detail = PBRelayErrorDetail()
        detail.reason = .protocolMismatch
        detail.serverRelayVersion = 17
        let rawBytes = try detail.serializedData()
        let unpadded = rawBytes.base64EncodedString().replacingOccurrences(of: "=", with: "")
        let json = """
        {"code":"failed_precondition","message":"relay version mismatch","details":[{"type":"remoteagent.v1.RelayErrorDetail","value":"\(unpadded)"},{"type":"ignored.bad","value":"!!!"}]}
        """
        let parsed = SecurityExecutor.parseConnectErrorBody(Data(json.utf8), fallbackCode: .unknown)
        #expect(parsed.code == .failedPrecondition)
        #expect(parsed.message == "relay version mismatch")
        #expect(parsed.details.count == 1)
        #expect(parsed.details[0].typeName == "remoteagent.v1.RelayErrorDetail")
        #expect(parsed.details[0].value == rawBytes)
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
                content.diskStatus.observedMs = 1
                content.diskStatus.availableBytes = 1_500_000_000
                content.diskStatus.pressure = .critical
                let payload = try content.serializedData()
                let sealed = try await daemonHost.securityOp {
                    $0.sealEnvelope = .with {
                        $0.purpose = SecurityPurpose.notify.rawValue
                        $0.machineID = daemon.id
                        $0.requestID = "disk:1:\(PBResourcePressure.critical.rawValue)"
                        $0.plaintext = payload
                    }
                }
                #expect(sealed.error.isEmpty)
                let envData = try sealed.sealed.envelope.serializedData()
                let expectedTag = "\(daemon.id)/NOTIFY_KIND_MACHINE_DISK"
                let noticeEn = try receiveMachineDiskNotification(
                    envelope: envData,
                    notifyRecord: notifyBlob,
                    machineID: daemon.id,
                    tag: expectedTag,
                    showContent: true,
                    locked: false,
                    locale: "en"
                )
                #expect(noticeEn?.machineID == daemon.id)
                #expect(noticeEn?.tag == expectedTag)
                #expect(noticeEn?.body == "Clean up now or sessions may not start.")
                let noticeZh = try receiveMachineDiskNotification(
                    envelope: envData,
                    notifyRecord: notifyBlob,
                    machineID: daemon.id,
                    tag: expectedTag,
                    showContent: true,
                    locked: false,
                    locale: "zh-CN"
                )
                #expect(noticeZh?.title == "Notify Mac 磁盘快满了，只剩 1.5 GB")

                // Locked state must redact detail to full_generic_body.
                let lockedNotice = try receiveMachineDiskNotification(
                    envelope: envData,
                    notifyRecord: notifyBlob,
                    machineID: daemon.id,
                    tag: expectedTag,
                    showContent: true,
                    locked: true,
                    locale: "en"
                )
                #expect(lockedNotice?.title == "")
                #expect(lockedNotice?.body == L10n.machineStatusHomeWarning(locale: "en"))

                // Fail-closed when outer tag or machineID does not match.
                let wrongTag = try receiveMachineDiskNotification(
                    envelope: envData,
                    notifyRecord: notifyBlob,
                    machineID: daemon.id,
                    tag: "wrong-tag",
                    showContent: true,
                    locked: false,
                    locale: "en"
                )
                #expect(wrongTag == nil)

                var tampered = sealed.sealed.envelope
                tampered.signature[0] ^= 0xff
                #expect(throws: SecurityWorkflowError.self) {
                    _ = try receiveMachineDiskNotification(
                        envelope: try tampered.serializedData(),
                        notifyRecord: notifyBlob,
                        machineID: daemon.id,
                        tag: expectedTag,
                        showContent: true,
                        locked: false,
                        locale: "en"
                    )
                }
            }
        }
    }
}
