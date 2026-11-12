import XCTest

/// Simulator touches on demo data: verifies delivery to the composer action,
/// not delivery to a daemon or CLI.
@MainActor
final class ComposerHitTests: XCTestCase {
    private struct Observation: Encodable {
        let scenario: String
        let repetition: Int
        let point: String
        let frame: [Double]
        let longPress: Bool?
        let tap: Bool
    }

    private func sendFrame(_ app: XCUIApplication) throws -> CGRect {
        // The invisible Command-Return shortcut can also appear in the AX
        // tree on multiline drafts. Touch the rightmost visible control.
        // Resolve frames, not an index-bound XCUIElement: the shortcut can
        // enter the tree between snapshots and shift that element's index.
        var frame: CGRect?
        let ready = XCTNSPredicateExpectation(predicate: NSPredicate { _, _ in
            frame = app.buttons.matching(identifier: "发送").allElementsBoundByIndex.map(\.frame)
                .filter { !$0.isEmpty && $0.minX.isFinite && $0.minY.isFinite && $0.midX > app.frame.width * 0.75 }
                .max { $0.midX < $1.midX }
            return frame != nil
        }, object: nil)
        XCTAssertEqual(XCTWaiter.wait(for: [ready], timeout: 5), .completed)
        return try XCTUnwrap(frame)
    }

    private func point(_ app: XCUIApplication, _ frame: CGRect, _ x: CGFloat, _ y: CGFloat) -> XCUICoordinate {
        app.coordinate(withNormalizedOffset: .zero)
            .withOffset(CGVector(dx: frame.minX + frame.width * x, dy: frame.minY + frame.height * y))
    }

    private func record(_ observations: [Observation], name: String) throws {
        let data = try JSONEncoder().encode(observations)
        let attachment = XCTAttachment(data: data, uniformTypeIdentifier: "public.json")
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
    }

    func testSendAndQueueAtCenterAndCornersAcrossKeyboardChanges() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-demo", "-demoRoute", "session/sample-queue", "-messageLocale", "zh-CN"]
        app.launch()
        defer { app.terminate() }
        let field = app.textFields["消息"]
        XCTAssertTrue(field.waitForExistence(timeout: 15))
        let points: [(String, CGFloat, CGFloat)] = [
            ("center", 0.5, 0.5), ("top-left", 0.01, 0.01),
            ("top-right", 0.99, 0.01), ("bottom-left", 0.01, 0.99), ("bottom-right", 0.99, 0.99),
        ]
        let queue = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "这一轮结束后再发")).firstMatch
        let list = app.descendants(matching: .any).matching(identifier: "chat-list").firstMatch
        var observations: [Observation] = []
        defer { try? record(observations, name: "running-send-hit-results") }
        func dismissKeyboard() {
            let start = list.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.2))
            start.press(forDuration: 0.1, thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.97)))
            XCTAssertTrue(app.keyboards.firstMatch.waitForNonExistence(timeout: 5))
        }
        for scenario in ["short", "multiline", "dismissed", "reopened"] {
            let draft = scenario == "short" ? "Hit check" : (1...8).map { "Line \($0) with a wrapped draft" }.joined(separator: "\n")
            for repetition in 0..<2 {
                for (name, x, y) in points {
                    field.tap()
                    field.typeText(draft)
                    XCTAssertEqual(field.value as? String, draft)
                    XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5))
                    if scenario == "dismissed" || scenario == "reopened" { dismissKeyboard() }
                    if scenario == "reopened" {
                        field.tap()
                        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5))
                    }
                    let frame = try sendFrame(app)
                    XCTAssertGreaterThanOrEqual(frame.width, 44)
                    XCTAssertGreaterThanOrEqual(frame.height, 44)
                    let point = point(app, frame, x, y)
                    // Hold at 0.44 pt from both edges: outside the scaled
                    // 42.24 pt rectangle if press feedback shrinks the target.
                    point.press(forDuration: 1)
                    let opened = queue.waitForExistence(timeout: 3)
                    XCTAssertTrue(opened, "\(scenario) \(name) long press")
                    print("COMPOSER_HIT scenario=\(scenario) repeat=\(repetition) point=\(name) frame=\(frame) longPress=\(opened)")
                    if opened {
                        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.15)).tap()
                        XCTAssertTrue(queue.waitForNonExistence(timeout: 3))
                    }
                    self.point(app, try sendFrame(app), x, y).tap()
                    let sent = XCTNSPredicateExpectation(predicate: NSPredicate(format: "value != %@", draft), object: field)
                    let delivered = XCTWaiter.wait(for: [sent], timeout: 3) == .completed
                    XCTAssertTrue(delivered, "\(scenario) \(name) tap clears draft")
                    print("COMPOSER_HIT scenario=\(scenario) repeat=\(repetition) point=\(name) tap=\(delivered)")
                    observations.append(Observation(scenario: scenario, repetition: repetition, point: name,
                                                    frame: [frame.minX, frame.minY, frame.width, frame.height],
                                                    longPress: opened, tap: delivered))
                    if !delivered { return }
                }
            }
            let shot = XCTAttachment(screenshot: app.screenshot())
            shot.name = "composer-\(scenario)"
            shot.lifetime = .keepAlways
            add(shot)
        }
    }

    func testIdleSendAtCenterAndCorners() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-demo", "-demoRoute", "session/sample-claude", "-messageLocale", "zh-CN"]
        app.launch()
        defer { app.terminate() }
        let field = app.textFields["消息"]
        XCTAssertTrue(field.waitForExistence(timeout: 15))
        var observations: [Observation] = []
        defer { try? record(observations, name: "idle-send-hit-results") }
        for repetition in 0..<3 {
            for (name, x, y) in [("center", 0.5, 0.5), ("top-left", 0.01, 0.01), ("top-right", 0.99, 0.01),
                                  ("bottom-left", 0.01, 0.99), ("bottom-right", 0.99, 0.99)] {
                let draft = repetition == 0 ? "Idle hit check" : (1...8).map { "Line \($0) of the idle draft" }.joined(separator: "\n")
                field.tap()
                field.typeText(draft)
                XCTAssertEqual(field.value as? String, draft)
                let frame = try sendFrame(app)
                XCTAssertGreaterThanOrEqual(frame.width, 44)
                XCTAssertGreaterThanOrEqual(frame.height, 44)
                point(app, frame, x, y).tap()
                let sent = XCTNSPredicateExpectation(predicate: NSPredicate(format: "value != %@", draft), object: field)
                let delivered = XCTWaiter.wait(for: [sent], timeout: 3) == .completed
                XCTAssertTrue(delivered, "idle \(name) tap")
                observations.append(Observation(scenario: repetition == 0 ? "short" : "multiline", repetition: repetition,
                                                point: name, frame: [frame.minX, frame.minY, frame.width, frame.height],
                                                longPress: nil, tap: delivered))
                if !delivered { return }
            }
        }
    }
}
