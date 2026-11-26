import Connect
import CryptoKit
import Foundation
import os
import RAProto
import SwiftProtobuf
import Testing

@testable import RACore

/// Per-host-keyed stalled URLProtocol so concurrent tests never collide on static state.
private final class StalledRPCProtocol: URLProtocol, @unchecked Sendable {
    private struct Slot {
        let started: TestLatch
        let stopped: TestLatch
    }
    private static let slots = OSAllocatedUnfairLock<[String: Slot]>(initialState: [:])

    static func register(host: String, started: TestLatch, stopped: TestLatch) {
        slots.withLock { $0[host] = Slot(started: started, stopped: stopped) }
    }

    static func unregister(host: String) {
        slots.withLock { _ = $0.removeValue(forKey: host) }
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        guard let host = request.url?.host else { return }
        let started = Self.slots.withLock { $0[host]?.started }
        if let started { Task { await started.open() } }
    }
    override func stopLoading() {
        guard let host = request.url?.host else { return }
        let stopped = Self.slots.withLock { $0[host]?.stopped }
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
        try await dispatched.wait(timeoutMs: 5_000, label: "bootstrap dispatch")
        waiter.cancel()
        await #expect(throws: CancellationError.self) {
            _ = try await waiter.value
        }

        // 2. Closing executor cancels in-flight HTTP request and stops URLSession loading promptly.
        let started = TestLatch()
        let stopped = TestLatch()
        let stallHost = "stall-sec-\(UUID().uuidString.lowercased()).example.com"
        StalledRPCProtocol.register(host: stallHost, started: started, stopped: stopped)
        defer { StalledRPCProtocol.unregister(host: stallHost) }

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
            $0.server = "https://\(stallHost)"
        })
        try await started.wait(timeoutMs: 5_000, label: "stalled security rpc start")
        stalledExec.close()
        try await stopped.wait(timeoutMs: 5_000, label: "stalled security rpc cancel")

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

    @Test func relayRpcRoundTripHeadersErrorDetailsCancelAndBootstrapToken() async throws {
        // 1. POST unary round-trip carries Authorization, Attempt, Connect-Protocol-Version, and returns body + relayEpoch.
        let listener = try Listener()
        defer { listener.close() }
        let captured = OSAllocatedUnfairLock<String>(initialState: "")
        let reqBody = try PBGetChallengeRequest.with { $0.deviceID = "dev-42" }.serializedData()
        let respBody = try PBLoginResponse.with { $0.token = "resp-tok" }.serializedData()

        var detail = PBNotForwarded()
        detail.reason = "machine_offline"
        let detailBytes = try detail.serializedData()
        let detailB64 = detailBytes.base64EncodedString().replacingOccurrences(of: "=", with: "")
        let errJson = Data("""
        {"code":"unavailable","message":"offline","details":[{"type":"remoteagent.v1.NotForwarded","value":"\(detailB64)"}]}
        """.utf8)

        listener.serve { fd in
            guard let headLines = readHead(fd) else { return }
            let head = headLines.joined(separator: "\r\n")
            captured.withLock { $0 = head }
            if head.hasPrefix("GET /healthz ") {
                let res = "HTTP/1.1 200 OK\r\nRemote-Agent-Relay-Epoch: 99\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok"
                writeAll(fd, Data(res.utf8))
            } else if head.contains("/Forward ") {
                let header = "HTTP/1.1 503 Service Unavailable\r\nContent-Type: application/json\r\nRemote-Agent-Relay-Epoch: 77\r\nContent-Length: \(errJson.count)\r\nConnection: close\r\n\r\n"
                writeAll(fd, Data(header.utf8) + errJson)
            } else {
                let header = "HTTP/1.1 200 OK\r\nContent-Type: application/proto\r\nRemote-Agent-Relay-Epoch: 55\r\nContent-Length: \(respBody.count)\r\nConnection: close\r\n\r\n"
                writeAll(fd, Data(header.utf8) + respBody)
            }
        }

        let mailbox = Mailbox<PBCoreEvent>()
        let store = MemorySecurityStoreV4()
        let serverURL = "http://127.0.0.1:\(listener.port)"
        store.writeBuckets([
            SecurityStoreBucketKey(bucket: 1, key: ""): SecurityStoreStoredBucket(bucket: 1, key: "", revision: "1", data: Data([1]))
        ])
        store.saveLoginToken(SavedLogin(token: "saved-tok", expiresMs: 9_876_543), server: serverURL, deviceId: "dev-42")
        let exec = SecurityExecutor(
            store: store,
            http: HTTP(),
            allowPlainHttp: true,
            defaultServer: serverURL,
            defaultDeviceId: "dev-42",
            emit: { mailbox.put($0) }
        )
        defer { exec.close() }

        // ReadBootstrap hydrates persisted login token for defaultDeviceId.
        exec.storage(.with { $0.opID = 10; $0.op = .readBootstrap })
        let bootRes = try await mailbox.take().securityStorageResult
        #expect(bootRes.opID == 10)
        #expect(bootRes.loginToken == "saved-tok")
        #expect(bootRes.loginTokenExpiresMs == 9_876_543)

        // POST round-trip.
        exec.relayRpc(.with {
            $0.opID = 11
            $0.method = .getChallenge
            $0.authToken = "tok-abc"
            $0.attemptHeader = "att-7"
            $0.requestBytes = reqBody
        })
        let postRes = try await mailbox.take().relayRpcResult
        #expect(postRes.opID == 11)
        #expect(postRes.connectCode == 0)
        #expect(postRes.responseBytes == respBody)
        #expect(postRes.relayEpoch == "55")
        let lastHead = captured.withLock { $0 }
        #expect(lastHead.contains("Authorization: Bearer tok-abc"))
        #expect(lastHead.contains("Remote-Agent-Attempt: att-7"))
        #expect(lastHead.contains("Connect-Protocol-Version: 1"))

        // GET /healthz round-trip.
        exec.relayRpc(.with {
            $0.opID = 12
            $0.method = .healthz
        })
        let healthRes = try await mailbox.take().relayRpcResult
        #expect(healthRes.opID == 12)
        #expect(healthRes.connectCode == 0)
        #expect(healthRes.relayEpoch == "99")

        // Connect error JSON + unpadded base64 details + relayEpoch passthrough.
        exec.relayRpc(.with {
            $0.opID = 13
            $0.method = .forward
            $0.authToken = "tok-abc"
            $0.requestBytes = Data([1, 2])
        })
        let errRes = try await mailbox.take().relayRpcResult
        #expect(errRes.opID == 13)
        #expect(errRes.connectCode == Int32(Code.unavailable.rawValue))
        #expect(errRes.diagnosticMessage == "offline")
        #expect(errRes.relayEpoch == "77")
        #expect(errRes.errorDetails.count == 1)
        #expect(errRes.errorDetails[0].typeName == "remoteagent.v1.NotForwarded")
        #expect(errRes.errorDetails[0].value == detailBytes)

        // Cancel effect aborts an in-flight stalled request without emitting RelayRpcResultEvent.
        let started = TestLatch()
        let stopped = TestLatch()
        let stallHost = "stall-relay-\(UUID().uuidString.lowercased()).example.com"
        StalledRPCProtocol.register(host: stallHost, started: started, stopped: stopped)
        defer { StalledRPCProtocol.unregister(host: stallHost) }

        let stalledCfg = HTTP.defaultConfiguration()
        stalledCfg.protocolClasses = [StalledRPCProtocol.self]
        let cancelMailbox = Mailbox<PBCoreEvent>()
        let cancelExec = SecurityExecutor(
            store: MemorySecurityStoreV4(),
            http: HTTP(configuration: stalledCfg),
            allowPlainHttp: true,
            defaultServer: "https://\(stallHost)",
            emit: { cancelMailbox.put($0) }
        )
        defer { cancelExec.close() }
        cancelExec.relayRpc(.with {
            $0.opID = 99
            $0.method = .listMachines
        })
        try await started.wait(timeoutMs: 5_000, label: "stalled relay rpc start")
        cancelExec.cancelRelayRpc(.with { $0.opID = 99 })
        try await stopped.wait(timeoutMs: 5_000, label: "stalled relay rpc cancel")
        #expect(cancelMailbox.all.isEmpty)
    }

    @Test func opaqueScheduleCacheBlobRoundTripAndMachineDeletion() async throws {
        let cache = MemoryCache()
        let remote = try Remote(
            account: Account(server: "https://example.com", deviceId: "dev-1", accountId: "acc-1"),
            http: HTTP(),
            cache: cache,
            securityStore: MemorySecurityStoreV4()
        )
        defer { remote.close() }

        let opaque = Data([0xde, 0xad, 0xbe, 0xef, 0x01, 0x02])
        await remote.writeScheduleCacheBlob("m1", blob: opaque)
        #expect(await remote.readScheduleCacheBlob("m1") == opaque)

        await remote.executeCacheDelete(.with { $0.removeMachineID = "m1" })
        #expect(await remote.readScheduleCacheBlob("m1") == nil)

        await remote.writeScheduleCacheBlob("m2", blob: opaque)
        await remote.deleteScheduleCache("m2")
        #expect(await remote.readScheduleCacheBlob("m2") == nil)
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
                _ = try await admin.testSyncSecurity()
                _ = try await admin.testRenewLease(force: true)
                let notifyBlob = try #require(adminStore.readNotifyBlob())

                let daemon = w.daemon
                let daemonStore = try #require(TestSecurity.find(w.server.url)?.store(daemon.id))
                let daemonNode = try TestSecurityNode(store: daemonStore, http: w.http, allowPlainHttp: true)
                defer { daemonNode.close() }
                let daemonBoot = try await daemonNode.client.bootstrap()
                #expect(daemonBoot.bootstrap == .ready)

                var content = PBNotifyContent()
                content.kind = .machineDisk
                content.machineID = daemon.id
                content.machineName = "Notify Mac"
                content.diskStatus.observedMs = 1
                content.diskStatus.availableBytes = 1_500_000_000
                content.diskStatus.pressure = .critical
                let payload = try content.serializedData()
                let sealed = try await daemonNode.securityOp {
                    $0.sealEnvelope = .with {
                        $0.purpose = SecurityPurpose.notify.rawValue
                        $0.machineID = daemon.id
                        $0.requestID = "disk:1:\(PBResourcePressure.critical.rawValue)"
                        $0.plaintext = payload
                    }
                }
                #expect(sealed.error.isEmpty)
                let envData = sealed.sealed.rawEnvelope
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
