import Foundation
import Testing
@testable import RACore

/// The 「连接」group's words (docs/design-system.md §6) for every state, driven by clientcore.
@Suite struct DirectViewTests {
    func status(_ state: DirectState, reason: DirectReason? = nil, file: Bool = false, read: Bool = false, warming: Bool = false, compared: Bool = false, files: [Int: Double] = [:], reads: [Int: Double] = [:]) -> DirectStatus {
        var s = DirectStatus(state: state, reason: reason)
        s.fileDirect = file; s.readDirect = read; s.warming = warming; s.compared = compared; s.fileMiBps = files; s.readMs = reads
        return s
    }
    let both: [Int: Double] = [DirectPath.direct: 48, DirectPath.relay: 175]

    @Test func directForBothPurposesSaysNothingMoreAndForOneSaysWhich() {
        let v = DirectView.make(status(.connected, file: true, read: true, files: [1: 6.3, 0: 4.1], reads: both))
        #expect(v.kind == .direct && v.mark == .online && v.word == L10n.transportStatusDirect() && v.sub == nil)
        #expect(v.speeds.map(\.value) == [L10n.transportFileSpeedBoth(direct: "6.3", relay: "4.1"), L10n.transportReadLatencyBoth(direct: "48", relay: "175")])
        #expect(DirectView.make(status(.connected, file: true)).sub == L10n.transportUseFile())
        #expect(DirectView.make(status(.connected, read: true)).sub == L10n.transportUseRead())
        #expect(DirectView.make(status(.connected, read: true)).summary == "\(L10n.transportStatusDirect()) · \(L10n.transportUseRead())")
    }

    @Test func measuringShowsASpinnerAndNoSecondLine() {
        let v = DirectView.make(status(.connected, warming: true, reads: [DirectPath.relay: 217]))
        #expect(v.kind == .measuring && v.mark == .run && v.word == L10n.transportStatusMeasuring() && v.sub == nil)
        #expect(v.speeds.map(\.value) == [L10n.transportFileSpeedPending(), L10n.transportReadLatencyRelay(relay: "217")], "a file not yet measured says so, and a path not measured is never made up")
    }

    @Test func relayHeadlineExplainsSlowFallbackOrPendingChannel() {
        let slow = DirectView.make(status(.connected, compared: true, reads: [DirectPath.direct: 220, DirectPath.relay: 180]))
        #expect(slow.kind == .relay && slow.mark == .off && slow.sub == L10n.transportDirectSlow())
        let sparse = DirectView.make(status(.connected, compared: false, reads: [DirectPath.direct: 160, DirectPath.relay: 175]))
        #expect(sparse.word == L10n.transportStatusRelay() && sparse.sub == L10n.transportReasonChannelPending())
        #expect(sparse.speeds.count == 2, "kept numbers stay in the rows")
    }

    @Test func tryingUnavailableExplicitReasonsAndOff() {
        #expect(DirectView.make(status(.connecting)).kind == .trying && DirectView.make(status(.connecting)).word == L10n.transportStatusConnecting())
        let none = DirectView.make(status(.none, reason: .noTraversal))
        #expect(none.kind == .relay && none.mark == .off && none.sub == L10n.transportReasonNoTraversal() && none.showDiagnosticsEntry)
        #expect(none.summary == "\(L10n.transportStatusRelay()) · \(L10n.transportReasonNoTraversal())")
        #expect(DirectView.make(status(.none, reason: nil)).sub == L10n.transportReasonStarting())
        #expect(DirectView.make(status(.none, reason: .securityPending)).sub == L10n.transportReasonSecurityPending())
        #expect(DirectView.make(status(.none, reason: .atCap)).sub == L10n.transportReasonAtCap())
        let off = DirectView.make(status(.none, reason: .disabled))
        #expect(off.kind == .off && off.summary == L10n.transportSummaryOff())
        #expect(DirectView.trying().mark == .run)
    }

    @Test func connCheckRowCarriesStatusReasonAndMetric() {
        let row = DirectView.connCheckRow(status(.connected, read: true, reads: [DirectPath.direct: 14]))
        #expect(row.visible && row.mark == .online && row.label == L10n.transportDirect() && row.status == L10n.transportStatusDirect() && row.metric == L10n.transportReadLatencyDirect(direct: "14"))
        let offline = DirectView.connCheckRow(status(.connected, read: true), online: false)
        #expect(!offline.visible)
    }

    @Test func numbersFollowTheWebsRounding() {
        #expect(DirectView.mibps(99.96) == "100" && DirectView.mibps(6.34) == "6.3" && DirectView.mibps(120.4) == "120")
        #expect(DirectView.ms(47.6) == "48")
    }
}
