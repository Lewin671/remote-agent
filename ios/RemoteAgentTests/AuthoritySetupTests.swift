import Foundation
import RACore
import RAProto
import Testing
@testable import RemoteAgent

@MainActor @Suite struct AuthoritySetupTests {
    @Test func unconfirmedAndLostDisplaysDoNotClaimABackup() async throws {
        let store = MemorySecurityStoreV4()
        let host = try StandaloneSecurityHost(store: store, http: AppModel.http, allowPlainHttp: true)
        defer { host.close() }
        let model = AuthoritySetupModel(
            mode: .create, server: "https://relay.example.com",
            makeHost: { host })
        await model.prepare()
        #expect(model.status?.bootstrap == .pendingCreate)
        #expect(model.hasDisplay && !model.canContinue)
        do {
            try await model.confirmBackupIfNeeded(host.client)
            Issue.record("unconfirmed backup accepted")
        } catch {}
        #expect(model.status?.pendingOp.codeSaved == false)
        model.saved = true
        try await model.confirmBackupIfNeeded(host.client)
        #expect(model.status?.pendingOp.codeSaved == true)

        let resumed = AuthoritySetupModel(
            mode: .create, server: "", status: try await host.client.getStatus(),
            makeHost: { host })
        #expect(!resumed.hasDisplay && resumed.canContinue)
    }

    @Test func failedPersistenceCannotAdvanceOrEraseTheDisplayedCode() async throws {
        let store = MemorySecurityStoreV4()
        let host = try StandaloneSecurityHost(store: store, http: AppModel.http, allowPlainHttp: true)
        defer { host.close() }
        let model = AuthoritySetupModel(
            mode: .create, server: "https://relay.example.com",
            makeHost: { host })
        await model.prepare()
        let original = model.code
        let resumed = AuthoritySetupModel(
            mode: .create, server: "", status: try await host.client.getStatus(),
            makeHost: { host })
        #expect(!resumed.canContinue)
        resumed.saved = true // A checkbox cannot confirm a code that is no longer visible.
        #expect(!resumed.canContinue)

        model.saved = true
        // Storage cleared unexpectedly: confirming backup fails and preserves the displayed code.
        _ = store.executeStorage(.with { $0.op = .clearAll })
        await model.run { client in try await model.confirmBackupIfNeeded(client) }
        #expect(!model.error.isEmpty && model.code == original)
        #expect(model.status?.pendingOp.codeSaved == false)
    }

    @Test func rotationAndHandoffRequireBackupAndStorageFailureNeverRunsSuccess() async throws {
        let rotation = RecoveryCodeDraft(); rotation.code = "DEMO-CODE-1"
        let handoff = HandoffDraft(); handoff.code = "DEMO-CODE-2"
        #expect(!rotation.canPublish && !handoff.canPublish)
        rotation.saved = true; handoff.saved = true
        #expect(rotation.canPublish && handoff.canPublish)
        let runner = ActionRunner()
        runner.run { throw SecurityFailure.storage } onDone: { rotation.success = true; handoff.offered = true }
        while runner.running { await Task.yield() }
        #expect(!runner.error.isEmpty && !rotation.success && !handoff.offered)
        #expect(rotation.code == "DEMO-CODE-1" && handoff.code == "DEMO-CODE-2")
    }

    @Test func rePairingReplacesTheEngineWithoutErasingIdentityOrHistory() async throws {
        let h = Harness()
        defer { h.cleanUp() }
        try await h.ready()
        let old = h.engine
        let before = h.model.context
        h.defaults.set("project", forKey: AccountDefaults.key("home-scope"))
        #expect(h.pending.write(opID: 1, bucket: .draft, key: "draft:session:s1", data: Data([1, 2, 3])).ok)
        try await h.model.joined(h.account)
        #expect(old.calls.contains("close"))
        #expect(h.engines.count == 2 && h.engine !== old)
        #expect(h.model.context !== before && h.model.stage == .ready)
        #expect(h.model.account == h.account)
        #expect(h.defaults.string(forKey: AccountDefaults.key("home-scope")) == "project")
        #expect(h.pending.read(opID: 2, bucket: .draft, key: "draft:session:s1").data == Data([1, 2, 3]))
    }
}
