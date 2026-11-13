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

    func testRingsNavigationCurveGestureAndMemorySwap() throws {
        continueAfterFailure = false
        let app = launch("machine/mac")
        defer { app.terminate() }
        for label in ["CPU，23%", "内存，70%", "磁盘可用，42 GB"] {
            XCTAssertTrue(app.descendants(matching: .any).matching(NSPredicate(format: "label == %@", label)).firstMatch.present(timeout: 20), label)
        }
        app.descendants(matching: .any).matching(NSPredicate(format: "label == %@", "CPU，23%")).firstMatch.tap()
        XCTAssertTrue(any(app, "status-chart").present(timeout: 10))
        XCTAssertTrue(app.staticTexts["占用最多"].exists)
        try XCTContext.runActivity(named: "Curve gesture keeps the page still") { _ in try checkCurveGesture(app) }
        try XCTContext.runActivity(named: "Memory and CPU swap the main chart") { _ in try checkMemorySwap(app) }
    }

    private func checkCurveGesture(_ app: XCUIApplication) throws {
        let chart = any(app, "status-chart")
        XCTAssertTrue(chart.present(timeout: 20))
        let title = app.staticTexts["MacBook"].firstMatch
        let before = title.frame
        // From the curve, a drag that is mostly downwards: the curve takes the touch, the page stays.
        chart.coordinate(withNormalizedOffset: CGVector(dx: 0.4, dy: 0.5))
            .press(forDuration: 0.4, thenDragTo: chart.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 3)))
        XCTAssertEqual(title.frame.minY, before.minY, accuracy: 1)
        XCTAssertTrue(app.staticTexts["实时"].present(timeout: 5))
        XCTAssertTrue(chart.label.hasPrefix("最近 2 分钟，最高 "))
    }

    private func checkMemorySwap(_ app: XCUIApplication) throws {
        let memory = any(app, "status-memory-card")
        XCTAssertTrue(memory.present(timeout: 20))
        XCTAssertFalse(any(app, "status-cpu-card").exists)
        memory.tap()
        let cpu = any(app, "status-cpu-card")
        XCTAssertTrue(cpu.present(timeout: 5))
        XCTAssertTrue(any(app, "status-memory-card").absent(timeout: 5))
        XCTAssertTrue(app.staticTexts["压力正常"].exists)
        cpu.tap()
        XCTAssertTrue(any(app, "status-memory-card").present(timeout: 5))
    }
}
