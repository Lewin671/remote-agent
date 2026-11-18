import Foundation
import Testing
import RAProto
@testable import RACore

@Suite("CoreText", .timeLimit(.minutes(2)))
struct CoreTextTests {
    @Test func literalAndEmpty() {
        #expect(renderCoreText(nil) == "")
        var text = PBCoreText()
        #expect(renderCoreText(text) == "")
        text.literal = "raw literal"
        #expect(renderCoreText(text) == "raw literal")
    }

    @Test func keyArgsAndNestedText() {
        var countArg = PBCoreTextArg()
        countArg.name = "count"
        countArg.intValue = 3

        var waiting = PBCoreText()
        waiting.key = "home.waiting_count"
        waiting.args = [countArg]

        #expect(renderCoreText(waiting, locale: "en") == L10n.homeWaitingCount(count: 3, locale: "en"))
        #expect(renderCoreText(waiting, locale: "zh-CN") == L10n.homeWaitingCount(count: 3, locale: "zh-CN"))

        var weekdayArg = PBCoreTextArg()
        weekdayArg.name = "day"
        weekdayArg.strValue = "mon"
        var weekday = PBCoreText()
        weekday.key = "time.weekday"
        weekday.args = [weekdayArg]

        #expect(renderCoreText(weekday, locale: "en") == L10n.timeWeekday(day: "mon", locale: "en"))

        var nested = PBCoreText()
        nested.key = "common.ios_save_failed"
        var reasonArg = PBCoreTextArg()
        reasonArg.name = "reason"
        reasonArg.textValue = weekday
        nested.args = [reasonArg]
        #expect(renderCoreText(nested, locale: "en") == L10n.commonIosSaveFailed(reason: weekday.rendered(locale: "en"), locale: "en"))
    }

    @Test func joinSkipsEmptySegmentsAndResolvesSeparator() {
        var first = PBCoreText()
        first.literal = "alpha"
        let empty = PBCoreText()
        var last = PBCoreText()
        last.literal = "beta"

        var joinedLiteral = PBCoreText()
        joinedLiteral.join = [first, empty, last]
        joinedLiteral.separatorLiteral = " · "
        #expect(renderCoreText(joinedLiteral) == "alpha · beta")

        var joinedKey = PBCoreText()
        joinedKey.join = [first, empty, last]
        joinedKey.separatorKey = "common.ios_list_separator"
        #expect(renderCoreText(joinedKey, locale: "en") == "alpha\(L10n.commonIosListSeparator(locale: "en"))beta")
    }

    @Test func timestampAndTimestampArgFormatInLocalZoneAndLocale() throws {
        // 2026-11-16 09:30:00 UTC -> 17:30 in Asia/Shanghai (Monday)
        let unixMs: Int64 = 1_794_821_400_000
        let zone = try #require(TimeZone(identifier: "Asia/Shanghai"))

        var timeText = PBCoreText()
        timeText.timestamp = PBCoreTimestampArg.with {
            $0.unixMs = unixMs
            $0.style = .time
        }
        let zhTime = renderCoreText(timeText, locale: "zh-CN", timeZone: zone)
        let enTime = renderCoreText(timeText, locale: "en", timeZone: zone)
        #expect(zhTime.contains("17:30"))
        #expect(!enTime.isEmpty)

        var weekdayText = PBCoreText()
        weekdayText.timestamp = PBCoreTimestampArg.with {
            $0.unixMs = unixMs
            $0.style = .weekday
        }
        #expect(renderCoreText(weekdayText, locale: "en", timeZone: zone) == "Monday")
        #expect(renderCoreText(weekdayText, locale: "zh-CN", timeZone: zone) == "星期一")

        var dayClock = PBCoreText()
        dayClock.key = "time.day_clock"
        dayClock.args = [
            PBCoreTextArg.with {
                $0.name = "day"
                $0.timestampValue = PBCoreTimestampArg.with {
                    $0.unixMs = unixMs
                    $0.style = .weekday
                }
            },
            PBCoreTextArg.with {
                $0.name = "clock"
                $0.timestampValue = PBCoreTimestampArg.with {
                    $0.unixMs = unixMs
                    $0.style = .time
                }
            }
        ]
        #expect(renderCoreText(dayClock, locale: "en", timeZone: zone) == "Monday \(enTime)")
        #expect(renderCoreText(dayClock, locale: "zh-CN", timeZone: zone) == "星期一 \(zhTime)")
    }
}
