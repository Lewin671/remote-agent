#if DEBUG
import Foundation
import RACore
import RAProto
import Testing

@testable import RemoteAgent

/// What the home and project screens keep between frames: the scope this device remembers, changes
/// to the project preferences on their way, and a new session's draft with its start.
@MainActor
@Suite struct HomeProjectTests {
    private func defaults() -> UserDefaults {
        let name = "home-tests-\(UUID().uuidString)"
        let d = UserDefaults(suiteName: name)!
        d.removePersistentDomain(forName: name)
        return d
    }

    /// Waits until `done` holds, or two seconds.
    private func until(_ done: @MainActor () -> Bool) async {
        for _ in 0..<200 where !done() { try? await Task.sleep(for: .milliseconds(10)) }
    }

    @Test func theScopeIsRememberedOnThisDeviceAndForgottenWithTheAccount() {
        let d = defaults()
        #expect(HomeScopes(defaults: d).remembered == nil)
        HomeScopes(defaults: d).choose(.of(key: "/Users/me/Code/foo"))
        #expect(HomeScopes(defaults: d).remembered == .of(key: "/Users/me/Code/foo"))
        HomeScopes(defaults: d).choose(.all)
        #expect(HomeScopes(defaults: d).remembered == .all)
        // Logging out clears what is kept under the account's prefix: the scope names one of its paths.
        AccountDefaults.clear(d)
        #expect(HomeScopes(defaults: d).remembered == nil)
    }

    @Test func theScopeAndTheDraftsBelongToTheAccountInUse() {
        let context = AccountContext(account: Demo.account, remote: Demo.remote())
        #expect(context.homeScopes === context.homeScopes)
        #expect(context.prefsChanges === context.prefsChanges)
        let seed = NewConfig(machineId: "mac", cwd: Demo.foo)
        let draft = context.newSessionDrafts.of(Demo.foo, initial: seed, settled: true)
        draft.text = "给登录接口加限流"
        // The seed only makes a draft that does not exist yet.
        let again = context.newSessionDrafts.of(Demo.foo, initial: NewConfig(machineId: "vps", cwd: Demo.foo), settled: true)
        #expect(again === draft && again.config.value == seed && again.text == "给登录接口加限流")
        #expect(context.newSessionDrafts.of("/other", initial: nil, settled: true) !== draft)
        context.startedPrompts.remember("s1", "say hi")
        #expect(context.startedPrompts.prompt("s1") == "say hi" && context.startedPrompts.prompt("s2") == nil)
    }

    @Test func aChangeThatDoesNotArriveLeavesTheListWithARetry() async {
        // A Remote that was never started has no connection: the write is refused.
        let remote = Demo.remote()
        let changes = PrefsChanges()
        var done = false
        changes.run(remote, .pin(path: Demo.foo)) { done = true }
        #expect(changes.states[Demo.foo] == .saving(.pin(path: Demo.foo)))
        // Nothing else is written for the project while one change is on its way.
        changes.run(remote, .hide(path: Demo.foo))
        #expect(changes.states[Demo.foo] == .saving(.pin(path: Demo.foo)))
        await until { changes.states[Demo.foo]?.saving == false }
        #expect(changes.states[Demo.foo] == .failed(.pin(path: Demo.foo), "没有连上服务器，连上后才能修改"))
        #expect(!done)
        #expect(changes.states[Demo.foo]?.ofSettings == false)
        #expect(PrefsChanges.State.saving(.setSettings(.init(path: "/p", machineId: "m"))).ofSettings)
    }

    @Test func aStartThatSurelyDidNotGoOutKeepsTheDraftAndCanBeTriedAgain() async {
        let remote = Demo.remote()
        let cfg = NewConfig(machineId: "mac", cwd: Demo.foo)
        let draft = NewSessionDraft(cfg, settled: true)
        #expect(!draft.drafting)
        draft.text = "  say hi \n"
        #expect(draft.drafting)
        var started: String?
        draft.start(remote, cfg) { sid, _, _ in started = sid }
        #expect(draft.running && draft.progress.running)
        // A second tap while one start is on its way does nothing.
        draft.start(remote, cfg) { sid, _, _ in started = sid }
        await until { !draft.running }
        #expect(started == nil)
        #expect(draft.prompt == "say hi" && draft.text == "  say hi \n")
        // Nothing reached a computer, so the outcome is known: it did not start, and ↑ is a new start.
        #expect(!draft.error.isEmpty && !draft.frozen && !draft.expired)
        draft.abandon()
        #expect(draft.error.isEmpty && !draft.progress.frozen)
    }

    @Test func theDraftsCliIsThePickUnlessItOnlyFellBackToIt() {
        let state = Demo.state(.everyday)
        // foo's latest session is Codex on the MacBook, which has both CLIs.
        let seed = projectStart(state, key: Demo.foo)
        let draft = NewSessionDraft(seed?.config, settled: true)
        #expect(newSessionPick(state, draft) == seed?.config.agent)
        draft.agent = .claude
        if let c = draft.config.value, let to = projectStart(state, key: Demo.foo, pick: .claude, machine: c.machineId) {
            draft.config.retarget(switchAgent(c, project: to.config, agent: .claude))
        }
        #expect(newSessionPick(state, draft) == .claude)
        // On a machine without Claude Code the draft falls back to Codex; the pick is still Claude Code.
        draft.config.choose(NewConfig(machineId: "vps", cwd: Demo.foo, agent: .codex))
        #expect(newSessionPick(state, draft) == .claude)
    }
}
#endif
