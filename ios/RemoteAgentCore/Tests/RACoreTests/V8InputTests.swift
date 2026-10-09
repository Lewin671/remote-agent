import Foundation
import RAProto
import Testing

@testable import RACore

/// Android's core V8InputTest.
@Suite struct V8InputTests {
    private func input(_ seq: UInt64, _ id: String, _ status: PBInputStatus, _ mode: PBSendMode = .unspecified, paused: Bool = false, text: String = "") -> PBEvent {
        Mk.ev(seq, Mk.inputState(id, status) { $0.mode = mode; $0.paused = paused; $0.text = text })
    }

    @Test func pendingProjectionFollowsQueueHeldPauseResumeAndDelivery() {
        let events = [
            input(1, "queue", .queued, .queue, text: "later"),
            input(2, "steer", .queued, .steer, text: "now"),
            input(3, "steer", .heldByCli),
            input(4, "queue", .queued, paused: true),
        ]
        let pending = pendingInputs(derive(events))
        #expect(pending.map(\.id) == ["queue", "steer"])
        #expect(pending[0].text == "later")
        #expect(pending[0].mode == .queue)
        #expect(pending[0].paused)
        #expect(pending[1].mode == .steer)
        #expect(pending[1].held)
        #expect(pendingInputs(derive(events + [input(5, "queue", .canceled)])).map(\.id) == ["steer"])
        #expect(pendingInputs(derive(events + [input(5, "queue", .canceled), input(6, "steer", .submitted)])).isEmpty)
        let resumed = pendingInputs(derive(events + [input(5, "queue", .queued)]))
        #expect(!resumed[0].paused)
    }

    @Test func sendAndApprovalCommandsCarryV8Choices() {
        let send = PBSendRequest.with { $0.sessionID = "s"; $0.requestID = "r"; $0.mode = .queue }
        let approval = PBResolveApprovalRequest.with { $0.sessionID = "s"; $0.approvalID = "a"; $0.allow = true; $0.always = true }
        #expect(send.mode == .queue)
        #expect(approval.always)
        #expect(approvalAlwaysText("Yes, and don't ask again for commands that start with `go test`") == "`go test` 开头的命令以后不再问")
        #expect(approvalAlwaysText("Yes, and always allow access to /w from this project") == "本项目访问 /w 以后不再问")
        #expect(approvalAlwaysText("Yes, and switch to accept edits") == "本会话自动接受编辑")
        #expect(approvalAlwaysText("New CLI wording") == "New CLI wording")
    }

    // --- not in Android's tests ---

    @Test func theKnownFormsMustTakeTheWholeMessage() {
        #expect(approvalAlwaysText("don't ask again for commands that start with npm run") == "`npm run` 开头的命令以后不再问")
        // Something after it, or a second line: not the known form.
        #expect(approvalAlwaysText("always allow access to /w from this project today") == "always allow access to /w from this project today")
        #expect(approvalAlwaysText("don't ask again for commands that start with a\nb") == "don't ask again for commands that start with a\nb")
        #expect(approvalAlwaysText("Accept Edits for this session") == "本会话自动接受编辑")
    }
}
