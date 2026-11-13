import XCTest

/// Real touches on 电脑状态 of the sample data: the three rings open it, a press on the curve is the curve's and
/// does not scroll the page, and a tap on the memory card makes it the main chart.
@MainActor
final class MachineStatusGestureTests: XCTestCase {
    private func launch(_ route: String, _ extra: [String] = []) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-demo", "-demoRoute", route, "-messageLocale", "zh-CN", "-machineFrozen"] + extra
        app.launch()
        return app
    }

    private func any(_ app: XCUIApplication, _ id: String) -> XCUIElement { app.descendants(matching: .any)[id].firstMatch }

    func testTheRingsReadAsTheirFiguresAndOpenTheStatusPage() throws {
        let app = launch("machine/mac")
        for label in ["CPU，23%", "内存，70%", "磁盘可用，42 GB"] {
            XCTAssertTrue(app.descendants(matching: .any).matching(NSPredicate(format: "label == %@", label)).firstMatch.waitForExistence(timeout: 20), label)
        }
        app.descendants(matching: .any).matching(NSPredicate(format: "label == %@", "CPU，23%")).firstMatch.tap()
        XCTAssertTrue(any(app, "status-chart").waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["占用最多"].exists)
    }

    func testAPressOnTheCurveDoesNotScrollThePageAndLetsGoBackToLive() throws {
        let app = launch("machine-status/mac")
        let chart = any(app, "status-chart")
        XCTAssertTrue(chart.waitForExistence(timeout: 20))
        let title = app.staticTexts["MacBook"].firstMatch
        let before = title.frame
        // From the curve, a drag that is mostly downwards: the curve takes the touch, the page stays.
        chart.coordinate(withNormalizedOffset: CGVector(dx: 0.4, dy: 0.5))
            .press(forDuration: 0.4, thenDragTo: chart.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 3)))
        XCTAssertEqual(title.frame.minY, before.minY, accuracy: 1)
        XCTAssertTrue(app.staticTexts["实时"].waitForExistence(timeout: 5))
        XCTAssertTrue(chart.label.hasPrefix("最近 2 分钟，最高 "))
    }

    func testTheMemoryCardTakesTheMainChartAndTheCpuCardTakesItBack() throws {
        let app = launch("machine-status/mac")
        let memory = any(app, "status-memory-card")
        XCTAssertTrue(memory.waitForExistence(timeout: 20))
        XCTAssertFalse(any(app, "status-cpu-card").exists)
        memory.tap()
        let cpu = any(app, "status-cpu-card")
        XCTAssertTrue(cpu.waitForExistence(timeout: 5))
        let gone = NSPredicate(format: "exists == false")
        expectation(for: gone, evaluatedWith: any(app, "status-memory-card"))
        waitForExpectations(timeout: 5)
        XCTAssertTrue(app.staticTexts["压力正常"].exists)
        cpu.tap()
        XCTAssertTrue(any(app, "status-memory-card").waitForExistence(timeout: 5))
    }
}
