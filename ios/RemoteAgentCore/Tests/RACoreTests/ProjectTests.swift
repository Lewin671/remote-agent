import Foundation
import RAProto
import Testing

@testable import RACore

/// Android's app ProjectTest.
@Suite struct ProjectTests {
    @Test func defaultModelResolvesSupportedEffortsWithoutPinningTheModel() {
        let models = PBListModelsResponse.with {
            $0.defaultModel = "fallback"
            $0.models = [.with { $0.slug = "fallback"; $0.efforts = ["low"] }, .with { $0.slug = "configured"; $0.efforts = ["high", "ultra"] }]
        }
        let settings = PBDaemonSettings.with { $0.defaultModel = "configured" }
        var c = NewConfig(machineId: "mac", cwd: "/work/app", effort: "high")
        #expect(projectModel(c, models: models, settings: settings)?.efforts == ["high", "ultra"])
        #expect(projectModel(c, models: models)?.efforts == ["low"])
        #expect(c.agentSettings().model == "")
        #expect(c.agentSettings().effort == "high")
        c.model = "unknown"
        #expect(projectModel(c, models: models, settings: settings) == nil)
    }

    private let path = "/work/app"

    private func set(_ agent: PBAgent, _ model: String = "", _ effort: String = "", _ mode: PBApprovalMode = .unspecified, standard: Bool = false) -> PBAgentSettings {
        .with { $0.agent = agent; $0.model = model; $0.effort = effort; $0.mode = mode; $0.standardContext = standard }
    }

    private func saved(_ machine: String, _ agent: PBAgent = .unspecified, _ sets: PBAgentSettings...) -> PBProjectSettings {
        .with { $0.path = path; $0.machineID = machine; $0.agent = agent; $0.agents = sets }
    }

    private func session(_ id: String, _ machine: String, _ agent: PBAgent, _ model: String, _ at: Int64, _ mode: PBApprovalMode = .unspecified, _ effort: String = "") -> SessionView {
        Mk.session(id, machine: machine, updatedMs: at) { $0.cwd = path; $0.model = model; $0.effort = effort; $0.agent = agent; $0.mode = mode }
    }

    private func host(_ id: String, _ agents: [PBAgent], online: Bool = true) -> MachineView {
        MachineView(id: id, online: online, lastSeenMs: 0, meta: .with { $0.agents = agents.map { a in .with { $0.agent = a } } })
    }

    private let both: [PBAgent] = [.codex, .claude]

    private func state(_ saved: PBProjectSettings? = nil, _ sessions: [SessionView] = [], _ machines: MachineView...) -> RemoteState {
        Mk.state(sessions: sessions, machines: machines, prefs: .with { if let saved { $0.settings = [saved] } })
    }

    private func config(_ machine: String, _ agent: PBAgent = .codex, _ model: String = "", _ effort: String = "", _ mode: Mode? = nil, standard: Bool = false, cwd: String? = nil) -> NewConfig {
        NewConfig(machineId: machine, cwd: cwd ?? path, agent: agent, model: model, effort: effort, mode: mode, standardContext: standard)
    }

    @Test func savedSettingsWinOverLatestSessionAndStayOnOfflineMachine() throws {
        let stored = saved("old", .claude, set(.claude, "sonnet", "high", .fullAccess, standard: true))
        let st = state(stored, [session("s", "new", .codex, "astra", 999)], host("old", both, online: false), host("new", both))
        let c = try #require(projectConfig(st, key: path))
        #expect(c == config("old", .claude, "sonnet", "high", .full, standard: true))
        #expect(c.agentSettings() == stored.agents[0])
    }

    @Test func aProjectWhoseSessionsAreAllOnPagesNotFetchedStartsOnTheMachineItsSummaryNames() {
        // The listing comes a page at a time: no session of the project is held, only the machines' summaries name it.
        func summed(_ id: String, _ created: Int64?) -> MachineView {
            MachineView(
                id: id, online: true, lastSeenMs: 0,
                meta: .with {
                    $0.agents = [.with { $0.agent = .claude }]
                    if let created { $0.projects = [.with { $0.path = path; $0.ended = 4; $0.latestCreatedMs = created }] }
                })
        }
        let st = state(nil, [], summed("idle", nil), summed("earlier", 100), summed("latest", 200))
        // The machine with the project's newest session, not just any machine.
        #expect(projectConfig(st, key: path) == config("latest", .claude))
        // The machines offered for it are those the summaries name, and every machine for a folder nothing names
        // (by ID here: Android gives them in its map's order, which a Swift dictionary does not keep).
        #expect(projectMachines(st, key: path) == ["latest", "earlier"])
        #expect(projectMachines(st, key: "/work/other") == ["earlier", "idle", "latest"])
        #expect(projectConfig(st, key: "/work/other") == nil)
        // A session held here still says more than a summary: the latest one's machine comes first.
        let held = state(nil, [session("s", "earlier", .claude, "sonnet", 999)], summed("earlier", 100), summed("latest", 200))
        #expect(projectConfig(held, key: path)?.machineId == "earlier")
        #expect(projectMachines(held, key: path) == ["earlier", "latest"])
    }

    @Test func theCliIsThePickThenTheRememberedOneThenTheLatestSessionsThenTheMachinesFirst() {
        let mac = host("mac", both)
        let sessions = [session("a", "mac", .claude, "sonnet", 20), session("b", "mac", .codex, "astra", 10)]
        // Nothing stored: the latest session's machine and CLI.
        // (Neither has a creation time, so the listing's order is by ID, as on Android.)
        #expect(projectConfig(state(nil, sessions, mac), key: path)?.agent == .claude)
        // The remembered CLI wins over the latest session's.
        let remembered = state(saved("mac", .codex), sessions, mac)
        #expect(projectConfig(remembered, key: path)?.agent == .codex)
        // The pick next to the message box wins over both.
        #expect(projectConfig(remembered, key: path, pick: .claude)?.agent == .claude)
        // Settings that remember no CLI (stored before a session started) leave it to the latest session.
        #expect(projectConfig(state(saved("mac"), sessions, mac), key: path)?.agent == .claude)
        // No session and nothing stored: the machine given, with its first CLI.
        #expect(projectConfig(state(nil, [], mac), key: path) == nil)
        #expect(projectConfig(state(nil, [], host("mac", [.claude, .codex])), key: path, machine: "mac") == config("mac", .claude))
        // The stored machine wins over the latest session's.
        #expect(projectConfig(state(saved("vps", .codex), sessions, mac, host("vps", both)), key: path)?.machineId == "vps")
    }

    @Test func aCliTheMachineLacksFallsBackToItsFirstAndIsNamed() throws {
        let vps = host("vps", [.codex])
        let stored = saved("vps", .claude, set(.claude, "sonnet"), set(.codex, "astra", "", .ask))
        let st = state(stored, [], vps)
        let c = try #require(projectConfig(st, key: path))
        // The CLI the machine has, with its own set; what is remembered is not touched.
        #expect(c == config("vps", .codex, "astra", "", .ask))
        #expect(missingAgent(st, c) == .claude)
        #expect(wantedAgent(st, key: path) == .claude)
        // With the CLI installed there is nothing to say.
        let ok = state(stored, [], host("vps", both))
        #expect(missingAgent(ok, try #require(projectConfig(ok, key: path))) == nil)
        // A draft kept on the other CLI while the machine has both is not a fallback.
        #expect(missingAgent(ok, config("vps", .codex)) == nil)
        // Nothing remembered and no session: the machine's first CLI is not a fallback.
        let fresh = state(nil, [], vps)
        #expect(missingAgent(fresh, config("vps", .codex)) == nil)
    }

    @Test func aCliWithoutAStoredSetStartsFromItsOwnLatestSession() {
        let mac = host("mac", both)
        let sessions = [
            session("new-codex", "mac", .codex, "astra", 30, .plan, "low"),
            session("claude", "mac", .claude, "sonnet", 20, .auto, "high"),
            session("old-claude", "mac", .claude, "opus", 10, .ask),
        ]
        let stored = saved("mac", .codex, set(.codex, "sol"))
        let st = state(stored, sessions, mac)
        // Codex has an entry: it is used as stored, empty fields meaning the machine's defaults.
        #expect(projectConfig(st, key: path) == config("mac", .codex, "sol"))
        // Claude Code has none: its own latest session, not the project's latest.
        #expect(projectConfig(st, key: path, pick: .claude) == config("mac", .claude, "sonnet", "high", .auto))
        // Plan is never carried over.
        #expect(projectConfig(state(nil, sessions, mac), key: path, pick: .codex) == config("mac", .codex, "astra", "low"))
        // An empty entry is the machine's defaults, not the latest session.
        let empty = state(saved("mac", .claude, set(.claude)), sessions, mac)
        #expect(projectConfig(empty, key: path) == config("mac", .claude))
        // No entry and no session on that CLI: the machine's defaults.
        #expect(agentSet(state(stored, Array(sessions.prefix(1)), mac), key: path, agent: .claude) == set(.claude))
    }

    // --- what a start inherits while the listing is only partly fetched (architecture §8 「会话列表分页」) ---

    /// An ended session as a page of the listing brings it: `at` is when it ended, which is its place there.
    private func ended(_ id: String, _ agent: PBAgent, _ model: String, _ at: Int64, machine: String = "mac", effort: String = "", mode: PBApprovalMode = .unspecified) -> SessionView {
        Mk.session(id, machine: machine, updatedMs: at, listedEndedMs: at) {
            $0.cwd = path
            $0.model = model
            $0.effort = effort
            $0.agent = agent
            $0.mode = mode
            $0.status = .exited
            $0.endedAt = .with { $0.seconds = at / 1000 }
        }
    }

    /// A machine whose summary says the project has `count` ended sessions.
    private func summed(_ id: String, _ count: UInt32, _ agents: [PBAgent]) -> MachineView {
        MachineView(
            id: id, online: true, lastSeenMs: 0,
            meta: .with {
                $0.agents = agents.map { a in .with { $0.agent = a } }
                $0.projects = [.with { $0.path = path; $0.ended = count }]
            })
    }

    /// Pages fetched down to `cursor`; nil for every page.
    private func paged(_ st: RemoteState, _ cursor: Int64?) -> RemoteState {
        var out = st
        out.sessionsNext = cursor.map { SessionPlace(endedMs: $0, sessionId: "") }
        return out
    }

    @Test func whatACliStartsWithIsNotSettledWhileItsLatestSessionMayBeOnAPageNotFetched() throws {
        let claude = ended("c1", .claude, "sonnet", 90_000, effort: "high", mode: .auto)
        let st = paged(state(nil, [claude], summed("mac", 9, both)), 50_000)
        // The project's first session in the listing is held: the machine, the CLI and its set are settled.
        #expect(projectStart(st, key: path) == ProjectStart(config: config("mac", .claude, "sonnet", "high", .auto), settled: true))
        // Codex has no session among the pages fetched, and eight ended sessions are not fetched: one of them may be Codex's.
        let codex = try #require(projectStart(st, key: path, pick: .codex))
        #expect(!codex.settled)
        #expect(codex.config == config("mac", .codex))
        #expect(!agentSetSettled(st, key: path, agent: .codex))
        #expect(agentSetSettled(st, key: path, agent: .claude))
        // A stored set settles it, as does having every page, or every ended session of the project.
        var stored = st
        stored.prefs = PrefsObject(revision: 1, prefs: .with { $0.settings = [saved("mac", .codex, set(.codex, "sol"))] })
        #expect(projectStart(stored, key: path)?.settled == true)
        #expect(projectStart(paged(st, nil), key: path, pick: .codex)?.settled == true)
        #expect(projectStart(paged(state(nil, [claude], summed("mac", 1, both)), 50_000), key: path, pick: .codex)?.settled == true)
        // The page with it came: settled, with its settings.
        let old = ended("x1", .codex, "astra", 40_000, effort: "low", mode: .ask)
        let fetched = paged(state(nil, [claude, old], summed("mac", 9, both)), 30_000)
        #expect(projectStart(fetched, key: path, pick: .codex) == ProjectStart(config: config("mac", .codex, "astra", "low", .ask), settled: true))
    }

    @Test func aSessionHeldPastThePagesFetchedDoesNotSettleIt() throws {
        // Fetched by its ID, or kept from before a reconnect: sessions not fetched yet come before it, and one of them may be the latest.
        let old = ended("x0", .codex, "old-model", 10_000)
        let st = paged(state(nil, [old], summed("mac", 9, both)), 50_000)
        let start = try #require(projectStart(st, key: path))
        #expect(!start.settled)
        // The guess is still the best one held.
        #expect(start.config.model == "old-model")
        // The pages reached it: it is the first.
        #expect(projectStart(paged(st, 9_000), key: path) == ProjectStart(config: config("mac", .codex, "old-model"), settled: true))
        // A session that has not ended is on the first page, always held: it settles it whatever else is not fetched.
        let live = session("l", "mac", .codex, "astra", 5)
        #expect(
            projectStart(paged(state(nil, [old, live], summed("mac", 9, both)), 50_000), key: path)
                == ProjectStart(config: config("mac", .codex, "astra"), settled: true))
    }

    @Test func itIsSettledForTheCliTheSessionReallyStartsOn() throws {
        // Claude Code is remembered, with its set, and the machine only has Codex: the session starts on Codex,
        // whose latest session is on a page not fetched.
        let stored = saved("vps", .claude, set(.claude, "sonnet"))
        let held = [ended("c1", .claude, "sonnet", 90_000, machine: "vps"), ended("c2", .claude, "sonnet", 80_000, machine: "vps")]
        let st = paged(state(stored, held, summed("vps", 9, [.codex])), 50_000)
        let start = try #require(projectStart(st, key: path))
        #expect(start.config.agent == .codex)
        #expect(!start.settled)
        // With Claude Code installed its stored set is used: nothing to wait for.
        #expect(
            projectStart(paged(state(stored, held, summed("vps", 9, both)), 50_000), key: path)
                == ProjectStart(config: config("vps", .claude, "sonnet"), settled: true))
    }

    @Test func beforeTheFirstPageCameNothingInheritedIsSettledUnlessTheCachedPageHasIt() {
        // At start only the cached page of an earlier run is held, and how many pages there are is not known.
        let claude = ended("c1", .claude, "sonnet", 90_000)
        var st = state(nil, [claude], summed("mac", 9, both))
        st.sessionsListed = false
        #expect(projectStart(st, key: path)?.settled == true)
        #expect(projectStart(st, key: path, pick: .codex)?.settled == false)
        // A project only the summaries name: its machine is a guess too.
        var none = state(nil, [], summed("mac", 9, both))
        none.sessionsListed = false
        #expect(projectStart(none, key: path) == ProjectStart(config: config("mac", .codex), settled: false))
        // A folder nothing names has nothing to inherit.
        #expect(projectStart(none, key: "/work/new", machine: "mac") == ProjectStart(config: config("mac", .codex, cwd: "/work/new"), settled: true))
    }

    @Test func aDraftWrittenOverAGuessTakesTheSettingsOnceTheyAreKnown() {
        let guess = config("mac", .codex)
        let inherited = config("mac", .codex, "astra", "low", .ask)
        // Seeded with a guess, then written: the settings fetched replace the guess, and stay.
        var seeded = DraftConfig(guess, settled: false)
        seeded.follow(guess, drafting: true, settled: false)
        #expect(seeded.value == guess)
        seeded.follow(inherited, drafting: true, settled: true)
        #expect(seeded.value == inherited)
        seeded.follow(config("m2", .claude), drafting: true)
        #expect(seeded.value == inherited)
        // Written first, then switched to a CLI whose settings are not known yet: the same.
        var d = DraftConfig(config("mac", .claude, "sonnet"))
        d.follow(config("mac", .claude, "sonnet"), drafting: true)
        d.retarget(guess, settled: false)
        #expect(d.value == guess)
        d.follow(guess, drafting: true, settled: false)
        d.follow(inherited, drafting: true, settled: true)
        #expect(d.value == inherited)
        // A switch to a CLI whose settings are known is not a guess: the draft keeps it.
        d.retarget(inherited)
        d.follow(guess, drafting: true)
        #expect(d.value == inherited)
        // What the user chose on this page is never replaced, guess or not.
        let mine = config("mac", .codex, "sol", "", .full)
        var chosen = DraftConfig(guess, settled: false)
        chosen.choose(mine)
        chosen.follow(inherited, drafting: true, settled: true)
        #expect(chosen.value == mine)
        chosen.follow(inherited, drafting: false, settled: true)
        #expect(chosen.value == mine)
    }

    @Test func theFirstStartEstablishesTheSettingsAndLaterOnesOnlyFillIn() throws {
        let used = config("mac", .claude, "sonnet", "high", .auto, standard: true)
        // Nothing stored: the machine, the CLI and its set.
        let first = try #require(startSettings(saved: nil, project: used, used: used))
        #expect(first == PrefsOp.SetSettings(path: path, machineId: "mac", agent: .claude, sets: [used.agentSettings()], keepExisting: true))
        let stored = applySettings(stored: nil, op: first, now: 1)
        var want = saved("mac", .claude, set(.claude, "sonnet", "high", .auto, standard: true))
        want.updatedMs = 1
        #expect(stored == want)
        // The same CLI again changes nothing.
        var opus = used
        opus.model = "opus"
        #expect(startSettings(saved: stored, project: used, used: opus) == nil)
        // The other CLI: remembered, and its set added; Claude Code's stays.
        let codex = config("mac", .codex, "astra")
        let next = applySettings(stored: stored, op: try #require(startSettings(saved: stored, project: codex, used: codex)), now: 2)
        #expect(next.agent == .codex)
        #expect(next.agents == [codex.agentSettings(), used.agentSettings()])
        // Back to Claude Code: only the remembered CLI changes.
        let back = applySettings(stored: next, op: try #require(startSettings(saved: next, project: used, used: opus)), now: 3)
        #expect(back.agent == .claude)
        #expect(back.agents == next.agents)
    }

    @Test func aMachinePickedJustForOneStartIsNotStored() throws {
        let project = config("mac", .codex, "astra", "high", .ask)
        let once = switchMachine(project, machineId: "vps", agents: [.codex])
        // Stored settings keep their machine, whatever the op carries.
        let stored = saved("mac", .claude, set(.claude, "sonnet"))
        let next = applySettings(stored: stored, op: try #require(startSettings(saved: stored, project: project, used: once)), now: 2)
        #expect(next.machineID == "mac")
        // The set added is the project's, not the other machine's defaults.
        #expect(next.agents == [project.agentSettings(), set(.claude, "sonnet")])
        // The first start: the project's machine is the one established.
        #expect(applySettings(stored: nil, op: try #require(startSettings(saved: nil, project: project, used: once)), now: 1).machineID == "mac")
        // The other machine only had another CLI: remembered, with no set from a machine it does not belong to.
        let other = config("vps", .claude)
        #expect(startSettings(saved: nil, project: project, used: other) == PrefsOp.SetSettings(path: path, machineId: "mac", agent: .claude, keepExisting: true))
    }

    @Test func anotherMachineInProjectSettingsStartsEveryCliOver() {
        let stored = saved("mac", .claude, set(.codex, "astra"), set(.claude, "sonnet", "", .auto))
        let moved = applySettings(stored: stored, op: PrefsOp.SetSettings(path: path, machineId: "vps", moved: true), now: 5)
        let st = state(moved, [session("s", "mac", .claude, "opus", 9)], host("vps", both))
        // The empty entries stand for that machine's defaults: no model from the old machine or its sessions.
        #expect(projectConfig(st, key: path) == config("vps", .claude))
        #expect(projectConfig(st, key: path, pick: .codex) == config("vps", .codex))
    }

    @Test func summaryUsesTheMachineModelDisplayName() {
        let models = PBListModelsResponse.with { $0.models = [.with { $0.slug = "gpt-6-astra"; $0.displayName = "GPT-6-Astra"; $0.defaultEffort = "medium" }] }
        let text = projectSummary(RemoteState(), NewConfig(machineId: "mac", cwd: "/work/app", model: "gpt-6-astra", mode: .ask), models: models)
        #expect(text.hasSuffix("询问 · GPT-6-Astra"), "\(text)")
    }

    @Test func summaryNamesTheMachineOnlyAmongSeveralAndCutsALongName() {
        func mv(_ id: String, _ host: String) -> MachineView { MachineView(id: id, online: true, lastSeenMs: 0, meta: .with { $0.hostname = host }) }
        let one = Mk.state(machines: [mv("a", "MacBook")])
        #expect(summaryMachine(one, id: "a") == "")
        let two = Mk.state(machines: [mv("a", "workstationM1-Pro-office-2024"), mv("b", "VPS")])
        #expect(summaryMachine(two, id: "a") == "workstationM1-P…")
        #expect(summaryMachine(two, id: "b") == "VPS")
    }

    @Test func aNewSessionCannotBeStartedInPlan() {
        #expect(newSessionModes == [.ask, .auto, .full])
    }

    private func machine(_ infos: PBAgentInfo...) -> MachineView {
        MachineView(id: "m", online: true, lastSeenMs: 0, meta: .with { $0.agents = infos })
    }

    private func info(_ agent: PBAgent, _ mode: PBApprovalMode, _ source: PBDefaultModeSource, varies: Bool = false) -> PBAgentInfo {
        .with { $0.agent = agent; $0.defaultMode = mode; $0.defaultModeSource = source; $0.defaultModeVaries = varies }
    }

    @Test func theCliDefaultTheMachineReportsIsShownWithoutAConfirmation() {
        // As measured on this machine: Claude Code defaults to bypass, Codex to auto.
        let m = machine(info(.claude, .fullAccess, .cliConfig, varies: true), info(.codex, .auto, .cliConfig))
        #expect(launchMode(chosen: nil, default: defaultModeOf(m, agent: .claude)) == .send(.unspecified))
        #expect(launchMode(chosen: nil, default: defaultModeOf(m, agent: .codex)) == .send(.unspecified))
        #expect(modeLabel(chosen: nil, default: defaultModeOf(m, agent: .claude), agent: .claude) == "默认：完全放行（项目设置可能不同）")
        #expect(modeLabel(chosen: nil, default: defaultModeOf(m, agent: .codex), agent: .codex) == "默认：自动")
        #expect(effectiveModeText(chosen: nil, default: defaultModeOf(m, agent: .codex)) == "自动")
        // A chosen mode needs no default.
        #expect(launchMode(chosen: .ask, default: defaultModeOf(m, agent: .claude)) == .send(.ask))
    }

    @Test func anUnknownDefaultDoesNotBlockAStart() {
        // The daemon could not read it: it could be full access.
        let old = machine(.with { $0.agent = .codex })
        #expect(launchMode(chosen: nil, default: defaultModeOf(old, agent: .codex)) == .send(.unspecified))
        // A mode outside the four (accept edits) is known and not full access.
        let edits = machine(info(.claude, .unspecified, .cliConfig))
        #expect(launchMode(chosen: nil, default: defaultModeOf(edits, agent: .claude)) == .send(.unspecified))
        #expect(modeLabel(chosen: nil, default: defaultModeOf(edits, agent: .claude), agent: .claude) == "默认：Claude Code 自己的模式")
    }

    @Test func onlyAStartedSessionTakesTheDraft() {
        // A refusal that asks for confirmation answers nothing: the draft stays.
        #expect(startedSession(nil) == nil)
        #expect(startedSession(PBCommandResult()) == nil)
        let ok = PBCommandResult.with { $0.createSession = .with { $0.session = .with { $0.id = "s1" } } }
        #expect(startedSession(ok) == "s1")
    }

    @Test func aDraftKeepsItsConfigurationWhenTheProjectChanges() {
        let first = NewConfig(machineId: "m1", cwd: "/work/app", agent: .codex, model: "gpt-a")
        let moved = NewConfig(machineId: "m2", cwd: "/work/app", agent: .claude)
        var d = DraftConfig(first)
        // Nothing written yet: it follows the project.
        d.follow(moved, drafting: false)
        #expect(d.value == moved)
        d.follow(first, drafting: false)
        // Once a draft is begun, an update does not retarget it.
        d.follow(moved, drafting: true)
        #expect(d.value == first)
        // Sent and cleared, it follows again.
        d.follow(moved, drafting: false)
        #expect(d.value == moved)
        // A choice stays even with nothing written.
        let mine = NewConfig(machineId: "m1", cwd: "/work/app", agent: .codex, mode: .ask)
        d.choose(mine)
        d.follow(first, drafting: false)
        #expect(d.value == mine)
    }

    // --- not in Android's tests ---

    @Test func aLongMachineNameIsCutByUtf16UnitsWithoutHalvingAnEmoji() {
        func mv(_ id: String, _ host: String) -> MachineView { MachineView(id: id, online: true, lastSeenMs: 0, meta: .with { $0.hostname = host }) }
        // Exactly 16 units stays; 17 is cut to 15 and the ellipsis.
        let st = Mk.state(machines: [mv("a", "0123456789abcdef"), mv("b", "0123456789abcdefg"), mv("c", "0123456789abcd😀x")])
        #expect(summaryMachine(st, id: "a") == "0123456789abcdef")
        #expect(summaryMachine(st, id: "b") == "0123456789abcde…")
        // The cut falls in the middle of the emoji, which Kotlin leaves as half a pair; here the half goes.
        #expect(summaryMachine(st, id: "c") == "0123456789abcd…")
    }
}

/// Android's SwitchTest (in ProjectTest.kt).
@Suite struct SwitchTests {
    private let c = NewConfig(machineId: "mac", cwd: "/p", agent: .codex, model: "gpt-6-astra", effort: "medium", mode: .auto)

    @Test func anotherCliTakesTheProjectsSetForIt() {
        let claude = NewConfig(machineId: "mac", cwd: "/p", agent: .claude, model: "sonnet", effort: "high", mode: .ask, standardContext: true)
        #expect(switchAgent(c, project: claude, agent: .claude) == claude)
        // On a machine picked just for this start the model is that machine's default.
        var once = c
        once.machineId = "vps"
        var want = claude
        want.machineId = "vps"
        want.model = ""
        want.effort = ""
        #expect(switchAgent(once, project: claude, agent: .claude) == want)
        // A project that knows nothing of that CLI: the machine's defaults.
        #expect(switchAgent(c, project: nil, agent: .claude) == NewConfig(machineId: "mac", cwd: "/p", agent: .claude))
    }

    @Test func anotherMachineKeepsTheCliOnlyWhenItHasIt() {
        var claude = c
        claude.agent = .claude
        let s = switchMachine(claude, machineId: "vps", agents: [.codex])
        #expect(s.agent == .codex)
        #expect(s.model == "")
        let kept = switchMachine(claude, machineId: "imac", agents: [.codex, .claude])
        #expect(kept.agent == .claude)
    }

    @Test func anotherMachineStartsInItsOwnDefaultMode() {
        // Full access chosen for Claude Code is not what the other machine's Codex starts in.
        var full = c
        full.agent = .claude
        full.mode = .full
        #expect(switchMachine(full, machineId: "vps", agents: [.codex]) == NewConfig(machineId: "vps", cwd: full.cwd, agent: .codex))
        #expect(switchMachine(full, machineId: "imac", agents: [.codex, .claude]).mode == nil)
    }

    @Test func theConfigurationIsTwoLines() {
        let (a, b) = configLines(RemoteState(), c, default: DefaultMode(mode: nil, known: false, varies: false))
        #expect(a.hasSuffix("自动"))
        #expect(b == "gpt-6-astra · medium")
    }
}
