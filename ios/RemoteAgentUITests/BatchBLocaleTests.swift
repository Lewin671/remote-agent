import XCTest
import notify

/// Simulator touches on isolated demo data; expected labels come from generated resources.
@MainActor
final class BatchBLocaleTests: XCTestCase {
    private func text(_ key: String, locale: String) throws -> String {
        let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        let catalog = root.appendingPathComponent("RemoteAgentCore/Localization/Localizable.xcstrings")
        let data = try Data(contentsOf: catalog)
        let json = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        let strings = try XCTUnwrap(json["strings"] as? [String: Any])
        let entry = try XCTUnwrap(strings[key] as? [String: Any])
        let localizations = try XCTUnwrap(entry["localizations"] as? [String: Any])
        let localized = try XCTUnwrap(localizations[locale == "zh-CN" ? "zh-Hans" : locale] as? [String: Any])
        let unit = try XCTUnwrap(localized["stringUnit"] as? [String: Any])
        return try XCTUnwrap(unit["value"] as? String)
    }

    private func capture(_ app: XCUIApplication, name: String) throws {
        let shot = app.screenshot()
        let attachment = XCTAttachment(screenshot: shot)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
    }

    func testComposerMenusInEnglish() throws {
        // Chinese menu gestures and keyboard geometry are exercised by
        // ComposerHitTests.testShortDraftImageTouchesMenusAndSendHitArea.
        for locale in ["en"] {
            let app = XCUIApplication()
            app.launchArguments = ["-demo", "-demoRoute", "session/sample-queue", "-fileUploads", "-messageLocale", locale, "-debug.sessionText", "Menu fixture".debugDescription]
            app.launch()
            let field = app.textFields[try text("composer_message", locale: locale)]
            XCTAssertTrue(field.present(timeout: 15))
            XCTAssertEqual(notify_post("io.github.lewin671.remoteagent.debug.session.draft"), 0)
            field.tap()
            XCTAssertEqual(field.value as? String, "Menu fixture")
            XCTAssertTrue(app.keyboards.firstMatch.present(timeout: 5))
            app.buttons[try text("new_session_ios_attach", locale: locale)].tap()
            XCTAssertTrue(app.buttons[try text("attachment_photos_videos", locale: locale)].present(timeout: 5))
            try capture(app, name: "final-\(locale)-attach-menu")
            app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.2)).tap()
            let send = app.buttons[try text("composer_send", locale: locale)].firstMatch
            XCTAssertTrue(send.present(timeout: 5))
            send.press(forDuration: 1)
            let afterTurn = try text("composer_after_turn", locale: locale)
            XCTAssertTrue(app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", afterTurn)).firstMatch.present(timeout: 5))
            try capture(app, name: "final-\(locale)-send-menu")
            XCTAssertTrue(app.keyboards.firstMatch.exists)
            app.terminate()
        }
    }
}
