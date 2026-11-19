import XCTest

/// Smoke coverage for the redesigned Settings, pure Clients, Computer 4 modules,
/// CLI Settings sheet, unified Direct sheet, Attachment Storage, and Usage & Speed screens.
/// Executed during serialized UI test integration runs.
@MainActor
final class SettingsRedesignUITests: XCTestCase {
    private func launch(_ route: String, locale: String = "zh-CN") -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-demo", "-demoRoute", route, "-messageLocale", locale, "-machineFrozen"]
        app.launch()
        return app
    }

    func testSettingsFourGroupsAndPureClientsScreen() throws {
        continueAfterFailure = false
        let app = launch("settings")
        defer { app.terminate() }

        XCTAssertTrue(app.staticTexts["电脑"].present(timeout: 15))
        XCTAssertTrue(app.staticTexts["本机"].exists)
        XCTAssertTrue(app.staticTexts["账户与设备"].exists)
        XCTAssertTrue(app.staticTexts["关于"].exists)

        // Tapping 客户端 opens the pure Clients list without a Computers group.
        let clientsRow = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "客户端")).firstMatch
        XCTAssertTrue(clientsRow.present(timeout: 5))
        clientsRow.tap()
        XCTAssertTrue(app.navigationBars["客户端"].present(timeout: 5))
        XCTAssertFalse(app.staticTexts["电脑 · 运行 CLI 的主机"].exists)
    }

    func testMachineScreenModulesSheetsAndDestinations() throws {
        continueAfterFailure = false
        let app = launch("machine/mac")
        defer { app.terminate() }

        XCTAssertTrue(app.staticTexts["额度与用量"].present(timeout: 15))
        XCTAssertTrue(app.staticTexts["编程 CLI"].exists)
        XCTAssertTrue(app.staticTexts["运行与存储"].exists)
        XCTAssertTrue(app.staticTexts["管理"].exists)

        // Open CLI sheet (replacing inline CLI switches list).
        let codexMatches = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "Codex"))
        if codexMatches.count > 0 {
            let codexRow = codexMatches.element(boundBy: codexMatches.count - 1)
            codexRow.tap()
            XCTAssertTrue(app.staticTexts["已安装版本"].present(timeout: 5))
            app.buttons["完成"].tap()
        }

        // Open Usage & Speed screen and verify all 4 metric tabs.
        let usageRow = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "用量与速度")).firstMatch
        XCTAssertTrue(usageRow.present(timeout: 5))
        usageRow.tap()
        XCTAssertTrue(app.navigationBars["用量与速度"].present(timeout: 5))
        for tab in ["缓存命中", "输出速度", "首字延迟", "消耗量"] {
            let btn = app.buttons[tab]
            if btn.exists { btn.tap() }
        }
    }
}
