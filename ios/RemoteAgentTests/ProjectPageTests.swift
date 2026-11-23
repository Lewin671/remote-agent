import Foundation
import Testing

@testable import RemoteAgent

/// Layout rules for the project and new-task sheets.
@MainActor
@Suite struct ProjectPageTests {
    @Test func modelControlsPinOnlyWithRoomForTwoRowsAndPadding() {
        #expect(shouldPinModelControls(bodyHeight: 228, controlsHeight: 132))
        #expect(!shouldPinModelControls(bodyHeight: 227, controlsHeight: 132))
        #expect(!shouldPinModelControls(bodyHeight: 228, controlsHeight: 132, rowHeight: 52))
        #expect(!shouldPinModelControls(bodyHeight: 228, controlsHeight: 132, rowHeight: 0))
    }

    @Test func modelToolWidthsDropCLINameBeforeTruncatingModel() {
        let full = NewTaskToolsLayout.widths(available: 300, cli: 100, compactCLI: 36, model: 180)
        #expect(full.cli == 100 && full.model == 180)
        let compact = NewTaskToolsLayout.widths(available: 250, cli: 100, compactCLI: 36, model: 180)
        #expect(compact.cli == 36 && compact.model == 180)
        let ellipsized = NewTaskToolsLayout.widths(available: 200, cli: 100, compactCLI: 36, model: 180)
        #expect(ellipsized.cli == 36 && ellipsized.model == 162)
        let single = NewTaskToolsLayout.widths(available: 200, cli: 0, compactCLI: 0, model: 280)
        #expect(single.cli == 0 && single.model == 200)
    }
}
