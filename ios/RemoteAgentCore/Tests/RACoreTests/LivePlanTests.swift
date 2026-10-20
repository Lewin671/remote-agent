import Foundation
import RAProto
import Testing

@testable import RACore

/// A pending plan reads as the plan event the live snapshot names, not as the terminal's box.
@Suite struct LivePlanTests {
    let screen =
        "⎿ /plan to preview\n────────\nReady to code?\nHere is Claude's plan:\n────────\n计划：在 run-P 目录创建 5 个 beta 前缀文件\n"
    let markdown = "# 计划：在 run-P 目录创建 5 个 beta 前缀文件\n\n## Context\n\n- 用户要求用前缀 beta"

    func history(_ text: String? = nil) -> [PBEvent] {
        var first = PBEvent()
        first.sessionID = "s"
        first.seq = 1
        first.agentMessage.text = "先问一个问题"
        var plan = PBEvent()
        plan.sessionID = "s"
        plan.seq = 2
        plan.planProposed.text = text ?? markdown
        return [first, plan]
    }

    func live(_ seq: UInt64 = 2) -> PBLiveSnapshot {
        var l = PBLiveSnapshot()
        l.session.id = "s"
        l.session.planOpen = true
        l.planEventSeq = seq
        l.planScreenHash = Data(repeating: 1, count: 32)
        l.planScreen = screen
        return l
    }

    @Test func aPendingPlanReadsAsItsEvent() {
        #expect(livePlanText(live(), events: history()) == markdown)
        #expect(currentActions(derive(history()), live: live(), events: history()).plan?.text == markdown)
        // A history that does not start at the first event still holds it.
        #expect(livePlanText(live(), events: Array(history().dropFirst())) == markdown)
    }

    @Test func theScreenStandsInOnlyWithoutThatEvent() {
        // Not here yet, another kind at that place, too large to carry, or empty.
        #expect(livePlanText(live(3), events: history()) == screen)
        #expect(livePlanText(live(1), events: history()) == screen)
        #expect(livePlanText(live(), events: history("长计划" + skeletonNote)) == screen)
        #expect(livePlanText(live(), events: history("  ")) == screen)
        #expect(currentActions(derive(history()), live: live()).plan?.text == screen)
    }

    @Test func onlyTheSnapshotSaysAPlanIsPending() {
        #expect(currentActions(derive(history()), live: nil, events: history()).plan == nil)
        var none = live()
        none.planEventSeq = 0
        #expect(currentActions(derive(history()), live: none, events: history()).plan == nil)
    }
}
