import Foundation
import RACore
import RAProto
import Testing

@testable import RemoteAgent

/// What a project's page decides apart from how it draws it: which answer its summary may show, what a
/// start is made of, and the changes to the project's preferences that wait for one another.
@MainActor
@Suite struct ProjectPageTests {
    /// Waits until `done` holds, or two seconds.
    private func until(_ done: @MainActor () -> Bool) async {
        for _ in 0..<200 where !done() { try? await Task.sleep(for: .milliseconds(10)) }
    }

    @Test func aModelListThatArrivesAfterAnotherCliWasPickedIsNotShown() async {
        let models = SummaryModels()
        let codex = SummaryModels.Key(machine: "mac", agent: .codex)
        let claude = SummaryModels.Key(machine: "mac", agent: .claude)
        func list(_ model: String) -> PBListModelsResponse { .with { $0.defaultModel = model } }
        // Codex's list is asked for first and is slow; the page's task for it is cancelled, the request is not.
        var release: CheckedContinuation<Void, Never>?
        let first = Task {
            await models.load(codex) { _ in
                await withCheckedContinuation { release = $0 }
                return list("gpt-5")
            }
        }
        await until { release != nil }
        await models.load(claude) { _ in list("opus") }
        #expect(models.list?.defaultModel == "opus")
        first.cancel()
        release?.resume()
        await first.value
        #expect(models.list?.defaultModel == "opus", "the summary is about Claude Code now")
        // Asking again starts from nothing, and no machine shows nothing.
        await models.load(codex) { _ in list("gpt-5") }
        #expect(models.list?.defaultModel == "gpt-5")
        await models.load(nil) { _ in list("x") }
        #expect(models.list == nil)
    }
}
