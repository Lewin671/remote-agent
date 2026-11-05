import RAProto
import Testing
@testable import RACore

@Suite struct QueueSnapshotTests {
    func input(_ seq: UInt64, _ status: PBInputStatus, paused: Bool = false) -> PBEvent {
        Mk.ev(seq, Mk.inputState("r", status) {
            $0.text = "example prompt"; $0.paused = paused
            $0.stage = paused ? .submitting : .waiting
            $0.mode = paused ? .queue : .steer
        })
    }
    func live(_ seq: UInt64, _ actions: [PBEvent] = [], running: Bool = true) -> PBLiveSnapshot {
        .with { $0.session.id = "s"; $0.session.lastSeq = seq; $0.actions = actions; $0.running = running }
    }

    @Test(arguments: [PBInputStatus.queued, .heldByCli], [PBInputStatus.submitted, .canceled, .failed, .needsTerminal])
    func newerOutcomesCannotBeRevived(pending: PBInputStatus, status: PBInputStatus) {
        let history = derive([input(1, .queued), input(2, status), Mk.ev(3, .turnEnded(.init()))])
        let result = currentActions(history, live: live(1, [input(0, pending)]))
        #expect(result.inputs["r"] == status)
        #expect(pendingInputs(result).isEmpty && !result.running)
    }

    @Test func newerQueueKeepsPauseStageModeAndOtherSnapshotInputsStillRecover() {
        let history = derive([input(2, .queued, paused: true)])
        let empty = currentActions(history, live: live(1))
        #expect(empty.inputs["r"] == .queued && pendingInputs(empty).count == 1)
        var other = input(0, .queued)
        other.inputState.requestID = "other"
        let result = currentActions(history, live: live(1, [input(0, .heldByCli), other]))
        #expect(result.inputDetails["r"] == history.inputDetails["r"])
        #expect(result.accepted["r"] == history.accepted["r"])
        #expect(result.inputs["other"] == .queued && history.inputs["other"] == nil)
    }

    @Test func refreshedSnapshotAtWatermarkRemovesOrRestoresRealPendingState() {
        let history = derive([input(1, .queued)])
        #expect(currentActions(history, live: live(1)).inputs["r"] == .unconfirmed)
        let result = currentActions(history, live: live(1, [input(0, .heldByCli, paused: true)]))
        #expect(result.inputs["r"] == .heldByCli && pendingInputs(result).count == 1)
        #expect(result.inputDetails["r"]?.paused == true)
        #expect(result.inputDetails["r"]?.mode == .queue && result.inputDetails["r"]?.stage == .submitting)
    }

    @Test func runningUsesLastTransitionAndProjectionPreservesWatermarks() {
        let start = Mk.ev(2, .turnStarted(.init()))
        let end = Mk.ev(3, .turnEnded(.init()))
        let unrelated = Mk.ev(4, .agentMessage(.with { $0.text = "reply" }))
        var projection = EventProjection()
        let first = projection.update([input(1, .queued), start])
        #expect(currentActions(first, live: live(1, running: false)).running)
        let later = projection.update([input(1, .queued), start, end, unrelated])
        #expect(!currentActions(later, live: live(1)).running)
        #expect(currentActions(later, live: live(3)).running)
        #expect(later.inputSeqs["r"] == 1 && first.runningSeq == 2 && first.running)
    }
}
