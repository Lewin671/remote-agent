import Foundation
import RACore
import Testing
@testable import RemoteAgent

@MainActor @Suite struct AuthoritySetupTests {
    @Test func unconfirmedAndLostDisplaysDoNotClaimABackup() async throws {
        let vault = Vault(service: "io.github.lewin671.remoteagent.setup-test.\(UUID().uuidString)")
        defer { try? vault.clear() }
        let gate = AuthorityGate(); gate.set(active: true, protectedDataAvailable: true)
        let bootstrap = AuthorityBootstrap(storage: vault, gate: gate)
        let model = AuthoritySetupModel(mode: .create, bootstrap: bootstrap, server: "https://relay.example.com")
        await model.prepare()
        #expect(model.status?.kind == .creation)
        #expect(model.hasDisplay && !model.canContinue)
        do { try await model.confirmBackupIfNeeded(); Issue.record("unconfirmed backup accepted") } catch {}
        #expect(try vault.readOperation()?.codeSaved == false)
        model.saved = true
        try await model.confirmBackupIfNeeded()
        #expect(try vault.readOperation()?.codeSaved == true)
        let resumed = AuthoritySetupModel(mode: .create, bootstrap: bootstrap, server: "", status: try await bootstrap.status())
        #expect(!resumed.hasDisplay && resumed.canContinue)
    }
    @Test func failedPersistenceCannotAdvanceOrEraseTheDisplayedCode() async throws {
        let vault = Vault(service: "io.github.lewin671.remoteagent.setup-test.\(UUID().uuidString)")
        defer { try? vault.clear() }
        let gate = AuthorityGate(); gate.set(active: true, protectedDataAvailable: true)
        let bootstrap = AuthorityBootstrap(storage: vault, gate: gate)
        let model = AuthoritySetupModel(mode: .create, bootstrap: bootstrap, server: "https://relay.example.com")
        await model.prepare()
        let original = model.code
        let resumed = AuthoritySetupModel(mode: .create, bootstrap: bootstrap, server: "", status: try await bootstrap.status())
        #expect(!resumed.canContinue)
        resumed.saved = true // A checkbox cannot confirm a code that is no longer visible.
        #expect(!resumed.canContinue)
        model.saved = true
        try vault.clear() // The receipt disappeared: the confirmation must fail.
        await model.run { try await model.confirmBackupIfNeeded() }
        #expect(!model.error.isEmpty && model.code == original)
        #expect(model.status?.codeSaved == false)
    }
    @Test func rotationAndHandoffRequireBackupAndStorageFailureNeverRunsSuccess() async throws {
        let vault = Vault(service: "io.github.lewin671.remoteagent.setup-test.\(UUID().uuidString)")
        defer { try? vault.clear() }
        let seed = Data(repeating: 1, count: 32), x = Data(repeating: 2, count: 32)
        let signer = try SigningKey(seed: seed)
        let keys = try Keys.derive(root: Data(repeating: 3, count: 32))
        let member = try Authority.newMember(signer: signer, xPrivate: x, kind: .client)
        let secret = Authority.newSecret()
        let genesis = try Authority().genesis(account: deviceId(publicKey: keys.account.publicKey),
            domain: Data(repeating: 4, count: 32), member: member,
            recoveryEd: Authority.newRecovery().publicKey, secret: secret, signer: signer, now: nowMs())
        let pin = try DeviceSecurity.pin(for: genesis)
        let record = try DeviceSecurity.Record(pin: pin, states: [1: genesis], memberID: member.id,
            xPrivate: x, member: true, secret: secret)
        try vault.initializeSecurity(record)
        try vault.savePublicProfile(AccountPublicProfile(publicKey: keys.account.publicKey, pin: pin))
        let account = Account(server: "https://relay.example.com", root: Data(), deviceSeed: seed, deviceId: member.id, accountId: pin.account)
        let gate = AuthorityGate(); gate.set(active: true, protectedDataAvailable: true)
        let security = DeviceSecurityClient(store: vault, memberID: member.id, signingKey: signer,
            fetchStates: { _ in throw UserError("unexpected network call") })
        let coordinator = AuthorityCoordinator(account: account, api: try Api(server: account.server, http: HTTP()),
            storage: vault, security: security, gate: gate, token: { throw UserError("unexpected token call") })
        let material = try await coordinator.prepareHandoffCode()
        let rotation = RecoveryCodeDraft(); rotation.prepared = material
        let handoff = HandoffDraft(); handoff.prepared = material
        #expect(!rotation.canPublish && !handoff.canPublish)
        do { try await coordinator.publishRecovery(material, codeSaved: rotation.saved); Issue.record("unconfirmed rotation") } catch {}
        do { try await coordinator.offerHandoff(material, codeSaved: handoff.saved); Issue.record("unconfirmed offer") } catch {}
        #expect(try vault.readOperation() == nil)
        rotation.saved = true; handoff.saved = true
        #expect(rotation.canPublish && handoff.canPublish)
        let runner = ActionRunner()
        runner.run { throw DeviceSecurity.Failure.storage } onDone: { rotation.success = true; handoff.offered = true }
        while runner.running { await Task.yield() }
        #expect(!runner.error.isEmpty && !rotation.success && !handoff.offered)
        #expect(rotation.prepared?.code == material.code && handoff.prepared?.code == material.code)
    }

    @Test func rePairingReplacesTheEngineWithoutErasingIdentityOrHistory() async throws {
        let h = Harness()
        defer { h.cleanUp() }
        try await h.ready()
        let old = h.engine
        let before = h.model.context
        h.defaults.set("project", forKey: AccountDefaults.key("home-scope"))
        try h.pending.put(folder: "session", name: "draft", data: Data([1, 2, 3]))
        try await h.model.joined(h.account)
        #expect(old.calls.contains("close"))
        #expect(h.engines.count == 2 && h.engine !== old)
        #expect(h.model.context !== before && h.model.stage == .ready)
        #expect(try h.vault.load() == h.account.stored)
        #expect(h.defaults.string(forKey: AccountDefaults.key("home-scope")) == "project")
        #expect(h.pending.get(folder: "session", name: "draft") == Data([1, 2, 3]))
    }

}
