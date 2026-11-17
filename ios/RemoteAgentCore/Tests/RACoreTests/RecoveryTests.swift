import Foundation
import Testing
import os
@testable import RACore

@Suite struct RecoveryTests {
    @Test func terminalRecoveryBackoffAndSharedGlobalBudget() {
        let time = OSAllocatedUnfairLock(initialState: Int64(0))
        let budget = RecoveryBudget()
        let t1 = TerminalRecovery(now: { time.withLock { $0 } }, random: { 0.5 }, budget: budget)
        let t2 = TerminalRecovery(now: { time.withLock { $0 } }, random: { 0.5 }, budget: budget)
        let t3 = TerminalRecovery(now: { time.withLock { $0 } }, random: { 0.5 }, budget: budget)

        #expect(t1.takeOrDelayMs() == nil)
        t1.recordFailureIfUnstabilized()
        let d1 = t1.takeOrDelayMs()
        #expect(d1 != nil && d1! >= 500)

        t1.onStabilized()
        time.withLock { $0 = 1_000 }
        #expect(t1.takeOrDelayMs() == nil)

        #expect(t2.takeOrDelayMs() == nil)
        #expect(t2.takeOrDelayMs() == nil)
        #expect(t3.takeOrDelayMs() == nil)
        // t1(1000)=3, t2(1000)=2,1, t3(1000)=0! A fresh 4th terminal still hits global wait >= 500ms:
        let t4 = TerminalRecovery(now: { time.withLock { $0 } }, random: { 0.5 }, budget: budget)
        let globalDelay = t4.takeOrDelayMs()
        #expect(globalDelay != nil && globalDelay! >= 500)
    }
}
