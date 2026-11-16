import Foundation
import RAProto
import Testing

@testable import RACore

/// Android's core ToolsTest (rendering helpers).
@Suite struct ToolsTests {
    @Test func theTimerUsesTheDaemonsClockAndFreezesWhileOffline() {
        let t = PBRunningTool.with { $0.title = "go test"; $0.sinceMs = 1_000; $0.seq = 1 }
        #expect(toolElapsed(t, now: 132_000) == 131_000)
        #expect(toolElapsed(t, now: 132_000, frozenAt: 60_000) == 59_000)
        #expect(toolElapsed(PBRunningTool.with { $0.title = "x" }, now: 5_000) == nil)
        #expect(clock(131_000) == "2:11")
        #expect(clock(3_723_000) == "1:02:03")
    }

    @Test func labelsNameClaudeToolsAndCodexKinds() {
        #expect(toolLabel(kind: .command, itemType: "Bash", agent: .claude) == "Bash")
        #expect(toolLabel(kind: .command, itemType: "CommandExecution", agent: .codex) == L10n.toolsCommand())
        #expect(toolLabel(kind: .command, itemType: "CommandExecution", agent: .antigravity) == L10n.toolsCommand())
    }
}
