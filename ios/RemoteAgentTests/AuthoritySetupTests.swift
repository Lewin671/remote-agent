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
}
