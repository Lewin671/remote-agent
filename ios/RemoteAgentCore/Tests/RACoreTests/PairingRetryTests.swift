import Foundation
import RAProto
import Testing

@testable import RACore

extension ServerTests {
    @Suite(.serialized, .timeLimit(.minutes(2))) struct PairingRetryTests {
        @Test func candidateDoesNotSignOrPersistWithoutLocalSasConfirmation() async throws {
            try await World.run(setUp: { w in
                try await w.daemon.putMachine("synthetic-manager")
                try await w.daemon.connect()
                try await w.daemon.waitReady()
            }) { w in
                let admin = try await w.remote(w.phone.account)
                try await admin.testBootstrapSecurity()
                let started = try await admin.securityClient.runAction {
                    $0.startPairing = .with { $0.webOrigin = "https://app.example.com" }
                }.pairing

                let joinStore = MemorySecurityStoreV4()
                let joinNode = try TestSecurityNode(store: joinStore, http: w.http, allowPlainHttp: true)
                defer { joinNode.close() }
                async let joining = joinNode.client.runAction {
                    $0.startJoinPairing = .with { $0.rawLink = started.url }
                }.pairing

                var polled = try await admin.securityClient.runAction {
                    $0.pollPairing = .with { $0.value = started.pairingID }
                }.pairing
                while !polled.joined {
                    try await realSleep(10)
                    polled = try await admin.securityClient.runAction {
                        $0.pollPairing = .with { $0.value = started.pairingID }
                    }.pairing
                }
                let joined = try await joining
                #expect(joined.sasEmoji == polled.sasEmoji)

                // User cancels instead of confirming SAS: no record persisted and not added to chain.
                _ = try await joinNode.client.runAction { $0.cancelJoinPairing = .with { $0.value = joined.pairingID } }
                let afterBoot = try await joinNode.client.bootstrap()
                #expect(afterBoot.bootstrap == .empty)
                #expect(!joinStore.hasPersistedState())
            }
        }

        @Test func aFailedDeliveryAcknowledgementDoesNotDiscardTheRegisteredIdentity() async throws {
            try await World.run(setUp: { w in
                try await w.daemon.putMachine("synthetic-manager")
                try await w.daemon.connect()
                try await w.daemon.waitReady()
            }) { w in
                let admin = try await w.remote(w.phone.account)
                try await admin.testBootstrapSecurity()
                let started = try await admin.securityClient.runAction {
                    $0.startPairing = .with { $0.webOrigin = "https://app.example.com" }
                }.pairing

                let faulty = Intercept.http(for: w.server.url) { request in
                    if request.url?.path.hasSuffix("/GetPairing") == true,
                       let body = request.httpBody,
                       let req = try? PBGetPairingRequest(serializedBytes: body),
                       req.received
                    {
                        throw URLError(.networkConnectionLost)
                    }
                    return request
                }
                let joinStore = MemorySecurityStoreV4()
                let joinNode = try TestSecurityNode(store: joinStore, http: faulty, allowPlainHttp: true)
                defer { joinNode.close() }
                async let joining = joinNode.client.runAction {
                    $0.startJoinPairing = .with { $0.rawLink = started.url }
                }.pairing

                var polled = try await admin.securityClient.runAction {
                    $0.pollPairing = .with { $0.value = started.pairingID }
                }.pairing
                while !polled.joined {
                    try await realSleep(10)
                    polled = try await admin.securityClient.runAction {
                        $0.pollPairing = .with { $0.value = started.pairingID }
                    }.pairing
                }
                let joined = try await joining

                async let confirmJoin = joinNode.client.runAction { $0.confirmJoinSas = .with { $0.value = joined.pairingID } }
                async let approveAdmin = admin.securityClient.runAction {
                    $0.approvePairing = .with { $0.pairingID = started.pairingID; $0.sasConfirmed = true }
                }
                _ = try await (confirmJoin, approveAdmin)

                let boot = try await joinNode.client.bootstrap()
                #expect(boot.bootstrap == .ready)
                let login = try await testLoginWithStore(api: TestApi(server: w.server.url, http: w.http), deviceId: boot.deviceID, store: joinStore)
                #expect(!login.token.isEmpty)
            }
        }

        @Test func rawServerErrorsTranslateToUserSafeLocalizedMessages() {
            let fx = PBSecurityWorkflowResultEffect.with {
                $0.ok = false
                $0.errorText = PBCoreText.with { $0.key = "security.pairing_expired_or_was_canceled" }
                $0.structuredErrorCode = .notFound
                $0.retryable = false
                $0.uncertain = false
            }
            let msg = translateSecurityErrorMessage(fx)
            #expect(msg == L10n.securityPairingExpiredOrWasCanceled())
            #expect(!msg.contains("security."))

            let adv = PBSecurityExitAdvice.with {
                $0.authority = true
                $0.messageText = PBCoreText.with { $0.key = "security.this_device_is_the_only_managing_client_transfer_management" }
            }
            let rendered = renderCoreText(adv.messageText)
            #expect(rendered == L10n.securityThisDeviceIsTheOnlyManagingClientTransferManagement())
            #expect(!rendered.contains("浏览器"))
            #expect(!rendered.lowercased().contains("browser"))
        }
    }
}
