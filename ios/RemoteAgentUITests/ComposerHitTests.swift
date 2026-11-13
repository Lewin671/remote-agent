import XCTest
import notify

/// Simulator touches on demo data: verifies delivery to the composer action,
/// not delivery to a daemon or CLI. Draft setup uses the existing DEBUG signal.
@MainActor
final class ComposerHitTests: XCTestCase {
    private enum Scenario: String {
        case short, multiline, dismissed
        var draft: String {
            self == .short ? "Hit check" : (1...8).map { "Line \($0) with a wrapped draft" }.joined(separator: "\n")
        }
    }

    private enum HitPoint: String, CaseIterable {
        case center, bottomRight = "bottom-right"
        var offset: (CGFloat, CGFloat) {
            self == .center ? (0.5, 0.5) : (0.99, 0.99)
        }
    }

    private struct Observation: Encodable {
        let scenario: String
        let point: String
        let frame: [Double]
        let longPress: Bool?
        let tap: Bool
    }

    private func present(_ element: XCUIElement, timeout: TimeInterval) -> Bool {
        element.exists || element.waitForExistence(timeout: timeout)
    }

    private func absent(_ element: XCUIElement, timeout: TimeInterval) -> Bool {
        !element.exists || element.waitForNonExistence(timeout: timeout)
    }

    private func sameFrame(_ a: CGRect, _ b: CGRect) -> Bool {
        abs(a.minX - b.minX) < 0.1 && abs(a.minY - b.minY) < 0.1 &&
        abs(a.width - b.width) < 0.1 && abs(a.height - b.height) < 0.1
    }

    private func sendFrame(_ app: XCUIApplication, keyboardShown: Bool, expected: CGRect? = nil) throws -> CGRect {
        let buttons = app.buttons.matching(identifier: "session-send")
        let screen = app.frame
        func snapshot() -> CGRect? {
            let keyboard = app.keyboards.firstMatch
            guard keyboardShown == keyboard.exists else { return nil }
            let frame = buttons.allElementsBoundByAccessibilityElement.map(\.frame)
                .filter { !$0.isEmpty && $0.minX.isFinite && $0.minY.isFinite && $0.midX > screen.width * 0.75 }
                .max { $0.midX < $1.midX }
            guard let frame else { return nil }
            if keyboardShown {
                let key = keyboard.frame
                guard !key.isEmpty, key.minY < screen.maxY, frame.maxY <= key.minY else { return nil }
            }
            return frame
        }
        // Reuse only geometry just verified against a known settled frame and
        // the current keyboard. A changed frame still takes the stable path.
        if let expected, let frame = snapshot(), sameFrame(expected, frame) { return frame }
        var previous: CGRect?
        var settled: CGRect?
        let ready = XCTNSPredicateExpectation(predicate: NSPredicate { _, _ in
            guard let frame = snapshot() else { previous = nil; return false }
            defer { previous = frame }
            guard let previous, self.sameFrame(previous, frame) else { return false }
            settled = frame
            return true
        }, object: nil)
        XCTAssertEqual(XCTWaiter.wait(for: [ready], timeout: 5), .completed)
        return try XCTUnwrap(settled)
    }

    private func draftIsCleared(_ field: XCUIElement) -> Bool {
        guard field.exists else { return false }
        let value = field.value as? String ?? ""
        return value.isEmpty || value == field.placeholderValue
    }

    private func point(_ app: XCUIApplication, _ frame: CGRect, _ hit: HitPoint) -> XCUICoordinate {
        let (x, y) = hit.offset
        return app.coordinate(withNormalizedOffset: .zero)
            .withOffset(CGVector(dx: frame.minX + frame.width * x, dy: frame.minY + frame.height * y))
    }

    private func record(_ observations: [Observation], name: String) throws {
        let data = try JSONEncoder().encode(observations)
        let attachment = XCTAttachment(data: data, uniformTypeIdentifier: "public.json")
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
    }

    private func fill(_ app: XCUIApplication, _ field: XCUIElement, _ draft: String) {
        // Prepare the fixture through the existing DEBUG signal. Native taps,
        // long presses and keyboard drags below still exercise the real UI.
        XCTAssertEqual(notify_post("io.github.lewin671.remoteagent.debug.session.draft"), 0)
        field.tap()
        XCTAssertEqual(field.value as? String, draft)
    }

    func testRunningSendHitAreaWithShortDraft() throws { try running(.short) }
    func testRunningSendHitAreaWithMultilineDraft() throws { try running(.multiline) }
    func testRunningSendHitAreaAfterDismissingKeyboard() throws { try running(.dismissed) }

    private func running(_ scenario: Scenario, points: [HitPoint] = HitPoint.allCases) throws {
        continueAfterFailure = false
        let draft = scenario.draft
        let app = XCUIApplication()
        app.launchArguments = ["-demo", "-demoRoute", "session/sample-queue", "-messageLocale", "zh-CN", "-debug.sessionText", draft.debugDescription]
        app.launch()
        defer { app.terminate() }
        let field = app.textFields["消息"]
        XCTAssertTrue(field.waitForExistence(timeout: 15))
        let queue = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "这一轮结束后再发")).firstMatch
        let list = app.descendants(matching: .any).matching(identifier: "chat-list").firstMatch
        var observations: [Observation] = []
        defer { try? record(observations, name: "running-send-hit-\(scenario.rawValue)") }
        var settledFrame: CGRect?
        for hit in points {
            fill(app, field, draft)
            XCTAssertTrue(present(app.keyboards.firstMatch, timeout: 5))
            if scenario == .dismissed {
                let start = list.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.2))
                start.press(forDuration: 0.1, thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.97)))
                XCTAssertTrue(absent(app.keyboards.firstMatch, timeout: 5))
            }
            let frame = try sendFrame(app, keyboardShown: scenario != .dismissed, expected: settledFrame)
            settledFrame = frame
            XCTAssertGreaterThanOrEqual(frame.width, 44)
            XCTAssertGreaterThanOrEqual(frame.height, 44)
            // 0.44 pt from an edge still lies outside a scaled 42.24 pt target.
            point(app, frame, hit).press(forDuration: 1)
            let opened = present(queue, timeout: 3)
            if !opened {
                observations.append(Observation(scenario: scenario.rawValue, point: hit.rawValue,
                    frame: [frame.minX, frame.minY, frame.width, frame.height], longPress: false, tap: false))
            }
            XCTAssertTrue(opened, "\(scenario.rawValue) \(hit.rawValue) long press")
            app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.15)).tap()
            XCTAssertTrue(absent(queue, timeout: 3))
            point(app, try sendFrame(app, keyboardShown: scenario != .dismissed, expected: frame), hit).tap()
            // Text-only sending clears synchronously before its RPC. The
            // native tap has already waited for the application to settle.
            let sent = draftIsCleared(field)
            observations.append(Observation(scenario: scenario.rawValue, point: hit.rawValue,
                frame: [frame.minX, frame.minY, frame.width, frame.height], longPress: opened, tap: sent))
            XCTAssertTrue(sent, "\(scenario.rawValue) \(hit.rawValue) tap clears draft")
        }
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "composer-\(scenario.rawValue)"
        shot.lifetime = .keepAlways
        add(shot)
    }

    func testIdleSendHitAreaWithShortDraft() throws { try idle() }

    private func idle(points: [HitPoint] = HitPoint.allCases) throws {
        continueAfterFailure = false
        let draft = "Idle hit check"
        let app = XCUIApplication()
        app.launchArguments = ["-demo", "-demoRoute", "session/sample-claude", "-messageLocale", "zh-CN", "-debug.sessionText", draft.debugDescription]
        app.launch()
        defer { app.terminate() }
        let field = app.textFields["消息"]
        XCTAssertTrue(field.waitForExistence(timeout: 15))
        var observations: [Observation] = []
        defer { try? record(observations, name: "idle-send-hit-results") }
        var settledFrame: CGRect?
        for hit in points {
            fill(app, field, draft)
            let frame = try sendFrame(app, keyboardShown: true, expected: settledFrame)
            settledFrame = frame
            XCTAssertGreaterThanOrEqual(frame.width, 44)
            XCTAssertGreaterThanOrEqual(frame.height, 44)
            point(app, frame, hit).tap()
            let sent = draftIsCleared(field)
            observations.append(Observation(scenario: "short", point: hit.rawValue,
                frame: [frame.minX, frame.minY, frame.width, frame.height], longPress: nil, tap: sent))
            XCTAssertTrue(sent, "idle \(hit.rawValue) tap")
        }
    }
}
