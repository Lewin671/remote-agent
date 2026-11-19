import Foundation
import RACore
import RAProto
import Testing

@testable import RemoteAgent

/// A value the checks below may change in place.
private final class Ref<Value> {
    var v: Value
    init(_ v: Value) { self.v = v }
}

extension Ref<ChatScroll> {
    func changed(rows: Int) -> Bool { v.contentChanged(rows: rows) }
}

extension Ref<LiveAnnouncer> {
    func announce(last: PBSessionTimelineItem?, dock: PBSessionDockView?, agent: String, now: Int64, initial: Bool) -> String? {
        v.announce(last: last, dock: dock, agent: agent, now: now, initial: initial)
    }
}

@Suite struct ChatScrollTests {

    @Test func aChatAtItsEndStaysThereAsContentArrives() {
        let scroll = Ref(ChatScroll())
        #expect(scroll.changed(rows: 10))
        // The list has not laid the new rows out yet: it is not at the bottom, and nobody dragged it there.
        scroll.v.observe(userDragging: false, atBottom: false, rows: 10)
        #expect(scroll.v.following)
        #expect(scroll.changed(rows: 11))
        #expect(scroll.v.fresh(rows: 11) == 0)
    }

    @Test func draggingAwayStopsFollowingAndCountsWhatArrives() {
        let scroll = Ref(ChatScroll())
        _ = scroll.changed(rows: 10)
        scroll.v.observe(userDragging: true, atBottom: false, rows: 10)
        #expect(!scroll.v.following)
        // A reply grows, rows come in, the keyboard comes up: nothing moves.
        #expect(!scroll.changed(rows: 10))
        #expect(!scroll.changed(rows: 13))
        #expect(scroll.v.fresh(rows: 13) == 3)
        scroll.v.observe(userDragging: false, atBottom: false, rows: 13)
        #expect(!scroll.v.following, "letting go does not start it again")
    }

    @Test func reachingTheEndStartsFollowingAgain() {
        let scroll = Ref(ChatScroll())
        _ = scroll.changed(rows: 5)
        scroll.v.observe(userDragging: true, atBottom: false, rows: 5)
        _ = scroll.changed(rows: 9)
        scroll.v.observe(userDragging: true, atBottom: true, rows: 9)
        #expect(scroll.v.following && scroll.v.fresh(rows: 9) == 0)
        #expect(scroll.changed(rows: 10))
    }

    @Test func askingForTheEndFollowsAgainAtOnce() {
        let scroll = Ref(ChatScroll())
        _ = scroll.changed(rows: 5)
        scroll.v.observe(userDragging: true, atBottom: false, rows: 5)
        _ = scroll.changed(rows: 8)
        scroll.v.jumpToEnd(rows: 8)
        #expect(scroll.v.following)
        // Still on its way there.
        scroll.v.observe(userDragging: false, atBottom: false, rows: 8)
        #expect(scroll.v.following && scroll.v.fresh(rows: 8) == 0)
    }

    @Test func rowsThatFoldAwayAreNeverCountedAsNew() {
        let scroll = Ref(ChatScroll())
        _ = scroll.changed(rows: 20)
        scroll.v.observe(userDragging: true, atBottom: false, rows: 20)
        #expect(scroll.v.fresh(rows: 18) == 0)
    }

    @Test func earlierHistoryJoiningInFrontIsNotNew() {
        let scroll = Ref(ChatScroll())
        _ = scroll.changed(rows: 40)
        scroll.v.observe(userDragging: true, atBottom: false, rows: 40)
        // 1 000 earlier rows join in front, and 2 new ones come in at the end.
        scroll.v.prepended(1_000)
        #expect(!scroll.changed(rows: 1_042))
        #expect(scroll.v.fresh(rows: 1_042) == 2)
    }

    @Test func earlierHistoryJoiningAtTheEndLeavesTheNextRowsToCount() {
        let scroll = Ref(ChatScroll())
        _ = scroll.changed(rows: 40)
        // Following, the rows are seen as they change, before or after they are said to have come in front.
        scroll.v.prepended(1_000)
        #expect(scroll.changed(rows: 1_040))
        scroll.v.observe(userDragging: true, atBottom: false, rows: 1_040)
        #expect(scroll.v.fresh(rows: 1_043) == 3)
    }

    @MainActor private func readingBack() -> ChatScrollTracker {
        let tracker = ChatScrollTracker()
        _ = tracker.viewport(height: 2_000, offset: 1_300, floor: 700, count: 10)
        _ = tracker.interaction(true)
        _ = tracker.viewport(height: 2_000, offset: 500, floor: 700, count: 10)
        _ = tracker.interaction(false)
        return tracker
    }

    @MainActor @Test func keepingSuspendsFollowUntilTheAnchorIsRestored() {
        let tracker = readingBack()
        tracker.keep()
        #expect(!tracker.viewport(height: 9_000, offset: 8_300, floor: 700, count: 500))
        #expect(!tracker.scroll.following)
        tracker.kept()
        _ = tracker.viewport(height: 9_000, offset: 8_300, floor: 700, count: 500)
        #expect(tracker.scroll.following)
    }

    @MainActor @Test func askingForTheEndOrDraggingEndsKeeping() {
        let tracker = readingBack()
        tracker.keep()
        tracker.jump()
        #expect(!tracker.keeping && tracker.scroll.following)
        #expect(tracker.viewport(height: 9_000, offset: 7_500, floor: 700, count: 10))
        tracker.keep()
        _ = tracker.interaction(true)
        #expect(!tracker.keeping)
        #expect(!tracker.viewport(height: 9_000, offset: 7_200, floor: 700, count: 10))
        #expect(!tracker.scroll.following)
    }

    @MainActor @Test func cachedSizeChangesAlreadyAtTheEndNeedNoScroll() {
        let tracker = ChatScrollTracker()
        _ = tracker.viewport(height: 10_000, offset: 9_300, floor: 700, count: 100)
        for i in 0..<100 {
            let height: CGFloat = i.isMultiple(of: 2) ? 10_010 : 10_000
            #expect(!tracker.viewport(height: height, offset: height - 700, floor: 700, count: 100))
        }
        #expect(tracker.viewport(height: 10_012, offset: 9_300, floor: 700, count: 100))
    }

    @MainActor @Test func growthWithinSlackWaitsForFingerAndDecelerationToEnd() {
        let tracker = ChatScrollTracker()
        _ = tracker.viewport(height: 2_000, offset: 1_300, floor: 700, count: 10)
        _ = tracker.interaction(true)
        #expect(!tracker.viewport(height: 2_001, offset: 1_300, floor: 699, count: 11))
        #expect(tracker.scroll.following)
        #expect(tracker.interaction(false))
        #expect(!tracker.interaction(false), "an idle callback does not repeatedly scroll")
        #expect(tracker.viewport(height: 2_200, offset: 1_300, floor: 700, count: 11))
    }

    @MainActor @Test func growthAndViewportChangesFollowOnlyAtTheEnd() {
        let tracker = ChatScrollTracker()
        #expect(!tracker.viewport(height: 2_000, offset: 1_300, floor: 700, count: 10))
        #expect(tracker.viewport(height: 2_300, offset: 1_300, floor: 700, count: 10))
        _ = tracker.viewport(height: 2_300, offset: 1_600, floor: 700, count: 10)
        #expect(tracker.viewport(height: 2_300, offset: 1_600, floor: 400, count: 10))
        _ = tracker.viewport(height: 2_300, offset: 1_900, floor: 400, count: 10)
        #expect(tracker.fresh == 0 && !tracker.away)
        _ = tracker.interaction(true)
        #expect(!tracker.viewport(height: 2_300, offset: 1_100, floor: 400, count: 10))
        _ = tracker.interaction(false)
        #expect(tracker.away)
        #expect(!tracker.viewport(height: 2_600, offset: 1_100, floor: 700, count: 13))
        #expect(tracker.fresh == 3)
        tracker.prepended(100)
        #expect(!tracker.viewport(height: 10_600, offset: 9_100, floor: 700, count: 113))
        #expect(tracker.fresh == 3, "history is not fresh")
        tracker.jump()
        #expect(tracker.fresh == 0)
        #expect(tracker.viewport(height: 10_700, offset: 9_100, floor: 700, count: 113))
    }

    @MainActor @Test func coherentViewportDoesNotReviveFollowingAfterAJumpBack() {
        let tracker = ChatScrollTracker()
        _ = tracker.viewport(height: 2_000, offset: 1_300, floor: 700, count: 10)
        tracker.leave()
        #expect(!tracker.viewport(height: 2_000, offset: 500, floor: 700, count: 10))
        #expect(!tracker.scroll.following && tracker.away)
    }

}

@Suite struct SessionWordsTests {
    @Test func moreThanThreeWaitingMessagesFoldToTwo() {
        let four = (0..<4).map { n in PBSessionTrayItemView.with { $0.requestID = "\(n)" } }
        #expect(shownPending(four, expanded: false).count == 2)
        #expect(shownPending(four, expanded: true).count == 4)
        #expect(shownPending(Array(four.prefix(3)), expanded: false).count == 3)
    }

    @Test func sendingNeedsTextAConnectionAndAComputerThatSpeaksTheSameProtocol() {
        func can(_ text: String, online: Bool = true, keeping: Bool = false, images: Bool = false, ready: Bool = true, refused: Bool = false) -> Bool {
            composerCanSend(text: text, online: online, keeping: keeping, hasImages: images, imagesReady: ready, refused: refused)
        }
        #expect(can("你好"))
        #expect(!can("  \n"))
        #expect(!can("你好", online: false), "the draft stays and sending waits")
        #expect(!can("你好", refused: true))
        #expect(!can("你好", keeping: true))
        #expect(can("", images: true))
        #expect(!can("带图", images: true, ready: false), "never a message with some of its images")
    }

    @Test func aLeadingSlashIsWarnedAbout() {
        #expect(slashWarning("/compact"))
        #expect(slashWarning("  /model"))
        #expect(!slashWarning("a/b"))
        #expect(!slashWarning(""))
    }

    @Test func voiceOverIsToldOfRepliesAndOfWhatWaitsButNotOfEverything() {
        let say = Ref(LiveAnnouncer())
        let reply = { (n: Int) in
            PBSessionTimelineItem.with {
                $0.id = "e\(n)"
                $0.agentMessage = .with { $0.markdown = "好" }
            }
        }
        let approval = PBSessionDockView.with {
            $0.hasItem_p = true
            $0.kind = .approval
            $0.itemID = "A"
            $0.title = .with { $0.literal = "Codex 请求执行命令" }
            $0.approval = .with { $0.approvalID = "A" }
        }
        // What is there when the page opens is not announced.
        #expect(say.announce(last: reply(1), dock: approval, agent: "Codex", now: 0, initial: true) == nil)
        #expect(say.announce(last: reply(1), dock: approval, agent: "Codex", now: 10_000, initial: false) == nil)
        #expect(say.announce(last: reply(2), dock: approval, agent: "Codex", now: 20_000, initial: false) == "Codex 回复了")
        // A burst of replies is one announcement.
        #expect(say.announce(last: reply(3), dock: approval, agent: "Codex", now: 21_000, initial: false) == nil)
        let turnEnd = PBSessionTimelineItem.with { $0.id = "e4"; $0.turnEnd = .with { $0.outcome = .completed } }
        #expect(say.announce(last: turnEnd, dock: approval, agent: "Codex", now: 40_000, initial: false) == nil)
        // Something new that needs the user is said at once.
        let question = PBSessionDockView.with {
            $0.hasItem_p = true
            $0.kind = .question
            $0.itemID = "Q"
            $0.title = .with { $0.literal = "Codex 在问你" }
            $0.question = .with { $0.questionID = "Q" }
        }
        #expect(say.announce(last: reply(5), dock: question, agent: "Codex", now: 41_000, initial: false) == "Codex 在问你")
        #expect(say.announce(last: reply(5), dock: question, agent: "Codex", now: 60_000, initial: false) == nil)
    }
}
