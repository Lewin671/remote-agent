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
        var projection = EventProjection()
        let result = projection.update([input(1, .queued), input(2, status), Mk.ev(3, .turnEnded(.init()))], live: live(1, [input(0, pending)]))
        #expect(result.inputStatus("r") == status)
        #expect(pendingInputs(result).isEmpty && !result.running)
    }

    @Test func newerQueueKeepsPauseStageModeAndOtherSnapshotInputsStillRecover() {
        var projection = EventProjection()
        let history = projection.update([input(2, .queued, paused: true)])
        let empty = projection.currentActions(live: live(1))
        #expect(empty.inputStatus("r") == .queued && pendingInputs(empty).count == 1)
        var other = input(0, .queued)
        other.inputState.requestID = "other"
        let result = projection.currentActions(live: live(1, [input(0, .heldByCli), other]))
        #expect(result.input("r")?.detail == history.input("r")?.detail)
        #expect(result.input("r")?.accepted == history.input("r")?.accepted)
        #expect(result.inputStatus("other") == .queued && history.inputStatus("other") == nil)
    }

    @Test func refreshedSnapshotAtWatermarkRemovesOrRestoresRealPendingState() {
        var projection = EventProjection()
        _ = projection.update([input(1, .queued)])
        #expect(projection.currentActions(live: live(1)).inputStatus("r") == .unconfirmed)
        let result = projection.currentActions(live: live(1, [input(0, .heldByCli, paused: true)]))
        #expect(result.inputStatus("r") == .heldByCli && pendingInputs(result).count == 1)
        #expect(result.input("r")?.detail.paused == true)
        #expect(result.input("r")?.detail.mode == .queue && result.input("r")?.detail.stage == .submitting)
    }

    @Test func runningUsesLastTransitionAndProjectionPreservesWatermarks() {
        let start = Mk.ev(2, .turnStarted(.init()))
        let end = Mk.ev(3, .turnEnded(.init()))
        let unrelated = Mk.ev(4, .agentMessage(.with { $0.text = "reply" }))
        var projection = EventProjection()
        let first = projection.update([input(1, .queued), start])
        #expect(projection.currentActions(live: live(1, running: false)).running)
        let later = projection.update([input(1, .queued), start, end, unrelated])
        #expect(!projection.currentActions(live: live(1)).running)
        #expect(projection.currentActions(live: live(3)).running)
        #expect(later.input("r")?.lastSeq == 1 && first.runningSeq == 2 && first.running)
    }
}
