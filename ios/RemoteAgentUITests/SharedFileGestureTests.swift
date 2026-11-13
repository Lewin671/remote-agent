import XCTest

/// A real long press on a file card of the sample data (no computer, no downloading): the menu of each state, and what
/// VoiceOver is given for the card and its control.
@MainActor
final class SharedFileGestureTests: XCTestCase {
    private func launch(_ cards: String, route: String = "sample-files", locale: String = "zh-CN") -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-demo", "-demoRoute", "session/" + route, "-messageLocale", locale, "-sharedFileCards", cards]
        app.launch()
        return app
    }

    private func card(_ app: XCUIApplication, _ label: String) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "label == %@", label)).firstMatch
    }

    func testLongPressOnADownloadedCardOffersTheFourActionsAndTheCardIsAButton() throws {
        let app = launch("f-video=downloaded")
        let card = card(app, "daocheng-final.mp4，视频，24.3 MB，已下载")
        XCTAssertTrue(card.waitForExistence(timeout: 15))
        XCTAssertTrue(card.isHittable)
        // The control reads as the action it does.
        XCTAssertTrue(app.descendants(matching: .any).matching(NSPredicate(format: "label == %@", "打开")).firstMatch.exists)
        card.press(forDuration: 0.8)
        for item in ["存到相册", "存到「文件」", "分享…", "删除本机副本"] {
            XCTAssertTrue(app.buttons[item].waitForExistence(timeout: 5), item)
        }
        XCTAssertTrue(app.staticTexts["来自 MacBook"].exists)
        XCTAssertTrue(app.staticTexts["~/Movies/daocheng/out/daocheng-final.mp4"].exists)
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "shared-file-menu-downloaded"
        shot.lifetime = .keepAlways
        add(shot)
        // A tap outside takes the menu down.
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.8, dy: 0.9)).tap()
        let gone = NSPredicate(format: "exists == false")
        expectation(for: gone, evaluatedWith: app.buttons["删除本机副本"])
        waitForExpectations(timeout: 5)
    }

    func testLongPressOnACardNotYetDownloadedOffersOnlyDownload() throws {
        let app = launch("f-video=available")
        let card = card(app, "daocheng-final.mp4，视频，24.3 MB，可下载")
        XCTAssertTrue(card.waitForExistence(timeout: 15))
        let downloads = { app.buttons.matching(NSPredicate(format: "label == %@", "下载")).count }
        let before = downloads()
        card.press(forDuration: 0.8)
        XCTAssertTrue(app.staticTexts["来自 MacBook"].waitForExistence(timeout: 5))
        // Its one item is 下载 (besides the card's own control).
        XCTAssertEqual(downloads(), before + 1)
        XCTAssertFalse(app.buttons["存到相册"].exists)
        XCTAssertFalse(app.buttons["删除本机副本"].exists)
    }

    func testDownloadingOffersPauseAndCancel() throws {
        let app = launch("f-video=downloadingEta")
        let card = card(app, "daocheng-final.mp4，视频，24.3 MB，下载中")
        XCTAssertTrue(card.waitForExistence(timeout: 15))
        card.press(forDuration: 0.8)
        XCTAssertTrue(app.buttons["暂停"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.buttons["取消下载"].exists)
    }

    func testAnAwayComputerShowsOnlyWhereTheFileCameFrom() throws {
        let app = launch("f-video=offline")
        let card = card(app, "daocheng-final.mp4，视频，24.3 MB，电脑不在线")
        XCTAssertTrue(card.waitForExistence(timeout: 15))
        let downloads = { app.buttons.matching(NSPredicate(format: "label == %@", "下载")).count }
        let before = downloads()
        card.press(forDuration: 0.8)
        XCTAssertTrue(app.staticTexts["来自 MacBook"].waitForExistence(timeout: 5))
        XCTAssertEqual(downloads(), before)
        XCTAssertFalse(app.buttons["删除本机副本"].exists)
    }
}
