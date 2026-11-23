import Foundation
import RAProto
import Testing
@testable import RACore

@Suite(.timeLimit(.minutes(2))) struct SettingsLocaleTests {
    @Test func localErrorsRetainMessagesUntilDisplayAndRawDetailsStayRaw() {
        let raw = "CLI 原因 % & \"quoted\""
        let failure = UserError(L10nMessage.settingsSaveError(reason: raw))
        let command = CommandError("test", L10nMessage.securityInvalidPairingKey())
        MessageLocale.$overrideForTesting.withValue("zh-CN") {
            #expect(failure.message == L10n.settingsSaveError(reason: raw))
        }
        MessageLocale.$overrideForTesting.withValue("en") {
            #expect(failure.message == L10n.settingsSaveError(reason: raw))
            #expect(failure.message.contains(raw))
            #expect(command.message == L10n.securityInvalidPairingKey())
            #expect(command.code == "test" && !command.fromDaemon && !command.unknown)
            #expect(UserError(raw).message == raw)
        }
    }
}
