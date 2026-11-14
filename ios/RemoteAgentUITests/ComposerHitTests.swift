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
        element.present(timeout: timeout)
    }

    private func absent(_ element: XCUIElement, timeout: TimeInterval) -> Bool {
        element.absent(timeout: timeout)
    }

    private func sameFrame(_ a: CGRect, _ b: CGRect) -> Bool {
        abs(a.minX - b.minX) < 0.1 && abs(a.minY - b.minY) < 0.1 &&
        abs(a.width - b.width) < 0.1 && abs(a.height - b.height) < 0.1
    }

    private func sendFrame(_ app: XCUIApplication, keyboardShown: Bool, expected: CGRect? = nil) throws -> CGRect {
        func flatten(_ node: any XCUIElementSnapshot) -> [any XCUIElementSnapshot] {
            [node] + node.children.flatMap(flatten)
        }
        func snapshot() -> CGRect? {
            // One AX snapshot keeps the keyboard and button geometry in the
            // same frame and avoids a remote query for every attribute.
            guard let tree = try? app.snapshot() else { return nil }
            let nodes = flatten(tree), screen = tree.frame
            let keyboard = nodes.first { $0.elementType == .keyboard }
            guard keyboardShown == (keyboard != nil) else { return nil }
            let frame = nodes.filter { $0.elementType == .button && $0.identifier == "session-send" }
                .map(\.frame)
                .filter { !$0.isEmpty && $0.minX.isFinite && $0.minY.isFinite && $0.midX > screen.width * 0.75 }
                .max { $0.midX < $1.midX }
            guard let frame else { return nil }
            if let keyboard {
                let key = keyboard.frame
                guard !key.isEmpty, key.minY < screen.maxY, frame.maxY <= key.minY else { return nil }
            }
            return frame
        }
        // Reuse only geometry just verified against a known settled frame and
        // the current keyboard. A changed frame still takes the stable path.
        if let expected, let frame = snapshot(), sameFrame(expected, frame) { return frame }
        // Seed the first sample now; the predicate's next sample still has
        // to match, so no initial one-second polling tick is wasted.
        var previous = snapshot()
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

    func testShortDraftImageTouchesMenusAndSendHitArea() throws { try running([.short]) }
    func testRunningSendHitAreaWithMultilineDraftAndDismissedKeyboard() throws {
        try running([.multiline, .dismissed])
    }

    private func running(_ scenarios: [Scenario], points: [HitPoint] = HitPoint.allCases) throws {
        continueAfterFailure = false
        let draft = try XCTUnwrap(scenarios.first).draft
        let app = XCUIApplication()
        app.launchArguments = ["-demo", "-demoRoute", "session/sample-queue", "-fileUploads", "-messageLocale", "zh-CN", "-debug.sessionText", draft.debugDescription]
        if scenarios.contains(.short) { app.launchArguments += ["-imageDraft", "ready"] }
        app.launch()
        defer { app.terminate() }
        if scenarios.contains(.short) {
            try XCTContext.runActivity(named: "Draft image geometry and touches") { _ in try checkDraftImageGeometryAndTouches(app) }
        }
        let field = app.textFields["消息"]
        XCTAssertTrue(field.present(timeout: 15))
        if scenarios.contains(.short) {
            try checkAttachmentMenu(app, draft: draft)
            clearDraftImages(app)
        }
        let queue = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "这一轮结束后再发")).firstMatch
        let list = app.descendants(matching: .any).matching(identifier: "chat-list").firstMatch
        // Both states use the same multiline fixture. Sending clears each
        // draft before the next state; keyboard dismissal remains a real drag.
        for scenario in scenarios {
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
                let composerTop = field.frame.minY
                point(app, frame, hit).press(forDuration: 1)
                let opened = present(queue, timeout: 3)
                if !opened {
                    observations.append(Observation(scenario: scenario.rawValue, point: hit.rawValue,
                        frame: [frame.minX, frame.minY, frame.width, frame.height], longPress: false, tap: false))
                }
                XCTAssertTrue(opened, "\(scenario.rawValue) \(hit.rawValue) long press")
                XCTAssertTrue(queue.isHittable)
                if hit == .center { capture(app, name: "ios-send-open") }
                app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.15)).tap()
                XCTAssertTrue(absent(queue, timeout: 3))
                if scenario == .short, hit == .center {
                    checkComposerRestored(app, draft: draft, top: composerTop, name: "ios-send-closed")
                }
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
    }

    private func capture(_ app: XCUIApplication, name: String) {
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = name
        shot.lifetime = .keepAlways
        add(shot)
    }

    private func checkComposerRestored(_ app: XCUIApplication, draft: String, top: CGFloat, name: String) {
        let field = app.textFields["消息"]
        capture(app, name: name)
        XCTAssertEqual(field.frame.minY, top, accuracy: 0.5, "\(name): composer must return after dismissal")
        XCTAssertTrue(app.keyboards.firstMatch.exists, name)
        XCTAssertEqual(field.value as? String, draft, "\(name): draft text must survive dismissal")
    }

    private func checkAttachmentMenu(_ app: XCUIApplication, draft: String) throws {
        let field = app.textFields["消息"]
        XCTAssertTrue(field.present(timeout: 15))
        fill(app, field, draft)
        XCTAssertTrue(app.keyboards.firstMatch.present(timeout: 5))
        let images = [app.otherElements["第 1 张，共 2 张"], app.otherElements["第 2 张，共 2 张"]]
        for image in images { XCTAssertTrue(image.exists) }
        let states = images.map { $0.value as? String }
        let top = field.frame.minY
        func checkRestored(_ name: String) {
            checkComposerRestored(app, draft: draft, top: top, name: name)
            for (image, state) in zip(images, states) {
                XCTAssertTrue(image.exists, "\(name): draft attachment must survive dismissal")
                XCTAssertEqual(image.value as? String, state, name)
            }
        }
        func openMenu() {
            app.buttons["添加附件"].tap()
            XCTAssertTrue(app.buttons["照片和视频"].present(timeout: 5))
            XCTAssertTrue(app.buttons["照片和视频"].isHittable)
            XCTAssertTrue(app.buttons["文件"].isHittable)
            // A system Menu covers the composer and can collapse the keyboard's
            // prediction strip (27 pt on iOS 27). Its open-state composer frame
            // is not a layout guarantee; check restoration only after dismissal.
        }
        capture(app, name: "ios-baseline")
        openMenu()
        capture(app, name: "ios-attach-open")
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.2)).tap()
        XCTAssertTrue(app.buttons["照片和视频"].absent(timeout: 5))
        checkRestored("ios-attach-closed")

        // Hit each native menu item and verify its destination, then cancel
        // without selecting anything: the existing draft must remain intact.
        openMenu()
        app.buttons["照片和视频"].tap()
        let photos = app.navigationBars["Photos"]
        XCTAssertTrue(photos.present(timeout: 10))
        capture(app, name: "ios-attach-photos-picker")
        app.buttons["Cancel"].tap()
        XCTAssertTrue(photos.absent(timeout: 5))
        checkRestored("ios-attach-photos-cancelled")

        openMenu()
        app.buttons["文件"].tap()
        let files = app.otherElements["Browse View (Picker)"]
        XCTAssertTrue(files.present(timeout: 10))
        capture(app, name: "ios-attach-files-picker")
        files.buttons["Cancel"].tap()
        XCTAssertTrue(files.absent(timeout: 5))
        checkRestored("ios-attach-files-cancelled")
    }

    private func checkDraftImageGeometryAndTouches(_ app: XCUIApplication) throws {
        let first = app.otherElements["第 1 张，共 3 张"]
        XCTAssertTrue(first.present(timeout: 15))
        let remove = app.buttons["移除图片 1"]
        let body = first.frame, hit = remove.frame
        let next = app.otherElements["第 2 张，共 3 张"].frame
        XCTAssertEqual(body.width, 56, accuracy: 0.5)
        XCTAssertEqual(hit.width, 44, accuracy: 0.5)
        XCTAssertEqual(hit.height, 44, accuracy: 0.5)
        XCTAssertEqual(hit.midX, body.maxX, accuracy: 0.5)
        XCTAssertEqual(hit.midY, body.minY, accuracy: 0.5)
        let overlap = body.intersection(hit)
        XCTAssertLessThanOrEqual(overlap.width * overlap.height, 22 * 22 + 1)
        XCTAssertFalse(hit.contains(CGPoint(x: body.midX, y: body.midY)))
        XCTAssertLessThanOrEqual(hit.maxX, next.minX)
        print("DRAFT_HIT body=\(body) remove=\(hit) next=\(next)")
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "ios-draft-images"
        shot.lifetime = .keepAlways
        add(shot)
        first.tap()
        let close = app.buttons["关闭"]
        XCTAssertTrue(close.present(timeout: 5))
        close.tap()
        XCTAssertTrue(first.present(timeout: 5))
        app.buttons["原图"].tap()
        // Reacquire after the original-size hint changes the composer height.
        first.tap()
        XCTAssertTrue(close.present(timeout: 5))
        close.tap()
        XCTAssertTrue(first.present(timeout: 5))
        // The upper-right part is outside the thumbnail and still removes it.
        remove.coordinate(withNormalizedOffset: CGVector(dx: 0.75, dy: 0.25)).tap()
        XCTAssertTrue(app.otherElements["第 1 张，共 2 张"].present(timeout: 5))
        // Keep both remaining images through the attachment menu checks.
    }

    private func clearDraftImages(_ app: XCUIApplication) {
        // Menu cancellation has verified these attachments survive. Clear the
        // fixture through DEBUG events before the text-only send hit matrix.
        XCTAssertEqual(notify_post("io.github.lewin671.remoteagent.debug.images.remove"), 0)
        let last = app.otherElements["第 1 张，共 1 张"]
        XCTAssertTrue(last.present(timeout: 5))
        XCTAssertEqual(notify_post("io.github.lewin671.remoteagent.debug.images.remove"), 0)
        XCTAssertTrue(last.absent(timeout: 5))
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
        XCTAssertTrue(field.present(timeout: 15))
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

/// Check the current accessibility state first. Native actions already wait
/// for the app to settle; only a missing transition needs XCTest's polling.
@MainActor
extension XCUIElement {
    func present(timeout: TimeInterval) -> Bool {
        exists || waitForExistence(timeout: timeout)
    }

    func absent(timeout: TimeInterval) -> Bool {
        !exists || waitForNonExistence(timeout: timeout)
    }
}
