import XCTest
import notify

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

    func testMenusForAvailableDownloadingDownloadedAndOfflineCards() throws {
        continueAfterFailure = false
        let app = launch("f-avail=available,f-dl=downloadingEta,f-done=downloaded,f-off=offline", route: "sample-files-states")
        defer { app.terminate() }
        let list = app.descendants(matching: .any).matching(identifier: "chat-list").firstMatch
        XCTAssertTrue(list.present(timeout: 15))
        // Start at the first card using the existing DEBUG navigation signal.
        // Opening all four menus below remains a real long press.
        XCTAssertEqual(notify_post("io.github.lewin671.remoteagent.debug.session.top"), 0)
        try XCTContext.runActivity(named: "Available card menu") { _ in try checkAvailable(app) }
        dismissMenu(app)
        try XCTContext.runActivity(named: "Downloading card menu") { _ in try checkDownloading(app) }
        dismissMenu(app)
        try XCTContext.runActivity(named: "Downloaded card menu") { _ in try checkDownloaded(app) }
        try XCTContext.runActivity(named: "Offline card menu") { _ in try checkOffline(app) }
        dismissMenu(app)
    }

    private func reveal(_ card: XCUIElement, in app: XCUIApplication) {
        let list = app.descendants(matching: .any).matching(identifier: "chat-list").firstMatch
        // A bounded traversal of the six-card fixture, not a timed wait.
        for _ in 0..<4 {
            if card.exists && card.isHittable { return }
            list.swipeUp()
        }
        XCTAssertTrue(card.isHittable)
    }

    private func dismissMenu(_ app: XCUIApplication) {
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.1)).tap()
        XCTAssertTrue(app.staticTexts["来自 MacBook"].absent(timeout: 5))
    }

    private func checkDownloaded(_ app: XCUIApplication) throws {
        let card = card(app, "daocheng-final.mp4，视频，24.3 MB，已下载")
        reveal(card, in: app)
        XCTAssertTrue(card.present(timeout: 15))
        XCTAssertTrue(card.isHittable)
        // The control reads as the action it does.
        XCTAssertTrue(app.descendants(matching: .any).matching(NSPredicate(format: "label == %@", "打开")).firstMatch.exists)
        card.press(forDuration: 0.8)
        for item in ["存到相册", "存到「文件」", "分享…", "删除本机副本"] {
            XCTAssertTrue(app.buttons[item].present(timeout: 5), item)
        }
        XCTAssertTrue(app.staticTexts["来自 MacBook"].exists)
        XCTAssertTrue(app.staticTexts["~/Movies/daocheng/out/daocheng-final.mp4"].exists)
        let shot = XCTAttachment(screenshot: app.screenshot())
        shot.name = "shared-file-menu-downloaded"
        shot.lifetime = .keepAlways
        add(shot)
        // A tap outside takes the menu down.
        dismissMenu(app)
        XCTAssertTrue(app.buttons["删除本机副本"].absent(timeout: 5))
    }

    private func checkAvailable(_ app: XCUIApplication) throws {
        let card = card(app, "daocheng-final.mp4，视频，24.3 MB，可下载")
        reveal(card, in: app)
        XCTAssertTrue(card.present(timeout: 15))
        let downloads = { app.buttons.matching(NSPredicate(format: "label == %@", "下载")).count }
        let before = downloads()
        card.press(forDuration: 0.8)
        XCTAssertTrue(app.staticTexts["来自 MacBook"].present(timeout: 5))
        // Its one item is 下载 (besides the card's own control).
        XCTAssertEqual(downloads(), before + 1)
        XCTAssertFalse(app.buttons["存到相册"].exists)
        XCTAssertFalse(app.buttons["删除本机副本"].exists)
    }

    private func checkDownloading(_ app: XCUIApplication) throws {
        let card = card(app, "daocheng-final.mp4，视频，24.3 MB，下载中")
        reveal(card, in: app)
        XCTAssertTrue(card.present(timeout: 15))
        card.press(forDuration: 0.8)
        XCTAssertTrue(app.buttons["暂停"].present(timeout: 5))
        XCTAssertTrue(app.buttons["取消下载"].exists)
    }

    private func checkOffline(_ app: XCUIApplication) throws {
        let card = card(app, "daocheng-final.mp4，视频，24.3 MB，电脑不在线")
        reveal(card, in: app)
        XCTAssertTrue(card.present(timeout: 15))
        let downloads = { app.buttons.matching(NSPredicate(format: "label == %@", "下载")).count }
        let before = downloads()
        card.press(forDuration: 0.8)
        XCTAssertTrue(app.staticTexts["来自 MacBook"].present(timeout: 5))
        XCTAssertEqual(downloads(), before)
        XCTAssertFalse(app.buttons["删除本机副本"].exists)
    }
}
