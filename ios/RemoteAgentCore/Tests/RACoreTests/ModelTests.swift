import Foundation
import RAProto
import Testing

@testable import RACore

/// Tests for the remaining shell helpers in `Model.swift` and `Agents.swift`.
@Suite(.timeLimit(.minutes(2))) struct ModelTests {
    private static func epochMs(_ y: Int, _ mo: Int, _ d: Int, _ h: Int, _ mi: Int, zone: TimeZone) -> Int64 {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = zone
        let date = cal.date(from: DateComponents(year: y, month: mo, day: d, hour: h, minute: mi))!
        return Int64(date.timeIntervalSince1970 * 1000)
    }

    @Test func titleInputEnforcesSingleLineAndCodePointLimit() {
        // 60 code points, not UTF-16 units: an emoji counts once and is never cut in half.
        #expect(titleInput(String(repeating: "字", count: 61)) == String(repeating: "字", count: 60))
        #expect(titleInput(String(repeating: "😀", count: 61)) == String(repeating: "😀", count: 60))
        #expect(titleInput(String(repeating: "a", count: 59) + "😀" + "b") == String(repeating: "a", count: 59) + "😀")
        #expect(titleInput("第一行\r\n第二行") == "第一行 第二行")
    }

    @Test func aTitleCountsCodePointsWhereSwiftWouldCountClusters() {
        // A family emoji is one Character and seven code points; a flag is two.
        let family = "👨‍👩‍👧‍👦"
        #expect(family.count == 1 && family.unicodeScalars.count == 7)
        let cut = titleInput(String(repeating: family, count: 9))
        #expect(cut.unicodeScalars.count == 60)
        // Runs of line breaks, mixed, are one space each.
        #expect(titleInput("a\n\r\n\rb\nc") == "a b c")
    }

    @Test func namesAgentsAndTimes() {
        #expect(agentName(.unspecified) == "Codex")
        #expect(agentName(.claude) == "Claude Code")
        let zone = TimeZone(identifier: "Asia/Shanghai")!
        let now = Self.epochMs(2026, 9, 29, 14, 0, zone: zone)
        #expect(whenText(now - 10_000, now: now, zone: zone) == L10n.timeJustNow())
        #expect(whenText(now - 180_000, now: now, zone: zone) == L10n.timeMinutesAgo(count: 3))
        #expect(whenText(now - (4 * 3600 + 55 * 60) * 1000, now: now, zone: zone) == "9:05")
        #expect(whenText(now - 24 * 3600_000, now: now, zone: zone) == L10n.timeYesterday())
        #expect(whenText(now - 48 * 3600_000, now: now, zone: zone) == L10n.timeMonthDay(day: 27, month: 9))
        #expect(plain("修 `bug`") == "修 bug")
    }

    // --- not in Android's tests: what the JVM does to text, kept the same here ---

    @Test func previewsDropMarksTheWayJavasRegexDoes() {
        #expect(plain("## 标题\n- 一\n> 引用\n**粗** [链接](http://x) ```go\ncode```") == "标题 一 引用 粗 链接 code")
        // \s is ASCII only: an ideographic space inside is kept, the ones at the ends go with trim().
        #expect(plain("\u{3000}a\u{3000}\u{3000}b \t c\u{3000}") == "a\u{3000}\u{3000}b c")
        // \d is ASCII only: a full-width digit is no image number.
        #expect(plain("[Image #１] x") == "[Image #１] x")
        // A line mark only counts at the start of a line, and \r\n is one line break.
        #expect(plain("a - b\r\n- c") == "a - b c")
    }
}
