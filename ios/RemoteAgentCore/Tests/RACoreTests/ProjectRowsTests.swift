import Foundation
import RAProto
import SwiftProtobuf
import Testing

@testable import RACore

/// What the project screens decide before drawing: the list of projects, a new one's folder, the new-session box and 项目设置.
@Suite(.timeLimit(.minutes(2))) struct ProjectRowsTests {
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

    private func machine(_ id: String, _ name: String, online: Bool = true, agents: [PBAgent] = [.codex], version: UInt32 = peerProtocolVersion) -> MachineView {
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

    // MARK: The new-session command and 项目设置 (retained for SessionScreen / Schedules / DefaultModeSheet)

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
        var d = ProjectSettingsDraft(cwd: "/p/foo", machine: "mac", agent: .codex)
        #expect(d.op() == nil && !d.moved && d.agents(st) == [.codex, .claude])
        var c = d.config(st)
        c.mode = .full
        d.edit(c)
        #expect(d.op() == PrefsOp.SetSettings(path: "/p/foo", machineId: "mac", sets: [c.agentSettings()]))
        // The sheet is for the CLI it was opened for; another CLI's set stays as stored, and is reached by opening it for that one.
        #expect(d.current(st) == .codex && d.config(st) == c)
        let other = ProjectSettingsDraft(cwd: "/p/foo", machine: "mac", agent: .claude)
        #expect(other.current(st) == .claude && other.config(st) == NewConfig(machineId: "mac", cwd: "/p/foo", agent: .claude))
    }

    @Test func anotherMachineStartsEveryCliOver() {
        let st = state([session("a", "/p/foo")])
        var d = ProjectSettingsDraft(cwd: "/p/foo", machine: "mac", agent: .claude)
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
            $0.slug = "claude-haiku-4-5-20251001"
            $0.contextWindow = 200_000
            $0.autoUnavailable = true
        }
        let models = PBListModelsResponse.with {
            $0.models = [sonnet, haiku]
            $0.defaultModel = "sonnet"
        }
        var c = NewConfig(machineId: "mac", cwd: "/p/foo", agent: .claude)
        #expect(settingsModelValue(c, shown: nil) == L10n.newSessionDefaultModel())
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
        let choices = modeChoices(h, models: models)
        #expect(choices.map(\.title) == [L10n.modelsDefault(), L10n.modesAsk(), L10n.modesAuto(), L10n.modesFullAccess()])
        #expect(choices[0].checked && choices[0].desc == "跟随 Claude Code 自己的配置")
        #expect(!choices[2].enabled && choices[2].desc == L10n.modesModelUnsupported())
        #expect(modeChoices(c, models: models)[2].desc == "由 Claude Code 自己判断，拿不准的在对话里问你")
        let agyModels = PBListModelsResponse.with {
            $0.models = [
                .with { $0.slug = "gemini-3.1-pro-high"; $0.autoUnavailable = true },
                .with { $0.slug = "gemini-3-flash"; $0.autoUnavailable = true },
            ]
        }
        let agyConfig = NewConfig(machineId: "m", cwd: "/p", agent: .antigravity)
        let agyChoices = modeChoices(agyConfig, models: agyModels)
        #expect(!agyChoices[2].enabled && agyChoices[2].desc == L10n.modesAccountUnsupported())
        // Red: full access chosen, or a default that could not be read.
        #expect(settingsModeRed(NewConfig(machineId: "m", cwd: "/p", mode: .full), default: DefaultMode(mode: .ask, known: true, varies: false)))
        #expect(settingsModeRed(NewConfig(machineId: "m", cwd: "/p"), default: DefaultMode(mode: nil, known: false, varies: false)))
        #expect(!settingsModeRed(NewConfig(machineId: "m", cwd: "/p"), default: DefaultMode(mode: .ask, known: true, varies: false)))
    }

    @Test func theMachinesOfAProjectSayWhyOneCannotBeChosen() {
        var st = state([session("a", "/p/foo"), session("b", "/p/foo", machine: "vps")])
        #expect(machineRows(st, cwd: "/p/foo", agent: .claude).map(\.note) == [
            "已装 Codex、Claude Code",
            "已装 Codex；这台电脑没有 Claude Code，会改用 Codex"
        ])
        st.machines["vps"]?.online = false
        #expect(machineRows(st, cwd: "/p/foo", agent: .codex).last == ProjectMachineRow(id: "vps", name: "VPS-dev", online: false, note: L10n.newSessionIosMachineUnavailable()))
    }
}
