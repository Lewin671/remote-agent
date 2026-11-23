import Foundation
import RAProto
import Testing

@testable import RACore

/// Tests for the remaining shell helpers in `Model.swift`, `Projects.swift`, and `JvmText.swift`.
@Suite(.timeLimit(.minutes(2))) struct ModelTests {
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

    @Test func saysWhichSideToUpdateWhenProtocolVersionsDiffer() {
        func m(_ v: UInt32) -> MachineView {
            MachineView(id: "m", online: true, lastSeenMs: 0, meta: .with { $0.hostname = "mac.local"; $0.protocolVersion = v })
        }
        #expect(protocolMismatch(m(peerProtocolVersion)) == nil)
        #expect(protocolMismatch(m(peerProtocolVersion - 1)) == "mac 上的 remote-agent 版本较旧，请在电脑上更新后再试")
        #expect(protocolMismatch(m(peerProtocolVersion + 1)) == L10n.errorsIosThisAppVersionIsOutOfDateUpdateAnd())
        // Not read yet: the daemon answers for itself.
        #expect(protocolMismatch(MachineView(id: "m", online: true, lastSeenMs: 0)) == nil)
        #expect(protocolMismatch(nil) == nil)
    }

    @Test func namesAgentsAndTimes() {
        #expect(agentName(.unspecified) == "Codex")
        #expect(agentName(.claude) == "Claude Code")
        let zone = Mk.shanghai
        let now = Mk.at(2026, 9, 29, 14, 0)
        #expect(whenText(now - 10_000, now: now, zone: zone) == L10n.timeJustNow())
        #expect(whenText(now - 180_000, now: now, zone: zone) == L10n.timeMinutesAgo(count: 3))
        #expect(whenText(now - (4 * 3600 + 55 * 60) * 1000, now: now, zone: zone) == "9:05")
        #expect(whenText(now - 24 * 3600_000, now: now, zone: zone) == L10n.timeYesterday())
        #expect(whenText(now - 48 * 3600_000, now: now, zone: zone) == L10n.timeMonthDay(day: 27, month: 9))
        #expect(projectName("/Users/me/Code/foo/") == "foo")
        #expect(plain("修 `bug`") == "修 bug")
    }

    @Test func aLongModelListShowsThreeAndThePickedOne() {
        let few = ["a", "b", "c", "d", "e"]
        #expect(shownModels(few, picked: nil, all: false) == few)
        let many = few + ["f", "g"]
        #expect(shownModels(many, picked: "b", all: false) == ["a", "b", "c"])
        #expect(shownModels(many, picked: "g", all: false) == ["a", "b", "c", "g"])
        #expect(shownModels(many, picked: "g", all: true) == many)
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

    @Test func aModelNameMatchesIgnoringCaseTheWayKotlinDoes() {
        #expect("Claude-HAIKU-5".jvmContainsIgnoringCase("haiku"))
        #expect(!"claude-sonnet-5-5".jvmContainsIgnoringCase("haiku"))
        // One character at a time: ß is not SS.
        #expect(!"STRASSE".jvmContainsIgnoringCase("straße"))
        #expect("ÉCOLE".jvmContainsIgnoringCase("école"))
        // Code units, not canonical equivalence: a decomposed é is another text.
        #expect(!"e\u{301}cole".jvmContainsIgnoringCase("\u{e9}cole"))
    }

    @Test func simpleCaseMappingsAreTheOnesJavaHas() {
        // İ lowers to i for Java; its full mapping, i with a combining dot, is not the simple one.
        #expect("İstanbul".jvmContainsIgnoringCase("istanbul"))
        #expect("ISTANBUL".jvmContainsIgnoringCase("İstanbul"))
        #expect("ırmak".jvmContainsIgnoringCase("İRMAK"))
        // Compared as Java does, by the lower case of the upper case: ϑ and ϴ both come to θ.
        #expect("\u{3D1}".jvmContainsIgnoringCase("\u{3F4}"))
        // ᾀ has the simple upper case ᾈ besides the full one of two letters.
        #expect("\u{1F80}".jvmContainsIgnoringCase("\u{1F88}"))
        #expect(!"\u{1F80}".jvmContainsIgnoringCase("\u{1F08}\u{399}"))
    }

    @Test func idsAndNamesAreCutByUtf16Units() {
        #expect(machineName(MachineView(id: "0123456789abcdef", online: true, lastSeenMs: 0)) == "01234567")
        #expect(machineName(nil, id: "") == L10n.commonUnknownComputer())
        #expect(machineName(MachineView(id: "x", online: true, lastSeenMs: 0, meta: .with { $0.hostname = "mac.local" })) == "mac")
        // An emoji is two units; a cut through one loses the half Swift cannot hold.
        #expect("ab😀cd".jvmTake(3) == "ab")
        #expect("ab😀cd".jvmTake(4) == "ab😀")
        #expect("ab😀cd".jvmTakeLast(3) == "cd")
        #expect("ab😀cd".jvmLength == 6)
    }

    /// What the app's screens trim, test for blankness and split by, as Kotlin does on Android.
    @Test func trimmingBlanknessAndLinesAreTheJvms() {
        #expect("  \t改一下 \n".jvmTrim() == "改一下")
        #expect("  改一下 \n\n".jvmTrimEnd() == "  改一下")
        // A non-breaking space is whitespace to Kotlin; a zero-width space is not.
        #expect("\u{a0}好\u{a0}".jvmTrim() == "好")
        #expect("\u{200b}好".jvmTrim() == "\u{200b}好")
        #expect("".jvmIsBlank && " \n\t\u{a0}".jvmIsBlank && !" a ".jvmIsBlank && !"\u{200b}".jvmIsBlank)
        #expect("".jvmTrim().isEmpty && "   ".jvmTrimEnd().isEmpty)
        // lines(): \r\n, \n and \r end a line, nothing else does, and there is always a first line.
        #expect("a\r\nb\nc\rd".jvmLines() == ["a", "b", "c", "d"])
        #expect("".jvmLines() == [""] && "a\n".jvmLines() == ["a", ""])
        #expect("a\u{2028}b\u{85}c".jvmLines() == ["a\u{2028}b\u{85}c"])
    }
}
