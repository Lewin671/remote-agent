import Testing
import RAProto
@testable import RACore

@Suite struct ScheduledClosedTests {
    @Test func latestExplicitStateIdentifiesTheCloseAndResetsOnResume() {
        func state(_ status: PBSessionStatus, _ automatic: Bool) -> PBEvent {
            PBEvent.with { $0.sessionState = PBSessionState.with { $0.status = status; $0.detail = "stopped"; $0.scheduledClosed = automatic } }
        }
        let auto = state(.exited, true)
        #expect(derive([auto]).scheduledClosed)
        #expect(!derive([state(.exited, false)]).scheduledClosed)
        #expect(!derive([auto, state(.starting, false)]).scheduledClosed)
        #expect(!derive([auto, state(.exited, false)]).scheduledClosed)
        #expect(!derive([state(.idle, true)]).scheduledClosed)
    }
}
