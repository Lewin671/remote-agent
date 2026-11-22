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
                try await admin.ensureSecurityBootstrapped()
                let started = try await admin.securityClient.startPairing(webOrigin: "https://app.example.com")

                let joinStore = MemorySecurityStoreV4()
                let joinHost = try StandaloneSecurityHost(store: joinStore, http: w.http, allowPlainHttp: true)
                defer { joinHost.close() }
                async let joining = joinHost.client.startJoinPairing(rawLink: started.url)

                var polled = try await admin.securityClient.pollPairing(pairingId: started.pairingID)
                while !polled.joined {
                    try await realSleep(10)
                    polled = try await admin.securityClient.pollPairing(pairingId: started.pairingID)
                }
                let joined = try await joining
                #expect(joined.sasEmoji == polled.sasEmoji)

                // User cancels instead of confirming SAS: no record persisted and not added to chain.
                try await joinHost.client.cancelJoinPairing(pairingId: joined.pairingID)
                let afterBoot = try await joinHost.client.bootstrap()
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
                try await admin.ensureSecurityBootstrapped()
                let started = try await admin.securityClient.startPairing(webOrigin: "https://app.example.com")

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
                let joinHost = try StandaloneSecurityHost(store: joinStore, http: faulty, allowPlainHttp: true)
                defer { joinHost.close() }
                async let joining = joinHost.client.startJoinPairing(rawLink: started.url)

                var polled = try await admin.securityClient.pollPairing(pairingId: started.pairingID)
                while !polled.joined {
                    try await realSleep(10)
                    polled = try await admin.securityClient.pollPairing(pairingId: started.pairingID)
                }
                let joined = try await joining

                async let confirmJoin = joinHost.client.confirmJoinSas(pairingId: joined.pairingID)
                async let approveAdmin = admin.securityClient.approvePairing(pairingId: started.pairingID, sasConfirmed: true)
                _ = try await (confirmJoin, approveAdmin)

                let boot = try await joinHost.client.bootstrap()
                #expect(boot.bootstrap == .ready)
                let login = try await loginWithStore(api: Api(server: w.server.url, http: w.http), deviceId: boot.deviceID, store: joinStore)
                #expect(!login.token.isEmpty)
            }
        }

        @Test func rawServerErrorsTranslateToUserSafeLocalizedMessages() {
            let fx = PBSecurityWorkflowResultEffect.with {
                $0.ok = false
                $0.error = "security.pairing_expired_or_was_canceled"
                $0.errorCode = "not_found"
                $0.retryable = false
                $0.uncertain = false
            }
            let msg = translateSecurityErrorMessage(fx)
            #expect(msg == L10n.securityPairingExpiredOrWasCanceled())
            #expect(!msg.contains("security."))
        }
    }
}
