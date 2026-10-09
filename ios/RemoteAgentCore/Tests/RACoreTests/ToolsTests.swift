import Foundation
import RAProto
import Testing

@testable import RACore

/// Android's core ToolsTest.
@Suite struct ToolsTests {
    private func start(_ seq: UInt64, _ title: String, _ id: String = "", input: String = "", digest: String = "", sec: Int64 = 100) -> PBEvent {
        Mk.ev(
            seq, .toolStarted(.with { $0.title = title; $0.callID = id; $0.input = input; $0.inputDigest = digest; $0.kind = .command; $0.itemType = "Bash" }),
            sec: sec)
    }

    private func call(_ seq: UInt64, _ title: String, _ id: String = "", input: String = "") -> PBEvent {
        Mk.ev(seq, .toolCall(.with { $0.title = title; $0.callID = id; $0.input = input; $0.status = "completed" }))
    }

    private func approval(_ fill: @escaping (inout PBApprovalRequested) -> Void) -> PBEvent.OneOf_Kind {
        .approvalRequested(.with { $0.approvalID = "a"; fill(&$0) })
    }

    private let turn = Mk.ev(1, Mk.turnStarted)

    @Test func parallelCallsPairByIdAndKeepTheirStart() {
        let d = derive([turn, start(2, "go test", "a"), start(3, "go build", "b"), call(4, "go build", "b")], agent: .claude)
        #expect(d.tools.map(\.title) == ["go test"])
        #expect(d.tools[0].seq == 2)
        #expect(d.completedAt[3] == 4)
        #expect(d.tools[0].since == 100_000)
    }

    @Test func aTurnEndingBeforeAResultLeavesItUnconfirmedNotDone() {
        let d = derive([turn, start(2, "go test ./..."), Mk.ev(3, Mk.turnEnded(.aborted))])
        #expect(d.tools.isEmpty)
        #expect(d.background.map(\.tool.title) == ["go test ./..."])
        #expect(!d.background[0].returned)
        #expect(d.unfinished == [2])
        // The next turn moves on; the row stays unconfirmed in the history.
        let d2 = derive([turn, start(2, "go test ./..."), Mk.ev(3, Mk.turnEnded()), Mk.ev(4, Mk.turnStarted)])
        #expect(d2.background.isEmpty)
        #expect(d2.unfinished == [2])
    }

    @Test func aCompletedTurnWithoutAResultClaimsNoBackground() {
        // Codex 0.159 records starts like "exec" and "wait" it never completes.
        let d = derive([turn, start(2, "exec"), Mk.ev(3, Mk.turnEnded(.completed))])
        #expect(d.background.isEmpty)
        #expect(d.noResult == [2])
        #expect(d.unfinished == [2])
    }

    @Test func aBackgroundRunIsNotASuccess() {
        let input = #"{"command":"npm run dev","run_in_background":true}"#
        let d = derive([turn, start(2, "npm run dev", "x", input: input), call(3, "npm run dev", "x", input: input)], agent: .claude)
        #expect(d.tools.isEmpty)
        #expect(d.background.count == 1)
        #expect(d.background[0].returned)
        #expect(!backgroundInput(#"{"run_in_background":false}"#))
        // Claude Code backgrounds a long command by itself: the daemon says so on the result.
        let auto = derive(
            [
                turn, start(2, "sleep 45", "y", input: "sleep 45"),
                Mk.ev(
                    3,
                    .toolCall(
                        .with {
                            $0.callID = "y"
                            $0.input = "sleep 45"
                            $0.background = .timedOut
                            $0.output = "Command running in background with ID: b2wvqs0hj. Output is being written to: /tmp/x"
                        })),
            ], agent: .claude)
        #expect(auto.background.count == 1)
    }

    @Test func waitingForApprovalIsNotRunning() {
        let d = derive([turn, start(2, "rm -rf build", "x", digest: "d1"), Mk.ev(3, approval { $0.inputDigest = "d1" })], agent: .claude)
        #expect(awaitingApproval(d.tools[0], d))
        let other = derive([turn, start(2, "ls", "y", digest: "d2")], agent: .claude)
        #expect(!awaitingApproval(other.tools[0], other))
        let codex = derive([turn, start(2, "exec", "x", input: "go test ./..."), Mk.ev(3, approval { $0.command = "go test ./..." })], agent: .codex)
        #expect(awaitingApproval(codex.tools[0], codex))
        #expect(!awaitingApproval(RunningTool(title: "different", callId: "y", since: 100_000, input: "ls"), codex))
        let byTitle = derive([turn, start(2, "rm -rf build", "x"), Mk.ev(3, approval { $0.command = "rm -rf build" })], agent: .codex)
        #expect(awaitingApproval(byTitle.tools[0], byTitle))
    }

    @Test func theTimerUsesTheDaemonsClockAndFreezesWhileOffline() {
        let t = RunningTool(title: "go test", callId: "", since: 1_000, seq: 1)
        #expect(toolElapsed(t, now: 132_000) == 131_000)
        #expect(toolElapsed(t, now: 132_000, frozenAt: 60_000) == 59_000)
        #expect(toolElapsed(RunningTool(title: "x", callId: "", since: 0), now: 5_000) == nil)
        #expect(clock(131_000) == "2:11")
        #expect(clock(3_723_000) == "1:02:03")
    }

    @Test func labelsNameClaudeToolsAndCodexKinds() {
        #expect(toolLabel(kind: .command, itemType: "Bash", agent: .claude) == "Bash")
        #expect(toolLabel(kind: .command, itemType: "CommandExecution", agent: .codex) == "命令")
    }
}

/// Android's ToolIdsTest (in ToolsTest.kt).
@Suite struct ToolIdsTests {
    private func start(_ seq: UInt64, _ id: String, _ title: String) -> PBEvent {
        Mk.ev(seq, .toolStarted(.with { $0.callID = id; $0.title = title; $0.kind = .command }))
    }

    private func call(_ seq: UInt64, _ id: String, _ bg: PBToolBackground = .unspecified, status: String = "completed") -> PBEvent {
        Mk.ev(seq, .toolCall(.with { $0.callID = id; $0.kind = .command; $0.status = status; $0.background = bg }))
    }

    private let turn = Mk.ev(1, Mk.turnStarted)

    @Test func aSecondEntryOfACodexCallDoesNotEndAnotherStart() {
        // Codex 0.159 via a v2 daemon: call c1 has two entries; c2 still runs.
        let d = derive([turn, start(2, "c1", "sleep 1; ls"), start(3, "c2", "go test"), call(4, "c1"), call(5, "c1#2")], agent: .codex)
        #expect(d.tools.map(\.callId) == ["c2"])
        #expect(d.completedAt[2] == 4)
    }

    @Test func theDaemonSaysACallWentToTheBackground() {
        let d = derive([turn, start(2, "c1", "npm run dev"), call(3, "c1", .requested)], agent: .claude)
        #expect(d.background.count == 1)
        #expect(d.background[0].returned)
    }

    @Test func aStillRunningExecAfterTheTurnIsItsStartNotANewTool() {
        let d = derive(
            [turn, start(2, "c1", "sleep 100"), Mk.ev(3, Mk.turnEnded(.completed)), call(4, "c1", .stillRunning, status: "running")],
            agent: .codex)
        #expect(d.completedAt[2] == 4)
        #expect(!d.unfinished.contains(2))
        #expect(!d.noResult.contains(2))
        #expect(d.background.count == 1)
    }
}
