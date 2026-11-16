import Foundation
import RAProto
import Testing

@testable import RACore

/// Android's core V8InputTest (UI helpers).
@Suite struct V8InputTests {
    @Test func sendAndApprovalCommandsCarryV8Choices() {
        let send = PBSendRequest.with { $0.sessionID = "s"; $0.requestID = "r"; $0.mode = .queue }
        let approval = PBResolveApprovalRequest.with { $0.sessionID = "s"; $0.approvalID = "a"; $0.allow = true; $0.always = true }
        #expect(send.mode == .queue)
        #expect(approval.always)
        #expect(approvalAlwaysText("Yes, and don't ask again for commands that start with `go test`") == "「go test」开头的命令以后不再问")
        #expect(approvalAlwaysText("Yes, and always allow access to /w from this project") == "本项目访问 /w 以后不再问")
        #expect(approvalAlwaysText("Yes, and switch to accept edits") == L10n.approvalAutoEdits())
        #expect(approvalAlwaysText("New CLI wording") == "New CLI wording")
    }

    // --- not in Android's tests ---

    @Test func theKnownFormsMustTakeTheWholeMessage() {
        #expect(approvalAlwaysText("don't ask again for commands that start with npm run") == "「npm run」开头的命令以后不再问")
        // Something after it, or a second line: not the known form.
        #expect(approvalAlwaysText("always allow access to /w from this project today") == "always allow access to /w from this project today")
        #expect(approvalAlwaysText("don't ask again for commands that start with a\nb") == "don't ask again for commands that start with a\nb")
        #expect(approvalAlwaysText("Accept Edits for this session") == L10n.approvalAutoEdits())
    }
}
