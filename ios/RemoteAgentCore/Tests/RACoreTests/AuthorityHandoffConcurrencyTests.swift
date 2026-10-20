import Foundation
import RAProto
import Testing

@testable import RACore

@Suite(.enabled(if: GoServer.available, GoServer.missing))
struct AuthorityHandoffConcurrencyTests {
    @Test func discardCannotDeleteAnOfferWhileItsPublicationIsInFlight() async throws {
        let server = try await ManagementRelay(); defer { server.close() }
        let vault = AuthorityTestVault(), gate = AuthorityGate()
        gate.set(active: true, protectedDataAvailable: true)
        let bootstrap = AuthorityBootstrap(storage: vault, gate: gate)
        _ = try await bootstrap.prepareCreation(server: server.url)
        try await bootstrap.confirmRecoveryCodeSaved()
        let account = try await bootstrap.continueCreation(setupToken: server.setupToken)
        try await bootstrap.finishCreation()
        let record = try vault.readSecurity()
        let offer = SecurityOperation(kind: .handoffOffer, parent: try DeviceSecurity.stateHash(record.state), signed: try Authority.newRecovery().publicKey)
        _ = try vault.prepareOperation(expectedHead: offer.parent) { _ in offer }
        let entered = Signal(), release = Mailbox<Bool>()
        let http = Intercept.http(for: server.url) { request in
            if request.url?.path.hasSuffix("/PutDeviceMeta") == true {
                entered.set()
                _ = await release.poll(5_000)
                throw URLError(.networkConnectionLost)
            }
            return request
        }
        let api = try Api(server: server.url, http: http)
        let security = DeviceSecurityClient(store: vault, memberID: account.deviceId, signingKey: try SigningKey(seed: account.deviceSeed), fetchStates: { after in
            var request = PBGetSecurityStatesRequest(); request.afterEpoch = after
            return try await api.client.getSecurityStates(request: request, headers: Api.headers(token: login(api: api, account: account))).value()
        })
        let coordinator = AuthorityCoordinator(account: account, api: api, storage: vault, security: security, gate: gate,
            token: { try await login(api: api, account: account) })
        let publishing = Task { try await coordinator.resumeHandoffOffer() }
        defer { release.put(true); publishing.cancel() }
        for _ in 0..<500 { if entered.isSet { break }; try await realSleep(10) }
        try #require(entered.isSet)
        var rejected = false
        do { try await coordinator.discardHandoffOffer() } catch { rejected = true }
        #expect(rejected)
        #expect(try vault.readOperation()?.id == offer.id)
        release.put(true)
        do { try await publishing.value; Issue.record("ignored uncertain metadata publication") } catch {}
        #expect(try vault.readOperation()?.result.isEmpty == false)
        var requiresWarning = false
        do { try await coordinator.discardHandoffOffer() } catch let error as PairError {
            requiresWarning = error.message.contains("公钥可能已经发布")
        }
        #expect(requiresWarning)
        #expect(try vault.readOperation()?.id == offer.id)
        try await coordinator.discardHandoffOffer(acknowledgePublishedKey: true)
        #expect(try vault.readOperation() == nil)
    }
}
