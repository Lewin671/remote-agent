import Foundation
import RAProto
import Testing
@testable import RACore

@Suite struct SettingsLocaleTests {
    @Test func localErrorsRetainMessagesUntilDisplayAndRawDetailsStayRaw() {
        let raw = "CLI 原因 % & \"quoted\""
        let failure = UserError(L10nMessage.settingsSaveError(reason: raw))
        let pair = PairError(L10nMessage.securityInvalidPairingKey())
        let command = CommandError("test", L10nMessage.scheduleSelectAtLeastOneDay())
        MessageLocale.$overrideForTesting.withValue("zh-CN") {
            #expect(failure.message == L10n.settingsSaveError(reason: raw))
            #expect(pair.message == L10n.securityInvalidPairingKey())
        }
        MessageLocale.$overrideForTesting.withValue("en") {
            #expect(failure.message == L10n.settingsSaveError(reason: raw))
            #expect(failure.message.contains(raw))
            #expect(pair.message == L10n.securityInvalidPairingKey())
            #expect(command.message == L10n.scheduleSelectAtLeastOneDay())
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
        let problems = cases.map { imageProblem($0.0) }
        for locale in ["zh-CN", "en"] {
            MessageLocale.$overrideForTesting.withValue(locale) {
                #expect(imageProblem(L10n.imageIosImageExceedsMb(count: 80)).kind == .dimensions)
                #expect(imageProblem(L10n.imageIosOriginalExceedsMb(count: 20)).kind == .tooLarge)
                #expect(imageProblem(L10n.imageThisImageHasNoLocalCopyRemoveItAnd()).kind == .sourceMissing)
                for (problem, item) in zip(problems, cases) {
                    #expect(problem.kind == item.1 && problem.retryable == item.2)
                    #expect(imageFailureLine([item.0], locale: locale) == problem.message)
                }
                #expect(imageFailureLine(["opaque"], locale: "en") == L10n.imageThisImageCouldNotBeProcessed(locale: "en"))
            }
        }
    }

    @Test func scheduleCountsDatesAndCodesKeepTheSameExecutionState() {
        let now = scheduleDate("2026-11-12T08:00:00Z")!
        let zone = TimeZone(secondsFromGMT: 0)!
        var trigger = ScheduleTrigger(type: "interval"); trigger.intervalSeconds = 7200
        var task = ScheduleTask(id: "fixture", spec: ScheduleSpec(title: "用户标题", message: "raw", trigger: trigger, target: ScheduleTarget(type: "new_session")))
        task.nextRunAt = "2026-11-13T08:00:00Z"
        let original = task
        for locale in ["zh-CN", "en"] {
            MessageLocale.$overrideForTesting.withValue(locale) {
                #expect(scheduleFrequency(trigger, zone: zone, now: now) == L10n.scheduleEveryHr(count: 2))
                #expect(task.listPreview(now: now, zone: zone) == L10n.scheduleNoRunsNext(time: scheduleMoment(scheduleDate(task.nextRunAt!)!, now: now, zone: zone)))
                for count in [0, 1, 2, 1000] {
                    #expect(scheduleBarText(Array(repeating: MachineSchedule(machineID: "fixture", task: task), count: count), online: true, now: now, zone: zone).isEmpty == (count == 0))
                }
                #expect(scheduleReason("execution_failed", short: true) == L10n.scheduleExecutionFailed())
                #expect(scheduleReason("raw，任务已停下", short: true).hasSuffix("raw，任务已停下"))
                #expect(task == original && task.group == .active)
                #expect(quotaSummaryRow(QuotaGroup(agent: .codex, title: "raw", status: .ok, rows: [], received: "", detail: "raw", lastRecord: false)) == nil)
            }
        }
    }
}
