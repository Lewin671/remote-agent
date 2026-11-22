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

    @Test func imageCategoriesAndExplicitLocaleAreIndependentOfDisplayLanguage() {
        let cases: [(String, ImageProblem.Kind, Bool)] = [
            ("原图超过20 MB", .tooLarge, false), ("dimensions", .dimensions, false),
            ("source missing", .sourceMissing, false), ("decode", .unreadable, false),
            ("storage full", .storage, true), ("offline", .offline, true), ("opaque", .failed, false)
        ]
        let problems = cases.map { rawImageProblem($0.0) }
        for locale in ["zh-CN", "en"] {
            MessageLocale.$overrideForTesting.withValue(locale) {
                #expect(imageProblem(ImageTooLarge(L10nMessage.imageIosImageExceedsMb(count: 80))).kind == .dimensions)
                #expect(imageProblem(ImageTooLarge(L10nMessage.imageIosOriginalExceedsMb(count: 20))).kind == .tooLarge)
                #expect(imageProblem(UserError(L10nMessage.imageThisImageHasNoLocalCopyRemoveItAnd())).kind == .sourceMissing)
                for (problem, item) in zip(problems, cases) {
                    #expect(problem.kind == item.1 && problem.retryable == item.2)
                    #expect(imageFailureLine([item.0].map(rawImageProblem), locale: locale) == problem.message)
                }
                #expect(imageFailureLine(["opaque"].map(rawImageProblem), locale: "en") == L10n.imageThisImageCouldNotBeProcessed(locale: "en"))
            }
        }
    }
}
