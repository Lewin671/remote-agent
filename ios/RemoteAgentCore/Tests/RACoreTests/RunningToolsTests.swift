import Foundation
import RAProto
import Testing

@testable import RACore

/// Android's app RunningToolsTest.
@Suite struct RunningToolsTests {
    private func start(_ seq: UInt64, _ title: String, _ id: String, input: String = "") -> PBEvent {
        Mk.ev(seq, .toolStarted(.with { $0.title = title; $0.callID = id; $0.input = input; $0.kind = .command; $0.itemType = "Bash" }))
    }

    private func call(_ seq: UInt64, _ title: String, _ id: String, status: String = "completed", input: String = "") -> PBEvent {
        Mk.ev(seq, .toolCall(.with { $0.title = title; $0.callID = id; $0.status = status; $0.input = input; $0.kind = .command }))
    }

    private func say(_ seq: UInt64, _ text: String) -> PBEvent { Mk.ev(seq, Mk.agent(text)) }
    private let turn = Mk.ev(1, Mk.turnStarted)

    private func chat(_ events: [PBEvent]) -> [ChatItem] { buildChat(events, d: derive(events, agent: .claude), machine: "MacBook") }

    private func isRunning(_ i: ChatItem) -> Bool { if case .running = i { return true } else { return false } }
    private func isTool(_ i: ChatItem) -> Bool { if case .tool = i { return true } else { return false } }

    @Test func aStartedToolShowsAtOnceWhereItStarted() {
        let items = chat([turn, start(2, "go test ./...", "a"), say(3, "等测试跑完")])
        #expect(isRunning(items[0]))
        #expect(items[0].key == "e2")
        if case .agent = items[1] {} else { Issue.record("not the agent's message: \(items[1])") }
    }

    @Test func itsResultUpdatesTheSameRow() {
        let items = chat([turn, start(2, "go test ./...", "a"), say(3, "等测试跑完"), call(4, "go test ./...", "a")])
        #expect(items.map(\.key) == ["e2", "e3"])
        #expect(isTool(items[0]))
    }

    @Test func parallelCallsKeepTheirOwnStateAndFailuresShow() {
        let items = chat([
            turn, start(2, "go test ./...", "a"), start(3, "go build ./...", "b"), start(4, "pnpm lint", "c"),
            call(5, "go build ./...", "b"), call(6, "pnpm lint", "c", status: "failed"),
        ])
        #expect(items.map(\.key) == ["e2", "e3", "e4"])
        #expect(isRunning(items[0]))
        #expect(isTool(items[1]))
        if case .tool(let t) = items[2] { #expect(!toolOk(t.call)) } else { Issue.record("not a tool: \(items[2])") }
    }

    @Test func aRunningToolIsNeverFoldedIntoTheCompletedOnes() {
        let items = chat([turn, call(2, "ls", "x"), call(3, "pwd", "y"), start(4, "go test", "a"), call(5, "cat a", "z")])
        if case .tools = items[0] {} else { Issue.record("not folded: \(items[0])") }
        #expect(isRunning(items[1]))
        #expect(isTool(items[2]))
    }

    @Test func anUnfinishedToolStaysUnconfirmedAfterTheTurn() {
        let items = chat([turn, start(2, "go test ./...", "a"), Mk.ev(3, Mk.turnEnded())])
        if case .running(let r) = items[0] { #expect(r.unfinished) } else { Issue.record("not running: \(items[0])") }
    }

    @Test func aBackgroundCallIsNotShownAsASuccess() {
        let input = #"{"command":"npm run dev","run_in_background":true}"#
        let items = chat([turn, start(2, "npm run dev", "a", input: input), Mk.ev(3, .toolCall(.with { $0.callID = "a"; $0.input = input; $0.background = .requested }))])
        if case .tool(let t) = items[0] { #expect(t.background) } else { Issue.record("not a tool: \(items[0])") }
    }

    @Test func aMatchingCodexApprovalReplacesTheRunningTimer() throws {
        let events = [turn, start(2, "exec", "a", input: "go test ./..."), Mk.ev(3, .approvalRequested(.with { $0.approvalID = "p"; $0.command = "go test ./..." }))]
        let d = derive(events, agent: .codex)
        let tool = try #require(d.tools.count == 1 ? d.tools[0] : nil)
        let state = toolState(tool, unfinished: false, d: d, frozenAt: 0)
        #expect(state == .approval)
        #expect(toolTimer(tool, state: state, now: 500_000, frozenAt: 0) == "—")
        #expect(toolState(RunningTool(title: "other", callId: "b", since: tool.since, input: "ls"), unfinished: false, d: d, frozenAt: 0) == .running)
    }

    // --- not in Android's tests ---

    @Test func aTimerReadsTheClockUntilTheConnectionDrops() {
        let d = Derived()
        let tool = RunningTool(title: "go test", callId: "a", since: 1_000)
        #expect(toolState(tool, unfinished: true, d: d, frozenAt: 5_000) == .unconfirmed)
        #expect(toolState(tool, unfinished: false, d: d, frozenAt: 5_000) == .sync)
        #expect(toolTimer(tool, state: .running, now: 133_000, frozenAt: 0) == "2:12")
        #expect(toolTimer(tool, state: .sync, now: 133_000, frozenAt: 61_000) == "1:00")
        #expect(toolTimer(RunningTool(title: "x", callId: "", since: 0), state: .running, now: 9, frozenAt: 0) == "")
        #expect(ToolState.unconfirmed.text == "状态待确认")
    }
}

/// Android's BackgroundFieldTest (in RunningToolsTest.kt).
@Suite struct BackgroundFieldTests {
    @Test func theDaemonsBackgroundFieldMakesTheRowUnconfirmedWithItsReason() throws {
        let events = [
            Mk.ev(1, Mk.turnStarted),
            Mk.ev(2, .toolStarted(.with { $0.callID = "c"; $0.title = "sleep 100"; $0.kind = .command })),
            Mk.ev(3, Mk.turnEnded(.completed)),
            Mk.ev(4, .toolCall(.with { $0.callID = "c"; $0.title = "sleep 100"; $0.status = "running"; $0.kind = .command; $0.background = .stillRunning })),
        ]
        let items = buildChat(events, d: derive(events, agent: .codex), machine: "M")
        let tools = items.compactMap { item -> ChatItem.Tool? in if case .tool(let t) = item { return t } else { return nil } }
        let tool = try #require(tools.count == 1 ? tools[0] : nil)
        #expect(tool.key == "e2")
        #expect(tool.background)
        #expect(!items.contains { if case .running = $0 { return true } else { return false } })
        #expect(backgroundNote(tool.call.background) == "这一轮结束时它仍在运行；它什么时候结束这里不知道，请到终端确认。")
        #expect(backgroundTitle(kind: .command, itemType: "Bash", agent: .claude) == "后台命令")
        #expect(backgroundTitle(kind: .other, itemType: "Task", agent: .claude) == "Task（后台）")
    }
}
