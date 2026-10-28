import RAProto
import Testing
@testable import RACore

@Suite struct HeldFilesTests {
    private let file = PBSentFile.with { $0.fileID = "file-1"; $0.name = "report.csv"; $0.sizeBytes = 42 }
    private let sender = PBOrigin.with { $0.kind = .client; $0.deviceID = "sender" }

    @Test(arguments: [PBAgent.codex, .claude], [false, true])
    func aHeldSnapshotWithoutQueuedHistoryRestoresAttachmentsAndOrigin(agent: PBAgent, mixed: Bool) throws {
        let event = Mk.ev(4, Mk.inputState("r", .heldByCli) {
            $0.text = mixed ? "inspect" : ""; $0.imageIds = mixed ? ["image-1"] : []
            $0.files = [file]; $0.mode = .queue; $0.stage = .waiting
        }, origin: sender)
        let live = PBLiveSnapshot.with { $0.actions = [event] }
        let state = currentActions(derive([], agent: agent), live: live)
        let input = try #require(pendingInputs(state).first)
        #expect(input.files == [file] && input.origin == sender && input.mode == .queue && input.held)
        #expect(state.accepted["r"]?.text == (mixed ? "inspect" : ""))
        #expect(state.accepted["r"]?.imageIds == (mixed ? ["image-1"] : []))
        let later = derive([event, Mk.ev(5, Mk.inputState("r", .heldByCli) { $0.stage = .pastingImages })], agent: agent)
        #expect(pendingInputs(later).first?.files == [file])
        #expect(later.inputDetails["r"]?.imageIds == (mixed ? ["image-1"] : []))
        #expect(pendingInputs(later).first?.origin == sender && pendingInputs(later).first?.mode == .queue)
    }

    @Test func heldSnapshotsKeepQueuedOriginalsAndStatusOnlySnapshotsKeepMetadata() throws {
        let queued = Mk.ev(1, Mk.inputState("r", .queued) {
            $0.text = "the complete original prompt"; $0.imageIds = ["image-1"]
            $0.files = [file]; $0.mode = .queue
        }, origin: sender)
        let history = derive([queued])
        let truncated = Mk.ev(4, Mk.inputState("r", .heldByCli) {
            $0.text = "the complete…"; $0.files = [file]; $0.mode = .steer; $0.stage = .waiting
        }, origin: .with { $0.kind = .local })
        let state = currentActions(history, live: .with { $0.actions = [truncated] })
        #expect(state.accepted["r"]?.text == "the complete original prompt")
        #expect(state.accepted["r"]?.imageIds == ["image-1"])
        #expect(pendingInputs(state).first?.origin == sender && pendingInputs(state).first?.mode == .steer)
        let phase = Mk.ev(5, Mk.inputState("r", .heldByCli) { $0.stage = .pastingImages })
        let later = currentActions(state, live: .with { $0.actions = [phase] })
        let input = try #require(pendingInputs(later).first)
        #expect(input.files == [file] && input.mode == .steer && input.origin == sender)
        #expect(later.inputDetails["r"]?.imageIds == ["image-1"])
        #expect(later.accepted["r"]?.stage == .pastingImages)
        #expect(history.inputDetails["r"]?.mode == .queue && history.accepted["r"]?.stage == .unspecified)
    }
}
