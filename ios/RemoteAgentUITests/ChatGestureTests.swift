import XCTest
import notify

/// Real simulator touches against synthetic data only. No account, relay or
/// CLI is involved; protocol end-to-end acceptance is a separate workflow.
@MainActor
final class ChatGestureTests: XCTestCase {
    func testTallTranscriptGesturesAndTextSelection() throws {
        continueAfterFailure = false
        let app = XCUIApplication()
        app.launchArguments = ["-demo", "-demoRoute", "session/sample-boundary", "-debug.sessionText", "Draft stays in the composer".debugDescription]
        app.launch()
        defer { app.terminate() }
        let list = app.descendants(matching: .any).matching(identifier: "chat-list").firstMatch
        XCTAssertTrue(list.present(timeout: 15))
        // A downward drag reads back from the initially visible end. Fast
        // swipes include native tracking and the resulting deceleration.
        list.swipeDown(velocity: .fast)
        let pill = app.buttons["回到最新消息"]
        XCTAssertTrue(pill.present(timeout: 5))
        // That drag and this return cover both directions and deceleration.
        // The host's 240-offset stress test covers accumulated layout work.
        list.swipeUp(velocity: .fast)
        list.swipeDown(velocity: .fast)
        XCTAssertTrue(pill.present(timeout: 5))
        pill.tap()
        XCTAssertTrue(pill.absent(timeout: 5))
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "Tall transcript after native flings"
        shot.lifetime = .keepAlways
        add(shot)
        try checkCodeExpansionKeyboardDismissalAndTextSelection(app)
    }

    private func checkCodeExpansionKeyboardDismissalAndTextSelection(_ app: XCUIApplication) throws {
        let list = app.descendants(matching: .any).matching(identifier: "chat-list").firstMatch
        XCTAssertTrue(list.present(timeout: 15))
        let expand = list.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "展开全文")).firstMatch
        XCTAssertTrue(expand.present(timeout: 5))
        expand.tap()
        let collapse = list.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "收起")).firstMatch
        // The expanded code is taller than the viewport. Native scrolling
        // brings its collapse control into sight without replacing the row.
        for _ in 0..<5 where !collapse.isHittable { list.swipeUp() }
        XCTAssertTrue(collapse.present(timeout: 5))
        collapse.tap()
        let field = app.textFields["消息"]
        XCTAssertTrue(field.present(timeout: 5))
        XCTAssertEqual(notify_post("io.github.lewin671.remoteagent.debug.session.draft"), 0)
        field.tap()
        XCTAssertEqual(field.value as? String, "Draft stays in the composer")
        // A slow drag over the list exercises interactive dismissal, including
        // the KeyboardTrack lag path when a software keyboard is available.
        let start = list.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.25))
        XCTAssertTrue(app.keyboards.firstMatch.present(timeout: 5))
        let end = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.97))
        start.press(forDuration: 0.1, thenDragTo: end)
        XCTAssertTrue(app.keyboards.firstMatch.absent(timeout: 5))
        XCTAssertTrue(field.value as? String == "Draft stays in the composer")
        XCTAssertTrue(app.buttons["更多"].exists, "session controls remain available after the drag")
        let keyboardShot = XCTAttachment(screenshot: app.screenshot())
        keyboardShot.name = "Composer after interactive keyboard drag"
        keyboardShot.lifetime = .keepAlways
        add(keyboardShot)
        // Dragging leaves a variable reading offset. Prepare a known reply,
        // then long-press its actual text rather than a viewport fraction that
        // can land on a gap or a different kind of Markdown block.
        XCTAssertEqual(notify_post("io.github.lewin671.remoteagent.debug.session.top"), 0)
        let reply = list.staticTexts["A short reply."].firstMatch
        XCTAssertTrue(reply.present(timeout: 5))
        XCTAssertTrue(reply.isHittable)
        // The row's action still crosses the hosting boundary into ChatPane.
        reply.press(forDuration: 1)
        let select = app.buttons["选择文字"]
        XCTAssertTrue(select.present(timeout: 5))
        select.tap()
        let selectedText = app.textViews["消息文字"]
        XCTAssertTrue(selectedText.present(timeout: 5))
        XCTAssertEqual(selectedText.value as? String, "A short reply.")
        app.buttons["完成"].tap()
    }
}
