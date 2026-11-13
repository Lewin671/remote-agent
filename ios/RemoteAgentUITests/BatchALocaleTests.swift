import XCTest

/// Real controls on isolated demo data. No server, account, or CLI is started.
@MainActor
final class BatchALocaleTests: XCTestCase {
    func testModelAndCliScreensInBothLanguages() throws {
        for locale in ["zh-CN", "en"] {
            let app = XCUIApplication()
            app.launchArguments = ["-demo", "-newTask", "/Users/me/Code/foo", "-messageLocale", locale]
            app.launch()
            let cli = app.buttons["new-task-cli"]
            XCTAssertTrue(cli.waitForExistence(timeout: 15))
            // The demo inherits the latest session's CLI, which varies with the
            // clock. Choose the long Codex list instead of assuming that default.
            try pickCli("Codex", in: app)
            let model = app.buttons["new-task-model"]
            XCTAssertTrue(model.waitForExistence(timeout: 5))
            model.tap()
            let all = app.buttons["new-model-show-all"]
            XCTAssertTrue(all.waitForExistence(timeout: 10))
            try capture(app, "\(locale)-codex-models")
            all.tap()
            XCTAssertTrue(app.buttons["new-model-back"].waitForExistence(timeout: 5))
            try capture(app, "\(locale)-all-models")
            app.buttons[locale == "en" ? "Done" : "完成"].tap()
            XCTAssertTrue(app.buttons["new-model-back"].waitForNonExistence(timeout: 5))
            try pickCli("Claude Code", in: app, captureAs: "\(locale)-cli-menu")
            XCTAssertTrue(model.waitForExistence(timeout: 5))
            model.tap()
            let standard = app.buttons[locale == "en" ? "Standard 200k" : "标准 200k"]
            XCTAssertTrue(standard.waitForExistence(timeout: 10))
            standard.tap()
            XCTAssertTrue(standard.isSelected, "the localized label must select the stable standard value")
            try capture(app, "\(locale)-claude-window")
            app.buttons["1M"].tap()
            XCTAssertTrue(app.buttons["1M"].isSelected)
            app.terminate()
        }
    }

    private func pickCli(_ name: String, in app: XCUIApplication, captureAs screenshot: String? = nil) throws {
        let cli = app.buttons["new-task-cli"]
        cli.tap()
        let option = app.buttons[name]
        let ready = try XCTUnwrap(option.waitForExistence(timeout: 5) ? option : nil, "CLI menu must offer \(name)")
        if let screenshot { try capture(app, screenshot) }
        ready.tap()
        let selected = XCTNSPredicateExpectation(predicate: NSPredicate(format: "label ENDSWITH %@", name), object: cli)
        XCTAssertEqual(XCTWaiter.wait(for: [selected], timeout: 5), .completed, "CLI selection must settle before opening its model list")
    }

    private func capture(_ app: XCUIApplication, _ name: String) throws {
        let screenshot = app.screenshot()
        let attachment = XCTAttachment(screenshot: screenshot)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        let directory = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("i18n-batch-a")
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        try screenshot.pngRepresentation.write(to: directory.appendingPathComponent(name + ".png"))
    }
}

extension BatchALocaleTests {
    func testConnectionDetailsInBothLanguages() throws {
        for locale in ["zh-CN", "en"] {
            let app = XCUIApplication()
            app.launchArguments = ["-demo", "-demoLink", "noServer", "-messageLocale", locale]
            app.launch()
            let details = app.buttons[locale == "en" ? "Details" : "详情"]
            XCTAssertTrue(details.waitForExistence(timeout: 15))
            details.tap()
            XCTAssertTrue(app.buttons[locale == "en" ? "Retry now" : "重试"].waitForExistence(timeout: 5))
            try capture(app, "\(locale)-connection-details")
            app.terminate()
        }
    }
}
