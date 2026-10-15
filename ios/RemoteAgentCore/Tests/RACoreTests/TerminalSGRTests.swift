import Foundation
import Testing

@testable import RACore

/// The cases every client reads a line of terminal history by. The web client
/// and Android run the same file, so a change there changes all three or
/// fails one. Runs are compared in the shape the file has them.
@Suite struct TerminalSGRCaseTests {
    let json: [String: Any]
    let cases: [[String: Any]]

    init() throws {
        json = try loadJSON("testdata/terminal/history-sgr.json")
        cases = json["cases"] as! [[String: Any]]
    }

    private func colour(_ c: TerminalColor) -> Any {
        switch c {
        case .palette(let n): n
        case .rgb: c.hex
        }
    }

    private func shape(_ runs: [TerminalRun]) -> [NSDictionary] {
        runs.map { run -> [String: Any] in
            var d: [String: Any] = ["text": run.text]
            if let fg = run.style.fg { d["fg"] = colour(fg) }
            if let bg = run.style.bg { d["bg"] = colour(bg) }
            if run.style.bold { d["bold"] = true }
            if run.style.dim { d["dim"] = true }
            if run.style.italic { d["italic"] = true }
            if run.style.underline { d["underline"] = true }
            if run.style.inverse { d["inverse"] = true }
            return d
        }.map { $0 as NSDictionary }
    }

    /// Runs every case and names all that differ, not only the first.
    @Test func everyCase() {
        #expect(!cases.isEmpty, "no history cases")
        let failed = cases.compactMap { c -> String? in
            let got = shape(terminalHistoryRuns(c["line"] as! String)) as NSArray
            let want = c["runs"] as! NSArray
            return got == want ? nil : "\(c["name"]!)\n  want \(want)\n  got  \(got)"
        }
        #expect(failed.isEmpty, "cases that read differently:\n\(failed.joined(separator: "\n"))")
    }

    /// The file's own account of the contract, so a reader cannot drift from it silently.
    @Test func theFileSaysWhatTheContractIs() {
        #expect(!(json["about"] as! String).isEmpty)
    }

    // --- not among the file's cases ---

    @Test func aPlainLineIsOneRunAndAnEmptyOneIsNone() {
        #expect(terminalHistoryRuns("plain text") == [TerminalRun(text: "plain text")])
        #expect(terminalHistoryRuns("").isEmpty)
    }

    @Test func anEmptyParameterIsNought() {
        // A missing parameter is 0, so the reset after the colour takes it away again.
        #expect(terminalHistoryRuns("\u{1b}[31;ma") == [TerminalRun(text: "a")])
        #expect(terminalHistoryRuns("\u{1b}[31m\u{1b}[ma") == [TerminalRun(text: "a")])
    }

    @Test func anUnknownExtendedColourLeavesTheStyleAlone() {
        #expect(terminalHistoryRuns("\u{1b}[38;9mx") == [TerminalRun(text: "x")])
        #expect(terminalHistoryRuns("\u{1b}[38;5;999mx") == [TerminalRun(text: "x")])
    }
}
