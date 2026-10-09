import Foundation
import RAProto
import SwiftProtobuf
import Testing

@testable import RACore

/// What the project screens decide before drawing: the list of projects, a new one's folder, the new-session box and 项目设置.
@Suite struct ProjectRowsTests {
    private let now: Int64 = 1_791_000_000_000
    private func ts(_ ms: Int64) -> Google_Protobuf_Timestamp { .with { $0.seconds = ms / 1000 } }

    private func session(
        _ id: String, _ cwd: String, machine: String = "mac", agent: PBAgent = .codex, ended: Int64? = nil, created: Int64? = nil
    ) -> SessionView {
        Mk.session(id, machine: machine, updatedMs: ended ?? now, listedEndedMs: ended ?? 0) {
            $0.cwd = cwd
            $0.title = id
            $0.agent = agent
            $0.createdAt = ts(created ?? now - 3_600_000)
            $0.status = ended != nil ? .exited : .idle
            if let ended { $0.endedAt = ts(ended) }
        }
    }

    private func machine(_ id: String, _ name: String, online: Bool = true, agents: [PBAgent] = [.codex], version: UInt32 = protocolVersion) -> MachineView {
        MachineView(id: id, online: online, lastSeenMs: 0, meta: .with { meta in
            meta.hostname = name
            meta.protocolVersion = version
            meta.agents = agents.map { a in
                .with {
                    $0.agent = a
                    $0.defaultMode = .ask
                    $0.defaultModeSource = .cliConfig
                }
            }
        })
    }

    private func state(
        _ sessions: [SessionView] = [], machines: [MachineView]? = nil, pinned: [String] = [], hidden: [String] = [],
        settings: [PBProjectSettings] = []
    ) -> RemoteState {
        Mk.state(
            sessions: sessions, machines: machines ?? [machine("mac", "MacBook", agents: [.codex, .claude]), machine("vps", "VPS-dev")],
            prefs: .with {
                $0.pinned = pinned.map { p in .with { $0.path = p } }
                $0.hidden = hidden.map { p in .with { $0.path = p } }
                $0.settings = settings
            }
        ) {
            $0.loaded = true
            $0.conn = .online
            $0.link = .online
            $0.synced = true
        }
    }

    // MARK: The list

    @Test func theListTitlesItsGroupsOnlyWhenItHasBoth() {
        let st = state([session("a", "/p/foo"), session("b", "/p/bar")], pinned: ["/p/foo"])
        let l = projectListing(st)
        #expect(l.pinned.map(\.key) == ["/p/foo"] && l.others.map(\.key) == ["/p/bar"] && l.titled)
        #expect(!l.searchable && !l.searching && l.noMatch == nil)
        #expect(!projectListing(state([session("a", "/p/foo")])).titled)
    }

    @Test func aSearchKeepsWhatHasItInNameOrPathAndSaysWhenNothingDoes() {
        let st = state((0..<9).map { session("s\($0)", "/Users/me/Code/proj\($0)") }, pinned: ["/Users/me/Code/proj1"])
        #expect(projectListing(st).searchable)
        let hit = projectListing(st, search: " PROJ1 ")
        #expect(hit.pinned.map(\.name) == ["proj1"] && hit.others.isEmpty && hit.searching && !hit.titled)
        #expect(projectListing(st, search: "code").others.count == 8)
        #expect(projectListing(st, search: "zzz").noMatch == "没有名字或路径里有「zzz」的项目")
    }

    @Test func choosingAFolderAddsItUnhidesItOrChangesNothing() {
        let st = state([session("a", "/p/foo"), session("h", "/p/old")], hidden: ["/p/old"])
        #expect(addProjectOp(st, machine: "vps", path: "/p/new") == .setSettings(PrefsOp.SetSettings(path: "/p/new", machineId: "vps")))
        #expect(addProjectOp(st, machine: "vps", path: "/p/old") == .unhide(path: "/p/old"))
        // Already a project: its settings stay as they are.
        #expect(addProjectOp(st, machine: "vps", path: "/p/foo") == nil)
        let added = state(settings: [.with { $0.path = "/p/new"; $0.machineID = "mac" }])
        #expect(addProjectOp(added, machine: "vps", path: "/p/new") == nil)
    }

    @Test func aFolderIsWrittenPieceByPiece() {
        #expect(pathCrumbs("/Users/me/Code/ai-bot") == ["~", "Code", "ai-bot"])
        #expect(pathCrumbs("/srv/www") == ["srv", "www"])
        #expect(childPath("/Users/me/", "Code") == "/Users/me/Code")
        #expect(childPath("/", "srv") == "/srv")
        #expect(listDirectoriesCommand(path: "/srv").listDirectories.path == "/srv")
    }

    @Test func machinesAreListedByNameAndTheFirstOnlineOneIsBrowsed() {
        let st = state(machines: [machine("z", "Alpha", online: false), machine("a", "Zeta"), machine("m", "Beta")])
        #expect(listedMachines(st).map(\.id) == ["z", "m", "a"])
        #expect(firstOnlineMachine(st) == "m")
        #expect(firstOnlineMachine(state(machines: [machine("z", "Alpha", online: false)])).isEmpty)
    }

    // MARK: The page

    @Test func aPageListsItsOwnSessionsAndCountsTheEndedByTheSummaries() {
        var st = state([
            session("run", "/Users/me/Code/foo", created: now - 1000), session("old", "/Users/me/Code/foo", machine: "vps", ended: now - 5000),
            session("other", "/p/bar"),
        ])
        st.machines["mac"]?.meta?.projects = [.with { $0.path = "/Users/me/Code/foo"; $0.ended = 12 }]
        let page = projectPage(st, key: "/Users/me/Code/foo")
        #expect(page.active.map(\.id) == ["run"] && page.ended.map(\.id) == ["old"])
        #expect(page.endedTotal == 13)
        #expect(page.subtitle == "~/Code/foo · MacBook · VPS-dev")
        #expect(page.meta == RowMeta(project: false, agent: false, machine: true))
        #expect(page.recent(all: false).count == 1 && page.wanted(all: false) == 3 && page.wanted(all: true) == 1)
    }

    @Test func onlyFullAccessIsRedInTheSummary() {
        let st = state()
        let ask = NewConfig(machineId: "mac", cwd: "/p/foo", mode: .ask)
        let full = NewConfig(machineId: "mac", cwd: "/p/foo", mode: .full)
        #expect(!isFullAccess(st, ask) && isFullAccess(st, full))
        let summary = projectSummary(st, full)
        #expect(summary == "Codex · 完全放行 · MacBook · 默认模型")
        let parts = summaryParts(summary, full: true)
        #expect(parts.before == "Codex · " && parts.full == "完全放行" && parts.after == " · MacBook · 默认模型")
        #expect(summaryParts(summary, full: false).full.isEmpty)
        #expect(foldedSummary(st, full) == "Codex · 完全放行 · MacBook")
        // Left to a CLI whose own default is full access.
        var own = st
        own.machines["mac"]?.meta?.agents[0].defaultMode = .fullAccess
        #expect(isFullAccess(own, NewConfig(machineId: "mac", cwd: "/p/foo")))
    }

    // MARK: The new-session box

    private func notices(
        _ st: RemoteState, _ c: NewConfig?, drafting: Bool = false, settled: Bool = true, pick: PBAgent? = nil, start: StartProgress = StartProgress()
    ) -> [NewSessionNotice] {
        newSessionNotices(st, config: c, name: "foo", pick: pick, drafting: drafting, settled: settled, start: start)
    }

    @Test func nothingIsSaidWhenASessionCanStart() {
        let st = state([session("a", "/p/foo")])
        let c = NewConfig(machineId: "mac", cwd: "/p/foo")
        #expect(notices(st, c).isEmpty)
        #expect(canStartSession(st, config: c, drafting: true, settled: true, running: false))
        #expect(!canStartSession(st, config: c, drafting: false, settled: true, running: false))
        #expect(!canStartSession(st, config: c, drafting: true, settled: false, running: false))
        #expect(!canStartSession(st, config: c, drafting: true, settled: true, running: true))
        #expect(!canStartSession(st, config: nil, drafting: true, settled: true, running: false))
    }

    @Test func anOfflineMachineIsSaidAndUnderADraftAnotherIsOffered() {
        var st = state([session("a", "/p/foo"), session("b", "/p/foo", machine: "vps")])
        st.machines["mac"]?.online = false
        let c = NewConfig(machineId: "mac", cwd: "/p/foo", agent: .claude, model: "sonnet", effort: "high")
        #expect(notices(st, c) == [.offline("MacBook 不在线，现在不能新建")])
        #expect(!canStartSession(st, config: c, drafting: true, settled: true, running: false))
        guard case .offlineChoice(let title, _, let choices, let none)? = notices(st, c, drafting: true).first else {
            Issue.record("no choice offered")
            return
        }
        #expect(title == "MacBook 已离线，任务尚未开始" && none == nil)
        #expect(choices.map(\.title) == ["改用 VPS-dev"])
        // That machine has only Codex: said, and the draft's model does not carry over.
        #expect(choices[0].note == "只有 Codex；会改用 Codex，模型和模式按这台机器重新选择")
        #expect(choices[0].config == NewConfig(machineId: "vps", cwd: "/p/foo", agent: .codex))
        st.machines["vps"]?.online = false
        guard case .offlineChoice(_, _, let empty, let said)? = notices(st, c, drafting: true).first else { return }
        #expect(empty.isEmpty && said == "foo 用过的其他电脑也都不在线。")
    }

    @Test func outOfReachOfTheServerNothingIsSaidOfTheMachine() {
        var st = state([session("a", "/p/foo")])
        st.machines["mac"]?.online = false
        st.link = .noNetwork
        st.conn = .offline
        let c = NewConfig(machineId: "mac", cwd: "/p/foo")
        #expect(notices(st, c, drafting: true).isEmpty)
        #expect(!canStartSession(st, config: c, drafting: true, settled: true, running: false))
    }

    @Test func aMachineOfAnotherProtocolVersionIsRefusedBeforeSending() {
        let st = state([session("a", "/p/foo")], machines: [machine("mac", "MacBook", version: protocolVersion - 1)])
        let c = NewConfig(machineId: "mac", cwd: "/p/foo")
        #expect(notices(st, c) == [.mismatch("MacBook 上的 remote-agent 版本较旧，请在电脑上更新后再试")])
        #expect(!canStartSession(st, config: c, drafting: true, settled: true, running: false))
    }

    @Test func aCliTheMachineLacksIsNeverReplacedSilently() {
        let st = state([session("a", "/p/foo", machine: "vps")], settings: [.with { $0.path = "/p/foo"; $0.machineID = "vps"; $0.agent = .claude }])
        let c = projectConfig(st, key: "/p/foo")!
        #expect(c.agent == .codex)
        #expect(notices(st, c) == [.missingAgent("VPS-dev 没有 Claude Code，这次用 Codex")])
    }

    @Test func aStartSaysHowItStands() {
        let st = state([session("a", "/p/foo")])
        let c = NewConfig(machineId: "mac", cwd: "/p/foo")
        #expect(notices(st, c, start: StartProgress(running: true)) == [.starting("正在 MacBook 上启动…")])
        #expect(notices(st, c, start: StartProgress(error: "目录不存在")) == [.failed("启动失败：目录不存在")])
        let unknown = notices(st, c, start: StartProgress(error: "结果未知", frozen: true, prompt: "say hi"))
        #expect(unknown == [.unknown("上次的启动结果不确定。再点 ↑ 会重发同一个请求，电脑收到过就不会再启动一次")])
        let expired = notices(st, c, start: StartProgress(frozen: true, expired: true, prompt: "say hi"))
        #expect(expired == [.unknown("不确定「say hi」有没有启动，它已过期，不会再重发。请先看看进行中的会话")])
        #expect(notices(st, c, settled: false) == [.reading("正在读取这个项目上次的设置…", retry: false)])
        var failed = st
        failed.sessionsError = "超时"
        #expect(notices(failed, c, settled: false) == [.reading("没能加载更早的会话：超时 · 点这里重试", retry: true)])
    }

    @Test func theStartCommandCarriesTheConfiguration() {
        let c = NewConfig(machineId: "mac", cwd: "/p/foo", agent: .claude, model: "sonnet", effort: "high", mode: .auto, standardContext: true)
        let r = createSessionCommand(c, prompt: promptText("  say hi\n"), mode: .auto).createSession
        #expect(r.cwd == "/p/foo" && r.prompt == "say hi" && r.agent == .claude && r.model == "sonnet" && r.effort == "high")
        #expect(r.mode == .auto && r.standardContext && r.imageIds.isEmpty)
        // The context window is Claude Code's alone.
        var codex = c
        codex.agent = .codex
        #expect(!createSessionCommand(codex, prompt: "x", mode: .unspecified).createSession.standardContext)
    }

    // MARK: 项目设置

    @Test func nothingIsStoredUntilSomethingChanged() {
        let st = state([session("a", "/p/foo")])
        var d = SettingsDraft(cwd: "/p/foo", machine: "mac", agent: .codex)
        #expect(d.op() == nil && !d.moved && d.agents(st) == [.codex, .claude])
        var c = d.config(st)
        c.mode = .full
        d.edit(c)
        #expect(d.op() == PrefsOp.SetSettings(path: "/p/foo", machineId: "mac", sets: [c.agentSettings()]))
        // The other CLI's set stays as stored, and is shown under its tab.
        d.tab = .claude
        #expect(d.config(st) == NewConfig(machineId: "mac", cwd: "/p/foo", agent: .claude))
    }

    @Test func anotherMachineStartsEveryCliOver() {
        let st = state([session("a", "/p/foo")])
        var d = SettingsDraft(cwd: "/p/foo", machine: "mac", agent: .claude)
        var c = d.config(st)
        c.model = "sonnet"
        d.edit(c)
        d.move(to: "vps")
        #expect(d.moved && d.edited.isEmpty)
        // That machine has no Claude Code: its first CLI is shown.
        #expect(d.current(st) == .codex && d.config(st) == NewConfig(machineId: "vps", cwd: "/p/foo"))
        #expect(d.op() == PrefsOp.SetSettings(path: "/p/foo", machineId: "vps", moved: true))
        d.move(to: "mac")
        #expect(d.op() == nil)
    }

    @Test func theRowsSayWhatIsInForce() {
        let sonnet = PBModelInfo.with {
            $0.slug = "sonnet"
            $0.displayName = "Sonnet"
            $0.efforts = ["low", "high"]
            $0.defaultEffort = "low"
            $0.contextWindow = 1_000_000
        }
        let haiku = PBModelInfo.with {
            $0.slug = "claude-haiku"
            $0.contextWindow = 200_000
        }
        let models = PBListModelsResponse.with {
            $0.models = [sonnet, haiku]
            $0.defaultModel = "sonnet"
        }
        var c = NewConfig(machineId: "mac", cwd: "/p/foo", agent: .claude)
        #expect(settingsModelValue(c, shown: nil) == "默认模型")
        #expect(settingsModelValue(c, shown: sonnet) == "Sonnet")
        #expect(defaultModelName(c, models: models, settings: nil) == "sonnet")
        #expect(defaultModelName(c, models: models, settings: .with { $0.claude.model = "opus" }) == "opus")
        #expect(effortChoices(c, models: models, settings: nil)?.selected == "low")
        #expect(effortChoices(c, models: models, settings: .with { $0.claude.effort = "high" })?.selected == "high")
        c.mode = .auto
        c = pickModel(c, sonnet)
        #expect(c.model == "sonnet" && c.effort == "low" && c.mode == .auto)
        #expect(settingsModelValue(c, shown: sonnet) == "Sonnet · low")
        #expect(settingsContext(c, shown: sonnet) == ("1M", false))
        // Haiku has neither a larger window nor auto.
        let h = pickModel(c, haiku)
        #expect(h.mode == nil && settingsContext(h, shown: haiku).fixed && effortChoices(h, models: models, settings: nil) == nil)
        let choices = modeChoices(h)
        #expect(choices.map(\.title) == ["默认", "询问", "自动", "完全放行"])
        #expect(choices[0].checked && choices[0].desc == "跟随 Claude Code 自己的配置")
        #expect(!choices[2].enabled && choices[2].desc == "当前 Haiku 模型不支持")
        #expect(modeChoices(c)[2].desc == "由 Claude Code 自己判断，拿不准的在对话里问你")
        // Red: full access chosen, or a default that could not be read.
        #expect(settingsModeRed(NewConfig(machineId: "m", cwd: "/p", mode: .full), default: DefaultMode(mode: .ask, known: true, varies: false)))
        #expect(settingsModeRed(NewConfig(machineId: "m", cwd: "/p"), default: DefaultMode(mode: nil, known: false, varies: false)))
        #expect(!settingsModeRed(NewConfig(machineId: "m", cwd: "/p"), default: DefaultMode(mode: .ask, known: true, varies: false)))
    }

    @Test func theMachinesOfAProjectSayWhyOneCannotBeChosen() {
        var st = state([session("a", "/p/foo"), session("b", "/p/foo", machine: "vps")])
        #expect(machineRows(st, cwd: "/p/foo", agent: .claude).map(\.note) == [nil, "没有 Claude Code"])
        st.machines["vps"]?.online = false
        #expect(machineRows(st, cwd: "/p/foo", agent: .codex).last == MachineRow(id: "vps", name: "VPS-dev", online: false, note: "不在线，现在不能新建"))
    }
}
