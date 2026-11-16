import Foundation
import RAProto
import Testing
@testable import RACore

@Suite struct CorebindSmokeTests {
    @Test func reduceAndIncrementalProjectionDecodeProjectionState() {
        let events = [
            Mk.ev(1, Mk.turnStarted),
            Mk.ev(2, Mk.inputState("r1", .queued) { $0.text = "hello" }),
            Mk.ev(3, .toolStarted(.with { $0.callID = "c1"; $0.title = "go test"; $0.inputDigest = "d1" })),
            Mk.ev(4, .approvalRequested(.with { $0.approvalID = "a1"; $0.inputDigest = "d1" })),
        ]
        let reduced = derive(events, agent: .claude)
        #expect(reduced.running)
        #expect(reduced.approvals.map(\.approvalID) == ["a1"])
        #expect(reduced.tools.count == 1)
        #expect(reduced.tools[0].awaitingApproval)
        #expect(reduced.pendingInputs.map(\.requestID) == ["r1"])

        var projection = EventProjection()
        let first = projection.update(Array(events.prefix(2)), agent: .claude)
        #expect(first.running && first.approvals.isEmpty)
        let second = projection.update(events, agent: .claude)
        #expect(second == reduced)

        let live = PBLiveSnapshot.with {
            $0.session.id = "s"
            $0.session.lastSeq = 4
            $0.running = false
            $0.actions = []
        }
        let overlaid = projection.update(events, agent: .claude, live: live)
        #expect(!overlaid.running)
        #expect(overlaid.approvals.isEmpty)
        #expect(overlaid.inputStatus("r1") == .unconfirmed)

        let reset = projection.update([Mk.ev(1, Mk.user("local"))], agent: .codex)
        #expect(reset.typedLocallySeqs == [1])
        #expect(reset.approvals.isEmpty)

        // Empty (0-byte) ProjectionState returns across gomobile must still decode cleanly.
        let planOnly = [Mk.ev(1, .planProposed(.with { $0.text = "plan" }))]
        #expect(projection.update(planOnly, agent: .codex).hasPlan)
        #expect(!projection.update(planOnly, agent: .codex, live: nil).hasPlan)
        #expect(projection.update([Mk.ev(1, Mk.agent("reply"))], agent: .codex) == PBProjectionState())
    }
}
