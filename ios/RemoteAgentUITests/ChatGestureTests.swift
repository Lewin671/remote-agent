import XCTest

/// Real simulator touches against synthetic data only. No account, relay or
/// CLI is involved; protocol end-to-end acceptance is a separate workflow.
@MainActor
final class ChatGestureTests: XCTestCase {
    func testComposerMenusKeepKeyboardAndPosition() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-demo", "-demoRoute", "session/sample-queue", "-fileUploads"]
        app.launch()
        let field = app.textFields["消息"]
        XCTAssertTrue(field.waitForExistence(timeout: 15))
        field.tap()
        field.typeText("Menu keyboard check")
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5))
        let top = field.frame.minY
        func check(_ name: String) {
            XCTAssertTrue(app.keyboards.firstMatch.exists, name)
            XCTAssertEqual(field.frame.minY, top, accuracy: 0.5, name)
            let shot = XCTAttachment(screenshot: app.screenshot())
            shot.name = name
            shot.lifetime = .keepAlways
            add(shot)
            print("COMPOSER_MENU \(name) top=\(field.frame.minY) baseline=\(top)")
        }
        check("ios-baseline")
        app.buttons["添加附件"].tap()
        XCTAssertTrue(app.buttons["照片和视频"].waitForExistence(timeout: 5))
        check("ios-attach-open")
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.2)).tap()
        XCTAssertTrue(app.buttons["照片和视频"].waitForNonExistence(timeout: 5))
        check("ios-attach-closed")
        app.buttons["发送"].firstMatch.press(forDuration: 1)
        XCTAssertTrue(app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "这一轮结束后再发")).firstMatch.waitForExistence(timeout: 5))
        check("ios-send-open")
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.2)).tap()
        XCTAssertTrue(app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "这一轮结束后再发")).firstMatch.waitForNonExistence(timeout: 5))
        check("ios-send-closed")
    }

    func testTallTranscriptDragFlingAndJump() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-demo", "-demoRoute", "session/sample-boundary"]
        app.launch()
        let list = app.descendants(matching: .any).matching(identifier: "chat-list").firstMatch
        XCTAssertTrue(list.waitForExistence(timeout: 15))
        // A downward drag reads back from the initially visible end. Fast
        // swipes include native tracking and the resulting deceleration.
        list.swipeDown(velocity: .fast)
        let pill = app.buttons["回到最新消息"]
        XCTAssertTrue(pill.waitForExistence(timeout: 5))
        for _ in 0..<12 {
            list.swipeDown(velocity: .fast)
            list.swipeUp(velocity: .fast)
        }
        list.swipeDown(velocity: .fast)
        XCTAssertTrue(pill.waitForExistence(timeout: 5))
        pill.tap()
        XCTAssertTrue(pill.waitForNonExistence(timeout: 5))
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "Tall transcript after native flings"
        shot.lifetime = .keepAlways
        add(shot)
    }

    func testCodeExpansionKeyboardDismissalAndTextSelection() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-demo", "-demoRoute", "session/sample-boundary"]
        app.launch()
        let list = app.descendants(matching: .any).matching(identifier: "chat-list").firstMatch
        XCTAssertTrue(list.waitForExistence(timeout: 15))
        let expand = list.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "展开全文")).firstMatch
        XCTAssertTrue(expand.waitForExistence(timeout: 5))
        expand.tap()
        let collapse = list.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "收起")).firstMatch
        // The expanded code is taller than the viewport. Native scrolling
        // brings its collapse control into sight without replacing the row.
        for _ in 0..<5 where !collapse.isHittable { list.swipeUp() }
        XCTAssertTrue(collapse.waitForExistence(timeout: 5))
        collapse.tap()
        let field = app.textFields["消息"]
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        field.tap()
        field.typeText("Draft stays in the composer")
        // A slow drag over the list exercises interactive dismissal, including
        // the KeyboardTrack lag path when a software keyboard is available.
        let start = list.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.25))
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5))
        let end = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.97))
        start.press(forDuration: 0.1, thenDragTo: end)
        XCTAssertTrue(app.keyboards.firstMatch.waitForNonExistence(timeout: 5))
        XCTAssertTrue(field.value as? String == "Draft stays in the composer")
        XCTAssertTrue(app.buttons["更多"].exists, "session controls remain available after the drag")
        let keyboardShot = XCTAttachment(screenshot: app.screenshot())
        keyboardShot.name = "Composer after interactive keyboard drag"
        keyboardShot.lifetime = .keepAlways
        add(keyboardShot)
        // A row's existing long-press action must cross the hosting boundary
        // back into ChatPane's selection sheet.
        list.coordinate(withNormalizedOffset: CGVector(dx: 0.3, dy: 0.4)).press(forDuration: 1)
        let select = app.buttons["选择文字"]
        XCTAssertTrue(select.waitForExistence(timeout: 5))
        select.tap()
        XCTAssertTrue(app.textViews["消息文字"].waitForExistence(timeout: 5))
        app.buttons["完成"].tap()
    }
}
