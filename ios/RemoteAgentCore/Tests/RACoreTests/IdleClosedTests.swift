import Testing
import RAProto
@testable import RACore

@Suite struct IdleClosedTests {
    @Test func latestExplicitStateIdentifiesTheCloseAndResetsOnResume() {
        func state(_ status: PBSessionStatus, _ idle: Bool) -> PBEvent {
            PBEvent.with { $0.sessionState = PBSessionState.with { $0.status = status; $0.detail = "stopped"; $0.idleClosed = idle } }
        }
        let idle = state(.exited, true)
        #expect(derive([idle]).idleClosed)
        #expect(!derive([idle]).scheduledClosed)
        #expect(!derive([state(.exited, false)]).idleClosed)
        #expect(!derive([idle, state(.starting, false)]).idleClosed)
        #expect(!derive([idle, state(.exited, false)]).idleClosed)
        #expect(!derive([state(.idle, true)]).idleClosed)
    }
}
